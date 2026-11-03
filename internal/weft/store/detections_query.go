package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"
)

// DetectionQuery filters detections. Empty fields do not filter.
type DetectionQuery struct {
	ProjectID  string
	SessionID  string
	SessionIDs []string
	Agent      string
	Actions    []string
	RuleID     string
	Source     string
	Ruleset    string
	SpanID     string
	ToolCallID string
	ToolName   string
	Overridden *bool
	Linked     *bool
	From, To   int64 // unix micros on created_at; 0 = open
	Limit      int
	Offset     int
	// SortBy is created_at (default, newest first), -created_at (oldest
	// first; a leading "-" or ":asc"/":desc" suffix also accepted), severity.
	SortBy string
}

func (q *DetectionQuery) where() (string, []any) {
	conds := []string{"d.project_id = ?"}
	args := []any{q.ProjectID}
	eq := func(col, v string) {
		if v != "" {
			conds = append(conds, col+" = ?")
			args = append(args, v)
		}
	}
	eq("d.session_id", q.SessionID)
	eq("d.agent_name", q.Agent)
	eq("d.source", q.Source)
	eq("d.ruleset", q.Ruleset)
	eq("d.span_id", q.SpanID)
	eq("d.tool_call_id", q.ToolCallID)
	if q.ToolName != "" {
		conds = append(conds, "d.tool_name = ? COLLATE NOCASE")
		args = append(args, q.ToolName)
	}
	if len(q.SessionIDs) > 0 {
		conds = append(conds, "d.session_id IN ("+placeholders(len(q.SessionIDs))+")")
		for _, s := range q.SessionIDs {
			args = append(args, s)
		}
	}
	if len(q.Actions) > 0 {
		conds = append(conds, "d.action IN ("+placeholders(len(q.Actions))+")")
		for _, a := range q.Actions {
			args = append(args, a)
		}
	}
	if q.RuleID != "" {
		conds = append(conds, "EXISTS (SELECT 1 FROM detection_rules r WHERE r.project_id = d.project_id AND r.detection_id = d.id AND r.rule_id = ?)")
		args = append(args, q.RuleID)
	}
	if q.Overridden != nil {
		conds = append(conds, "d.overridden = ?")
		args = append(args, *q.Overridden)
	}
	if q.Linked != nil {
		if *q.Linked {
			conds = append(conds, "d.span_id IS NOT NULL")
		} else {
			conds = append(conds, "d.span_id IS NULL")
		}
	}
	if q.From > 0 {
		conds = append(conds, "d.created_at >= ?")
		args = append(args, q.From)
	}
	if q.To > 0 {
		conds = append(conds, "d.created_at < ?")
		args = append(args, q.To)
	}
	return strings.Join(conds, " AND "), args
}

func (q *DetectionQuery) orderBy() (string, error) {
	field, desc := strings.TrimSpace(q.SortBy), true
	if strings.HasPrefix(field, "-") {
		field, desc = field[1:], false
	}
	if f, dir, ok := strings.Cut(field, ":"); ok {
		field, desc = f, !strings.EqualFold(dir, "asc")
	}
	dir := " DESC"
	if !desc {
		dir = " ASC"
	}
	switch field {
	case "", "created_at":
		return "d.created_at" + dir + ", d.id" + dir, nil
	case "severity":
		return "d.severity_rank" + dir + ", d.created_at DESC, d.id", nil
	case "action":
		return "CASE d.action WHEN 'block' THEN 3 WHEN 'require_approval' THEN 2 WHEN 'log' THEN 1 ELSE 0 END" + dir + ", d.created_at DESC, d.id", nil
	}
	return "", Invalidf("sort_by must be created_at, severity or action")
}

const detectionCols = `d.id, d.project_id, d.event_id, d.session_id, d.span_id, d.trace_id, d.link, d.engine_span_id, d.tool_call_id,
	d.tool_name, d.agent_name, d.action, d.mode, d.severity, d.rules, d.source, d.ruleset, d.event_source, d.overridden,
	d.overridden_at, d.replay_job_id, d.created_at, d.ingested_at`

