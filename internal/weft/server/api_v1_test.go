package server

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// v1Span is an OTLP span with events, links and an optional open end.
type v1Span struct {
	trace, id, parent, name string
	startMS, durMS          int
	attrs                   map[string]any
	err                     string
	open                    bool
	events                  []v1Event
	links                   [][2]string // trace, span
}

type v1Event struct {
	name  string
	atMS  int
	attrs map[string]any
}

func v1OTLP(base time.Time, spans []v1Span) []byte {
	var out []any
	ts := func(ms int) string {
		return strconv.FormatInt(base.Add(time.Duration(ms)*time.Millisecond).UnixNano(), 10)
	}
	for _, s := range spans {
		var attrs []any
		for k, v := range s.attrs {
			attrs = append(attrs, otlpAttr(k, v))
		}
		sp := map[string]any{"traceId": s.trace, "spanId": s.id, "name": s.name, "kind": 1, "attributes": attrs,
			"startTimeUnixNano": ts(s.startMS)}
		if !s.open {
			sp["endTimeUnixNano"] = ts(s.startMS + s.durMS)
		}
		if s.parent != "" {
			sp["parentSpanId"] = s.parent
		}
		if s.err != "" {
			sp["status"] = map[string]any{"code": 2, "message": s.err}
		}
		var evs []any
		for _, ev := range s.events {
			var ea []any
			for k, v := range ev.attrs {
				ea = append(ea, otlpAttr(k, v))
			}
			evs = append(evs, map[string]any{"name": ev.name, "timeUnixNano": ts(ev.atMS), "attributes": ea})
		}
		if evs != nil {
			sp["events"] = evs
		}
		var links []any
		for _, l := range s.links {
			links = append(links, map[string]any{"traceId": l[0], "spanId": l[1]})
		}
		if links != nil {
			sp["links"] = links
		}
		out = append(out, sp)
	}
	b, _ := json.Marshal(map[string]any{"resourceSpans": []any{map[string]any{
		"resource":   map[string]any{"attributes": []any{otlpAttr("service.name", "team-svc")}},
		"scopeSpans": []any{map[string]any{"scope": map[string]any{"name": "test"}, "spans": out}}}}})
	return b
}

func (e *testEnv) v1Send(project string, base time.Time, spans []v1Span) {
	e.t.Helper()
	if code, out := e.raw("/otel/v1/traces", v1OTLP(base, spans), map[string]string{"project_id": project}); code != 200 {
		e.t.Fatalf("otlp: %d %s", code, out)
	}
}

func (e *testEnv) v1Get(path string) map[string]any {
	e.t.Helper()
	code, out := e.raw(path, nil, nil)
	if code != 200 {
		e.t.Fatalf("GET %s -> %d: %s", path, code, out)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		e.t.Fatalf("GET %s: bad JSON %s", path, out)
	}
	return m
}

func v1Items(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	l, ok := m["items"].([]any)
	if !ok {
		t.Fatalf("no items in %v", m)
	}
	out := make([]map[string]any, len(l))
	for i, x := range l {
		out[i] = x.(map[string]any)
	}
	return out
}

const (
	thinkDelegate = "The user wants facts about Oslo; the researcher agent should gather them."
	thinkSearch   = "I should search the web for Oslo facts."
	thinkSummary  = "The researcher returned three facts; I will summarise them."
)

