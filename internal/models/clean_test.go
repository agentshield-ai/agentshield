package models

import (
	"fmt"
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

// A rule loaded with an empty or whitespace-only id gets a stable generated
// id (from its title), so its verdict is stored rather than rejected, and
// every path (push, OTLP, replay) names it the same way.
func TestCleanRuleID(t *testing.T) {
	a := CleanRuleID("   ", "Whitespace id rule")
	if !strings.HasPrefix(a, GeneratedRuleIDPrefix) || len(a) > MaxRuleIDLength {
		t.Fatalf("CleanRuleID(whitespace) = %q", a)
	}
	if b := CleanRuleID("", " Whitespace id rule\t"); b != a {
		t.Errorf("generated id depends on padding: %q vs %q", b, a)
	}
	if c := CleanRuleID("\t\n", "Another rule"); c == a {
		t.Error("different titles give the same generated id")
	}
	for _, id := range []string{a, "rule-1", " rule-1 ", strings.Repeat("y", 300), "ctl\tid"} {
		once := CleanRuleID(id, "t")
		if CleanRuleID(once, "t") != once || CleanRuleID(once, "other title") != once {
			t.Errorf("CleanRuleID not idempotent for %q", id)
		}
	}
	if got := CleanRuleID(" rule-1 ", "t"); got != "rule-1" {
		t.Errorf("padded id = %q", got)
	}
	if got := CleanRuleID(strings.Repeat("y", 300), "t"); got != ShortID(strings.Repeat("y", 300), MaxRuleIDLength) {
		t.Errorf("long id = %q", got)
	}
}

// Tags are bounded in count, in length each, and in total bytes.
func TestCleanTagsTotalBudget(t *testing.T) {
	var tags []string
	for i := 0; i < 80; i++ {
		tags = append(tags, strings.Repeat("g", 300))
	}
	got := CleanTags(tags)
	total := 0
	for _, tg := range got {
		total += len(tg)
	}
	if total > MaxRuleTagBytes || len(got) != MaxRuleTagBytes/MaxRuleTagLength {
		t.Fatalf("CleanTags kept %d tags, %d bytes", len(got), total)
	}
}

// SelectRules: a repeated id keeps its most severe entry, the most severe
// come first (stable otherwise), at most MaxRules are kept, and selecting
// twice changes nothing.
func TestSelectRules(t *testing.T) {
	var in []Rule
	levels := []string{"low", "medium", "high", "critical"}
	for i := 0; i < 150; i++ {
		in = append(in, Rule{ID: fmt.Sprintf("r%03d", i), Level: levels[(i*7)%4]})
	}
	in = append(in, Rule{ID: "dup", Title: "a", Level: "low"}, Rule{ID: "dup", Title: "b", Level: "critical"}, Rule{ID: "dup", Title: "c", Level: "critical"})
	out := SelectRules(in)
	if len(out) != MaxRules {
		t.Fatalf("kept %d rules", len(out))
	}
	seen := map[string]bool{}
	for i, r := range out {
		if seen[r.ID] {
			t.Fatalf("id %s kept twice", r.ID)
		}
		seen[r.ID] = true
		if i > 0 && LevelRank(r.Level) > LevelRank(out[i-1].Level) {
			t.Fatalf("not most severe first at %d", i)
		}
		if i > 0 && r.Level == out[i-1].Level && r.ID != "dup" && out[i-1].ID != "dup" && r.ID < out[i-1].ID {
			t.Fatalf("order among equals not kept at %d: %s after %s", i, r.ID, out[i-1].ID)
		}
	}
	var dup *Rule
	for i := range out {
		if out[i].ID == "dup" {
			dup = &out[i]
		}
	}
	if dup == nil || dup.Level != "critical" || dup.Title != "b" {
		t.Fatalf("dup = %+v, want its first critical entry", dup)
	}
	again := SelectRules(out)
	for i := range out {
		if again[i].ID != out[i].ID {
			t.Fatal("SelectRules is not idempotent")
		}
	}
	if SelectRules(nil) == nil {
		t.Error("SelectRules(nil) is nil")
	}
}
