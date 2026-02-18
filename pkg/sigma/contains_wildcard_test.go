package sigmalite

import (
	"testing"
)

// TestContainsModifierWildcardHandling verifies that the 'contains' modifier
// treats * and ? as literal characters, not wildcards.
// This test prevents regression of the bug where "*:*" pattern was incorrectly
// matching URLs like "https://example.com" due to wildcard expansion.
func TestContainsModifierWildcardHandling(t *testing.T) {
	// Test rule with contains modifier that has literal * and ? characters
	ruleYAML := `
title: Test Contains Wildcard Handling
description: Test that contains modifier treats * and ? as literals
detection:
  selection_literal_asterisk:
    event_type: tool_call
    command|contains: "*:*"
  selection_literal_question:
    event_type: tool_call  
    command|contains: "file?.txt"
  selection_multiple_patterns:
    event_type: tool_call
    command|contains:
      - "*:*"
      - "wildcard?"
      - "literal*pattern"
  condition: selection_literal_asterisk or selection_literal_question or selection_multiple_patterns
`

	rule, err := ParseRule([]byte(ruleYAML))
	if err != nil {
		t.Fatalf("parsing rule: %v", err)
	}

	tests := []struct {
		name     string
		entry    *LogEntry
		expected bool
		reason   string
	}{
		// Tests for "*:*" pattern
		{
			name: "exact match *:* should match",
			entry: &LogEntry{
				Fields: map[string]string{
					"event_type": "tool_call",
					"command":    "*:*",
				},
			},
			expected: true,
			reason:   "Exact literal match should work",
		},
		{
			name: "command containing *:* should match",
			entry: &LogEntry{
				Fields: map[string]string{
					"event_type": "tool_call",
					"command":    "some *:* permission",
				},
			},
			expected: true,
			reason:   "Contains should find literal *:* substring",
		},
		{
			name: "https://x should NOT match *:* pattern",
			entry: &LogEntry{
				Fields: map[string]string{
					"event_type": "tool_call",
					"command":    "https://x",
				},
			},
			expected: false,
			reason:   "URL should not match literal *:* pattern",
		},
		{
			name: ":// should NOT match *:* pattern",
			entry: &LogEntry{
				Fields: map[string]string{
					"event_type": "tool_call",
					"command":    "://",
				},
			},
			expected: false,
			reason:   "Protocol separator should not match literal *:* pattern",
		},
		{
			name: "a:b should NOT match *:* pattern",
			entry: &LogEntry{
				Fields: map[string]string{
					"event_type": "tool_call",
					"command":    "a:b",
				},
			},
			expected: false,
			reason:   "Simple colon should not match literal *:* pattern",
		},
		
		// Tests for "file?.txt" pattern
		{
			name: "exact match file?.txt should match",
			entry: &LogEntry{
				Fields: map[string]string{
					"event_type": "tool_call",
					"command":    "file?.txt",
				},
			},
			expected: true,
			reason:   "Exact literal match with ? should work",
		},
		{
			name: "file1.txt should NOT match file?.txt pattern",
			entry: &LogEntry{
				Fields: map[string]string{
					"event_type": "tool_call",
					"command":    "file1.txt",
				},
			},
			expected: false,
			reason:   "? should be treated as literal character, not wildcard",
		},
		{
			name: "filea.txt should NOT match file?.txt pattern",
			entry: &LogEntry{
				Fields: map[string]string{
					"event_type": "tool_call",
					"command":    "filea.txt",
				},
			},
			expected: false,
			reason:   "? should be treated as literal character, not wildcard",
		},
		{
			name: "rm file?.txt should match",
			entry: &LogEntry{
				Fields: map[string]string{
					"event_type": "tool_call",
					"command":    "rm file?.txt",
				},
			},
			expected: true,
			reason:   "Contains should find literal file?.txt substring",
		},

		// Tests for multiple patterns
		{
			name: "wildcard? should match multiple patterns",
			entry: &LogEntry{
				Fields: map[string]string{
					"event_type": "tool_call",
					"command":    "some wildcard? here",
				},
			},
			expected: true,
			reason:   "Should match one of the multiple contains patterns",
		},
		{
			name: "literal*pattern should match multiple patterns",
			entry: &LogEntry{
				Fields: map[string]string{
					"event_type": "tool_call",
					"command":    "found literal*pattern match",
				},
			},
			expected: true,
			reason:   "Should match one of the multiple contains patterns",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := rule.Detection.Matches(tt.entry, nil)
			if result != tt.expected {
				t.Errorf("%s: got %v, want %v. %s", tt.name, result, tt.expected, tt.reason)
				t.Logf("Entry: %+v", tt.entry.Fields)
			}
		})
	}
}

// TestWildcardModifierStillWorks verifies that wildcard behavior still works
// correctly for non-contains modifiers (regression test for the fix).
func TestWildcardModifierStillWorks(t *testing.T) {
	// Test rule WITHOUT contains modifier - should treat * and ? as wildcards
	ruleYAML := `
title: Test Wildcard Behavior Without Contains
description: Test that wildcards work normally without contains modifier
detection:
  selection_wildcard:
    event_type: tool_call
    command: "file*.txt"  # No contains modifier - should be treated as wildcard
  condition: selection_wildcard
`

	rule, err := ParseRule([]byte(ruleYAML))
	if err != nil {
		t.Fatalf("parsing rule: %v", err)
	}

	tests := []struct {
		name     string
		entry    *LogEntry
		expected bool
		reason   string
	}{
		{
			name: "file123.txt should match wildcard pattern",
			entry: &LogEntry{
				Fields: map[string]string{
					"event_type": "tool_call",
					"command":    "file123.txt",
				},
			},
			expected: true,
			reason:   "Wildcard * should match multiple characters",
		},
		{
			name: "file.txt should match wildcard pattern",
			entry: &LogEntry{
				Fields: map[string]string{
					"event_type": "tool_call",
					"command":    "file.txt",
				},
			},
			expected: true,
			reason:   "Wildcard * should match zero characters",
		},
		{
			name: "fileabc.txt should match wildcard pattern",
			entry: &LogEntry{
				Fields: map[string]string{
					"event_type": "tool_call",
					"command":    "fileabc.txt",
				},
			},
			expected: true,
			reason:   "Wildcard * should match letters",
		},
		{
			name: "notfile.txt should NOT match wildcard pattern",
			entry: &LogEntry{
				Fields: map[string]string{
					"event_type": "tool_call",
					"command":    "notfile.txt",
				},
			},
			expected: false,
			reason:   "Should not match pattern that doesn't start with 'file'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := rule.Detection.Matches(tt.entry, nil)
			if result != tt.expected {
				t.Errorf("%s: got %v, want %v. %s", tt.name, result, tt.expected, tt.reason)
				t.Logf("Entry: %+v", tt.entry.Fields)
			}
		})
	}
}