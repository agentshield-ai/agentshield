package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

var detBase = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func detTS(offset time.Duration) string { return detBase.Add(offset).Format(time.RFC3339Nano) }

// detTestSpan stores one OTLP-shaped span (attributes under otel.attributes).
type detTestSpan struct {
	id, trace, parent string
	at                time.Duration
	attrs             map[string]any
	inputs            map[string]any
}

func putSpans(t *testing.T, s *Store, spans ...detTestSpan) {
	t.Helper()
	var calls []CompleteCall
	for _, sp := range spans {
		st := CallStart{ProjectID: proj, ID: sp.id, TraceID: sp.trace, OpName: "span", StartedAt: detTS(sp.at),
			Attributes: map[string]any{"otel": map[string]any{"attributes": sp.attrs, "scope": map[string]any{"name": "test"}}}, Inputs: sp.inputs}
		if sp.parent != "" {
			st.ParentID = ptr(sp.parent)
		}
		if c, ok := sp.attrs["gen_ai.conversation.id"].(string); ok {
			st.ThreadID = ptr(c)
		}
		calls = append(calls, CompleteCall{Start: st, End: &CallEnd{EndedAt: detTS(sp.at + time.Second), Summary: map[string]any{}}})
	}
	if _, err := s.UpsertComplete(context.Background(), calls); err != nil {
		t.Fatal(err)
	}
}

func toolSpan(id, trace, parent, session, tool, toolCallID, agent string, at time.Duration) detTestSpan {
	a := map[string]any{"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": tool}
	if session != "" {
		a["gen_ai.conversation.id"] = session
	}
	if toolCallID != "" {
		a["gen_ai.tool.call.id"] = toolCallID
	}
	if agent != "" {
		a["gen_ai.agent.name"] = agent
	}
	return detTestSpan{id: id, trace: trace, parent: parent, at: at, attrs: a}
}

func agentRoot(id, trace, session, agent string, at time.Duration) detTestSpan {
	a := map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": agent}
	if session != "" {
		a["gen_ai.conversation.id"] = session
	}
	return detTestSpan{id: id, trace: trace, at: at, attrs: a}
}

func det(eventID, session, tool, toolCallID, action string, at time.Duration, rules ...DetectionRule) DetectionInput {
	return DetectionInput{EventID: eventID, SessionID: session, ToolName: tool, ToolCallID: toolCallID, Action: action, Mode: "enforce",
		Rules: rules, CreatedAt: Micros(detBase.Add(at))}
}

