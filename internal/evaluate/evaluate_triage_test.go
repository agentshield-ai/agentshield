package evaluate

import (
	"context"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/triage"
)

// MockRuleEvaluator implements RuleEvaluator for testing
type MockRuleEvaluator struct {
	results []engine.RuleResult
}

func (m *MockRuleEvaluator) Evaluate(fields map[string]string) []engine.RuleResult {
	return m.results
}

// MockTriager implements the TriageService interface for testing
type MockTriager struct {
	results []triage.TriageResult
	err     error
	lastCtx context.Context
}

func (m *MockTriager) ShouldTriage(alert engine.RuleResult) bool {
	return alert.Severity == engine.SeverityHigh || alert.Severity == engine.SeverityCritical
}

func (m *MockTriager) TriageAlerts(ctx context.Context, alerts []engine.RuleResult, req *models.EvaluationRequest) ([]triage.TriageResult, error) {
	m.lastCtx = ctx
	if m.err != nil {
		return nil, m.err
	}
	return m.results, nil
}

func (m *MockTriager) HealthCheck(ctx context.Context) error {
	return m.err
}

func TestEvaluatorWithTriageIntegration(t *testing.T) {
	// Create mock rule evaluator with alerts
	mockEngine := &MockRuleEvaluator{
		results: []engine.RuleResult{
			{
				RuleName:    "high-severity-rule",
				Description: "High severity test rule",
				Severity:    engine.SeverityHigh,
				Matched:     true,
			},
			{
				RuleName:    "low-severity-rule",
				Description: "Low severity test rule",
				Severity:    engine.SeverityLow,
				Matched:     true,
			},
		},
	}

	// Create mock triager
	mockTriager := &MockTriager{
		results: []triage.TriageResult{
			{
				Verdict:         "allow",
				Confidence:      0.85,
				Reasoning:       "Analysis suggests false positive",
				SuggestedAction: "Monitor future occurrences",
				Provider:        "mock",
				Model:           "mock-model",
				ProcessingTime:  100,
			},
		},
	}

	// Create evaluator with triage
	evaluator := NewEvaluator(mockEngine, config.ModeEnforce, "http://localhost/feedback", mockTriager, nil)

	req := &models.EvaluationRequest{
		EventID: "test-event-123",
		Tool:    "file_read",
		Args:    map[string]string{"path": "/etc/passwd"},
		Fields:  map[string]string{"tool": "file_read"},
	}

	response, err := evaluator.Evaluate(req)
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}

	// Check basic response
	if response.EventID != req.EventID {
		t.Errorf("Expected EventID %s, got %s", req.EventID, response.EventID)
	}

	if len(response.Alerts) != 2 {
		t.Errorf("Expected 2 alerts, got %d", len(response.Alerts))
	}

	// Check triage results
	if len(response.TriageResults) != 1 {
		t.Errorf("Expected 1 triage result, got %d", len(response.TriageResults))
	}

	triageResult := response.TriageResults[0]
	if triageResult.Verdict != "allow" {
		t.Errorf("Expected triage verdict 'allow', got %s", triageResult.Verdict)
	}

	if triageResult.Confidence != 0.85 {
		t.Errorf("Expected triage confidence 0.85, got %f", triageResult.Confidence)
	}

	// In enforce mode, triage must not downgrade blocking decisions.
	if response.Action != models.ActionBlock {
		t.Errorf("Expected action 'block', got %s", response.Action)
	}
}

type triageCtxKey string

func TestEvaluatorPropagatesContextToTriage(t *testing.T) {
	mockEngine := &MockRuleEvaluator{
		results: []engine.RuleResult{
			{
				RuleName: "high-severity-rule",
				Severity: engine.SeverityHigh,
				Matched:  true,
			},
		},
	}
	mockTriager := &MockTriager{}
	evaluator := NewEvaluator(mockEngine, config.ModeEnforce, "", mockTriager, nil)

	ctx := context.WithValue(context.Background(), triageCtxKey("request_id"), "req-123")
	_, err := evaluator.EvaluateWithContext(ctx, &models.EvaluationRequest{
		EventID: "event-context-propagation",
		Fields:  map[string]string{"tool": "exec"},
	})
	if err != nil {
		t.Fatalf("EvaluateWithContext failed: %v", err)
	}

	if mockTriager.lastCtx == nil {
		t.Fatal("expected triager to receive a context")
	}
	if got, _ := mockTriager.lastCtx.Value(triageCtxKey("request_id")).(string); got != "req-123" {
		t.Fatalf("expected triager context to include request_id req-123, got %q", got)
	}
}

func TestEvaluatorWithoutTriage(t *testing.T) {
	// Create mock rule evaluator with high severity alert
	mockEngine := &MockRuleEvaluator{
		results: []engine.RuleResult{
			{
				RuleName:    "critical-rule",
				Description: "Critical test rule",
				Severity:    engine.SeverityCritical,
				Matched:     true,
			},
		},
	}

	// Create evaluator without triage
	evaluator := NewEvaluator(mockEngine, config.ModeEnforce, "http://localhost/feedback", nil, nil)

	req := &models.EvaluationRequest{
		EventID: "test-event-456",
		Tool:    "network_scan",
		Fields:  map[string]string{"tool": "network_scan"},
	}

	response, err := evaluator.Evaluate(req)
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}

	// Should have no triage results
	if len(response.TriageResults) != 0 {
		t.Errorf("Expected 0 triage results without triager, got %d", len(response.TriageResults))
	}

	// Should block based on rule severity alone
	if response.Action != models.ActionBlock {
		t.Errorf("Expected action 'block', got %s", response.Action)
	}

	if !response.Overridable {
		t.Error("Expected response to be overridable in enforce mode")
	}
}

