package evaluate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/session"
	"github.com/agentshield-ai/agentshield/internal/telemetry"
)

// weftBodyLimit is the largest request body Weft accepts (64 MiB), OTLP
// exports included.
const weftBodyLimit = 64 << 20

// Serialization allowances for the bounds below (OTLP protobuf): a key, the
// framing of one attribute or slice element, a span's fixed fields (ids,
// name, times, status, kind) and an event's (name, time).
const (
	maxKeyBytes   = 64
	fieldOverhead = 16
	fixedBytes    = 1024
	eventBytes    = 64
)

// attrBytes bounds one attribute whose value is at most v bytes.
func attrBytes(v int) int { return maxKeyBytes + v + fieldOverhead }

// worstCaseEvaluateSpanBytes bounds the serialized evaluate span: at most
// telemetry.MaxSpanAttributes attributes of at most
// telemetry.MaxAttributeValueLength bytes, the rule id list
// (models.MaxRules ids), two events per rule a verdict carries (the
// agentshield.rule.matched event with id, title, level and tags; the legacy
// rule.matched event with id, title and level) and the few other events
// (cache.hit) at their attribute limits. The engine adds no links.
func worstCaseEvaluateSpanBytes() int {
	attrs := telemetry.MaxSpanAttributes*attrBytes(telemetry.MaxAttributeValueLength) +
		models.MaxRules*(models.MaxRuleIDLength+fieldOverhead)
	ruleEvent := eventBytes + attrBytes(models.MaxRuleIDLength) + attrBytes(models.MaxRuleTitleLength) + attrBytes(16) +
		attrBytes(models.MaxRuleTagBytes+models.MaxRuleTags*fieldOverhead)
	legacyEvent := eventBytes + attrBytes(models.MaxRuleIDLength) + attrBytes(models.MaxRuleTitleLength) + attrBytes(16)
	otherEvents := (telemetry.MaxSpanEvents - 2*models.MaxRules) *
		(eventBytes + telemetry.MaxEventAttributes*attrBytes(telemetry.MaxAttributeValueLength))
	return fixedBytes + attrs + models.MaxRules*(ruleEvent+legacyEvent) + otherEvents
}

// worstCaseAnySpanBytes bounds any span the engine's span limits allow,
// whatever instrumentation made it: every attribute and every event filled
// to its limits (string slices aside, which only the evaluate span uses).
func worstCaseAnySpanBytes() int {
	return fixedBytes + telemetry.MaxSpanAttributes*attrBytes(telemetry.MaxAttributeValueLength) +
		telemetry.MaxSpanEvents*(eventBytes+telemetry.MaxEventAttributes*attrBytes(telemetry.MaxAttributeValueLength)) +
		telemetry.MaxSpanLinks*(eventBytes+telemetry.MaxLinkAttributes*attrBytes(telemetry.MaxAttributeValueLength))
}

// An export request must never be too large for Weft, or every verdict in
// it is lost: one evaluate span is bounded well under 1 MiB, and a batch of
// them far below 64 MiB (even spans filled to every limit fit).
func TestEvaluateSpan_WorstCaseSizeTimesBatchUnderWeftLimit(t *testing.T) {
	span := worstCaseEvaluateSpanBytes()
	if span >= 640<<10 {
		t.Errorf("worst-case evaluate span is %d bytes; it must stay well under 1 MiB", span)
	}
	if batch := span * telemetry.MaxExportBatchSize; batch > weftBodyLimit/4 {
		t.Errorf("worst-case export of evaluate spans is %d bytes, not far below Weft's %d", batch, weftBodyLimit)
	}
	if batch := worstCaseAnySpanBytes() * telemetry.MaxExportBatchSize; batch >= weftBodyLimit {
		t.Errorf("an export of spans filled to every limit is %d bytes, over Weft's %d", batch, weftBodyLimit)
	}
	// Every rule a verdict carries gets both its events.
	if telemetry.MaxSpanEvents < 2*models.MaxRules+1 {
		t.Errorf("span event limit %d leaves no room for %d rules", telemetry.MaxSpanEvents, models.MaxRules)
	}
	l := telemetry.SpanLimits()
	if l.EventCountLimit != telemetry.MaxSpanEvents || l.AttributeCountLimit != telemetry.MaxSpanAttributes ||
		l.AttributePerEventCountLimit != telemetry.MaxEventAttributes || l.LinkCountLimit != telemetry.MaxSpanLinks {
		t.Errorf("span limits = %+v", l)
	}
}

// The bound holds for real: verdicts with hundreds of rule-heavy matches
// (long ids, titles and many long tags), the longest ids the engine accepts
// and long session fields, exported through the engine's own exporter to a
// collector, all arrive, each span within the bound and with one rule event
// per rule the verdict carries.
func TestEvaluateSpan_RuleHeavyVerdictsExportWithinBound(t *testing.T) {
	var mu sync.Mutex
	var spans, largestBody, largestSpan, ruleEvents int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req coltracepb.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		largestBody = max(largestBody, len(body))
		for _, rs := range req.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, sp := range ss.Spans {
					spans++
					largestSpan = max(largestSpan, proto.Size(sp))
					n := 0
					for _, e := range sp.Events {
						if e.Name == EventRuleMatched {
							n++
						}
					}
					if ruleEvents == 0 || n < ruleEvents {
						ruleEvents = n
					}
				}
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &config.TelemetryConfig{Enabled: true, Endpoint: srv.URL + "/v1/traces", ServiceName: "agentshield", SampleRate: 1, Insecure: true, MaxRetrySec: 5}
	tp, shutdown, err := telemetry.Init(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	var alerts []engine.RuleResult
	for i := 0; i < 300; i++ {
		tags := make([]string, 0, 80)
		for k := 0; k < 80; k++ {
			tags = append(tags, fmt.Sprintf("tag%02d.", k)+strings.Repeat("\"", 300))
		}
		alerts = append(alerts, engine.RuleResult{RuleID: fmt.Sprintf("r%03d-", i) + strings.Repeat("\"", 400),
			RuleName: strings.Repeat("T", 2000), Severity: engine.SeverityCritical, Tags: tags, Matched: true})
	}
	ev := NewEvaluator(&mockEngine{mockResults: alerts}, config.ModeEnforce, "", nil, nil)
	ev.SetTracer(tp.Tracer("agentshield"))
	reg := session.NewRegistry(50, 0)
	ev.SetSessionRegistry(reg)
	pad := func(p string) string { return p + strings.Repeat("x", models.MaxIDLength-len(p)) }
	const n = 3*telemetry.MaxExportBatchSize + 1
	for i := 0; i < n; i++ {
		req := &models.EvaluationRequest{EventID: pad(fmt.Sprintf("e%d-", i)), SessionID: pad(fmt.Sprintf("s%d-", i%2)),
			ToolCallID: pad("tc-"), AgentName: pad("agent-"), Tool: strings.Repeat("B", 200), Source: strings.Repeat("<", 9999)}
		if _, err := ev.EvaluateWithContext(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	bound := worstCaseEvaluateSpanBytes()
	if spans != n || ruleEvents != models.MaxRules {
		t.Fatalf("collector got %d of %d spans, at least %d rule events each (want %d)", spans, n, ruleEvents, models.MaxRules)
	}
	if largestSpan > bound || largestBody > bound*telemetry.MaxExportBatchSize {
		t.Fatalf("largest span %d bytes (bound %d), largest export %d bytes", largestSpan, bound, largestBody)
	}
	t.Logf("largest span %d bytes, largest export %d bytes, bound %d per span", largestSpan, largestBody, bound)
}
