package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/evaluate"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/telemetry"
	"github.com/agentshield-ai/agentshield/internal/weftpush"
)

type fixedEngine []engine.RuleResult

func (f fixedEngine) Evaluate(map[string]string) []engine.RuleResult { return f }

// agreeAlerts is a verdict with more matched rules than a detection keeps,
// in no particular severity order, with the rule values the engine loads
// whatever they hold: a whitespace-only id, padded, over-long and
// control-character ids, a repeated id (low first, then critical), and
// more and longer tags than are kept.
func agreeAlerts() []engine.RuleResult {
	levels := []engine.AlertSeverity{engine.SeverityLow, engine.SeverityMedium, engine.SeverityHigh, engine.SeverityCritical}
	var out []engine.RuleResult
	for i := 0; i < 150; i++ {
		tags := []string{fmt.Sprintf("attack.t%d", i)}
		if i%10 == 0 {
			for k := 0; k < 80; k++ {
				tags = append(tags, fmt.Sprintf("tag%02d.\x01", k)+strings.Repeat("g", 300))
			}
		}
		out = append(out, engine.RuleResult{RuleID: fmt.Sprintf("agree-%03d", i), RuleName: fmt.Sprintf("Rule %d \t line\nnext", i),
			Severity: levels[(i*7)%4], Tags: tags, Matched: true})
	}
	return append(out,
		engine.RuleResult{RuleID: "   ", RuleName: "Whitespace id rule", Severity: engine.SeverityCritical, Tags: []string{"attack.x"}},
		engine.RuleResult{RuleID: " agree-trim ", RuleName: "Trim", Severity: engine.SeverityCritical},
		engine.RuleResult{RuleID: "agree-long-" + strings.Repeat("y", 300), RuleName: "Long", Severity: engine.SeverityCritical},
		engine.RuleResult{RuleID: "agree-ctl\tid", RuleName: "Ctl", Severity: engine.SeverityCritical},
		engine.RuleResult{RuleID: "agree-dup", RuleName: "dup a", Severity: engine.SeverityLow, Tags: []string{"a"}},
		engine.RuleResult{RuleID: "agree-dup", RuleName: "dup b", Severity: engine.SeverityCritical, Tags: []string{"b"}},
	)
}

// The pushed and the OTLP copy of one verdict store identical rules: the
// engine selects and cleans them once (evaluate.WeftRules) for both paths,
// the span's limits keep every rule event, and Weft applies the same
// selection. Rules whose id is only whitespace are stored under the same
// generated id on both paths, and a repeated id keeps its most severe level.
func TestShieldPushAndOTelCopiesStoreIdenticalRules(t *testing.T) {
	e := newShieldEnv(t, "")
	ctx := context.Background()
	alerts := agreeAlerts()

	tcfg := &config.TelemetryConfig{Enabled: true, Endpoint: e.srv.URL + "/otel/v1/traces", Insecure: true, SampleRate: 1,
		ServiceName: "agentshield", MaxRetrySec: 5, Headers: map[string]string{"project_id": "agree/otel", "Authorization": "Bearer " + e.key}}
	tp, shutdown, err := telemetry.Init(ctx, tcfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	ev := evaluate.NewEvaluator(fixedEngine(alerts), config.ModeEnforce, "", nil, nil)
	ev.SetTracer(tp.Tracer("agentshield"))
	req := &models.EvaluationRequest{EventID: "agree-1", SessionID: "s-agree", Tool: "Bash", Source: "claude-code",
		Args: map[string]string{"command": "x"}}
	resp, err := ev.Evaluate(req)
	if err != nil {
		t.Fatal(err)
	}
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := shutdown(sctx); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AGREE_WEFT_KEY", e.key)
	p, err := weftpush.New(config.WeftConfig{Endpoint: e.srv.URL, Project: "agree/push", APIKeyEnv: "AGREE_WEFT_KEY", TimeoutSec: 5})
	if err != nil {
		t.Fatal(err)
	}
	p.Start(ctx)
	p.Verdict(req, resp)
	p.Close(sctx)
	if st := p.Stats(); st.Sent != 1 {
		t.Fatalf("push stats = %+v", st)
	}

	rulesOf := func(project string) []any {
		q := url.Values{"project_id": {project}, "session_id": {"s-agree"}}
		code, out := e.do(http.MethodGet, "/api/v1/detections?"+q.Encode(), nil, "")
		items, _ := out["items"].([]any)
		if code != http.StatusOK || len(items) != 1 {
			t.Fatalf("%s detections: %d %v", project, code, out)
		}
		d := items[0].(map[string]any)
		if d["severity"] != "critical" || d["action"] != "block" {
			t.Errorf("%s detection = %v %v", project, d["severity"], d["action"])
		}
		return d["rules"].([]any)
	}
	pushed, otel := rulesOf("agree/push"), rulesOf("agree/otel")
	if !reflect.DeepEqual(pushed, otel) {
		for i := 0; i < max(len(pushed), len(otel)); i++ {
			var a, b any
			if i < len(pushed) {
				a = pushed[i]
			}
			if i < len(otel) {
				b = otel[i]
			}
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("push and OTLP copies differ (%d vs %d rules); first at %d:\npush %v\notel %v", len(pushed), len(otel), i, a, b)
			}
		}
	}
	if len(pushed) != models.MaxRules {
		t.Fatalf("stored %d rules, want %d", len(pushed), models.MaxRules)
	}
	byID := map[string]map[string]any{}
	last := 5
	for _, x := range pushed {
		r := x.(map[string]any)
		byID[r["id"].(string)] = r
		rank := models.LevelRank(r["level"].(string))
		if rank > last {
			t.Fatalf("rules not most severe first: %v", pushed)
		}
		last = rank
		total := 0
		for _, tag := range r["tags"].([]any) {
			total += len(tag.(string))
		}
		if total > models.MaxRuleTagBytes {
			t.Errorf("rule %s keeps %d bytes of tags", r["id"], total)
		}
	}
	gen := models.CleanRuleID("   ", "Whitespace id rule")
	for _, id := range []string{gen, "agree-trim", models.ShortID("agree-long-"+strings.Repeat("y", 300), 256), models.ShortID("agree-ctl\tid", 256), "agree-dup"} {
		if byID[id] == nil {
			t.Errorf("rule %q missing", id)
		}
	}
	if !strings.HasPrefix(gen, models.GeneratedRuleIDPrefix) {
		t.Errorf("generated id = %q", gen)
	}
	if r := byID["agree-dup"]; r == nil || r["level"] != "critical" || r["title"] != "dup b" {
		t.Errorf("repeated rule id = %v, want its critical entry", r)
	}
}
