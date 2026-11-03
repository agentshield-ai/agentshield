package evals

// Scope scorers (docs/weft/DESIGN.md §6): judges that score a session, an
// agent within a session, or a hand-off, and the runner behind
// POST /api/v1/scores/run. Results are stored as scope scores (feedback
// with feedback_type weft.score.<scorer> and the scope in the payload).
//
// The three agent judges are also registered as server-runnable scorer
// classes, so a scorer object of that class can run on a single call too;
// there {input} is the call's inputs and {output} its output.

import (
	"context"
	"fmt"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// The judge prompts. {input} is the context or request, {output} what is
// being judged; {{ and }} are literal braces.
const handoffQualityPrompt = "You are reviewing a hand-off between two AI agents.\n\n" +
	"Context on the delegating side, ending with the input it passed on:\n<handoff_context>\n{input}\n</handoff_context>\n\n" +
	"What the receiving agent sent back, and what the delegating agent did afterwards:\n<handoff_outcome>\n{output}\n</handoff_outcome>\n\n" +
	"Decide two things. First, given the request, was it sensible to hand this piece of work to that agent, with that input? " +
	"Second, did the delegating agent actually make use of what came back? Rate the hand-off overall from 0 (harmful or wasted) to 1 (well chosen and well used). " +
	`Answer with JSON only: {{"appropriate": true|false, "result_used": true|false, "score": <number from 0 to 1>, "reasoning": "<one or two sentences>"}}`

const reasoningConsistencyPrompt = "You are checking whether an AI agent acted on its own stated reasoning.\n\n" +
	"The task the agent was given:\n<task>\n{input}\n</task>\n\n" +
	"The agent's reasoning and actions, in the order they happened:\n<trace>\n{output}\n</trace>\n\n" +
	"For each action, ask whether it follows from the reasoning written before it. Also look for reasoning that announced a step " +
	"which never happened, or that a later action contradicted. Rate consistency from 0 (actions unrelated to the reasoning) to 1 (fully consistent). " +
	`Answer with JSON only: {{"consistent": true|false, "score": <number from 0 to 1>, "reasoning": "<one or two sentences>"}}`

const goalCompletionPrompt = "You are deciding whether a session with an AI agent system achieved what the user wanted.\n\n" +
	"The user's request:\n<goal>\n{input}\n</goal>\n\n" +
	"What happened in the session, ending with the final answer:\n<session>\n{output}\n</session>\n\n" +
	"Judge the outcome, not the style: does the final result satisfy the request fully, partly, or not at all? " +
	"Rate completion from 0 (not achieved) to 1 (fully achieved). " +
	`Answer with JSON only: {{"goal_achieved": true|false, "score": <number from 0 to 1>, "reasoning": "<one or two sentences>"}}`

// scopeJudge describes one built-in scope judge.
type scopeJudge struct {
	prompt   string
	boolKeys []string
	scopes   map[string]bool
}

// ScopeJudges are the built-in agent judges and the scopes they apply to.
var ScopeJudges = map[string]scopeJudge{
	"HandoffQualityJudge":       {handoffQualityPrompt, []string{"appropriate", "result_used"}, map[string]bool{store.ScopeHandoff: true}},
	"ReasoningConsistencyJudge": {reasoningConsistencyPrompt, []string{"consistent"}, map[string]bool{store.ScopeAgent: true, store.ScopeSession: true}},
	"GoalCompletionJudge":       {goalCompletionPrompt, []string{"goal_achieved"}, map[string]bool{store.ScopeSession: true}},
}

// ScorerScopes limits scorer classes to scopes when they run through
// POST /api/v1/scores/run; classes not listed run at any scope. The agent
// judges are added from ScopeJudges.
var ScorerScopes = map[string]map[string]bool{
	"ToolSafetyScorer": {store.ScopeSession: true, store.ScopeAgent: true},
}

// DefaultJudgeModel is used when neither the request nor the scorer names one.
const DefaultJudgeModel = "gpt-4o-mini"

func init() {
	for name, j := range ScopeJudges {
		extraScorers[name] = scopeJudgeFunc(j)
		RunnableScorers[name] = true
		ScorerScopes[name] = j.scopes
	}
}

// scopeJudgeFunc runs a judge in JSON mode and normalises its reply to
// {score (0..1), <bool keys>, reasoning}. A generic {score 1-5, correct}
// reply (the offline mock/json model) is mapped onto the same shape.
func scopeJudgeFunc(j scopeJudge) scorerFunc {
	return func(e *Engine, ctx context.Context, projectID string, val, example map[string]any, output any, rec *callRec, _ map[string]any) (any, error) {
		jv := map[string]any{}
		for k, v := range val {
			jv[k] = v
		}
		if strField(jv, "scoring_prompt") == "" {
			jv["scoring_prompt"] = j.prompt
		}
		if jv["model"] == nil || jv["model"] == "" {
			if m := strField(jv, "model_id"); m != "" {
				jv["model"] = m
			} else {
				jv["model"] = DefaultJudgeModel
			}
		}
		jv["response_format"] = "json_object"
		res, err := e.judge(ctx, projectID, jv, example, output, rec)
		if err != nil {
			return nil, err
		}
		m, ok := res.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("the judge did not reply with a JSON object")
		}
		s, hasScore := judgeScore01(m)
		for _, k := range j.boolKeys {
			if _, ok := m[k].(bool); ok {
				continue
			}
			if c, ok := m["correct"].(bool); ok {
				m[k] = c
			} else if hasScore {
				m[k] = s >= 0.5
			}
		}
		if hasScore {
			m["score"] = s
		} else if b, ok := m[j.boolKeys[0]].(bool); ok {
			if b {
				m["score"] = 1.0
			} else {
				m["score"] = 0.0
			}
		}
		if _, ok := m["reasoning"]; !ok {
			if r, ok := m["reason"].(string); ok {
				m["reasoning"] = r
			}
		}
		return m, nil
	}
}

