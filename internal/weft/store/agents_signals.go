package store

// Agent signals: turn-scoped tags and ratings, as in Weave's Agents
// "Signals" tab.
//
// A signal result is ordinary feedback on a turn's root call. The scorer
// fields of Weave's feedback batch schema (scorer_ratings, scorer_tags and
// their confidences and reasons) and the span_* denormalised turn fields are
// kept in the feedback payload under the same keys, so /feedback/query
// returns them and the agent chat views lift them back into the top-level
// AgentChatFeedback fields.
//
// This file also implements the signal_filters and insight_filters of
// /agents/spans/query and /agents/spans/stats.

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// SignalPreset is a built-in signal definition.
type SignalPreset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"` // "tag" or "rating"
	Description string `json:"description"`
}

// SignalPresets mirrors Weave's turn presets (four tags, four ratings) plus
// sentiment and task-success ratings.
var SignalPresets = []SignalPreset{
	{"user_frustration", "User Frustration", "tag",
		"The user shows frustration, anger, confusion or dissatisfaction with the agent (complaints, repeated rephrasing, sarcasm, giving up)."},
	{"jailbreak", "Malicious Intent (Jailbreaking)", "tag",
		"The user tries to jailbreak the system, extract restricted content, perform prompt injection, use role-play exploits, or otherwise get the agent to ignore its guardrails."},
	{"nsfw", "NSFW", "tag",
		"The user input or the agent output contains explicit sexual content, graphic violence, or other material inappropriate for a workplace."},
	{"low_quality_response", "Low Quality Response", "tag",
		"The agent output is factually wrong, off-topic, evasive, repetitive, refuses without justification, or otherwise fails to address the request."},
	{"user_satisfaction", "User Satisfaction", "rating",
		"How satisfied the user is: 1 for clear satisfaction (positive feedback, follow-up engagement, task completion), 0 for clear dissatisfaction (complaints, repeated rephrasing, abandonment)."},
	{"user_good_intent", "User Good Intent", "rating",
		"Whether the user's intent is benign and legitimate (1) or a jailbreak attempt, harmful request or prompt injection (0)."},
	{"safe_for_work", "Safe-for-Work", "rating",
		"Whether the conversation suits a professional setting (1) or contains explicit, violent or otherwise inappropriate content (0)."},
	{"response_quality", "Response Quality", "rating",
		"Whether the agent's response is accurate, complete and directly addresses the request (1) or not (0)."},
	{"sentiment", "Sentiment", "rating",
		"The overall sentiment the user expresses in this turn, from 0 (very negative) through 0.5 (neutral) to 1 (very positive)."},
	{"task_success", "Task Success", "rating",
		"Whether the agent completed the task the user asked for in this turn: 1 for fully completed, 0.5 for partially, 0 for failed or not attempted."},
}

// PresetByID finds a preset.
func PresetByID(id string) (SignalPreset, bool) {
	for _, p := range SignalPresets {
		if p.ID == id {
			return p, true
		}
	}
	return SignalPreset{}, false
}

// ---------------------------------------------------------------- normalisation

const (
	maxSignalTags    = 10
	maxSignalTagLen  = 36
	maxSignalReason  = 256
	maxSignalRatings = 32
)

var signalIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,35}$`)

// ValidSignalID reports whether id can name a signal (and its tag).
func ValidSignalID(id string) bool { return signalIDRe.MatchString(id) }

// NormalizeSignalReason drops control and markup characters, collapses
// whitespace and truncates to 256 characters.
func NormalizeSignalReason(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case r == '<' || r == '>' || r == '`' || r == '�':
			continue
		case unicode.IsSpace(r):
			if !space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = true
			continue
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			continue
		}
		space = false
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(out) > maxSignalReason {
		out = string([]rune(out)[:maxSignalReason])
	}
	return out
}

// NormalizeSignalTags lowercases, trims and de-duplicates tags, keeping at
// most ten. A tag longer than 36 characters rejects the whole result.
func NormalizeSignalTags(tags []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		if utf8.RuneCountInString(t) > maxSignalTagLen {
			return nil, Invalidf("tag %q is longer than %d characters", t, maxSignalTagLen)
		}
		seen[t] = true
		if len(out) < maxSignalTags {
			out = append(out, t)
		}
	}
	return out, nil
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// SignalScores is the scorer part of Weave's feedback batch schema.
type SignalScores struct {
	ScorerRatings           map[string]float64 `json:"scorer_ratings,omitempty"`
	ScorerRatingConfidences map[string]float64 `json:"scorer_rating_confidences,omitempty"`
	ScorerRatingReasons     map[string]string  `json:"scorer_rating_reasons,omitempty"`
	ScorerTags              []string           `json:"scorer_tags,omitempty"`
	ScorerTagConfidences    map[string]float64 `json:"scorer_tag_confidences,omitempty"`
	ScorerTagReasons        map[string]string  `json:"scorer_tag_reasons,omitempty"`
}

// Empty reports whether no rating or tag is set.
func (sc *SignalScores) Empty() bool { return len(sc.ScorerRatings) == 0 && len(sc.ScorerTags) == 0 }

