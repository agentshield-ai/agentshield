package proxyadapter_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/cache"
	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/evaluate"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/proxyadapter"
	"github.com/agentshield-ai/agentshield/internal/session"
)

// projectRulesDir locates the project's vendored rules directory by walking
// up from the test file. Mirrors internal/engine/iam_bug_test.go's helper.
func projectRulesDir(tb testing.TB) string {
	tb.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		tb.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(filename)
	for range 5 {
		candidate := filepath.Join(dir, "rules", "rules", "ai_agent")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		dir = filepath.Dir(dir)
	}
	tb.Skip("project rules directory not found — skipping integration test")
	return ""
}

// newIntegrationEvaluator builds a real engine + evaluator + session registry
// against the project rule corpus. The session registry is mandatory for the
// layer-spanning rule (a1c4d2f8…); without it, session.tool_count is never
// populated and the rule's selection_active_session selector never matches.
func newIntegrationEvaluator(tb testing.TB) *evaluate.Evaluator {
	tb.Helper()
	rulesDir := projectRulesDir(tb)
	eng, err := engine.NewEngine(rulesDir)
	if err != nil {
		tb.Fatalf("engine.NewEngine: %v", err)
	}
	ev := evaluate.NewEvaluator(eng, config.ModeEnforce, "", nil, nil)
	ev.SetCache(cache.NewVerdictCache(1000, 5*time.Minute))
	ev.SetSessionRegistry(session.NewRegistry(50, 15*time.Minute))
	return ev
}

func toolCallRequest(eventID, sessionID, command string) *models.EvaluationRequest {
	return &models.EvaluationRequest{
		EventID:   eventID,
		EventType: "tool_call",
		SessionID: sessionID,
		Tool:      "Bash",
		Args:      map[string]string{"command": command},
		Fields: map[string]string{
			"event_type": "tool_call",
			"command":    command,
		},
	}
}

func proxyObservation(eventID, sessionID, url string) *models.EvaluationRequest {
	obs := proxyadapter.Observation{
		RequestID: eventID,
		SessionID: sessionID,
		Method:    "GET",
		URL:       url,
		Decision:  "allow",
	}
	req, err := proxyadapter.Normalize(obs, "crabtrap")
	if err != nil {
		panic(err)
	}
	return req
}

// TestLayerSpanning_ReconThenProxyEgress is the architectural-correctness
// regression test for issue #44.
//
//	step 1: agent runs a benign Bash tool call in session S (warms the
//	        session registry's window for S)
//	step 2: agentshield-proxy-adapter ingests a CrabTrap observation of
//	        an egress to a private IP in the same session S
//	expect: step 2 blocks and the layer-spanning rule fires
//
// Compare against TestLayerSpanning_ProxyEgress_FreshSession below: identical
// step 2 in a fresh session must NOT trigger the layer-spanning rule, only
// the per-request SSRF rule.
func TestLayerSpanning_ReconThenProxyEgress(t *testing.T) {
	ev := newIntegrationEvaluator(t)
	ctx := context.Background()
	const sess = "agent-session-warm"

	// Step 1: warm the session.
	resp, err := ev.EvaluateWithContext(ctx, toolCallRequest("evt-warm-1", sess, "ls /etc"))
	if err != nil {
		t.Fatalf("step 1 evaluate: %v", err)
	}
	if resp.Action != models.ActionAllow {
		t.Fatalf("step 1 action = %s, want allow", resp.Action)
	}

	// Step 2: proxy observation in the same session.
	resp, err = ev.EvaluateWithContext(ctx, proxyObservation("evt-warm-2", sess, "http://10.0.0.5:8080/admin"))
	if err != nil {
		t.Fatalf("step 2 evaluate: %v", err)
	}
	if resp.Action != models.ActionBlock {
		t.Fatalf("step 2 action = %s, want block", resp.Action)
	}
	if !ruleFired(resp.Alerts, "a1c4d2f8-5e6b-7a8c-9d0e-proxy-recon01") {
		t.Errorf("step 2: layer-spanning rule did not fire; alerts: %v", ruleIDs(resp.Alerts))
	}
}

func TestLayerSpanning_ProxyEgress_FreshSession(t *testing.T) {
	ev := newIntegrationEvaluator(t)
	ctx := context.Background()

	// Fresh session — layer-spanning rule must not fire.
	resp, err := ev.EvaluateWithContext(ctx, proxyObservation("evt-fresh-1", "agent-session-fresh", "http://10.0.0.5:8080/admin"))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if resp.Action != models.ActionBlock {
		t.Errorf("action = %s, want block (per-request SSRF rule must still fire)", resp.Action)
	}
	if !ruleFired(resp.Alerts, "7c3e2d8a-1b4f-4d9c-9e2f-egress-ssrf01") {
		t.Errorf("per-request SSRF rule did not fire; alerts: %v", ruleIDs(resp.Alerts))
	}
	if ruleFired(resp.Alerts, "a1c4d2f8-5e6b-7a8c-9d0e-proxy-recon01") {
		t.Errorf("layer-spanning rule fired in fresh session — must require prior tool calls")
	}
}

// TestLayerSpanning_ProxyEgress_PublicHost: a proxy observation to a public
// (not private/link-local) host in a warm session must NOT fire either the
// SSRF rule or the layer-spanning rule. Validates the rule's URL-classifier
// guard.
func TestLayerSpanning_ProxyEgress_PublicHost(t *testing.T) {
	ev := newIntegrationEvaluator(t)
	ctx := context.Background()
	const sess = "agent-session-public"

	if _, err := ev.EvaluateWithContext(ctx, toolCallRequest("evt-pub-1", sess, "ls /etc")); err != nil {
		t.Fatalf("warm-up evaluate: %v", err)
	}

	resp, err := ev.EvaluateWithContext(ctx, proxyObservation("evt-pub-2", sess, "https://api.github.com/repos/foo/bar"))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if ruleFired(resp.Alerts, "a1c4d2f8-5e6b-7a8c-9d0e-proxy-recon01") {
		t.Errorf("layer-spanning rule fired on public host — must require url.is_private_ip or url.is_link_local")
	}
}

func ruleFired(alerts []engine.RuleResult, ruleID string) bool {
	for _, a := range alerts {
		if a.RuleID == ruleID {
			return true
		}
	}
	return false
}

func ruleIDs(alerts []engine.RuleResult) []string {
	out := make([]string, 0, len(alerts))
	for _, a := range alerts {
		out = append(out, a.RuleID)
	}
	return out
}
