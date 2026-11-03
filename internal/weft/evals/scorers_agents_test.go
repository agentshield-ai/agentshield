package evals

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/llm"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

func agentSpan(pid, id, parent, name string, start, dur int, attrs map[string]any, inputs map[string]any, output any) store.CompleteCall {
	base := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	st := store.CallStart{ProjectID: pid, ID: id, TraceID: "tr", OpName: name,
		StartedAt:  base.Add(time.Duration(start) * time.Millisecond).Format(time.RFC3339Nano),
		Attributes: map[string]any{"otel": map[string]any{"span_name": name, "attributes": attrs}}, Inputs: inputs}
	if parent != "" {
		st.ParentID = &parent
	}
	return store.CompleteCall{Start: st, End: &store.CallEnd{EndedAt: base.Add(time.Duration(start+dur) * time.Millisecond).Format(time.RFC3339Nano),
		Output: output, Summary: map[string]any{}}}
}

func TestScopeJudgesOffline(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := New(st, llm.New(nil, nil), nil)
	defer e.Close()
	ctx := context.Background()
	pid := "acme/judges"
	_, err = st.UpsertComplete(ctx, []store.CompleteCall{
		agentSpan(pid, "lead", "", "invoke_agent lead", 0, 1000, map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "lead",
			"gen_ai.conversation.id": "j1"}, map[string]any{"messages": []any{map[string]any{"role": "user", "content": "Plan a trip to Rome"}}}, "Trip planned."),
		agentSpan(pid, "c1", "lead", "chat", 10, 50, map[string]any{"gen_ai.operation.name": "chat"}, map[string]any{},
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "thinking", "thinking": "Ask the booker for flights."}}}),
		agentSpan(pid, "book", "lead", "invoke_agent booker", 100, 500, map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "booker"},
			map[string]any{"task": "Find flights to Rome"}, "Flight AZ123 at 9:00"),
	})
	if err != nil {
		t.Fatal(err)
	}
	hs, _, err := st.QueryHandoffs(ctx, store.HandoffsQuery{ProjectID: pid, Kind: store.HandoffDelegate})
	if err != nil || len(hs) != 1 {
		t.Fatalf("handoffs %v %v", hs, err)
	}
	cases := []struct {
		req     ScopeRunReq
		results int
		boolKey string
	}{
		{ScopeRunReq{Scope: "handoff", Scorers: []string{"HandoffQualityJudge"}, HandoffIDs: []string{hs[0].ID}}, 1, "result_used"},
		{ScopeRunReq{Scope: "handoff", Scorers: []string{"HandoffQualityJudge"}, SessionIDs: []string{"j1"}}, 1, "appropriate"},
		{ScopeRunReq{Scope: "agent", Scorers: []string{"ReasoningConsistencyJudge"}, SessionIDs: []string{"j1"}}, 2, "consistent"},
		{ScopeRunReq{Scope: "agent", Scorers: []string{"ReasoningConsistencyJudge"}, SessionIDs: []string{"j1"}, Agent: "booker"}, 1, "consistent"},
		{ScopeRunReq{Scope: "session", Scorers: []string{"GoalCompletionJudge", "ReasoningConsistencyJudge"}, SessionIDs: []string{"j1"}}, 2, ""},
	}
	for _, c := range cases {
		c.req.ProjectID, c.req.Model = pid, "mock/json"
		res, err := e.RunScopeScorers(ctx, c.req, nil)
		if err != nil {
			t.Fatalf("%+v: %v", c.req, err)
		}
		if len(res) != c.results {
			t.Fatalf("%+v: %d results", c.req, len(res))
		}
		for _, r := range res {
			if r.Error != "" || r.Value == nil || *r.Value < 0 || *r.Value > 1 || r.FeedbackID == "" || r.Reason == "" {
				t.Fatalf("result %+v", r)
			}
			out := r.Output.(map[string]any)
			if c.boolKey != "" {
				if _, ok := out[c.boolKey].(bool); !ok {
					t.Fatalf("%s missing in %v", c.boolKey, out)
				}
			}
		}
	}
	scores, total, err := st.QueryScopeScores(ctx, store.ScopeScoresQuery{ProjectID: pid, SessionID: "j1"})
	if err != nil || total != 7 {
		t.Fatalf("scores %d %v", total, err)
	}
	for _, sc := range scores {
		if sc.Model != "mock/json" || sc.Ref == "" || sc.SessionID != "j1" {
			t.Fatalf("score %+v", sc)
		}
	}
	// The judge prompt carries the scope context.
	tg, err := st.ScopeTargets(ctx, store.ScopeTargetsReq{ProjectID: pid, Scope: "handoff", HandoffIDs: []string{hs[0].ID}})
	if err != nil || len(tg) != 1 {
		t.Fatal(err)
	}
	for _, want := range []string{"Plan a trip to Rome", "Find flights to Rome", "Flight AZ123", "Ask the booker for flights."} {
		if !strings.Contains(tg[0].Input+tg[0].Output, want) {
			t.Fatalf("handoff context lacks %q:\n%s\n---\n%s", want, tg[0].Input, tg[0].Output)
		}
	}
	// Each judge is also a server-runnable scorer class.
	for name := range ScopeJudges {
		if !RunnableScorers[name] {
			t.Fatalf("%s not runnable", name)
		}
	}
}

func TestScopeJudgeNormalisesReplies(t *testing.T) {
	j := ScopeJudges["HandoffQualityJudge"]
	if j.prompt == "" || len(j.boolKeys) != 2 || !j.scopes["handoff"] || j.scopes["session"] {
		t.Fatalf("judge %+v", j)
	}
	for _, p := range []string{handoffQualityPrompt, reasoningConsistencyPrompt, goalCompletionPrompt} {
		r := Render(p, map[string]any{"input": "IN", "output": "OUT"})
		if !strings.Contains(r, "IN") || !strings.Contains(r, "OUT") || !strings.Contains(r, `{"`) || strings.Contains(r, "{input}") {
			t.Fatalf("prompt renders badly: %s", r)
		}
	}
}
