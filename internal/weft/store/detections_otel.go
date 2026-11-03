package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode"
)

// AgentShield's engine exports an `evaluate` span per verdict (instrumentation
// scope `agentshield`, docs/weft/DESIGN.md §5.1). Ingest keeps it as an
// ordinary span and also stores it as a detection, keyed by the engine's
// event id (attribute event.id) so a verdict that is also pushed to
// /api/v1/shield/ingest is stored once.

// Replay evaluations. Weft's remote replay (internal/weft/shield) sends each
// re-evaluation of a recorded tool call to the engine with replay: true, an
// event id "weft-replay-<job>-<n>" and a session id "weft-replay:<job>:<id>".
// An engine that exports its evaluate spans to Weft reports these back. They
// are not live traffic: they never become detections, sessions or agent
// activity (the replay job stores its own results as source=replay
// detections). The engine reserves the namespace for replay: it rejects a
// live request with a namespaced session id or a replay event id, and a
// replay without the namespaced session id (models.CheckReplayNamespace).
const (
	ReplayEventPrefix   = "weft-replay-"
	ReplaySessionPrefix = "weft-replay:"
	// AttrReplay is the attribute engines put on replayed evaluate spans.
	AttrReplay = "agentshield.replay"
)

// InReplayNamespace reports whether an event or session id is in the replay
// namespace. Engines never push replays, so /api/v1/shield/ingest rejects
// these ids rather than storing or silently dropping them.
func InReplayNamespace(eventID, sessionID string) bool {
	// Ingest trims ids, so check the trimmed form: " weft-replay-x" would
	// otherwise be stored inside the namespace.
	e := strings.TrimLeftFunc(eventID, unicode.IsSpace)
	s := strings.TrimLeftFunc(sessionID, unicode.IsSpace)
	return strings.HasPrefix(e, ReplayEventPrefix) || strings.HasPrefix(s, ReplaySessionPrefix)
}