// twoAgentSession is an orchestrator that delegates to a researcher through a
// tool call, with Anthropic thinking blocks on the orchestrator's model calls
// and semconv reasoning parts on the researcher's.
func twoAgentSession(trace, session string) (children, parents []v1Span, ids map[string]string) {
	ids = map[string]string{}
	for _, k := range []string{"O", "C1", "X1", "R", "C2", "X2", "C3", "C4"} {
		ids[k] = hexID(session+k, 16)
	}
	userMsg := "Find three facts about Oslo and summarise them"
	anthropic1 := map[string]any{"type": "message", "role": "assistant", "model": "claude-sonnet-4", "content": []any{
		map[string]any{"type": "thinking", "thinking": thinkDelegate, "signature": "sig1"},
		map[string]any{"type": "tool_use", "id": "toolu_1", "name": "delegate_research", "input": map[string]any{"task": "Find three facts about Oslo"}}},
		"stop_reason": "tool_use"}
	anthropic2 := map[string]any{"type": "message", "role": "assistant", "model": "claude-sonnet-4", "content": []any{
		map[string]any{"type": "thinking", "thinking": thinkSummary, "signature": "sig2"},
		map[string]any{"type": "text", "text": "Oslo is Norway's capital, sits on a fjord, and hosts the Nobel Peace Prize."}}, "stop_reason": "end_turn"}
	history := map[string]any{"messages": []any{map[string]any{"role": "user", "content": userMsg}, anthropic1,
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "3 facts"}}}}}
	children = []v1Span{
		{trace, ids["C2"], ids["R"], "chat gpt-4o", 1100, 300, map[string]any{"gen_ai.operation.name": "chat", "gen_ai.request.model": "gpt-4o",
			"gen_ai.provider.name": "openai", "gen_ai.input.messages": genaiMsgs("user", "Find three facts about Oslo"),
			"gen_ai.output.messages":    `[{"role":"assistant","parts":[{"type":"reasoning","content":"` + thinkSearch + `"},{"type":"tool_call","id":"call_ws","name":"web_search","arguments":{"q":"Oslo facts"}}]}]`,
			"gen_ai.usage.input_tokens": 80, "gen_ai.usage.output_tokens": 70, "gen_ai.usage.reasoning.output_tokens": 50}, "", false, nil, nil},
		{trace, ids["X2"], ids["R"], "execute_tool web_search", 1450, 400, map[string]any{"gen_ai.operation.name": "execute_tool",
			"gen_ai.tool.name": "web_search", "gen_ai.tool.call.id": "call_ws", "gen_ai.tool.call.arguments": `{"q":"Oslo facts"}`,
			"gen_ai.tool.call.result": `"Oslo is the capital; it lies on the Oslofjord; the Nobel Peace Prize is awarded there."`}, "", false, nil, nil},
		{trace, ids["C3"], ids["R"], "chat gpt-4o", 1900, 300, map[string]any{"gen_ai.operation.name": "chat", "gen_ai.request.model": "gpt-4o",
			"gen_ai.output.messages":    genaiMsgs("assistant", "1. Capital of Norway 2. On the Oslofjord 3. Nobel Peace Prize"),
			"gen_ai.usage.input_tokens": 150, "gen_ai.usage.output_tokens": 30}, "", false, nil, nil},
		{trace, ids["R"], ids["X1"], "invoke_agent researcher", 1000, 1300, map[string]any{"gen_ai.operation.name": "invoke_agent",
			"gen_ai.agent.name": "researcher", "gen_ai.input.messages": genaiMsgs("user", "Find three facts about Oslo"),
			"gen_ai.output.messages": genaiMsgs("assistant", "1. Capital of Norway 2. On the Oslofjord 3. Nobel Peace Prize")}, "", false, nil, nil},
	}
	parents = []v1Span{
		{trace, ids["O"], "", "invoke_agent orchestrator", 0, 4000, map[string]any{"gen_ai.operation.name": "invoke_agent",
			"gen_ai.agent.name": "orchestrator", "gen_ai.conversation.id": session, "gen_ai.input.messages": genaiMsgs("user", userMsg),
			"gen_ai.output.messages": genaiMsgs("assistant", "Oslo is Norway's capital, sits on a fjord, and hosts the Nobel Peace Prize.")}, "", false, nil, nil},
		{trace, ids["C1"], ids["O"], "chat claude-sonnet-4", 50, 800, map[string]any{"gen_ai.operation.name": "chat",
			"gen_ai.provider.name": "anthropic", "gen_ai.request.model": "claude-sonnet-4",
			"weft.inputs": map[string]any{"messages": []any{map[string]any{"role": "user", "content": userMsg}}}, "weft.output": anthropic1,
			"gen_ai.usage.input_tokens": 200, "gen_ai.usage.output_tokens": 120}, "", false, nil, nil},
		{trace, ids["X1"], ids["O"], "execute_tool delegate_research", 900, 1500, map[string]any{"gen_ai.operation.name": "execute_tool",
			"gen_ai.tool.name": "delegate_research", "gen_ai.tool.call.id": "toolu_1", "gen_ai.tool.call.arguments": `{"task":"Find three facts about Oslo"}`}, "", false, nil, nil},
		{trace, ids["C4"], ids["O"], "chat claude-sonnet-4", 2500, 1000, map[string]any{"gen_ai.operation.name": "chat",
			"gen_ai.provider.name": "anthropic", "gen_ai.request.model": "claude-sonnet-4", "weft.inputs": history, "weft.output": anthropic2,
			"gen_ai.usage.input_tokens": 400, "gen_ai.usage.output_tokens": 90}, "", false, nil, nil},
	}
	return children, parents, ids
}