// ScopeRunReq is the body of POST /api/v1/scores/run.
type ScopeRunReq struct {
	ProjectID  string   `json:"project_id"`
	Scope      string   `json:"scope"`
	Scorers    []string `json:"scorers"`
	SessionIDs []string `json:"session_ids"`
	Agent      string   `json:"agent"`
	HandoffIDs []string `json:"handoff_ids"`
	// Model overrides the judge model of built-in judges (e.g. mock/json).
	Model string `json:"model"`
}

// ScopeRunResult is one scorer result on one target.
type ScopeRunResult struct {
	Scope        string   `json:"scope"`
	SessionID    string   `json:"session_id"`
	Agent        string   `json:"agent,omitempty"`
	HandoffID    string   `json:"handoff_id,omitempty"`
	Scorer       string   `json:"scorer"`
	Value        *float64 `json:"value"`
	Reason       string   `json:"reason,omitempty"`
	Output       any      `json:"output,omitempty"`
	FeedbackID   string   `json:"feedback_id,omitempty"`
	ScorerCallID string   `json:"scorer_call_id,omitempty"`
	Error        string   `json:"error,omitempty"`
}

const (
	maxScopeTargets = 100
	maxScopeScorers = 10
)

// RunScopeScorers scores sessions, agents or hand-offs and stores the scores.
func (e *Engine) RunScopeScorers(ctx context.Context, req ScopeRunReq, user *string) ([]ScopeRunResult, error) {
	pid, err := refs.NormalizeProject(req.ProjectID)
	if err != nil {
		return nil, &store.InvalidError{Msg: err.Error()}
	}
	switch req.Scope {
	case store.ScopeSession, store.ScopeAgent, store.ScopeHandoff:
	default:
		return nil, store.Invalidf("scope must be session, agent or handoff")
	}
	if len(req.Scorers) == 0 || len(req.Scorers) > maxScopeScorers {
		return nil, store.Invalidf("scorers must list 1 to %d scorers", maxScopeScorers)
	}
	if len(req.SessionIDs) == 0 && !(req.Scope == store.ScopeHandoff && len(req.HandoffIDs) > 0) {
		return nil, store.Invalidf("session_ids is required (or handoff_ids for the handoff scope)")
	}
	if len(req.SessionIDs) > maxScopeTargets || len(req.HandoffIDs) > maxScopeTargets {
		return nil, store.Invalidf("at most %d sessions or hand-offs per run", maxScopeTargets)
	}
	scorers := make([]*Scorer, 0, len(req.Scorers))
	for _, name := range req.Scorers {
		name = strings.TrimSpace(name)
		if !refs.IsRef(name) {
			// A scorer class by name: the agent judges, ToolSafetyScorer, or
			// any server-runnable class that needs no configuration.
			if !RunnableScorers[name] {
				return nil, store.Invalidf("unknown scorer %q: use a scorer class (%s) or a scorer ref", name, runnableScorerList())
			}
			if sc, ok := ScorerScopes[name]; ok && !sc[req.Scope] {
				return nil, store.Invalidf("%s does not score at the %s scope", name, req.Scope)
			}
			val := map[string]any{"_class_name": name}
			if _, judge := ScopeJudges[name]; judge && req.Model != "" {
				val["model"] = req.Model
			}
			scorers = append(scorers, &Scorer{Name: name, Val: val})
			continue
		}
		sc, err := e.loadScorer(ctx, refs.Normalize(name))
		if err != nil {
			return nil, err
		}
		if allowed, ok := ScorerScopes[className(sc.Val)]; ok && !allowed[req.Scope] {
			return nil, store.Invalidf("%s does not score at the %s scope", sc.Name, req.Scope)
		}
		if req.Model != "" && ScopeJudges[className(sc.Val)].prompt != "" {
			v := map[string]any{}
			for k, x := range sc.Val {
				v[k] = x
			}
			v["model"] = req.Model
			sc.Val = v
		}
		scorers = append(scorers, sc)
	}
	targets, err := e.Store.ScopeTargets(ctx, store.ScopeTargetsReq{ProjectID: pid, Scope: req.Scope, SessionIDs: req.SessionIDs,
		Agent: req.Agent, HandoffIDs: req.HandoffIDs})
	if err != nil {
		return nil, err
	}
	if len(targets) > maxScopeTargets {
		return nil, store.Invalidf("this run has %d targets; at most %d are allowed", len(targets), maxScopeTargets)
	}
	out := []ScopeRunResult{}
	for _, t := range targets {
		for _, sc := range scorers {
			out = append(out, e.scoreTarget(ctx, pid, t, sc, user))
		}
	}
	return out, nil
}

