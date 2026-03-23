package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	sigma "github.com/agentshield-ai/agentshield/pkg/sigma"
)

func TestNewEngine(t *testing.T) {
	// Create temporary rules directory
	tmpDir, err := os.MkdirTemp("", "engine_test_rules")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a simple Sigma rule
	testRule := `
title: Test Rule
id: test-rule-1
status: experimental
description: A test rule for unit testing
logsource:
  category: test
detection:
  selection:
    field1: "value1"
  condition: selection
level: medium
`

	rulePath := filepath.Join(tmpDir, "test_rule.yml")
	err = os.WriteFile(rulePath, []byte(testRule), 0644)
	if err != nil {
		t.Fatalf("writing test rule: %v", err)
	}

	// Test creating engine
	engine, err := NewEngine(tmpDir)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	if engine == nil {
		t.Fatal("NewEngine() returned nil")
	}

	// Check that rule was loaded
	rules := engine.GetLoadedRules()
	if len(rules) != 1 {
		t.Errorf("expected 1 rule loaded, got %d", len(rules))
	}

	if len(rules) > 0 {
		rule := rules[0]
		if rule.RuleID != "test-rule-1" {
			t.Errorf("expected rule ID 'test-rule-1', got '%s'", rule.RuleID)
		}
		if rule.RuleName != "Test Rule" {
			t.Errorf("expected rule name 'Test Rule', got '%s'", rule.RuleName)
		}
		if rule.Severity != SeverityMedium {
			t.Errorf("expected severity medium, got %s", rule.Severity)
		}
	}
}

func TestNewEngineNonexistentDir(t *testing.T) {
	_, err := NewEngine("/nonexistent/path")
	if err == nil {
		t.Error("NewEngine() should fail for nonexistent directory")
	}
}

func TestIsPathSafe(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "path_test")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	engine := &Engine{
		rulesDir: tmpDir,
	}

	tests := []struct {
		name     string
		filePath string
		expected bool
	}{
		{
			name:     "file in rules dir is safe",
			filePath: filepath.Join(tmpDir, "rule.yml"),
			expected: true,
		},
		{
			name:     "file in subdirectory is safe",
			filePath: filepath.Join(tmpDir, "subdir", "rule.yml"),
			expected: true,
		},
		{
			name:     "file outside rules dir is unsafe",
			filePath: "/etc/passwd",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := engine.isPathSafe(tt.filePath)
			if result != tt.expected {
				t.Errorf("isPathSafe(%s) = %v, want %v", tt.filePath, result, tt.expected)
			}
		})
	}
}

func TestValidateRuleRegexComplexity(t *testing.T) {
	tests := []struct {
		name         string
		ruleYAML     string
		wantRejected bool // true means the rule should be rejected (not loaded)
	}{
		{
			name: "safe regex rule loads successfully",
			ruleYAML: `
title: Safe Regex Rule
id: safe-regex-1
status: experimental
logsource:
  category: test
detection:
  selection:
    field|re: "^https?://[a-zA-Z0-9.]+$"
  condition: selection
level: medium
`,
			wantRejected: false,
		},
		{
			name: "nested quantifier (.*) star rejected",
			ruleYAML: `
title: Evil Nested Star
id: evil-nested-star
status: experimental
logsource:
  category: test
detection:
  selection:
    field|re: "(.*)*evil"
  condition: selection
level: medium
`,
			wantRejected: true,
		},
		{
			name: "nested quantifier (.+)+ rejected",
			ruleYAML: `
title: Evil Nested Plus
id: evil-nested-plus
status: experimental
logsource:
  category: test
detection:
  selection:
    field|re: "(.+)+attack"
  condition: selection
level: medium
`,
			wantRejected: true,
		},
		{
			name: "very long regex pattern rejected",
			ruleYAML: `
title: Long Regex Rule
id: long-regex-1
status: experimental
logsource:
  category: test
detection:
  selection:
    field|re: "` + strings.Repeat("a", 1001) + `"
  condition: selection
level: medium
`,
			wantRejected: true,
		},
		{
			name: "rule without re modifier is unaffected",
			ruleYAML: `
title: Normal Contains Rule
id: normal-contains-1
status: experimental
logsource:
  category: test
detection:
  selection:
    field|contains: "totally fine (.*)*"
  condition: selection
level: medium
`,
			wantRejected: false,
		},
		{
			name: "valid regex with character class passes",
			ruleYAML: `
title: Valid Regex
id: valid-regex-1
status: experimental
logsource:
  category: test
detection:
  selection:
    field|re: "[0-9]{1,5}\\.[0-9]{1,5}"
  condition: selection
level: medium
`,
			wantRejected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", "regex_complexity_test")
			if err != nil {
				t.Fatalf("creating temp dir: %v", err)
			}
			defer os.RemoveAll(tmpDir)

			rulePath := filepath.Join(tmpDir, "rule.yml")
			if err := os.WriteFile(rulePath, []byte(tt.ruleYAML), 0644); err != nil {
				t.Fatalf("writing rule file: %v", err)
			}

			eng, engineErr := NewEngine(tmpDir)
			if engineErr != nil {
				t.Fatalf("NewEngine() unexpected error: %v", engineErr)
			}

			loaded := eng.GetLoadedRules()
			if tt.wantRejected && len(loaded) != 0 {
				t.Errorf("expected dangerous rule to be rejected, but %d rules loaded", len(loaded))
			}
			if !tt.wantRejected && len(loaded) != 1 {
				t.Errorf("expected rule to be loaded, but %d rules loaded", len(loaded))
			}
		})
	}
}

