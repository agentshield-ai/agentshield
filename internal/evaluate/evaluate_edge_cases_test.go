package evaluate

import (
	"fmt"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/triage"
)

// MockDeepTriager for testing deep triage functionality
type MockDeepTriager struct {
	investigateCalled bool
	shouldTriage      bool
}

func (m *MockDeepTriager) ShouldDeepTriage(alert engine.RuleResult) bool {
	return m.shouldTriage
}

func (m *MockDeepTriager) InvestigateAsync(alerts []engine.RuleResult, req *models.EvaluationRequest, fastResults []triage.TriageResult) {
	m.investigateCalled = true
}

// Test fireDeepTriage function coverage
func TestFireDeepTriage(t *testing.T) {
	t.Run("nil deep triager", func(t *testing.T) {
		evaluator := &Evaluator{
			deepTriager: nil,
		}

		alerts := []engine.RuleResult{{RuleID: "test", Matched: true}}
		req := &models.EvaluationRequest{EventID: "test"}
		var triageResults []triage.TriageResult

		// Should not panic with nil deepTriager
		evaluator.fireDeepTriage(alerts, req, triageResults)
	})

	t.Run("with deep triager", func(t *testing.T) {
		mockDeepTriager := &MockDeepTriager{shouldTriage: true}
		evaluator := &Evaluator{
			deepTriager: mockDeepTriager,
		}

		alerts := []engine.RuleResult{{RuleID: "test", Matched: true}}
		req := &models.EvaluationRequest{EventID: "test"}
		var triageResults []triage.TriageResult

		evaluator.fireDeepTriage(alerts, req, triageResults)

		if !mockDeepTriager.investigateCalled {
			t.Error("Expected InvestigateAsync to be called")
		}
	})

	t.Run("empty alerts list", func(t *testing.T) {
		mockDeepTriager := &MockDeepTriager{shouldTriage: true}
		evaluator := &Evaluator{
			deepTriager: mockDeepTriager,
		}

		var alerts []engine.RuleResult
		req := &models.EvaluationRequest{EventID: "test"}
		var triageResults []triage.TriageResult

		evaluator.fireDeepTriage(alerts, req, triageResults)

		if !mockDeepTriager.investigateCalled {
			t.Error("Expected InvestigateAsync to be called even with empty alerts")
		}
	})
}

// Test edge cases for determineEffectiveMode
func TestDetermineEffectiveModeEdgeCases(t *testing.T) {
	t.Run("invalid mode with different default modes", func(t *testing.T) {
		modes := []config.EvaluationMode{
			config.ModeEnforce,
			config.ModeAudit,
			config.ModeShadow,
		}

		for _, defaultMode := range modes {
			evaluator := &Evaluator{defaultMode: defaultMode}

			// Test various invalid modes
			invalidModes := []string{"invalid", "ENFORCE", "Audit", "123", "enforce_mode", ""}

			for _, invalidMode := range invalidModes {
				result := evaluator.determineEffectiveMode(invalidMode)
				if result != defaultMode {
					t.Errorf("With default %s and invalid mode '%s', expected %s, got %s",
						defaultMode, invalidMode, defaultMode, result)
				}
			}
		}
	})

	t.Run("unknown default mode fallback", func(t *testing.T) {
		evaluator := &Evaluator{defaultMode: config.EvaluationMode("unknown")}

		result := evaluator.determineEffectiveMode("")
		if result != config.EvaluationMode("unknown") {
			t.Errorf("Expected unknown mode to be returned as-is, got %s", result)
		}

		// Even with valid requested mode, should return default for unknown default
		result = evaluator.determineEffectiveMode("audit")
		if result != config.EvaluationMode("unknown") {
			t.Errorf("Expected unknown default mode to be used, got %s", result)
		}
	})

	t.Run("same mode request", func(t *testing.T) {
		// Test requesting the same mode as default
		evaluator := &Evaluator{defaultMode: config.ModeEnforce}

		result := evaluator.determineEffectiveMode("enforce")
		if result != config.ModeEnforce {
			t.Errorf("Expected enforce mode, got %s", result)
		}
	})
}

