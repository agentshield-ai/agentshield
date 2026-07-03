package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRuleIdentities(t *testing.T) {
	dir := t.TempDir()
	writeRule := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	writeRule("a.yml", "id: rule-a-id\ntitle: Rule A Title\n")
	writeRule("b.yaml", "id: rule-b-id\ntitle: Rule B Title\n")
	writeRule("notes.txt", "id: ignored\n")

	refs, err := loadRuleIdentities(dir)
	if err != nil {
		t.Fatalf("loadRuleIdentities: %v", err)
	}

	for _, want := range []string{"rule-a-id", "Rule A Title", "rule-b-id", "Rule B Title"} {
		if !refs[want] {
			t.Errorf("expected reference %q to be valid", want)
		}
	}
	if refs["ignored"] {
		t.Error("non-YAML file should not contribute references")
	}
}

func TestValidateTestcaseReferences(t *testing.T) {
	valid := map[string]bool{
		"Real Rule Title": true,
		"real-rule-id":    true,
	}

	tests := []struct {
		name      string
		testcases []*Testcase
		wantErr   bool
	}{
		{
			name: "all references resolve",
			testcases: []*Testcase{
				{ID: "tc1", Expected: Expected{MustTriggerRules: []string{"Real Rule Title"}}},
				{ID: "tc2", Expected: Expected{MustNotTrigger: []string{"real-rule-id"}}},
			},
			wantErr: false,
		},
		{
			name: "unknown must_trigger reference fails",
			testcases: []*Testcase{
				{ID: "tc3", Expected: Expected{MustTriggerRules: []string{"agent-nonexistent-001"}}},
			},
			wantErr: true,
		},
		{
			name: "unknown must_not_trigger reference fails",
			testcases: []*Testcase{
				{ID: "tc4", Expected: Expected{MustNotTrigger: []string{"agent-nonexistent-002"}}},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTestcaseReferences(tt.testcases, valid)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateTestcaseReferences() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestCorpusTestcaseReferencesResolve is an integration guard: every rule
// reference used by a committed testcase must resolve against the vendored
// rule corpus. This is what catches a testcase asserting a rule that does not
// exist (previously a silent no-op).
func TestCorpusTestcaseReferencesResolve(t *testing.T) {
	root := projectRoot()
	if root == "" {
		t.Skip("project root not found — skipping integration test")
	}
	rulesDir := filepath.Join(root, "rules")
	testcasesDir := filepath.Join(root, "bench", "testcases")
	if _, err := os.Stat(rulesDir); err != nil {
		t.Skip("rules directory not found — skipping")
	}

	refs, err := loadRuleIdentities(rulesDir)
	if err != nil {
		t.Fatalf("loading rule identities: %v", err)
	}

	var testcases []*Testcase
	err = filepath.WalkDir(testcasesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := filepath.Ext(d.Name())
		if ext != ".yml" && ext != ".yaml" {
			return nil
		}
		tc, err := loadTestcase(path)
		if err != nil {
			t.Errorf("loading testcase %s: %v", path, err)
			return nil
		}
		testcases = append(testcases, tc)
		return nil
	})
	if err != nil {
		t.Fatalf("walking testcases: %v", err)
	}

	if err := validateTestcaseReferences(testcases, refs); err != nil {
		t.Error(err)
	}
}

// projectRoot walks up from this test file to the repository root (the
// directory containing go.mod).
func projectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}
