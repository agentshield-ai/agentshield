package evals

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/llm"
	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

func publishObj(t *testing.T, st *store.Store, pid, id string, val map[string]any) string {
	t.Helper()
	d, _, err := st.CreateObject(context.Background(), pid, id, val, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return refs.ObjectRef(pid, id, d, false)
}

func monitorObj(scorers []string, extra map[string]any) map[string]any {
	l := make([]any, len(scorers))
	for i, s := range scorers {
		l[i] = s
	}
	v := map[string]any{"_class_name": "Monitor", "_bases": []any{"Object", "BaseModel"}, "active": true, "sampling_rate": 1,
		"op_names": []any{}, "scorers": l, "session_idle_seconds": 0}
	for k, x := range extra {
		v[k] = x
	}
	return v
}

func scopeScores(t *testing.T, st *store.Store, q store.ScopeScoresQuery) []store.ScopeScore {
	t.Helper()
	l, _, err := st.QueryScopeScores(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// Monitors with scope scorers score new sessions, agents and hand-offs
// once the session is complete, at most once per target, and honour
// sampling (DESIGN.md §6).
func TestMonitorsRunScopeScorersOnCompleteSessions(t *testing.T) {
	old := scopeTick
	scopeTick = 20 * time.Millisecond
	defer func() { scopeTick = old }()
	st, err := store.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := New(st, llm.New(nil, nil), nil)
	defer e.Close()
	ctx := context.Background()
	pid := "acme/mon"

	goal := publishObj(t, st, pid, "goal", map[string]any{"_class_name": "GoalCompletionJudge", "model": "mock/json"})
	handoff := publishObj(t, st, pid, "handoff", map[string]any{"_class_name": "HandoffQualityJudge", "model": "mock/json"})
	reasoning := publishObj(t, st, pid, "reasoning", map[string]any{"_class_name": "ReasoningConsistencyJudge", "model": "mock/json"})
	publishObj(t, st, pid, "goal-monitor", monitorObj([]string{goal, handoff}, nil))
	publishObj(t, st, pid, "agent-monitor", monitorObj([]string{reasoning}, map[string]any{"scopes": []any{"agent"}}))
	publishObj(t, st, pid, "never-monitor", monitorObj([]string{goal}, map[string]any{"sampling_rate": 0}))
	publishObj(t, st, pid, "slow-monitor", monitorObj([]string{goal}, map[string]any{"session_idle_seconds": 3600}))

	ended, err := st.UpsertComplete(ctx, []store.CompleteCall{
		agentSpan(pid, "lead", "", "invoke_agent lead", 0, 1000, map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "lead",
			"gen_ai.conversation.id": "m1"}, map[string]any{"messages": []any{map[string]any{"role": "user", "content": "Plan a trip to Rome"}}}, "Trip planned."),
		agentSpan(pid, "c1", "lead", "chat", 10, 50, map[string]any{"gen_ai.operation.name": "chat"}, map[string]any{},
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "thinking", "thinking": "Ask the booker for flights."}}}),
		agentSpan(pid, "book", "lead", "invoke_agent booker", 100, 500, map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "booker"},
			map[string]any{"task": "Find flights to Rome"}, "Flight AZ123 at 9:00"),
	})
	if err != nil {
		t.Fatal(err)
	}
	e.Notify(ended)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(scopeScores(t, st, store.ScopeScoresQuery{ProjectID: pid, Limit: 100})) >= 4 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond) // let any extra (wrong) scores land
	sess := scopeScores(t, st, store.ScopeScoresQuery{ProjectID: pid, Scope: store.ScopeSession, Limit: 100})
	if len(sess) != 1 || sess[0].Scorer != "goal" || sess[0].SessionID != "m1" || sess[0].Value == nil {
		t.Fatalf("session scores = %+v", sess)
	}
	if h := scopeScores(t, st, store.ScopeScoresQuery{ProjectID: pid, Scope: store.ScopeHandoff, Limit: 100}); len(h) != 1 || h[0].Scorer != "handoff" {
		t.Fatalf("hand-off scores = %+v", h)
	}
	if a := scopeScores(t, st, store.ScopeScoresQuery{ProjectID: pid, Scope: store.ScopeAgent, Limit: 100}); len(a) != 2 {
		t.Fatalf("agent scores = %+v", a)
	}
	// The score is attributed to the monitor and shows in the session summary.
	fb, err := st.QueryFeedback(ctx, pid, nil, nil, 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fb {
		if f.FeedbackType == "weft.scorer.goal" || f.FeedbackType == "weft.scorer.handoff" {
			t.Errorf("scope scorer ran on a single call: %+v", f)
		}
		if f.FeedbackType == "weft.score.goal" && (f.TriggerRef == nil || f.Creator == nil || *f.Creator != "monitor:goal-monitor") {
			t.Errorf("score not attributed to the monitor: %+v", f)
		}
	}
	ss, err := st.GetAgentSession(ctx, pid, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := ss.Scores["goal"]; !ok || a.Count != 1 {
		t.Fatalf("session summary scores = %+v", ss.Scores)
	}

	// The same session ending again (or new spans) does not score it twice.
	e.Notify(ended)
	time.Sleep(200 * time.Millisecond)
	if n := len(scopeScores(t, st, store.ScopeScoresQuery{ProjectID: pid, Limit: 100})); n != 4 {
		t.Fatalf("scores after re-notify = %d, want 4", n)
	}
	// Sampled out once, recorded, and not retried.
	tgt := store.ScopeTarget{Scope: store.ScopeSession, SessionID: "m1"}
	if s, _ := st.MonitorTargetStatus(ctx, pid, "never-monitor", goal, tgt); s != store.MonitorTargetSampledOut {
		t.Errorf("sampled-out status = %q", s)
	}
	// A monitor waiting for an hour of idle time has not scored yet.
	if s, _ := st.MonitorTargetStatus(ctx, pid, "slow-monitor", goal, tgt); s != "" {
		t.Errorf("slow monitor status = %q", s)
	}
}

// A running session is not complete; ten times the idle time later it is.
func TestScopeMonitorCompletion(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := New(st, llm.New(nil, nil), nil)
	defer e.Close()
	ctx := context.Background()
	pid := "acme/run"
	goal := publishObj(t, st, pid, "goal", map[string]any{"_class_name": "GoalCompletionJudge", "model": "mock/json"})
	publishObj(t, st, pid, "m", monitorObj([]string{goal}, map[string]any{"session_idle_seconds": 1}))
	start := store.CallStart{ProjectID: pid, ID: "root", TraceID: "t", OpName: "invoke_agent a", StartedAt: "2026-06-01T09:00:00Z",
		Attributes: map[string]any{"otel": map[string]any{"span_name": "invoke_agent a", "attributes": map[string]any{
			"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "a", "gen_ai.conversation.id": "r1"}}}}
	if _, err := st.UpsertComplete(ctx, []store.CompleteCall{{Start: start}}); err != nil {
		t.Fatal(err)
	}
	k := pendingKey{pid, "r1"}
	if done, err := e.scoreSessionIfComplete(ctx, k, 2*time.Second); err != nil || done {
		t.Fatalf("running session counted as complete: %v %v", done, err)
	}
	if done, err := e.scoreSessionIfComplete(ctx, k, 500*time.Millisecond); err != nil || done {
		t.Fatalf("session idle for less than session_idle_seconds counted as complete: %v %v", done, err)
	}
	if done, err := e.scoreSessionIfComplete(ctx, k, 11*time.Second); err != nil || !done {
		t.Fatalf("abandoned session not complete: %v %v", done, err)
	}
	if n := len(scopeScores(t, st, store.ScopeScoresQuery{ProjectID: pid, Limit: 10})); n != 1 {
		t.Fatalf("scores = %d", n)
	}
}