func v1Num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func v1Strs(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, x := range l {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

// TestAPIv1EndToEnd sends a two-agent session over OTLP (children first, in
// two requests) and checks every view of it, then scores its hand-off.
func TestAPIv1EndToEnd(t *testing.T) {
	e := newEnv(t)
	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	trace := hexID("e2etrace", 32)
	children, parents, ids := twoAgentSession(trace, "e2e-1")
	e.v1Send(pid, base, children)
	e.v1Send(pid, base, parents)
	P := "project_id=" + url.QueryEscape(pid)

	// Session summary.
	ss := v1Items(t, e.v1Get("/api/v1/sessions?"+P))
	if len(ss) != 1 {
		t.Fatalf("sessions: %v", ss)
	}
	s := ss[0]
	if s["session_id"] != "e2e-1" || strings.Join(v1Strs(s["agents"]), ",") != "orchestrator,researcher" ||
		s["team_id"] != "auto:orchestrator+researcher" || s["team_name"] != "orchestrator + researcher" ||
		v1Num(s["handoff_count"]) != 1 || v1Num(s["reasoning_step_count"]) != 3 || v1Num(s["tool_call_count"]) != 2 ||
		v1Num(s["llm_call_count"]) != 4 || v1Num(s["span_count"]) != 8 || v1Num(s["turn_count"]) != 1 || v1Num(s["error_count"]) != 0 ||
		v1Num(s["input_tokens"]) != 830 || v1Num(s["output_tokens"]) != 310 || v1Num(s["reasoning_tokens"]) != 50 ||
		s["status"] != "ok" || s["title"] != "Find three facts about Oslo and summarise them" || v1Num(s["duration_ms"]) != 4000 {
		b, _ := json.MarshalIndent(s, "", " ")
		t.Fatalf("summary: %s", b)
	}
	if d, ok := s["detection_count"].(map[string]any); !ok || len(d) != 0 {
		t.Fatalf("detection_count: %v", s["detection_count"])
	}
	one := e.v1Get("/api/v1/sessions/e2e-1?" + P)
	if one["session_id"] != "e2e-1" || len(v1Strs(one["trace_ids"])) != 1 || v1Strs(one["tools"])[0] != "delegate_research" {
		t.Fatalf("session: %v", one)
	}

	// Timeline: every item type, ordered, reasoning tied to the action it led to.
	tl := e.v1Get("/api/v1/sessions/e2e-1/timeline?" + P)
	types := map[string]int{}
	var prev string
	for _, it := range v1Items(t, tl) {
		types[it["type"].(string)]++
		if at, _ := it["started_at"].(string); at < prev {
			t.Fatalf("timeline out of order at %v", it)
		} else {
			prev = at
		}
		p := it["payload"].(map[string]any)
		if it["type"] == "reasoning" && p["text"] == thinkDelegate {
			if p["next_span_id"] != ids["X1"] || it["agent_name"] != "orchestrator" || it["span_id"] != ids["C1"] {
				t.Fatalf("reasoning item: %v", it)
			}
		}
		if it["type"] == "handoff" && p["kind"] == "delegate" {
			if p["from_agent"] != "orchestrator" || p["to_agent"] != "researcher" || it["span_id"] != ids["R"] ||
				p["input_preview"] != "Find three facts about Oslo" || p["status"] != "ok" {
				t.Fatalf("handoff item: %v", it)
			}
		}
	}
	want := map[string]int{"turn": 1, "message": 2, "llm": 4, "reasoning": 3, "tool_call": 2, "handoff": 2}
	for k, n := range want {
		if types[k] != n {
			t.Fatalf("timeline types %v, want %v", types, want)
		}
	}
	if f := e.v1Get("/api/v1/sessions/e2e-1/timeline?" + P + "&types=reasoning,handoff&agent=orchestrator"); v1Num(f["total_count"]) != 3 {
		t.Fatalf("filtered timeline: %v", f["total_count"])
	}

	// Graph.
	g := e.v1Get("/api/v1/sessions/e2e-1/graph?" + P)
	nodes := g["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("nodes %v", nodes)
	}
	for _, n := range nodes {
		nm := n.(map[string]any)
		if v1Num(nm["calls"]) != 1 || v1Num(nm["tokens"]) == 0 {
			t.Fatalf("node %v", nm)
		}
	}
	edges := g["edges"].([]any)
	if len(edges) != 2 {
		t.Fatalf("edges %v", edges)
	}
	d := edges[0].(map[string]any)
	if d["from"] != "orchestrator" || d["to"] != "researcher" || d["kind"] != "delegate" || v1Num(d["count"]) != 1 || v1Num(d["p50_ms"]) != 1300 {
		t.Fatalf("edge %v", d)
	}

	// Reasoning, with next_span_id.
	rs := v1Items(t, e.v1Get("/api/v1/sessions/e2e-1/reasoning?"+P))
	var got []string
	for _, r := range rs {
		got = append(got, fmt.Sprintf("%s|%s|%v|%v", r["agent_name"], r["kind"], r["next_span_id"], r["text"]))
	}
	wantR := []string{
		"orchestrator|thinking|" + ids["X1"] + "|" + thinkDelegate,
		"researcher|thinking|" + ids["X2"] + "|" + thinkSearch,
		"orchestrator|thinking|<nil>|" + thinkSummary,
	}
	if strings.Join(got, "\n") != strings.Join(wantR, "\n") {
		t.Fatalf("reasoning:\n%s", strings.Join(got, "\n"))
	}
	if v1Num(rs[1]["tokens"]) != 50 {
		t.Fatalf("reasoning tokens: %v", rs[1]["tokens"])
	}

	// Team.
	teams := v1Items(t, e.v1Get("/api/v1/teams?"+P))
	if len(teams) != 1 || teams[0]["team_id"] != "auto:orchestrator+researcher" || v1Num(teams[0]["session_count"]) != 1 || teams[0]["auto"] != true {
		t.Fatalf("teams %v", teams)
	}

	// Hand-offs and agents.
	hs := v1Items(t, e.v1Get("/api/v1/handoffs?"+P+"&session_id=e2e-1&kind=delegate"))
	if len(hs) != 1 || hs[0]["team_id"] != "auto:orchestrator+researcher" {
		t.Fatalf("handoffs %v", hs)
	}
	hid := hs[0]["id"].(string)
	ag := map[string]map[string]any{}
	for _, a := range v1Items(t, e.v1Get("/api/v1/agents?"+P)) {
		ag[a["agent_name"].(string)] = a
	}
	if v1Num(ag["orchestrator"]["handoffs_out"]) != 1 || v1Num(ag["researcher"]["handoffs_in"]) != 1 || v1Num(ag["researcher"]["reasoning_tokens"]) != 50 ||
		v1Num(ag["orchestrator"]["reasoning_step_count"]) != 2 {
		t.Fatalf("agents %v", ag)
	}
	det := e.v1Get("/api/v1/agents/researcher?" + P)
	if len(det["tool_counts"].([]any)) != 1 || det["handoffs_from"].([]any)[0].(map[string]any)["agent"] != "orchestrator" ||
		det["recent_sessions"].([]any)[0].(map[string]any)["session_id"] != "e2e-1" {
		t.Fatalf("agent detail %v", det)
	}

	// Score the hand-off, the session and each agent with the offline judge.
	run := e.post("/api/v1/scores/run", map[string]any{"project_id": pid, "scope": "handoff", "scorers": []any{"HandoffQualityJudge"},
		"handoff_ids": []any{hid}, "model": "mock/json"})
	res := v1Items(t, run)
	if len(res) != 1 || res[0]["error"] != nil || res[0]["value"] == nil || res[0]["feedback_id"] == nil || res[0]["handoff_id"] != hid {
		t.Fatalf("run: %v", run)
	}
	out := res[0]["output"].(map[string]any)
	if _, ok := out["appropriate"].(bool); !ok {
		t.Fatalf("judge output %v", out)
	}
	e.post("/api/v1/scores/run", map[string]any{"project_id": pid, "scope": "session", "scorers": []any{"GoalCompletionJudge", "ReasoningConsistencyJudge"},
		"session_ids": []any{"e2e-1"}, "model": "mock/json"})
	ar := v1Items(t, e.post("/api/v1/scores/run", map[string]any{"project_id": pid, "scope": "agent", "scorers": []any{"ReasoningConsistencyJudge"},
		"session_ids": []any{"e2e-1"}, "model": "mock/json"}))
	if len(ar) != 2 {
		t.Fatalf("agent scope run: %v", ar)
	}
	sc := v1Items(t, e.v1Get("/api/v1/scores?"+P+"&scope=handoff&session_id=e2e-1"))
	if len(sc) != 1 || sc[0]["handoff_id"] != hid || sc[0]["scorer"] != "HandoffQualityJudge" || sc[0]["value"] == nil {
		t.Fatalf("scores %v", sc)
	}
	if all := e.v1Get("/api/v1/scores?" + P + "&session_id=e2e-1"); v1Num(all["total_count"]) != 5 {
		t.Fatalf("all scores %v", all["total_count"])
	}
	if a := e.v1Get("/api/v1/scores?" + P + "&scope=agent&agent=researcher"); v1Num(a["total_count"]) != 1 {
		t.Fatalf("agent scores %v", a["total_count"])
	}
	team := e.v1Get("/api/v1/teams/" + url.PathEscape("auto:orchestrator+researcher") + "?" + P)
	stats := team["handoff_stats"].([]any)
	if len(stats) != 1 || stats[0].(map[string]any)["mean_score"] == nil || len(team["recent_sessions"].([]any)) != 1 ||
		len(team["agent_stats"].([]any)) != 2 || len(team["graph"].(map[string]any)["edges"].([]any)) != 2 {
		t.Fatalf("team %v", team)
	}
	s2 := e.v1Get("/api/v1/sessions/e2e-1?" + P)
	if _, ok := s2["scores"].(map[string]any)["GoalCompletionJudge"]; !ok {
		t.Fatalf("session scores %v", s2["scores"])
	}
	ag2 := e.v1Get("/api/v1/agents/researcher?" + P)
	if _, ok := ag2["scores"].(map[string]any)["ReasoningConsistencyJudge"]; !ok {
		t.Fatalf("agent scores %v", ag2["scores"])
	}
	// Judges' own model calls are not agent activity.
	if again := e.v1Get("/api/v1/sessions?" + P); v1Num(again["total_count"]) != 1 {
		t.Fatalf("judge calls leaked into sessions: %v", again["total_count"])
	}
}

// seedV1 adds two more sessions next to the end-to-end one: an explicit
// team that failed, and a single agent that is still running.
func seedV1(t *testing.T, e *testEnv, base time.Time) {
	t.Helper()
	ch, pa, _ := twoAgentSession(hexID("tr1", 32), "e2e-1")
	e.v1Send(pid, base, append(ch, pa...))
	t2 := hexID("tr2", 32)
	e.v1Send(pid, base.Add(time.Minute), []v1Span{
		{t2, hexID("s2a", 16), "", "invoke_agent triage", 0, 500, map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "triage",
			"gen_ai.conversation.id": "support-1", "weft.team.id": "support", "weft.team.name": "Support desk",
			"gen_ai.input.messages": genaiMsgs("user", "My invoice is wrong")}, "", false, nil, nil},
		{t2, hexID("s2h", 16), hexID("s2a", 16), "handoff", 100, 1, map[string]any{"weft.handoff.to_agent": "billing", "weft.handoff.kind": "handoff"}, "", false, nil, nil},
		{t2, hexID("s2b", 16), "", "invoke_agent billing", 600, 900, map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "billing",
			"gen_ai.conversation.id": "support-1"}, "database down", false, nil, nil},
		{t2, hexID("s2c", 16), hexID("s2b", 16), "chat gpt-4o-mini", 610, 100, map[string]any{"gen_ai.operation.name": "chat",
			"gen_ai.request.model": "gpt-4o-mini", "gen_ai.output.messages": genaiMsgs("assistant", "Checking")}, "", false,
			[]v1Event{{"weft.reasoning", 605, map[string]any{"weft.reasoning.text": "Look up the invoice first.", "weft.reasoning.kind": "summary"}}}, nil},
	})
	t3 := hexID("tr3", 32)
	e.v1Send(pid, base.Add(2*time.Minute), []v1Span{
		{t3, hexID("s3a", 16), "", "invoke_agent coder", 0, 0, map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "coder",
			"gen_ai.conversation.id": "code-1", "gen_ai.input.messages": genaiMsgs("user", "Fix the flaky test")}, "", true, nil, nil},
	})
}

