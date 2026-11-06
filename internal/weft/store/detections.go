package store

// Detections: AgentShield verdicts attached to the tool calls they concern
// (docs/weft/DESIGN.md §2 Detection row, §5).
//
// A detection arrives three ways: pushed by the engine to
// /api/v1/shield/ingest, derived from the engine's `evaluate` span over OTLP
// (detections_otel.go), or produced by a replay (source=replay). Engine
// detections are unique per (project, source, event_id), where event_id is
// the engine's evaluation event id, so the push and the OTLP span of one
// verdict merge into one row and re-sending is idempotent.
//
// Correlation (§5.3) attaches a detection to a tool span (an agent_spans row
// with operation execute_tool):
//
//  1. the span with the same gen_ai.tool.call.id in the same session;
//  2. failing that, the span in the same session with the same tool name
//     whose start is nearest to the verdict time, within ±5 s;
//  3. failing that, the detection stays attached to the session only.
//
// Detections that arrive before their span are linked from the call hook
// when the span (or the ancestor that gives it its session) arrives, and a
// time-based link is upgraded when the span with the exact tool call id
// arrives later.

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/google/uuid"
)

func init() {
	registerSchema(`
CREATE TABLE IF NOT EXISTS detections (
	project_id TEXT NOT NULL,
	id TEXT NOT NULL,
	event_id TEXT NOT NULL,
	source TEXT NOT NULL DEFAULT 'engine',
	ruleset TEXT NOT NULL DEFAULT '',
	session_id TEXT NOT NULL DEFAULT '',
	span_id TEXT,
	trace_id TEXT,
	link TEXT NOT NULL DEFAULT '',
	engine_span_id TEXT,
	tool_call_id TEXT NOT NULL DEFAULT '',
	tool_name TEXT NOT NULL DEFAULT '',
	agent_name TEXT NOT NULL DEFAULT '',
	action TEXT NOT NULL,
	mode TEXT NOT NULL DEFAULT '',
	severity TEXT NOT NULL DEFAULT '',
	severity_rank INTEGER NOT NULL DEFAULT 0,
	rules TEXT NOT NULL DEFAULT '[]',
	event_source TEXT NOT NULL DEFAULT '',
	overridden INTEGER NOT NULL DEFAULT 0,
	overridden_at INTEGER,
	replay_job_id TEXT,
	created_at INTEGER NOT NULL,
	ingested_at INTEGER NOT NULL,
	PRIMARY KEY (project_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS detections_event ON detections(project_id, source, event_id);
CREATE INDEX IF NOT EXISTS detections_created ON detections(project_id, created_at);
CREATE INDEX IF NOT EXISTS detections_session ON detections(project_id, session_id, created_at);
CREATE INDEX IF NOT EXISTS detections_span ON detections(project_id, span_id);
CREATE INDEX IF NOT EXISTS detections_agent ON detections(project_id, agent_name, created_at);
CREATE INDEX IF NOT EXISTS detections_tool_call ON detections(project_id, tool_call_id) WHERE tool_call_id <> '';
CREATE INDEX IF NOT EXISTS detections_unlinked ON detections(project_id, session_id) WHERE span_id IS NULL;
CREATE TABLE IF NOT EXISTS detection_rules (
	project_id TEXT NOT NULL,
	detection_id TEXT NOT NULL,
	rule_id TEXT NOT NULL,
	title TEXT NOT NULL DEFAULT '',
	level TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (project_id, detection_id, rule_id)
);
CREATE INDEX IF NOT EXISTS detection_rules_rule ON detection_rules(project_id, rule_id);
CREATE TABLE IF NOT EXISTS detection_overrides_pending (
	project_id TEXT NOT NULL,
	event_id TEXT NOT NULL,
	session_id TEXT NOT NULL DEFAULT '',
	at INTEGER NOT NULL,
	PRIMARY KEY (project_id, event_id)
);
CREATE INDEX IF NOT EXISTS agent_spans_tool_call ON agent_spans(project_id, tool_call_id) WHERE tool_call_id <> '';
`)
	registerCallHook(detectionCallHook)
}

