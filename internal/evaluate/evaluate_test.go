package evaluate

import (
	"testing"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"
)

// mockEngine implements a simple mock for testing
type mockEngine struct {
	mockResults []engine.RuleResult
}

func (m *mockEngine) Evaluate(fields map[string]string) []engine.RuleResult {
	return m.mockResults
}

func TestDetermineEffectiveMode(t *testing.T) {
	tests := []struct {
		name        string
		defaultMode config.EvaluationMode
		expected    config.EvaluationMode
	}{
		{name: "enforce uses server default", defaultMode: config.ModeEnforce, expected: config.ModeEnforce},
		{name: "audit uses server default", defaultMode: config.ModeAudit, expected: config.ModeAudit},
		{name: "shadow uses server default", defaultMode: config.ModeShadow, expected: config.ModeShadow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evaluator := &Evaluator{defaultMode: tt.defaultMode}
			result := evaluator.determineEffectiveMode()
			if result != tt.expected {
				t.Errorf("determineEffectiveMode() = %s, want %s", result, tt.expected)
			}
		})
	}
}

func TestDetermineAction(t *testing.T) {
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
			name:                "enforce mode with critical alert blocks",
			mode:                config.ModeEnforce,
			criticalCount:       1,
			highCount:           0,
			mediumCount:         0,
			expectedAction:      models.ActionBlock,
			expectedOverridable: true,
		},
		{
			name:                "enforce mode with high alert blocks",
			mode:                config.ModeEnforce,
			criticalCount:       0,
			highCount:           1,
			mediumCount:         0,
			expectedAction:      models.ActionBlock,
			expectedOverridable: true,
		},
		{
			name:                "enforce mode with no high/critical allows",
			mode:                config.ModeEnforce,
			criticalCount:       0,
			highCount:           0,
			mediumCount:         0,
			expectedAction:      models.ActionAllow,
			expectedOverridable: false,
		},
		{
			name:                "enforce mode with medium alert requires approval",
			mode:                config.ModeEnforce,
			criticalCount:       0,
			highCount:           0,
			mediumCount:         1,
			expectedAction:      models.ActionRequireApproval,
			expectedOverridable: true,
		},
		{
			name:                "enforce mode critical takes precedence over medium",
			mode:                config.ModeEnforce,
			criticalCount:       1,
			highCount:           0,
			mediumCount:         3,
			expectedAction:      models.ActionBlock,
			expectedOverridable: true,
		},
		{
			name:                "enforce mode high takes precedence over medium",
			mode:                config.ModeEnforce,
			criticalCount:       0,
			highCount:           1,
			mediumCount:         5,
			expectedAction:      models.ActionBlock,
			expectedOverridable: true,
		},
		{
			name:                "audit mode always logs even with medium",
			mode:                config.ModeAudit,
			criticalCount:       5,
			highCount:           3,
			mediumCount:         2,
			expectedAction:      models.ActionLog,
			expectedOverridable: false,
		},
		{
			name:                "audit mode logs on medium only",
			mode:                config.ModeAudit,
			criticalCount:       0,
			highCount:           0,
			mediumCount:         1,
			expectedAction:      models.ActionLog,
			expectedOverridable: false,
		},
		{
			name:                "shadow mode always allows even with medium",
			mode:                config.ModeShadow,
			criticalCount:       5,
			highCount:           3,
			mediumCount:         2,
			expectedAction:      models.ActionAllow,
			expectedOverridable: false,
		},
		{
			name:                "shadow mode allows on medium only",
			mode:                config.ModeShadow,
			criticalCount:       0,
			highCount:           0,
			mediumCount:         1,
			expectedAction:      models.ActionAllow,
			expectedOverridable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evaluator := &Evaluator{}

			action, overridable := evaluator.determineAction(tt.mode, tt.criticalCount, tt.highCount, tt.mediumCount)

			if action != tt.expectedAction {
				t.Errorf("determineAction() action = %s, want %s", action, tt.expectedAction)
			}

			if overridable != tt.expectedOverridable {
				t.Errorf("determineAction() overridable = %t, want %t", overridable, tt.expectedOverridable)
			}
		})
	}
}

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name           string
		mockResults    []engine.RuleResult
		defaultMode    config.EvaluationMode
		expectedAction models.Action
		expectedAlerts int
	}{
		{
			name:           "no matched rules allows",
			mockResults:    []engine.RuleResult{},
			defaultMode:    config.ModeEnforce,
			expectedAction: models.ActionAllow,
			expectedAlerts: 0,
		},
		{
			name: "matched critical rule blocks in enforce mode",
			mockResults: []engine.RuleResult{
				{
					RuleID:   "test-rule-critical",
					RuleName: "Critical Test Rule",
					Severity: engine.SeverityCritical,
					Matched:  true,
				},
			},
			defaultMode:    config.ModeEnforce,
			expectedAction: models.ActionBlock,
			expectedAlerts: 1,
		},
		{
			name: "matched critical rule logs in audit mode",
			mockResults: []engine.RuleResult{
				{
					RuleID:   "test-rule-critical",
					RuleName: "Critical Test Rule",
					Severity: engine.SeverityCritical,
					Matched:  true,
				},
			},
			defaultMode:    config.ModeAudit,
			expectedAction: models.ActionLog,
			expectedAlerts: 1,
		},
		{
			name: "matched low severity allows in enforce mode",
			mockResults: []engine.RuleResult{
				{
					RuleID:   "test-rule-low",
					RuleName: "Low Severity Rule",
					Severity: engine.SeverityLow,
					Matched:  true,
				},
			},
			defaultMode:    config.ModeEnforce,
			expectedAction: models.ActionAllow,
			expectedAlerts: 1,
		},
		{
			name: "matched medium severity requires approval in enforce mode",
			mockResults: []engine.RuleResult{
				{
					RuleID:   "test-rule-medium",
					RuleName: "Medium Severity Rule",
					Severity: engine.SeverityMedium,
					Matched:  true,
				},
			},
			defaultMode:    config.ModeEnforce,
			expectedAction: models.ActionRequireApproval,
			expectedAlerts: 1,
		},
		{
			name: "matched medium severity logs in audit mode",
			mockResults: []engine.RuleResult{
				{
					RuleID:   "test-rule-medium",
					RuleName: "Medium Severity Rule",
					Severity: engine.SeverityMedium,
					Matched:  true,
				},
			},
			defaultMode:    config.ModeAudit,
			expectedAction: models.ActionLog,
			expectedAlerts: 1,
		},
		{
			name: "matched medium severity allows in shadow mode",
			mockResults: []engine.RuleResult{
				{
					RuleID:   "test-rule-medium",
					RuleName: "Medium Severity Rule",
					Severity: engine.SeverityMedium,
					Matched:  true,
				},
			},
			defaultMode:    config.ModeShadow,
			expectedAction: models.ActionAllow,
			expectedAlerts: 1,
		},
		// client mode override removed: server-side mode only
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockEng := &mockEngine{
				mockResults: tt.mockResults,
			}

			evaluator := NewEvaluator(mockEng, tt.defaultMode, "", nil, nil)

			req := &models.EvaluationRequest{
				EventID: "test-event-123",
				Fields: map[string]string{
					"test": "value",
				},
			}

			response, err := evaluator.Evaluate(req)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}

			if response.Action != tt.expectedAction {
				t.Errorf("Evaluate() action = %s, want %s", response.Action, tt.expectedAction)
			}

			if len(response.Alerts) != tt.expectedAlerts {
				t.Errorf("Evaluate() alerts count = %d, want %d", len(response.Alerts), tt.expectedAlerts)
			}

			if response.EventID != req.EventID {
				t.Errorf("Evaluate() event_id = %s, want %s", response.EventID, req.EventID)
			}

			// Check that only matched rules are in alerts
			for _, alert := range response.Alerts {
				if !alert.Matched {
					t.Errorf("Alert should only contain matched rules, got unmatched rule: %s", alert.RuleID)
				}
			}
		})
	}
}

func TestGetModeInfo(t *testing.T) {
	info := GetModeInfo()

	// Check that all expected keys exist
	expectedKeys := []string{"modes", "downgrade_policy", "blocking_severities", "approval_severities"}
	for _, key := range expectedKeys {
		if _, exists := info[key]; !exists {
			t.Errorf("GetModeInfo() missing key: %s", key)
		}
	}

	// Check that modes contains expected modes
	modes, ok := info["modes"].(map[string]string)
	if !ok {
		t.Fatalf("modes should be map[string]string")
	}

	expectedModes := []string{"enforce", "audit", "shadow"}
	for _, mode := range expectedModes {
		if _, exists := modes[mode]; !exists {
			t.Errorf("GetModeInfo() modes missing: %s", mode)
		}
	}
}