func TestAPIv1SessionFilters(t *testing.T) {
	e := newEnv(t)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	seedV1(t, e, base)
	P := "project_id=" + url.QueryEscape(pid)
	ids := func(path string) string {
		var out []string
		for _, s := range v1Items(t, e.v1Get(path)) {
			out = append(out, s["session_id"].(string))
		}
		return strings.Join(out, ",")
	}
	cases := []struct{ query, want string }{
		{"", "code-1,support-1,e2e-1"},
		{"sort_by=started_at", "e2e-1,support-1,code-1"},
		{"sort_by=started_at:desc", "code-1,support-1,e2e-1"},
		{"agent=researcher", "e2e-1"},
		{"team_id=support", "support-1"},
		{"team_id=" + url.QueryEscape("auto:orchestrator+researcher"), "e2e-1"},
		{"status=error", "support-1"},
		{"status=running", "code-1"},
		{"status=ok", "e2e-1"},
		{"model=gpt-4o-mini", "support-1"},
		{"tool=web_search", "e2e-1"},
		{"q=invoice", "support-1"},
		{"q=BILLING", "support-1"},
		{"q=support+desk", "support-1"},
		{"has_detection=any", ""},
		{"has_detection=block", ""},
		{"from=" + url.QueryEscape(base.Add(90*time.Second).Format(time.RFC3339)), "code-1"},
		{"to=" + url.QueryEscape(base.Add(30*time.Second).Format(time.RFC3339)), "e2e-1"},
		{"limit=1&offset=1", "support-1"},
		{"limit=2", "code-1,support-1"},
	}
	for _, c := range cases {
		if got := ids("/api/v1/sessions?" + P + "&" + c.query); got != c.want {
			t.Errorf("%s: got %q want %q", c.query, got, c.want)
		}
	}
	if m := e.v1Get("/api/v1/sessions?" + P + "&limit=1"); v1Num(m["total_count"]) != 3 {
		t.Fatalf("total_count %v", m["total_count"])
	}
	sup := e.v1Get("/api/v1/sessions/support-1?" + P)
	if sup["team_id"] != "support" || sup["team_name"] != "Support desk" || v1Num(sup["handoff_count"]) != 1 || v1Num(sup["error_count"]) != 1 ||
		v1Num(sup["reasoning_step_count"]) != 1 {
		t.Fatalf("support session %v", sup)
	}
	rs := v1Items(t, e.v1Get("/api/v1/sessions/support-1/reasoning?"+P))
	if len(rs) != 1 || rs[0]["kind"] != "summary" || rs[0]["source"] != "event" {
		t.Fatalf("event reasoning %v", rs)
	}
	tl := v1Items(t, e.v1Get("/api/v1/sessions/support-1/timeline?"+P+"&types=error"))
	if len(tl) != 1 || tl[0]["payload"].(map[string]any)["message"] != "database down" {
		t.Fatalf("errors %v", tl)
	}

	// Score filters.
	e.post("/api/v1/scores/run", map[string]any{"project_id": pid, "scope": "session", "scorers": []any{"GoalCompletionJudge"},
		"session_ids": []any{"e2e-1", "support-1"}, "model": "mock/json"})
	scores := v1Items(t, e.v1Get("/api/v1/scores?"+P+"&scope=session&scorer=GoalCompletionJudge"))
	if len(scores) != 2 {
		t.Fatalf("scores %v", scores)
	}
	v := map[string]float64{}
	for _, s := range scores {
		v[s["session_id"].(string)] = v1Num(s["value"])
	}
	lo, hi := "e2e-1", "support-1"
	if v[lo] > v[hi] {
		lo, hi = hi, lo
	}
	if got := ids("/api/v1/sessions?" + P + "&min_score=" + url.QueryEscape(fmt.Sprintf("GoalCompletionJudge:%v", v[hi]))); !strings.Contains(got, hi) ||
		(v[lo] < v[hi] && strings.Contains(got, lo)) {
		t.Errorf("min_score: %q (%v)", got, v)
	}
	if got := ids("/api/v1/sessions?" + P + "&max_score=" + url.QueryEscape(fmt.Sprintf("GoalCompletionJudge:%v", v[lo]))); !strings.Contains(got, lo) {
		t.Errorf("max_score: %q (%v)", got, v)
	}

	// Teams, hand-offs and agents filters.
	if got := v1Items(t, e.v1Get("/api/v1/teams?"+P+"&agent=billing")); len(got) != 1 || got[0]["team_id"] != "support" || got[0]["name"] != "Support desk" ||
		v1Num(got[0]["error_session_count"]) != 1 {
		t.Fatalf("teams by agent %v", got)
	}
	if m := e.v1Get("/api/v1/teams?" + P + "&sort_by=session_count&limit=1"); v1Num(m["total_count"]) != 3 || len(v1Items(t, m)) != 1 {
		t.Fatalf("teams paging %v", m)
	}
	for _, c := range []struct{ query, want string }{
		{"", "3"}, {"team_id=support", "1"}, {"from_agent=triage", "1"}, {"to_agent=researcher", "1"}, {"kind=return", "1"},
		{"agent=orchestrator", "2"}, {"session_id=support-1", "1"},
	} {
		if m := e.v1Get("/api/v1/handoffs?" + P + "&" + c.query); fmt.Sprint(m["total_count"]) != c.want {
			t.Errorf("handoffs %s: %v", c.query, m["total_count"])
		}
	}
	if m := e.v1Get("/api/v1/agents?" + P + "&team_id=support"); v1Num(m["total_count"]) != 2 {
		t.Fatalf("agents by team %v", m)
	}
	if m := e.v1Get("/api/v1/agents?" + P + "&q=cod"); v1Num(m["total_count"]) != 1 {
		t.Fatalf("agents by q %v", m)
	}
	if m := e.v1Get("/api/v1/agents?" + P + "&sort_by=handoffs_out:desc&limit=1"); v1Items(t, m)[0]["agent_name"] != "orchestrator" &&
		v1Items(t, m)[0]["agent_name"] != "triage" {
		t.Fatalf("agents sort %v", m)
	}
}