// Detection actions, modes and sources.
const (
	DetectionAllow           = "allow"
	DetectionLog             = "log"
	DetectionRequireApproval = "require_approval"
	DetectionBlock           = "block"

	DetectionSourceEngine = "engine"
	DetectionSourceReplay = "replay"

	// Link methods stored on a linked detection.
	LinkToolCallID  = "tool_call_id"
	LinkNearestTime = "nearest_time"
	LinkReplay      = "replay"

	// CorrelationWindow is the ±window of the nearest-time fallback.
	CorrelationWindow = 5 * time.Second
)

var detectionActions = map[string]bool{DetectionAllow: true, DetectionLog: true, DetectionRequireApproval: true, DetectionBlock: true}
var detectionModes = map[string]bool{"": true, "enforce": true, "audit": true, "shadow": true}

// SeverityRank orders severities: none 0, low 1, medium 2, high 3, critical 4.
func SeverityRank(s string) int {
	switch s {
	case "low":
		return 1
	case "medium":
		return 2
	case "high":
		return 3
	case "critical":
		return 4
	}
	return 0
}

func normSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none":
		return ""
	case "informational", "info", "low":
		return "low"
	case "medium", "med":
		return "medium"
	case "high":
		return "high"
	case "critical", "crit":
		return "critical"
	}
	return "unknown"
}

// DetectionRule is one matched rule.
type DetectionRule struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Level string   `json:"level"`
	Tags  []string `json:"tags"`
}

// Detection is a stored verdict. Link says how span_id was found:
// tool_call_id, nearest_time or replay; session when it is attached to its
// session only, none when it has no session either.
type Detection struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	EventID   string `json:"event_id"`
	SessionID string `json:"session_id"`
	// SessionTitle labels the session in lists (its name, else its first
	// user request); filled by /api/v1/detections, empty elsewhere.
	SessionTitle string          `json:"session_title,omitempty"`
	SpanID       *string         `json:"span_id"`
	TraceID      *string         `json:"trace_id"`
	Link         string          `json:"link"`
	EngineSpanID *string         `json:"engine_span_id"`
	ToolCallID   string          `json:"tool_call_id"`
	ToolName     string          `json:"tool_name"`
	AgentName    string          `json:"agent_name"`
	Action       string          `json:"action"`
	Mode         string          `json:"mode"`
	Severity     string          `json:"severity"`
	Rules        []DetectionRule `json:"rules"`
	Source       string          `json:"source"`
	Ruleset      string          `json:"ruleset"`
	EventSource  string          `json:"event_source"`
	Overridden   bool            `json:"overridden"`
	OverriddenAt *string         `json:"overridden_at"`
	ReplayJobID  *string         `json:"replay_job_id"`
	CreatedAt    string          `json:"created_at"`
	IngestedAt   string          `json:"ingested_at"`
}

// DetectionInput is a detection to store.
type DetectionInput struct {
	EventID     string
	SessionID   string
	ToolCallID  string
	ToolName    string
	AgentName   string
	Action      string
	Mode        string
	Severity    string // derived from the rules when empty
	Rules       []DetectionRule
	Source      string // engine (default) or replay
	Ruleset     string
	EventSource string // the platform that sent the tool call (openclaw, claude-code, ...)
	// SpanID and TraceID name the evaluated tool span directly (replay).
	SpanID, TraceID string
	// EngineSpanID is the engine's evaluate span when it came over OTLP.
	EngineSpanID string
	ReplayJobID  string
	Overridden   bool
	CreatedAt    int64 // verdict time, unix micros; 0 means now
}

// DetectionUpsert reports one stored detection.
type DetectionUpsert struct {
	ID      string `json:"id"`
	EventID string `json:"event_id"`
	Created bool   `json:"created"`
}

func detCheckLen(field, v string, max int) error {
	if len(v) > max {
		return Invalidf("%s is longer than %d bytes", field, max)
	}
	if strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return Invalidf("%s contains control characters", field)
	}
	return nil
}

// detClean makes a descriptive field (a name or label, never an id) safe to
// store instead of rejecting it: control characters become spaces, runs of
// whitespace collapse, and the value is cut to max bytes on a rune boundary
// (models.CleanText, which the engine applies too). The engine accepts such
// values on live verdicts, so rejecting them here would drop a real block.
func detClean(v string, max int) string {
	return models.CleanText(v, max)
}

