package models

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestShortID(t *testing.T) {
	for _, id := range []string{"", "rule-1", strings.Repeat("a", 256), "Ω"} {
		if got := ShortID(id, 256); got != id {
			t.Errorf("ShortID(%q) = %q, want unchanged", id, got)
		}
	}
	long := strings.Repeat("Ω", 200) // 400 bytes
	a, b := ShortID(long, 256), ShortID(long+"x", 256)
	if len(a) > 256 || !utf8.ValidString(a) || a == b || ShortID(long, 256) != a {
		t.Fatalf("ShortID not bounded, stable and distinct: %q %q", a, b)
	}
	if !strings.HasPrefix(a, "ΩΩ") || !strings.Contains(a, "~sha256:") {
		t.Errorf("ShortID keeps no readable prefix: %q", a)
	}
	if ShortID(a, 256) != a {
		t.Error("ShortID is not idempotent")
	}
	ctl := ShortID("rule\tid", 256)
	if strings.ContainsRune(ctl, '\t') || !strings.HasPrefix(ctl, "rule_id~sha256:") || ctl == ShortID("rule id", 256) {
		t.Errorf("ShortID(control) = %q", ctl)
	}
	if got := ShortID(string([]byte{0xff, 'a'}), 256); !utf8.ValidString(got) {
		t.Errorf("ShortID(invalid UTF-8) = %q", got)
	}
	if got := ShortID(long, 10); len(got) != 10 {
		t.Errorf("ShortID with a tiny max = %q", got)
	}
}

func TestCleanTagsAndText(t *testing.T) {
	tags := []string{"attack.x\tfoo", "", " \n ", strings.Repeat("é", 200)}
	for i := 0; i < 100; i++ {
		tags = append(tags, "t")
	}
	got := CleanTags(tags)
	if len(got) != MaxRuleTags || got[0] != "attack.x foo" || len(got[1]) > MaxRuleTagLength || !utf8.ValidString(got[1]) {
		t.Fatalf("CleanTags = %d tags, %q, %d bytes", len(got), got[0], len(got[1]))
	}
	if CleanTags(nil) == nil {
		t.Error("CleanTags(nil) is nil")
	}
	if got := Truncate(strings.Repeat("<", 1000), 128); len(got) > 128 || !strings.HasSuffix(got, TruncateMarker) {
		t.Errorf("Truncate = %q", got)
	}
	if got := Truncate("short", 128); got != "short" {
		t.Errorf("Truncate(short) = %q", got)
	}
}
