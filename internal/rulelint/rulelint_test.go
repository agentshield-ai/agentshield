package rulelint

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// findRulesDir locates rules/rules/ from the test file location, mirroring the
// convention in internal/evaluate and internal/engine tests.
func findRulesDir(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("cannot determine test file location")
	}
	dir := filepath.Dir(filename)
	for range 6 {
		candidate := filepath.Join(dir, "rules", "rules")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("rules directory not found — skipping rule lint")
	return ""
}

func lintCorpus(t *testing.T) *Result {
	t.Helper()
	res, err := LintDir(findRulesDir(t))
	if err != nil {
		t.Fatalf("LintDir: %v", err)
	}
	return res
}

// TestNoUndocumentedRuleGaps is the guardrail: a rule may not match on an
// event_type no producer emits, a session field the registry never injects, or a
// malformed MITRE tag — unless the gap is explicitly documented in the ground-truth
// lists in rulelint.go. A new violation fails CI with an actionable message.
func TestNoUndocumentedRuleGaps(t *testing.T) {
	res := lintCorpus(t)
	if len(res.Findings) == 0 {
		return
	}
	for _, f := range res.Findings {
		t.Errorf("%s", f)
	}
	t.Logf("\n%d undocumented rule gap(s). Each is a rule that cannot fire (or a malformed tag). "+
		"Fix the rule, add a producer, or — if intentional — document it in the relevant ground-truth "+
		"list in internal/rulelint/rulelint.go with justification.", len(res.Findings))
}

// TestGroundTruthSetsConsistent guards the invariants of the lists themselves:
// no event_type may be simultaneously "emitted" and "a known gap", and no session
// field may be both injected and a known gap. Such overlap would mask real gaps.
func TestGroundTruthSetsConsistent(t *testing.T) {
	for et := range KnownUnemittedEventTypes {
		if _, ok := EmittedEventTypes[et]; ok {
			t.Errorf("event_type %q is in both EmittedEventTypes and KnownUnemittedEventTypes", et)
		}
	}
	for f := range KnownUnpopulatedSessionFields {
		if InjectedSessionFields[f] {
			t.Errorf("session field %q is in both InjectedSessionFields and KnownUnpopulatedSessionFields", f)
		}
	}
}

// TestGapListsAreNotStale ensures every documented gap is still referenced by at
// least one rule. When a rule is fixed or a producer is added, its gap entry must
// be removed; this test forces that cleanup so the lists reflect reality and don't
// silently permit a future re-introduction.
func TestGapListsAreNotStale(t *testing.T) {
	res := lintCorpus(t)

	for et, area := range KnownUnemittedEventTypes {
		if !res.ReferencedEventTypes[et] {
			t.Errorf("KnownUnemittedEventTypes[%q] (%s) is no longer referenced by any rule — "+
				"remove it, or move it to EmittedEventTypes if a producer now emits it", et, area)
		}
	}
	for f, area := range KnownUnpopulatedSessionFields {
		if !res.ReferencedSessionFields[f] {
			t.Errorf("KnownUnpopulatedSessionFields[%q] (%s) is no longer referenced by any rule — "+
				"remove it, or move it to InjectedSessionFields if the registry now injects it", f, area)
		}
	}
	for id, reason := range ConfirmedInvalidMitreIDs {
		if !res.ReferencedMitreIDs[id] {
			t.Errorf("ConfirmedInvalidMitreIDs[%q] (%s) is no longer referenced by any rule — "+
				"remove it (the tag was presumably fixed)", id, reason)
		}
	}
	for slug, reason := range NonStandardTactics {
		if !res.ReferencedTactics[slug] {
			t.Errorf("NonStandardTactics[%q] (%s) is no longer referenced by any rule — remove it", slug, reason)
		}
	}
}

// TestReportSummary prints a coverage snapshot. Informational; never fails.
func TestReportSummary(t *testing.T) {
	res := lintCorpus(t)
	t.Logf("rule lint: %d event_types referenced (%d documented gaps), %d session fields referenced (%d documented gaps), %d findings",
		len(res.ReferencedEventTypes), len(KnownUnemittedEventTypes),
		len(res.ReferencedSessionFields), len(KnownUnpopulatedSessionFields),
		len(res.Findings))
}
