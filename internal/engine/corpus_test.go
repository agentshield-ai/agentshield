package engine

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	sigmalite "github.com/agentshield-ai/agentshield/pkg/sigma"
)

// liveEventTypes are the event_type values the shipping event sources can
// emit today: the connector normalisers (plugins/*/src/normalise.ts) and the
// server-side default mapping (defaultEventTypeForTool in internal/server).
// A rule that gates exclusively on other event types can never fire in
// production, regardless of how good its detection logic is.
var liveEventTypes = map[string]bool{
	"tool_call":     true,
	"file_read":     true,
	"file_write":    true,
	"file_edit":     true,
	"session_spawn": true,
}

// knownUnreachableRules are corpus rules that currently gate exclusively on
// event types no shipping connector emits. They load fine but cannot fire in
// production. This list is a ratchet: wiring an event type through a
// connector (and adding it to liveEventTypes) shrinks it, and new rules must
// not be added to it — key new detections on emitted event types, or emit
// the new type from a connector first.
var knownUnreachableRules = map[string]bool{
	"ai_agent_authority_hijacking.yml":       true, // content_retrieval, tool_response, user_input
	"ai_agent_context_poisoning.yml":         true, // document_retrieval, tool_sequence
	"ai_agent_inter_agent_spoofing.yml":      true, // agent_handoff, agent_message
	"ai_agent_mcp_rug_pull.yml":              true, // tool_description*, tool_execution, tool_list_comparison
	"ai_agent_mcp_tool_poisoning.yml":        true, // tool_description, tool_response
	"ai_agent_openclaw_prompt_injection.yml": true, // tool_response, tool_sequence
	"ai_agent_prompt_injection_direct.yml":   true, // user_input
	"ai_agent_prompt_injection_exfil.yml":    true, // output_generation
	"ai_agent_prompt_injection_indirect.yml": true, // content_retrieval
	"ai_agent_rag_image_exfiltration.yml":    true, // llm_response
	"ai_agent_urgency_manipulation.yml":      true, // content_retrieval, tool_response, user_input
}

// corpusRuleFiles lists every YAML file in the vendored rules directory.
func corpusRuleFiles(t *testing.T, rulesDir string) []string {
	t.Helper()

	var files []string
	err := filepath.WalkDir(rulesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext == ".yml" || ext == ".yaml" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking rules directory: %v", err)
	}
	sort.Strings(files)
	return files
}

// TestCorpusAllRulesLoad fails when any rule file in the vendored corpus does
// not parse and load. Previously a broken rule only produced a startup WARN
// and was silently skipped, so it could pass the sigma-ai sync check while
// providing zero detection coverage (issue #71).
func TestCorpusAllRulesLoad(t *testing.T) {
	rulesDir := projectRulesDir()
	if rulesDir == "" {
		t.Skip("project rules directory not found — skipping integration test")
	}

	eng, err := NewEngine(rulesDir)
	if err != nil {
		t.Fatalf("creating engine: %v", err)
	}

	files := corpusRuleFiles(t, rulesDir)
	if len(files) == 0 {
		t.Fatal("no rule files found in corpus")
	}

	failed := 0
	for _, path := range files {
		if _, err := eng.loadRuleFile(path); err != nil {
			failed++
			t.Errorf("rule fails to load: %s: %v", filepath.Base(path), err)
		}
	}

	loaded := len(eng.GetLoadedRules())
	if loaded != len(files)-failed {
		t.Errorf("engine loaded %d rules but corpus has %d loadable files (of %d total)",
			loaded, len(files)-failed, len(files))
	}
	t.Logf("corpus: %d rule files, %d loaded", len(files), loaded)
}

// TestCorpusEventTypeReachability fails when a rule outside the known
// allowlist gates exclusively on event_type values that no shipping event
// source emits — such a rule silently provides no production coverage.
func TestCorpusEventTypeReachability(t *testing.T) {
	rulesDir := projectRulesDir()
	if rulesDir == "" {
		t.Skip("project rules directory not found — skipping integration test")
	}

	for _, path := range corpusRuleFiles(t, rulesDir) {
		base := filepath.Base(path)

		content, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("reading %s: %v", base, err)
			continue
		}
		rule, err := sigmalite.ParseRule(content)
		if err != nil {
			// Load failures are TestCorpusAllRulesLoad's job.
			continue
		}
		if rule.Detection == nil || rule.Detection.Expr == nil {
			continue
		}

		var referenced []string
		reachable := false
		for _, atom := range extractSearchAtoms(rule.Detection.Expr) {
			if atom.Field != "event_type" {
				continue
			}
			for _, pattern := range atom.Patterns {
				referenced = append(referenced, pattern)
				// Wildcard patterns can match emitted values; treat as reachable.
				if strings.Contains(pattern, "*") || liveEventTypes[strings.ToLower(pattern)] {
					reachable = true
				}
			}
		}

		if len(referenced) == 0 {
			// Rule does not gate on event_type (e.g. session-behavioural
			// rules matching only on session.* fields) — reachable.
			continue
		}

		switch {
		case !reachable && !knownUnreachableRules[base]:
			t.Errorf("%s gates only on un-emitted event types %v — no shipping connector can trigger it; "+
				"key it on an emitted event type or wire the new type through a connector first",
				base, referenced)
		case reachable && knownUnreachableRules[base]:
			t.Logf("%s is now reachable — remove it from knownUnreachableRules", base)
		}
	}
}