// Normalize validates a detection and fills defaults. Identifiers are
// checked strictly; descriptive fields are cleaned (detClean).
func (d *DetectionInput) Normalize() error {
	d.ToolName = detClean(d.ToolName, 256)
	d.AgentName = detClean(d.AgentName, 256)
	d.EventSource = detClean(d.EventSource, 128)
	d.Ruleset = detClean(d.Ruleset, 256)
	d.Severity = detClean(d.Severity, 32)
	d.EventID = strings.TrimSpace(d.EventID)
	if d.EventID == "" {
		return Invalidf("event_id is required")
	}
	if d.Source == "" {
		d.Source = DetectionSourceEngine
	}
	if d.Source != DetectionSourceEngine && d.Source != DetectionSourceReplay {
		return Invalidf("source must be engine or replay")
	}
	d.Action = strings.ToLower(strings.TrimSpace(d.Action))
	if !detectionActions[d.Action] {
		return Invalidf("action must be allow, log, require_approval or block")
	}
	d.Mode = strings.ToLower(strings.TrimSpace(d.Mode))
	if !detectionModes[d.Mode] {
		return Invalidf("mode must be enforce, audit or shadow")
	}
	type idField struct {
		name, v string
		max     int
	}
	fields := []idField{
		{"event_id", d.EventID, 512}, {"tool_name", d.ToolName, 256}, {"agent_name", d.AgentName, 256}, {"ruleset", d.Ruleset, 256},
		{"event_source", d.EventSource, 128}, {"span_id", d.SpanID, 256}, {"trace_id", d.TraceID, 256},
		{"engine_span_id", d.EngineSpanID, 256}, {"severity", d.Severity, 32},
	}
	if d.Source != DetectionSourceReplay {
		fields = append(fields, idField{"session_id", d.SessionID, 256}, idField{"tool_call_id", d.ToolCallID, 256})
	}
	// A replay detection (only Weft's replay produces them: ingest accepts
	// source=engine only) names a recorded session and tool call by the ids
	// Weft stored for them, whatever length or characters those have (OTLP
	// accepts any string), so it is stored and attached to them as they are.
	for _, f := range fields {
		if err := detCheckLen(f.name, f.v, f.max); err != nil {
			return err
		}
	}
	best := ""
	cleaned := make([]models.Rule, 0, len(d.Rules))
	for _, r := range d.Rules {
		// Rule ids, titles and tags come from the engine's rule files, which
		// it loads whatever they hold: clean them rather than reject the
		// verdict (an empty or whitespace-only id gets a stable generated
		// id, an over-long or control-character one a stable prefix + hash
		// stand-in, models.CleanRuleID; tags are cleaned and bounded). The
		// engine applies the same functions, and the same selection
		// (models.SelectRules: a repeated id keeps its most severe entry,
		// the most severe first, at most models.MaxRules), so the pushed and
		// the OTLP copy of a verdict, and a replay of it, store the same
		// rules.
		r.ID = models.CleanRuleID(r.ID, r.Title)
		r.Title = detClean(r.Title, models.MaxRuleTitleLength)
		r.Level = normSeverity(r.Level)
		if r.Level == "unknown" {
			return Invalidf("rule level must be low, medium, high or critical")
		}
		r.Tags = models.CleanTags(r.Tags)
		if SeverityRank(r.Level) > SeverityRank(best) {
			best = r.Level
		}
		cleaned = append(cleaned, models.Rule{ID: r.ID, Title: r.Title, Level: r.Level, Tags: r.Tags})
	}
	// The detection's severity still counts every rule.
	rules := make([]DetectionRule, 0, min(len(cleaned), models.MaxRules))
	for _, r := range models.SelectRules(cleaned) {
		rules = append(rules, DetectionRule{ID: r.ID, Title: r.Title, Level: r.Level, Tags: r.Tags})
	}
	d.Rules = rules
	sev := normSeverity(d.Severity)
	if sev == "unknown" {
		return Invalidf("severity must be none, low, medium, high or critical")
	}
	if sev == "" {
		sev = best
	}
	if sev == "" {
		sev = "none"
	}
	d.Severity = sev
	if d.CreatedAt == 0 {
		d.CreatedAt = nowMicros()
	}
	return nil
}

