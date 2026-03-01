package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	sigmalite "github.com/agentshield-ai/agentshield/pkg/sigma"
)

// projectRulesDir returns the path to the project's vendored rules directory
// by walking up from the test file location. Returns "" if not found.
func projectRulesDir() string {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	// Walk up from internal/engine/ to project root
	dir := filepath.Dir(filename)
	for range 5 {
		candidate := filepath.Join(dir, "rules", "rules", "ai_agent")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		dir = filepath.Dir(dir)
	}
	return ""
}

func TestActualIAMRuleBug(t *testing.T) {
	rulesDir := projectRulesDir()
	if rulesDir == "" {
		t.Skip("project rules directory not found — skipping integration test")
	}

	engine, err := NewEngine(rulesDir)
	if err != nil {
		t.Fatalf("creating engine: %v", err)
	}

	t.Logf("Loaded %d rules", len(engine.GetLoadedRules()))

	tests := []struct {
		name            string
		eventType       string
		command         string
		expectedMatches int
		description     string
	}{
		{
			name:            "legitimate az login - should match",
			eventType:       "tool_call",
			command:         "az login --tenant mycompany.com",
			expectedMatches: 1,
			description:     "Should match azure selection",
		},
		{
			name:            "http://x - BUG CASE - should NOT match",
			eventType:       "tool_call",
			command:         "http://x",
			expectedMatches: 0,
			description:     "BUG: contains ://, should NOT match but currently does",
		},
		{
			name:            "https://example.com - BUG CASE - should NOT match",
			eventType:       "tool_call",
			command:         "https://example.com",
			expectedMatches: 0,
			description:     "BUG: contains ://, should NOT match but currently does",
		},
		{
			name:            ":// - BUG CASE - should NOT match",
			eventType:       "tool_call",
			command:         "://",
			expectedMatches: 0,
			description:     "BUG: just ://, should NOT match but currently does",
		},
		{
			name:            "curl https://example.com - BUG CASE - should NOT match",
			eventType:       "tool_call",
			command:         "curl https://example.com",
			expectedMatches: 0,
			description:     "BUG: curl with URL, should NOT match but currently does",
		},
		{
			name:            "echo hello - should NOT match",
			eventType:       "tool_call",
			command:         "echo hello",
			expectedMatches: 0,
			description:     "Simple command, should not match",
		},
		{
			name:            "curl foo - should NOT match",
			eventType:       "tool_call",
			command:         "curl foo",
			expectedMatches: 0,
			description:     "curl without URL, should not match",
		},
		{
			name:            "ls -la - should NOT match",
			eventType:       "tool_call",
			command:         "ls -la",
			expectedMatches: 0,
			description:     "ls command, should not match",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := map[string]string{
				"event_type": tt.eventType,
				"command":    tt.command,
			}

			results := engine.Evaluate(fields)
			actualMatches := 0

			// Count only matches for the IAM escalation rule
			const iamRuleID = "ae1ca6c2-6700-5044-b8f0-f59f559f6609"
			for _, result := range results {
				if result.RuleID == iamRuleID {
					actualMatches++
				}
			}

			if actualMatches != tt.expectedMatches {
				t.Errorf("%s: got %d IAM rule matches, want %d. %s",
					tt.name, actualMatches, tt.expectedMatches, tt.description)
				t.Logf("Input fields: %+v", fields)
				if len(results) > 0 {
					for _, result := range results {
						if result.RuleID == iamRuleID {
							t.Logf("IAM rule matched: %+v", result)
						}
					}
				}
			} else {
				t.Logf("%s: PASS - got %d IAM matches as expected", tt.name, actualMatches)
			}
		})
	}
}

func TestDirectIAMRuleParsing(t *testing.T) {
	rulesDir := projectRulesDir()
	if rulesDir == "" {
		t.Skip("project rules directory not found — skipping integration test")
	}

	rulePath := filepath.Join(rulesDir, "ai_agent_cloud_iam_escalation.yml")
	ruleBytes, err := os.ReadFile(rulePath)
	if err != nil {
		t.Skipf("IAM rule file not found at %s — skipping", rulePath)
	}

	rule, err := sigmalite.ParseRule(ruleBytes)
	if err != nil {
		t.Fatalf("parsing IAM rule: %v", err)
	}

	tests := []struct {
		name     string
		entry    *sigmalite.LogEntry
		expected bool
	}{
		{
			name: "legitimate az login should match",
			entry: &sigmalite.LogEntry{
				Fields: map[string]string{
					"event_type": "tool_call",
					"command":    "az login",
				},
			},
			expected: true,
		},
		{
			name: "https://x should NOT match",
			entry: &sigmalite.LogEntry{
				Fields: map[string]string{
					"event_type": "tool_call",
					"command":    "https://x",
				},
			},
			expected: false,
		},
		{
			name: ":// should NOT match",
			entry: &sigmalite.LogEntry{
				Fields: map[string]string{
					"event_type": "tool_call",
					"command":    "://",
				},
			},
			expected: false,
		},
		{
			name: "curl https://example.com should NOT match",
			entry: &sigmalite.LogEntry{
				Fields: map[string]string{
					"event_type": "tool_call",
					"command":    "curl https://example.com",
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := rule.Detection.Matches(tt.entry, nil)
			if result != tt.expected {
				t.Errorf("%s: got %v, want %v", tt.name, result, tt.expected)
				t.Logf("Entry: %+v", tt.entry.Fields)
			} else {
				t.Logf("%s: PASS - got %v as expected", tt.name, result)
			}
		})
	}
}
