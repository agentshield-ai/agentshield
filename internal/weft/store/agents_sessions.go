package store

// Sessions, teams, hand-offs and reasoning for the native API
// (docs/weft/DESIGN.md §2, §3). Summaries are computed at query time from
// the agents index (agent_spans joined with calls), agent_handoffs,
// reasoning_steps and agent_span_meta, so usage and cost always reflect
// the latest roll-up and price book.

import (
	"context"
	"database/sql"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// ---------------------------------------------------------------- detections

// sessionDetectionCounts, when set, returns AgentShield detection counts by
// action ("block", "require_approval", ...) for each session. It defaults to
// nil; the store then uses a DetectionCountsBySession method when the
// detections module provides one, and reports no detections otherwise.
var sessionDetectionCounts func(s *Store, ctx context.Context, projectID string, sessionIDs []string) (map[string]map[string]int, error)

type detectionsBySession interface {
	DetectionCountsBySession(ctx context.Context, projectID string, sessionIDs []string) (map[string]map[string]int, error)
}

// detectionsByAgent is optional: per-agent counts by action.
type detectionsByAgent interface {
	DetectionCountsByAgent(ctx context.Context, projectID string, agentNames []string) (map[string]map[string]int, error)
}

// detectionsBySessionAgent is optional: one session's counts per agent and action.
type detectionsBySessionAgent interface {
	DetectionCountsBySessionAgent(ctx context.Context, projectID, sessionID string) (map[string]map[string]int, error)
}

func (s *Store) detectionCounts(ctx context.Context, pid string, sessions []string) (map[string]map[string]int, error) {
	if len(sessions) == 0 {
		return map[string]map[string]int{}, nil
	}
	if sessionDetectionCounts != nil {
		return sessionDetectionCounts(s, ctx, pid, sessions)
	}
	if d, ok := any(s).(detectionsBySession); ok {
		return d.DetectionCountsBySession(ctx, pid, sessions)
	}
	return map[string]map[string]int{}, nil
}

func (s *Store) agentDetectionCounts(ctx context.Context, pid string, agents []string) (map[string]map[string]int, error) {
	if d, ok := any(s).(detectionsByAgent); ok && len(agents) > 0 {
		return d.DetectionCountsByAgent(ctx, pid, agents)
	}
	return map[string]map[string]int{}, nil
}

func (s *Store) sessionAgentDetectionCounts(ctx context.Context, pid, session string) (map[string]map[string]int, error) {
	if d, ok := any(s).(detectionsBySessionAgent); ok {
		return d.DetectionCountsBySessionAgent(ctx, pid, session)
	}
	return map[string]map[string]int{}, nil
}

func sumCounts(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// ---------------------------------------------------------------- helpers

// ListSort is a parsed sort_by: "field", "-field" or "field:asc|desc".
type ListSort struct {
	Field string
	Desc  bool
}

// ParseListSort parses a sort_by value; def applies when s is empty.
func ParseListSort(s string, def ListSort) ListSort {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	if strings.HasPrefix(s, "-") {
		return ListSort{Field: s[1:], Desc: true}
	}
	if f, d, ok := strings.Cut(s, ":"); ok {
		return ListSort{Field: f, Desc: strings.EqualFold(d, "desc")}
	}
	return ListSort{Field: s}
}

func sortRows[T any](rows []T, sb ListSort, field func(T, string) any, tie func(T) string) {
	sort.SliceStable(rows, func(i, j int) bool {
		less, eq := sortLess(field(rows[i], sb.Field), field(rows[j], sb.Field), sb.Desc)
		if !eq {
			return less
		}
		return tie(rows[i]) < tie(rows[j])
	})
}

func pageRows[T any](rows []T, limit, offset, def, max int) []T {
	if limit <= 0 {
		limit = def
	}
	if limit > max {
		limit = max
	}
	return page(rows, &limit, offset, def)
}

func msPtr(us int64, ok bool) *float64 {
	if !ok {
		return nil
	}
	v := math.Round(float64(us)/1000*1000) / 1000
	return &v
}

func fptr(v float64) *float64 { return &v }

// ---------------------------------------------------------------- spans

// verdictSpanIDs lists AgentShield verdict spans: stored as spans and as
// detections, but not agent activity (agents_handoffs.go).
func (s *Store) verdictSpanIDs(ctx context.Context, pid string) (map[string]bool, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT id FROM agent_span_meta WHERE project_id = ? AND verdict = 1`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// loadActivitySpans is loadAgentSpans split into agent activity and
// AgentShield verdict spans.
func (s *Store) loadActivitySpans(ctx context.Context, pid string, f agentSpanFilter) (kept, verdicts []*agentSpan, err error) {
	spans, err := s.loadAgentSpans(ctx, pid, f)
	if err != nil || len(spans) == 0 {
		return nil, nil, err
	}
	v, err := s.verdictSpanIDs(ctx, pid)
	if err != nil {
		return nil, nil, err
	}
	if len(v) == 0 {
		return spans, nil, nil
	}
	for _, sp := range spans {
		if v[sp.ID] {
			verdicts = append(verdicts, sp)
		} else {
			kept = append(kept, sp)
		}
	}
	return kept, verdicts, nil
}

// ---------------------------------------------------------------- teams

type sessionTeam struct {
	ID, Name string
	Explicit bool
	Agents   []string // first appearance order
}

func autoTeam(agents []string) (string, string) {
	if len(agents) == 0 {
		return "", ""
	}
	s := append([]string(nil), agents...)
	sort.Strings(s)
	return "auto:" + strings.Join(s, "+"), strings.Join(s, " + ")
}

// sessionAgents lists each session's agents in order of first appearance:
// agents named on spans, then hand-off endpoints.
func (s *Store) sessionAgents(ctx context.Context, pid string, sessions []string) (map[string][]string, error) {
	type first struct {
		agent string
		at    int64
	}
	byS := map[string][]first{}
	seen := map[[2]string]bool{}
	run := func(q string, args []any) error {
		rows, err := s.r.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sid, a string
			var at sql.NullInt64
			if err := rows.Scan(&sid, &a, &at); err != nil {
				return err
			}
			if a == "" || seen[[2]string{sid, a}] {
				continue
			}
			seen[[2]string{sid, a}] = true
			byS[sid] = append(byS[sid], first{a, at.Int64})
		}
		return rows.Err()
	}
	cond, cargs := "", []any{}
	hcond := ""
	if sessions != nil {
		if len(sessions) == 0 {
			return map[string][]string{}, nil
		}
		cond, cargs = inList("s.conversation_id", sessions)
		cond = " AND " + cond
		hc, _ := inList("session_id", sessions)
		hcond = " AND " + hc
	}
	if err := run(`SELECT s.conversation_id, s.agent_name, MIN(c.started_at) FROM agent_spans s
		JOIN calls c ON c.project_id = s.project_id AND c.id = s.id
		LEFT JOIN agent_span_meta m ON m.project_id = s.project_id AND m.id = s.id
		WHERE s.project_id = ? AND s.conversation_id != '' AND s.agent_name != '' AND c.deleted_at IS NULL
			AND COALESCE(m.verdict, 0) = 0`+cond+`
		GROUP BY s.conversation_id, s.agent_name ORDER BY 3`, append([]any{pid}, cargs...)); err != nil {
		return nil, err
	}
	for _, col := range []string{"from_agent", "to_agent"} {
		if err := run(`SELECT session_id, `+col+`, MIN(started_at) FROM agent_handoffs WHERE project_id = ? AND session_id != ''`+hcond+`
			GROUP BY session_id, `+col+` ORDER BY 3`, append([]any{pid}, cargs...)); err != nil {
			return nil, err
		}
	}
	out := map[string][]string{}
	for sid, l := range byS {
		sort.SliceStable(l, func(i, j int) bool { return l[i].at < l[j].at })
		for _, f := range l {
			out[sid] = append(out[sid], f.agent)
		}
	}
	return out, nil
}

// sessionTeams assigns each session its team: the first weft.team.id seen on
// its spans, else the auto team of its agents.
func (s *Store) sessionTeams(ctx context.Context, pid string, sessions []string) (map[string]*sessionTeam, error) {
	agents, err := s.sessionAgents(ctx, pid, sessions)
	if err != nil {
		return nil, err
	}
	cond, cargs := "", []any{}
	if sessions != nil {
		cond, cargs = inList("s.conversation_id", sessions)
		cond = " AND " + cond
	}
	rows, err := s.r.QueryContext(ctx, `SELECT s.conversation_id, m.team_id, m.team_name FROM agent_span_meta m
		JOIN agent_spans s ON s.project_id = m.project_id AND s.id = m.id
		JOIN calls c ON c.project_id = m.project_id AND c.id = m.id
		WHERE m.project_id = ? AND m.team_id != '' AND s.conversation_id != '' AND c.deleted_at IS NULL`+cond+`
		ORDER BY c.started_at`, append([]any{pid}, cargs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*sessionTeam{}
	for rows.Next() {
		var sid, id, name string
		if err := rows.Scan(&sid, &id, &name); err != nil {
			return nil, err
		}
		if t := out[sid]; t != nil {
			if t.Name == t.ID && name != "" {
				t.Name = name
			}
			continue
		}
		if name == "" {
			name = id
		}
		out[sid] = &sessionTeam{ID: id, Name: name, Explicit: true}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for sid, a := range agents {
		t := out[sid]
		if t == nil {
			id, name := autoTeam(a)
			t = &sessionTeam{ID: id, Name: name}
			out[sid] = t
		}
		t.Agents = a
	}
	return out, nil
}

// teamSessions lists the sessions of a team.
func (s *Store) teamSessions(ctx context.Context, pid, teamID string) ([]string, map[string]*sessionTeam, error) {
	teams, err := s.sessionTeams(ctx, pid, nil)
	if err != nil {
		return nil, nil, err
	}
	var out []string
	for sid, t := range teams {
		if t.ID == teamID {
			out = append(out, sid)
		}
	}
	sort.Strings(out)
	return out, teams, nil
}

// ---------------------------------------------------------------- hand-offs

// Handoff is an edge between two agents (DESIGN.md §2.1).
type Handoff struct {
	ID            string   `json:"id"`
	SessionID     string   `json:"session_id"`
	TraceID       string   `json:"trace_id"`
	SourceSpanID  string   `json:"source_span_id"`
	FromSpanID    *string  `json:"from_span_id"`
	ToSpanID      *string  `json:"to_span_id"`
	FromAgent     string   `json:"from_agent"`
	ToAgent       string   `json:"to_agent"`
	Kind          string   `json:"kind"`
	Source        string   `json:"source"`
	StartedAt     *string  `json:"started_at"`
	EndedAt       *string  `json:"ended_at"`
	DurationMS    *float64 `json:"duration_ms"`
	Status        string   `json:"status"`
	InputPreview  string   `json:"input_preview"`
	OutputPreview string   `json:"output_preview"`
	TeamID        string   `json:"team_id,omitempty"`
	startUS       int64
	endUS         int64
	hasEnd        bool
}

// HandoffsQuery filters /api/v1/handoffs.
type HandoffsQuery struct {
	ProjectID  string
	SessionID  string
	SessionIDs []string // nil: any
	FromAgent  string
	ToAgent    string
	Agent      string // either end
	TeamID     string
	Kind       string
	From, To   string
	Limit      int
	Offset     int
	SortBy     string
}

func (s *Store) loadHandoffs(ctx context.Context, pid string, q HandoffsQuery) ([]*Handoff, error) {
	where := []string{"h.project_id = ?", "c.deleted_at IS NULL"}
	args := []any{pid}
	for _, f := range []struct{ col, val string }{{"h.session_id", q.SessionID}, {"h.from_agent", q.FromAgent},
		{"h.to_agent", q.ToAgent}, {"h.kind", q.Kind}} {
		if f.val != "" {
			where = append(where, f.col+" = ?")
			args = append(args, f.val)
		}
	}
	if q.Agent != "" {
		where = append(where, "(h.from_agent = ? OR h.to_agent = ?)")
		args = append(args, q.Agent, q.Agent)
	}
	if q.SessionIDs != nil {
		if len(q.SessionIDs) == 0 {
			return nil, nil
		}
		c, a := inList("h.session_id", q.SessionIDs)
		where = append(where, c)
		args = append(args, a...)
	}
	after, before, err := timeRange(&q.From, &q.To)
	if err != nil {
		return nil, err
	}
	if after > 0 {
		where = append(where, "h.started_at >= ?")
		args = append(args, after)
	}
	if before > 0 {
		where = append(where, "h.started_at < ?")
		args = append(args, before)
	}
	rows, err := s.r.QueryContext(ctx, `SELECT h.id, h.session_id, h.trace_id, h.source_span_id, h.from_span_id, h.to_span_id, h.from_agent,
			h.to_agent, h.kind, h.source, h.started_at, h.ended_at, h.status, h.input_preview, h.output_preview
		FROM agent_handoffs h JOIN calls c ON c.project_id = h.project_id AND c.id = h.source_span_id
		WHERE `+strings.Join(where, " AND ")+` ORDER BY h.started_at, h.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Handoff
	for rows.Next() {
		h := &Handoff{}
		var from, to string
		var started, ended sql.NullInt64
		if err := rows.Scan(&h.ID, &h.SessionID, &h.TraceID, &h.SourceSpanID, &from, &to, &h.FromAgent, &h.ToAgent, &h.Kind, &h.Source,
			&started, &ended, &h.Status, &h.InputPreview, &h.OutputPreview); err != nil {
			return nil, err
		}
		h.FromSpanID, h.ToSpanID = ptrIf(from), ptrIf(to)
		h.startUS, h.endUS, h.hasEnd = started.Int64, ended.Int64, ended.Valid
		h.StartedAt, h.EndedAt = fmtNullMicros(started), fmtNullMicros(ended)
		if started.Valid && ended.Valid {
			h.DurationMS = msPtr(ended.Int64-started.Int64, true)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// QueryHandoffs lists hand-offs with their team.
func (s *Store) QueryHandoffs(ctx context.Context, q HandoffsQuery) ([]*Handoff, int, error) {
	pid, err := refs.NormalizeProject(q.ProjectID)
	if err != nil {
		return nil, 0, &InvalidError{Msg: err.Error()}
	}
	if q.Kind != "" && q.Kind != HandoffDelegate && q.Kind != HandoffHandoff && q.Kind != HandoffReturn {
		return nil, 0, Invalidf("kind must be delegate, handoff or return")
	}
	var teams map[string]*sessionTeam
	if q.TeamID != "" {
		var ids []string
		if ids, teams, err = s.teamSessions(ctx, pid, q.TeamID); err != nil {
			return nil, 0, err
		}
		q.SessionIDs = ids
		if ids == nil {
			q.SessionIDs = []string{}
		}
	}
	rows, err := s.loadHandoffs(ctx, pid, q)
	if err != nil {
		return nil, 0, err
	}
	if teams == nil {
		sids := map[string]bool{}
		for _, h := range rows {
			if h.SessionID != "" {
				sids[h.SessionID] = true
			}
		}
		if len(sids) > 0 {
			if teams, err = s.sessionTeams(ctx, pid, sortedKeys(sids)); err != nil {
				return nil, 0, err
			}
		}
	}
	for _, h := range rows {
		if t := teams[h.SessionID]; t != nil {
			h.TeamID = t.ID
		}
	}
	sb := ParseListSort(q.SortBy, ListSort{Field: "started_at"})
	switch sb.Field {
	case "started_at", "duration_ms", "from_agent", "to_agent", "kind", "status", "session_id":
	default:
		return nil, 0, Invalidf("cannot sort hand-offs by %q", sb.Field)
	}
	sortRows(rows, sb, func(h *Handoff, f string) any {
		switch f {
		case "started_at":
			return h.startUS
		case "duration_ms":
			return derefF(h.DurationMS)
		case "from_agent":
			return h.FromAgent
		case "to_agent":
			return h.ToAgent
		case "kind":
			return h.Kind
		case "status":
			return h.Status
		}
		return h.SessionID
	}, func(h *Handoff) string { return h.ID })
	return pageRows(rows, q.Limit, q.Offset, 100, 1000), len(rows), nil
}

// ---------------------------------------------------------------- reasoning

// ReasoningStep is one reasoning step (DESIGN.md §2.2).
type ReasoningStep struct {
	ID         string  `json:"id"`
	SpanID     string  `json:"span_id"`
	SessionID  string  `json:"session_id"`
	TraceID    string  `json:"trace_id"`
	AgentName  string  `json:"agent_name"`
	Index      int     `json:"index"`
	SpanIndex  int     `json:"span_index"`
	Kind       string  `json:"kind"`
	Text       string  `json:"text"`
	Tokens     *int64  `json:"tokens"`
	StartedAt  *string `json:"started_at"`
	NextSpanID *string `json:"next_span_id"`
	Source     string  `json:"source"`
	Duplicate  bool    `json:"duplicate,omitempty"`
	startUS    int64
	hash       string
}

// markDuplicates flags steps that repeat an earlier step of the session:
// reasoning re-sent to a model as part of its history.
func markDuplicates(steps []*ReasoningStep) {
	sort.SliceStable(steps, func(i, j int) bool {
		if steps[i].startUS != steps[j].startUS {
			return steps[i].startUS < steps[j].startUS
		}
		if steps[i].SpanID != steps[j].SpanID {
			return steps[i].SpanID < steps[j].SpanID
		}
		if (steps[i].Source == ReasoningFromInput) != (steps[j].Source == ReasoningFromInput) {
			return steps[i].Source == ReasoningFromInput
		}
		return steps[i].SpanIndex < steps[j].SpanIndex
	})
	produced := map[string]bool{}
	for _, st := range steps {
		if st.Source != ReasoningFromInput {
			produced[st.hash] = true
		}
	}
	seenInput := map[string]bool{}
	for _, st := range steps {
		if st.Source != ReasoningFromInput {
			continue
		}
		if produced[st.hash] || seenInput[st.hash] {
			st.Duplicate = true
		}
		seenInput[st.hash] = true
	}
}

type reasoningFilter struct {
	sessions []string
	withText bool
}

// loadReasoning loads steps by session, duplicates flagged, in session order.
func (s *Store) loadReasoning(ctx context.Context, pid string, f reasoningFilter) (map[string][]*ReasoningStep, error) {
	if len(f.sessions) == 0 {
		return map[string][]*ReasoningStep{}, nil
	}
	cols := "r.id, r.span_id, r.session_id, r.trace_id, r.agent_name, r.idx, r.kind, r.tokens, r.started_at, r.next_span_id, r.source, r.text_hash"
	if f.withText {
		cols += ", r.text"
	}
	out := map[string][]*ReasoningStep{}
	for start := 0; start < len(f.sessions); start += 400 {
		end := min(start+400, len(f.sessions))
		c, a := inList("r.session_id", f.sessions[start:end])
		rows, err := s.r.QueryContext(ctx, `SELECT `+cols+` FROM reasoning_steps r
			JOIN calls c ON c.project_id = r.project_id AND c.id = r.span_id
			WHERE r.project_id = ? AND c.deleted_at IS NULL AND `+c, append([]any{pid}, a...)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			st := &ReasoningStep{}
			var tok sql.NullInt64
			var next string
			dest := []any{&st.ID, &st.SpanID, &st.SessionID, &st.TraceID, &st.AgentName, &st.SpanIndex, &st.Kind, &tok, &st.startUS, &next, &st.Source, &st.hash}
			if f.withText {
				dest = append(dest, &st.Text)
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				return nil, err
			}
			if tok.Valid {
				v := tok.Int64
				st.Tokens = &v
			}
			st.NextSpanID = ptrIf(next)
			st.StartedAt = microsPtr(st.startUS)
			out[st.SessionID] = append(out[st.SessionID], st)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	for _, l := range out {
		markDuplicates(l)
		i := 0
		for _, st := range l {
			if !st.Duplicate {
				st.Index = i
				i++
			} else {
				st.Index = -1
			}
		}
	}
	return out, nil
}

// SessionReasoningQuery is /api/v1/sessions/{id}/reasoning.
type SessionReasoningQuery struct {
	ProjectID string
	SessionID string
	Agent     string
	Kind      string
	// Q keeps the steps whose text contains it (case-insensitive).
	Q                 string
	IncludeDuplicates bool
	Limit, Offset     int
}

// SessionReasoning returns a session's reasoning steps in order.
func (s *Store) SessionReasoning(ctx context.Context, q SessionReasoningQuery) ([]*ReasoningStep, int, error) {
	pid, err := refs.NormalizeProject(q.ProjectID)
	if err != nil {
		return nil, 0, &InvalidError{Msg: err.Error()}
	}
	if err := s.sessionExists(ctx, pid, q.SessionID); err != nil {
		return nil, 0, err
	}
	all, err := s.loadReasoning(ctx, pid, reasoningFilter{sessions: []string{q.SessionID}, withText: true})
	if err != nil {
		return nil, 0, err
	}
	out := []*ReasoningStep{}
	qq := strings.ToLower(normSearchQuery(q.Q))
	for _, st := range all[q.SessionID] {
		if (st.Duplicate && !q.IncludeDuplicates) || (q.Agent != "" && st.AgentName != q.Agent) || (q.Kind != "" && st.Kind != q.Kind) {
			continue
		}
		if qq != "" && !strings.Contains(strings.ToLower(st.Text), qq) {
			continue
		}
		out = append(out, st)
	}
	return pageRows(out, q.Limit, q.Offset, 500, 5000), len(out), nil
}

func (s *Store) sessionExists(ctx context.Context, pid, sessionID string) error {
	if sessionID == "" {
		return ErrNotFound
	}
	var one int
	err := s.r.QueryRowContext(ctx, `SELECT 1 FROM agent_spans s JOIN calls c ON c.project_id = s.project_id AND c.id = s.id
		WHERE s.project_id = ? AND s.conversation_id = ? AND c.deleted_at IS NULL LIMIT 1`, pid, sessionID).Scan(&one)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	return err
}

// ---------------------------------------------------------------- sessions

// SessionSummary is one session (DESIGN.md §2).
type SessionSummary struct {
	SessionID    string  `json:"session_id"`
	Name         string  `json:"name"`
	Title        string  `json:"title"`
	StartedAt    *string `json:"started_at"`
	LastActivity *string `json:"last_activity"`
	// DurationMS is null when unknown: a session seen only through
	// AgentShield verdicts (VerdictsOnly) has no agent spans to time.
	DurationMS *int64 `json:"duration_ms"`
	// VerdictsOnly marks a session known only from AgentShield verdicts (no
	// agent spans recorded). Its started_at and last_activity come from the
	// verdicts, so it still sorts and filters by time.
	VerdictsOnly       bool                `json:"verdicts_only"`
	Agents             []string            `json:"agents"`
	TeamID             string              `json:"team_id"`
	TeamName           string              `json:"team_name"`
	TurnCount          int                 `json:"turn_count"`
	TraceCount         int                 `json:"trace_count"`
	SpanCount          int                 `json:"span_count"`
	LLMCallCount       int                 `json:"llm_call_count"`
	ToolCallCount      int                 `json:"tool_call_count"`
	HandoffCount       int                 `json:"handoff_count"`
	ReasoningStepCount int                 `json:"reasoning_step_count"`
	DetectionCount     map[string]int      `json:"detection_count"`
	ErrorCount         int                 `json:"error_count"`
	InputTokens        int64               `json:"input_tokens"`
	OutputTokens       int64               `json:"output_tokens"`
	ReasoningTokens    int64               `json:"reasoning_tokens"`
	CostUSD            *float64            `json:"cost_usd"`
	Status             string              `json:"status"`
	Models             []string            `json:"models"`
	Tools              []string            `json:"tools"`
	Scores             map[string]ScoreAgg `json:"scores"`
	TraceIDs           []string            `json:"trace_ids,omitempty"`

	spans    []*agentSpan
	firstUS  int64
	lastUS   int64
	titleSrc string
}

// sessionStatus is the session's own outcome, from its top-level spans (the
// roots of its traces), the same way the Traces list reads a trace's status
// off its root: error when one of them failed, running while one has not
// ended, otherwise ok. A failed step under a root that did not fail (a tool
// call, model call, hand-off or delegated agent) leaves the session ok; the
// status filter's failed_step value finds those (error_count > 0). A session
// with no spans of its own (VerdictsOnly) is unknown (buildSessions).
func sessionStatus(spans []*agentSpan) string {
	ids := make(map[string]bool, len(spans))
	for _, sp := range spans {
		ids[sp.ID] = true
	}
	running, failed := false, false
	for _, sp := range spans {
		root := sp.ParentID == "" || !ids[sp.ParentID]
		if root && !sp.Ended {
			running = true
		}
		if root && sp.Exception != nil {
			failed = true
		}
	}
	switch {
	case failed:
		return "error"
	case running:
		return "running"
	}
	return "ok"
}

func summarizeSession(id string, spans []*agentSpan) *SessionSummary {
	ss := &SessionSummary{SessionID: id, spans: spans, DetectionCount: map[string]int{}, Scores: map[string]ScoreAgg{}}
	traces, models, tools, agents := map[string]bool{}, map[string]bool{}, map[string]bool{}, []string{}
	seenAgent := map[string]bool{}
	var cost float64
	hasCost := false
	ids := make(map[string]bool, len(spans))
	for _, sp := range spans {
		ids[sp.ID] = true
	}
	var titleSpan *agentSpan
	for _, sp := range spans {
		ss.SpanCount++
		traces[sp.TraceID] = true
		if ss.Name == "" && sp.ConvName != "" {
			ss.Name = sp.ConvName
		}
		if sp.IsTurn {
			ss.TurnCount++
		}
		switch sp.Op {
		case OpChat:
			ss.LLMCallCount++
		case OpExecuteTool:
			ss.ToolCallCount++
			if sp.ToolName != "" {
				tools[sp.ToolName] = true
			}
		}
		if sp.Exception != nil {
			ss.ErrorCount++
		}
		if sp.RequestModel != "" {
			models[sp.RequestModel] = true
		}
		if sp.AgentName != "" && !seenAgent[sp.AgentName] {
			seenAgent[sp.AgentName] = true
			agents = append(agents, sp.AgentName)
		}
		ss.InputTokens += int64(sp.InTok)
		ss.OutputTokens += int64(sp.OutTok)
		ss.ReasoningTokens += int64(sp.ReasonTok)
		if sp.HasCost {
			hasCost = true
			cost += sp.InCost + sp.OutCost
		}
		if ss.firstUS == 0 || sp.StartedUS < ss.firstUS {
			ss.firstUS = sp.StartedUS
		}
		end := sp.StartedUS
		if sp.Ended {
			end = sp.EndedUS
		}
		if end > ss.lastUS {
			ss.lastUS = end
		}
		root := sp.ParentID == "" || !ids[sp.ParentID]
		if titleSpan == nil && (sp.IsTurn || root) {
			titleSpan = sp
		}
	}
	if ss.TurnCount == 0 {
		ss.TurnCount = len(traces)
	}
	ss.TraceCount = len(traces)
	ss.TraceIDs = sortedKeys(traces)
	ss.Agents = agents
	ss.Models, ss.Tools = sortedKeys(models), sortedKeys(tools)
	if hasCost {
		ss.CostUSD = fptr(math.Round(cost*1e8) / 1e8)
	}
	ss.StartedAt, ss.LastActivity = microsPtr(ss.firstUS), microsPtr(ss.lastUS)
	d := (ss.lastUS - ss.firstUS) / 1000
	ss.DurationMS = &d
	ss.Status = sessionStatus(spans)
	if titleSpan != nil {
		ss.titleSrc = titleSpan.ID
	}
	return ss
}

// SessionsQuery filters /api/v1/sessions.
type SessionsQuery struct {
	ProjectID string
	Agent     string
	TeamID    string
	Status    string
	// HasDetection: "any", or an action such as "block" or "require_approval".
	HasDetection string
	// MinScore / MaxScore: "scorer:value" on session-scope scores.
	MinScore string
	MaxScore string
	Model    string
	Tool     string
	Q        string
	From, To string
	// ScoreBelow: "value" or "scorer:value": a session-scope score mean
	// (of any scorer, or of the named one) strictly below value.
	ScoreBelow string
	// CostMin keeps sessions costing at least this (unknown cost counts as 0).
	CostMin *float64
	Limit   int
	Offset  int
	SortBy  string
}

// Session status filters beyond the stored status (QuerySessions).
const (
	// SessionStatusFailedStep: the session ended without failing, but a
	// step under its top-level spans did (a tool call, model call, hand-off
	// or delegated agent): "went on after a failed step". On Traces the same
	// filter value is a trace whose root ended without an error but has a
	// failed span under it (descendant_error).
	SessionStatusFailedStep = "failed_step"
	// SessionStatusUnknown is the status of a session known only from
	// AgentShield verdicts (no agent spans): its outcome is not known. Only
	// status=unknown lists them; ok, error, failed_step and running never do.
	SessionStatusUnknown = "unknown"
	// HasDetectionFlagged: blocked at least once, or asked for approval
	// ApprovalHeavy or more times.
	HasDetectionFlagged = "flagged"
	// ApprovalHeavy is the number of approval requests that makes a session
	// approval-heavy (approval fatigue).
	ApprovalHeavy = 3
)

// IsFlaggedSession reports a session that was blocked at least once or asked
// for approval ApprovalHeavy or more times.
func IsFlaggedSession(counts map[string]int) bool {
	return counts[DetectionBlock] > 0 || counts[DetectionRequireApproval] >= ApprovalHeavy
}

// lowestScore is the lowest session-scope score mean, or false without one.
func lowestScore(ss *SessionSummary) (float64, bool) {
	low, ok := 0.0, false
	for _, a := range ss.Scores {
		if a.Mean != nil && (!ok || *a.Mean < low) {
			low, ok = *a.Mean, true
		}
	}
	return low, ok
}

func parseScoreFilter(s string) (string, float64, error) {
	if s == "" {
		return "", 0, nil
	}
	i := strings.LastIndexByte(s, ':')
	if i <= 0 {
		return "", 0, Invalidf("score filters take scorer:value, got %q", s)
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s[i+1:]), 64)
	if err != nil {
		return "", 0, Invalidf("score filters take scorer:value, got %q", s)
	}
	return strings.TrimSpace(s[:i]), v, nil
}

// sessionsWithin lists sessions with spans started in [after, before).
func (s *Store) sessionsWithin(ctx context.Context, pid string, after, before int64) ([]string, error) {
	where := []string{"s.project_id = ?", "s.conversation_id != ''", "c.deleted_at IS NULL"}
	args := []any{pid}
	if after > 0 {
		where = append(where, "COALESCE(c.ended_at, c.started_at) >= ?")
		args = append(args, after)
	}
	if before > 0 {
		where = append(where, "c.started_at < ?")
		args = append(args, before)
	}
	rows, err := s.r.QueryContext(ctx, `SELECT DISTINCT s.conversation_id FROM agent_spans s JOIN calls c ON c.project_id = s.project_id AND c.id = s.id
		WHERE `+strings.Join(where, " AND "), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// buildSessions summarises sessions (all when ids is nil) with teams,
// hand-off and reasoning counts, detections and session scores.
func (s *Store) buildSessions(ctx context.Context, pid string, ids []string) ([]*SessionSummary, error) {
	spans, verdicts, err := s.loadActivitySpans(ctx, pid, agentSpanFilter{ConversationIDs: ids})
	if err != nil {
		return nil, err
	}
	by := map[string][]*agentSpan{}
	for _, sp := range spans {
		if sp.ConvID != "" {
			by[sp.ConvID] = append(by[sp.ConvID], sp)
		}
	}
	// A session seen only through AgentShield verdicts is still a session.
	vby := map[string][]*agentSpan{}
	for _, sp := range verdicts {
		if sp.ConvID != "" {
			vby[sp.ConvID] = append(vby[sp.ConvID], sp)
			if by[sp.ConvID] == nil {
				by[sp.ConvID] = []*agentSpan{}
			}
		}
	}
	out := make([]*SessionSummary, 0, len(by))
	sids := make([]string, 0, len(by))
	for id, l := range by {
		ss := summarizeSession(id, l)
		// started_at, last_activity and duration come from the session's
		// own spans: a verdict can arrive long after the tool call it
		// concerns. A session seen through verdicts alone takes started_at
		// and last_activity from them (so it sorts and filters by time), but
		// its duration is unknown: verdict times say when tool calls were
		// checked, not how long the session ran.
		if len(l) == 0 {
			for _, v := range vby[id] {
				if ss.firstUS == 0 || v.StartedUS < ss.firstUS {
					ss.firstUS = v.StartedUS
				}
				if v.StartedUS > ss.lastUS {
					ss.lastUS = v.StartedUS
				}
			}
			ss.StartedAt, ss.LastActivity = microsPtr(ss.firstUS), microsPtr(ss.lastUS)
			ss.DurationMS = nil
			ss.VerdictsOnly = true
			// No agent spans: whether the session succeeded is not known
			// (a blocked tool call does not mean the session failed).
			ss.Status = SessionStatusUnknown
		}
		out = append(out, ss)
		sids = append(sids, id)
	}
	if len(out) == 0 {
		return out, nil
	}
	sort.Strings(sids)
	teams, err := s.sessionTeams(ctx, pid, sids)
	if err != nil {
		return nil, err
	}
	hcount := map[string]int{}
	for start := 0; start < len(sids); start += 400 {
		end := min(start+400, len(sids))
		c, a := inList("h.session_id", sids[start:end])
		rows, err := s.r.QueryContext(ctx, `SELECT h.session_id, COUNT(*) FROM agent_handoffs h
			JOIN calls c ON c.project_id = h.project_id AND c.id = h.source_span_id
			WHERE h.project_id = ? AND h.kind != 'return' AND c.deleted_at IS NULL AND `+c+` GROUP BY h.session_id`, append([]any{pid}, a...)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var sid string
			var n int
			if err := rows.Scan(&sid, &n); err != nil {
				rows.Close()
				return nil, err
			}
			hcount[sid] = n
		}
		rows.Close()
	}
	reasoning, err := s.loadReasoning(ctx, pid, reasoningFilter{sessions: sids})
	if err != nil {
		return nil, err
	}
	dets, err := s.detectionCounts(ctx, pid, sids)
	if err != nil {
		return nil, err
	}
	scores, _, err := s.QueryScopeScores(ctx, ScopeScoresQuery{ProjectID: pid, Scope: ScopeSession, SessionIDs: sids, Limit: math.MaxInt32})
	if err != nil {
		return nil, err
	}
	agg := aggregateScores(scores, func(sc ScopeScore) string { return sc.SessionID })
	for _, ss := range out {
		if t := teams[ss.SessionID]; t != nil {
			ss.TeamID, ss.TeamName = t.ID, t.Name
			for _, a := range t.Agents {
				if !containsStr(ss.Agents, a) {
					ss.Agents = append(ss.Agents, a)
				}
			}
		}
		ss.HandoffCount = hcount[ss.SessionID]
		for _, st := range reasoning[ss.SessionID] {
			if !st.Duplicate {
				ss.ReasoningStepCount++
			}
		}
		if d := dets[ss.SessionID]; d != nil {
			ss.DetectionCount = d
		}
		if a := agg[ss.SessionID]; a != nil {
			ss.Scores = a
		}
	}
	return out, nil
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// SessionTitles returns a label for each of the given sessions: its name
// when it has one, else its title (the first user request). Sessions with
// neither are left out.
func (s *Store) SessionTitles(ctx context.Context, projectID string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	sessions, err := s.buildSessions(ctx, pid, ids)
	if err != nil {
		return nil, err
	}
	if err := s.fillTitles(ctx, pid, sessions); err != nil {
		return nil, err
	}
	for _, ss := range sessions {
		switch {
		case ss.Name != "" && ss.Name != ss.SessionID:
			out[ss.SessionID] = ss.Name
		case ss.Title != "":
			out[ss.SessionID] = ss.Title
		}
	}
	return out, nil
}

// fillTitles sets each session's title: the first user request.
func (s *Store) fillTitles(ctx context.Context, pid string, sessions []*SessionSummary) error {
	var ids []string
	for _, ss := range sessions {
		if ss.titleSrc != "" {
			ids = append(ids, ss.titleSrc)
		}
	}
	prev, err := spanPreviewsQ(ctx, s.r, pid, ids, 300)
	if err != nil {
		return err
	}
	for _, ss := range sessions {
		ss.Title = prev[ss.titleSrc][0]
	}
	return nil
}

// spanPreviewsQ reads short input and output texts of spans.
func spanPreviewsQ(ctx context.Context, q queryer, projectID string, ids []string, n int) (map[string][2]string, error) {
	out := map[string][2]string{}
	for start := 0; start < len(ids); start += 400 {
		end := min(start+400, len(ids))
		cond, args := inList("id", ids[start:end])
		rows, err := q.QueryContext(ctx, `SELECT id, inputs, output FROM calls WHERE project_id = ? AND `+cond, append([]any{projectID}, args...)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, in string
			var o sql.NullString
			if err := rows.Scan(&id, &in, &o); err != nil {
				rows.Close()
				return nil, err
			}
			var outV any
			if o.Valid {
				outV = decodeJSON(o.String)
			}
			out[id] = [2]string{truncateText(inputPreview(decodeMap(in)), n), truncateText(outputPreview(outV), n)}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func sessionField(ss *SessionSummary, f string) any {
	switch f {
	case "started_at":
		return ss.firstUS
	case "last_activity":
		return ss.lastUS
	case "duration_ms":
		if ss.DurationMS == nil {
			return int64(-1) // unknown sorts as shortest
		}
		return *ss.DurationMS
	case "span_count":
		return int64(ss.SpanCount)
	case "turn_count":
		return int64(ss.TurnCount)
	case "tool_call_count":
		return int64(ss.ToolCallCount)
	case "handoff_count":
		return int64(ss.HandoffCount)
	case "reasoning_step_count":
		return int64(ss.ReasoningStepCount)
	case "error_count":
		return int64(ss.ErrorCount)
	case "detection_count":
		return int64(sumCounts(ss.DetectionCount))
	case "input_tokens":
		return ss.InputTokens
	case "output_tokens":
		return ss.OutputTokens
	case "reasoning_tokens":
		return ss.ReasoningTokens
	case "cost_usd":
		return derefF(ss.CostUSD)
	case "status":
		return ss.Status
	case "name":
		return ss.Name
	}
	return ss.SessionID
}

var sessionSortFields = map[string]bool{"started_at": true, "last_activity": true, "duration_ms": true, "span_count": true,
	"turn_count": true, "tool_call_count": true, "handoff_count": true, "reasoning_step_count": true, "error_count": true,
	"detection_count": true, "input_tokens": true, "output_tokens": true, "reasoning_tokens": true, "cost_usd": true,
	"status": true, "name": true, "session_id": true}

// QuerySessions lists sessions matching q, newest activity first by default.
func (s *Store) QuerySessions(ctx context.Context, q SessionsQuery) ([]*SessionSummary, int, error) {
	out, pid, qq, err := s.matchingSessions(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	total := len(out)
	paged := pageRows(out, q.Limit, q.Offset, 50, 1000)
	if qq == "" {
		if err := s.fillTitles(ctx, pid, paged); err != nil {
			return nil, 0, err
		}
	}
	for _, ss := range paged {
		ss.TraceIDs = nil
	}
	return paged, total, nil
}

// MatchingSessionIDs returns the ids of every session matching q (no
// paging), for filters that apply through sessions (detections).
func (s *Store) MatchingSessionIDs(ctx context.Context, q SessionsQuery) ([]string, error) {
	out, _, _, err := s.matchingSessions(ctx, q)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(out))
	for i, ss := range out {
		ids[i] = ss.SessionID
	}
	return ids, nil
}

// matchingSessions returns every session matching q, sorted, unpaged; qq is
// the normalised text query (titles are filled when it is set).
func (s *Store) matchingSessions(ctx context.Context, q SessionsQuery) (out []*SessionSummary, pid, qq string, err error) {
	f, err := compileSessionFilter(q)
	if err != nil {
		return nil, "", "", err
	}
	cands, pid, err := s.sessionCandidates(ctx, q, f)
	if err != nil {
		return nil, "", "", err
	}
	out = cands[:0]
	for _, ss := range cands {
		if f.match(ss) {
			out = append(out, ss)
		}
	}
	return out, pid, f.qq, nil
}

// sessionFilter is a compiled SessionsQuery: everything but the time range,
// which sessionCandidates applies.
type sessionFilter struct {
	q                    SessionsQuery
	sortBy               ListSort
	minScorer, maxScorer string
	minV, maxV           float64
	belowScorer          string
	belowV               float64
	belowSet             bool
	qq                   string
	textHits             map[string]bool
}

func compileSessionFilter(q SessionsQuery) (*sessionFilter, error) {
	f := &sessionFilter{q: q, sortBy: ParseListSort(q.SortBy, ListSort{Field: "last_activity", Desc: true})}
	if !sessionSortFields[f.sortBy.Field] {
		return nil, Invalidf("cannot sort sessions by %q", f.sortBy.Field)
	}
	switch q.Status {
	case "", "running", "ok", "error", SessionStatusFailedStep, SessionStatusUnknown:
	default:
		return nil, Invalidf("status must be running, ok, error, failed_step or unknown")
	}
	var err error
	if f.minScorer, f.minV, err = parseScoreFilter(q.MinScore); err != nil {
		return nil, err
	}
	if f.maxScorer, f.maxV, err = parseScoreFilter(q.MaxScore); err != nil {
		return nil, err
	}
	f.belowSet = strings.TrimSpace(q.ScoreBelow) != ""
	if f.belowSet {
		raw := strings.TrimSpace(q.ScoreBelow)
		if i := strings.LastIndex(raw, ":"); i >= 0 {
			f.belowScorer, raw = strings.TrimSpace(raw[:i]), raw[i+1:]
		}
		if f.belowV, err = strconv.ParseFloat(strings.TrimSpace(raw), 64); err != nil {
			return nil, Invalidf("score_below must be value or scorer:value")
		}
	}
	f.qq = strings.ToLower(normSearchQuery(q.Q))
	return f, nil
}

// sessionCandidates builds the sessions whose activity overlaps q's time
// range, sorted by f's sort, with titles and text hits filled when f has a
// text query. Every other filter is f.match.
func (s *Store) sessionCandidates(ctx context.Context, q SessionsQuery, f *sessionFilter) ([]*SessionSummary, string, error) {
	pid, err := refs.NormalizeProject(q.ProjectID)
	if err != nil {
		return nil, "", &InvalidError{Msg: err.Error()}
	}
	after, before, err := timeRange(&q.From, &q.To)
	if err != nil {
		return nil, "", err
	}
	var ids []string
	if after > 0 || before > 0 {
		if ids, err = s.sessionsWithin(ctx, pid, after, before); err != nil {
			return nil, "", err
		}
	}
	sessions, err := s.buildSessions(ctx, pid, ids)
	if err != nil {
		return nil, "", err
	}
	if after > 0 || before > 0 {
		// sessionsWithin also matches on verdict spans; keep the sessions
		// whose own activity overlaps the range.
		kept := sessions[:0]
		for _, ss := range sessions {
			if (after > 0 && ss.lastUS < after) || (before > 0 && ss.firstUS >= before) {
				continue
			}
			kept = append(kept, ss)
		}
		sessions = kept
	}
	if f.qq != "" {
		if err := s.fillTitles(ctx, pid, sessions); err != nil {
			return nil, "", err
		}
		if f.textHits, err = s.sessionsMatchingText(ctx, pid, f.qq); err != nil { // agents_search.go
			return nil, "", err
		}
	}
	sortRows(sessions, f.sortBy, sessionField, func(ss *SessionSummary) string { return ss.SessionID })
	return sessions, pid, nil
}

// match applies every filter but the time range to one session.
func (f *sessionFilter) match(ss *SessionSummary) bool {
	q := &f.q
	if q.Agent != "" && !containsStr(ss.Agents, q.Agent) {
		return false
	}
	if q.TeamID != "" && ss.TeamID != q.TeamID {
		return false
	}
	if !sessionStatusMatches(ss, q.Status) {
		return false
	}
	if q.Model != "" && !containsStr(ss.Models, q.Model) {
		return false
	}
	if q.Tool != "" && !containsStr(ss.Tools, q.Tool) {
		return false
	}
	switch q.HasDetection {
	case "":
	case "any":
		if sumCounts(ss.DetectionCount) == 0 {
			return false
		}
	case HasDetectionFlagged:
		if !IsFlaggedSession(ss.DetectionCount) {
			return false
		}
	default:
		if ss.DetectionCount[q.HasDetection] == 0 {
			return false
		}
	}
	if f.minScorer != "" {
		if a, ok := ss.Scores[f.minScorer]; !ok || a.Mean == nil || *a.Mean < f.minV {
			return false
		}
	}
	if f.maxScorer != "" {
		if a, ok := ss.Scores[f.maxScorer]; !ok || a.Mean == nil || *a.Mean > f.maxV {
			return false
		}
	}
	if f.belowSet {
		hit := false
		for name, a := range ss.Scores {
			if (f.belowScorer == "" || name == f.belowScorer) && a.Mean != nil && *a.Mean < f.belowV {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if q.CostMin != nil {
		cost := 0.0
		if ss.CostUSD != nil {
			cost = *ss.CostUSD
		}
		if cost < *q.CostMin {
			return false
		}
	}
	if f.qq != "" {
		hay := strings.ToLower(strings.Join(append([]string{ss.SessionID, ss.Name, ss.Title, ss.TeamID, ss.TeamName}, ss.Agents...), "\n"))
		if !strings.Contains(hay, f.qq) && !f.textHits[ss.SessionID] {
			return false
		}
	}
	return true
}

// GetAgentSession returns one session's summary.
func (s *Store) GetAgentSession(ctx context.Context, projectID, sessionID string) (*SessionSummary, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	l, err := s.buildSessions(ctx, pid, []string{sessionID})
	if err != nil {
		return nil, err
	}
	if len(l) == 0 {
		return nil, ErrNotFound
	}
	if err := s.fillTitles(ctx, pid, l); err != nil {
		return nil, err
	}
	return l[0], nil
}

// ---------------------------------------------------------------- graph

// GraphNode is one agent of a delegation graph.
type GraphNode struct {
	Agent      string         `json:"agent"`
	Calls      int64          `json:"calls"`
	Spans      int64          `json:"spans"`
	Errors     int64          `json:"errors"`
	Tokens     int64          `json:"tokens"`
	CostUSD    *float64       `json:"cost_usd"`
	ToolCalls  int64          `json:"tool_calls"`
	Detections map[string]int `json:"detections"`
	Sessions   int            `json:"sessions,omitempty"`
}

// GraphEdge aggregates the hand-offs between two agents.
type GraphEdge struct {
	From      string              `json:"from"`
	To        string              `json:"to"`
	Kind      string              `json:"kind"`
	Count     int                 `json:"count"`
	Errors    int                 `json:"errors"`
	ErrorRate float64             `json:"error_rate"`
	P50MS     *float64            `json:"p50_ms"`
	P95MS     *float64            `json:"p95_ms"`
	MeanScore *float64            `json:"mean_score"`
	Scores    map[string]ScoreAgg `json:"scores,omitempty"`
}

// Graph is a delegation graph (DESIGN.md §3).
type Graph struct {
	Nodes []*GraphNode `json:"nodes"`
	Edges []*GraphEdge `json:"edges"`
}

func buildGraph(spans []*agentSpan, edges []*Handoff, dets map[string]map[string]int, handoffScores map[string]map[string]ScoreAgg) *Graph {
	nodes := map[string]*GraphNode{}
	sessions := map[string]map[string]bool{}
	node := func(a string) *GraphNode {
		n := nodes[a]
		if n == nil {
			n = &GraphNode{Agent: a, Detections: map[string]int{}}
			if d := dets[a]; d != nil {
				n.Detections = d
			}
			nodes[a] = n
			sessions[a] = map[string]bool{}
		}
		return n
	}
	costs := map[string][2]float64{}
	for _, sp := range spans {
		if sp.AgentName == "" {
			continue
		}
		n := node(sp.AgentName)
		n.Spans++
		if sp.IsInvocation {
			n.Calls++
		}
		if sp.Exception != nil {
			n.Errors++
		}
		if sp.Op == OpExecuteTool {
			n.ToolCalls++
		}
		n.Tokens += int64(sp.InTok + sp.OutTok)
		if sp.HasCost {
			c := costs[sp.AgentName]
			costs[sp.AgentName] = [2]float64{c[0] + sp.InCost + sp.OutCost, 1}
		}
		if sp.ConvID != "" {
			sessions[sp.AgentName][sp.ConvID] = true
		}
	}
	type ek struct{ from, to, kind string }
	agg := map[ek]*GraphEdge{}
	durs := map[ek][]float64{}
	scoreSum := map[ek][2]float64{}
	for _, h := range edges {
		node(h.FromAgent)
		node(h.ToAgent)
		if h.SessionID != "" {
			sessions[h.FromAgent][h.SessionID] = true
			sessions[h.ToAgent][h.SessionID] = true
		}
		k := ek{h.FromAgent, h.ToAgent, h.Kind}
		e := agg[k]
		if e == nil {
			e = &GraphEdge{From: h.FromAgent, To: h.ToAgent, Kind: h.Kind}
			agg[k] = e
		}
		e.Count++
		if h.Status == "error" {
			e.Errors++
		}
		if h.DurationMS != nil && h.Kind != HandoffReturn {
			durs[k] = append(durs[k], *h.DurationMS)
		}
		for scorer, a := range handoffScores[h.ID] {
			if a.Mean == nil {
				continue
			}
			sum := scoreSum[k]
			scoreSum[k] = [2]float64{sum[0] + *a.Mean, sum[1] + 1}
			if e.Scores == nil {
				e.Scores = map[string]ScoreAgg{}
			}
			cur := e.Scores[scorer]
			prevMean := 0.0
			if cur.Mean != nil {
				prevMean = *cur.Mean
			}
			m := (prevMean*float64(cur.Count) + *a.Mean) / float64(cur.Count+1)
			e.Scores[scorer] = ScoreAgg{Mean: &m, Count: cur.Count + 1}
		}
	}
	g := &Graph{Nodes: []*GraphNode{}, Edges: []*GraphEdge{}}
	for a, n := range nodes {
		if c, ok := costs[a]; ok && c[1] > 0 {
			n.CostUSD = fptr(math.Round(c[0]*1e8) / 1e8)
		}
		n.Sessions = len(sessions[a])
		g.Nodes = append(g.Nodes, n)
	}
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].Agent < g.Nodes[j].Agent })
	for k, e := range agg {
		e.ErrorRate = math.Round(float64(e.Errors)/float64(e.Count)*1e4) / 1e4
		d := durs[k]
		sort.Float64s(d)
		e.P50MS, e.P95MS = pctPtr(d, 0.5), pctPtr(d, 0.95)
		if s := scoreSum[k]; s[1] > 0 {
			e.MeanScore = fptr(s[0] / s[1])
		}
		g.Edges = append(g.Edges, e)
	}
	sort.Slice(g.Edges, func(i, j int) bool {
		a, b := g.Edges[i], g.Edges[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.To != b.To {
			return a.To < b.To
		}
		return a.Kind < b.Kind
	})
	return g
}

func (s *Store) handoffScores(ctx context.Context, pid string, sessions []string) (map[string]map[string]ScoreAgg, error) {
	scores, _, err := s.QueryScopeScores(ctx, ScopeScoresQuery{ProjectID: pid, Scope: ScopeHandoff, SessionIDs: sessions, Limit: math.MaxInt32})
	if err != nil {
		return nil, err
	}
	return aggregateScores(scores, func(sc ScopeScore) string { return sc.HandoffID }), nil
}

// SessionGraph is the delegation graph of one session.
func (s *Store) SessionGraph(ctx context.Context, projectID, sessionID string) (*Graph, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	spans, verdicts, err := s.loadActivitySpans(ctx, pid, agentSpanFilter{ConversationIDs: []string{sessionID}})
	if err != nil {
		return nil, err
	}
	if len(spans)+len(verdicts) == 0 {
		return nil, ErrNotFound
	}
	edges, err := s.loadHandoffs(ctx, pid, HandoffsQuery{SessionID: sessionID})
	if err != nil {
		return nil, err
	}
	dets, err := s.sessionAgentDetectionCounts(ctx, pid, sessionID)
	if err != nil {
		return nil, err
	}
	hs, err := s.handoffScores(ctx, pid, []string{sessionID})
	if err != nil {
		return nil, err
	}
	g := buildGraph(spans, edges, dets, hs)
	for _, n := range g.Nodes {
		n.Sessions = 0
	}
	return g, nil
}

// ---------------------------------------------------------------- timeline

// TimelineItem is one entry of a session timeline.
type TimelineItem struct {
	Type      string         `json:"type"`
	SpanID    string         `json:"span_id"`
	TraceID   string         `json:"trace_id"`
	AgentName string         `json:"agent_name"`
	StartedAt *string        `json:"started_at"`
	EndedAt   *string        `json:"ended_at"`
	Payload   map[string]any `json:"payload"`
	startUS   int64
	order     int
}

// Timeline item types.
var timelineOrder = map[string]int{"turn": 0, "message": 1, "llm": 2, "reasoning": 3, "tool_call": 4, "handoff": 5, "error": 6}

// SessionTimelineQuery is /api/v1/sessions/{id}/timeline.
type SessionTimelineQuery struct {
	ProjectID string
	SessionID string
	Types     []string
	Agent     string
	Limit     int
	Offset    int
}

func previewArgs(c *Call) string {
	if c == nil {
		return ""
	}
	return truncateText(InputText(c.Inputs), 2000)
}

func previewOut(c *Call) string {
	if c == nil {
		return ""
	}
	return truncateText(outputPreview(c.Output), 2000)
}

func (s *Store) sessionTimeline(ctx context.Context, pid, sessionID string) ([]*TimelineItem, error) {
	spans, verdicts, err := s.loadActivitySpans(ctx, pid, agentSpanFilter{ConversationIDs: []string{sessionID}})
	if err != nil {
		return nil, err
	}
	if len(spans)+len(verdicts) == 0 {
		return nil, ErrNotFound
	}
	ids := make(map[string]bool, len(spans))
	for _, sp := range spans {
		ids[sp.ID] = true
	}
	turns := []*agentSpan{}
	for _, sp := range spans {
		if sp.IsTurn {
			turns = append(turns, sp)
		}
	}
	if len(turns) == 0 {
		for _, sp := range spans {
			if sp.ParentID == "" || !ids[sp.ParentID] {
				turns = append(turns, sp)
			}
		}
	}
	need := map[string]bool{}
	for _, sp := range turns {
		need[sp.ID] = true
	}
	for _, sp := range spans {
		if sp.Op == OpChat || sp.Op == OpExecuteTool {
			need[sp.ID] = true
		}
	}
	calls, err := s.callsByID(ctx, pid, sortedKeys(need))
	if err != nil {
		return nil, err
	}
	var items []*TimelineItem
	add := func(typ string, sp *agentSpan, start int64, end *string, payload map[string]any) {
		it := &TimelineItem{Type: typ, SpanID: sp.ID, TraceID: sp.TraceID, AgentName: sp.AgentName, StartedAt: microsPtr(start),
			EndedAt: end, Payload: payload, startUS: start, order: timelineOrder[typ]}
		items = append(items, it)
	}
	endOf := func(sp *agentSpan) *string {
		if !sp.Ended {
			return nil
		}
		return microsPtr(sp.EndedUS)
	}
	durOf := func(sp *agentSpan) *float64 { return msPtr(sp.EndedUS-sp.StartedUS, sp.Ended) }
	for _, sp := range turns {
		c := calls[sp.ID]
		in, out := "", ""
		if c != nil {
			in, out = truncateText(inputPreview(c.Inputs), 4000), truncateText(outputPreview(c.Output), 4000)
		}
		add("turn", sp, sp.StartedUS, endOf(sp), map[string]any{"status": sp.statusCode(), "duration_ms": durOf(sp),
			"input_preview": in, "output_preview": out, "name": sp.displayName()})
		if in != "" {
			add("message", sp, sp.StartedUS, nil, map[string]any{"role": "user", "text": in})
		}
		if out != "" && sp.Ended {
			it := &TimelineItem{Type: "message", SpanID: sp.ID, TraceID: sp.TraceID, AgentName: sp.AgentName, StartedAt: microsPtr(sp.EndedUS),
				Payload: map[string]any{"role": "assistant", "text": out}, startUS: sp.EndedUS, order: 7}
			items = append(items, it)
		}
	}
	for _, sp := range spans {
		c := calls[sp.ID]
		switch sp.Op {
		case OpChat:
			p := map[string]any{"model": sp.RequestModel, "provider": sp.Provider, "input_tokens": int64(sp.InTok),
				"output_tokens": int64(sp.OutTok), "reasoning_tokens": int64(sp.ReasonTok), "duration_ms": durOf(sp), "status": sp.statusCode(),
				"output_preview": previewOut(c), "name": sp.displayName()}
			if sp.HasCost {
				p["cost_usd"] = sp.InCost + sp.OutCost
			} else {
				p["cost_usd"] = nil
			}
			var tcs []any
			if c != nil {
				for _, m := range OutputMessages(c.Output) {
					for _, tc := range m.ToolCalls {
						tcs = append(tcs, map[string]any{"id": tc.ID, "name": tc.Name, "arguments": truncateText(tc.Arguments, 2000)})
					}
				}
			}
			if tcs == nil {
				tcs = []any{}
			}
			p["tool_calls"] = tcs
			add("llm", sp, sp.StartedUS, endOf(sp), p)
		case OpExecuteTool:
			add("tool_call", sp, sp.StartedUS, endOf(sp), map[string]any{"tool_name": sp.ToolName, "tool_call_id": sp.ToolCallID,
				"arguments": previewArgs(c), "result": previewOut(c), "status": sp.statusCode(), "duration_ms": durOf(sp)})
		}
		if sp.Exception != nil {
			first := strings.SplitN(*sp.Exception, "\n", 2)[0]
			add("error", sp, sp.StartedUS, endOf(sp), map[string]any{"message": truncateText(first, 1000), "error_type": sp.errorType(),
				"operation": sp.Op, "name": sp.displayName()})
		}
	}
	steps, err := s.loadReasoning(ctx, pid, reasoningFilter{sessions: []string{sessionID}, withText: true})
	if err != nil {
		return nil, err
	}
	byID := map[string]*agentSpan{}
	for _, sp := range spans {
		byID[sp.ID] = sp
	}
	for _, st := range steps[sessionID] {
		if st.Duplicate {
			continue
		}
		sp := byID[st.SpanID]
		if sp == nil {
			continue
		}
		it := &TimelineItem{Type: "reasoning", SpanID: st.SpanID, TraceID: st.TraceID, AgentName: st.AgentName, StartedAt: st.StartedAt,
			Payload: map[string]any{"step_id": st.ID, "index": st.Index, "kind": st.Kind, "text": st.Text, "tokens": st.Tokens,
				"next_span_id": st.NextSpanID, "source": st.Source}, startUS: st.startUS, order: timelineOrder["reasoning"]}
		items = append(items, it)
	}
	edges, err := s.loadHandoffs(ctx, pid, HandoffsQuery{SessionID: sessionID})
	if err != nil {
		return nil, err
	}
	for _, h := range edges {
		agent := h.FromAgent
		it := &TimelineItem{Type: "handoff", SpanID: h.SourceSpanID, TraceID: h.TraceID, AgentName: agent, StartedAt: h.StartedAt,
			EndedAt: h.EndedAt, Payload: map[string]any{"handoff_id": h.ID, "from_agent": h.FromAgent, "to_agent": h.ToAgent, "kind": h.Kind,
				"source": h.Source, "status": h.Status, "from_span_id": h.FromSpanID, "to_span_id": h.ToSpanID, "duration_ms": h.DurationMS,
				"input_preview": h.InputPreview, "output_preview": h.OutputPreview}, startUS: h.startUS, order: timelineOrder["handoff"]}
		items = append(items, it)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].startUS != items[j].startUS {
			return items[i].startUS < items[j].startUS
		}
		if items[i].order != items[j].order {
			return items[i].order < items[j].order
		}
		return items[i].SpanID < items[j].SpanID
	})
	return items, nil
}

// SessionTimeline returns a session's ordered timeline.
func (s *Store) SessionTimeline(ctx context.Context, q SessionTimelineQuery) ([]*TimelineItem, int, error) {
	pid, err := refs.NormalizeProject(q.ProjectID)
	if err != nil {
		return nil, 0, &InvalidError{Msg: err.Error()}
	}
	types := map[string]bool{}
	for _, t := range q.Types {
		if t = strings.TrimSpace(t); t == "" {
			continue
		}
		if _, ok := timelineOrder[t]; !ok {
			return nil, 0, Invalidf("unknown timeline type %q", t)
		}
		types[t] = true
	}
	items, err := s.sessionTimeline(ctx, pid, q.SessionID)
	if err != nil {
		return nil, 0, err
	}
	out := items[:0]
	for _, it := range items {
		if (len(types) > 0 && !types[it.Type]) || (q.Agent != "" && it.AgentName != q.Agent) {
			continue
		}
		out = append(out, it)
	}
	return pageRows(out, q.Limit, q.Offset, 1000, 10000), len(out), nil
}

// ---------------------------------------------------------------- agents

// AgentSummary is one agent of /api/v1/agents: the agents-index statistics
// plus hand-offs, reasoning, detections and scores.
type AgentSummary struct {
	*AgentStats
	ReasoningTokens    int64          `json:"reasoning_tokens"`
	ReasoningStepCount int            `json:"reasoning_step_count"`
	HandoffsIn         int            `json:"handoffs_in"`
	HandoffsOut        int            `json:"handoffs_out"`
	SessionCount       int            `json:"session_count"`
	Tools              []string       `json:"tools"`
	Teams              []string       `json:"teams"`
	DetectionCounts    map[string]int `json:"detection_counts"`
	// DetectionRate is flagged tool calls / tool calls (ToolCallFlags).
	DetectionRate    *float64            `json:"detection_rate"`
	FlaggedToolCalls int                 `json:"flagged_tool_calls"`
	Scores           map[string]ScoreAgg `json:"scores"`
}

// AgentsV1Query filters /api/v1/agents.
type AgentsV1Query struct {
	ProjectID string
	Q         string
	TeamID    string
	From, To  string
	Limit     int
	Offset    int
	SortBy    string
}

func (s *Store) agentSummaries(ctx context.Context, pid string, after, before int64, only string) ([]*AgentSummary, error) {
	spans, _, err := s.loadActivitySpans(ctx, pid, agentSpanFilter{After: after, Before: before, AgentName: only})
	if err != nil {
		return nil, err
	}
	groups := map[string]*spanAgg{}
	tools := map[string]map[string]bool{}
	for _, sp := range spans {
		if sp.AgentName == "" {
			continue
		}
		g := groups[sp.AgentName]
		if g == nil {
			g = newSpanAgg()
			groups[sp.AgentName] = g
			tools[sp.AgentName] = map[string]bool{}
		}
		g.add(sp)
		if sp.Op == OpExecuteTool && sp.ToolName != "" {
			tools[sp.AgentName][sp.ToolName] = true
		}
	}
	// Agents that only appear as hand-off endpoints (graph nodes).
	hq := HandoffsQuery{Agent: only, From: "", To: ""}
	edges, err := s.loadHandoffs(ctx, pid, hq)
	if err != nil {
		return nil, err
	}
	in, out := map[string]int{}, map[string]int{}
	for _, h := range edges {
		if (after > 0 && h.startUS < after) || (before > 0 && h.startUS >= before) || h.Kind == HandoffReturn {
			continue
		}
		in[h.ToAgent]++
		out[h.FromAgent]++
		for _, a := range []string{h.FromAgent, h.ToAgent} {
			if (only == "" || a == only) && groups[a] == nil {
				groups[a] = newSpanAgg()
				tools[a] = map[string]bool{}
			}
		}
	}
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)
	dets, err := s.agentDetectionCounts(ctx, pid, names)
	if err != nil {
		return nil, err
	}
	flags, err := s.ToolCallFlags(ctx, DetectionQuery{ProjectID: pid, Agent: only, From: after, To: before})
	if err != nil {
		return nil, err
	}
	teams, err := s.sessionTeams(ctx, pid, nil)
	if err != nil {
		return nil, err
	}
	agentTeams := map[string]map[string]bool{}
	agentSessions := map[string]int{}
	for _, t := range teams {
		for _, a := range t.Agents {
			if agentTeams[a] == nil {
				agentTeams[a] = map[string]bool{}
			}
			agentTeams[a][t.ID] = true
			agentSessions[a]++
		}
	}
	scores, _, err := s.QueryScopeScores(ctx, ScopeScoresQuery{ProjectID: pid, Scope: ScopeAgent, Agent: only, Limit: math.MaxInt32})
	if err != nil {
		return nil, err
	}
	agg := aggregateScores(scores, func(sc ScopeScore) string { return sc.Agent })
	steps := map[string]int{}
	{
		cond, args := "", []any{pid}
		if only != "" {
			cond, args = " AND r.agent_name = ?", append(args, only)
		}
		rows, err := s.r.QueryContext(ctx, `SELECT r.agent_name, COUNT(*) FROM reasoning_steps r
			JOIN calls c ON c.project_id = r.project_id AND c.id = r.span_id
			WHERE r.project_id = ? AND r.source != 'input' AND c.deleted_at IS NULL`+cond+` GROUP BY r.agent_name`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var a string
			var n int
			if err := rows.Scan(&a, &n); err != nil {
				rows.Close()
				return nil, err
			}
			steps[a] = n
		}
		rows.Close()
	}
	res := make([]*AgentSummary, 0, len(names))
	for _, n := range names {
		g := groups[n]
		a := &AgentSummary{AgentStats: agentStatsFrom(pid, n, g, true), ReasoningTokens: int64(g.reason), HandoffsIn: in[n],
			HandoffsOut: out[n], SessionCount: agentSessions[n], Tools: sortedKeys(tools[n]), Teams: sortedKeys(agentTeams[n]),
			DetectionCounts: map[string]int{}, Scores: map[string]ScoreAgg{}, ReasoningStepCount: steps[n]}
		if a.SessionCount < a.ConversationCount {
			a.SessionCount = a.ConversationCount
		}
		if d := dets[n]; d != nil {
			a.DetectionCounts = d
		}
		if f, ok := flags[n]; ok {
			a.FlaggedToolCalls, a.DetectionRate = f.Flagged, f.Rate()
		}
		if sc := agg[n]; sc != nil {
			a.Scores = sc
		}
		res = append(res, a)
	}
	return res, nil
}

// QueryAgentsV1 lists agents with hand-off, reasoning, detection and score figures.
func (s *Store) QueryAgentsV1(ctx context.Context, q AgentsV1Query) ([]*AgentSummary, int, error) {
	pid, err := refs.NormalizeProject(q.ProjectID)
	if err != nil {
		return nil, 0, &InvalidError{Msg: err.Error()}
	}
	after, before, err := timeRange(&q.From, &q.To)
	if err != nil {
		return nil, 0, err
	}
	sb := ParseListSort(q.SortBy, ListSort{Field: "last_seen", Desc: true})
	extra := map[string]bool{"reasoning_tokens": true, "handoffs_in": true, "handoffs_out": true, "session_count": true, "detection_count": true}
	if !agentSortFields[sb.Field] && !extra[sb.Field] {
		return nil, 0, Invalidf("cannot sort agents by %q", sb.Field)
	}
	rows, err := s.agentSummaries(ctx, pid, after, before, "")
	if err != nil {
		return nil, 0, err
	}
	qq := strings.ToLower(strings.TrimSpace(q.Q))
	out := rows[:0]
	for _, a := range rows {
		if qq != "" && !strings.Contains(strings.ToLower(a.AgentName), qq) {
			continue
		}
		if q.TeamID != "" && !containsStr(a.Teams, q.TeamID) {
			continue
		}
		out = append(out, a)
	}
	sortRows(out, sb, func(a *AgentSummary, f string) any {
		switch f {
		case "reasoning_tokens":
			return a.ReasoningTokens
		case "handoffs_in":
			return int64(a.HandoffsIn)
		case "handoffs_out":
			return int64(a.HandoffsOut)
		case "session_count":
			return int64(a.SessionCount)
		case "detection_count":
			return int64(sumCounts(a.DetectionCounts))
		}
		return agentStatsField(a.AgentStats, f)
	}, func(a *AgentSummary) string { return a.AgentName })
	return pageRows(out, q.Limit, q.Offset, 100, 1000), len(out), nil
}

// AgentCounterpart counts hand-offs with one other agent.
type AgentCounterpart struct {
	Agent string `json:"agent"`
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// AgentSessionRef is a session an agent took part in.
type AgentSessionRef struct {
	SessionID    string  `json:"session_id"`
	TeamID       string  `json:"team_id"`
	LastActivity *string `json:"last_activity"`
	lastUS       int64
}

// AgentToolCount counts an agent's calls of one tool.
type AgentToolCount struct {
	Tool   string `json:"tool"`
	Count  int    `json:"count"`
	Errors int    `json:"errors"`
}

// AgentDetail is GET /api/v1/agents/{name}.
type AgentDetail struct {
	*AgentSummary
	ToolCounts     []AgentToolCount   `json:"tool_counts"`
	HandoffsFrom   []AgentCounterpart `json:"handoffs_from"`
	HandoffsTo     []AgentCounterpart `json:"handoffs_to"`
	RecentSessions []AgentSessionRef  `json:"recent_sessions"`
}

// GetAgentV1 returns one agent with its tools, hand-off partners and sessions.
func (s *Store) GetAgentV1(ctx context.Context, projectID, name string) (*AgentDetail, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	rows, err := s.agentSummaries(ctx, pid, 0, 0, name)
	if err != nil {
		return nil, err
	}
	var a *AgentSummary
	for _, r := range rows {
		if r.AgentName == name {
			a = r
		}
	}
	if a == nil {
		return nil, ErrNotFound
	}
	d := &AgentDetail{AgentSummary: a, ToolCounts: []AgentToolCount{}, HandoffsFrom: []AgentCounterpart{}, HandoffsTo: []AgentCounterpart{},
		RecentSessions: []AgentSessionRef{}}
	spans, _, err := s.loadActivitySpans(ctx, pid, agentSpanFilter{AgentName: name})
	if err != nil {
		return nil, err
	}
	tc := map[string]*AgentToolCount{}
	sess := map[string]int64{}
	for _, sp := range spans {
		if sp.Op == OpExecuteTool && sp.ToolName != "" {
			t := tc[sp.ToolName]
			if t == nil {
				t = &AgentToolCount{Tool: sp.ToolName}
				tc[sp.ToolName] = t
			}
			t.Count++
			if sp.Exception != nil {
				t.Errors++
			}
		}
		if sp.ConvID != "" {
			end := sp.StartedUS
			if sp.Ended {
				end = sp.EndedUS
			}
			if end > sess[sp.ConvID] {
				sess[sp.ConvID] = end
			}
		}
	}
	for _, t := range tc {
		d.ToolCounts = append(d.ToolCounts, *t)
	}
	sort.Slice(d.ToolCounts, func(i, j int) bool {
		if d.ToolCounts[i].Count != d.ToolCounts[j].Count {
			return d.ToolCounts[i].Count > d.ToolCounts[j].Count
		}
		return d.ToolCounts[i].Tool < d.ToolCounts[j].Tool
	})
	edges, err := s.loadHandoffs(ctx, pid, HandoffsQuery{Agent: name})
	if err != nil {
		return nil, err
	}
	from, to := map[[2]string]int{}, map[[2]string]int{}
	for _, h := range edges {
		if h.ToAgent == name {
			from[[2]string{h.FromAgent, h.Kind}]++
		}
		if h.FromAgent == name {
			to[[2]string{h.ToAgent, h.Kind}]++
		}
		if h.SessionID != "" {
			if v := h.endUS; v > sess[h.SessionID] {
				sess[h.SessionID] = v
			} else if _, ok := sess[h.SessionID]; !ok {
				sess[h.SessionID] = h.startUS
			}
		}
	}
	for k, n := range from {
		d.HandoffsFrom = append(d.HandoffsFrom, AgentCounterpart{Agent: k[0], Kind: k[1], Count: n})
	}
	for k, n := range to {
		d.HandoffsTo = append(d.HandoffsTo, AgentCounterpart{Agent: k[0], Kind: k[1], Count: n})
	}
	for _, l := range [][]AgentCounterpart{d.HandoffsFrom, d.HandoffsTo} {
		sort.Slice(l, func(i, j int) bool {
			if l[i].Count != l[j].Count {
				return l[i].Count > l[j].Count
			}
			return l[i].Agent+l[i].Kind < l[j].Agent+l[j].Kind
		})
	}
	sids := make([]string, 0, len(sess))
	for sid := range sess {
		sids = append(sids, sid)
	}
	sort.Strings(sids)
	teams, err := s.sessionTeams(ctx, pid, sids)
	if err != nil {
		return nil, err
	}
	for _, sid := range sids {
		r := AgentSessionRef{SessionID: sid, LastActivity: microsPtr(sess[sid]), lastUS: sess[sid]}
		if t := teams[sid]; t != nil {
			r.TeamID = t.ID
		}
		d.RecentSessions = append(d.RecentSessions, r)
	}
	sort.SliceStable(d.RecentSessions, func(i, j int) bool { return d.RecentSessions[i].lastUS > d.RecentSessions[j].lastUS })
	if len(d.RecentSessions) > 50 {
		d.RecentSessions = d.RecentSessions[:50]
	}
	if d.SessionCount < len(sids) {
		d.SessionCount = len(sids)
	}
	return d, nil
}

// ---------------------------------------------------------------- teams API

// TeamSummary is one team of /api/v1/teams.
type TeamSummary struct {
	TeamID          string         `json:"team_id"`
	Name            string         `json:"name"`
	Auto            bool           `json:"auto"`
	Agents          []string       `json:"agents"`
	SessionCount    int            `json:"session_count"`
	ErrorCount      int            `json:"error_session_count"`
	HandoffCount    int            `json:"handoff_count"`
	SpanCount       int            `json:"span_count"`
	InputTokens     int64          `json:"input_tokens"`
	OutputTokens    int64          `json:"output_tokens"`
	CostUSD         *float64       `json:"cost_usd"`
	FirstSeen       *string        `json:"first_seen"`
	LastActivity    *string        `json:"last_activity"`
	DetectionCount  map[string]int `json:"detection_count"`
	firstUS, lastUS int64
}

// TeamsQuery filters /api/v1/teams.
type TeamsQuery struct {
	ProjectID string
	Q         string
	Agent     string
	From, To  string
	Limit     int
	Offset    int
	SortBy    string
}

func teamsFrom(sessions []*SessionSummary) map[string]*TeamSummary {
	out := map[string]*TeamSummary{}
	for _, ss := range sessions {
		if ss.TeamID == "" {
			continue
		}
		t := out[ss.TeamID]
		if t == nil {
			t = &TeamSummary{TeamID: ss.TeamID, Name: ss.TeamName, Auto: strings.HasPrefix(ss.TeamID, "auto:"), DetectionCount: map[string]int{}}
			out[ss.TeamID] = t
		}
		t.SessionCount++
		if ss.Status == "error" {
			t.ErrorCount++
		}
		t.HandoffCount += ss.HandoffCount
		t.SpanCount += ss.SpanCount
		t.InputTokens += ss.InputTokens
		t.OutputTokens += ss.OutputTokens
		if ss.CostUSD != nil {
			c := *ss.CostUSD
			if t.CostUSD != nil {
				c += *t.CostUSD
			}
			t.CostUSD = fptr(math.Round(c*1e8) / 1e8)
		}
		for _, a := range ss.Agents {
			if !containsStr(t.Agents, a) {
				t.Agents = append(t.Agents, a)
			}
		}
		for k, v := range ss.DetectionCount {
			t.DetectionCount[k] += v
		}
		if t.firstUS == 0 || ss.firstUS < t.firstUS {
			t.firstUS = ss.firstUS
		}
		if ss.lastUS > t.lastUS {
			t.lastUS = ss.lastUS
		}
	}
	for _, t := range out {
		sort.Strings(t.Agents)
		t.FirstSeen, t.LastActivity = microsPtr(t.firstUS), microsPtr(t.lastUS)
	}
	return out
}

// QueryTeams lists teams, most recently active first by default.
func (s *Store) QueryTeams(ctx context.Context, q TeamsQuery) ([]*TeamSummary, int, error) {
	pid, err := refs.NormalizeProject(q.ProjectID)
	if err != nil {
		return nil, 0, &InvalidError{Msg: err.Error()}
	}
	sb := ParseListSort(q.SortBy, ListSort{Field: "last_activity", Desc: true})
	switch sb.Field {
	case "last_activity", "first_seen", "session_count", "handoff_count", "error_session_count", "cost_usd", "name", "team_id":
	default:
		return nil, 0, Invalidf("cannot sort teams by %q", sb.Field)
	}
	after, before, err := timeRange(&q.From, &q.To)
	if err != nil {
		return nil, 0, err
	}
	var ids []string
	if after > 0 || before > 0 {
		if ids, err = s.sessionsWithin(ctx, pid, after, before); err != nil {
			return nil, 0, err
		}
	}
	sessions, err := s.buildSessions(ctx, pid, ids)
	if err != nil {
		return nil, 0, err
	}
	teams := teamsFrom(sessions)
	qq := strings.ToLower(strings.TrimSpace(q.Q))
	out := make([]*TeamSummary, 0, len(teams))
	for _, t := range teams {
		if q.Agent != "" && !containsStr(t.Agents, q.Agent) {
			continue
		}
		if qq != "" && !strings.Contains(strings.ToLower(t.TeamID+"\n"+t.Name+"\n"+strings.Join(t.Agents, "\n")), qq) {
			continue
		}
		out = append(out, t)
	}
	sortRows(out, sb, func(t *TeamSummary, f string) any {
		switch f {
		case "last_activity":
			return t.lastUS
		case "first_seen":
			return t.firstUS
		case "session_count":
			return int64(t.SessionCount)
		case "handoff_count":
			return int64(t.HandoffCount)
		case "error_session_count":
			return int64(t.ErrorCount)
		case "cost_usd":
			return derefF(t.CostUSD)
		case "name":
			return t.Name
		}
		return t.TeamID
	}, func(t *TeamSummary) string { return t.TeamID })
	return pageRows(out, q.Limit, q.Offset, 100, 1000), len(out), nil
}

// TeamAgentRow is one agent of a team's agent table.
type TeamAgentRow struct {
	Agent       string         `json:"agent"`
	Calls       int64          `json:"calls"`
	Errors      int64          `json:"errors"`
	ToolCalls   int64          `json:"tool_calls"`
	Tokens      int64          `json:"tokens"`
	CostUSD     *float64       `json:"cost_usd"`
	Sessions    int            `json:"sessions"`
	HandoffsIn  int            `json:"handoffs_in"`
	HandoffsOut int            `json:"handoffs_out"`
	Detections  map[string]int `json:"detections"`
}

// TeamDetail is GET /api/v1/teams/{id}.
type TeamDetail struct {
	*TeamSummary
	AgentStats     []TeamAgentRow    `json:"agent_stats"`
	Graph          *Graph            `json:"graph"`
	HandoffStats   []*GraphEdge      `json:"handoff_stats"`
	RecentSessions []*SessionSummary `json:"recent_sessions"`
}

// GetAgentTeam returns a team with its aggregated graph, per-hand-off
// statistics and recent sessions.
func (s *Store) GetAgentTeam(ctx context.Context, projectID, teamID string) (*TeamDetail, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	ids, _, err := s.teamSessions(ctx, pid, teamID)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, ErrNotFound
	}
	sessions, err := s.buildSessions(ctx, pid, ids)
	if err != nil {
		return nil, err
	}
	teams := teamsFrom(sessions)
	t := teams[teamID]
	if t == nil {
		return nil, ErrNotFound
	}
	spans, _, err := s.loadActivitySpans(ctx, pid, agentSpanFilter{ConversationIDs: ids})
	if err != nil {
		return nil, err
	}
	edges, err := s.loadHandoffs(ctx, pid, HandoffsQuery{SessionIDs: ids})
	if err != nil {
		return nil, err
	}
	hs, err := s.handoffScores(ctx, pid, ids)
	if err != nil {
		return nil, err
	}
	dets, err := s.agentDetectionCounts(ctx, pid, t.Agents)
	if err != nil {
		return nil, err
	}
	g := buildGraph(spans, edges, dets, hs)
	d := &TeamDetail{TeamSummary: t, Graph: g, HandoffStats: []*GraphEdge{}, AgentStats: []TeamAgentRow{}}
	in, out := map[string]int{}, map[string]int{}
	for _, e := range g.Edges {
		if e.Kind == HandoffReturn {
			continue
		}
		d.HandoffStats = append(d.HandoffStats, e)
		in[e.To] += e.Count
		out[e.From] += e.Count
	}
	sort.SliceStable(d.HandoffStats, func(i, j int) bool { return d.HandoffStats[i].Count > d.HandoffStats[j].Count })
	for _, n := range g.Nodes {
		d.AgentStats = append(d.AgentStats, TeamAgentRow{Agent: n.Agent, Calls: n.Calls, Errors: n.Errors, ToolCalls: n.ToolCalls,
			Tokens: n.Tokens, CostUSD: n.CostUSD, Sessions: n.Sessions, HandoffsIn: in[n.Agent], HandoffsOut: out[n.Agent], Detections: n.Detections})
	}
	sortRows(sessions, ListSort{Field: "last_activity", Desc: true}, sessionField, func(ss *SessionSummary) string { return ss.SessionID })
	if len(sessions) > 50 {
		sessions = sessions[:50]
	}
	if err := s.fillTitles(ctx, pid, sessions); err != nil {
		return nil, err
	}
	for _, ss := range sessions {
		ss.TraceIDs = nil
	}
	d.RecentSessions = sessions
	return d, nil
}

// sessionStatusMatches applies the status filter. The values mean the same
// on Sessions and Traces (docs/weft/guides/filters.md): error, the session
// itself failed (a top-level span); failed_step, it ended without failing
// but a step in it failed; ok, it ended and nothing in it failed; running,
// a top-level span has not ended; unknown, verdicts only.
func sessionStatusMatches(ss *SessionSummary, status string) bool {
	switch status {
	case "":
		return true
	case SessionStatusFailedStep:
		return ss.Status == "ok" && ss.ErrorCount > 0
	case "ok":
		return ss.Status == "ok" && ss.ErrorCount == 0
	}
	return ss.Status == status
}
