package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// gs is one span of a hand-off test, stored the way OTLP ingest stores it.
type gs struct {
	id, parent, trace, name string
	start, dur              int // ms
	attrs                   map[string]any
	inputs                  map[string]any
	output                  any
	err                     string
	events                  []any
	links                   []any
}

var graphBase = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

func (g gs) complete(pid string) CompleteCall {
	attrs := map[string]any{}
	for k, v := range g.attrs {
		attrs[k] = v
	}
	otel := map[string]any{"span_name": g.name, "attributes": attrs, "events": g.events, "links": g.links}
	if g.events == nil {
		otel["events"] = []any{}
	}
	if g.links == nil {
		otel["links"] = []any{}
	}
	trace := g.trace
	if trace == "" {
		trace = "trace1"
	}
	st := CallStart{ProjectID: pid, ID: g.id, TraceID: trace, OpName: g.name,
		StartedAt: graphBase.Add(time.Duration(g.start) * time.Millisecond).Format(time.RFC3339Nano),
		Attributes: map[string]any{"otel": otel}, Inputs: g.inputs}
	if st.Inputs == nil {
		st.Inputs = map[string]any{}
	}
	if g.parent != "" {
		p := g.parent
		st.ParentID = &p
	}
	en := &CallEnd{EndedAt: graphBase.Add(time.Duration(g.start+g.dur) * time.Millisecond).Format(time.RFC3339Nano), Output: g.output,
		Summary: map[string]any{}}
	if g.err != "" {
		e := g.err
		en.Exception = &e
	}
	return CompleteCall{Start: st, End: en}
}

func agentAttrs(name, conv string) map[string]any {
	m := map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": name}
	if conv != "" {
		m["gen_ai.conversation.id"] = conv
	}
	return m
}