func TestAPIv1Errors(t *testing.T) {
	e := newEnv(t)
	seedV1(t, e, time.Now().Add(-time.Hour))
	P := "project_id=" + url.QueryEscape(pid)
	for _, c := range []struct {
		path string
		code int
	}{
		{"/api/v1/sessions", 400},
		{"/api/v1/sessions?" + P + "&status=bogus", 400},
		{"/api/v1/sessions?" + P + "&sort_by=nope", 400},
		{"/api/v1/sessions?" + P + "&limit=-1", 400},
		{"/api/v1/sessions?" + P + "&min_score=nocolon", 400},
		{"/api/v1/sessions?" + P + "&from=yesterday", 400},
		{"/api/v1/sessions/nope?" + P, 404},
		{"/api/v1/sessions/nope/timeline?" + P, 404},
		{"/api/v1/sessions/nope/graph?" + P, 404},
		{"/api/v1/sessions/nope/reasoning?" + P, 404},
		{"/api/v1/sessions/e2e-1/timeline?" + P + "&types=bogus", 400},
		{"/api/v1/agents/nobody?" + P, 404},
		{"/api/v1/teams/nobody?" + P, 404},
		{"/api/v1/handoffs?" + P + "&kind=bogus", 400},
		{"/api/v1/scores?" + P + "&scope=bogus", 400},
		{"/api/v1/sessions?project_id=bad%2Fproject%2Fid", 400},
	} {
		if code, out := e.raw(c.path, nil, nil); code != c.code {
			t.Errorf("%s: %d %s", c.path, code, out)
		}
	}
	for _, c := range []struct {
		body map[string]any
		code int
	}{
		{map[string]any{"scope": "session", "scorers": []any{"GoalCompletionJudge"}, "session_ids": []any{"e2e-1"}}, 400},
		{map[string]any{"project_id": pid, "scope": "turn", "scorers": []any{"GoalCompletionJudge"}, "session_ids": []any{"e2e-1"}}, 400},
		{map[string]any{"project_id": pid, "scope": "handoff", "scorers": []any{"GoalCompletionJudge"}, "session_ids": []any{"e2e-1"}}, 400},
		{map[string]any{"project_id": pid, "scope": "session", "scorers": []any{"NoSuchJudge"}, "session_ids": []any{"e2e-1"}}, 400},
		{map[string]any{"project_id": pid, "scope": "session", "scorers": []any{"GoalCompletionJudge"}}, 400},
		{map[string]any{"project_id": pid, "scope": "session", "scorers": []any{"GoalCompletionJudge"}, "session_ids": []any{"nope"}}, 400},
		{map[string]any{"project_id": pid, "scope": "handoff", "scorers": []any{"HandoffQualityJudge"}, "handoff_ids": []any{"nope"}}, 400},
	} {
		if code, out := e.raw("/api/v1/scores/run", c.body, nil); code != c.code {
			t.Errorf("%v: %d %s", c.body, code, out)
		}
	}
}