func TestExtractSearchAtoms(t *testing.T) {
	tests := []struct {
		name     string
		expr     sigma.Expr
		expected int
	}{
		{
			name:     "single SearchAtom",
			expr:     &sigma.SearchAtom{Field: "f1", Patterns: []string{"v1"}},
			expected: 1,
		},
		{
			name: "AndExpr with two atoms",
			expr: &sigma.AndExpr{X: []sigma.Expr{
				&sigma.SearchAtom{Field: "f1", Patterns: []string{"v1"}},
				&sigma.SearchAtom{Field: "f2", Patterns: []string{"v2"}},
			}},
			expected: 2,
		},
		{
			name: "OrExpr with two atoms",
			expr: &sigma.OrExpr{X: []sigma.Expr{
				&sigma.SearchAtom{Field: "f1", Patterns: []string{"v1"}},
				&sigma.SearchAtom{Field: "f2", Patterns: []string{"v2"}},
			}},
			expected: 2,
		},
		{
			name: "NotExpr wrapping atom",
			expr: &sigma.NotExpr{X: &sigma.SearchAtom{Field: "f1", Patterns: []string{"v1"}}},
			expected: 1,
		},
		{
			name: "NamedExpr wrapping atom",
			expr: &sigma.NamedExpr{Name: "selection", X: &sigma.SearchAtom{Field: "f1", Patterns: []string{"v1"}}},
			expected: 1,
		},
		{
			name: "nested structure",
			expr: &sigma.NamedExpr{
				Name: "sel",
				X: &sigma.AndExpr{X: []sigma.Expr{
					&sigma.SearchAtom{Field: "f1", Patterns: []string{"v1"}},
					&sigma.OrExpr{X: []sigma.Expr{
						&sigma.SearchAtom{Field: "f2", Patterns: []string{"v2"}},
						&sigma.NotExpr{X: &sigma.SearchAtom{Field: "f3", Patterns: []string{"v3"}}},
					}},
				}},
			},
			expected: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			atoms := extractSearchAtoms(tt.expr)
			if len(atoms) != tt.expected {
				t.Errorf("extractSearchAtoms() returned %d atoms, want %d", len(atoms), tt.expected)
			}
		})
	}
}

