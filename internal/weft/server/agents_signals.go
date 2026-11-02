package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// Agent signals (Weft extensions to Weave's /agents/* API): turn-scoped
// tags and ratings scored by an LLM judge and stored as feedback in Weave's
// feedback batch schema (scorer_ratings, scorer_tags, confidences, reasons).

func init() {
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		r.Get("/agents/signals/presets", s.agentSignalPresets)
		r.Post("/agents/signals/presets", s.agentSignalPresets)
		r.Post("/agents/signals/run", handle(s.agentSignalsRun))
		r.Post("/agents/signals/query", handle(s.agentSignalsQuery))
		r.Post("/agents/feedback/batch/create", handle(s.agentFeedbackBatchCreate))
	})
}

func (s *Server) agentSignalPresets(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"presets": store.SignalPresets})
}

func (s *Server) agentSignalsQuery(ctx context.Context, _ *http.Request, req *store.AgentSignalsQueryReq) (any, error) {
	return s.store.QuerySignalResults(ctx, *req)
}

type agentFeedbackBatchReq struct {
	Batch []store.AgentFeedbackItem `json:"batch"`
}

// agentFeedbackBatchCreate is /feedback/batch/create with the scorer and
// span fields of Weave's batch schema kept (normalised) in the payload.
func (s *Server) agentFeedbackBatchCreate(ctx context.Context, r *http.Request, req *agentFeedbackBatchReq) (any, error) {
	if len(req.Batch) > 1000 {
		return nil, store.Invalidf("batch too large (max 1000)")
	}
	res := make([]any, 0, len(req.Batch))
	for _, it := range req.Batch {
		if _, err := project(it.ProjectID); err != nil {
			return nil, err
		}
		it.WBUserID = userFrom(r, it.WBUserID)
		f, err := s.store.CreateAgentFeedback(ctx, it)
		if err != nil {
			return nil, err
		}
		res = append(res, map[string]any{"id": f.ID, "created_at": f.CreatedAt, "wb_user_id": f.WBUserID, "payload": f.Payload})
	}
	return map[string]any{"res": res}, nil
}

// SignalSpec defines one signal to run: a preset id, or a custom signal.
type SignalSpec struct {
	ID          string `json:"id"`
	Preset      string `json:"preset"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
}

func (sp SignalSpec) resolve() (SignalSpec, error) {
	if sp.Preset != "" {
		p, ok := store.PresetByID(sp.Preset)
		if !ok {
			return sp, store.Invalidf("unknown signal preset %q", sp.Preset)
		}
		if sp.ID == "" {
			sp.ID = p.ID
		}
		if sp.Name == "" {
			sp.Name = p.Name
		}
		if sp.Kind == "" {
			sp.Kind = p.Kind
		}
		if sp.Description == "" {
			sp.Description = p.Description
		}
	}
	sp.ID = strings.ToLower(strings.TrimSpace(sp.ID))
	if !store.ValidSignalID(sp.ID) {
		return sp, store.Invalidf("signal id %q must be 1-36 lowercase letters, digits, '_', '-' or '.'", sp.ID)
	}
	if sp.Kind != "tag" && sp.Kind != "rating" {
		return sp, store.Invalidf("signal %q: kind must be tag or rating", sp.ID)
	}
	if strings.TrimSpace(sp.Description) == "" || len(sp.Description) > 4000 {
		return sp, store.Invalidf("signal %q needs a description (max 4000 characters)", sp.ID)
	}
	if sp.Name == "" {
		sp.Name = sp.ID
	}
	return sp, nil
}

type signalsRunReq struct {
	ProjectID      string       `json:"project_id"`
	Signals        []SignalSpec `json:"signals"`
	Model          string       `json:"model"`
	AgentName      string       `json:"agent_name"`
	ConversationID string       `json:"conversation_id"`
	TraceIDs       []string     `json:"trace_ids"`
	StartedAfter   *string      `json:"started_after"`
	StartedBefore  *string      `json:"started_before"`
	Limit          int          `json:"limit"`
	Rescore        bool         `json:"rescore"`
}

type signalRunResult struct {
	SpanID     string   `json:"span_id"`
	TraceID    string   `json:"trace_id"`
	Signal     string   `json:"signal"`
	Kind       string   `json:"kind"`
	Score      *float64 `json:"score,omitempty"`
	Matched    *bool    `json:"matched,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	FeedbackID string   `json:"feedback_id,omitempty"`
	Error      string   `json:"error,omitempty"`
}