// UpsertDetections stores detections (idempotent per source and event_id),
// links them to tool spans and applies pending overrides.
func (s *Store) UpsertDetections(ctx context.Context, projectID string, in []DetectionInput) ([]DetectionUpsert, error) {
	for i := range in {
		if err := in[i].Normalize(); err != nil {
			return nil, err
		}
	}
	out := make([]DetectionUpsert, 0, len(in))
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.touchProject(ctx, tx, projectID); err != nil {
			return err
		}
		for i := range in {
			r, err := upsertDetectionTx(ctx, tx, projectID, &in[i])
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

type detDB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// upsertDetectionTx stores one normalized detection.
func upsertDetectionTx(ctx context.Context, tx detDB, projectID string, d *DetectionInput) (DetectionUpsert, error) {
	rulesJSON, _ := json.Marshal(d.Rules)
	now := nowMicros()
	var id string
	var oldRules string
	err := tx.QueryRowContext(ctx, `SELECT id, rules FROM detections WHERE project_id = ? AND source = ? AND event_id = ?`,
		projectID, d.Source, d.EventID).Scan(&id, &oldRules)
	created := false
	switch {
	case err == sql.ErrNoRows:
		created = true
		id = uuid.Must(uuid.NewV7()).String()
		var overriddenAt any
		if d.Overridden {
			overriddenAt = now
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO detections(project_id, id, event_id, source, ruleset, session_id, span_id, trace_id, link,
				engine_span_id, tool_call_id, tool_name, agent_name, action, mode, severity, severity_rank, rules, event_source,
				overridden, overridden_at, replay_job_id, created_at, ingested_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			projectID, id, d.EventID, d.Source, d.Ruleset, d.SessionID, detNullIfEmpty(d.SpanID), detNullIfEmpty(d.TraceID), detLinkFor(d),
			detNullIfEmpty(d.EngineSpanID), d.ToolCallID, d.ToolName, d.AgentName, d.Action, d.Mode, d.Severity, SeverityRank(d.Severity),
			string(rulesJSON), d.EventSource, d.Overridden, overriddenAt, detNullIfEmpty(d.ReplayJobID), d.CreatedAt, now)
		if err != nil {
			return DetectionUpsert{}, err
		}
	case err != nil:
		return DetectionUpsert{}, err
	default:
		// Merge: non-empty incoming values win; a link and an override stick.
		if len(d.Rules) == 0 && oldRules != "[]" && oldRules != "" {
			rulesJSON = []byte(oldRules)
		}
		_, err = tx.ExecContext(ctx, `UPDATE detections SET
				ruleset = CASE WHEN ? <> '' THEN ? ELSE ruleset END,
				session_id = CASE WHEN ? <> '' THEN ? ELSE session_id END,
				span_id = COALESCE(?, span_id), trace_id = COALESCE(?, trace_id),
				link = CASE WHEN ? IS NOT NULL THEN ? ELSE link END,
				engine_span_id = COALESCE(?, engine_span_id),
				tool_call_id = CASE WHEN ? <> '' THEN ? ELSE tool_call_id END,
				tool_name = CASE WHEN ? <> '' THEN ? ELSE tool_name END,
				agent_name = CASE WHEN ? <> '' THEN ? ELSE agent_name END,
				action = ?, mode = CASE WHEN ? <> '' THEN ? ELSE mode END,
				severity = ?, severity_rank = ?, rules = ?,
				event_source = CASE WHEN ? <> '' THEN ? ELSE event_source END,
				overridden = MAX(overridden, ?), overridden_at = CASE WHEN overridden = 0 AND ? THEN ? ELSE overridden_at END,
				replay_job_id = COALESCE(?, replay_job_id)
			WHERE project_id = ? AND id = ?`,
			d.Ruleset, d.Ruleset, d.SessionID, d.SessionID,
			detNullIfEmpty(d.SpanID), detNullIfEmpty(d.TraceID), detNullIfEmpty(d.SpanID), LinkReplay,
			detNullIfEmpty(d.EngineSpanID), d.ToolCallID, d.ToolCallID, d.ToolName, d.ToolName, d.AgentName, d.AgentName,
			d.Action, d.Mode, d.Mode, d.Severity, SeverityRank(d.Severity), string(rulesJSON), d.EventSource, d.EventSource,
			d.Overridden, d.Overridden, now, detNullIfEmpty(d.ReplayJobID), projectID, id)
		if err != nil {
			return DetectionUpsert{}, err
		}
	}
	var rules []DetectionRule
	_ = json.Unmarshal(rulesJSON, &rules)
	if _, err := tx.ExecContext(ctx, `DELETE FROM detection_rules WHERE project_id = ? AND detection_id = ?`, projectID, id); err != nil {
		return DetectionUpsert{}, err
	}
	for _, r := range rules {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO detection_rules(project_id, detection_id, rule_id, title, level) VALUES(?,?,?,?,?)`,
			projectID, id, r.ID, r.Title, r.Level); err != nil {
			return DetectionUpsert{}, err
		}
	}
	if d.Source == DetectionSourceEngine {
		if err := applyPendingOverrideTx(ctx, tx, projectID, id, d.EventID); err != nil {
			return DetectionUpsert{}, err
		}
	}
	if err := linkDetectionTx(ctx, tx, projectID, id); err != nil {
		return DetectionUpsert{}, err
	}
	return DetectionUpsert{ID: id, EventID: d.EventID, Created: created}, nil
}

func detLinkFor(d *DetectionInput) string {
	if d.SpanID != "" {
		return LinkReplay
	}
	return ""
}

func detNullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ---------------------------------------------------------------- overrides

// DetectionOverride marks the engine detection for EventID (or, without one,
// the session's latest block or require_approval) as overridden by the user.
type DetectionOverride struct {
	EventID   string
	SessionID string
	At        int64 // unix micros; 0 means now
}

// OverrideResult counts applied and pending overrides.
type OverrideResult struct {
	Applied int `json:"overrides_applied"`
	Pending int `json:"overrides_pending"`
}

// ApplyDetectionOverrides marks detections overridden. An override whose
// detection has not arrived yet is kept and applied when it does.
func (s *Store) ApplyDetectionOverrides(ctx context.Context, projectID string, ovs []DetectionOverride) (OverrideResult, error) {
	var res OverrideResult
	for _, o := range ovs {
		if err := detCheckLen("event_id", o.EventID, 512); err != nil {
			return res, err
		}
		if err := detCheckLen("session_id", o.SessionID, 256); err != nil {
			return res, err
		}
		if o.EventID == "" && o.SessionID == "" {
			return res, Invalidf("an override needs event_id or session_id")
		}
	}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.touchProject(ctx, tx, projectID); err != nil {
			return err
		}
		for _, o := range ovs {
			at := o.At
			if at == 0 {
				at = nowMicros()
			}
			var r sql.Result
			var err error
			if o.EventID != "" {
				r, err = tx.ExecContext(ctx, `UPDATE detections SET overridden = 1, overridden_at = COALESCE(overridden_at, ?)
					WHERE project_id = ? AND source = 'engine' AND event_id = ?`, at, projectID, o.EventID)
			} else {
				r, err = tx.ExecContext(ctx, `UPDATE detections SET overridden = 1, overridden_at = ?
					WHERE project_id = ? AND id = (SELECT id FROM detections WHERE project_id = ? AND source = 'engine' AND session_id = ?
						AND action IN ('block', 'require_approval') AND overridden = 0 ORDER BY created_at DESC LIMIT 1)`,
					at, projectID, projectID, o.SessionID)
			}
			if err != nil {
				return err
			}
			if n, _ := r.RowsAffected(); n > 0 {
				res.Applied++
				continue
			}
			if o.EventID == "" {
				continue // nothing to wait for
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO detection_overrides_pending(project_id, event_id, session_id, at) VALUES(?,?,?,?)
				ON CONFLICT(project_id, event_id) DO NOTHING`, projectID, o.EventID, o.SessionID, at); err != nil {
				return err
			}
			res.Pending++
		}
		return nil
	})
	return res, err
}