func scanDetection(sc interface{ Scan(...any) error }) (Detection, error) {
	var d Detection
	var span, trace, engineSpan, replayJob sql.NullString
	var rules string
	var overriddenAt sql.NullInt64
	var created, ingested int64
	if err := sc.Scan(&d.ID, &d.ProjectID, &d.EventID, &d.SessionID, &span, &trace, &d.Link, &engineSpan, &d.ToolCallID,
		&d.ToolName, &d.AgentName, &d.Action, &d.Mode, &d.Severity, &rules, &d.Source, &d.Ruleset, &d.EventSource, &d.Overridden,
		&overriddenAt, &replayJob, &created, &ingested); err != nil {
		return d, err
	}
	d.SpanID, d.TraceID, d.EngineSpanID, d.ReplayJobID = strPtr(span), strPtr(trace), strPtr(engineSpan), strPtr(replayJob)
	d.OverriddenAt = fmtNullMicros(overriddenAt)
	d.CreatedAt, d.IngestedAt = FormatMicros(created), FormatMicros(ingested)
	if err := json.Unmarshal([]byte(rules), &d.Rules); err != nil || d.Rules == nil {
		d.Rules = []DetectionRule{}
	}
	if d.SpanID == nil {
		d.Link = "session"
		if d.SessionID == "" {
			d.Link = "none"
		}
	}
	return d, nil
}