func init() {
	// replay_spans lists the engine evaluate spans of replayed tool calls
	// (indexReplaySpan). A trace made of them (and the engine's HTTP span)
	// is a replay trace, which the Traces list hides by default through the
	// summary.weft.replay call field (replayTraceSQL).
	registerSchema(`
CREATE TABLE IF NOT EXISTS replay_spans (
	project_id TEXT NOT NULL,
	span_id TEXT NOT NULL,
	trace_id TEXT NOT NULL,
	PRIMARY KEY (project_id, span_id)
);
CREATE INDEX IF NOT EXISTS replay_spans_trace ON replay_spans(project_id, trace_id);
`)
	registerCallHook(indexReplaySpan)
	registerMigration(func(db *sql.DB) error {
		return runOnce(db, "replay-spans-1", func(tx *sql.Tx) error {
			// replay_traces (before replay_spans) marked a whole trace on
			// any span claiming to be a replay; it is rebuilt as replay_spans.
			if _, err := tx.Exec(`DROP TABLE IF EXISTS replay_traces`); err != nil {
				return err
			}
			rows, err := tx.Query(`SELECT project_id, id FROM calls WHERE instr(attributes, 'agentshield') > 0
				AND (instr(attributes, 'weft-replay') > 0 OR instr(attributes, 'agentshield.replay') > 0)`)
			if err != nil {
				return err
			}
			var keys [][2]string
			for rows.Next() {
				var p, id string
				if err := rows.Scan(&p, &id); err != nil {
					rows.Close()
					return err
				}
				keys = append(keys, [2]string{p, id})
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			for _, k := range keys {
				if err := indexReplaySpan(context.Background(), tx, k[0], k[1]); err != nil {
					return err
				}
			}
			return nil
		})
	})
	// Databases written before replay evaluations were recognised may hold
	// engine detections and phantom sessions for them; drop both once.
	registerMigration(func(db *sql.DB) error {
		return runOnce(db, "replay-evaluations-1", func(tx *sql.Tx) error {
			if _, err := tx.Exec(`DELETE FROM detection_rules WHERE (project_id, detection_id) IN (SELECT project_id, id FROM detections
				WHERE source = 'engine' AND substr(session_id, 1, 12) = 'weft-replay:')`); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM detections WHERE source = 'engine' AND substr(session_id, 1, 12) = 'weft-replay:'`); err != nil {
				return err
			}
			_, err := tx.Exec(`UPDATE agent_spans SET conversation_id = '', conversation_name = '', own_conversation = 0, is_turn = 0,
					agent_name = '', agent_id = '', agent_version = '', agent_description = '', own_agent = 0
				WHERE substr(conversation_id, 1, 12) = 'weft-replay:'`)
			return err
		})
	})
}

// EngineHTTPScope is the instrumentation scope of the engine's HTTP server
// spans (otelchi), the root of the trace of each evaluation it serves.
const EngineHTTPScope = "github.com/riandyrn/otelchi"

// replayTraceSQL is 1 when call c belongs to a replay trace: one that holds
// a replayed evaluate span (replay_spans) and nothing else but, as its root,
// the engine's HTTP span. Anyone who can write OTLP to a project can send a
// span claiming to be a replay with any trace id, so a trace with any other
// span (agent activity, a live evaluate span, anything not the engine's
// HTTP root) is never a replay trace: such a span cannot hide it from the
// Traces list. The claiming span itself stays a visible span of that trace.
// A replay span whose HTTP root has not arrived yet already marks its trace
// (callsquery.go, field summary.weft.replay).
const replayTraceSQL = `(CASE WHEN EXISTS (SELECT 1 FROM replay_spans rs WHERE rs.project_id = c.project_id AND rs.trace_id = c.trace_id)
	AND NOT EXISTS (SELECT 1 FROM calls ro WHERE ro.project_id = c.project_id AND ro.trace_id = c.trace_id AND ro.deleted_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM replay_spans rs2 WHERE rs2.project_id = ro.project_id AND rs2.span_id = ro.id)
		AND NOT (ro.parent_id IS NULL AND COALESCE(json_extract(ro.attributes, '$.otel.scope.name'), '') = '` + EngineHTTPScope + `'))
	THEN 1 ELSE 0 END)`

// indexReplaySpan (a call hook) records a replayed evaluate span: a span of
// the engine's instrumentation scope (agentshield) that isReplaySpan
// accepts. A span without that scope claiming to be a replay (any OTLP
// writer can add agentshield.replay) is not recorded.
func indexReplaySpan(ctx context.Context, tx *sql.Tx, projectID, id string) error {
	var traceID, attrs string
	err := tx.QueryRowContext(ctx, `SELECT trace_id, attributes FROM calls WHERE project_id = ? AND id = ?`, projectID, id).Scan(&traceID, &attrs)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	replay := false
	if traceID != "" && strings.Contains(attrs, "agentshield") {
		am := decodeMap(attrs)
		sc, _ := asMap(am["otel"])["scope"].(map[string]any)
		replay = sc != nil && sc["name"] == "agentshield" && isReplaySpan(newSpanAttrs(am))
	}
	if !replay {
		_, err = tx.ExecContext(ctx, `DELETE FROM replay_spans WHERE project_id = ? AND span_id = ?`, projectID, id)
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO replay_spans(project_id, span_id, trace_id) VALUES(?, ?, ?)
		ON CONFLICT(project_id, span_id) DO UPDATE SET trace_id = excluded.trace_id`, projectID, id, traceID)
	return err
}

// isReplaySpan reports an engine evaluate span of a replayed tool call: one
// tagged agentshield.replay = true. Engines older than that attribute cannot
// set it, so a span whose session id is in the replay namespace counts too;
// current engines accept that session id only on a replay request (with the
// attribute), so the fallback cannot hide their live traffic. The event id
// prefix alone is not enough: it is no evidence of a replay, and an older
// engine would accept it on a live call.
func isReplaySpan(a spanAttrs) bool {
	if a.truthy(AttrReplay) {
		return true
	}
	// Engines older than the replay attribute: the namespaced session id.
	// Compare the raw value exactly as the engine does; trimming here would
	// let a padded live id (" weft-replay:x", which the engine treats as
	// live) be dropped as a replay.
	for _, k := range []string{"gen_ai.conversation.id", "session.id"} {
		if v, ok := a.get(k).(string); ok && v != "" {
			return strings.HasPrefix(v, ReplaySessionPrefix)
		}
	}
	return false
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
