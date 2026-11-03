package evals

// Agent signals: an LLM judge scores one turn against one tag or rating
// signal, and the result is stored as weft.signal feedback with the
// feedback batch schema's scorer fields (see store/agents_signals.go). Signals run
// on demand (/agents/signals/run) and automatically on turns that end while
// the project's `agent-signals` config has them enabled.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// SignalSpec defines one signal: a preset id, or a custom signal.
type SignalSpec struct {
	ID          string `json:"id"`
	Preset      string `json:"preset"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	// SampleRate (0..1, default 1) applies to automatic scoring only.
	SampleRate *float64 `json:"sample_rate,omitempty"`
}

// Resolve fills a preset's fields and validates the spec.
func (sp SignalSpec) Resolve() (SignalSpec, error) {
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

// Version identifies what the judge is asked; a changed kind or
// description is a new version, which scores turns again.
func (sp SignalSpec) Version() string { return store.SignalVersion(sp.Kind, sp.Description) }

// SignalRunResult is one judged (turn, signal) pair.
type SignalRunResult struct {
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

// JudgeNumber normalises a judge's number onto 0..1 (1-5, 1-10 and 0-100
// scales are rescaled; booleans and yes/no are 1/0).
func JudgeNumber(v any) (float64, bool) {
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

// ParseJudge extracts the JSON object from a judge's reply.
func ParseJudge(content string) (map[string]any, error) {
	i, j := strings.Index(content, "{"), strings.LastIndex(content, "}")
	if i < 0 || j <= i {
		if len(content) > 120 {
			content = content[:120] + "…"
		}
		return nil, fmt.Errorf("judge reply is not JSON: %q", content)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(content[i:j+1]), &m); err != nil {
		return nil, fmt.Errorf("judge reply is not JSON: %v", err)
	}
	return m, nil
}

// SignalScores turns a judge reply into scorer fields.
func SignalScores(sp SignalSpec, m map[string]any) (store.SignalScores, SignalRunResult, error) {
	var sc store.SignalScores
	res := SignalRunResult{Signal: sp.ID, Kind: sp.Kind}
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
	if c, ok := JudgeNumber(m["confidence"]); ok && m["confidence"] != nil {
		conf = &c
		res.Confidence = conf
	}
	if sp.Kind == "tag" {
		matched, found := false, false
		for _, k := range []string{"matched", "flagged", "detected", "present", "correct", "value"} {
			if v, ok := m[k]; ok {
				if x, ok := JudgeNumber(v); ok {
					matched, found = x >= 0.5, true
					break
				}
			}
		}
		if !found {
			if x, ok := JudgeNumber(m["score"]); ok {
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
		if x, ok := JudgeNumber(m[k]); ok && m[k] != nil {
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

// CompletionText returns the first choice's message content.
func CompletionText(resp map[string]any) string {
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

// ScoreTurnSignal judges one turn against one (resolved) signal and stores
// the result as weft.signal feedback on the turn's root call.
func (e *Engine) ScoreTurnSignal(ctx context.Context, projectID string, turn store.SignalTurn, sp SignalSpec, model string, creator, user *string) SignalRunResult {
	res := SignalRunResult{SpanID: turn.SpanID, TraceID: turn.TraceID, Signal: sp.ID, Kind: sp.Kind}
	fail := func(err error) SignalRunResult {
		res.Error = err.Error()
		return res
	}
	transcript := store.TurnTranscript(turn.Chat, 24000)
	if strings.TrimSpace(transcript) == "" {
		return fail(fmt.Errorf("turn has no messages to score"))
	}
	inputs := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "system", "content": signalSystemPrompt},
			map[string]any{"role": "user", "content": signalPrompt(sp, transcript)},
		},
		"temperature":     0,
		"response_format": map[string]any{"type": "json_object"},
	}
	resp, callID, err := e.TrackedCompletion(ctx, projectID, nil, inputs, true)
	if err != nil {
		return fail(err)
	}
	m, err := ParseJudge(CompletionText(resp))
	if err != nil {
		return fail(err)
	}
	sc, parsed, err := SignalScores(sp, m)
	if err != nil {
		return fail(err)
	}
	parsed.SpanID, parsed.TraceID = res.SpanID, res.TraceID
	payload := map[string]any{"signal": sp.ID, "signal_name": sp.Name, "signal_kind": sp.Kind, "signal_version": sp.Version(),
		"model": model, "scorer_call_id": callID, "output": m}
	if parsed.Reason != "" {
		payload["reason"] = parsed.Reason
	}
	f, err := e.Store.CreateAgentFeedback(ctx, store.AgentFeedbackItem{ProjectID: projectID, Ref: refs.CallRef(projectID, turn.SpanID),
		FeedbackType: "weft.signal", Payload: payload, Creator: creator, UserID: user, SignalScores: sc,
		ScorerTraceID: callID, SpanTraceID: turn.TraceID, SpanConversationID: turn.ConversationID,
		SpanAgentName: turn.AgentName, SpanAgentVersion: turn.AgentVersion, SpanStatusCode: turn.Status})
	if err != nil {
		return fail(err)
	}
	parsed.FeedbackID = f.ID
	return parsed
}

// ---------------------------------------------------------------- automatic signals

// SignalsConfig is the project's `agent-signals` object.
type SignalsConfig struct {
	Model   string
	Auto    bool
	Signals []SignalSpec
}

type signalsCacheEntry struct {
	at  time.Time
	cfg *SignalsConfig
}

var (
	signalsCacheMu sync.Mutex
	signalsCache   = map[*Engine]map[string]signalsCacheEntry{}
	signalInflight sync.Map // projectID/spanID/signal/version -> struct{}
)

// ParseSignalsConfig reads enabled signals from an `agent-signals` value.
// Automatic scoring is on unless `auto` is false, and needs a model.
func ParseSignalsConfig(v any) *SignalsConfig {
	m, _ := v.(map[string]any)
	cfg := &SignalsConfig{Auto: true}
	if m == nil {
		return cfg
	}
	cfg.Model, _ = m["model"].(string)
	if a, ok := m["auto"].(bool); ok {
		cfg.Auto = a
	}
	list, _ := m["signals"].([]any)
	for _, x := range list {
		s, _ := x.(map[string]any)
		if s == nil {
			continue
		}
		if en, ok := s["enabled"].(bool); ok && !en {
			continue
		}
		str := func(k string) string { v, _ := s[k].(string); return v }
		sp := SignalSpec{ID: str("id"), Name: str("name"), Kind: str("kind"), Description: str("description")}
		if p := str("preset"); p != "" && p == sp.ID {
			sp.Preset = p
		}
		if r, ok := store.ToFloat(s["sample_rate"]); ok {
			sp.SampleRate = &r
		}
		if rs, err := sp.Resolve(); err == nil {
			cfg.Signals = append(cfg.Signals, rs)
		}
	}
	return cfg
}

func (e *Engine) signalsConfig(ctx context.Context, projectID string) (*SignalsConfig, error) {
	signalsCacheMu.Lock()
	ent, ok := signalsCache[e][projectID]
	signalsCacheMu.Unlock()
	if ok && time.Since(ent.at) < 10*time.Second {
		return ent.cfg, nil
	}
	objs, err := e.Store.QueryObjects(ctx, store.ObjsQuery{ProjectID: projectID, Filter: &store.ObjsFilter{ObjectIDs: []string{store.SignalsObjectID}, LatestOnly: true}, Limit: 1})
	if err != nil {
		return nil, err
	}
	cfg := &SignalsConfig{}
	if len(objs) > 0 {
		cfg = ParseSignalsConfig(objs[0].Val)
	}
	signalsCacheMu.Lock()
	if signalsCache[e] == nil {
		signalsCache[e] = map[string]signalsCacheEntry{}
	}
	signalsCache[e][projectID] = signalsCacheEntry{at: time.Now(), cfg: cfg}
	signalsCacheMu.Unlock()
	return cfg, nil
}

func (e *Engine) invalidateSignals(projectID string) {
	signalsCacheMu.Lock()
	delete(signalsCache[e], projectID)
	signalsCacheMu.Unlock()
}

// runAutoSignals scores an ended call with the project's enabled signals
// when the call is an agent turn. Each signal's sample rate applies;
// internal calls and turns already scored by the same signal version are
// skipped.
func (e *Engine) runAutoSignals(ctx context.Context, ended store.EndedCall) error {
	cfg, err := e.signalsConfig(ctx, ended.ProjectID)
	if err != nil || !cfg.Auto || cfg.Model == "" || len(cfg.Signals) == 0 {
		return err
	}
	call, err := e.Store.GetCall(ctx, ended.ProjectID, ended.ID, false, false, false)
	if err != nil || call == nil || isInternal(call) || call.EndedAt == nil {
		return err
	}
	turn, ok, err := e.Store.SignalTurnForCall(ctx, ended.ProjectID, ended.ID)
	if err != nil || !ok {
		return err
	}
	creator := "weft-signals-auto"
	for _, sp := range cfg.Signals {
		rate := 1.0
		if sp.SampleRate != nil {
			rate = *sp.SampleRate
		}
		if rate <= 0 || (rate < 1 && rand.Float64() >= rate) {
			continue
		}
		key := ended.ProjectID + "/" + turn.SpanID + "/" + sp.ID + "/" + sp.Version()
		if _, busy := signalInflight.LoadOrStore(key, struct{}{}); busy {
			continue
		}
		done, err := e.Store.SignalScoredTurns(ctx, ended.ProjectID, sp.ID, sp.Version(), []string{turn.SpanID})
		if err == nil && !done[turn.SpanID] {
			if r := e.ScoreTurnSignal(ctx, ended.ProjectID, turn, sp, cfg.Model, &creator, nil); r.Error != "" {
				e.Log.Warn("signal scoring failed", "signal", sp.ID, "span_id", turn.SpanID, "err", r.Error)
			}
		}
		signalInflight.Delete(key)
		if err != nil {
			return err
		}
	}
	return nil
}
