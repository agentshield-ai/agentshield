package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// attnSession is one session: a root agent span, optionally failed, and an
// optional failed tool call under it.
func attnSession(i int, session string, rootErr, toolErr bool) []CompleteCall {
	at := time.Duration(i) * time.Millisecond
	root := CallStart{ProjectID: proj, ID: fmt.Sprintf("r-%s", session), TraceID: "t-" + session, OpName: "invoke_agent", StartedAt: detTS(at),
		Attributes: map[string]any{"otel": map[string]any{"attributes": map[string]any{"gen_ai.operation.name": "invoke_agent",
			"gen_ai.agent.name": "coder", "gen_ai.conversation.id": session}}}, Inputs: map[string]any{}}
	end := &CallEnd{EndedAt: detTS(at + time.Second), Summary: map[string]any{}}
	if rootErr {
		e := "boom"
		end.Exception = &e
	}
	out := []CompleteCall{{Start: root, End: end}}
	if toolErr {
		p := root.ID
		tool := CallStart{ProjectID: proj, ID: "x-" + session, TraceID: root.TraceID, ParentID: &p, OpName: "execute_tool", StartedAt: detTS(at + 10*time.Millisecond),
			Attributes: map[string]any{"otel": map[string]any{"attributes": map[string]any{"gen_ai.operation.name": "execute_tool",
				"gen_ai.tool.name": "Bash"}}}, Inputs: map[string]any{"command": "ls"}}
		e := "exit 1"
		out = append(out, CompleteCall{Start: tool, End: &CallEnd{EndedAt: detTS(at + 20*time.Millisecond), Summary: map[string]any{}, Exception: &e}})
	}
	return out
}

// The attention counts and the sessions filters behind them run over every
// matching session: more than one page (1000) of sessions is counted in
// full, not truncated.
func TestSessionsAttentionCountsEverySession(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const failed = 1005
	var calls []CompleteCall
	for i := 0; i < failed; i++ {
		calls = append(calls, attnSession(i, fmt.Sprintf("f-%04d", i), true, false)...)
	}
	calls = append(calls, attnSession(2000, "step-1", false, true)...)
	calls = append(calls, attnSession(2001, "blocked-1", false, false)...)
	calls = append(calls, attnSession(2002, "approvals-1", false, false)...)
	calls = append(calls, attnSession(2003, "low-1", false, false)...)
	if _, err := s.UpsertComplete(ctx, calls); err != nil {
		t.Fatal(err)
	}
	mustUpsert(t, s, det("e-b", "blocked-1", "Bash", "", "block", 0),
		det("e-a1", "approvals-1", "Bash", "", "require_approval", 0), det("e-a2", "approvals-1", "Bash", "", "require_approval", 0),
		det("e-a3", "approvals-1", "Bash", "", "require_approval", 0), det("e-a4", "step-1", "Bash", "", "require_approval", 0))
	v := 0.2
	if _, err := s.CreateScopeScore(ctx, ScopeScoreCreate{ProjectID: proj, Scope: ScopeSession, SessionID: "low-1", Scorer: "goal", Value: &v}); err != nil {
		t.Fatal(err)
	}

	a, err := s.SessionsAttention(ctx, SessionsQuery{ProjectID: proj})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, c := range a.Items {
		got[c.ID] = c.Count
	}
	want := map[string]int{"failed": failed, "failed_step": 1, "security": 2, "low_score": 1, "cost": 0}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("%s = %d, want %d", k, got[k], n)
		}
	}
	if a.Sessions != failed+4 || a.Blocked != 1 || a.ApprovalHeavy != 1 || a.LowestScore == nil || *a.LowestScore != 0.2 || a.CostSpikeThreshold != nil {
		t.Errorf("attention = %+v", a)
	}
	if a.Items[0].Latest == nil || a.Items[0].Latest.SessionID != "f-1004" {
		t.Errorf("latest failed = %+v", a.Items[0].Latest)
	}
	// Each card's filter lists exactly the sessions it counts.
	for _, c := range a.Items {
		if len(c.Filter) == 0 {
			continue
		}
		q := SessionsQuery{ProjectID: proj, Status: c.Filter["status"], HasDetection: c.Filter["has_detection"], ScoreBelow: c.Filter["score_below"]}
		_, total, err := s.QuerySessions(ctx, q)
		if err != nil || total != c.Count {
			t.Errorf("%s: filter %v lists %d sessions (%v), card counts %d", c.ID, c.Filter, total, err, c.Count)
		}
	}
	// A filter combines with the card: failed sessions of another agent.
	if a, err := s.SessionsAttention(ctx, SessionsQuery{ProjectID: proj, Agent: "nobody"}); err != nil || a.Sessions != 0 || a.Items[0].Count != 0 {
		t.Errorf("agent filter: %+v %v", a, err)
	}

	// Detections filtered through sessions resolve every matching session.
	ids, err := s.MatchingSessionIDs(ctx, SessionsQuery{ProjectID: proj, Status: "ok"})
	if err != nil || len(ids) != 4 {
		t.Fatalf("ok sessions = %v %v", ids, err)
	}
	_, total, err := s.QueryDetections(ctx, DetectionQuery{ProjectID: proj, Sessions: &SessionsQuery{HasDetection: HasDetectionFlagged}})
	if err != nil || total != 4 {
		t.Errorf("detections in flagged sessions = %d %v", total, err)
	}
	_, total, err = s.QueryDetections(ctx, DetectionQuery{ProjectID: proj, Sessions: &SessionsQuery{Status: "error"}})
	if err != nil || total != 0 {
		t.Errorf("detections in failed sessions = %d %v (want 0: none of them has one)", total, err)
	}
	st, err := s.DetectionStatsFor(ctx, DetectionQuery{ProjectID: proj, Sessions: &SessionsQuery{Status: SessionStatusFailedStep}})
	if err != nil || st.Total != 1 {
		t.Errorf("stats in failed-step sessions = %+v %v", st, err)
	}
}