func TestEvaluatorTriageFailure(t *testing.T) {
	// Create mock rule evaluator
	mockEngine := &MockRuleEvaluator{
		results: []engine.RuleResult{
			{
				RuleName:    "high-severity-rule",
				Description: "High severity test rule",
				Severity:    engine.SeverityHigh,
				Matched:     true,
			},
		},
	}

	// Create mock triager that fails
	mockTriager := &MockTriager{
		err: testError{"triage service unavailable"},
	}

	evaluator := NewEvaluator(mockEngine, config.ModeEnforce, "", mockTriager, nil)

	req := &models.EvaluationRequest{
		EventID: "test-event-789",
		Fields:  map[string]string{"tool": "suspicious_tool"},
	}

	response, err := evaluator.Evaluate(req)
	if err != nil {
		t.Fatalf("Evaluate should not fail when triage fails: %v", err)
	}

	// Should fall back to rule-only decision
	if response.Action != models.ActionBlock {
		t.Errorf("Expected fallback action 'block', got %s", response.Action)
	}

	// Should have no triage results due to failure
	if len(response.TriageResults) != 0 {
		t.Errorf("Expected 0 triage results on failure, got %d", len(response.TriageResults))
	}
}

// TestDetermineActionAcrossModes validates that determineAction correctly maps
// severity counts to actions across all evaluation modes.
// (Previously tested via incorporateTriageResults, which was removed because
// triage results never altered enforcement decisions — a security invariant.)
func TestDetermineActionAcrossModes(t *testing.T) {
	evaluator := &Evaluator{
		defaultMode: config.ModeEnforce,
	}

	tests := []struct {
		name                string
		mode                config.EvaluationMode
		criticalCount       int
		highCount           int
		mediumCount         int
		expectedAction      models.Action
		expectedOverridable bool
	}{
		{
			name:                "Enforce mode - high severity blocks",
			mode:                config.ModeEnforce,
			highCount:           1,
			expectedAction:      models.ActionBlock,
			expectedOverridable: true,
		},
		{
			name:                "Enforce mode - critical severity blocks",
			mode:                config.ModeEnforce,
			criticalCount:       1,
			expectedAction:      models.ActionBlock,
			expectedOverridable: true,
		},
		{
			name:                "Audit mode - always logs regardless of severity",
			mode:                config.ModeAudit,
			criticalCount:       1,
			highCount:           1,
			expectedAction:      models.ActionLog,
			expectedOverridable: false,
		},
		{
			name:                "Shadow mode - always allows regardless of severity",
			mode:                config.ModeShadow,
			criticalCount:       1,
			highCount:           1,
			expectedAction:      models.ActionAllow,
			expectedOverridable: false,
		},
		{
			name:                "Enforce mode - medium severity requires approval",
			mode:                config.ModeEnforce,
			mediumCount:         1,
			expectedAction:      models.ActionRequireApproval,
			expectedOverridable: true,
		},
		{
			name:                "Audit mode - medium severity logs",
			mode:                config.ModeAudit,
			mediumCount:         1,
			expectedAction:      models.ActionLog,
			expectedOverridable: false,
		},
		{
			name:                "Shadow mode - medium severity allows",
			mode:                config.ModeShadow,
			mediumCount:         1,
			expectedAction:      models.ActionAllow,
			expectedOverridable: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			action, overridable := evaluator.determineAction(
				test.mode,
				test.criticalCount,
				test.highCount,
				test.mediumCount,
			)

			if action != test.expectedAction {
				t.Errorf("Expected action %s, got %s", test.expectedAction, action)
			}

			if overridable != test.expectedOverridable {
				t.Errorf("Expected overridable %t, got %t", test.expectedOverridable, overridable)
			}
		})
	}
}

func TestEvaluatorDifferentModes(t *testing.T) {
	// Test that triage integration works correctly in different evaluation modes
	mockEngine := &MockRuleEvaluator{
		results: []engine.RuleResult{
			{
				RuleName: "test-rule",
				Severity: engine.SeverityHigh,
				Matched:  true,
			},
		},
	}

	mockTriager := &MockTriager{
		results: []triage.TriageResult{
			{
				Verdict:    "allow",
				Confidence: 0.9,
			},
		},
	}

	modes := []config.EvaluationMode{
		config.ModeEnforce,
		config.ModeAudit,
		config.ModeShadow,
	}

	expectedActions := map[config.EvaluationMode]models.Action{
		config.ModeEnforce: models.ActionBlock, // Enforce blocks on high/critical regardless of triage allow verdict
		config.ModeAudit:   models.ActionLog,   // Audit always logs
		config.ModeShadow:  models.ActionAllow, // Shadow always allows
	}

	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			evaluator := NewEvaluator(mockEngine, mode, "", mockTriager, nil)

			req := &models.EvaluationRequest{
				EventID: "test-event",
				Fields:  map[string]string{"tool": "test"},
			}

			response, err := evaluator.Evaluate(req)
			if err != nil {
				t.Fatalf("Evaluate failed: %v", err)
			}

			expectedAction := expectedActions[mode]
			if response.Action != expectedAction {
				t.Errorf("Mode %s: expected action %s, got %s", mode, expectedAction, response.Action)
			}

			if response.EffectiveMode != mode {
				t.Errorf("Expected effective mode %s, got %s", mode, response.EffectiveMode)
			}
		})
	}
}

// testError implements the error interface for testing
type testError struct {
	msg string
}

func (e testError) Error() string {
	return e.msg
}