// QueryDetections lists detections and the total matching count.
func (s *Store) QueryDetections(ctx context.Context, q DetectionQuery) ([]Detection, int, error) {
	where, args := q.where()
	order, err := q.orderBy()
	if err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM detections d WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.r.QueryContext(ctx, `SELECT `+detectionCols+` FROM detections d WHERE `+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`,
		append(args, limit, max(q.Offset, 0))...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Detection{}
	for rows.Next() {
		d, err := scanDetection(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	return out, total, rows.Err()
}

// GetDetection reads one detection.
func (s *Store) GetDetection(ctx context.Context, projectID, id string) (*Detection, error) {
	d, err := scanDetection(s.r.QueryRowContext(ctx, `SELECT `+detectionCols+` FROM detections d WHERE d.project_id = ? AND d.id = ?`, projectID, id))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// ActionCounts maps action to count.
type ActionCounts map[string]int

// RuleCount is a per-rule row of DetectionStats.
type RuleCount struct {
	RuleID   string       `json:"rule_id"`
	Title    string       `json:"title"`
	Level    string       `json:"level"`
	Count    int          `json:"count"`
	ByAction ActionCounts `json:"by_action"`
}

// AgentCount is a per-agent row of DetectionStats. Rate is the agent's
// detection rate (ToolCallFlags), nil when Weft has none of its tool calls.
type AgentCount struct {
	Agent            string       `json:"agent"`
	Count            int          `json:"count"`
	ByAction         ActionCounts `json:"by_action"`
	ToolCalls        int          `json:"tool_calls"`
	FlaggedToolCalls int          `json:"flagged_tool_calls"`
	Rate             *float64     `json:"detection_rate"`
}

// DayCount is a per-day (UTC) row of DetectionStats.
type DayCount struct {
	Day        string       `json:"day"`
	Count      int          `json:"count"`
	ByAction   ActionCounts `json:"by_action"`
	BySeverity ActionCounts `json:"by_severity"`
}

// DetectionStats aggregates detections.
type DetectionStats struct {
	Total      int          `json:"total"`
	ByAction   ActionCounts `json:"by_action"`
	BySeverity ActionCounts `json:"by_severity"`
	BySource   ActionCounts `json:"by_source"`
	Overridden int          `json:"overridden"`
	Linked     int          `json:"linked"`
	ByRule     []RuleCount  `json:"by_rule"`
	ByAgent    []AgentCount `json:"by_agent"`
	ByDay      []DayCount   `json:"by_day"`
}

// DetectionStatsFor aggregates the detections q selects (Limit, Offset and
// SortBy are ignored): counts by action, severity, source, rule, agent and day.
func (s *Store) DetectionStatsFor(ctx context.Context, q DetectionQuery) (*DetectionStats, error) {
	where, args := q.where()
	st := &DetectionStats{ByAction: ActionCounts{}, BySeverity: ActionCounts{}, BySource: ActionCounts{}, ByRule: []RuleCount{}, ByAgent: []AgentCount{}, ByDay: []DayCount{}}
	rows, err := s.r.QueryContext(ctx, `SELECT d.action, d.severity, d.source, d.agent_name, d.overridden, d.span_id IS NOT NULL,
			strftime('%Y-%m-%d', d.created_at / 1000000, 'unixepoch'), COUNT(*)
		FROM detections d WHERE `+where+` GROUP BY 1, 2, 3, 4, 5, 6, 7`, args...)
	if err != nil {
		return nil, err
	}
	agents := map[string]*AgentCount{}
	days := map[string]*DayCount{}
	for rows.Next() {
		var action, sev, source, agent, day string
		var overridden, linked bool
		var n int
		if err := rows.Scan(&action, &sev, &source, &agent, &overridden, &linked, &day, &n); err != nil {
			rows.Close()
			return nil, err
		}
		st.Total += n
		st.ByAction[action] += n
		st.BySeverity[sev] += n
		st.BySource[source] += n
		if overridden {
			st.Overridden += n
		}
		if linked {
			st.Linked += n
		}
		a := agents[agent]
		if a == nil {
			a = &AgentCount{Agent: agent, ByAction: ActionCounts{}}
			agents[agent] = a
		}
		a.Count += n
		a.ByAction[action] += n
		dc := days[day]
		if dc == nil {
			dc = &DayCount{Day: day, ByAction: ActionCounts{}, BySeverity: ActionCounts{}}
			days[day] = dc
		}
		dc.Count += n
		dc.ByAction[action] += n
		dc.BySeverity[sev] += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.r.QueryContext(ctx, `SELECT r.rule_id, MAX(r.title), MAX(r.level), d.action, COUNT(*)
		FROM detection_rules r JOIN detections d ON d.project_id = r.project_id AND d.id = r.detection_id
		WHERE `+where+` GROUP BY r.rule_id, d.action`, args...)
	if err != nil {
		return nil, err
	}
	rules := map[string]*RuleCount{}
	for rows.Next() {
		var id, title, level, action string
		var n int
		if err := rows.Scan(&id, &title, &level, &action, &n); err != nil {
			rows.Close()
			return nil, err
		}
		r := rules[id]
		if r == nil {
			r = &RuleCount{RuleID: id, Title: title, Level: level, ByAction: ActionCounts{}}
			rules[id] = r
		}
		r.Count += n
		r.ByAction[action] += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, r := range rules {
		st.ByRule = append(st.ByRule, *r)
	}
	sort.Slice(st.ByRule, func(i, j int) bool {
		if st.ByRule[i].Count != st.ByRule[j].Count {
			return st.ByRule[i].Count > st.ByRule[j].Count
		}
		return st.ByRule[i].RuleID < st.ByRule[j].RuleID
	})
	flags, err := s.ToolCallFlags(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, a := range agents {
		if f, ok := flags[a.Agent]; ok && a.Agent != "" {
			a.ToolCalls, a.FlaggedToolCalls, a.Rate = f.ToolCalls, f.Flagged, f.Rate()
		}
		st.ByAgent = append(st.ByAgent, *a)
	}
	sort.Slice(st.ByAgent, func(i, j int) bool {
		if st.ByAgent[i].Count != st.ByAgent[j].Count {
			return st.ByAgent[i].Count > st.ByAgent[j].Count
		}
		return st.ByAgent[i].Agent < st.ByAgent[j].Agent
	})
	for _, d := range days {
		st.ByDay = append(st.ByDay, *d)
	}
	sort.Slice(st.ByDay, func(i, j int) bool { return st.ByDay[i].Day < st.ByDay[j].Day })
	return st, nil
}

// ToolCallFlag is the basis of an agent's detection rate.
type ToolCallFlag struct {
	ToolCalls int // recorded tool calls (execute_tool spans)
	Flagged   int // of those, the ones with a non-allow engine verdict
}

// Rate is Flagged / ToolCalls rounded to 4 places, or nil without tool calls.
func (f ToolCallFlag) Rate() *float64 {
	if f.ToolCalls <= 0 {
		return nil
	}
	r := math.Round(float64(f.Flagged)/float64(f.ToolCalls)*1e4) / 1e4
	return &r
}

// ToolCallFlags returns, per agent, the detection-rate basis over q's window
// (and session and agent, when given). This is the one definition of the
// detection rate (docs/weft/API.md, "Detection rate"), used by the agents
// API and the Security stats alike:
//
//	detection_rate = flagged tool calls / tool calls
//
// A tool call is a recorded execute_tool span of the agent that started in
// the window. It is flagged when at least one engine verdict (not a replay)
// other than allow is linked to it (detections.span_id) and matches q's
// other filters (action, rule, ...). A tool call with several detections
// counts once, and detections that are not linked to a recorded tool call
// (no span, or a session Weft has no spans for) are not part of the rate,
// so it is always between 0 and 1.
func (s *Store) ToolCallFlags(ctx context.Context, q DetectionQuery) (map[string]ToolCallFlag, error) {
	conds := []string{"a.project_id = ?", "a.operation_name = 'execute_tool'", "a.agent_name <> ''", "c.deleted_at IS NULL"}
	args := []any{q.ProjectID}
	if q.SessionID != "" {
		conds = append(conds, "a.conversation_id = ?")
		args = append(args, q.SessionID)
	}
	if q.Agent != "" {
		conds = append(conds, "a.agent_name = ?")
		args = append(args, q.Agent)
	}
	if q.From > 0 {
		conds = append(conds, "c.started_at >= ?")
		args = append(args, q.From)
	}
	if q.To > 0 {
		conds = append(conds, "c.started_at < ?")
		args = append(args, q.To)
	}
	// The verdict filter: q's detection filters, except agent and time,
	// which apply to the tool call.
	dq := q
	dq.Agent, dq.From, dq.To = "", 0, 0
	dwhere, dargs := dq.where()
	rows, err := s.r.QueryContext(ctx, `SELECT a.agent_name, COUNT(*), COALESCE(SUM(EXISTS (SELECT 1 FROM detections d
			WHERE d.span_id = a.id AND d.source = 'engine' AND d.action <> 'allow' AND `+dwhere+`)), 0)
		FROM agent_spans a JOIN calls c ON c.project_id = a.project_id AND c.id = a.id
		WHERE `+strings.Join(conds, " AND ")+` GROUP BY a.agent_name`, append(dargs, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ToolCallFlag{}
	for rows.Next() {
		var a string
		var f ToolCallFlag
		if err := rows.Scan(&a, &f.ToolCalls, &f.Flagged); err != nil {
			return nil, err
		}
		out[a] = f
	}
	return out, rows.Err()
}

// DetectionCountsBySession returns, for each session, the number of engine
// detections by action (allow, log, require_approval, block). Replay
// detections are not counted. Sessions without detections are absent. With
// no session ids it covers every session of the project.
func (s *Store) DetectionCountsBySession(ctx context.Context, projectID string, sessionIDs []string) (map[string]map[string]int, error) {
	out := map[string]map[string]int{}
	run := func(ids []string) error {
		q := `SELECT session_id, action, COUNT(*) FROM detections WHERE project_id = ? AND source = 'engine' AND session_id <> ''`
		args := []any{projectID}
		if len(ids) > 0 {
			q += ` AND session_id IN (` + placeholders(len(ids)) + `)`
			for _, id := range ids {
				args = append(args, id)
			}
		}
		rows, err := s.r.QueryContext(ctx, q+` GROUP BY session_id, action`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sid, action string
			var n int
			if err := rows.Scan(&sid, &action, &n); err != nil {
				return err
			}
			m := out[sid]
			if m == nil {
				m = map[string]int{}
				out[sid] = m
			}
			m[action] += n
		}
		return rows.Err()
	}
	if len(sessionIDs) == 0 {
		return out, run(nil)
	}
	for i := 0; i < len(sessionIDs); i += 500 {
		if err := run(sessionIDs[i:min(i+500, len(sessionIDs))]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// DeleteReplayDetections removes replay detections of a ruleset in the given
// sessions (before a replay re-run stores fresh results).
func (s *Store) DeleteReplayDetections(ctx context.Context, projectID, ruleset string, sessionIDs []string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for i := 0; i < len(sessionIDs); i += 500 {
			ids := sessionIDs[i:min(i+500, len(sessionIDs))]
			args := []any{projectID, ruleset}
			for _, id := range ids {
				args = append(args, id)
			}
			in := placeholders(len(ids))
			if _, err := tx.ExecContext(ctx, `DELETE FROM detection_rules WHERE project_id = ? AND detection_id IN (
				SELECT id FROM detections WHERE project_id = ? AND source = 'replay' AND ruleset = ? AND session_id IN (`+in+`))`,
				append([]any{projectID}, args...)...); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM detections WHERE project_id = ? AND source = 'replay' AND ruleset = ? AND session_id IN (`+in+`)`,
				args...); err != nil {
				return err
			}
		}
		return nil
	})
}