func mustUpsert(t *testing.T, s *Store, in ...DetectionInput) []DetectionUpsert {
	t.Helper()
	r, err := s.UpsertDetections(context.Background(), proj, in)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func getDet(t *testing.T, s *Store, eventID string) Detection {
	t.Helper()
	items, _, err := s.QueryDetections(context.Background(), DetectionQuery{ProjectID: proj, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range items {
		if d.EventID == eventID {
			return d
		}
	}
	t.Fatalf("detection %s not found", eventID)
	return Detection{}
}

func spanOf(d Detection) string {
	if d.SpanID == nil {
		return ""
	}
	return *d.SpanID
}

var critRule = DetectionRule{ID: "r-pipe", Title: "Pipe to shell", Level: "critical", Tags: []string{"attack.t1059"}}

func TestDetectionNormalizeValidation(t *testing.T) {
	bad := []DetectionInput{
		{Action: "block"},                          // no event id
		{EventID: "e", Action: "deny"},             // bad action
		{EventID: "e", Action: "block", Mode: "x"}, // bad mode
		{EventID: "e", Action: "block", Source: "elsewhere"},
		{EventID: "e", Action: "block", SessionID: strings.Repeat("s", 300)},
		{EventID: "e", Action: "block", ToolName: "a\x00b"},
		{EventID: "e", Action: "block", Rules: []DetectionRule{{Title: "no id"}}},
		{EventID: "e", Action: "block", Rules: []DetectionRule{{ID: "r", Level: "extreme"}}},
		{EventID: "e", Action: "block", Severity: "huge"},
	}
	for i, d := range bad {
		if err := d.Normalize(); err == nil {
			t.Errorf("case %d: expected a validation error for %+v", i, d)
		}
	}
	d := DetectionInput{EventID: " e1 ", Action: "BLOCK", Rules: []DetectionRule{{ID: "a", Level: "informational"}, {ID: "b", Level: "high"}, {ID: "b", Level: "high"}}}
	if err := d.Normalize(); err != nil {
		t.Fatal(err)
	}
	if d.EventID != "e1" || d.Action != "block" || d.Source != "engine" || d.Severity != "high" || len(d.Rules) != 2 || d.Rules[0].Level != "low" {
		t.Errorf("normalized = %+v", d)
	}
	none := DetectionInput{EventID: "e2", Action: "allow"}
	_ = none.Normalize()
	if none.Severity != "none" {
		t.Errorf("severity without rules = %q", none.Severity)
	}
}

func TestDetectionUpsertIsIdempotentAndMerges(t *testing.T) {
	s := newTestStore(t)
	r1 := mustUpsert(t, s, det("evt-1", "s1", "Bash", "", "block", 0, critRule))
	r2 := mustUpsert(t, s, DetectionInput{EventID: "evt-1", Action: "block", AgentName: "coder", EngineSpanID: "span-9"})
	if !r1[0].Created || r2[0].Created || r1[0].ID != r2[0].ID {
		t.Fatalf("upserts = %+v %+v", r1, r2)
	}
	d := getDet(t, s, "evt-1")
	if d.AgentName != "coder" || d.SessionID != "s1" || d.ToolName != "Bash" || len(d.Rules) != 1 || d.EngineSpanID == nil || *d.EngineSpanID != "span-9" {
		t.Errorf("merged = %+v", d)
	}
	_, total, _ := s.QueryDetections(context.Background(), DetectionQuery{ProjectID: proj})
	if total != 1 {
		t.Errorf("total = %d", total)
	}
}

func TestCorrelationByToolCallID(t *testing.T) {
	s := newTestStore(t)
	putSpans(t, s, agentRoot("root", "t1", "s1", "coder", 0),
		toolSpan("tool-a", "t1", "root", "", "Bash", "toolu_A", "", 1*time.Second),
		toolSpan("tool-b", "t1", "root", "", "Bash", "toolu_B", "", 2*time.Second))
	// Verdict time is far from the span: the tool call id still links it.
	mustUpsert(t, s, det("evt-b", "s1", "Bash", "toolu_B", "block", time.Minute, critRule))
	d := getDet(t, s, "evt-b")
	if spanOf(d) != "tool-b" || d.Link != LinkToolCallID || d.TraceID == nil || *d.TraceID != "t1" {
		t.Fatalf("linked to %q via %q", spanOf(d), d.Link)
	}
	if d.AgentName != "coder" {
		t.Errorf("agent should be filled from the tool span, got %q", d.AgentName)
	}
	// Same tool call id in a different session does not match.
	mustUpsert(t, s, det("evt-x", "other-session", "Bash", "toolu_A", "block", 0))
	if d := getDet(t, s, "evt-x"); d.SpanID != nil {
		t.Errorf("cross-session tool call id must not link, got %s", spanOf(d))
	}
}

func TestCorrelationNearestTimeFallback(t *testing.T) {
	s := newTestStore(t)
	putSpans(t, s, agentRoot("root", "t1", "s1", "coder", 0),
		toolSpan("early", "t1", "root", "", "Bash", "", "", 0),
		toolSpan("near", "t1", "root", "", "Bash", "", "", 3*time.Second),
		toolSpan("read", "t1", "root", "", "Read", "", "", 2900*time.Millisecond),
		toolSpan("far", "t1", "root", "", "Bash", "", "", 20*time.Second),
		toolSpan("other-id", "t1", "root", "", "Bash", "toolu_Z", "", 2950*time.Millisecond))
	mustUpsert(t, s, det("evt-1", "s1", "bash", "", "require_approval", 2900*time.Millisecond)) // tool name case differs
	if d := getDet(t, s, "evt-1"); spanOf(d) != "other-id" || d.Link != LinkNearestTime {
		// The nearest Bash span is other-id (2.95 s) whose tool call id is set;
		// a detection without a tool call id may take it.
		t.Fatalf("nearest: span %q link %q", spanOf(d), d.Link)
	}
	// A detection whose tool call id differs from the span's skips that span,
	// and a span already claimed is not reused.
	mustUpsert(t, s, det("evt-2", "s1", "Bash", "toolu_Q", "block", 2950*time.Millisecond))
	if d := getDet(t, s, "evt-2"); spanOf(d) != "near" {
		t.Fatalf("second detection linked to %q", spanOf(d))
	}
	// Outside the ±5 s window nothing links: session only.
	mustUpsert(t, s, det("evt-3", "s1", "Bash", "", "block", 40*time.Second))
	if d := getDet(t, s, "evt-3"); d.SpanID != nil || d.Link != "session" {
		t.Fatalf("outside window: span %q link %q", spanOf(d), d.Link)
	}
}

func TestCorrelationSessionOnlyAndNone(t *testing.T) {
	s := newTestStore(t)
	mustUpsert(t, s, det("evt-s", "lonely", "Bash", "toolu_1", "block", 0), det("evt-n", "", "Bash", "", "log", 0))
	if d := getDet(t, s, "evt-s"); d.SpanID != nil || d.Link != "session" {
		t.Errorf("session-only: %+v", d)
	}
	if d := getDet(t, s, "evt-n"); d.Link != "none" {
		t.Errorf("no session: link %q", d.Link)
	}
}

func TestCorrelationLateSpan(t *testing.T) {
	s := newTestStore(t)
	// Detections arrive first.
	mustUpsert(t, s,
		det("evt-tc", "s1", "Bash", "toolu_late", "block", 10*time.Second, critRule),
		det("evt-time", "s1", "Read", "", "require_approval", 1*time.Second))
	if d := getDet(t, s, "evt-tc"); d.SpanID != nil {
		t.Fatal("should be unlinked before the span arrives")
	}
	// The tool span arrives with its session: linked by tool call id.
	putSpans(t, s, agentRoot("root", "t1", "s1", "coder", 0), toolSpan("bash-1", "t1", "root", "", "Bash", "toolu_late", "", 9*time.Second))
	if d := getDet(t, s, "evt-tc"); spanOf(d) != "bash-1" || d.Link != LinkToolCallID || d.AgentName != "coder" {
		t.Fatalf("late span: %q %q %q", spanOf(d), d.Link, d.AgentName)
	}
	// Children first: a Read span arrives before its parent, so it has no
	// session yet; the parent's arrival gives it one and links the detection.
	putSpans(t, s, toolSpan("read-1", "t2", "root2", "", "Read", "", "", 1500*time.Millisecond))
	if d := getDet(t, s, "evt-time"); d.SpanID != nil {
		t.Fatal("no session yet: must stay unlinked")
	}
	putSpans(t, s, agentRoot("root2", "t2", "s1", "reviewer", 0))
	if d := getDet(t, s, "evt-time"); spanOf(d) != "read-1" || d.Link != LinkNearestTime || d.AgentName != "reviewer" {
		t.Fatalf("after parent arrived: %q %q %q", spanOf(d), d.Link, d.AgentName)
	}
}

// Spans of a trace arrive in one batch in any order: a farther span that
// arrives first must give the detection up when a nearer one arrives.
func TestCorrelationLateSpansNearestWins(t *testing.T) {
	s := newTestStore(t)
	mustUpsert(t, s, det("evt-cat", "s1", "Bash", "", "block", 3*time.Second))
	putSpans(t, s, agentRoot("root", "t1", "s1", "coder", 0),
		toolSpan("ls", "t1", "root", "", "Bash", "toolu_ls", "", 1*time.Second),
		toolSpan("cat", "t1", "root", "", "Bash", "", "", 3020*time.Millisecond))
	if d := getDet(t, s, "evt-cat"); spanOf(d) != "cat" || d.Link != LinkNearestTime {
		t.Fatalf("linked to %q via %q, want the nearer span", spanOf(d), d.Link)
	}
	// The span a detection gave up is offered to other waiting detections.
	mustUpsert(t, s, det("evt-ls-late", "s1", "Bash", "", "log", 900*time.Millisecond))
	if d := getDet(t, s, "evt-ls-late"); spanOf(d) != "ls" {
		t.Fatalf("second detection linked to %q", spanOf(d))
	}
}

func TestCorrelationUpgradesTimeLinkToToolCallID(t *testing.T) {
	s := newTestStore(t)
	putSpans(t, s, agentRoot("root", "t1", "s1", "coder", 0), toolSpan("guess", "t1", "root", "", "Bash", "", "", 1*time.Second))
	mustUpsert(t, s, det("evt-1", "s1", "Bash", "toolu_real", "block", 1100*time.Millisecond))
	if d := getDet(t, s, "evt-1"); spanOf(d) != "guess" || d.Link != LinkNearestTime {
		t.Fatalf("initial link: %q %q", spanOf(d), d.Link)
	}
	putSpans(t, s, toolSpan("real", "t1", "root", "", "Bash", "toolu_real", "", 1200*time.Millisecond))
	if d := getDet(t, s, "evt-1"); spanOf(d) != "real" || d.Link != LinkToolCallID {
		t.Fatalf("after exact span: %q %q", spanOf(d), d.Link)
	}
}

func TestDetectionFromAgentShieldSpan(t *testing.T) {
	s := newTestStore(t)
	putSpans(t, s, agentRoot("root", "t1", "s1", "coder", 0), toolSpan("tool-1", "t1", "root", "", "Bash", "toolu_1", "", time.Second))
	engineSpan := CompleteCall{Start: CallStart{ProjectID: proj, ID: "eval-span", TraceID: "engine-trace", OpName: "evaluate",
		StartedAt: detTS(1100 * time.Millisecond), Attributes: map[string]any{"otel": map[string]any{
			"scope": map[string]any{"name": "agentshield"},
			"attributes": map[string]any{
				"event.id": "evt-otel", "gen_ai.conversation.id": "s1", "gen_ai.tool.name": "Bash", "gen_ai.tool.call.id": "toolu_1",
				"agentshield.verdict.action": "block", "agentshield.verdict.mode": "enforce", "agentshield.verdict.severity": "critical",
				"agentshield.rule_ids": []any{"r-pipe"}, "source": "claude-code",
			},
			"events": []any{map[string]any{"name": "agentshield.rule.matched", "attributes": map[string]any{
				"rule.id": "r-pipe", "rule.title": "Pipe to shell", "rule.level": "critical", "rule.tags": []any{"attack.t1059"}}}},
		}}}, End: &CallEnd{EndedAt: detTS(1200 * time.Millisecond), Summary: map[string]any{}}}
	if _, err := s.UpsertComplete(context.Background(), []CompleteCall{engineSpan}); err != nil {
		t.Fatal(err)
	}
	d := getDet(t, s, "evt-otel")
	if d.Action != "block" || d.Mode != "enforce" || d.Severity != "critical" || d.EventSource != "claude-code" ||
		len(d.Rules) != 1 || d.Rules[0].Title != "Pipe to shell" || d.Rules[0].Tags[0] != "attack.t1059" {
		t.Errorf("otel detection = %+v", d)
	}
	if spanOf(d) != "tool-1" || d.Link != LinkToolCallID || d.EngineSpanID == nil || *d.EngineSpanID != "eval-span" {
		t.Errorf("link %q via %q engine span %v", spanOf(d), d.Link, d.EngineSpanID)
	}
	// The engine span is still an ordinary call.
	if c, err := s.GetCall(context.Background(), proj, "eval-span", false, false, false); err != nil || c.OpName != "evaluate" {
		t.Errorf("engine span not stored as a call: %v", err)
	}
	// The same verdict pushed afterwards merges into the same row.
	r := mustUpsert(t, s, DetectionInput{EventID: "evt-otel", Action: "block", AgentName: "coder", SessionID: "s1"})
	if r[0].Created || r[0].ID != d.ID {
		t.Errorf("push after OTLP created a second detection: %+v", r)
	}
	// Legacy engines (no agentshield.* attributes) still produce detections.
	legacy := CompleteCall{Start: CallStart{ProjectID: proj, ID: "legacy", TraceID: "lt", OpName: "evaluate", StartedAt: detTS(0),
		Attributes: map[string]any{"otel": map[string]any{"scope": map[string]any{"name": "agentshield"}, "attributes": map[string]any{
			"event.id": "evt-legacy", "session.id": "s1", "tool.name": "Read", "verdict.action": "log", "verdict.mode": "audit"},
			"events": []any{map[string]any{"name": "rule.matched", "attributes": map[string]any{"rule.id": "r2", "rule.name": "R2", "rule.severity": "medium"}}}}}}}
	if _, err := s.UpsertComplete(context.Background(), []CompleteCall{legacy}); err != nil {
		t.Fatal(err)
	}
	if d := getDet(t, s, "evt-legacy"); d.Action != "log" || d.Severity != "medium" || d.ToolName != "Read" {
		t.Errorf("legacy = %+v", d)
	}
	// Spans from other scopes without a verdict are not detections.
	_, total, _ := s.QueryDetections(context.Background(), DetectionQuery{ProjectID: proj})
	if total != 2 {
		t.Errorf("total detections = %d", total)
	}
}

func TestDetectionOverrides(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	mustUpsert(t, s, det("evt-1", "s1", "Bash", "", "block", 0), det("evt-2", "s1", "Bash", "", "require_approval", time.Second))
	res, err := s.ApplyDetectionOverrides(ctx, proj, []DetectionOverride{{EventID: "evt-1"}, {SessionID: "s1"}, {EventID: "evt-later", SessionID: "s1"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied != 2 || res.Pending != 1 {
		t.Fatalf("result = %+v", res)
	}
	if !getDet(t, s, "evt-1").Overridden || !getDet(t, s, "evt-2").Overridden {
		t.Error("both detections should be overridden")
	}
	mustUpsert(t, s, det("evt-later", "s1", "Bash", "", "block", 2*time.Second))
	if d := getDet(t, s, "evt-later"); !d.Overridden || d.OverriddenAt == nil {
		t.Error("pending override should apply when the detection arrives")
	}
	// Overridden sticks across re-sends.
	mustUpsert(t, s, det("evt-1", "s1", "Bash", "", "block", 0))
	if !getDet(t, s, "evt-1").Overridden {
		t.Error("re-sent detection lost its override")
	}
	if _, err := s.ApplyDetectionOverrides(ctx, proj, []DetectionOverride{{}}); err == nil {
		t.Error("an override without ids should be rejected")
	}
}

func TestDetectionQueriesAndStats(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	putSpans(t, s, agentRoot("root", "t1", "s1", "coder", 0),
		toolSpan("a", "t1", "root", "", "Bash", "tc1", "", 0), toolSpan("b", "t1", "root", "", "Bash", "tc2", "", time.Second),
		toolSpan("c", "t1", "root", "", "Read", "tc3", "", 2*time.Second), toolSpan("d", "t1", "root", "", "Read", "tc4", "", 3*time.Second))
	med := DetectionRule{ID: "r-med", Title: "Medium", Level: "medium"}
	d1 := det("e1", "s1", "Bash", "tc1", "block", 0, critRule, med)
	d1.AgentName = "coder"
	d2 := det("e2", "s1", "Read", "tc3", "require_approval", 2*time.Second, med)
	d3 := det("e3", "s2", "Bash", "", "allow", 25*time.Hour)
	d3.AgentName = "helper"
	mustUpsert(t, s, d1, d2, d3)
	_, _ = s.ApplyDetectionOverrides(ctx, proj, []DetectionOverride{{EventID: "e2"}})

	check := func(q DetectionQuery, want ...string) {
		t.Helper()
		q.ProjectID = proj
		items, total, err := s.QueryDetections(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, d := range items {
			got = append(got, d.EventID)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") || total != len(want) {
			t.Errorf("query %+v = %v (total %d), want %v", q, got, total, want)
		}
	}
	tr, fa := true, false
	check(DetectionQuery{}, "e3", "e2", "e1")
	check(DetectionQuery{SortBy: "-created_at"}, "e1", "e2", "e3")
	check(DetectionQuery{SortBy: "severity"}, "e1", "e2", "e3")
	check(DetectionQuery{SessionID: "s1"}, "e2", "e1")
	check(DetectionQuery{Agent: "coder"}, "e2", "e1") // e2's agent comes from its linked span
	check(DetectionQuery{Actions: []string{"block", "require_approval"}}, "e2", "e1")
	check(DetectionQuery{RuleID: "r-med"}, "e2", "e1")
	check(DetectionQuery{RuleID: "r-pipe"}, "e1")
	check(DetectionQuery{Source: "replay"})
	check(DetectionQuery{Overridden: &tr}, "e2")
	check(DetectionQuery{Linked: &fa}, "e3")
	check(DetectionQuery{ToolCallID: "tc3"}, "e2")
	check(DetectionQuery{SpanID: "a"}, "e1")
	check(DetectionQuery{From: Micros(detBase.Add(time.Second)), To: Micros(detBase.Add(time.Hour))}, "e2")
	if items, total, _ := s.QueryDetections(ctx, DetectionQuery{ProjectID: proj, Limit: 1, Offset: 1}); total != 3 || len(items) != 1 || items[0].EventID != "e2" {
		t.Errorf("paged = %v total %d", items, total)
	}
	if _, _, err := s.QueryDetections(ctx, DetectionQuery{ProjectID: proj, SortBy: "bogus"}); err == nil {
		t.Error("bad sort_by should fail")
	}

	st, err := s.DetectionStatsFor(ctx, DetectionQuery{ProjectID: proj})
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 3 || st.ByAction["block"] != 1 || st.ByAction["require_approval"] != 1 || st.ByAction["allow"] != 1 ||
		st.BySeverity["critical"] != 1 || st.BySeverity["none"] != 1 || st.Overridden != 1 || st.Linked != 2 || st.BySource["engine"] != 3 {
		t.Errorf("stats = %+v", st)
	}
	if len(st.ByRule) != 2 || st.ByRule[0].RuleID != "r-med" || st.ByRule[0].Count != 2 || st.ByRule[0].ByAction["block"] != 1 {
		t.Errorf("by_rule = %+v", st.ByRule)
	}
	if len(st.ByDay) != 2 || st.ByDay[0].Day != "2026-03-01" || st.ByDay[0].Count != 2 || st.ByDay[1].Day != "2026-03-02" {
		t.Errorf("by_day = %+v", st.ByDay)
	}
	var coder *AgentCount
	for i := range st.ByAgent {
		if st.ByAgent[i].Agent == "coder" {
			coder = &st.ByAgent[i]
		}
	}
	if coder == nil || coder.Count != 2 || coder.ToolCalls != 4 || coder.Rate == nil || *coder.Rate != 0.5 {
		t.Errorf("coder = %+v", coder)
	}

	counts, err := s.DetectionCountsBySession(ctx, proj, []string{"s1", "s2", "s3"})
	if err != nil {
		t.Fatal(err)
	}
	if counts["s1"]["block"] != 1 || counts["s1"]["require_approval"] != 1 || counts["s2"]["allow"] != 1 || counts["s3"] != nil {
		t.Errorf("counts = %v", counts)
	}
	all, _ := s.DetectionCountsBySession(ctx, proj, nil)
	if len(all) != 2 {
		t.Errorf("all sessions = %v", all)
	}
	byAgent, err := s.DetectionCountsByAgent(ctx, proj, []string{"coder", "helper", "ghost"})
	if err != nil || byAgent["coder"]["block"] != 1 || byAgent["coder"]["require_approval"] != 1 || byAgent["helper"]["allow"] != 1 || byAgent["ghost"] != nil {
		t.Errorf("by agent = %v %v", byAgent, err)
	}
	inSession, err := s.DetectionCountsBySessionAgent(ctx, proj, "s1")
	if err != nil || len(inSession) != 1 || inSession["coder"]["block"] != 1 {
		t.Errorf("by session agent = %v %v", inSession, err)
	}
}

func TestShieldInsights(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	// Session s1: a planner+coder team; 6 approvals for coder (fatigue), 3 overrides.
	putSpans(t, s, agentRoot("p", "t1", "s1", "planner", 0), agentRoot("c", "t1", "s1", "coder", time.Second))
	var in []DetectionInput
	for i := 0; i < 6; i++ {
		d := det("appr-"+itoa(i), "s1", "Bash", "", "require_approval", time.Duration(i)*time.Minute, DetectionRule{ID: "r-curl", Level: "medium"})
		d.AgentName = "coder"
		in = append(in, d)
	}
	p := det("plan-1", "s1", "WebFetch", "", "block", 2*time.Minute, DetectionRule{ID: "r-curl", Level: "high"})
	p.AgentName = "planner"
	b := det("blk-1", "s1", "Bash", "", "block", 3*time.Minute, critRule)
	b.AgentName = "coder"
	in = append(in, p, b)
	// Session s2: single agent, one block.
	solo := det("solo-1", "s2", "Bash", "", "block", 0, critRule)
	solo.AgentName = "loner"
	in = append(in, solo)
	mustUpsert(t, s, in...)
	if _, err := s.ApplyDetectionOverrides(ctx, proj, []DetectionOverride{{EventID: "appr-0"}, {EventID: "appr-1"}, {EventID: "blk-1"}, {EventID: "plan-1"}}); err != nil {
		t.Fatal(err)
	}
	ins, err := s.ShieldInsights(ctx, InsightsQuery{ProjectID: proj, Bucket: "hour"})
	if err != nil {
		t.Fatal(err)
	}
	f := ins.ApprovalFatigue
	if len(f.BySession) != 1 || f.BySession[0].SessionID != "s1" || f.BySession[0].RequireApproval != 6 || f.BySession[0].Approved != 2 || !f.BySession[0].Fatigued {
		t.Errorf("fatigue by session = %+v", f.BySession)
	}
	if len(f.ByAgent) != 1 || f.ByAgent[0].Agent != "coder" || f.ByAgent[0].Sessions != 1 {
		t.Errorf("fatigue by agent = %+v", f.ByAgent)
	}
	if len(f.Series) != 1 || f.Series[0].Bucket != "2026-03-01T12:00:00Z" || f.Series[0].RequireApproval != 6 {
		t.Errorf("fatigue series = %+v", f.Series)
	}
	e := ins.OverrideEscalation
	if len(e.BySession) != 1 || e.BySession[0].Overrides != 4 || e.BySession[0].OverriddenBlocks != 2 || !e.BySession[0].Escalating {
		t.Errorf("escalation = %+v", e.BySession)
	}
	if len(ins.CrossAgent) != 1 {
		t.Fatalf("cross agent = %+v", ins.CrossAgent)
	}
	team := ins.CrossAgent[0]
	if team.TeamID != "auto:coder+planner" || !team.Correlated || len(team.SharedRules) != 1 || team.SharedRules[0].RuleID != "r-curl" ||
		len(team.Incidents) != 1 || strings.Join(team.Incidents[0].Agents, ",") != "coder,planner" || team.Incidents[0].MaxAction != "block" {
		t.Errorf("team = %+v", team)
	}
	// weft.team.id on a span names the team.
	putSpans(t, s, detTestSpan{id: "teamspan", trace: "t1", parent: "p", at: time.Second, attrs: map[string]any{"weft.team.id": "red-team", "gen_ai.conversation.id": "s1"}})
	ins, _ = s.ShieldInsights(ctx, InsightsQuery{ProjectID: proj})
	if ins.CrossAgent[0].TeamID != "red-team" {
		t.Errorf("team id = %s", ins.CrossAgent[0].TeamID)
	}
	if _, err := s.ShieldInsights(ctx, InsightsQuery{ProjectID: proj, Bucket: "week"}); err == nil {
		t.Error("bad bucket should fail")
	}
}

// A sub-agent launched by a tool (a coding agent's Task tool) is recorded as
// invoke_agent with the tool-use id. Verdicts on that tool call link to it by
// id, before or after it arrives, and never by time.
func TestCorrelationSubAgentToolCall(t *testing.T) {
	s := newTestStore(t)
	sub := agentRoot("task-1", "t1", "s1", "researcher", 2*time.Second)
	sub.parent = "root"
	sub.attrs["gen_ai.tool.call.id"] = "toolu_task"
	sub.attrs["gen_ai.tool.name"] = "Task"
	putSpans(t, s, agentRoot("root", "t1", "s1", "coder", 0), sub)
	mustUpsert(t, s, det("evt-task", "s1", "Task", "toolu_task", "require_approval", time.Minute))
	if d := getDet(t, s, "evt-task"); spanOf(d) != "task-1" || d.Link != LinkToolCallID {
		t.Fatalf("detection first: %q %q", spanOf(d), d.Link)
	}
	// Detection first, sub-agent span later.
	mustUpsert(t, s, det("evt-task2", "s1", "Task", "toolu_task2", "block", 3*time.Second))
	sub2 := agentRoot("task-2", "t1", "s1", "writer", 3*time.Second)
	sub2.parent = "root"
	sub2.attrs["gen_ai.tool.call.id"] = "toolu_task2"
	putSpans(t, s, sub2)
	if d := getDet(t, s, "evt-task2"); spanOf(d) != "task-2" || d.Link != LinkToolCallID {
		t.Fatalf("late sub-agent: %q %q", spanOf(d), d.Link)
	}
	// An invoke_agent span without a tool call id never takes a time-based link.
	mustUpsert(t, s, det("evt-near", "s1", "Task", "", "block", 2*time.Second))
	if d := getDet(t, s, "evt-near"); d.SpanID != nil && (spanOf(d) == "root" || spanOf(d) == "task-1") {
		t.Fatalf("time link to an agent span: %q", spanOf(d))
	}
}
