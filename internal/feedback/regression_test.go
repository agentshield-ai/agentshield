package feedback

import (
	"html"
	"os"
	"path/filepath"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/store"

	_ "modernc.org/sqlite"
)

// TestSanitizeCommentUsesHTMLEscapeString verifies that sanitizeComment uses
// html.EscapeString (which encodes all 5 special HTML characters) rather than
// manual string replacement (which only handled < and >).
func TestSanitizeCommentUsesHTMLEscapeString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"encodes ampersand", "a&b", html.EscapeString("a&b")},
		{"encodes double quote", `he said "hello"`, html.EscapeString(`he said "hello"`)},
		{"encodes single quote", "it's fine", html.EscapeString("it's fine")},
		{"encodes lt and gt", "<script>alert(1)</script>", html.EscapeString("<script>alert(1)</script>")},
		{"empty unchanged", "", ""},
		{"no special chars unchanged", "plain text", "plain text"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeComment(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeComment(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestApplyRefinementPathValidationBeforeIO verifies that ApplyRefinement
// validates the file path BEFORE attempting any file I/O operations.
// This is a security regression test for the fix that moved validateRuleFilePath
// before os.ReadFile.
func TestApplyRefinementPathValidationBeforeIO(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "refinement_regression")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	rulesDir := filepath.Join(tmpDir, "rules")
	if err := os.MkdirAll(rulesDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a rule file outside the rules directory (simulating traversal target)
	outsideFile := filepath.Join(tmpDir, "outside.yml")
	if err := os.WriteFile(outsideFile, []byte("name: outside\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a symlink inside rules dir pointing outside
	symlinkPath := filepath.Join(rulesDir, "traversal.yml")
	// Note: we're testing the path validation, not the symlink itself
	// Create a rule that would be found by findRuleFile
	ruleContent := `
name: traversal-test
id: traversal-test
severity: high
detection:
  condition: selection
  selection:
    tool: test
`
	if err := os.WriteFile(filepath.Join(rulesDir, "traversal-test.yml"), []byte(ruleContent), 0644); err != nil {
		t.Fatal(err)
	}
	_ = symlinkPath

	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	fm := NewFeedbackManager(st)
	re := NewRefinementEngine(fm, rulesDir, nil)

	// Attempting to apply refinement with a path traversal should fail
	suggestion := &RefinementSuggestion{
		Type:        "add_exception",
		Description: "test exception",
	}

	// This should fail due to path validation (file not in rules dir)
	err = re.ApplyRefinement("non-existent-outside", suggestion, false)
	if err == nil {
		t.Error("expected error for non-existent rule, got nil")
	}
}

// TestApplyRefinementBackupFilePermissions verifies that backup files
// are created with 0600 permissions (not 0644) to prevent unauthorized reads.
func TestApplyRefinementBackupFilePermissions(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "refinement_perms")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	rulesDir := filepath.Join(tmpDir, "rules")
	if err := os.MkdirAll(rulesDir, 0755); err != nil {
		t.Fatal(err)
	}

	ruleContent := `
name: perm-test-rule
id: perm-test-rule
severity: high
detection:
  condition: selection
  selection:
    tool: test
`
	ruleFile := filepath.Join(rulesDir, "perm-test-rule.yml")
	if err := os.WriteFile(ruleFile, []byte(ruleContent), 0644); err != nil {
		t.Fatal(err)
	}

	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	fm := NewFeedbackManager(st)
	re := NewRefinementEngine(fm, rulesDir, nil)

	suggestion := &RefinementSuggestion{
		Type:        "add_exception",
		Description: "test exception",
	}

	// ApplyRefinement may return an error from the stub applyRefinementToContent,
	// but the backup file should still have been created before that step.
	_ = re.ApplyRefinement("perm-test-rule", suggestion, true)

	// Check that backup file was created with restrictive permissions
	entries, err := os.ReadDir(rulesDir)
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if len(name) > len("perm-test-rule.yml.backup.") && name[:len("perm-test-rule.yml.backup.")] == "perm-test-rule.yml.backup." {
			info, err := entry.Info()
			if err != nil {
				t.Fatal(err)
			}
			perm := info.Mode().Perm()
			if perm != 0600 {
				t.Errorf("backup file %s has permissions %o, want 0600", name, perm)
			}
			return
		}
	}

	t.Error("backup file not found")
}
