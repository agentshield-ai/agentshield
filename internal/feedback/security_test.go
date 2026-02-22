package feedback

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/store"
	_ "modernc.org/sqlite"
)

func newTestStoreOrSkip(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	return st
}

// TestValidateRuleFilePath_PathTraversal verifies S-5: the rule file path
// validator blocks directory traversal attacks that could allow writing
// arbitrary files outside the rules directory.
func TestValidateRuleFilePath_PathTraversal(t *testing.T) {
	// Create a temporary rules directory
	rulesDir, err := os.MkdirTemp("", "rules_sec_test_*")
	if err != nil {
		t.Fatalf("creating temp rules dir: %v", err)
	}
	defer os.RemoveAll(rulesDir)

	engine := &RefinementEngine{rulesDir: rulesDir}

	tests := []struct {
		name      string
		path      string
		expectErr bool
	}{
		{
			name:      "valid path inside rules dir",
			path:      filepath.Join(rulesDir, "my_rule.yaml"),
			expectErr: false,
		},
		{
			name:      "valid nested path inside rules dir",
			path:      filepath.Join(rulesDir, "subdir", "rule.yaml"),
			expectErr: false,
		},
		{
			name:      "dot-dot traversal",
			path:      filepath.Join(rulesDir, "..", "..", "etc", "passwd"),
			expectErr: true,
		},
		{
			name:      "absolute path outside rules dir",
			path:      "/etc/cron.d/malicious",
			expectErr: true,
		},
		{
			name:      "relative traversal with dots",
			path:      rulesDir + "/../../etc/shadow",
			expectErr: true,
		},
		{
			name:      "embedded .. in path",
			path:      filepath.Join(rulesDir, "legitimate/../../../tmp/malicious"),
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := engine.validateRuleFilePath(tt.path)
			if tt.expectErr && err == nil {
				t.Errorf("expected path traversal error for %q, got nil", tt.path)
			}
			if !tt.expectErr && err != nil {
				t.Errorf("unexpected error for valid path %q: %v", tt.path, err)
			}
		})
	}
}

// TestValidateRuleFilePath_RawDotDot verifies that a raw string containing ".."
// is rejected. This guards against cases where the path is constructed from
// user-controlled input without going through filepath.Join.
func TestValidateRuleFilePath_RawDotDot(t *testing.T) {
	rulesDir, err := os.MkdirTemp("", "rules_dotdot_*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(rulesDir)

	engine := &RefinementEngine{rulesDir: rulesDir}

	// Construct path manually with ".." to simulate attacker-controlled input.
	// Note: filepath.Join would clean this automatically; this tests the
	// defense-in-depth string check in validateRuleFilePath.
	rawPath := rulesDir + "/subdir/../../../etc/passwd"
	err = engine.validateRuleFilePath(rawPath)
	if err == nil {
		t.Error("expected error for raw path containing '..', got nil")
	}
}

// TestApplyRefinement_RuleNotFound verifies S-5: ApplyRefinement returns
// an error early when the rule file does not exist, without writing anything.
func TestApplyRefinement_RuleNotFound(t *testing.T) {
	rulesDir, err := os.MkdirTemp("", "rules_refinement_*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(rulesDir)

	st := newTestStoreOrSkip(t)
	defer st.Close()
	fm := NewFeedbackManager(st)
	engine := NewRefinementEngine(fm, rulesDir, nil)

	suggestion := &RefinementSuggestion{
		Type:        "add_exception",
		Description: "Test suggestion",
	}

	// Rule doesn't exist in the rules dir
	err = engine.ApplyRefinement("nonexistent-rule", suggestion, false)
	if err == nil {
		t.Error("expected error when rule file does not exist, got nil")
	}
}

// TestSanitizeCommentXSS verifies M-1: sanitizeComment strips HTML tags
// to prevent stored XSS.
func TestSanitizeCommentXSS(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		check   func(string) bool
		desc    string
	}{
		{
			name:  "script tag neutralized",
			input: `<script>alert(1)</script>`,
			check: func(s string) bool { return !strings.Contains(s, "<script>") },
			desc:  "should not contain <script>",
		},
		{
			name:  "iframe tag neutralized",
			input: `<iframe src="evil.com"></iframe>`,
			check: func(s string) bool { return !strings.Contains(s, "<iframe") },
			desc:  "should not contain <iframe",
		},
		{
			name:  "img onerror neutralized",
			input: `<img src=x onerror=alert(1)>`,
			check: func(s string) bool { return !strings.Contains(s, "<img") },
			desc:  "should not contain <img",
		},
		{
			name:  "plain text unchanged semantically",
			input: "This is a normal comment",
			check: func(s string) bool { return s == "This is a normal comment" },
			desc:  "plain text should pass through unchanged",
		},
		{
			name:  "empty string unchanged",
			input: "",
			check: func(s string) bool { return s == "" },
			desc:  "empty string should remain empty",
		},
		// New tests for enhanced html.EscapeString sanitization
		{
			name:  "ampersand encoded",
			input: `a&b`,
			check: func(s string) bool { return s == "a&amp;b" },
			desc:  "ampersand should be encoded to prevent double-encoding attacks",
		},
		{
			name:  "double quote encoded",
			input: `"onclick="alert(1)`,
			check: func(s string) bool { return !strings.Contains(s, `"onclick`) },
			desc:  "double quotes should be encoded to prevent attribute injection",
		},
		{
			name:  "single quote encoded",
			input: `' onmouseover='alert(1)`,
			check: func(s string) bool { return !strings.Contains(s, `'`) },
			desc:  "single quotes should be encoded to prevent attribute injection",
		},
		{
			name:  "combined XSS via attributes",
			input: `" onfocus="alert(document.cookie)" autofocus="`,
			check: func(s string) bool { return !strings.Contains(s, `onfocus="`) },
			desc:  "attribute-based XSS vectors should be neutralized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeComment(tt.input)
			if !tt.check(result) {
				t.Errorf("sanitizeComment(%q) = %q; %s", tt.input, result, tt.desc)
			}
		})
	}
}
