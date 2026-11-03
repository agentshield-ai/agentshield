package evaluate

import (
	"context"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/cache"
	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/session"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func evalWithExporter(t *testing.T, ev *Evaluator, req *models.EvaluationRequest) (*EvaluationResponse, tracetest.SpanStubs) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	ev.SetTracer(tp.Tracer("agentshield"))
	resp, err := ev.EvaluateWithContext(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	_ = tp.ForceFlush(context.Background())
	return resp, exp.GetSpans()
}

func attrMap(kvs []attribute.KeyValue) map[string]attribute.Value {
	m := make(map[string]attribute.Value, len(kvs))
	for _, kv := range kvs {
		m[string(kv.Key)] = kv.Value
	}
	return m
}

func TestEvaluateSpan_WeftAttributesAndRuleEvents(t *testing.T) {
	eng := &mockEngine{mockResults: []engine.RuleResult{
		{RuleID: "r-crit", RuleName: "Pipe to shell", Severity: engine.SeverityCritical, Matched: true, Tags: []string{"attack.execution", "attack.t1059"}},
		{RuleID: "r-med", RuleName: "Curl", Severity: engine.SeverityMedium, Matched: true},
	}}
	ev := NewEvaluator(eng, config.ModeEnforce, "", nil, nil)
	req := &models.EvaluationRequest{
		EventID: "evt-1", SessionID: "sess-1", Tool: "Bash", Source: "claude-code",
		ToolCallID: "toolu_01", AgentName: "coder",
		Fields: map[string]string{"tool": "Bash"},
	}
	resp, spans := evalWithExporter(t, ev, req)
	if resp.Action != models.ActionBlock {
		t.Fatalf("action = %s", resp.Action)
	}
	if len(spans) != 1 {
		t.Fatalf("spans = %d", len(spans))
	}
	sp := spans[0]
	a := attrMap(sp.Attributes)
	wantStr := map[string]string{
		"gen_ai.conversation.id":       "sess-1",
		"gen_ai.tool.name":             "Bash",
		"gen_ai.tool.call.id":          "toolu_01",
		"gen_ai.agent.name":            "coder",
		"agentshield.verdict.action":   "block",
		"agentshield.verdict.mode":     "enforce",
		"agentshield.verdict.severity": "critical",
		// legacy attributes are kept
		"event.id": "evt-1", "session.id": "sess-1", "tool.name": "Bash", "source": "claude-code",
		"verdict.action": "block", "verdict.mode": "enforce",
	}
	for k, want := range wantStr {
		if got := a[k].AsString(); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	ids := a["agentshield.rule_ids"].AsStringSlice()
	if len(ids) != 2 || ids[0] != "r-crit" || ids[1] != "r-med" {
		t.Errorf("rule_ids = %v", ids)
	}

	var matched []map[string]attribute.Value
	legacy := 0
	for _, e := range sp.Events {
		switch e.Name {
		case "agentshield.rule.matched":
			matched = append(matched, attrMap(e.Attributes))
		case "rule.matched":
			legacy++
		}
	}
	if len(matched) != 2 || legacy != 2 {
		t.Fatalf("agentshield.rule.matched = %d, rule.matched = %d", len(matched), legacy)
	}
	m := matched[0]
	if m["rule.id"].AsString() != "r-crit" || m["rule.title"].AsString() != "Pipe to shell" || m["rule.level"].AsString() != "critical" {
		t.Errorf("event attrs = %v", m)
	}
	if tags := m["rule.tags"].AsStringSlice(); len(tags) != 2 || tags[1] != "attack.t1059" {
		t.Errorf("rule.tags = %v", tags)
	}
	if tags, ok := matched[1]["rule.tags"]; !ok || len(tags.AsStringSlice()) != 0 {
		t.Errorf("untagged rule should carry an empty rule.tags, got %v", tags)
	}
}

func TestEvaluateSpan_OptionalIDsOmittedAndAllowSeverityNone(t *testing.T) {
	ev := NewEvaluator(&mockEngine{}, config.ModeEnforce, "", nil, nil)
	req := &models.EvaluationRequest{EventID: "evt-2", Tool: "Read", Fields: map[string]string{}}
	_, spans := evalWithExporter(t, ev, req)
	a := attrMap(spans[0].Attributes)
	for _, k := range []string{"gen_ai.conversation.id", "gen_ai.tool.call.id", "gen_ai.agent.name"} {
		if _, ok := a[k]; ok {
			t.Errorf("%s should be absent when not supplied", k)
		}
	}
	if a["agentshield.verdict.action"].AsString() != "allow" || a["agentshield.verdict.severity"].AsString() != "none" {
		t.Errorf("verdict attrs = %v / %v", a["agentshield.verdict.action"], a["agentshield.verdict.severity"])
	}
	if ids, ok := a["agentshield.rule_ids"]; !ok || len(ids.AsStringSlice()) != 0 {
		t.Errorf("rule_ids should be an empty list")
	}
}

func TestEvaluateSpan_CachedVerdictStillCarriesRuleEvents(t *testing.T) {
	eng := &mockEngine{mockResults: []engine.RuleResult{{RuleID: "r1", RuleName: "R1", Severity: engine.SeverityHigh, Matched: true}}}
	ev := NewEvaluator(eng, config.ModeEnforce, "", nil, nil)
	ev.SetCache(cache.NewVerdictCache(10, time.Minute))
	mk := func(id string) *models.EvaluationRequest {
		return &models.EvaluationRequest{EventID: id, SessionID: "s", Tool: "Bash", ToolCallID: "tc-" + id,
			Args: map[string]string{"command": "x"}, Fields: map[string]string{"command": "x"}}
	}
	_, _ = evalWithExporter(t, ev, mk("a"))
	resp, spans := evalWithExporter(t, ev, mk("b"))
	if !resp.Cached {
		t.Fatal("second evaluation should be a cache hit")
	}
	a := attrMap(spans[0].Attributes)
	if a["gen_ai.tool.call.id"].AsString() != "tc-b" || a["agentshield.verdict.action"].AsString() != "block" {
		t.Errorf("cached span attrs = %v", a)
	}
	n := 0
	for _, e := range spans[0].Events {
		if e.Name == "agentshield.rule.matched" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("cached verdict rule events = %d", n)
	}
}

func TestEnforceActionAndMaxSeverity(t *testing.T) {
	cases := []struct {
		alerts []engine.RuleResult
		action models.Action
		sev    string
	}{
		{nil, models.ActionAllow, "none"},
		{[]engine.RuleResult{{Severity: engine.SeverityLow}}, models.ActionAllow, "low"},
		{[]engine.RuleResult{{Severity: engine.SeverityLow}, {Severity: engine.SeverityMedium}}, models.ActionRequireApproval, "medium"},
		{[]engine.RuleResult{{Severity: engine.SeverityMedium}, {Severity: engine.SeverityHigh}}, models.ActionBlock, "high"},
		{[]engine.RuleResult{{Severity: engine.SeverityCritical}}, models.ActionBlock, "critical"},
	}
	for _, c := range cases {
		if got := EnforceAction(c.alerts); got != c.action {
			t.Errorf("EnforceAction(%v) = %s, want %s", c.alerts, got, c.action)
		}
		if got := MaxSeverity(c.alerts); got != c.sev {
			t.Errorf("MaxSeverity(%v) = %s, want %s", c.alerts, got, c.sev)
		}
	}
}

func TestEvaluateSpan_ReplayAttribute(t *testing.T) {
	ev := NewEvaluator(&mockEngine{}, config.ModeShadow, "", nil, nil)
	_, spans := evalWithExporter(t, ev, &models.EvaluationRequest{EventID: "weft-replay-j-0", SessionID: "weft-replay:j:s", Tool: "Bash",
		Fields: map[string]string{"tool": "Bash"}, Replay: true})
	if len(spans) != 1 {
		t.Fatalf("spans = %d", len(spans))
	}
	if v, ok := attrMap(spans[0].Attributes)[AttrReplay]; !ok || !v.AsBool() {
		t.Fatalf("%s missing on a replay evaluation: %v", AttrReplay, spans[0].Attributes)
	}
	_, spans = evalWithExporter(t, ev, &models.EvaluationRequest{EventID: "live-1", SessionID: "s", Tool: "Bash", Fields: map[string]string{"tool": "Bash"}})
	if _, ok := attrMap(spans[0].Attributes)[AttrReplay]; ok {
		t.Fatalf("%s set on a live evaluation", AttrReplay)
	}
}

// The replay flag only takes effect inside the replay session namespace: a
// live session id is neither isolated from cross-session correlation nor
// tagged as a replay.
func TestEvaluate_ReplayFlagIgnoredOutsideReplayNamespace(t *testing.T) {
	ev := NewEvaluator(&mockEngine{mockResults: []engine.RuleResult{{RuleID: "r", Matched: true, Severity: engine.SeverityHigh}}}, config.ModeShadow, "", nil, nil)
	reg := session.NewRegistry(50, time.Minute)
	ev.SetSessionRegistry(reg)
	_, spans := evalWithExporter(t, ev, &models.EvaluationRequest{EventID: "e1", SessionID: "live-1", Tool: "Bash",
		Fields: map[string]string{"tool": "Bash"}, Replay: true})
	if _, ok := attrMap(spans[0].Attributes)[AttrReplay]; ok {
		t.Fatalf("%s set on a live session", AttrReplay)
	}
	if f := reg.DeriveAllFields("live-2", time.Minute); f["session.cross_session_count"] != "1" {
		t.Fatalf("live session isolated by a forged replay flag: %v", f)
	}
	_, _ = evalWithExporter(t, ev, &models.EvaluationRequest{EventID: "weft-replay-j-0", SessionID: "weft-replay:j:s", Tool: "Bash",
		Fields: map[string]string{"tool": "Bash"}, Replay: true})
	if f := reg.DeriveAllFields("live-2", time.Minute); f["session.cross_session_count"] != "1" {
		t.Fatalf("namespaced replay counted in live cross-session fields: %v", f)
	}
}