func TestAPIv1Auth(t *testing.T) {
	e := newEnv(t, "root")
	seedV1(t, e, time.Now().Add(-time.Hour))
	P := "project_id=" + url.QueryEscape(pid)
	scoped, _ := createKey(e, "root", map[string]any{"name": "ci", "projects": []any{pid}})
	other, _ := createKey(e, "root", map[string]any{"name": "other", "projects": []any{"acme/other"}})
	ro, _ := createKey(e, "root", map[string]any{"name": "dash", "read_only": true})
	run := map[string]any{"project_id": pid, "scope": "session", "scorers": []any{"GoalCompletionJudge"}, "session_ids": []any{"e2e-1"}, "model": "mock/json"}
	paths := []string{"/api/v1/sessions?" + P, "/api/v1/sessions/e2e-1?" + P, "/api/v1/sessions/e2e-1/timeline?" + P,
		"/api/v1/sessions/e2e-1/graph?" + P, "/api/v1/sessions/e2e-1/reasoning?" + P, "/api/v1/agents?" + P, "/api/v1/agents/researcher?" + P,
		"/api/v1/teams?" + P, "/api/v1/teams/support?" + P, "/api/v1/handoffs?" + P, "/api/v1/scores?" + P}
	for _, p := range paths {
		e.mustCode(401, "GET", p, "", nil)
		e.mustCode(200, "GET", p, scoped, nil)
		e.mustCode(200, "GET", p, ro, nil)
		e.mustCode(403, "GET", p, other, nil)
		e.mustCode(403, "GET", strings.Replace(p, P, "", 1), scoped, nil) // a scoped key must name its project
	}
	e.mustCode(401, "POST", "/api/v1/scores/run", "", run)
	e.mustCode(403, "POST", "/api/v1/scores/run", other, run)
	e.mustCode(403, "POST", "/api/v1/scores/run", ro, run)
	e.mustCode(200, "POST", "/api/v1/scores/run", scoped, run)
}