func (e *Engine) scoreTarget(ctx context.Context, pid string, t store.ScopeTarget, sc *Scorer, user *string) ScopeRunResult {
	res := ScopeRunResult{Scope: t.Scope, SessionID: t.SessionID, Agent: t.Agent, HandoffID: t.HandoffID, Scorer: sc.Name}
	example := map[string]any{"input": t.Input, "session_id": t.SessionID}
	if t.Agent != "" {
		example["agent"] = t.Agent
	}
	if t.HandoffID != "" {
		example["handoff_id"] = t.HandoffID
	}
	attrs := map[string]any{"weft": map[string]any{"internal": true, "scope": t.Scope, "scored_session_id": t.SessionID}}
	score, rec, err := e.RunScorer(ctx, pid, sc, example, t.Output, nil, attrs)
	if rec != nil {
		res.ScorerCallID = rec.id
	}
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Output = jsonSafe(score)
	res.Value = store.ScoreValue(res.Output)
	if m, ok := res.Output.(map[string]any); ok {
		res.Reason = firstString(m, "reasoning", "reason")
	}
	model, _ := sc.Val["model"].(string)
	var runnable *string
	if sc.Ref != "" {
		r := sc.Ref
		runnable = &r
	}
	var callRef *string
	if rec != nil {
		r := refs.CallRef(pid, rec.id)
		callRef = &r
	}
	fb, err := e.Store.CreateScopeScore(ctx, store.ScopeScoreCreate{ProjectID: pid, Scope: t.Scope, SessionID: t.SessionID, Agent: t.Agent,
		HandoffID: t.HandoffID, Scorer: sc.Name, Value: res.Value, Reason: res.Reason, Output: res.Output, Model: model, WBUserID: user,
		RunnableRef: runnable, CallRef: callRef})
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.FeedbackID = fb.ID
	return res
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}