// Normalize validates and normalises the scores in place.
func (sc *SignalScores) Normalize() error {
	if len(sc.ScorerRatings) > maxSignalRatings {
		return Invalidf("too many scorer_ratings (max %d)", maxSignalRatings)
	}
	for k, v := range sc.ScorerRatings {
		if strings.TrimSpace(k) == "" || len(k) > 128 {
			return Invalidf("invalid rating name %q", k)
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return Invalidf("rating %q is not a finite number", k)
		}
	}
	tags, err := NormalizeSignalTags(sc.ScorerTags)
	if err != nil {
		return err
	}
	sc.ScorerTags = tags
	normConf := func(m map[string]float64, lower bool) map[string]float64 {
		out := map[string]float64{}
		for k, v := range m {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			if lower {
				k = strings.ToLower(strings.TrimSpace(k))
			}
			out[k] = clamp01(v)
		}
		return out
	}
	normReason := func(m map[string]string, lower bool) map[string]string {
		out := map[string]string{}
		for k, v := range m {
			if lower {
				k = strings.ToLower(strings.TrimSpace(k))
			}
			if r := NormalizeSignalReason(v); r != "" {
				out[k] = r
			}
		}
		return out
	}
	sc.ScorerRatingConfidences = normConf(sc.ScorerRatingConfidences, false)
	sc.ScorerTagConfidences = normConf(sc.ScorerTagConfidences, true)
	sc.ScorerRatingReasons = normReason(sc.ScorerRatingReasons, false)
	sc.ScorerTagReasons = normReason(sc.ScorerTagReasons, true)
	return nil
}

// toPayload writes the scores into a feedback payload.
func (sc *SignalScores) toPayload(p map[string]any) {
	put := func(k string, v any, n int) {
		if n > 0 {
			p[k] = v
		}
	}
	put("scorer_ratings", sc.ScorerRatings, len(sc.ScorerRatings))
	put("scorer_rating_confidences", sc.ScorerRatingConfidences, len(sc.ScorerRatingConfidences))
	put("scorer_rating_reasons", sc.ScorerRatingReasons, len(sc.ScorerRatingReasons))
	p["scorer_tags"] = sc.ScorerTags
	if sc.ScorerTags == nil {
		p["scorer_tags"] = []string{}
	}
	put("scorer_tag_confidences", sc.ScorerTagConfidences, len(sc.ScorerTagConfidences))
	put("scorer_tag_reasons", sc.ScorerTagReasons, len(sc.ScorerTagReasons))
}

// signalScoresOf reads scorer_* fields from a feedback payload.
func signalScoresOf(payload any) (SignalScores, bool) {
	p := asMap(payload)
	var sc SignalScores
	if p == nil {
		return sc, false
	}
	floats := func(k string) map[string]float64 {
		m := asMap(p[k])
		if m == nil {
			return nil
		}
		out := map[string]float64{}
		for name, v := range m {
			if x, ok := toFloat(v); ok {
				out[name] = x
			} else if b, ok := v.(bool); ok {
				out[name] = map[bool]float64{true: 1, false: 0}[b]
			}
		}
		return out
	}
	strs := func(k string) map[string]string {
		m := asMap(p[k])
		if m == nil {
			return nil
		}
		out := map[string]string{}
		for name, v := range m {
			if s, ok := v.(string); ok {
				out[name] = s
			}
		}
		return out
	}
	sc.ScorerRatings = floats("scorer_ratings")
	sc.ScorerRatingConfidences = floats("scorer_rating_confidences")
	sc.ScorerRatingReasons = strs("scorer_rating_reasons")
	sc.ScorerTagConfidences = floats("scorer_tag_confidences")
	sc.ScorerTagReasons = strs("scorer_tag_reasons")
	_, hasTags := p["scorer_tags"]
	if l, ok := p["scorer_tags"].([]any); ok {
		for _, x := range l {
			if s, ok := x.(string); ok {
				sc.ScorerTags = append(sc.ScorerTags, s)
			}
		}
	}
	return sc, len(sc.ScorerRatings) > 0 || hasTags
}

// signalRatings turns scorer_ratings into conversation ratings.
func signalRatings(sc SignalScores) []ConversationRating {
	keys := make([]string, 0, len(sc.ScorerRatings))
	for k := range sc.ScorerRatings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]ConversationRating, 0, len(keys))
	for _, k := range keys {
		r := ConversationRating{Name: k, Value: sc.ScorerRatings[k]}
		if c, ok := sc.ScorerRatingConfidences[k]; ok {
			c := c
			r.Confidence = &c
		}
		if s, ok := sc.ScorerRatingReasons[k]; ok {
			s := s
			r.Reason = &s
		}
		out = append(out, r)
	}
	return out
}

// ---------------------------------------------------------------- feedback create