func applyPendingOverrideTx(ctx context.Context, tx detDB, projectID, id, eventID string) error {
	var at int64
	err := tx.QueryRowContext(ctx, `SELECT at FROM detection_overrides_pending WHERE project_id = ? AND event_id = ?`, projectID, eventID).Scan(&at)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE detections SET overridden = 1, overridden_at = COALESCE(overridden_at, ?) WHERE project_id = ? AND id = ?`,
		at, projectID, id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM detection_overrides_pending WHERE project_id = ? AND event_id = ?`, projectID, eventID)
	return err
}

// ---------------------------------------------------------------- correlation

type detSpanRef struct {
	id, traceID, agent string
}

// linkDetectionTx attaches a detection to its tool span (§5.3), when it is
// not linked by tool call id or by replay already.
func linkDetectionTx(ctx context.Context, tx detDB, projectID, id string) error {
	var source, session, toolCallID, toolName, link, eventSource string
	var createdAt int64
	err := tx.QueryRowContext(ctx, `SELECT source, session_id, tool_call_id, tool_name, link, created_at, event_source FROM detections
		WHERE project_id = ? AND id = ?`, projectID, id).Scan(&source, &session, &toolCallID, &toolName, &link, &createdAt, &eventSource)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	if link == LinkToolCallID || link == LinkReplay {
		return nil
	}
	if toolCallID != "" {
		var ref detSpanRef
		err := tx.QueryRowContext(ctx, `SELECT a.id, a.trace_id, a.agent_name FROM agent_spans a
			JOIN calls c ON c.project_id = a.project_id AND c.id = a.id
			WHERE a.project_id = ? AND a.tool_call_id = ? AND a.operation_name IN ('execute_tool', 'invoke_agent') AND c.deleted_at IS NULL
				AND (? = '' OR a.conversation_id = ? OR a.conversation_id = '')
			ORDER BY a.conversation_id = ? DESC, c.started_at LIMIT 1`,
			projectID, toolCallID, session, session, session).Scan(&ref.id, &ref.traceID, &ref.agent)
		switch {
		case err == nil:
			return setLinkTx(ctx, tx, projectID, id, ref, LinkToolCallID)
		case err != sql.ErrNoRows:
			return err
		}
	}
	if link == LinkNearestTime || session == "" || toolName == "" {
		return nil
	}
	ref, ok, err := nearestToolSpanTx(ctx, tx, projectID, id, source, eventSource, session, toolName, toolCallID, createdAt)
	if err != nil || !ok {
		return err
	}
	return setLinkTx(ctx, tx, projectID, id, ref, LinkNearestTime)
}