const signalSystemPrompt = `You are a careful evaluator of conversations between a user and an AI agent.
You score ONE turn of a conversation against ONE signal and answer with a single JSON object only.`

func signalPrompt(sp SignalSpec, transcript string) string {
	var format string
	if sp.Kind == "tag" {
		format = `{"matched": true or false, "confidence": number from 0 to 1, "reason": "one or two sentences"}
Set "matched" to true only when the turn clearly shows the signal.`
	} else {
		format = `{"score": number from 0 to 1, "confidence": number from 0 to 1, "reason": "one or two sentences"}`
	}
	return fmt.Sprintf("Signal: %s\n%s\n\nTurn transcript:\n<transcript>\n%s\n</transcript>\n\nRespond with JSON only, in this shape:\n%s",
		sp.Name, sp.Description, transcript, format)
}

// judgeNumber normalises a judge's number onto 0..1 (1-5, 1-10 and 0-100
// scales are rescaled).
func judgeNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "yes":
			return 1, true
		case "false", "no":
			return 0, true
		}
	}
	x, ok := store.ToFloat(v)
	if !ok || math.IsNaN(x) || math.IsInf(x, 0) {
		return 0, false
	}
	switch {
	case x <= 1:
	case x <= 5:
		x = (x - 1) / 4
	case x <= 10:
		x /= 10
	case x <= 100:
		x /= 100
	}
	return math.Max(0, math.Min(1, x)), true
}