func TestSessionFiltersCostAndScore(t *testing.T) {
	if th, ok := CostSpikeThreshold([]float64{0.001, 0.0016, 0.0016, 0.002, 0}); !ok || th != 0.0048 {
		t.Errorf("threshold = %v %v", th, ok)
	}
	if _, ok := CostSpikeThreshold([]float64{1, 2, 3}); ok {
		t.Error("threshold from three sessions")
	}
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.UpsertComplete(ctx, append(attnSession(0, "a", false, false), attnSession(1, "b", false, false)...)); err != nil {
		t.Fatal(err)
	}
	for sess, v := range map[string]float64{"a": 0.4, "b": 0.9} {
		v := v
		if _, err := s.CreateScopeScore(ctx, ScopeScoreCreate{ProjectID: proj, Scope: ScopeSession, SessionID: sess, Scorer: "goal", Value: &v}); err != nil {
			t.Fatal(err)
		}
	}
	for in, want := range map[string]int{"0.5": 1, "goal:0.5": 1, "other:0.5": 0, "0.95": 2, "0.4": 0} {
		if _, n, err := s.QuerySessions(ctx, SessionsQuery{ProjectID: proj, ScoreBelow: in}); err != nil || n != want {
			t.Errorf("score_below=%s: %d %v, want %d", in, n, err, want)
		}
	}
	if _, _, err := s.QuerySessions(ctx, SessionsQuery{ProjectID: proj, ScoreBelow: "x"}); err == nil {
		t.Error("bad score_below accepted")
	}
	zero, one := 0.0, 1.0
	if _, n, _ := s.QuerySessions(ctx, SessionsQuery{ProjectID: proj, CostMin: &zero}); n != 2 {
		t.Errorf("cost_min 0 = %d", n)
	}
	if _, n, _ := s.QuerySessions(ctx, SessionsQuery{ProjectID: proj, CostMin: &one}); n != 0 {
		t.Errorf("cost_min 1 = %d", n)
	}
}

// Per-agent detection counts are engine verdicts, like the rate beside them;
// replay detections count only when source=replay is asked for.
func TestDetectionStatsByAgentExcludesReplay(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	live := det("e-1", "s1", "Bash", "", "block", 0)
	live.AgentName = "coder"
	rp := det("rp-1", "s1", "Bash", "", "block", 0)
	rp.AgentName, rp.Source, rp.Ruleset = "coder", DetectionSourceReplay, "candidate"
	rp2 := rp
	rp2.EventID, rp2.AgentName = "rp-2", "ghost"
	mustUpsert(t, s, live, rp, rp2)
	st, err := s.DetectionStatsFor(ctx, DetectionQuery{ProjectID: proj})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.ByAgent) != 1 || st.ByAgent[0].Agent != "coder" || st.ByAgent[0].Count != 1 || st.ByAgent[0].ByAction["block"] != 1 {
		t.Errorf("by_agent = %+v", st.ByAgent)
	}
	if st.Total != 3 || st.BySource[DetectionSourceReplay] != 2 {
		t.Errorf("totals = %d %v", st.Total, st.BySource)
	}
	st, err = s.DetectionStatsFor(ctx, DetectionQuery{ProjectID: proj, Source: DetectionSourceReplay})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.ByAgent) != 2 {
		t.Errorf("replay by_agent = %+v", st.ByAgent)
	}
}