// verdictBody is an AgentShield verdict span (instrumentation scope agentshield).
func verdictBody(base time.Time, trace, id, session, agent string, atMS int) []byte {
	b := v1OTLP(base, []v1Span{{trace, id, "", "evaluate", atMS, 2, map[string]any{
		"agentshield.verdict.action": "block", "agentshield.verdict.mode": "enforce", "gen_ai.conversation.id": session,
		"gen_ai.tool.name": "web_search", "gen_ai.tool.call.id": "call_ws", "gen_ai.agent.name": agent}, "", false, nil, nil}})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	m["resourceSpans"].([]any)[0].(map[string]any)["scopeSpans"].([]any)[0].(map[string]any)["scope"] = map[string]any{"name": "agentshield"}
	b, _ = json.Marshal(m)
	return b
}

// TestAPIv1VerdictSpans: AgentShield verdict spans count as detections, not
// as agent activity, and a session seen only through verdicts is listed.
func TestAPIv1VerdictSpans(t *testing.T) {
	e := newEnv(t)
	base := time.Now().Add(-time.Hour)
	ch, pa, _ := twoAgentSession(hexID("vt", 32), "e2e-1")
	e.v1Send(pid, base, append(ch, pa...))
	for _, b := range [][]byte{verdictBody(base, hexID("ev", 32), hexID("ev1", 16), "e2e-1", "researcher", 1450),
		verdictBody(base, hexID("ev2", 32), hexID("ev2", 16), "guarded-only", "claude-code", 100)} {
		if code, out := e.raw("/otel/v1/traces", b, map[string]string{"project_id": pid}); code != 200 {
			t.Fatal(code, string(out))
		}
	}
	P := "project_id=" + url.QueryEscape(pid)
	s := e.v1Get("/api/v1/sessions/e2e-1?" + P)
	if v1Num(s["span_count"]) != 8 || strings.Join(v1Strs(s["agents"]), ",") != "orchestrator,researcher" {
		t.Fatalf("verdict span counted as activity: %v", s)
	}
	if d := s["detection_count"].(map[string]any); len(d) > 0 && v1Num(d["block"]) != 1 {
		t.Fatalf("detections %v", d)
	}
	for _, n := range e.v1Get("/api/v1/sessions/e2e-1/graph?" + P)["nodes"].([]any) {
		if v1Num(n.(map[string]any)["calls"]) != 1 {
			t.Fatalf("verdict span counted as an invocation: %v", n)
		}
	}
	g := e.v1Get("/api/v1/sessions/guarded-only?" + P)
	if v1Num(g["span_count"]) != 0 || g["started_at"] == nil || g["status"] != "ok" {
		t.Fatalf("verdict-only session %v", g)
	}
	if tl := e.v1Get("/api/v1/sessions/guarded-only/timeline?" + P); v1Num(tl["total_count"]) != 0 {
		t.Fatalf("verdict-only timeline %v", tl)
	}
	if a := e.v1Get("/api/v1/agents?" + P + "&q=claude-code"); v1Num(a["total_count"]) != 0 {
		t.Fatalf("verdict agent listed %v", a)
	}
	// Scorer classes run by name; ToolSafetyScorer scores sessions and agents only.
	res := v1Items(t, e.post("/api/v1/scores/run", map[string]any{"project_id": pid, "scope": "session", "scorers": []any{"ToolSafetyScorer"},
		"session_ids": []any{"e2e-1"}}))
	if len(res) != 1 || res[0]["error"] != nil || res[0]["value"] == nil || res[0]["feedback_id"] == nil {
		t.Fatalf("ToolSafetyScorer: %v", res)
	}
	if code, out := e.raw("/api/v1/scores/run", map[string]any{"project_id": pid, "scope": "handoff", "scorers": []any{"ToolSafetyScorer"},
		"session_ids": []any{"e2e-1"}}, nil); code != 400 {
		t.Fatalf("ToolSafetyScorer at handoff scope: %d %s", code, out)
	}
}
