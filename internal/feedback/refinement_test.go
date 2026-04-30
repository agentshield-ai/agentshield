package feedback

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/store"

	_ "modernc.org/sqlite" // Import SQLite driver
)

func TestDetermineRecommendedAction(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer func() { _ = st.Close() }()

	fm := NewFeedbackManager(st)
	re := NewRefinementEngine(fm, "/tmp", nil)

	tests := []struct {
		fpRate   float64
		expected string
	}{
		{0.0, "none"},
		{0.05, "none"},
		{0.15, "monitor"},
		{0.25, "refine"},
		{0.5, "refine_urgently"},
		{0.8, "disable"},
		{1.0, "disable"},
	}

	for _, test := range tests {
		result := re.determineRecommendedAction(test.fpRate)
		if result != test.expected {
			t.Errorf("determineRecommendedAction(%.2f) = %s, expected %s", test.fpRate, result, test.expected)
		}
	}
}

func TestGenerateSuggestions(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer func() { _ = st.Close() }()

	fm := NewFeedbackManager(st)
	re := NewRefinementEngine(fm, "/tmp", nil)

	// Test high false positive rate
	stats := &RuleStats{
		RuleName:          "test-rule",
		FalsePositiveRate: 0.6,
		TotalAlerts:       100,
	}

	patterns := []string{"tool:file_read (5 occurrences)", "tool:network_scan (3 occurrences)"}

	suggestions := re.generateSuggestions(stats, patterns)

	if len(suggestions) == 0 {
		t.Error("Expected suggestions for high FP rate")
	}

	// Check that we have a severity reduction suggestion
	hasSeverityReduction := false
	for _, suggestion := range suggestions {
		if suggestion.Type == "reduce_severity" {
			hasSeverityReduction = true
			break
		}
	}

	if !hasSeverityReduction {
		t.Error("Expected severity reduction suggestion for very high FP rate")
	}

	// Test medium false positive rate
	stats.FalsePositiveRate = 0.35
	suggestions = re.generateSuggestions(stats, patterns)

	hasException := false
	for _, suggestion := range suggestions {
		if suggestion.Type == "add_exception" {
			hasException = true
			break
		}
	}

	if !hasException {
		t.Error("Expected exception suggestion for medium FP rate")
	}

	// Test with patterns
	hasNarrowCondition := false
	for _, suggestion := range suggestions {
		if suggestion.Type == "narrow_condition" {
			hasNarrowCondition = true
			break
		}
	}

	if !hasNarrowCondition {
		t.Error("Expected narrow condition suggestion when patterns exist")
	}
}