// MicrosFromQuery parses a time filter: RFC 3339, a date, or unix seconds.
func MicrosFromQuery(v string) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	if us, err := ParseTime(v); err == nil {
		return us, nil
	}
	var secs float64
	if err := json.Unmarshal([]byte(v), &secs); err == nil && secs > 0 {
		return int64(secs * float64(time.Second/time.Microsecond)), nil
	}
	return 0, Invalidf("invalid time %q (use RFC 3339 or unix seconds)", v)
}

// CountToolCalls counts the recorded tool calls (execute_tool spans) of a
// session, optionally of one agent.
func (s *Store) CountToolCalls(ctx context.Context, projectID, sessionID, agent string) (int, error) {
	var n int
	err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_spans a JOIN calls c ON c.project_id = a.project_id AND c.id = a.id
		WHERE a.project_id = ? AND a.conversation_id = ? AND a.operation_name = 'execute_tool' AND c.deleted_at IS NULL
			AND (? = '' OR a.agent_name = ?)`, projectID, sessionID, agent, agent).Scan(&n)
	return n, err
}

// ConversationOf returns the session (conversation id) of a call, if any.
func (s *Store) ConversationOf(ctx context.Context, projectID, callID string) (string, error) {
	var conv string
	err := s.r.QueryRowContext(ctx, `SELECT conversation_id FROM agent_spans WHERE project_id = ? AND id = ?`, projectID, callID).Scan(&conv)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return conv, err
}

// DetectionCountsByAgent returns, for each agent, the number of engine
// detections by action (agents without detections are absent; no names means
// every agent of the project).
func (s *Store) DetectionCountsByAgent(ctx context.Context, projectID string, agentNames []string) (map[string]map[string]int, error) {
	out := map[string]map[string]int{}
	run := func(names []string) error {
		q := `SELECT agent_name, action, COUNT(*) FROM detections WHERE project_id = ? AND source = 'engine' AND agent_name <> ''`
		args := []any{projectID}
		if len(names) > 0 {
			q += ` AND agent_name IN (` + placeholders(len(names)) + `)`
			for _, n := range names {
				args = append(args, n)
			}
		}
		return s.scanCounts(ctx, out, q+` GROUP BY agent_name, action`, args...)
	}
	if len(agentNames) == 0 {
		return out, run(nil)
	}
	for i := 0; i < len(agentNames); i += 500 {
		if err := run(agentNames[i:min(i+500, len(agentNames))]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// DetectionCountsBySessionAgent returns one session's engine detections by
// agent and action. Detections without an agent are counted under "".
func (s *Store) DetectionCountsBySessionAgent(ctx context.Context, projectID, sessionID string) (map[string]map[string]int, error) {
	out := map[string]map[string]int{}
	err := s.scanCounts(ctx, out, `SELECT agent_name, action, COUNT(*) FROM detections
		WHERE project_id = ? AND source = 'engine' AND session_id = ? GROUP BY agent_name, action`, projectID, sessionID)
	return out, err
}

func (s *Store) scanCounts(ctx context.Context, out map[string]map[string]int, q string, args ...any) error {
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key, action string
		var n int
		if err := rows.Scan(&key, &action, &n); err != nil {
			return err
		}
		m := out[key]
		if m == nil {
			m = map[string]int{}
			out[key] = m
		}
		m[action] += n
	}
	return rows.Err()
}