// AgentFeedbackItem is one element of Weave's feedback batch schema
// (FeedbackBatchCreateParams.Batch), including the scorer and span fields.
type AgentFeedbackItem struct {
	ID            *string        `json:"id"`
	ProjectID     string         `json:"project_id"`
	WeaveRef      string         `json:"weave_ref"`
	FeedbackType  string         `json:"feedback_type"`
	Payload       map[string]any `json:"payload"`
	AnnotationRef *string        `json:"annotation_ref"`
	CallRef       *string        `json:"call_ref"`
	Creator       *string        `json:"creator"`
	QueueID       *string        `json:"queue_id"`
	RunnableRef   *string        `json:"runnable_ref"`
	TriggerRef    *string        `json:"trigger_ref"`
	WBUserID      *string        `json:"wb_user_id"`
	SignalScores
	ScorerTraceID      string `json:"scorer_trace_id"`
	SpanAgentName      string `json:"span_agent_name"`
	SpanAgentVersion   string `json:"span_agent_version"`
	SpanConversationID string `json:"span_conversation_id"`
	SpanStatusCode     string `json:"span_status_code"`
	SpanTraceID        string `json:"span_trace_id"`
}

// CreateAgentFeedback stores one item of the feedback batch schema. The
// scorer fields are normalised and kept in the payload; the span_* fields
// are filled from the span index when the ref is an indexed call.
func (s *Store) CreateAgentFeedback(ctx context.Context, it AgentFeedbackItem) (*Feedback, error) {
	pid, err := refs.NormalizeProject(it.ProjectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	if err := it.SignalScores.Normalize(); err != nil {
		return nil, err
	}
	payload := map[string]any{}
	for k, v := range it.Payload {
		payload[k] = v
	}
	if !it.SignalScores.Empty() || it.Payload["scorer_tags"] != nil {
		it.SignalScores.toPayload(payload)
	}
	span := map[string]string{
		"span_agent_name": it.SpanAgentName, "span_agent_version": it.SpanAgentVersion, "span_conversation_id": it.SpanConversationID,
		"span_status_code": it.SpanStatusCode, "span_trace_id": it.SpanTraceID, "scorer_trace_id": it.ScorerTraceID,
	}
	if r, err := refs.Parse(refs.Normalize(strings.TrimSpace(it.WeaveRef))); err == nil && r.Kind == refs.KindCall {
		var trace, conv, agent, version string
		var exc sql.NullString
		var ended sql.NullInt64
		err := s.r.QueryRowContext(ctx, `SELECT s.trace_id, s.conversation_id, s.agent_name, s.agent_version, c.exception, c.ended_at
			FROM agent_spans s JOIN calls c ON c.project_id = s.project_id AND c.id = s.id WHERE s.project_id = ? AND s.id = ?`, pid, r.Name).
			Scan(&trace, &conv, &agent, &version, &exc, &ended)
		if err == nil {
			status := "UNSET"
			if exc.Valid {
				status = "ERROR"
			} else if ended.Valid {
				status = "OK"
			}
			for k, v := range map[string]string{"span_trace_id": trace, "span_conversation_id": conv, "span_agent_name": agent,
				"span_agent_version": version, "span_status_code": status} {
				if span[k] == "" {
					span[k] = v
				}
			}
		} else if err != sql.ErrNoRows {
			return nil, err
		}
	}
	for k, v := range span {
		if v != "" {
			payload[k] = v
		}
	}
	id := ""
	if it.ID != nil {
		id = *it.ID
	}
	return s.CreateFeedback(ctx, FeedbackCreate{ID: id, ProjectID: pid, WeaveRef: it.WeaveRef, FeedbackType: it.FeedbackType, Payload: payload,
		Creator: it.Creator, WBUserID: it.WBUserID, AnnotationRef: it.AnnotationRef, RunnableRef: it.RunnableRef, CallRef: it.CallRef,
		TriggerRef: it.TriggerRef, QueueID: it.QueueID})
}

// ---------------------------------------------------------------- turns

// turnSpans picks the turns among spans: conversation turns per
// conversation, and agent invocations at the top of their trace otherwise.
func turnSpans(spans []*agentSpan) []*agentSpan {
	byConv := map[string][]*agentSpan{}
	ids := map[string]bool{}
	for _, sp := range spans {
		ids[sp.ID] = true
		if sp.ConvID != "" {
			byConv[sp.ConvID] = append(byConv[sp.ConvID], sp)
		}
	}
	var out []*agentSpan
	for _, ss := range byConv {
		out = append(out, conversationTurns(ss)...)
	}
	for _, sp := range spans {
		if sp.ConvID == "" && sp.IsInvocation && !ids[sp.ParentID] {
			out = append(out, sp)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedUS > out[j].StartedUS })
	return out
}

// SignalTurnFilter selects turns to score.
type SignalTurnFilter struct {
	AgentName      string
	ConversationID string
	TraceIDs       []string
	StartedAfter   *string
	StartedBefore  *string
	Limit          int
}

// SignalTurn is one turn with its chat transcript.
type SignalTurn struct {
	SpanID, TraceID, ConversationID, AgentName, AgentVersion, Status string
	Chat                                                             *AgentTraceChat
}

// SignalTurns returns the most recent turns matching f, newest first.
func (s *Store) SignalTurns(ctx context.Context, projectID string, f SignalTurnFilter) ([]SignalTurn, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	a, b, err := timeRange(f.StartedAfter, f.StartedBefore)
	if err != nil {
		return nil, err
	}
	lf := agentSpanFilter{After: a, Before: b, AgentName: f.AgentName}
	if f.ConversationID != "" {
		lf.ConversationIDs = []string{f.ConversationID}
	}
	if f.TraceIDs != nil {
		lf.TraceIDs = f.TraceIDs
	}
	spans, err := s.loadAgentSpans(ctx, pid, lf)
	if err != nil {
		return nil, err
	}
	turns := turnSpans(spans)
	if f.Limit > 0 && len(turns) > f.Limit {
		turns = turns[:f.Limit]
	}
	if len(turns) == 0 {
		return nil, nil
	}
	var traces []string
	seen := map[string]bool{}
	for _, t := range turns {
		if !seen[t.TraceID] {
			seen[t.TraceID] = true
			traces = append(traces, t.TraceID)
		}
	}
	all, calls, err := s.traceBodies(ctx, pid, traces, false)
	if err != nil {
		return nil, err
	}
	out := make([]SignalTurn, 0, len(turns))
	for _, t := range turns {
		tree := subtree(t.ID, all)
		var root *agentSpan
		for _, sp := range tree {
			if sp.ID == t.ID {
				root = sp
			}
		}
		if root == nil {
			continue
		}
		out = append(out, SignalTurn{SpanID: t.ID, TraceID: t.TraceID, ConversationID: t.ConvID, AgentName: t.AgentName,
			AgentVersion: t.AgentVersion, Status: t.statusCode(),
			Chat: buildTraceChat(t.TraceID, []*agentSpan{root}, tree, calls, false, true)})
	}
	return out, nil
}

// TurnTranscript renders a turn as plain text for an LLM judge, keeping the
// start and end of long transcripts.
func TurnTranscript(chat *AgentTraceChat, maxChars int) string {
	clip := func(s string, n int) string {
		s = strings.TrimSpace(s)
		if utf8.RuneCountInString(s) <= n {
			return s
		}
		return string([]rune(s)[:n]) + " …"
	}
	var lines []string
	for _, m := range chat.Messages {
		switch {
		case m.UserMessage != nil && m.UserMessage.Text != "":
			lines = append(lines, "USER: "+clip(m.UserMessage.Text, 4000))
		case m.AssistantMessage != nil && (m.AssistantMessage.Text != "" || len(m.AssistantMessage.RequestedTools) > 0):
			t := m.AssistantMessage.Text
			if t == "" {
				t = "(requests tools: " + strings.Join(m.AssistantMessage.RequestedTools, ", ") + ")"
			}
			lines = append(lines, "ASSISTANT: "+clip(t, 4000))
		case m.ToolCall != nil:
			name, args, res := "", "", ""
			if m.ToolCall.ToolName != nil {
				name = *m.ToolCall.ToolName
			}
			if m.ToolCall.ToolArguments != nil {
				args = *m.ToolCall.ToolArguments
			}
			if m.ToolCall.ToolResult != nil {
				res = *m.ToolCall.ToolResult
			}
			st := ""
			if m.StatusCode != nil && *m.StatusCode == "ERROR" {
				st = " [ERROR]"
			}
			lines = append(lines, fmt.Sprintf("TOOL %s(%s)%s -> %s", name, clip(args, 600), st, clip(res, 1000)))
		case m.AgentStart != nil && m.AgentName != nil:
			lines = append(lines, "AGENT START: "+*m.AgentName)
		}
	}
	text := strings.Join(lines, "\n")
	if maxChars > 0 && utf8.RuneCountInString(text) > maxChars {
		r := []rune(text)
		head := maxChars / 3
		text = string(r[:head]) + "\n…\n" + string(r[len(r)-(maxChars-head):])
	}
	return text
}

// SignalScoredTurns returns the turn span ids that already carry feedback
// from the signal id.
func (s *Store) SignalScoredTurns(ctx context.Context, projectID, signalID string, spanIDs []string) (map[string]bool, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	refList := make([]string, len(spanIDs))
	byRef := map[string]string{}
	for i, id := range spanIDs {
		refList[i] = refs.CallRef(pid, id)
		byRef[refList[i]] = id
	}
	fbs, err := s.feedbackForRefs(ctx, pid, refList)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, f := range fbs {
		if p := asMap(f.Payload); p != nil && p["signal"] == signalID {
			out[byRef[f.WeaveRef]] = true
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- turn feedback

type turnFeedback struct {
	FeedbackID, FeedbackType      string
	Payload                       map[string]any
	CreatedUS                     int64
	Creator, WBUserID             *string
	SpanID, TraceID, ConvID       string
	ConvName, Agent, AgentVersion string
	StartedUS                     int64
	Status                        string
}

type turnFeedbackFilter struct {
	After, Before int64 // on the turn's start
	AgentName     string
	TraceIDs      []string
}

// loadTurnFeedback loads feedback on indexed calls, joined with the span.
func (s *Store) loadTurnFeedback(ctx context.Context, pid string, f turnFeedbackFilter) ([]turnFeedback, error) {
	prefix := strings.TrimSuffix(refs.CallRef(pid, "x"), "x")
	n := utf8.RuneCountInString(prefix)
	where := []string{"f.project_id = ?", "substr(f.weave_ref, 1, ?) = ?", "c.deleted_at IS NULL"}
	args := []any{n + 1, pid, n, prefix}
	if f.After > 0 {
		where = append(where, "c.started_at >= ?")
		args = append(args, f.After)
	}
	if f.Before > 0 {
		where = append(where, "c.started_at < ?")
		args = append(args, f.Before)
	}
	if f.AgentName != "" {
		where = append(where, "s.agent_name = ?")
		args = append(args, f.AgentName)
	}
	if f.TraceIDs != nil {
		if len(f.TraceIDs) == 0 {
			return nil, nil
		}
		q, a := inList("s.trace_id", f.TraceIDs)
		where = append(where, q)
		args = append(args, a...)
	}
	rows, err := s.r.QueryContext(ctx, `SELECT f.id, f.feedback_type, f.payload, f.created_at, f.creator, f.wb_user_id,
		s.id, s.trace_id, s.conversation_id, s.conversation_name, s.agent_name, s.agent_version, c.started_at, c.exception IS NOT NULL, c.ended_at IS NOT NULL
		FROM feedback f JOIN agent_spans s ON s.project_id = f.project_id AND s.id = substr(f.weave_ref, ?)
		JOIN calls c ON c.project_id = s.project_id AND c.id = s.id
		WHERE `+strings.Join(where, " AND ")+` ORDER BY f.created_at DESC LIMIT 50000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []turnFeedback
	for rows.Next() {
		var t turnFeedback
		var payload string
		var creator, user sql.NullString
		var started sql.NullInt64
		var failed, ended bool
		if err := rows.Scan(&t.FeedbackID, &t.FeedbackType, &payload, &t.CreatedUS, &creator, &user, &t.SpanID, &t.TraceID, &t.ConvID,
			&t.ConvName, &t.Agent, &t.AgentVersion, &started, &failed, &ended); err != nil {
			return nil, err
		}
		t.Payload = asMap(decodeJSON(payload))
		if t.Payload == nil {
			t.Payload = map[string]any{}
		}
		t.Creator, t.WBUserID = strPtr(creator), strPtr(user)
		t.StartedUS = started.Int64
		t.Status = "UNSET"
		if failed {
			t.Status = "ERROR"
		} else if ended {
			t.Status = "OK"
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (t turnFeedback) feedback() Feedback {
	return Feedback{ID: t.FeedbackID, FeedbackType: t.FeedbackType, Payload: t.Payload}
}

// ---------------------------------------------------------------- filters

// AgentSignalRatingFilter is one rating condition of signal_filters.
type AgentSignalRatingFilter struct {
	Op        string  `json:"op"`
	ScorerKey string  `json:"scorer_key"`
	Value     float64 `json:"value"`
}

// AgentSignalFilters keeps spans whose turn has matching signal results:
// every rating condition must hold, and when tags are given the turn must
// carry at least one of them.
type AgentSignalFilters struct {
	Ratings []AgentSignalRatingFilter `json:"ratings"`
	Tags    []string                  `json:"tags"`
}

// AgentInsightFilter is Weave's conversation insight filter. Values within a
// filter are ORed, filters are ANDed. Insight values come from turn feedback
// whose payload has an "insights" map; intent_sentiment is also derived from
// sentiment/satisfaction ratings and the user_frustration tag.
type AgentInsightFilter struct {
	Field   string   `json:"field"`
	Values  []string `json:"values"`
	Exclude bool     `json:"exclude"`
}

var insightFields = map[string][]string{
	"intent_category":  nil,
	"intent_sentiment": {"frustrated", "dissatisfied", "neutral", "satisfied", "delighted"},
	"failure_category": nil,
	"failure_severity": {"info", "major", "minor"},
	"intent_topic_id":  nil,
	"failure_topic_id": nil,
}

func (f *AgentSignalFilters) empty() bool {
	return f == nil || (len(f.Ratings) == 0 && len(f.Tags) == 0)
}

func validateSignalFilters(sf *AgentSignalFilters, ifs []AgentInsightFilter) error {
	if sf != nil {
		for _, r := range sf.Ratings {
			switch r.Op {
			case "gte", "gt", "lte", "lt", "eq":
			default:
				return Invalidf("signal_filters: unknown rating op %q", r.Op)
			}
			if r.ScorerKey == "" {
				return Invalidf("signal_filters: scorer_key is required")
			}
		}
	}
	for _, f := range ifs {
		allowed, ok := insightFields[f.Field]
		if !ok {
			return Invalidf("insight_filters: unknown field %q", f.Field)
		}
		if allowed != nil {
			for _, v := range f.Values {
				ok := false
				for _, a := range allowed {
					ok = ok || a == v
				}
				if !ok {
					return Invalidf("insight_filters: %s does not accept %q (allowed: %s)", f.Field, v, strings.Join(allowed, ", "))
				}
			}
		}
	}
	return nil
}

func compareOp(v float64, op string, want float64) bool {
	const eps = 1e-9
	switch op {
	case "gte":
		return v >= want-eps
	case "gt":
		return v > want+eps
	case "lte":
		return v <= want+eps
	case "lt":
		return v < want-eps
	case "eq":
		return math.Abs(v-want) <= eps
	}
	return false
}

// SentimentBucket maps a 0..1 sentiment or satisfaction rating onto Weave's
// intent_sentiment values.
func SentimentBucket(v float64) string {
	switch {
	case v < 0.2:
		return "frustrated"
	case v < 0.4:
		return "dissatisfied"
	case v < 0.6:
		return "neutral"
	case v < 0.8:
		return "satisfied"
	}
	return "delighted"
}

type turnSignals struct {
	ratings map[string][]float64 // lower-cased rating name
	tags    map[string]bool
}

func feedbackInsights(t turnFeedback, ratings []ConversationRating, tags []string) map[string][]string {
	out := map[string][]string{}
	for k, v := range asMap(t.Payload["insights"]) {
		if _, ok := insightFields[k]; !ok {
			continue
		}
		switch x := v.(type) {
		case string:
			out[k] = append(out[k], x)
		case []any:
			for _, e := range x {
				if s, ok := e.(string); ok {
					out[k] = append(out[k], s)
				}
			}
		}
	}
	for _, r := range ratings {
		switch strings.ToLower(r.Name) {
		case "sentiment", "user_satisfaction":
			out["intent_sentiment"] = append(out["intent_sentiment"], SentimentBucket(r.Value))
		}
	}
	for _, tg := range tags {
		if strings.EqualFold(tg, "user_frustration") {
			out["intent_sentiment"] = append(out["intent_sentiment"], "frustrated")
		}
	}
	return out
}

// applySignalFilters keeps spans whose turn (trace) matches signal_filters
// and whose conversation (or trace, outside conversations) matches
// insight_filters.
func (s *Store) applySignalFilters(ctx context.Context, pid string, spans []*agentSpan, sf *AgentSignalFilters, ifs []AgentInsightFilter) ([]*agentSpan, error) {
	if err := validateSignalFilters(sf, ifs); err != nil {
		return nil, err
	}
	active := ifs[:0:0]
	for _, f := range ifs {
		if len(f.Values) > 0 {
			active = append(active, f)
		}
	}
	if sf.empty() && len(active) == 0 {
		return spans, nil
	}
	traceSet := map[string]bool{}
	var traces []string
	for _, sp := range spans {
		if !traceSet[sp.TraceID] {
			traceSet[sp.TraceID] = true
			traces = append(traces, sp.TraceID)
		}
	}
	var fbs []turnFeedback
	if len(active) > 0 {
		// Insight filters are conversation-scoped: load every turn's feedback.
		all, err := s.loadTurnFeedback(ctx, pid, turnFeedbackFilter{})
		if err != nil {
			return nil, err
		}
		fbs = all
	} else {
		for start := 0; start < len(traces); start += 500 {
			part, err := s.loadTurnFeedback(ctx, pid, turnFeedbackFilter{TraceIDs: traces[start:min(start+500, len(traces))]})
			if err != nil {
				return nil, err
			}
			fbs = append(fbs, part...)
		}
	}
	byTrace := map[string]*turnSignals{}
	insights := map[string]map[string]map[string]bool{} // conv key -> field -> values
	for _, t := range fbs {
		f := t.feedback()
		ratings, tags := feedbackRatings(f), feedbackTags(f)
		ts := byTrace[t.TraceID]
		if ts == nil {
			ts = &turnSignals{ratings: map[string][]float64{}, tags: map[string]bool{}}
			byTrace[t.TraceID] = ts
		}
		for _, r := range ratings {
			k := strings.ToLower(r.Name)
			ts.ratings[k] = append(ts.ratings[k], r.Value)
		}
		for _, tg := range tags {
			ts.tags[strings.ToLower(tg)] = true
		}
		key := "trace:" + t.TraceID
		if t.ConvID != "" {
			key = "conv:" + t.ConvID
		}
		for field, vals := range feedbackInsights(t, ratings, tags) {
			if insights[key] == nil {
				insights[key] = map[string]map[string]bool{}
			}
			if insights[key][field] == nil {
				insights[key][field] = map[string]bool{}
			}
			for _, v := range vals {
				insights[key][field][v] = true
			}
		}
	}
	signalOK := func(trace string) bool {
		if sf.empty() {
			return true
		}
		ts := byTrace[trace]
		if ts == nil {
			return false
		}
		for _, r := range sf.Ratings {
			ok := false
			for _, v := range ts.ratings[strings.ToLower(r.ScorerKey)] {
				if compareOp(v, r.Op, r.Value) {
					ok = true
					break
				}
			}
			if !ok {
				return false
			}
		}
		if len(sf.Tags) > 0 {
			for _, tg := range sf.Tags {
				if ts.tags[strings.ToLower(strings.TrimSpace(tg))] {
					return true
				}
			}
			return false
		}
		return true
	}
	insightOK := func(sp *agentSpan) bool {
		key := "trace:" + sp.TraceID
		if sp.ConvID != "" {
			key = "conv:" + sp.ConvID
		}
		for _, f := range active {
			hit := false
			for _, v := range f.Values {
				if insights[key][f.Field][v] {
					hit = true
					break
				}
			}
			if hit == f.Exclude {
				return false
			}
		}
		return true
	}
	out := spans[:0:0]
	for _, sp := range spans {
		if signalOK(sp.TraceID) && insightOK(sp) {
			out = append(out, sp)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- results query

// AgentSignalsQueryReq is the body of /agents/signals/query (Weft).
type AgentSignalsQueryReq struct {
	ProjectID      string   `json:"project_id"`
	AgentName      *string  `json:"agent_name"`
	ConversationID *string  `json:"conversation_id"`
	TraceIDs       []string `json:"trace_ids"`
	Signal         *string  `json:"signal"` // scorer key (rating name, or signal id for tags)
	Kind           *string  `json:"kind"`   // "rating" or "tag"
	Tag            *string  `json:"tag"`
	MinScore       *float64 `json:"min_score"`
	MaxScore       *float64 `json:"max_score"`
	MatchedOnly    *bool    `json:"matched_only"` // tag rows without tags are hidden by default
	StartedAfter   *string  `json:"started_after"`
	StartedBefore  *string  `json:"started_before"`
	Limit          *int     `json:"limit"`
	Offset         int      `json:"offset"`
}

// AgentSignalResult is one rating or tag result on a turn.
type AgentSignalResult struct {
	FeedbackID       string               `json:"feedback_id"`
	FeedbackType     string               `json:"feedback_type"`
	Kind             string               `json:"kind"`
	Signal           string               `json:"signal"`
	SignalID         *string              `json:"signal_id"`
	SignalName       *string              `json:"signal_name"`
	Score            *float64             `json:"score"`
	Tags             []string             `json:"tags"`
	Confidence       *float64             `json:"confidence"`
	Reason           *string              `json:"reason"`
	Creator          *string              `json:"creator"`
	CreatedAt        string               `json:"created_at"`
	SpanID           string               `json:"span_id"`
	TraceID          string               `json:"trace_id"`
	ConversationID   *string              `json:"conversation_id"`
	ConversationName *string              `json:"conversation_name"`
	AgentName        *string              `json:"agent_name"`
	AgentVersion     *string              `json:"agent_version"`
	TurnStartedAt    *string              `json:"turn_started_at"`
	TurnStatus       string               `json:"turn_status"`
	LastMessage      *AgentMessagePreview `json:"last_message"`
	ScorerCallID     *string              `json:"scorer_call_id"`
}

// AgentSignalSummary aggregates one signal over the filtered results.
type AgentSignalSummary struct {
	Signal     string         `json:"signal"`
	SignalName *string        `json:"signal_name"`
	Kind       string         `json:"kind"`
	Count      int            `json:"count"`
	Matched    int            `json:"matched"`
	Avg        *float64       `json:"avg"`
	TagCounts  map[string]int `json:"tag_counts,omitempty"`
}

// AgentSignalsQueryRes is the response of /agents/signals/query.
type AgentSignalsQueryRes struct {
	Results    []*AgentSignalResult  `json:"results"`
	Summary    []*AgentSignalSummary `json:"summary"`
	TotalCount int                   `json:"total_count"`
}

func payloadStr(p map[string]any, k string) *string {
	if s, ok := p[k].(string); ok && s != "" {
		return &s
	}
	return nil
}

// QuerySignalResults lists signal (and other scorer/annotation) results on
// turns, newest first, with a per-signal summary.
func (s *Store) QuerySignalResults(ctx context.Context, req AgentSignalsQueryReq) (*AgentSignalsQueryRes, error) {
	pid, err := refs.NormalizeProject(req.ProjectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	a, b, err := timeRange(req.StartedAfter, req.StartedBefore)
	if err != nil {
		return nil, err
	}
	f := turnFeedbackFilter{After: a, Before: b, TraceIDs: req.TraceIDs}
	if req.AgentName != nil {
		f.AgentName = *req.AgentName
	}
	fbs, err := s.loadTurnFeedback(ctx, pid, f)
	if err != nil {
		return nil, err
	}
	matchedOnly := req.MatchedOnly == nil || *req.MatchedOnly
	var rows []*AgentSignalResult
	for _, t := range fbs {
		if req.ConversationID != nil && t.ConvID != *req.ConversationID {
			continue
		}
		fb := t.feedback()
		sc, isSignal := signalScoresOf(t.Payload)
		base := AgentSignalResult{FeedbackID: t.FeedbackID, FeedbackType: t.FeedbackType, Creator: t.Creator, CreatedAt: FormatMicros(t.CreatedUS),
			SpanID: t.SpanID, TraceID: t.TraceID, ConversationID: ptrIf(t.ConvID), ConversationName: ptrIf(t.ConvName),
			AgentName: ptrIf(t.Agent), AgentVersion: ptrIf(t.AgentVersion), TurnStartedAt: microsPtr(t.StartedUS), TurnStatus: t.Status,
			SignalID: payloadStr(t.Payload, "signal"), SignalName: payloadStr(t.Payload, "signal_name"), ScorerCallID: payloadStr(t.Payload, "scorer_call_id"),
			Tags: []string{}}
		var ratings []ConversationRating
		var tags []string
		if isSignal {
			ratings, tags = signalRatings(sc), sc.ScorerTags
		} else {
			ratings, tags = feedbackRatings(fb), feedbackTags(fb)
		}
		for _, r := range ratings {
			row := base
			row.Kind, row.Signal = "rating", r.Name
			v := r.Value
			row.Score, row.Confidence, row.Reason = &v, r.Confidence, r.Reason
			rows = append(rows, &row)
		}
		kind, _ := t.Payload["signal_kind"].(string)
		if len(tags) > 0 || (kind == "tag" && !matchedOnly) {
			row := base
			row.Kind, row.Tags = "tag", append([]string{}, tags...)
			row.Signal = "tags"
			if row.SignalID != nil {
				row.Signal = *row.SignalID
			} else if fb.FeedbackType == "wandb.reaction.1" {
				row.Signal = "reactions"
			} else if strings.HasPrefix(fb.FeedbackType, "wandb.") {
				row.Signal = strings.TrimPrefix(fb.FeedbackType, "wandb.")
			}
			for _, tg := range tags {
				if c, ok := sc.ScorerTagConfidences[strings.ToLower(tg)]; ok && row.Confidence == nil {
					c := c
					row.Confidence = &c
				}
				if r, ok := sc.ScorerTagReasons[strings.ToLower(tg)]; ok && row.Reason == nil {
					r := r
					row.Reason = &r
				}
			}
			if row.Reason == nil {
				row.Reason = payloadStr(t.Payload, "reason")
			}
			rows = append(rows, &row)
		}
	}
	// Filters on result rows.
	kept := rows[:0]
	for _, r := range rows {
		if req.Kind != nil && *req.Kind != "" && r.Kind != *req.Kind {
			continue
		}
		if req.Signal != nil && *req.Signal != "" && !strings.EqualFold(r.Signal, *req.Signal) {
			continue
		}
		if req.Tag != nil && *req.Tag != "" {
			hit := false
			for _, tg := range r.Tags {
				hit = hit || strings.EqualFold(tg, *req.Tag)
			}
			if !hit {
				continue
			}
		}
		if (req.MinScore != nil || req.MaxScore != nil) && r.Kind == "rating" {
			if req.MinScore != nil && *r.Score < *req.MinScore-1e-9 || req.MaxScore != nil && *r.Score > *req.MaxScore+1e-9 {
				continue
			}
		}
		kept = append(kept, r)
	}
	rows = kept
	// Newest turns first; results on one turn stay together.
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := "", ""
		if rows[i].TurnStartedAt != nil {
			a = *rows[i].TurnStartedAt
		}
		if rows[j].TurnStartedAt != nil {
			b = *rows[j].TurnStartedAt
		}
		return a > b
	})
	// Summary.
	sums := map[string]*AgentSignalSummary{}
	totals := map[string]float64{}
	var order []string
	for _, r := range rows {
		k := r.Kind + "\x00" + r.Signal
		sm := sums[k]
		if sm == nil {
			sm = &AgentSignalSummary{Signal: r.Signal, SignalName: r.SignalName, Kind: r.Kind}
			if r.Kind == "tag" {
				sm.TagCounts = map[string]int{}
			}
			sums[k] = sm
			order = append(order, k)
		}
		sm.Count++
		if r.Kind == "rating" {
			totals[k] += *r.Score
		} else {
			if len(r.Tags) > 0 {
				sm.Matched++
			}
			for _, tg := range r.Tags {
				sm.TagCounts[tg]++
			}
		}
	}
	res := &AgentSignalsQueryRes{Results: []*AgentSignalResult{}, Summary: []*AgentSignalSummary{}, TotalCount: len(rows)}
	sort.Strings(order)
	for _, k := range order {
		sm := sums[k]
		if sm.Kind == "rating" && sm.Count > 0 {
			avg := totals[k] / float64(sm.Count)
			sm.Avg = &avg
		}
		res.Summary = append(res.Summary, sm)
	}
	pg := page(rows, req.Limit, req.Offset, 100)
	if len(pg) > 0 {
		need := map[string]bool{}
		var ids []string
		for _, r := range pg {
			if !need[r.SpanID] {
				need[r.SpanID] = true
				ids = append(ids, r.SpanID)
			}
		}
		calls, err := s.callsByID(ctx, pid, ids)
		if err != nil {
			return nil, err
		}
		for _, r := range pg {
			c := calls[r.SpanID]
			if c == nil {
				continue
			}
			if t, _ := assistantText(OutputMessages(c.Output)); t != "" {
				r.LastMessage = &AgentMessagePreview{Role: "assistant_message", Text: truncateText(t, 200)}
			} else if t, _ := lastUserText(InputMessages(c.Inputs)); t != "" {
				r.LastMessage = &AgentMessagePreview{Role: "user_message", Text: truncateText(t, 200)}
			}
		}
	}
	res.Results = pg
	return res, nil
}