// nearestToolCandidates bounds the tool spans (or detections) near a
// verdict whose tool names are compared in Go (ToolNamesMatch, toolnames.go).
const nearestToolCandidates = 500

// nearestToolSpanTx finds the session's tool span for the same tool
// (ToolNamesMatch: the same name, or a harness alias of it, e.g. Gemini
// CLI's run_shell_command for AgentShield's exec) starting nearest to at
// (within the correlation window) that no other detection of the same
// source has claimed. A span whose tool call id is set and differs from the
// detection's is never chosen.
func nearestToolSpanTx(ctx context.Context, tx detDB, projectID, detID, source, eventSource, session, toolName, toolCallID string, at int64) (detSpanRef, bool, error) {
	w := CorrelationWindow.Microseconds()
	rows, err := tx.QueryContext(ctx, `SELECT a.id, a.trace_id, a.agent_name, a.tool_name FROM agent_spans a
		JOIN calls c ON c.project_id = a.project_id AND c.id = a.id
		WHERE a.project_id = ? AND a.conversation_id = ? AND a.operation_name = 'execute_tool' AND a.tool_name <> ''
			AND c.deleted_at IS NULL AND c.started_at BETWEEN ? AND ?
			AND (? = '' OR a.tool_call_id = '' OR a.tool_call_id = ?)
			AND NOT EXISTS (SELECT 1 FROM detections d WHERE d.project_id = a.project_id AND d.span_id = a.id AND d.source = ? AND d.id <> ?)
		ORDER BY abs(c.started_at - ?), c.started_at, a.id LIMIT ?`,
		projectID, session, at-w, at+w, toolCallID, toolCallID, source, detID, at, nearestToolCandidates)
	if err != nil {
		return detSpanRef{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var ref detSpanRef
		var spanTool string
		if err := rows.Scan(&ref.id, &ref.traceID, &ref.agent, &spanTool); err != nil {
			return detSpanRef{}, false, err
		}
		if ToolNamesMatch(eventSource, toolName, spanTool) {
			return ref, true, nil
		}
	}
	return detSpanRef{}, false, rows.Err()
}

func setLinkTx(ctx context.Context, tx detDB, projectID, id string, ref detSpanRef, method string) error {
	_, err := tx.ExecContext(ctx, `UPDATE detections SET span_id = ?, trace_id = ?, link = ?,
			agent_name = CASE WHEN agent_name = '' THEN ? ELSE agent_name END
		WHERE project_id = ? AND id = ?`, ref.id, ref.traceID, method, ref.agent, projectID, id)
	return err
}

// detectionCallHook runs for every stored call start and end: it turns
// AgentShield evaluate spans into detections and links waiting detections
// to tool spans that just arrived (or just learned their session).
func detectionCallHook(ctx context.Context, tx *sql.Tx, projectID, id string) error {
	if err := detectionFromSpanTx(ctx, tx, projectID, id); err != nil { // detections_otel.go
		return err
	}
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM detections WHERE project_id = ? LIMIT 1`, projectID).Scan(&one)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var op, conv, toolCallID, traceID string
	err = tx.QueryRowContext(ctx, `SELECT operation_name, conversation_id, tool_call_id, trace_id FROM agent_spans WHERE project_id = ? AND id = ?`,
		projectID, id).Scan(&op, &conv, &toolCallID, &traceID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	// A sub-agent started by a tool (e.g. a coding agent's Task tool) is an
	// invoke_agent span carrying the tool-use id; verdicts on that tool call
	// link to it by id (never by time).
	if op == OpExecuteTool || (op == OpInvokeAgent && toolCallID != "") {
		return linkToolSpanTx(ctx, tx, projectID, id)
	}
	if conv == "" {
		return nil
	}
	// A parent that arrived after its children may just have given tool
	// spans of this trace their session; link waiting detections of it.
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM detections WHERE project_id = ? AND session_id = ? AND span_id IS NULL LIMIT 1`,
		projectID, conv).Scan(&one)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id FROM agent_spans a WHERE a.project_id = ? AND a.trace_id = ? AND a.conversation_id = ?
			AND a.operation_name = 'execute_tool'
			AND NOT EXISTS (SELECT 1 FROM detections d WHERE d.project_id = a.project_id AND d.span_id = a.id)
		LIMIT 1000`, projectID, traceID, conv)
	if err != nil {
		return err
	}
	var spans []string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			rows.Close()
			return err
		}
		spans = append(spans, sid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, sid := range spans {
		if err := linkToolSpanTx(ctx, tx, projectID, sid); err != nil {
			return err
		}
	}
	return nil
}

// linkToolSpanTx links detections waiting for this tool span: those with its
// tool call id (upgrading a time-based link), then the nearest one in its
// session for its tool (ToolNamesMatch) that is unlinked or time-linked to a span
// farther from its verdict (spans of a trace can arrive in any order). A span
// a detection moves away from is offered to the other waiting detections.
func linkToolSpanTx(ctx context.Context, tx detDB, projectID, spanID string) error {
	return linkToolSpanDepth(ctx, tx, projectID, spanID, 0)
}

func linkToolSpanDepth(ctx context.Context, tx detDB, projectID, spanID string, depth int) error {
	var conv, toolCallID, toolName, traceID, agent, op string
	var startedAt sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT a.conversation_id, a.tool_call_id, a.tool_name, a.trace_id, a.agent_name, c.started_at, a.operation_name
		FROM agent_spans a JOIN calls c ON c.project_id = a.project_id AND c.id = a.id
		WHERE a.project_id = ? AND a.id = ? AND c.deleted_at IS NULL
			AND (a.operation_name = 'execute_tool' OR (a.operation_name = 'invoke_agent' AND a.tool_call_id <> ''))`, projectID, spanID).
		Scan(&conv, &toolCallID, &toolName, &traceID, &agent, &startedAt, &op)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	ref := detSpanRef{id: spanID, traceID: traceID, agent: agent}
	var freed []string
	if toolCallID != "" {
		rows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(span_id, '') FROM detections WHERE project_id = ? AND tool_call_id = ?
			AND link NOT IN ('tool_call_id', 'replay') AND (session_id = ? OR session_id = '' OR ? = '')`, projectID, toolCallID, conv, conv)
		if err != nil {
			return err
		}
		var ids, old []string
		for rows.Next() {
			var id, prev string
			if err := rows.Scan(&id, &prev); err != nil {
				rows.Close()
				return err
			}
			ids, old = append(ids, id), append(old, prev)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for i, did := range ids {
			if err := setLinkTx(ctx, tx, projectID, did, ref, LinkToolCallID); err != nil {
				return err
			}
			if old[i] != "" && old[i] != spanID {
				freed = append(freed, old[i])
			}
		}
	}
	if op == OpExecuteTool && conv != "" && toolName != "" && startedAt.Valid {
		did, prev, ok, err := nearestDetectionTx(ctx, tx, projectID, spanID, conv, toolName, toolCallID, startedAt.Int64)
		if err != nil {
			return err
		}
		if ok {
			if err := setLinkTx(ctx, tx, projectID, did, ref, LinkNearestTime); err != nil {
				return err
			}
			if prev != "" {
				freed = append(freed, prev)
			}
		}
	}
	if depth < 3 {
		for _, f := range freed {
			if err := linkToolSpanDepth(ctx, tx, projectID, f, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// nearestDetectionTx finds the detection of a session for this tool span's
// tool (ToolNamesMatch, by the detection's harness) nearest to the span's
// start that is unlinked or time-linked to a span farther from its verdict.
// It returns the detection and the span it was linked to.
func nearestDetectionTx(ctx context.Context, tx detDB, projectID, spanID, conv, toolName, toolCallID string, at int64) (string, string, bool, error) {
	w := CorrelationWindow.Microseconds()
	rows, err := tx.QueryContext(ctx, `SELECT d.id, COALESCE(d.span_id, ''), d.event_source, d.tool_name FROM detections d
		LEFT JOIN calls oc ON oc.project_id = d.project_id AND oc.id = d.span_id
		WHERE d.project_id = ? AND d.session_id = ? AND d.tool_name <> '' AND d.created_at BETWEEN ? AND ?
			AND (d.tool_call_id = '' OR d.tool_call_id = ?)
			AND (d.span_id IS NULL OR (d.link = 'nearest_time' AND d.span_id <> ?
				AND abs(d.created_at - COALESCE(oc.started_at, 0)) > abs(d.created_at - ?)))
			AND NOT EXISTS (SELECT 1 FROM detections o WHERE o.project_id = d.project_id AND o.span_id = ? AND o.source = d.source)
		ORDER BY abs(d.created_at - ?), d.created_at, d.id LIMIT ?`,
		projectID, conv, at-w, at+w, toolCallID, spanID, at, spanID, at, nearestToolCandidates)
	if err != nil {
		return "", "", false, err
	}
	defer rows.Close()
	for rows.Next() {
		var did, prev, eventSource, detTool string
		if err := rows.Scan(&did, &prev, &eventSource, &detTool); err != nil {
			return "", "", false, err
		}
		if ToolNamesMatch(eventSource, detTool, toolName) {
			return did, prev, true, nil
		}
	}
	return "", "", false, rows.Err()
}

func detQueryIDs(ctx context.Context, tx detDB, q string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// RelinkDetections re-runs correlation for a project's unlinked and
// time-linked detections (repair, tests).
func (s *Store) RelinkDetections(ctx context.Context, projectID string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		ids, err := detQueryIDs(ctx, tx, `SELECT id FROM detections WHERE project_id = ? AND link NOT IN ('tool_call_id', 'replay')`, projectID)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := linkDetectionTx(ctx, tx, projectID, id); err != nil {
				return err
			}
		}
		return nil
	})
}