// parseJudge extracts the JSON object from a judge's reply.
func parseJudge(content string) (map[string]any, error) {
	i, j := strings.Index(content, "{"), strings.LastIndex(content, "}")
	if i < 0 || j <= i {
		return nil, fmt.Errorf("judge reply is not JSON: %q", truncate(content, 120))
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(content[i:j+1]), &m); err != nil {
		return nil, fmt.Errorf("judge reply is not JSON: %v", err)
	}
	return m, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// signalScores turns a judge reply into Weave scorer fields.
func signalScores(sp SignalSpec, m map[string]any) (store.SignalScores, signalRunResult, error) {
	var sc store.SignalScores
	res := signalRunResult{Signal: sp.ID, Kind: sp.Kind}
	reason := ""
	for _, k := range []string{"reason", "reasoning", "explanation", "rationale"} {
		if s, ok := m[k].(string); ok && s != "" {
			reason = s
			break
		}
	}
	reason = store.NormalizeSignalReason(reason)
	res.Reason = reason
	var conf *float64
	if c, ok := judgeNumber(m["confidence"]); ok && m["confidence"] != nil {
		conf = &c
		res.Confidence = conf
	}
	if sp.Kind == "tag" {
		matched, found := false, false
		for _, k := range []string{"matched", "flagged", "detected", "present", "correct", "value"} {
			if v, ok := m[k]; ok {
				if x, ok := judgeNumber(v); ok {
					matched, found = x >= 0.5, true
					break
				}
			}
		}
		if !found {
			if x, ok := judgeNumber(m["score"]); ok {
				matched, found = x >= 0.5, true
			}
		}
		if !found {
			return sc, res, fmt.Errorf("judge reply has no matched field")
		}
		res.Matched = &matched
		sc.ScorerTags = []string{}
		if matched {
			sc.ScorerTags = []string{sp.ID}
			if conf != nil {
				sc.ScorerTagConfidences = map[string]float64{sp.ID: *conf}
			}
			if reason != "" {
				sc.ScorerTagReasons = map[string]string{sp.ID: reason}
			}
		}
		return sc, res, nil
	}
	var score float64
	found := false
	for _, k := range []string{"score", "rating", "value"} {
		if x, ok := judgeNumber(m[k]); ok && m[k] != nil {
			score, found = x, true
			break
		}
	}
	if !found {
		return sc, res, fmt.Errorf("judge reply has no score")
	}
	res.Score = &score
	sc.ScorerRatings = map[string]float64{sp.ID: score}
	if conf != nil {
		sc.ScorerRatingConfidences = map[string]float64{sp.ID: *conf}
	}
	if reason != "" {
		sc.ScorerRatingReasons = map[string]string{sp.ID: reason}
	}
	return sc, res, nil
}

func (s *Server) agentSignalsRun(ctx context.Context, r *http.Request, req *signalsRunReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	if len(req.Signals) == 0 || len(req.Signals) > 12 {
		return nil, store.Invalidf("signals: give 1 to 12 signals")
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, store.Invalidf("model is required")
	}
	specs := make([]SignalSpec, 0, len(req.Signals))
	seen := map[string]bool{}
	for _, sp := range req.Signals {
		sp, err := sp.resolve()
		if err != nil {
			return nil, err
		}
		if seen[sp.ID] {
			return nil, store.Invalidf("signal %q given twice", sp.ID)
		}
		seen[sp.ID] = true
		specs = append(specs, sp)
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		return nil, store.Invalidf("limit must be at most 200 turns")
	}
	turns, err := s.store.SignalTurns(ctx, pid, store.SignalTurnFilter{AgentName: req.AgentName, ConversationID: req.ConversationID,
		TraceIDs: req.TraceIDs, StartedAfter: req.StartedAfter, StartedBefore: req.StartedBefore, Limit: limit})
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(turns))
	for i, t := range turns {
		ids[i] = t.SpanID
	}
	type job struct {
		turn store.SignalTurn
		sp   SignalSpec
	}
	var jobs []job
	skipped := 0
	for _, sp := range specs {
		done := map[string]bool{}
		if !req.Rescore && len(ids) > 0 {
			if done, err = s.store.SignalScoredTurns(ctx, pid, sp.ID, ids); err != nil {
				return nil, err
			}
		}
		for _, t := range turns {
			if done[t.SpanID] {
				skipped++
				continue
			}
			jobs = append(jobs, job{t, sp})
		}
	}
	creator := "weft-signals"
	user := userFrom(r, nil)
	results := make([]signalRunResult, len(jobs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, j job) {
			defer wg.Done()
			defer func() { <-sem }()
			res := signalRunResult{SpanID: j.turn.SpanID, TraceID: j.turn.TraceID, Signal: j.sp.ID, Kind: j.sp.Kind}
			fail := func(err error) {
				res.Error = err.Error()
				results[i] = res
			}
			transcript := store.TurnTranscript(j.turn.Chat, 24000)
			if strings.TrimSpace(transcript) == "" {
				fail(fmt.Errorf("turn has no messages to score"))
				return
			}
			inputs := map[string]any{
				"model": req.Model,
				"messages": []any{
					map[string]any{"role": "system", "content": signalSystemPrompt},
					map[string]any{"role": "user", "content": signalPrompt(j.sp, transcript)},
				},
				"temperature":     0,
				"response_format": map[string]any{"type": "json_object"},
			}
			resp, callID, err := s.engine.TrackedCompletion(ctx, pid, nil, inputs, true)
			if err != nil {
				fail(err)
				return
			}
			content := completionText(resp)
			m, err := parseJudge(content)
			if err != nil {
				fail(err)
				return
			}
			sc, parsed, err := signalScores(j.sp, m)
			if err != nil {
				fail(err)
				return
			}
			parsed.SpanID, parsed.TraceID = res.SpanID, res.TraceID
			payload := map[string]any{"signal": j.sp.ID, "signal_name": j.sp.Name, "signal_kind": j.sp.Kind, "model": req.Model,
				"scorer_call_id": callID, "output": m}
			if parsed.Reason != "" {
				payload["reason"] = parsed.Reason
			}
			f, err := s.store.CreateAgentFeedback(ctx, store.AgentFeedbackItem{ProjectID: pid, WeaveRef: refs.CallRef(pid, j.turn.SpanID),
				FeedbackType: "wandb.agent_monitor", Payload: payload, Creator: &creator, WBUserID: user, SignalScores: sc,
				ScorerTraceID: callID, SpanTraceID: j.turn.TraceID, SpanConversationID: j.turn.ConversationID,
				SpanAgentName: j.turn.AgentName, SpanAgentVersion: j.turn.AgentVersion, SpanStatusCode: j.turn.Status})
			if err != nil {
				fail(err)
				return
			}
			parsed.FeedbackID = f.ID
			results[i] = parsed
		}(i, j)
	}
	wg.Wait()
	scored, failed := 0, 0
	for _, r := range results {
		if r.Error != "" {
			failed++
		} else {
			scored++
		}
	}
	return map[string]any{"turns": len(turns), "scored": scored, "skipped": skipped, "failed": failed, "results": results}, nil
}

// completionText returns the first choice's message content.
func completionText(resp map[string]any) string {
	ch, _ := resp["choices"].([]any)
	if len(ch) == 0 {
		return ""
	}
	c, _ := ch[0].(map[string]any)
	msg, _ := c["message"].(map[string]any)
	switch t := msg["content"].(type) {
	case string:
		return t
	case []any:
		var parts []string
		for _, p := range t {
			if m, ok := p.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "")
	}
	return ""
}