func edgeKeys(t *testing.T, st *Store, pid string) []string {
	t.Helper()
	rows, _, err := st.QueryHandoffs(context.Background(), HandoffsQuery{ProjectID: pid, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, h := range rows {
		out = append(out, h.FromAgent+">"+h.ToAgent+":"+h.Kind+":"+h.Source)
	}
	sort.Strings(out)
	return out
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// ingestOrders writes spans in different orders and arrival patterns.
var ingestOrders = []struct {
	name  string
	write func(t *testing.T, st *Store, pid string, spans []gs)
}{
	{"one batch", func(t *testing.T, st *Store, pid string, spans []gs) {
		var cs []CompleteCall
		for _, s := range spans {
			cs = append(cs, s.complete(pid))
		}
		if _, err := st.UpsertComplete(context.Background(), cs); err != nil {
			t.Fatal(err)
		}
	}},
	{"one batch reversed", func(t *testing.T, st *Store, pid string, spans []gs) {
		var cs []CompleteCall
		for i := len(spans) - 1; i >= 0; i-- {
			cs = append(cs, spans[i].complete(pid))
		}
		if _, err := st.UpsertComplete(context.Background(), cs); err != nil {
			t.Fatal(err)
		}
	}},
	{"children first, one span per request", func(t *testing.T, st *Store, pid string, spans []gs) {
		for i := len(spans) - 1; i >= 0; i-- {
			if _, err := st.UpsertComplete(context.Background(), []CompleteCall{spans[i].complete(pid)}); err != nil {
				t.Fatal(err)
			}
		}
	}},
	{"starts then ends", func(t *testing.T, st *Store, pid string, spans []gs) {
		ctx := context.Background()
		for i := len(spans) - 1; i >= 0; i-- {
			if err := st.StartCall(ctx, spans[i].complete(pid).Start); err != nil {
				t.Fatal(err)
			}
		}
		for _, s := range spans {
			c := s.complete(pid)
			c.End.ProjectID, c.End.ID = pid, s.id
			if err := st.EndCall(ctx, *c.End); err != nil {
				t.Fatal(err)
			}
		}
	}},
}

var handoffCases = []struct {
	name  string
	spans []gs
	want  []string
}{
	{"nested invoke_agent delegates and returns", []gs{
		{id: "o", name: "invoke_agent orchestrator", start: 0, dur: 1000, attrs: agentAttrs("orchestrator", "s1")},
		{id: "r", parent: "o", name: "invoke_agent researcher", start: 100, dur: 500, attrs: agentAttrs("researcher", ""),
			inputs: map[string]any{"task": "find sources"}, output: "three sources"},
		{id: "t", parent: "r", name: "execute_tool search", start: 200, dur: 100, attrs: map[string]any{"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "search"}},
	}, []string{"orchestrator>researcher:delegate:nested", "researcher>orchestrator:return:return"}},
	{"nested invocation of the same agent is not a hand-off", []gs{
		{id: "o", name: "invoke_agent solver", start: 0, dur: 1000, attrs: agentAttrs("solver", "s1")},
		{id: "r", parent: "o", name: "invoke_agent solver", start: 100, dur: 500, attrs: agentAttrs("solver", "")},
	}, nil},
	{"weft.handoff attributes", []gs{
		{id: "o", name: "invoke_agent orchestrator", start: 0, dur: 1000, attrs: agentAttrs("orchestrator", "s2")},
		{id: "h", parent: "o", name: "route", start: 100, dur: 10, attrs: map[string]any{"weft.handoff.to_agent": "writer", "weft.handoff.kind": "handoff"}},
		{id: "w", parent: "o", name: "invoke_agent writer", start: 120, dur: 400, attrs: agentAttrs("writer", "")},
	}, []string{"orchestrator>writer:handoff:attribute"}},
	{"weft.handoff delegate gets a return", []gs{
		{id: "o", name: "invoke_agent lead", start: 0, dur: 1000, attrs: agentAttrs("lead", "s2b")},
		{id: "h", parent: "o", name: "ask", start: 100, dur: 600, attrs: map[string]any{"weft.handoff.to_agent": "analyst", "weft.handoff.from_agent": "lead",
			"weft.handoff.kind": "delegate"}},
		{id: "w", parent: "h", name: "invoke_agent analyst", start: 120, dur: 400, attrs: agentAttrs("analyst", "")},
	}, []string{"analyst>lead:return:return", "lead>analyst:delegate:attribute"}},
	{"OpenAI Agents SDK handoff span", []gs{
		{id: "wf", name: "Agent workflow", start: 0, dur: 900, attrs: map[string]any{"gen_ai.conversation.id": "s3"}},
		{id: "a1", parent: "wf", name: "Triage", start: 0, dur: 300, attrs: map[string]any{"openinference.span.kind": "AGENT", "graph.node.id": "Triage"}},
		{id: "h", parent: "a1", name: "handoff to Billing", start: 200, dur: 1, attrs: map[string]any{"from_agent": "Triage", "to_agent": "Billing"}},
		{id: "a2", parent: "wf", name: "Billing", start: 310, dur: 500, attrs: map[string]any{"openinference.span.kind": "AGENT", "graph.node.id": "Billing",
			"graph.node.parent_id": "Triage"}},
	}, []string{"Triage>Billing:handoff:openai_agents"}},
	{"OpenInference graph.node.parent_id without a handoff span", []gs{
		{id: "wf", name: "Agent workflow", start: 0, dur: 900, attrs: map[string]any{"gen_ai.conversation.id": "s3b"}},
		{id: "a1", parent: "wf", name: "Triage", start: 0, dur: 300, attrs: map[string]any{"openinference.span.kind": "AGENT", "graph.node.id": "Triage"}},
		{id: "a2", parent: "wf", name: "Refunds", start: 310, dur: 500, attrs: map[string]any{"openinference.span.kind": "AGENT", "graph.node.id": "Refunds",
			"graph.node.parent_id": "Triage"}},
	}, []string{"Triage>Refunds:handoff:openai_agents"}},
	{"Logfire-style handoff span name", []gs{
		{id: "a1", name: "invoke_agent Triage", start: 0, dur: 300, attrs: agentAttrs("Triage", "s3c")},
		{id: "h", parent: "a1", name: "Handoff: Triage → Support", start: 200, dur: 1, attrs: map[string]any{}},
		{id: "a2", name: "invoke_agent Support", start: 310, dur: 500, attrs: agentAttrs("Support", "s3c")},
	}, []string{"Triage>Support:handoff:openai_agents"}},
	{"LangGraph agent-node transitions", []gs{
		{id: "g", name: "LangGraph", start: 0, dur: 1000, attrs: map[string]any{"openinference.span.kind": "CHAIN", "gen_ai.conversation.id": "s4"}},
		{id: "n1", parent: "g", name: "supervisor", start: 10, dur: 190, attrs: map[string]any{"openinference.span.kind": "CHAIN",
			"metadata": `{"langgraph_node":"supervisor","langgraph_step":1}`}},
		{id: "c1", parent: "n1", name: "ChatOpenAI", start: 20, dur: 80, attrs: map[string]any{"openinference.span.kind": "LLM",
			"metadata": `{"langgraph_node":"supervisor","langgraph_step":1}`}},
		{id: "n2", parent: "g", name: "researcher", start: 210, dur: 290, attrs: map[string]any{"langgraph_node": "researcher", "langgraph_step": 2}},
		{id: "c2", parent: "n2", name: "chat gpt-4o", start: 220, dur: 100, attrs: map[string]any{"gen_ai.operation.name": "chat", "langgraph_node": "researcher"}},
		{id: "n3", parent: "g", name: "tools", start: 510, dur: 10, attrs: map[string]any{"langgraph_node": "tools", "langgraph_step": 3}},
		{id: "n4", parent: "g", name: "supervisor", start: 530, dur: 170, attrs: map[string]any{"langgraph_node": "supervisor", "langgraph_step": 4}},
		{id: "c4", parent: "n4", name: "chat gpt-4o", start: 540, dur: 100, attrs: map[string]any{"gen_ai.operation.name": "chat"}},
	}, []string{"researcher>supervisor:handoff:langgraph", "supervisor>researcher:handoff:langgraph"}},
	{"CrewAI delegation tool", []gs{
		{id: "m", name: "Manager", start: 0, dur: 1000, attrs: agentAttrs("Manager", "s5")},
		{id: "t", parent: "m", name: "Delegate work to coworker", start: 100, dur: 700, attrs: map[string]any{"gen_ai.operation.name": "execute_tool",
			"gen_ai.tool.name": "Delegate work to coworker"}, inputs: map[string]any{"coworker": "Senior Researcher", "task": "find X"}},
		{id: "r", parent: "t", name: "Senior Researcher", start: 150, dur: 550, attrs: agentAttrs("Senior Researcher", "")},
	}, []string{"Manager>Senior Researcher:delegate:crewai", "Senior Researcher>Manager:return:return"}},
	{"Google ADK transfer_to_agent", []gs{
		{id: "a", name: "invoke_agent root_agent", start: 0, dur: 1000, attrs: agentAttrs("root_agent", "s6")},
		{id: "l", parent: "a", name: "call_llm", start: 10, dur: 90, attrs: map[string]any{"gcp.vertex.agent.llm_request": "{}"}},
		{id: "t", parent: "a", name: "execute_tool transfer_to_agent", start: 110, dur: 10, attrs: map[string]any{"gen_ai.operation.name": "execute_tool",
			"gen_ai.tool.name": "transfer_to_agent"}, inputs: map[string]any{"agent_name": "billing_agent"}},
		{id: "b", parent: "a", name: "invoke_agent billing_agent", start: 130, dur: 700, attrs: agentAttrs("billing_agent", "")},
	}, []string{"root_agent>billing_agent:handoff:adk"}},
	{"span link to another agent's span in another trace", []gs{
		{id: "p", trace: "ta", name: "invoke_agent planner", start: 0, dur: 100, attrs: agentAttrs("planner", "s7")},
		{id: "e", trace: "tb", name: "invoke_agent executor", start: 200, dur: 200, attrs: agentAttrs("executor", "s7"),
			links: []any{map[string]any{"trace_id": "ta", "span_id": "p", "attributes": map[string]any{}}}},
	}, []string{"planner>executor:handoff:link"}},
}

func TestHandoffRules(t *testing.T) {
	for _, c := range handoffCases {
		for oi, o := range ingestOrders {
			t.Run(c.name+"/"+o.name, func(t *testing.T) {
				st := openTestStore(t)
				pid := fmt.Sprintf("acme/h%d", oi)
				o.write(t, st, pid, c.spans)
				got := edgeKeys(t, st, pid)
				if strings.Join(got, ",") != strings.Join(c.want, ",") {
					t.Fatalf("edges = %v, want %v", got, c.want)
				}
				// A rebuild from scratch gives the same edges.
				if err := st.ReindexAgentGraph(context.Background(), pid); err != nil {
					t.Fatal(err)
				}
				if again := edgeKeys(t, st, pid); strings.Join(again, ",") != strings.Join(c.want, ",") {
					t.Fatalf("after reindex: %v", again)
				}
			})
		}
	}
}

func TestHandoffFields(t *testing.T) {
	st := openTestStore(t)
	pid := "acme/fields"
	ingestOrders[2].write(t, st, pid, handoffCases[0].spans)
	rows, total, err := st.QueryHandoffs(context.Background(), HandoffsQuery{ProjectID: pid, Kind: HandoffDelegate})
	if err != nil || total != 1 {
		t.Fatalf("rows=%v total=%d err=%v", rows, total, err)
	}
	h := rows[0]
	if h.SessionID != "s1" || h.TraceID != "trace1" || *h.FromSpanID != "o" || *h.ToSpanID != "r" || h.SourceSpanID != "r" ||
		h.Status != "ok" || h.InputPreview != "find sources" || h.OutputPreview != "three sources" || h.DurationMS == nil || *h.DurationMS != 500 {
		t.Fatalf("handoff = %+v", h)
	}
	if h.TeamID != "auto:orchestrator+researcher" {
		t.Fatalf("team = %q", h.TeamID)
	}
	// Hand-off ids are stable across re-derivation.
	if err := st.ReindexAgentGraph(context.Background(), pid); err != nil {
		t.Fatal(err)
	}
	again, _, _ := st.QueryHandoffs(context.Background(), HandoffsQuery{ProjectID: pid, Kind: HandoffDelegate})
	if again[0].ID != h.ID {
		t.Fatalf("id changed: %s -> %s", h.ID, again[0].ID)
	}
	// Filters.
	for _, q := range []HandoffsQuery{{FromAgent: "orchestrator"}, {ToAgent: "researcher"}, {SessionID: "s1", Kind: "delegate"},
		{TeamID: "auto:orchestrator+researcher", Kind: "delegate"}} {
		q.ProjectID = pid
		if _, n, err := st.QueryHandoffs(context.Background(), q); err != nil || n != 1 {
			t.Fatalf("%+v: n=%d err=%v", q, n, err)
		}
	}
	if _, n, _ := st.QueryHandoffs(context.Background(), HandoffsQuery{ProjectID: pid, TeamID: "auto:nobody"}); n != 0 {
		t.Fatalf("unknown team matched %d", n)
	}
}

func TestHandoffRunningAndError(t *testing.T) {
	st := openTestStore(t)
	pid := "acme/status"
	ctx := context.Background()
	o := gs{id: "o", name: "invoke_agent boss", dur: 1000, attrs: agentAttrs("boss", "s1")}
	w := gs{id: "w", parent: "o", name: "invoke_agent worker", start: 10, dur: 100, attrs: agentAttrs("worker", "")}
	if err := st.StartCall(ctx, w.complete(pid).Start); err != nil {
		t.Fatal(err)
	}
	if err := st.StartCall(ctx, o.complete(pid).Start); err != nil {
		t.Fatal(err)
	}
	rows, _, _ := st.QueryHandoffs(ctx, HandoffsQuery{ProjectID: pid})
	if len(rows) != 1 || rows[0].Status != "running" || rows[0].EndedAt != nil {
		t.Fatalf("running: %+v", rows)
	}
	w.err = "boom"
	c := w.complete(pid)
	c.End.ProjectID, c.End.ID = pid, "w"
	if err := st.EndCall(ctx, *c.End); err != nil {
		t.Fatal(err)
	}
	got := edgeKeys(t, st, pid)
	if strings.Join(got, ",") != "boss>worker:delegate:nested,worker>boss:return:return" {
		t.Fatalf("edges %v", got)
	}
	rows, _, _ = st.QueryHandoffs(ctx, HandoffsQuery{ProjectID: pid})
	for _, h := range rows {
		if h.Status != "error" {
			t.Fatalf("status %+v", h)
		}
	}
}

func TestAgentGraphBackfill(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "w.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	pid := "acme/backfill"
	ingestOrders[0].write(t, st, pid, handoffCases[0].spans)
	ingestOrders[0].write(t, st, pid, []gs{{id: "c", parent: "o", name: "chat", start: 50, dur: 10, attrs: map[string]any{"gen_ai.operation.name": "chat"},
		output: map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "thinking", "thinking": "delegate the search"}}}}})
	// Simulate a database written before the agent graph existed.
	for _, q := range []string{`DELETE FROM agent_handoffs`, `DELETE FROM agent_span_meta`, `DELETE FROM reasoning_steps`,
		`DELETE FROM agent_span_links`, `DELETE FROM weft_backfills`} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if got := edgeKeys(t, st, pid); len(got) != 0 {
		t.Fatalf("not cleared: %v", got)
	}
	_ = st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if got := edgeKeys(t, st, pid); strings.Join(got, ",") != "orchestrator>researcher:delegate:nested,researcher>orchestrator:return:return" {
		t.Fatalf("after backfill: %v", got)
	}
	steps, _, err := st.SessionReasoning(context.Background(), SessionReasoningQuery{ProjectID: pid, SessionID: "s1"})
	if err != nil || len(steps) != 1 || steps[0].NextSpanID == nil || *steps[0].NextSpanID != "r" {
		t.Fatalf("reasoning after backfill: %+v %v", steps, err)
	}
	// The backfill runs once: a second open does not redo it.
	if _, err := st.DB().Exec(`DELETE FROM agent_handoffs`); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if got := edgeKeys(t, st2, pid); len(got) != 0 {
		t.Fatalf("backfill ran twice: %v", got)
	}
}