func TestValidateRuleFilePath(t *testing.T) {
	// Create temporary directory structure
	tmpDir, err := os.MkdirTemp("", "agentshield-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	rulesDir := filepath.Join(tmpDir, "rules")
	err = os.MkdirAll(rulesDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create rules dir: %v", err)
	}

	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer func() { _ = st.Close() }()

	fm := NewFeedbackManager(st)
	re := NewRefinementEngine(fm, rulesDir, nil)

	tests := []struct {
		name        string
		path        string
		expectError bool
	}{
		{
			name:        "Valid path in rules directory",
			path:        filepath.Join(rulesDir, "test.yml"),
			expectError: false,
		},
		{
			name:        "Path traversal attempt",
			path:        filepath.Join(rulesDir, "../../../etc/passwd"),
			expectError: true,
		},
		{
			name:        "Path with .. component",
			path:        rulesDir + "/subdir/../test.yml",
			expectError: true,
		},
		{
			name:        "Path outside rules directory",
			path:        "/tmp/malicious.yml",
			expectError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := re.validateRuleFilePath(test.path)

			if test.expectError && err == nil {
				t.Error("Expected error but got none")
			}

			if !test.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestValidateRuleYAML(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer func() { _ = st.Close() }()

	fm := NewFeedbackManager(st)
	re := NewRefinementEngine(fm, "/tmp", nil)

	tests := []struct {
		name        string
		yaml        string
		expectError bool
	}{
		{
			name: "Valid YAML",
			yaml: `
name: test-rule
severity: high
detection:
  condition: selection
  selection:
    tool: file_read`,
			expectError: false,
		},
		{
			name:        "Invalid YAML syntax",
			yaml:        "invalid: yaml: content: [unclosed",
			expectError: true,
		},
		{
			name:        "Empty content",
			yaml:        "",
			expectError: false, // Empty YAML is valid (null document)
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := re.validateRuleYAML(test.yaml)

			if test.expectError && err == nil {
				t.Error("Expected error but got none")
			}

			if !test.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestAnalyzeFalsePositivePatterns(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer func() { _ = st.Close() }()

	// Add test alerts
	alerts := []*store.Alert{
		{
			RuleName:    "test-rule",
			Tool:        "file_read",
			ActionTaken: "block",
			Timestamp:   time.Now(),
		},
		{
			RuleName:    "test-rule",
			Tool:        "file_read",
			ActionTaken: "block",
			Timestamp:   time.Now(),
		},
		{
			RuleName:    "test-rule",
			Tool:        "network_scan",
			ActionTaken: "log",
			Timestamp:   time.Now(),
		},
		{
			RuleName:    "other-rule",
			Tool:        "file_write",
			ActionTaken: "block",
			Timestamp:   time.Now(),
		},
	}

	for _, alert := range alerts {
		err := st.InsertAlert(alert)
		if err != nil {
			t.Fatalf("Failed to insert test alert: %v", err)
		}
	}

	fm := NewFeedbackManager(st)
	re := NewRefinementEngine(fm, "/tmp", nil)

	patterns, err := re.analyzeFalsePositivePatterns("test-rule")
	if err != nil {
		t.Fatalf("analyzeFalsePositivePatterns failed: %v", err)
	}

	// Should find patterns with at least 2 occurrences
	hasFileReadPattern := false
	for _, pattern := range patterns {
		if containsSubstring(pattern, "tool:file_read") && containsSubstring(pattern, "2 occurrences") {
			hasFileReadPattern = true
			break
		}
	}

	if !hasFileReadPattern {
		t.Error("Expected to find file_read pattern with 2 occurrences")
	}

	// Should not include network_scan (only 1 occurrence)
	hasNetworkScanPattern := false
	for _, pattern := range patterns {
		if containsSubstring(pattern, "tool:network_scan") {
			hasNetworkScanPattern = true
			break
		}
	}

	if hasNetworkScanPattern {
		t.Error("Should not include network_scan pattern (only 1 occurrence)")
	}
}

func TestFindRuleFile(t *testing.T) {
	// Create temporary directory structure
	tmpDir, err := os.MkdirTemp("", "agentshield-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	rulesDir := filepath.Join(tmpDir, "rules")
	err = os.MkdirAll(rulesDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create rules dir: %v", err)
	}

	// Create test rule files
	testRule1 := `
name: test-rule-1
id: rule-1
severity: high
`
	testRule2 := `
name: test-rule-2  
id: rule-2
severity: medium
`

	err = os.WriteFile(filepath.Join(rulesDir, "rule1.yml"), []byte(testRule1), 0644)
	if err != nil {
		t.Fatalf("Failed to write test rule file: %v", err)
	}

	err = os.WriteFile(filepath.Join(rulesDir, "rule2.yaml"), []byte(testRule2), 0644)
	if err != nil {
		t.Fatalf("Failed to write test rule file: %v", err)
	}

	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer func() { _ = st.Close() }()

	fm := NewFeedbackManager(st)
	re := NewRefinementEngine(fm, rulesDir, nil)

	// Test finding rule by name
	ruleFile, err := re.findRuleFile("test-rule-1")
	if err != nil {
		t.Fatalf("Failed to find rule file: %v", err)
	}

	expectedFile := filepath.Join(rulesDir, "rule1.yml")
	if ruleFile != expectedFile {
		t.Errorf("Expected rule file %s, got %s", expectedFile, ruleFile)
	}

	// Test finding rule by ID
	ruleFile, err = re.findRuleFile("rule-2")
	if err != nil {
		t.Fatalf("Failed to find rule file by ID: %v", err)
	}

	expectedFile = filepath.Join(rulesDir, "rule2.yaml")
	if ruleFile != expectedFile {
		t.Errorf("Expected rule file %s, got %s", expectedFile, ruleFile)
	}

	// Test non-existent rule
	_, err = re.findRuleFile("non-existent-rule")
	if err == nil {
		t.Error("Expected error for non-existent rule")
	}
}

func TestRefinementResult(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer func() { _ = st.Close() }()

	// Add test alerts
	alert := &store.Alert{
		RuleName:    "test-rule",
		Severity:    "high",
		Tool:        "suspicious_tool",
		ActionTaken: "block",
		Timestamp:   time.Now(),
	}

	err = st.InsertAlert(alert)
	if err != nil {
		t.Fatalf("Failed to insert test alert: %v", err)
	}

	fm := NewFeedbackManager(st)

	// Create a temporary rules directory
	tmpDir, err := os.MkdirTemp("", "agentshield-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create a test rule file
	testRule := `
name: test-rule
severity: high
detection:
  condition: selection
  selection:
    tool: suspicious_tool
`
	ruleFile := filepath.Join(tmpDir, "test-rule.yml")
	err = os.WriteFile(ruleFile, []byte(testRule), 0644)
	if err != nil {
		t.Fatalf("Failed to write test rule: %v", err)
	}

	re := NewRefinementEngine(fm, tmpDir, nil)

	ctx := context.Background()
	result, err := re.AnalyzeRule(ctx, "test-rule")
	if err != nil {
		t.Fatalf("AnalyzeRule failed: %v", err)
	}

	if result.RuleName != "test-rule" {
		t.Errorf("Expected rule name 'test-rule', got %s", result.RuleName)
	}

	if result.RuleFile != ruleFile {
		t.Errorf("Expected rule file %s, got %s", ruleFile, result.RuleFile)
	}

	if result.TotalAlerts != 1 {
		t.Errorf("Expected 1 total alert, got %d", result.TotalAlerts)
	}

	if result.RecommendedAction == "" {
		t.Error("Expected recommended action to be set")
	}
}

// Helper function
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
			func() bool {
				for i := 0; i <= len(s)-len(substr); i++ {
					if s[i:i+len(substr)] == substr {
						return true
					}
				}
				return false
			}()))
}
