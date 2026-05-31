package evaluate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/session"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestIntegration_SessionSequencing_DetectsReconExfil is a full-pipeline
// integration test that uses a real Sigma engine, a real session registry,
// and a real OTel span recorder. It simulates a session where 3 benign
// reconnaissance calls (ls, cat, find) are followed by an exfiltration
// attempt (curl), and verifies:
//
//  1. Recon calls are allowed (no rule matches because the current tool is
//     recon, not exfil).
//  2. The curl call triggers the recon-then-exfil rule because
//     session.recent_tools now contains the recon tools.
//  3. OTel spans are emitted for every evaluation, and the final span
//     carries session context attributes.
func TestIntegration_SessionSequencing_DetectsReconExfil(t *testing.T) {
	// Write a Sigma rule that detects recon→exfil patterns.
	// selection_recon matches when session.recent_tools contains recon tools.
	// selection_exfil matches when the current tool is an exfil tool.
	// Both must match (AND) for the rule to fire.
	tmpDir := t.TempDir()
	ruleYAML := `title: Recon Then Exfil Integration
id: integration-recon-exfil
status: test
logsource:
    product: ai_agent
    category: agent_events
detection:
    selection_recon:
        session.recent_tools|re: '.*(ls|find|cat).*'
    selection_exfil:
        tool|re: '.*(curl|wget).*'
    condition: selection_recon and selection_exfil
level: high
`
	if err := os.WriteFile(filepath.Join(tmpDir, "recon_exfil.yml"), []byte(ruleYAML), 0644); err != nil {
		t.Fatal(err)
	}

	eng, err := engine.NewEngine(tmpDir)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}

	// Set up OTel span recorder for assertion
	spanRecorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	tracer := tp.Tracer("agentshield")

	registry := session.NewRegistry(50, 15*time.Minute)

	evaluator := NewEvaluator(eng, config.ModeEnforce, "", nil, nil)
	evaluator.SetTracer(tracer)
	evaluator.SetSessionRegistry(registry)

	ctx := context.Background()
	sessionID := "integration-sess-1"

	// ── Phase 1: Benign reconnaissance ──────────────────────────────────
	// These should be allowed. On the first call DeriveFields returns nil
	// (no prior events). On subsequent calls, session.recent_tools contains
	// recon tools but the current tool is also recon — not exfil — so the
	// selection_exfil condition does not match.
	for _, tool := range []string{"ls", "cat", "find"} {
		resp, evalErr := evaluator.EvaluateWithContext(ctx, &models.EvaluationRequest{
			EventID:   "evt-" + tool,
			SessionID: sessionID,
			Tool:      tool,
			Fields:    map[string]string{"tool": tool},
		})
		if evalErr != nil {
			t.Fatalf("evaluate %s: %v", tool, evalErr)
		}
		if resp.Action == models.ActionBlock {
			t.Errorf("expected %s to be allowed during recon phase, got %s", tool, resp.Action)
		}
	}

	// ── Phase 2: Exfiltration attempt ───────────────────────────────────
	// By this point the session registry has recorded [ls, cat, find].
	// DeriveFields returns session.recent_tools="ls,cat,find" which matches
	// selection_recon. The current tool "curl" matches selection_exfil.
	// Both conditions are true → rule fires → high severity → block.
	resp, err := evaluator.EvaluateWithContext(ctx, &models.EvaluationRequest{
		EventID:   "evt-curl",
		SessionID: sessionID,
		Tool:      "curl",
		Fields:    map[string]string{"tool": "curl"},
	})
	if err != nil {
		t.Fatalf("evaluate curl: %v", err)
	}
	if resp.Action != models.ActionBlock {
		t.Errorf("expected curl after recon to be blocked, got %s", resp.Action)
	}
	if len(resp.Alerts) == 0 {
		t.Error("expected alerts for recon→exfil pattern")
	}

	// ── Phase 3: Verify OTel spans ──────────────────────────────────────
	_ = tp.ForceFlush(ctx)
	spans := spanRecorder.Ended()
	if len(spans) < 4 {
		t.Errorf("expected at least 4 spans (3 recon + 1 exfil), got %d", len(spans))
	}

	// The last span (curl evaluation) should carry session context fields
	// set by the evaluate code when it calls DeriveFields.
	lastSpan := spans[len(spans)-1]
	foundSessionToolCount := false
	for _, attr := range lastSpan.Attributes() {
		if string(attr.Key) == "session.tool_count" {
			foundSessionToolCount = true
		}
	}
	if !foundSessionToolCount {
		t.Error("expected session.tool_count attribute on final span")
	}

	// The last span should also have a rule.matched event
	ruleMatchEvents := 0
	for _, ev := range lastSpan.Events() {
		if ev.Name == "rule.matched" {
			ruleMatchEvents++
		}
	}
	if ruleMatchEvents == 0 {
		t.Error("expected rule.matched span event on final evaluation")
	}
}
