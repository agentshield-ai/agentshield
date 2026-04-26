package feedback

import (
	"strings"
	"testing"
)

// TestSanitizeCommentXSS verifies M-1: sanitizeComment strips HTML tags
// to prevent stored XSS.
func TestSanitizeCommentXSS(t *testing.T) {
	tests := []struct {
		name  string
		input string
		check func(string) bool
		desc  string
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