// Test edge cases for determineAction
func TestDetermineActionEdgeCases(t *testing.T) {
	evaluator := &Evaluator{}

	t.Run("unknown mode defaults to enforce behavior", func(t *testing.T) {
		unknownMode := config.EvaluationMode("unknown")

		// With alerts - should block like enforce mode
		action, overridable := evaluator.determineAction(unknownMode, 1, 0)
		if action != models.ActionBlock || !overridable {
			t.Errorf("Unknown mode with alerts: expected block/overridable, got %s/%t", action, overridable)
		}

		// Without alerts - should allow like enforce mode
		action, overridable = evaluator.determineAction(unknownMode, 0, 0)
		if action != models.ActionAllow || overridable {
			t.Errorf("Unknown mode without alerts: expected allow/not overridable, got %s/%t", action, overridable)
		}
	})

	t.Run("large counts", func(t *testing.T) {
		// Test with very large counts
		action, overridable := evaluator.determineAction(config.ModeEnforce, 999999, 888888)
		if action != models.ActionBlock || !overridable {
			t.Errorf("Large counts: expected block/overridable, got %s/%t", action, overridable)
		}
	})

	t.Run("zero vs non-zero combinations", func(t *testing.T) {
		testCases := []struct {
			critical, high int
			expectBlock    bool
		}{
			{0, 0, false},
			{1, 0, true},
			{0, 1, true},
			{1, 1, true},
		}

		for _, tc := range testCases {
			action, _ := evaluator.determineAction(config.ModeEnforce, tc.critical, tc.high)
			actualBlock := action == models.ActionBlock
			if actualBlock != tc.expectBlock {
				t.Errorf("Critical=%d, High=%d: expected block=%t, got block=%t",
					tc.critical, tc.high, tc.expectBlock, actualBlock)
			}
		}
	})
}