func TestMapSeverity(t *testing.T) {
	engine := &Engine{}

	tests := []struct {
		input    string
		expected AlertSeverity
	}{
		{"low", SeverityLow},
		{"Low", SeverityLow},
		{"LOW", SeverityLow},
		{"informational", SeverityLow},
		{"info", SeverityLow},
		{"medium", SeverityMedium},
		{"Medium", SeverityMedium},
		{"med", SeverityMedium},
		{"high", SeverityHigh},
		{"High", SeverityHigh},
		{"critical", SeverityCritical},
		{"Critical", SeverityCritical},
		{"crit", SeverityCritical},
		{"unknown", SeverityMedium}, // Default fallback
		{"", SeverityMedium},        // Default fallback
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := engine.mapSeverity(sigma.Level(tt.input))
			
			if result != tt.expected {
				t.Errorf("mapSeverity(%s) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestEvaluate(t *testing.T) {
	// Create temporary rules directory
	tmpDir, err := os.MkdirTemp("", "engine_eval_test")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a test rule that matches a specific field
	testRule := `
title: Test Evaluation Rule
id: test-eval-rule
status: experimental
description: Rule for testing evaluation
logsource:
  category: test
detection:
  selection:
    command: "test_command"
  condition: selection
level: high
`

	rulePath := filepath.Join(tmpDir, "test_eval_rule.yml")
	err = os.WriteFile(rulePath, []byte(testRule), 0644)
	if err != nil {
		t.Fatalf("writing test rule: %v", err)
	}

	// Create engine
	engine, err := NewEngine(tmpDir)
	if err != nil {
		t.Fatalf("creating engine: %v", err)
	}

	tests := []struct {
		name           string
		fields         map[string]string
		expectedMatches int
	}{
		{
			name: "matching fields should trigger rule",
			fields: map[string]string{
				"command": "test_command",
			},
			expectedMatches: 1,
		},
		{
			name: "non-matching fields should not trigger rule",
			fields: map[string]string{
				"command": "other_command",
			},
			expectedMatches: 0,
		},
		{
			name: "empty fields should not trigger rule",
			fields: map[string]string{},
			expectedMatches: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := engine.Evaluate(tt.fields)

			// Engine now only returns matched rules, so length equals match count
			if len(results) != tt.expectedMatches {
				t.Errorf("Evaluate() returned %d matched rules, want %d", len(results), tt.expectedMatches)
			}

			// Verify rule metadata is populated for matched results
			for _, result := range results {
				if result.RuleID == "" {
					t.Error("Rule ID should not be empty")
				}
				if result.RuleName == "" {
					t.Error("Rule name should not be empty")
				}
				if result.Severity == "" {
					t.Error("Rule severity should not be empty")
				}
				if !result.Matched {
					t.Error("All returned results should be matched")
				}
			}
		})
	}
}

func TestGetStats(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "engine_stats_test")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	engine, err := NewEngine(tmpDir)
	if err != nil {
		t.Fatalf("creating engine: %v", err)
	}

	stats := engine.GetStats()

	expectedKeys := []string{"rules_loaded", "rules_dir", "last_load", "uptime"}
	for _, key := range expectedKeys {
		if _, exists := stats[key]; !exists {
			t.Errorf("GetStats() missing key: %s", key)
		}
	}

	if stats["rules_dir"] != tmpDir {
		t.Errorf("GetStats() rules_dir = %v, want %s", stats["rules_dir"], tmpDir)
	}

	rulesLoaded, ok := stats["rules_loaded"].(int)
	if !ok {
		t.Error("GetStats() rules_loaded should be int")
	}

	if rulesLoaded < 0 {
		t.Errorf("GetStats() rules_loaded = %d, should be >= 0", rulesLoaded)
	}
}

func TestMultiFieldSelectionLogic(t *testing.T) {
	// Test the potential bug where multi-field selections match when only one field matches
	tmpDir, err := os.MkdirTemp("", "multifield_test")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a rule with TWO required fields in the selection
	testRule := `
title: Multi-Field Selection Test
id: multifield-test-rule
status: experimental
description: Rule requiring BOTH event_type AND command to match
logsource:
  category: test
detection:
  selection:
    event_type: "tool_call"
    command|contains: "dangerous"
  condition: selection
level: high
`

	rulePath := filepath.Join(tmpDir, "multifield_rule.yml")
	err = os.WriteFile(rulePath, []byte(testRule), 0644)
	if err != nil {
		t.Fatalf("writing test rule: %v", err)
	}

	engine, err := NewEngine(tmpDir)
	if err != nil {
		t.Fatalf("creating engine: %v", err)
	}

	tests := []struct {
		name            string
		fields          map[string]string
		expectedMatches int
		description     string
	}{
		{
			name: "both fields match - should match",
			fields: map[string]string{
				"event_type": "tool_call",
				"command":    "dangerous_command",
			},
			expectedMatches: 1,
			description:     "Both event_type and command match, should trigger rule",
		},
		{
			name: "only event_type matches - should NOT match",
			fields: map[string]string{
				"event_type": "tool_call",
				"command":    "safe_command",
			},
			expectedMatches: 0,
			description:     "Only event_type matches, should NOT trigger rule",
		},
		{
			name: "only command matches - should NOT match",
			fields: map[string]string{
				"event_type": "other_event",
				"command":    "dangerous_command",
			},
			expectedMatches: 0,
			description:     "Only command matches, should NOT trigger rule",
		},
		{
			name: "neither field matches - should NOT match",
			fields: map[string]string{
				"event_type": "other_event",
				"command":    "safe_command",
			},
			expectedMatches: 0,
			description:     "Neither field matches, should NOT trigger rule",
		},
		{
			name: "missing command field - should NOT match",
			fields: map[string]string{
				"event_type": "tool_call",
			},
			expectedMatches: 0,
			description:     "Command field missing, should NOT trigger rule",
		},
		{
			name: "missing event_type field - should NOT match",
			fields: map[string]string{
				"command": "dangerous_command",
			},
			expectedMatches: 0,
			description:     "Event type field missing, should NOT trigger rule",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := engine.Evaluate(tt.fields)

			if len(results) != tt.expectedMatches {
				t.Errorf("%s: got %d matches, want %d. %s", tt.name, len(results), tt.expectedMatches, tt.description)
				t.Logf("Input fields: %+v", tt.fields)
				if len(results) > 0 {
					t.Logf("Matched rules: %+v", results)
				}
			}
		})
	}
}

