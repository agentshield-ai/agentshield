package replay

import (
	"encoding/json"
	"os"

	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"
)

// defaultDumpLimit bounds --dump-fields output when no limit is given.
const defaultDumpLimit = 200

// fieldDumpRecord is one line of the field dump.
type fieldDumpRecord struct {
	TraceIndex int    `json:"trace_index"`
	EventIndex int    `json:"event_index"`
	TraceID    string `json:"trace_id,omitempty"`
	Malicious  bool   `json:"malicious"`
	RiskSource string `json:"risk_source,omitempty"`
	Kind       string `json:"kind"`
	Tool       string `json:"tool"`
	// Fields is what the rule engine actually saw.
	Fields map[string]string `json:"fields"`
	// ProdFields is the production-shaped subset, absent when the event has no
	// production analogue.
	ProdFields   map[string]string `json:"production_fields,omitempty"`
	MatchedRules []string          `json:"matched_rules,omitempty"`
	ProdMatched  []string          `json:"production_matched_rules,omitempty"`
}

// fieldDumper writes a bounded JSONL record of the field maps fed to the engine.
type fieldDumper struct {
	enc     *json.Encoder
	file    *os.File
	written int
	limit   int
	// focus keeps the dump to the events worth reading: everything in a
	// malicious trace, plus anything that alerted regardless of label, so
	// false positives can be triaged from the same file.
	focus bool
}

func newFieldDumper(path string, limit int) (*fieldDumper, error) {
	if path == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultDumpLimit
	}
	f, err := os.Create(path) //nolint:gosec // operator-supplied debug output path
	if err != nil {
		return nil, err
	}
	return &fieldDumper{enc: json.NewEncoder(f), file: f, limit: limit, focus: true}, nil
}

func (d *fieldDumper) Write(result ReplayResult, req, prodReq *models.EvaluationRequest) {
	if d == nil || d.written >= d.limit {
		return
	}
	if d.focus && result.Label.Present && !result.Label.Malicious && len(result.Alerts) == 0 {
		return
	}
	rec := fieldDumpRecord{
		TraceIndex:   result.TraceIndex,
		EventIndex:   result.EventIndex,
		TraceID:      result.Label.TraceID,
		Malicious:    result.Label.Malicious,
		RiskSource:   result.Label.RiskSource,
		Kind:         string(result.Kind),
		Tool:         result.ToolName,
		MatchedRules: ruleNames(result.Alerts),
		ProdMatched:  ruleNames(result.ProdAlerts),
	}
	if req != nil {
		rec.Fields = req.Fields
	}
	if prodReq != nil {
		rec.ProdFields = prodReq.Fields
	}
	_ = d.enc.Encode(rec)
	d.written++
}

func (d *fieldDumper) Close() {
	if d == nil || d.file == nil {
		return
	}
	_ = d.file.Close()
}

func ruleNames(alerts []engine.RuleResult) []string {
	if len(alerts) == 0 {
		return nil
	}
	names := make([]string, 0, len(alerts))
	for _, a := range alerts {
		names = append(names, a.RuleName)
	}
	return names
}