// Test edge cases for incorporateTriageResults
func TestIncorporateTriageResultsEdgeCases(t *testing.T) {
	evaluator := &Evaluator{}

	t.Run("empty triage results", func(t *testing.T) {
		var emptyResults []triage.TriageResult
		action, overridable := evaluator.incorporateTriageResults(config.ModeEnforce, 1, 0, emptyResults)

		// Should default to rule-only decision (block for critical)
		if action != models.ActionBlock || !overridable {
			t.Errorf("Empty triage results: expected block/overridable, got %s/%t", action, overridable)
		}
	})

	t.Run("unknown verdict types", func(t *testing.T) {
		triageResults := []triage.TriageResult{
			{Verdict: "unknown", Confidence: 0.9},
			{Verdict: "maybe", Confidence: 0.8},
			{Verdict: "", Confidence: 0.7},
		}

		action, overridable := evaluator.incorporateTriageResults(config.ModeEnforce, 1, 0, triageResults)

		// Unknown verdicts shouldn't count as allow or block, so should stick with original decision
		if action != models.ActionBlock || !overridable {
			t.Errorf("Unknown verdicts: expected block/overridable, got %s/%t", action, overridable)
		}
	})

	t.Run("investigate without explicit block downgrades to log", func(t *testing.T) {
		triageResults := []triage.TriageResult{
			{Verdict: "investigate", Confidence: 0.9},
			{Verdict: "allow", Confidence: 0.8},
		}

		action, overridable := evaluator.incorporateTriageResults(config.ModeEnforce, 1, 0, triageResults)

		// investigate-only (no explicit block verdict) should downgrade to log for human follow-up
		if action != models.ActionLog || !overridable {
			t.Errorf("Investigate verdict: expected log/overridable, got %s/%t", action, overridable)
		}
	})

	t.Run("equal allow/block counts", func(t *testing.T) {
		triageResults := []triage.TriageResult{
			{Verdict: "allow", Confidence: 0.9},
			{Verdict: "block", Confidence: 0.8},
		}

		action, overridable := evaluator.incorporateTriageResults(config.ModeEnforce, 1, 0, triageResults)

		// Tie should default to original decision (block)
		if action != models.ActionBlock || !overridable {
			t.Errorf("Equal counts: expected block/overridable, got %s/%t", action, overridable)
		}
	})

	t.Run("low confidence allows don't override", func(t *testing.T) {
		triageResults := []triage.TriageResult{
			{Verdict: "allow", Confidence: 0.5}, // Low confidence
			{Verdict: "allow", Confidence: 0.6}, // Low confidence
		}

		action, overridable := evaluator.incorporateTriageResults(config.ModeEnforce, 1, 0, triageResults)

		// Low confidence allows shouldn't override block decision
		if action != models.ActionBlock || !overridable {
			t.Errorf("Low confidence allows: expected block/overridable, got %s/%t", action, overridable)
		}
	})

	t.Run("boundary confidence threshold", func(t *testing.T) {
		// Test exactly at threshold
		triageResults := []triage.TriageResult{
			{Verdict: "allow", Confidence: 0.7}, // Exactly at threshold
		}

		action, _ := evaluator.incorporateTriageResults(config.ModeEnforce, 1, 0, triageResults)

		// Should NOT override since 0.7 is not > 0.7
		if action != models.ActionBlock {
			t.Errorf("Boundary confidence (0.7): expected block, got %s", action)
		}

		// Test just above threshold
		triageResults[0].Confidence = 0.71
		action, _ = evaluator.incorporateTriageResults(config.ModeEnforce, 1, 0, triageResults)

		// Should override since 0.71 > 0.7
		if action != models.ActionLog {
			t.Errorf("Above threshold confidence (0.71): expected log, got %s", action)
		}
	})

	t.Run("complex scenario - majority high confidence allows", func(t *testing.T) {
		triageResults := []triage.TriageResult{
			{Verdict: "allow", Confidence: 0.9},  // High confidence allow
			{Verdict: "allow", Confidence: 0.8},  // High confidence allow
			{Verdict: "allow", Confidence: 0.75}, // High confidence allow
			{Verdict: "block", Confidence: 0.9},  // High confidence block
			{Verdict: "allow", Confidence: 0.5},  // Low confidence allow (doesn't count)
		}

		action, overridable := evaluator.incorporateTriageResults(config.ModeEnforce, 1, 0, triageResults)

		// 3 high confidence allows > 5/2 = 2.5, so should downgrade to log
		if action != models.ActionLog || !overridable {
			t.Errorf("Complex scenario: expected log/overridable, got %s/%t", action, overridable)
		}
	})
}

