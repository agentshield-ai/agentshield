package sigmalite

import (
	"strings"
	"testing"
)

// TestComputeModifierFlags verifies the new modifierFlags optimization
// correctly maps modifier strings to boolean flags.
func TestComputeModifierFlags(t *testing.T) {
	tests := []struct {
		name      string
		modifiers []string
		check     func(modifierFlags) bool
		desc      string
	}{
		{"empty", nil, func(f modifierFlags) bool {
			return !f.re && !f.cidr && !f.contains && !f.all && !f.startswith && !f.endswith && !f.windash && !f.base64 && !f.base64offset && !f.expand
		}, "all flags should be false for empty modifiers"},
		{"re", []string{"re"}, func(f modifierFlags) bool { return f.re }, "re flag"},
		{"cidr", []string{"cidr"}, func(f modifierFlags) bool { return f.cidr }, "cidr flag"},
		{"contains", []string{"contains"}, func(f modifierFlags) bool { return f.contains }, "contains flag"},
		{"all", []string{"all"}, func(f modifierFlags) bool { return f.all }, "all flag"},
		{"startswith", []string{"startswith"}, func(f modifierFlags) bool { return f.startswith }, "startswith flag"},
		{"endswith", []string{"endswith"}, func(f modifierFlags) bool { return f.endswith }, "endswith flag"},
		{"windash", []string{"windash"}, func(f modifierFlags) bool { return f.windash }, "windash flag"},
		{"base64", []string{"base64"}, func(f modifierFlags) bool { return f.base64 }, "base64 flag"},
		{"base64offset", []string{"base64offset"}, func(f modifierFlags) bool { return f.base64offset }, "base64offset flag"},
		{"expand", []string{"expand"}, func(f modifierFlags) bool { return f.expand }, "expand flag"},
		{"multiple", []string{"contains", "all"}, func(f modifierFlags) bool {
			return f.contains && f.all && !f.re && !f.cidr
		}, "multiple flags set simultaneously"},
		{"unknown ignored", []string{"unknown_modifier"}, func(f modifierFlags) bool {
			return !f.re && !f.cidr && !f.contains && !f.all
		}, "unknown modifiers should be ignored"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags := computeModifierFlags(tt.modifiers)
			if !tt.check(flags) {
				t.Errorf("computeModifierFlags(%v): %s", tt.modifiers, tt.desc)
			}
		})
	}
}

// TestLockedCompileUpToDateBugfix verifies the fix for the cache invalidation
// bug where compiledModifiers was compared to itself instead of atom.Modifiers.
// Before the fix: slices.Equal(atom.compiledModifiers, atom.compiledModifiers)
// After the fix:  slices.Equal(atom.compiledModifiers, atom.Modifiers)
func TestLockedCompileUpToDateBugfix(t *testing.T) {
	atom := &SearchAtom{
		Field:    "command",
		Patterns: []string{"test"},
	}

	// Compile with no modifiers
	entry := &LogEntry{Fields: map[string]string{"command": "test"}}
	if !atom.ExprMatches(entry, nil) {
		t.Fatal("expected match with no modifiers")
	}

	// Now change modifiers - with the bug, the cache would incorrectly
	// report up-to-date because it compared compiledModifiers to itself.
	atom.Modifiers = []string{"contains"}

	// With the bugfix, the cache should be invalidated and recompiled.
	// "contains" modifier means "test" should match "this is a test value"
	entry2 := &LogEntry{Fields: map[string]string{"command": "this is a test value"}}
	if !atom.ExprMatches(entry2, nil) {
		t.Error("expected match with 'contains' modifier after cache invalidation (bugfix)")
	}
}

