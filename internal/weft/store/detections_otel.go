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

// Replay evaluations. Weft's remote replay (internal/weft/shield) sends each
// re-evaluation of a recorded tool call to the engine with replay: true, an
// event id "weft-replay-<job>-<n>" and a session id "weft-replay:<job>:<id>".
// An engine that exports its evaluate spans to Weft, or pushes its verdicts,
// reports these back. They are not live traffic: they never become
// detections, sessions or agent activity (the replay job stores its own
// results as source=replay detections).
const (
	ReplayEventPrefix   = "weft-replay-"
	ReplaySessionPrefix = "weft-replay:"
	// AttrReplay is the attribute engines put on replayed evaluate spans.
	AttrReplay = "agentshield.replay"
)

// IsReplayEvaluation reports whether an engine verdict came from a replay.
func IsReplayEvaluation(eventID, sessionID string) bool {
	return strings.HasPrefix(eventID, ReplayEventPrefix) || strings.HasPrefix(sessionID, ReplaySessionPrefix)
}

func init() {
	// Databases written before replay evaluations were recognised may hold
	// engine detections and phantom sessions for them; drop both once.
	registerMigration(func(db *sql.DB) error {
		return runOnce(db, "replay-evaluations-1", func(tx *sql.Tx) error {
			if _, err := tx.Exec(`DELETE FROM detection_rules WHERE (project_id, detection_id) IN (SELECT project_id, id FROM detections
				WHERE source = 'engine' AND (substr(event_id, 1, 12) = 'weft-replay-' OR substr(session_id, 1, 12) = 'weft-replay:'))`); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM detections WHERE source = 'engine'
				AND (substr(event_id, 1, 12) = 'weft-replay-' OR substr(session_id, 1, 12) = 'weft-replay:')`); err != nil {
				return err
			}
			_, err := tx.Exec(`UPDATE agent_spans SET conversation_id = '', conversation_name = '', own_conversation = 0, is_turn = 0,
					agent_name = '', agent_id = '', agent_version = '', agent_description = '', own_agent = 0
				WHERE substr(conversation_id, 1, 12) = 'weft-replay:'`)
			return err
		})
	})
}

// isReplaySpan reports an engine evaluate span of a replayed tool call.
func isReplaySpan(a spanAttrs) bool {
	return a.truthy(AttrReplay) ||
		IsReplayEvaluation(a.str("event.id", "agentshield.event.id"), a.str("gen_ai.conversation.id", "session.id"))
}

// DetectionFromSpan derives a detection from a stored call's attributes, or
// reports false when the call is not an AgentShield verdict span: a span of
// instrumentation scope `agentshield` with a verdict action, or any span with
// agentshield.verdict.action. Replayed evaluations are not detections.
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
	if action == "" || isReplaySpan(newSpanAttrs(attributes)) {
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