// Test comprehensive Evaluate scenarios with adversarial inputs
func TestEvaluateAdversarialInputs(t *testing.T) {
	// Create evaluator with all components
	mockEng := &MockRuleEvaluator{
		results: []engine.RuleResult{
			{RuleID: "test", Severity: engine.SeverityHigh, Matched: true},
		},
	}

	mockTriager := &MockTriager{
		results: []triage.TriageResult{
			{Verdict: "allow", Confidence: 0.8},
		},
	}

	mockDeepTriager := &MockDeepTriager{shouldTriage: true}

	evaluator := NewEvaluator(
		mockEng,
		config.ModeEnforce,
		"http://feedback.example.com",
		mockTriager,
		mockDeepTriager,
	)

	t.Run("extremely long event ID", func(t *testing.T) {
		longID := make([]byte, 1024*1024) // 1MB event ID
		for i := range longID {
			longID[i] = 'A'
		}

		req := &models.EvaluationRequest{
			EventID: string(longID),
			Fields:  map[string]string{"test": "value"},
		}

		response, err := evaluator.Evaluate(req)
		if err != nil {
			t.Fatalf("Should handle long event ID: %v", err)
		}

		if response.EventID != string(longID) {
			t.Error("Event ID should be preserved even if long")
		}
	})

	t.Run("unicode and null bytes in fields", func(t *testing.T) {
		req := &models.EvaluationRequest{
			EventID: "test-unicode",
			Fields: map[string]string{
				"unicode":    "🚀💀\x00\uFEFF",
				"null_bytes": "\x00\x01\x02",
				"mixed":      "normal_text\x00unicode🎭\uFFFE",
				"":           "empty_key",
			},
		}

		response, err := evaluator.Evaluate(req)
		if err != nil {
			t.Fatalf("Should handle unicode and null bytes: %v", err)
		}

		if response.EventID != req.EventID {
			t.Error("Event ID should be preserved")
		}
	})

	t.Run("nil and empty fields", func(t *testing.T) {
		req := &models.EvaluationRequest{
			EventID: "test-nil-fields",
			Fields:  nil, // nil fields map
		}

		response, err := evaluator.Evaluate(req)
		if err != nil {
			t.Fatalf("Should handle nil fields: %v", err)
		}

		if response.EventID != req.EventID {
			t.Error("Event ID should be preserved")
		}
	})

	t.Run("extremely long field values", func(t *testing.T) {
		longValue := make([]byte, 2*1024*1024) // 2MB field value
		for i := range longValue {
			longValue[i] = byte('A' + (i % 26))
		}

		req := &models.EvaluationRequest{
			EventID: "test-long-fields",
			Fields: map[string]string{
				"huge_field": string(longValue),
				"normal":     "normal_value",
			},
		}

		response, err := evaluator.Evaluate(req)
		if err != nil {
			t.Fatalf("Should handle huge field values: %v", err)
		}

		if response.EventID != req.EventID {
			t.Error("Event ID should be preserved")
		}
	})

	t.Run("deep triage called with alerts", func(t *testing.T) {
		req := &models.EvaluationRequest{
			EventID: "test-deep-triage",
			Fields:  map[string]string{"test": "value"},
		}

		response, err := evaluator.Evaluate(req)
		if err != nil {
			t.Fatalf("Evaluate failed: %v", err)
		}

		if !mockDeepTriager.investigateCalled {
			t.Error("Expected deep triage to be called when alerts present")
		}

		if len(response.Alerts) == 0 {
			t.Error("Expected alerts to be present for deep triage test")
		}

		// Reset for next test
		mockDeepTriager.investigateCalled = false
	})

	t.Run("no deep triage when no alerts", func(t *testing.T) {
		// Temporarily override engine to return no results
		noAlertsEngine := &MockRuleEvaluator{results: []engine.RuleResult{}}
		evaluatorNoAlerts := NewEvaluator(
			noAlertsEngine,
			config.ModeEnforce,
			"http://feedback.example.com",
			mockTriager,
			mockDeepTriager,
		)

		req := &models.EvaluationRequest{
			EventID: "test-no-alerts",
			Fields:  map[string]string{"test": "value"},
		}

		response, err := evaluatorNoAlerts.Evaluate(req)
		if err != nil {
			t.Fatalf("Evaluate failed: %v", err)
		}

		if mockDeepTriager.investigateCalled {
			t.Error("Deep triage should not be called when no alerts")
		}

		if len(response.Alerts) != 0 {
			t.Errorf("Expected 0 alerts, got %d", len(response.Alerts))
		}

		if response.Action != models.ActionAllow {
			t.Errorf("Expected allow action with no alerts, got %s", response.Action)
		}
	})
}

// Test concurrent access safety
func TestEvaluateConcurrentAccess(t *testing.T) {
	mockEng := &MockRuleEvaluator{
		results: []engine.RuleResult{
			{RuleID: "test", Severity: engine.SeverityHigh, Matched: true},
		},
	}

	evaluator := NewEvaluator(mockEng, config.ModeEnforce, "", nil, nil)

	// Run multiple evaluations concurrently
	const numGoroutines = 100
	results := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			req := &models.EvaluationRequest{
				EventID: fmt.Sprintf("concurrent-test-%d", id),
				Fields:  map[string]string{"test": "concurrent"},
			}

			_, err := evaluator.Evaluate(req)
			results <- err
		}(i)
	}

	// Check all results
	for i := 0; i < numGoroutines; i++ {
		if err := <-results; err != nil {
			t.Errorf("Concurrent evaluation %d failed: %v", i, err)
		}
	}
}