// --- Session-aware Sigma rule tests ---

func TestEvaluate_SessionReconThenExfil(t *testing.T) {
	tmpDir := t.TempDir()
	ruleYAML := `
title: Recon Then Exfil Test
id: test-recon-exfil
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
	rulePath := filepath.Join(tmpDir, "test_recon_exfil.yml")
	if err := os.WriteFile(rulePath, []byte(ruleYAML), 0644); err != nil {
		t.Fatal(err)
	}

	eng, err := NewEngine(tmpDir)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// Should match: recon tools in history + exfil tool current
	fields := map[string]string{
		"tool":                 "curl",
		"session.recent_tools": "ls,cat,env",
		"session.tool_count":   "3",
	}
	results := eng.Evaluate(fields)
	if len(results) == 0 {
		t.Error("expected rule to match recon->exfil pattern")
	}
}

func TestEvaluate_SessionReconThenExfil_NoMatch_BenignTool(t *testing.T) {
	tmpDir := t.TempDir()
	ruleYAML := `
title: Recon Then Exfil Test
id: test-recon-exfil-neg
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
	rulePath := filepath.Join(tmpDir, "test_recon_exfil.yml")
	if err := os.WriteFile(rulePath, []byte(ruleYAML), 0644); err != nil {
		t.Fatal(err)
	}

	eng, err := NewEngine(tmpDir)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// Should NOT match: current tool is "echo", not an exfil tool
	fields := map[string]string{
		"tool":                 "echo",
		"session.recent_tools": "ls,cat",
		"session.tool_count":   "2",
	}
	results := eng.Evaluate(fields)
	if len(results) != 0 {
		t.Error("expected no match for benign current tool")
	}
}

func TestEvaluate_SessionReconThenExfil_NoMatch_NoReconHistory(t *testing.T) {
	tmpDir := t.TempDir()
	ruleYAML := `
title: Recon Then Exfil Test
id: test-recon-exfil-no-hist
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
	rulePath := filepath.Join(tmpDir, "test_recon_exfil.yml")
	if err := os.WriteFile(rulePath, []byte(ruleYAML), 0644); err != nil {
		t.Fatal(err)
	}

	eng, err := NewEngine(tmpDir)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// Should NOT match: exfil tool present but no recon history
	fields := map[string]string{
		"tool":                 "curl",
		"session.recent_tools": "echo,mkdir",
		"session.tool_count":   "2",
	}
	results := eng.Evaluate(fields)
	if len(results) != 0 {
		t.Error("expected no match when session history has no recon tools")
	}
}

func TestEvaluate_SessionVelocityAnomaly(t *testing.T) {
	tmpDir := t.TempDir()
	ruleYAML := `
title: Session Velocity Anomaly Test
id: test-session-velocity
status: test
logsource:
    product: ai_agent
    category: agent_events
detection:
    selection:
        session.tool_count|re: '^([2-9][0-9]|[1-9][0-9]{2,})$'
    condition: selection
level: medium
`
	rulePath := filepath.Join(tmpDir, "test_velocity.yml")
	if err := os.WriteFile(rulePath, []byte(ruleYAML), 0644); err != nil {
		t.Fatal(err)
	}

	eng, err := NewEngine(tmpDir)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	tests := []struct {
		name        string
		toolCount   string
		shouldMatch bool
	}{
		{"20 tools should match", "20", true},
		{"25 tools should match", "25", true},
		{"99 tools should match", "99", true},
		{"100 tools should match", "100", true},
		{"999 tools should match", "999", true},
		{"5 tools should not match", "5", false},
		{"10 tools should not match", "10", false},
		{"19 tools should not match", "19", false},
		{"1 tool should not match", "1", false},
		{"0 tools should not match", "0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := map[string]string{
				"tool":                 "some_tool",
				"session.tool_count":   tt.toolCount,
				"session.recent_tools": "ls,cat,echo",
			}
			results := eng.Evaluate(fields)
			if tt.shouldMatch && len(results) == 0 {
				t.Errorf("expected match for tool_count=%s", tt.toolCount)
			}
			if !tt.shouldMatch && len(results) != 0 {
				t.Errorf("expected no match for tool_count=%s", tt.toolCount)
			}
		})
	}
}