// TestIsRegexpSpecialLookupTable verifies the lookup-table optimization
// for isRegexpSpecial returns the same results as the original
// strings.IndexByte implementation.
func TestIsRegexpSpecialLookupTable(t *testing.T) {
	specials := `\.+*?()|[]{}^$`

	// Check all special characters are detected
	for _, c := range []byte(specials) {
		if !isRegexpSpecial(c) {
			t.Errorf("isRegexpSpecial(%q) = false, want true", c)
		}
	}

	// Check non-special characters are not detected
	for _, c := range []byte("abcdefghijklmnopqrstuvwxyz0123456789 _-=@#%&!~`:;'\",<>/") {
		if c == '\\' || c == '.' || c == '+' || c == '*' || c == '?' ||
			c == '(' || c == ')' || c == '|' || c == '[' || c == ']' ||
			c == '{' || c == '}' || c == '^' || c == '$' {
			continue
		}
		if isRegexpSpecial(c) {
			t.Errorf("isRegexpSpecial(%q) = true, want false", c)
		}
	}
}

// TestAppendPatternRegexpWithModifierFlags verifies that appendPatternRegexp
// works correctly with the new modifierFlags parameter instead of []string.
func TestAppendPatternRegexpWithModifierFlags(t *testing.T) {
	tests := []struct {
		name      string
		pattern   string
		modifiers []string
		isMessage bool
		input     string
		wantMatch bool
	}{
		{
			name:      "contains modifier matches substring",
			pattern:   "test",
			modifiers: []string{"contains"},
			input:     "this is a test value",
			wantMatch: true,
		},
		{
			name:      "startswith modifier anchors at start",
			pattern:   "test",
			modifiers: []string{"startswith"},
			input:     "test_value",
			wantMatch: true,
		},
		{
			name:      "startswith does not match mid-string",
			pattern:   "test",
			modifiers: []string{"startswith"},
			input:     "not_test",
			wantMatch: false,
		},
		{
			name:      "endswith modifier anchors at end",
			pattern:   "test",
			modifiers: []string{"endswith"},
			input:     "this_is_test",
			wantMatch: true,
		},
		{
			name:      "re modifier uses raw regex",
			pattern:   "^test[0-9]+$",
			modifiers: []string{"re"},
			input:     "test123",
			wantMatch: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mf := computeModifierFlags(tt.modifiers)
			var sb strings.Builder
			if !appendPatternRegexp(&sb, tt.pattern, mf, tt.isMessage) {
				t.Fatal("appendPatternRegexp returned false")
			}

			// The generated pattern should match (or not) the input.
			// Use the compiled atom for a proper test.
			atom := &SearchAtom{
				Field:     "field",
				Modifiers: tt.modifiers,
				Patterns:  []string{tt.pattern},
			}
			entry := &LogEntry{Fields: map[string]string{"field": tt.input}}
			got := atom.ExprMatches(entry, nil)
			if got != tt.wantMatch {
				t.Errorf("ExprMatches() = %v, want %v (pattern=%q, modifiers=%v, input=%q)",
					got, tt.wantMatch, tt.pattern, tt.modifiers, tt.input)
			}
		})
	}
}

// TestEqualFoldFieldMatching verifies that strings.EqualFold is used for
// case-insensitive field name matching instead of strings.ToLower comparison.
func TestEqualFoldFieldMatching(t *testing.T) {
	atom := &SearchAtom{
		Field:    "CommandLine",
		Patterns: []string{"test"},
	}

	tests := []struct {
		name      string
		fieldName string
		value     string
		want      bool
	}{
		{"exact case", "CommandLine", "test", true},
		{"all lower", "commandline", "test", true},
		{"all upper", "COMMANDLINE", "test", true},
		{"mixed case", "cOmMaNdLiNe", "test", true},
		{"different field", "OtherField", "test", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := &LogEntry{Fields: map[string]string{tt.fieldName: tt.value}}
			got := atom.ExprMatches(entry, nil)
			if got != tt.want {
				t.Errorf("ExprMatches with field %q = %v, want %v", tt.fieldName, got, tt.want)
			}
		})
	}
}
