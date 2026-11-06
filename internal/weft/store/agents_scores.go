package store

// Scope-aware scores (docs/weft/DESIGN.md §6): feedback rows whose payload
// carries a scope and the target ids. Any feedback whose payload has a valid
// "scope" counts, whatever its feedback_type, so SDKs can write scores with
// /feedback/create; the server's scope scorers use feedback_type
// "weft.score.<scorer>". Call-level scorer results (weft.scorer.*) are
// reported as span-scope scores when a session is named.

import (
	"context"
	"database/sql"
	"sort"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// Score scopes.
const (
	ScopeSession = "session"
	ScopeAgent   = "agent"
	ScopeHandoff = "handoff"
	ScopeTurn    = "turn"
	ScopeSpan    = "span"
)

// ScoreScopes lists the valid scopes.
var ScoreScopes = map[string]bool{ScopeSession: true, ScopeAgent: true, ScopeHandoff: true, ScopeTurn: true, ScopeSpan: true}

// ScopeScore is one score of /api/v1/scores.
type ScopeScore struct {
	ID           string   `json:"id"`
	Scope        string   `json:"scope"`
	SessionID    string   `json:"session_id,omitempty"`
	Agent        string   `json:"agent,omitempty"`
	HandoffID    string   `json:"handoff_id,omitempty"`
	TurnID       string   `json:"turn_id,omitempty"`
	SpanID       string   `json:"span_id,omitempty"`
	Scorer       string   `json:"scorer"`
	Value        *float64 `json:"value"`
	Reason       string   `json:"reason,omitempty"`
	Output       any      `json:"output,omitempty"`
	Model        string   `json:"model,omitempty"`
	Ref          string   `json:"ref"`
	FeedbackType string   `json:"feedback_type"`
	Creator      *string  `json:"creator"`
	CreatedAt    string   `json:"created_at"`
	createdUS    int64
}

// ScoreValue reduces a scorer output to a number: numbers as they are,
// booleans as 1 or 0, and for objects their value, score, safe, passed,
// pass or correct field, else their first numeric or boolean field (by key
// order).
func ScoreValue(v any) *float64 {
	f := func(x float64) *float64 { return &x }
	switch t := v.(type) {
	case bool:
		if t {
			return f(1)
		}
		return f(0)
	case map[string]any:
		for _, k := range []string{"value", "score", "safe", "passed", "pass", "correct"} {
			if x, ok := t[k]; ok {
				if r := ScoreValue(x); r != nil {
					return r
				}
			}
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			switch t[k].(type) {
			case bool:
				return ScoreValue(t[k])
			}
			if x, ok := toFloat(t[k]); ok {
				return f(x)
			}
		}
		return nil
	case string, nil:
		return nil
	}
	if x, ok := toFloat(v); ok {
		return f(x)
	}
	return nil
}

func scoreFromFeedback(id, ref, ftype, payload string, creator sql.NullString, created int64) (ScopeScore, bool) {
	p := decodeMap(payload)
	sc := ScopeScore{ID: id, Ref: ref, FeedbackType: ftype, Creator: strPtr(creator), CreatedAt: FormatMicros(created), createdUS: created}
	str := func(k string) string { s, _ := p[k].(string); return s }
	sc.Scope = str("scope")
	if sc.Scope == "" && strings.HasPrefix(ftype, "weft.scorer.") {
		sc.Scope, sc.Scorer = ScopeSpan, strings.TrimPrefix(ftype, "weft.scorer.")
		if r, err := refs.Parse(ref); err == nil && r.Kind == refs.KindSpan {
			sc.SpanID = r.Name
		}
		sc.Output = p["output"]
		sc.Value = ScoreValue(p["output"])
		if m := asMap(p["output"]); m != nil {
			sc.Reason = firstStr(m["reasoning"], m["reason"])
		}
		return sc, true
	}
	if !ScoreScopes[sc.Scope] {
		return sc, false
	}
	sc.SessionID, sc.Agent, sc.HandoffID, sc.TurnID, sc.SpanID = str("session_id"), str("agent"), str("handoff_id"), str("turn_id"), str("span_id")
	sc.Scorer, sc.Reason, sc.Model = str("scorer"), str("reason"), str("model")
	if sc.Scorer == "" {
		sc.Scorer = strings.TrimPrefix(strings.TrimPrefix(ftype, "weft.score."), "weft.scorer.")
	}
	sc.Output = p["output"]
	if v := ScoreValue(p["value"]); v != nil {
		sc.Value = v
	} else {
		sc.Value = ScoreValue(p["output"])
	}
	return sc, true
}

// ScopeScoresQuery filters /api/v1/scores.
type ScopeScoresQuery struct {
	ProjectID  string
	Scope      string
	SessionID  string
	SessionIDs []string
	Agent      string
	HandoffID  string
	Scorer     string
	Limit      int
	Offset     int
}

// QueryScopeScores returns scores newest first, and the total before paging.
func (s *Store) QueryScopeScores(ctx context.Context, q ScopeScoresQuery) ([]ScopeScore, int, error) {
	pid, err := refs.NormalizeProject(q.ProjectID)
	if err != nil {
		return nil, 0, &InvalidError{Msg: err.Error()}
	}
	if q.Scope != "" && !ScoreScopes[q.Scope] {
		return nil, 0, Invalidf("scope must be one of session, agent, handoff, turn, span")
	}
	where := []string{"project_id = ?", "json_extract(payload, '$.scope') IN ('session','agent','handoff','turn','span')"}
	args := []any{pid}
	for _, f := range []struct{ path, val string }{{"scope", q.Scope}, {"session_id", q.SessionID}, {"agent", q.Agent},
		{"handoff_id", q.HandoffID}, {"scorer", q.Scorer}} {
		if f.val != "" {
			where = append(where, "json_extract(payload, '$."+f.path+"') = ?")
			args = append(args, f.val)
		}
	}
	if q.SessionIDs != nil {
		if len(q.SessionIDs) == 0 {
			return []ScopeScore{}, 0, nil
		}
		c, a := inList("json_extract(payload, '$.session_id')", q.SessionIDs)
		where = append(where, c)
		args = append(args, a...)
	}
	out, err := s.readScores(ctx, `SELECT id, ref, feedback_type, payload, creator, created_at FROM feedback WHERE `+
		strings.Join(where, " AND "), args...)
	if err != nil {
		return nil, 0, err
	}
	// Call-level scorer results on the session's spans.
	if q.SessionID != "" && (q.Scope == "" || q.Scope == ScopeSpan) && q.Agent == "" && q.HandoffID == "" {
		spans, err := s.sessionSpanAgents(ctx, pid, q.SessionID)
		if err != nil {
			return nil, 0, err
		}
		ids := make([]string, 0, len(spans))
		for id := range spans {
			ids = append(ids, refs.SpanRef(pid, id))
		}
		sort.Strings(ids)
		for start := 0; start < len(ids); start += 400 {
			end := min(start+400, len(ids))
			c, a := inList("ref", ids[start:end])
			args := append([]any{pid}, a...)
			cond := ""
			if q.Scorer != "" {
				cond = " AND feedback_type = ?"
				args = append(args, "weft.scorer."+q.Scorer)
			}
			more, err := s.readScores(ctx, `SELECT id, ref, feedback_type, payload, creator, created_at FROM feedback
				WHERE project_id = ? AND feedback_type LIKE 'weft.scorer.%' AND `+c+cond, args...)
			if err != nil {
				return nil, 0, err
			}
			for _, sc := range more {
				sc.SessionID, sc.Agent = q.SessionID, spans[sc.SpanID]
				out = append(out, sc)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].createdUS > out[j].createdUS })
	total := len(out)
	lim := q.Limit
	if lim <= 0 {
		lim = 100
	}
	return page(out, &lim, q.Offset, 100), total, nil
}

func (s *Store) readScores(ctx context.Context, query string, args ...any) ([]ScopeScore, error) {
	rows, err := s.r.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScopeScore
	for rows.Next() {
		var id, ref, ft, payload string
		var creator sql.NullString
		var created int64
		if err := rows.Scan(&id, &ref, &ft, &payload, &creator, &created); err != nil {
			return nil, err
		}
		if sc, ok := scoreFromFeedback(id, ref, ft, payload, creator, created); ok {
			out = append(out, sc)
		}
	}
	return out, rows.Err()
}

// sessionSpanAgents maps the session's span ids to their agent names.
func (s *Store) sessionSpanAgents(ctx context.Context, pid, sessionID string) (map[string]string, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT id, agent_name FROM agent_spans WHERE project_id = ? AND conversation_id = ?`, pid, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, a string
		if err := rows.Scan(&id, &a); err != nil {
			return nil, err
		}
		out[id] = a
	}
	return out, rows.Err()
}

// ScoreAgg is the mean of one scorer's numeric scores.
type ScoreAgg struct {
	Mean  *float64 `json:"mean"`
	Count int      `json:"count"`
}

// aggregateScores averages scores per key and scorer.
func aggregateScores(scores []ScopeScore, key func(ScopeScore) string) map[string]map[string]ScoreAgg {
	sums := map[string]map[string][2]float64{}
	for _, sc := range scores {
		k := key(sc)
		if k == "" {
			continue
		}
		m := sums[k]
		if m == nil {
			m = map[string][2]float64{}
			sums[k] = m
		}
		cur := m[sc.Scorer]
		if sc.Value != nil {
			cur[0] += *sc.Value
			cur[1]++
		}
		m[sc.Scorer] = cur
	}
	out := map[string]map[string]ScoreAgg{}
	for k, m := range sums {
		o := map[string]ScoreAgg{}
		for scorer, v := range m {
			a := ScoreAgg{Count: int(v[1])}
			if v[1] > 0 {
				mean := v[0] / v[1]
				a.Mean = &mean
			}
			o[scorer] = a
		}
		out[k] = o
	}
	return out
}

// ScopeScoreCreate stores one scope score.
type ScopeScoreCreate struct {
	ProjectID   string
	Scope       string
	SessionID   string
	Agent       string
	HandoffID   string
	TurnID      string
	SpanID      string
	Scorer      string
	Value       *float64
	Reason      string
	Output      any
	Model       string
	Creator     *string
	UserID      *string
	RunnableRef *string
	CallRef     *string
	// TriggerRef is the monitor that produced the score, if any.
	TriggerRef *string
}

// scoreAnchor is the span a scope score is attached to (its ref).
func (s *Store) scoreAnchor(ctx context.Context, pid string, c *ScopeScoreCreate) (string, error) {
	var id string
	var err error
	switch c.Scope {
	case ScopeSpan:
		id = c.SpanID
	case ScopeTurn:
		id = c.TurnID
	case ScopeHandoff:
		var sess string
		err = s.r.QueryRowContext(ctx, `SELECT source_span_id, session_id FROM agent_handoffs WHERE project_id = ? AND id = ?`, pid, c.HandoffID).Scan(&id, &sess)
		if err == sql.ErrNoRows {
			return "", Invalidf("unknown handoff_id %q", c.HandoffID)
		}
		if c.SessionID == "" {
			c.SessionID = sess
		}
	case ScopeAgent:
		err = s.r.QueryRowContext(ctx, `SELECT s.id FROM agent_spans s JOIN calls c ON c.project_id = s.project_id AND c.id = s.id
			WHERE s.project_id = ? AND s.conversation_id = ? AND s.agent_name = ? AND c.deleted_at IS NULL
			ORDER BY s.own_agent DESC, c.started_at LIMIT 1`, pid, c.SessionID, c.Agent).Scan(&id)
		if err == sql.ErrNoRows {
			return "", Invalidf("agent %q has no spans in session %q", c.Agent, c.SessionID)
		}
	case ScopeSession:
		err = s.r.QueryRowContext(ctx, `SELECT s.id FROM agent_spans s JOIN calls c ON c.project_id = s.project_id AND c.id = s.id
			WHERE s.project_id = ? AND s.conversation_id = ? AND c.deleted_at IS NULL ORDER BY c.started_at LIMIT 1`, pid, c.SessionID).Scan(&id)
		if err == sql.ErrNoRows {
			return "", Invalidf("unknown session %q", c.SessionID)
		}
	default:
		return "", Invalidf("scope must be one of session, agent, handoff, turn, span")
	}
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", Invalidf("a %s score needs its target id", c.Scope)
	}
	return refs.SpanRef(pid, id), nil
}

// CreateScopeScore stores a scope score as feedback on its anchor span.
func (s *Store) CreateScopeScore(ctx context.Context, c ScopeScoreCreate) (*Feedback, error) {
	pid, err := refs.NormalizeProject(c.ProjectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	if c.Scorer == "" || len(c.Scorer) > 200 {
		return nil, Invalidf("scorer is required")
	}
	if !ValidFeedbackType(c.Scorer) {
		return nil, Invalidf("scorer may only contain letters, digits and _ . : * - (it becomes the feedback type weft.score.<scorer>)")
	}
	ref, err := s.scoreAnchor(ctx, pid, &c)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"scope": c.Scope, "scorer": c.Scorer}
	for k, v := range map[string]string{"session_id": c.SessionID, "agent": c.Agent, "handoff_id": c.HandoffID, "turn_id": c.TurnID,
		"span_id": c.SpanID, "reason": c.Reason, "model": c.Model} {
		if v != "" {
			payload[k] = v
		}
	}
	if c.Value != nil {
		payload["value"] = *c.Value
	}
	if c.Output != nil {
		payload["output"] = c.Output
	}
	return s.CreateFeedback(ctx, FeedbackCreate{ProjectID: pid, Ref: ref, FeedbackType: "weft.score." + c.Scorer, Payload: payload,
		Creator: c.Creator, UserID: c.UserID, RunnableRef: c.RunnableRef, CallRef: c.CallRef, TriggerRef: c.TriggerRef})
}
