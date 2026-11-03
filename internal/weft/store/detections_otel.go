package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// AgentShield's engine exports an `evaluate` span per verdict (instrumentation
// scope `agentshield`, docs/weft/DESIGN.md §5.1). Ingest keeps it as an
// ordinary span and also stores it as a detection, keyed by the engine's
// event id (attribute event.id) so a verdict that is also pushed to
// /api/v1/shield/ingest is stored once.

// DetectionFromSpan derives a detection from a stored call's attributes, or
// reports false when the call is not an AgentShield verdict span: a span of
// instrumentation scope `agentshield` with a verdict action, or any span with
// agentshield.verdict.action.
func DetectionFromSpan(callID string, startedAt int64, attributes map[string]any) (DetectionInput, bool) {
	o, _ := attributes["otel"].(map[string]any)
	attrs, _ := o["attributes"].(map[string]any)
	if attrs == nil {
		return DetectionInput{}, false
	}
	scope, _ := o["scope"].(map[string]any)
	scopeName, _ := scope["name"].(string)
	action := detAttrStr(attrs, "agentshield.verdict.action")
	if action == "" && scopeName == "agentshield" {
		action = detAttrStr(attrs, "verdict.action") // engines before §5.1
	}
	if action == "" {
		return DetectionInput{}, false
	}
	d := DetectionInput{
		EventID:      detAttrStr(attrs, "event.id", "agentshield.event.id"),
		SessionID:    detAttrStr(attrs, "gen_ai.conversation.id", "session.id"),
		ToolCallID:   detAttrStr(attrs, "gen_ai.tool.call.id"),
		ToolName:     detAttrStr(attrs, "gen_ai.tool.name", "tool.name"),
		AgentName:    detAttrStr(attrs, "gen_ai.agent.name"),
		Action:       action,
		Mode:         detAttrStr(attrs, "agentshield.verdict.mode", "verdict.mode"),
		Severity:     detAttrStr(attrs, "agentshield.verdict.severity"),
		Source:       DetectionSourceEngine,
		EventSource:  detAttrStr(attrs, "source", "agentshield.source"),
		EngineSpanID: callID,
		CreatedAt:    startedAt,
	}
	if d.EventID == "" {
		d.EventID = "span:" + callID
	}
	events, _ := o["events"].([]any)
	var legacy []DetectionRule
	for _, e := range events {
		em, _ := e.(map[string]any)
		name, _ := em["name"].(string)
		ea, _ := em["attributes"].(map[string]any)
		switch name {
		case "agentshield.rule.matched":
			d.Rules = append(d.Rules, DetectionRule{ID: detAttrStr(ea, "rule.id"), Title: detAttrStr(ea, "rule.title", "rule.name"),
				Level: detAttrStr(ea, "rule.level", "rule.severity"), Tags: detAttrStrings(ea["rule.tags"])})
		case "rule.matched":
			legacy = append(legacy, DetectionRule{ID: detAttrStr(ea, "rule.id"), Title: detAttrStr(ea, "rule.name"), Level: detAttrStr(ea, "rule.severity")})
		}
	}
	if len(d.Rules) == 0 {
		d.Rules = legacy
	}
	if len(d.Rules) == 0 {
		for _, id := range detAttrStrings(attrs["agentshield.rule_ids"]) {
			d.Rules = append(d.Rules, DetectionRule{ID: id})
		}
	}
	kept := d.Rules[:0]
	for _, r := range d.Rules {
		if strings.TrimSpace(r.ID) != "" {
			kept = append(kept, r)
		}
	}
	d.Rules = kept
	return d, true
}

func detAttrStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case nil:
		default:
			if s := strings.TrimSpace(fmt.Sprint(v)); s != "" {
				return s
			}
		}
	}
	return ""
}

func detAttrStrings(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case string:
		if t != "" {
			return []string{t}
		}
	}
	return nil
}

// detectionFromSpanTx stores a detection for an AgentShield verdict span.
// Malformed verdict spans are kept as spans and skipped as detections.
func detectionFromSpanTx(ctx context.Context, tx *sql.Tx, projectID, id string) error {
	var attrs string
	var startedAt sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT attributes, started_at FROM calls WHERE project_id = ? AND id = ?
		AND (instr(attributes, 'agentshield.verdict.action') > 0 OR instr(attributes, '"agentshield"') > 0)`, projectID, id).Scan(&attrs, &startedAt)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	d, ok := DetectionFromSpan(id, startedAt.Int64, decodeMap(attrs))
	if !ok {
		return nil
	}
	if err := d.Normalize(); err != nil {
		return nil
	}
	_, err = upsertDetectionTx(ctx, tx, projectID, &d)
	return err
}
