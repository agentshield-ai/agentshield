package replay

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// benchTestcase matches the YAML structure used by agentshieldbench.
type benchTestcase struct {
	ID        string        `yaml:"id"`
	Severity  string        `yaml:"severity"`
	SessionID string        `yaml:"session_id,omitempty"`
	Triage    bool          `yaml:"triage"`
	Benign    bool          `yaml:"benign"`
	Rationale string        `yaml:"rationale,omitempty"`
	Events    []benchEvent  `yaml:"events"`
	Expected  benchExpected `yaml:"expected"`
}

type benchEvent struct {
	EventID string            `yaml:"event_id"`
	Tool    string            `yaml:"tool"`
	Args    map[string]string `yaml:"args"`
	Fields  map[string]string `yaml:"fields"`
}

type benchExpected struct {
	Action           string   `yaml:"action"`
	MustTriggerRules []string `yaml:"must_trigger_rules,omitempty"`
}

// ExportTestcases writes interesting replay results as bench YAML testcases.
// Only results with alerts (rule matches) are exported.
func ExportTestcases(results []ReplayResult, outputDir string, datasetSlug string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}

	exported := 0
	for _, result := range results {
		if len(result.Alerts) == 0 {
			continue
		}

		slug := sanitizeSlug(datasetSlug)
		id := fmt.Sprintf("hf_%s_%d_%d", slug, result.TraceIndex, result.EventIndex)

		// Determine severity from highest-severity alert
		severity := "medium"
		for _, alert := range result.Alerts {
			if string(alert.Severity) == "critical" {
				severity = "critical"
				break
			}
			if string(alert.Severity) == "high" {
				severity = "high"
			}
		}

		// Collect triggered rule IDs
		var ruleIDs []string
		var ruleNames []string
		for _, alert := range result.Alerts {
			ruleIDs = append(ruleIDs, alert.RuleID)
			ruleNames = append(ruleNames, alert.RuleName)
		}

		tc := benchTestcase{
			ID:        id,
			Severity:  severity,
			SessionID: result.SessionID,
			Triage:    false,
			Benign:    false,
			Rationale: fmt.Sprintf("Extracted from HuggingFace dataset %s (trace %d, event %d). Triggered: %s",
				datasetSlug, result.TraceIndex, result.EventIndex, strings.Join(ruleNames, ", ")),
			Events: []benchEvent{
				{
					EventID: fmt.Sprintf("evt-%s", id),
					Tool:    result.ToolName,
					Args:    map[string]string{"command": result.Command},
					Fields: map[string]string{
						"event_type": "tool_call",
						"command":    result.Command,
					},
				},
			},
			Expected: benchExpected{
				Action:           strings.ToUpper(result.Action),
				MustTriggerRules: ruleIDs,
			},
		}

		filename := filepath.Join(outputDir, id+".yaml")
		data, err := yaml.Marshal(tc)
		if err != nil {
			slog.Warn("Failed to marshal testcase", "id", id, "error", err)
			continue
		}
		if err := os.WriteFile(filename, data, 0o644); err != nil {
			slog.Warn("Failed to write testcase", "path", filename, "error", err)
			continue
		}
		exported++
	}

	slog.Info("Exported testcases", "count", exported, "dir", outputDir)
	return nil
}

// sanitizeSlug converts a dataset name to a filesystem-safe slug.
func sanitizeSlug(s string) string {
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, s)
	return s
}
