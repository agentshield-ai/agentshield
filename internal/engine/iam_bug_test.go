package engine

import (
	"os"
	"testing"

	sigmalite "github.com/agentshield-ai/agentshield/pkg/sigma"
)

func TestActualIAMRuleBug(t *testing.T) {
	// Test against the actual IAM rule that's reportedly causing the issue
	engine, err := NewEngine("/home/agent/.agentshield/rules")
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
			for _, result := range results {
				if result.RuleID == "agent-cloud-iam-escalation-001" {
					actualMatches++
				}
			}

			if actualMatches != tt.expectedMatches {
				t.Errorf("%s: got %d IAM rule matches, want %d. %s", 
					tt.name, actualMatches, tt.expectedMatches, tt.description)
				t.Logf("Input fields: %+v", fields)
				if len(results) > 0 {
					for _, result := range results {
						if result.RuleID == "agent-cloud-iam-escalation-001" {
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
	// Read and parse the actual IAM rule file
	ruleBytes, err := os.ReadFile("/home/agent/.agentshield/rules/agent_cloud_iam_escalation.yml")
	if err != nil {
		t.Fatalf("reading IAM rule: %v", err)
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