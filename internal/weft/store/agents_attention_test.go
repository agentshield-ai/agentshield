package store

import (
	"context"
	"fmt"
	"sort"
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
	// ok means nothing failed anywhere, so step-1 (a failed tool call) is not ok.
	ids, err := s.MatchingSessionIDs(ctx, SessionsQuery{ProjectID: proj, Status: "ok"})
	if err != nil || len(ids) != 3 {
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

// statusSpan is one stored span for the status tests: OTLP attributes,
// optional failure, scope and span links.
type statusSpan struct {
	id, trace, parent string
	at                time.Duration
	attrs             map[string]any
	err, open         bool
	scope             string
	links             []map[string]any
}

func (sp statusSpan) call() CompleteCall {
	o := map[string]any{"attributes": sp.attrs}
	if sp.scope != "" {
		o["scope"] = map[string]any{"name": sp.scope}
	}
	if sp.links != nil {
		o["links"] = sp.links
	}
	st := CallStart{ProjectID: proj, ID: sp.id, TraceID: sp.trace, OpName: "span", StartedAt: detTS(sp.at), Attributes: map[string]any{"otel": o}, Inputs: map[string]any{}}
	if sp.parent != "" {
		st.ParentID = ptr(sp.parent)
	}
	if sp.open {
		return CompleteCall{Start: st}
	}
	end := &CallEnd{EndedAt: detTS(sp.at + time.Second), Summary: map[string]any{}}
	if sp.err {
		e := "boom"
		end.Exception = &e
	}
	return CompleteCall{Start: st, End: end}
}

func statusAgentAttrs(agent, session string) map[string]any {
	a := map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": agent}
	if session != "" {
		a["gen_ai.conversation.id"] = session
	}
	return a
}

// runStatusRoots lists the trace roots the Traces page's status chip keeps
// (summary.weft.run_status), by trace id.
func runStatusRoots(t *testing.T, s *Store, status string) []string {
	t.Helper()
	var ids []string
	err := s.QueryCalls(context.Background(), CallsQuery{ProjectID: proj, Filter: &CallsFilter{TraceRootsOnly: true},
		Query: map[string]any{"$expr": map[string]any{"$eq": []any{map[string]any{"$getField": "summary.weft.run_status"}, map[string]any{"$literal": status}}}}},
		func(c *Call) error {
			ids = append(ids, c.TraceID)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(ids)
	return ids
}

// The status filter means the same on Sessions and Traces. A trace's status
// is read off its top-level span, agent span or not: error, it failed;
// failed_step, it ended without failing but a span under it failed, or it
// is a sub-agent run linked to its caller (span link, parent call link, or
// a hand-off to its agent) that failed; running, it has not ended; ok,
// nothing failed. A session combines its traces' statuses (any error, then
// any running, then any failed step). AgentShield verdict records (engine
// evaluate traces) are not runs: they take their session's status, unknown
// for a session known only from verdicts.
func TestSessionStatusFilterMatchesTraces(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	calls := append(attnSession(0, "root-error", true, false), attnSession(1, "recovered", false, true)...)
	calls = append(calls, attnSession(2, "ok-1", false, false)...)
	calls = append(calls, attnSession(3, "both", true, true)...)
	// A delegated agent that failed under a root that did not.
	sub := attnSession(4, "sub-failed", false, false)
	p := sub[0].Start.ID
	e := "sub-agent failed"
	sub = append(sub, CompleteCall{Start: CallStart{ProjectID: proj, ID: "sub-sub-failed", TraceID: sub[0].Start.TraceID, ParentID: &p, OpName: "invoke_agent",
		StartedAt: detTS(4*time.Millisecond + 5*time.Millisecond), Attributes: map[string]any{"otel": map[string]any{"attributes": map[string]any{
			"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "helper"}}}, Inputs: map[string]any{}},
		End: &CallEnd{EndedAt: detTS(4*time.Millisecond + 50*time.Millisecond), Summary: map[string]any{}, Exception: &e}})
	calls = append(calls, sub...)
	// Running: the root has not ended (a failed step under it does not matter).
	run := attnSession(5, "running-1", false, true)
	run[0].End = nil
	calls = append(calls, run...)
	// A session known only from an engine verdict (a blocked tool call).
	calls = append(calls, verdictSpan("v-1", "e-v", "verdicts-only", "", 0))

	// The verifier's shapes.
	http := func(id, trace string, err bool) statusSpan {
		return statusSpan{id: id, trace: trace, at: 10 * time.Second, attrs: map[string]any{"http.method": "POST"}, err: err}
	}
	tool := func(id, trace, parent string) statusSpan {
		return statusSpan{id: id, trace: trace, parent: parent, at: 12 * time.Second, attrs: map[string]any{"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "Bash"}}
	}
	spans := []statusSpan{
		// A non-agent top-level span (an HTTP server span) that succeeded
		// around an agent that failed: the run went on after a failed step.
		http("w1", "t-wrap-ok", false),
		{id: "w1-a", trace: "t-wrap-ok", parent: "w1", at: 11 * time.Second, attrs: statusAgentAttrs("coder", "wrap-ok-agent-err"), err: true},
		tool("w1-x", "t-wrap-ok", "w1-a"),
		// ... and one that failed around an agent that succeeded: failed.
		http("w2", "t-wrap-err", true),
		{id: "w2-a", trace: "t-wrap-err", parent: "w2", at: 11 * time.Second, attrs: statusAgentAttrs("coder", "wrap-err-agent-ok")},
		tool("w2-x", "t-wrap-err", "w2-a"),
		// A helper that failed in its own trace with nothing linking it to
		// the caller: two separate runs, one failed.
		{id: "u-caller", trace: "t-unlinked-caller", at: 20 * time.Second, attrs: statusAgentAttrs("coder", "sub-unlinked")},
		tool("u-x", "t-unlinked-caller", "u-caller"),
		{id: "u-helper", trace: "t-unlinked-helper", at: 21 * time.Second, attrs: statusAgentAttrs("helper", "sub-unlinked"), err: true},
		// A helper in its own trace linked to its caller by an OTel span link.
		{id: "l-caller", trace: "t-link-caller", at: 30 * time.Second, attrs: statusAgentAttrs("coder", "sub-linked-span")},
		tool("l-x", "t-link-caller", "l-caller"),
		{id: "l-helper", trace: "t-link-helper", at: 31 * time.Second, attrs: statusAgentAttrs("helper", "sub-linked-span"), err: true,
			links: []map[string]any{{"trace_id": "t-link-caller", "span_id": "l-x"}}},
		// ... by a parent call link.
		{id: "p-caller", trace: "t-pc-caller", at: 40 * time.Second, attrs: statusAgentAttrs("coder", "sub-linked-parentcall")},
		{id: "p-helper", trace: "t-pc-helper", at: 41 * time.Second, err: true, attrs: map[string]any{"gen_ai.operation.name": "invoke_agent",
			"gen_ai.agent.name": "helper", "gen_ai.conversation.id": "sub-linked-parentcall", "weft.parent_call.id": "p-caller", "weft.parent_call.trace_id": "t-pc-caller"}},
		// ... by a hand-off to its agent recorded in the caller's trace.
		{id: "h-caller", trace: "t-ho-caller", at: 50 * time.Second, attrs: statusAgentAttrs("coder", "sub-linked-handoff")},
		{id: "h-x", trace: "t-ho-caller", parent: "h-caller", at: 50 * time.Second, attrs: map[string]any{"gen_ai.operation.name": "execute_tool",
			"gen_ai.tool.name": "transfer_to_helper", "weft.handoff.to_agent": "helper"}},
		{id: "h-helper", trace: "t-ho-helper", at: 51 * time.Second, attrs: statusAgentAttrs("helper", "sub-linked-handoff"), err: true},
		// The engine's evaluate traces (HTTP root + evaluate child) of
		// sessions with agent data follow their session.
		{id: "eh-1", trace: "t-eng-recovered", at: 60 * time.Second, scope: "agentshield", attrs: map[string]any{"http.method": "POST", "http.route": "/api/v1/evaluate"}},
		{id: "ev-1", trace: "t-eng-recovered", parent: "eh-1", at: 60 * time.Second, scope: "agentshield", attrs: map[string]any{"event.id": "e-r",
			"gen_ai.conversation.id": "recovered", "gen_ai.tool.name": "Bash", "agentshield.verdict.action": "block"}},
		{id: "eh-2", trace: "t-eng-both", at: 61 * time.Second, scope: "agentshield", attrs: map[string]any{"http.method": "POST"}},
		{id: "ev-2", trace: "t-eng-both", parent: "eh-2", at: 61 * time.Second, scope: "agentshield", attrs: map[string]any{"event.id": "e-b",
			"gen_ai.conversation.id": "both", "agentshield.verdict.action": "allow"}},
	}
	for _, sp := range spans {
		calls = append(calls, sp.call())
	}
	ok := verdictSpan("v-ok", "e-ok", "ok-1", "", 2*time.Second)
	ok.Start.TraceID = "t-eng-ok"
	calls = append(calls, ok)
	if _, err := s.UpsertComplete(ctx, calls); err != nil {
		t.Fatal(err)
	}

	wantSessions := map[string][]string{
		"error":                 {"both", "root-error", "sub-unlinked", "wrap-err-agent-ok"},
		SessionStatusFailedStep: {"recovered", "sub-failed", "sub-linked-handoff", "sub-linked-parentcall", "sub-linked-span", "wrap-ok-agent-err"},
		"ok":                    {"ok-1"},
		"running":               {"running-1"},
		SessionStatusUnknown:    {"verdicts-only"},
	}
	// The same chip on Traces (trace roots, summary.weft.run_status).
	wantTraces := map[string][]string{
		"error": {"t-both", "t-eng-both", "t-root-error", "t-unlinked-helper", "t-wrap-err"},
		SessionStatusFailedStep: {"t-eng-recovered", "t-ho-helper", "t-pc-helper", "t-recovered", "t-sub-failed", "t-link-helper",
			"t-wrap-ok"},
		"ok":                 {"t-eng-ok", "t-ho-caller", "t-link-caller", "t-ok-1", "t-pc-caller", "t-unlinked-caller"},
		"running":            {"t-running-1"},
		SessionStatusUnknown: {"vt-v-1"},
	}
	for status, ids := range wantSessions {
		got, err := s.MatchingSessionIDs(ctx, SessionsQuery{ProjectID: proj, Status: status})
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(got)
		if fmt.Sprint(got) != fmt.Sprint(ids) {
			t.Errorf("Sessions status=%s: %v, want %v", status, got, ids)
		}
		want := append([]string(nil), wantTraces[status]...)
		sort.Strings(want)
		if got := runStatusRoots(t, s, status); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("Traces status=%s: %v, want %v", status, got, want)
		}
	}
	// Every trace with a session is under its session's chip, except the
	// other runs of a session with several (only t-unlinked-caller here:
	// a linked helper is a step of its caller's run).
	sessionOf := map[string]string{}
	all, _, err := s.QuerySessions(ctx, SessionsQuery{ProjectID: proj, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	for _, ss := range all {
		full, err := s.GetAgentSession(ctx, proj, ss.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		for _, tid := range full.TraceIDs {
			sessionOf[tid] = ss.SessionID
		}
		if (full.FailedStep) != (full.Status == "ok" && containsStr(wantSessions[SessionStatusFailedStep], ss.SessionID)) {
			t.Errorf("session %s: status %s failed_step %v", ss.SessionID, full.Status, full.FailedStep)
		}
	}
	if ss, err := s.GetAgentSession(ctx, proj, "verdicts-only"); err != nil || ss.Status != SessionStatusUnknown || !ss.VerdictsOnly || ss.FailedStep {
		t.Errorf("verdict-only session: %+v %v", ss, err)
	}
	if _, _, err := s.QuerySessions(ctx, SessionsQuery{ProjectID: proj, Status: "bogus"}); err == nil {
		t.Error("bad status accepted")
	}
}

// A card's link opens the request's filters with the card's filter laid over
// them ({...base, ...card.filter}); when both set the same key, the card's
// value wins. The card must count exactly that list, not the intersection of
// the two values (the verifier's cases: has=approval with the security card,
// score_below with the low-score card, has_detection=block with security).
func TestSessionsAttentionCountsTheLinkedList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var calls []CompleteCall
	for i, id := range []string{"s-block", "s-heavy", "s-1appr", "s-low", "s-mid", "s-hi", "s-fail", "s-step"} {
		calls = append(calls, attnSession(i, id, id == "s-fail", id == "s-step")...)
	}
	if _, err := s.UpsertComplete(ctx, calls); err != nil {
		t.Fatal(err)
	}
	mustUpsert(t, s, det("e-b", "s-block", "Bash", "", "block", 0),
		det("e-h1", "s-heavy", "Bash", "", "require_approval", 0), det("e-h2", "s-heavy", "Bash", "", "require_approval", 0),
		det("e-h3", "s-heavy", "Bash", "", "require_approval", 0), det("e-1", "s-1appr", "Bash", "", "require_approval", 0))
	for sess, v := range map[string]float64{"s-low": 0.2, "s-mid": 0.4, "s-hi": 0.9} {
		v := v
		if _, err := s.CreateScopeScore(ctx, ScopeScoreCreate{ProjectID: proj, Scope: ScopeSession, SessionID: sess, Scorer: "goal", Value: &v}); err != nil {
			t.Fatal(err)
		}
	}
	bases := []SessionsQuery{
		{ProjectID: proj},
		{ProjectID: proj, HasDetection: DetectionRequireApproval},
		{ProjectID: proj, HasDetection: DetectionBlock},
		{ProjectID: proj, ScoreBelow: "0.3"},
		{ProjectID: proj, ScoreBelow: "goal:0.9"},
		{ProjectID: proj, Status: "error"},
		{ProjectID: proj, Status: "ok"},
	}
	for _, base := range bases {
		a, err := s.SessionsAttention(ctx, base)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range a.Items {
			if len(c.Filter) == 0 {
				continue
			}
			q, err := withCardFilter(base, c.Filter)
			if err != nil {
				t.Fatal(err)
			}
			rows, total, err := s.QuerySessions(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			if total != c.Count {
				t.Errorf("base %+v, card %s: count %d, its link lists %d", base, c.ID, c.Count, total)
			}
			if total > 0 && (c.Latest == nil || c.Latest.SessionID != rows[0].SessionID) {
				t.Errorf("base %+v, card %s: latest %+v, list starts with %s", base, c.ID, c.Latest, rows[0].SessionID)
			}
		}
	}
	// The verifier's first case, spelled out: with has=approval the security
	// card lists (and counts) every flagged session, the blocked one too.
	a, err := s.SessionsAttention(ctx, SessionsQuery{ProjectID: proj, HasDetection: DetectionRequireApproval})
	if err != nil {
		t.Fatal(err)
	}
	if a.Items[2].ID != "security" || a.Items[2].Count != 2 || a.Blocked != 1 || a.ApprovalHeavy != 1 {
		t.Errorf("security card under has=approval: %+v blocked %d approval-heavy %d", a.Items[2], a.Blocked, a.ApprovalHeavy)
	}
	// And with score_below=0.3 the low-score card is every session below 0.5.
	if a, err = s.SessionsAttention(ctx, SessionsQuery{ProjectID: proj, ScoreBelow: "0.3"}); err != nil {
		t.Fatal(err)
	}
	if a.Items[3].ID != "low_score" || a.Items[3].Count != 2 || a.LowestScore == nil || *a.LowestScore != 0.2 {
		t.Errorf("low-score card under score_below=0.3: %+v lowest %v", a.Items[3], a.LowestScore)
	}
}
