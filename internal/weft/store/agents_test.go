package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestDeriveSpanKinds(t *testing.T) {
	otel := func(attrs map[string]any) map[string]any {
		return map[string]any{"otel": map[string]any{"attributes": attrs}}
	}
	cases := []struct {
		name, op string
		attrs    map[string]any
		inputs   map[string]any
		summary  map[string]any
		wantOp   string
		agent    string
		tool     string
		model    string
		provider string
	}{
		{"genai agent", "invoke_agent travel", otel(map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "travel"}), nil, nil, OpInvokeAgent, "travel", "", "", ""},
		{"agent from span name", "invoke_agent planner", otel(map[string]any{"gen_ai.operation.name": "invoke_agent"}), nil, nil, OpInvokeAgent, "planner", "", "", ""},
		{"genai chat", "chat gpt-4o", otel(map[string]any{"gen_ai.operation.name": "chat", "gen_ai.request.model": "gpt-4o", "gen_ai.provider.name": "openai"}), nil, nil, OpChat, "", "", "gpt-4o", "openai"},
		{"genai tool", "execute_tool search", otel(map[string]any{"gen_ai.operation.name": "execute_tool"}), nil, nil, OpExecuteTool, "", "search", "", ""},
		{"openinference llm", "ChatOpenAI", otel(map[string]any{"openinference.span.kind": "LLM", "llm.model_name": "gpt-4o-mini", "llm.provider": "openai"}), nil, nil, OpChat, "", "", "gpt-4o-mini", "openai"},
		{"openinference tool", "search", otel(map[string]any{"openinference.span.kind": "TOOL", "tool.name": "web_search"}), nil, nil, OpExecuteTool, "", "web_search", "", ""},
		{"openinference retriever", "retrieve", otel(map[string]any{"openinference.span.kind": "RETRIEVER"}), nil, nil, OpRetrieval, "", "", "", ""},
		{"openinference chain", "LangGraph", otel(map[string]any{"openinference.span.kind": "CHAIN"}), nil, nil, OpChain, "", "", "", ""},
		{"traceloop agent", "triage.agent", otel(map[string]any{"traceloop.span.kind": "agent", "traceloop.entity.name": "triage"}), nil, nil, OpInvokeAgent, "triage", "", "", ""},
		{"traceloop tool", "lookup.tool", otel(map[string]any{"traceloop.span.kind": "tool", "traceloop.entity.name": "lookup"}), nil, nil, OpExecuteTool, "", "lookup", "", ""},
		{"traceloop llm", "openai.chat", otel(map[string]any{"llm.request.type": "chat", "gen_ai.system": "openai", "gen_ai.request.model": "gpt-4o"}), nil, nil, OpChat, "", "", "gpt-4o", "openai"},
		{"mlflow tool", "fn", otel(map[string]any{"mlflow.spanType": `"TOOL"`}), nil, nil, OpExecuteTool, "", "fn", "", ""},
		{"mlflow chat model", "fn", otel(map[string]any{"mlflow.spanType": `"CHAT_MODEL"`}), nil, nil, OpChat, "", "", "", ""},
		{"vercel generate", "ai.generateText.doGenerate", otel(map[string]any{"ai.operationId": "ai.generateText.doGenerate", "ai.model.id": "gpt-4o-mini", "ai.model.provider": "openai.chat"}), nil, nil, OpChat, "", "", "gpt-4o-mini", "openai"},
		{"vercel operation.name", "x", otel(map[string]any{"operation.name": "ai.streamText.doStream myfn"}), nil, nil, OpChat, "", "", "", ""},
		{"vercel tool", "ai.toolCall", otel(map[string]any{"ai.operationId": "ai.toolCall", "ai.toolCall.name": "rhyme"}), nil, nil, OpExecuteTool, "", "rhyme", "", ""},
		{"vercel embed", "ai.embedMany", otel(map[string]any{"ai.operationId": "ai.embedMany"}), nil, nil, OpEmbeddings, "", "", "", ""},
		{"vercel wrapper", "ai.generateText", otel(map[string]any{"ai.operationId": "ai.generateText"}), nil, nil, OpChain, "", "", "", ""},
		{"adk llm", "call_llm", otel(map[string]any{"gcp.vertex.agent.llm_request": "{}"}), nil, nil, OpChat, "", "", "", ""},
		{"adk agent", "invoke_agent root_agent", otel(map[string]any{"gen_ai.agent.name": "root_agent"}), nil, nil, OpInvokeAgent, "root_agent", "", "", ""},
		{"pydantic agent run", "agent run", otel(map[string]any{"agent_name": "support", "model_name": "gpt-4o"}), nil, nil, OpInvokeAgent, "support", "", "gpt-4o", ""},
		{"pydantic tool", "running tool", otel(map[string]any{"gen_ai.tool.name": "lookup"}), nil, nil, OpExecuteTool, "", "lookup", "", ""},
		{"weft sdk agent", "weft:///e/p/op/support_bot:abc", map[string]any{"weft.span_kind": "agent"}, nil, nil, OpInvokeAgent, "support_bot", "", "", ""},
		{"weft sdk nested attr", "my_tool", map[string]any{"weft": map[string]any{"span_kind": "tool"}}, nil, nil, OpExecuteTool, "", "my_tool", "", ""},
		{"weave client kind", "lookup", map[string]any{"weave": map[string]any{"kind": "tool"}}, nil, nil, OpExecuteTool, "", "lookup", "", ""},
		{"weave client llm", "my_llm", map[string]any{"weave": map[string]any{"kind": "llm"}}, nil, nil, OpChat, "", "", "", ""},
		{"sdk openai", "openai.chat.completions.create", map[string]any{}, map[string]any{"model": "gpt-4o"}, nil, OpChat, "", "", "gpt-4o", "openai"},
		{"sdk llm by usage", "my_llm", map[string]any{}, map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}},
			map[string]any{"usage": map[string]any{"m": map[string]any{"prompt_tokens": 1}}}, OpChat, "", "", "", ""},
		{"plain op", "helper", map[string]any{}, map[string]any{"x": 1}, nil, "", "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.inputs == nil {
				c.inputs = map[string]any{}
			}
			d := DeriveSpan(c.op, "", nil, nil, "id1", c.attrs, c.inputs, c.summary)
			if d.Operation != c.wantOp || d.AgentName != c.agent || d.ToolName != c.tool || d.RequestModel != c.model || d.ProviderName != c.provider {
				t.Fatalf("got %+v", d)
			}
		})
	}
	turn := "id1"
	if d := DeriveSpan("x", "", nil, &turn, "id1", map[string]any{}, map[string]any{}, nil); !d.IsTurn {
		t.Fatal("turn_id == id is a turn")
	}
	if d := DeriveSpan("x", "", nil, nil, "id1", otel(map[string]any{"wandb.is_turn": true}), map[string]any{}, nil); !d.IsTurn {
		t.Fatal("wandb.is_turn is a turn")
	}
}

func TestAgentIndexInheritanceOutOfOrder(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	at := func(ms int) string { return now.Add(time.Duration(ms) * time.Millisecond).Format(time.RFC3339Nano) }
	parent := "root"
	child := "child"
	call := func(id string, parentID *string, op string, attrs map[string]any, thread *string) CompleteCall {
		return CompleteCall{Start: CallStart{ProjectID: "e/p", ID: id, TraceID: "tr", ParentID: parentID, OpName: op, StartedAt: at(0),
			Attributes: attrs, ThreadID: thread}, End: &CallEnd{EndedAt: at(10)}}
	}
	conv := "conv-x"
	// Grandchild and child first, then the root that names the agent.
	if _, err := st.UpsertComplete(ctx, []CompleteCall{
		call("grandchild", &child, "execute_tool t", map[string]any{"gen_ai.operation.name": "execute_tool"}, nil),
		call(child, &parent, "chat m", map[string]any{"gen_ai.operation.name": "chat"}, nil),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertComplete(ctx, []CompleteCall{
		call(parent, nil, "invoke_agent a", map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "alpha", "gen_ai.agent.version": "3"}, &conv),
	}); err != nil {
		t.Fatal(err)
	}
	spans, err := st.loadAgentSpans(ctx, "e/p", agentSpanFilter{})
	if err != nil || len(spans) != 3 {
		t.Fatalf("spans: %v %d", err, len(spans))
	}
	for _, sp := range spans {
		if sp.AgentName != "alpha" || sp.AgentVersion != "3" || sp.ConvID != conv {
			t.Fatalf("%s not inherited: %+v", sp.ID, sp)
		}
	}
	// A rebuild yields the same index.
	if err := st.ReindexAgentSpans(ctx, "e/p"); err != nil {
		t.Fatal(err)
	}
	spans, _ = st.loadAgentSpans(ctx, "e/p", agentSpanFilter{})
	for _, sp := range spans {
		if sp.AgentName != "alpha" || sp.ConvID != conv {
			t.Fatalf("after reindex %s: %+v", sp.ID, sp)
		}
	}
}

func TestAgentIndexBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := st.UpsertComplete(ctx, []CompleteCall{{Start: CallStart{ProjectID: "e/p", ID: "a", OpName: "invoke_agent bot", StartedAt: now,
		Attributes: map[string]any{"gen_ai.operation.name": "invoke_agent"}}, End: &CallEnd{EndedAt: now}}}); err != nil {
		t.Fatal(err)
	}
	// Simulate a database from before the index existed.
	if _, err := st.DB().Exec(`DELETE FROM agent_spans`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, _, err := st.QueryAgents(ctx, AgentsQueryReq{ProjectID: "e/p"})
	if err != nil || len(rows) != 1 || rows[0].AgentName != "bot" {
		t.Fatalf("backfilled agents: %v %+v", err, rows)
	}
}

func TestSpanGroupsCountSDKTurns(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	th := "conv-x"
	for i, id := range []string{"t1", "t2"} {
		st := start(id, "", "support_agent")
		st.TraceID = id
		st.ThreadID, st.TurnID = &th, ptr(id)
		st.StartedAt = fmt.Sprintf("2026-01-01T00:00:0%dZ", i)
		if err := s.StartCall(ctx, st); err != nil {
			t.Fatal(err)
		}
		if err := s.EndCall(ctx, end(id, nil, nil)); err != nil {
			t.Fatal(err)
		}
	}
	agg := newSpanAgg()
	rows, err := s.ReadDB().QueryContext(ctx, `SELECT is_turn FROM agent_spans WHERE project_id = ? AND conversation_id = ?`, proj, th)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var turn bool
		if err := rows.Scan(&turn); err != nil {
			t.Fatal(err)
		}
		agg.add(&agentSpan{IsTurn: turn})
	}
	if agg.turns != 2 || agg.invocations != 0 {
		t.Fatalf("turns=%d invocations=%d, want 2 and 0", agg.turns, agg.invocations)
	}
}

// Media attached to the first LLM call's user message (attach_media) shows on
// the turn's user message when the turn's own prompt carries none.
func TestConversationChatLLMInputMedia(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	th := "conv-media"
	part := map[string]any{"type": "uri", "uri": "s3://cust-media/frames/1.png", "mime_type": "image/png", "modality": "image"}
	root := start("turn", "", "turn")
	root.TraceID, root.ThreadID, root.TurnID = "tm", &th, ptr("turn")
	root.Attributes = map[string]any{"weft": map[string]any{"span_kind": "agent"}}
	root.Inputs = map[string]any{"messages": []any{map[string]any{"role": "user", "content": "show frame"}}}
	llm := start("llm", "turn", "chat")
	llm.TraceID, llm.ThreadID = "tm", &th
	llm.Attributes = map[string]any{"weft": map[string]any{"span_kind": "llm"}, "gen_ai": map[string]any{"operation": map[string]any{"name": "chat"}}}
	llm.Inputs = map[string]any{"model": "gpt-4o", "messages": []any{map[string]any{"role": "user", "content": []any{part}}}}
	for _, st := range []CallStart{root, llm} {
		if err := s.StartCall(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"llm", "turn"} {
		if err := s.EndCall(ctx, end(id, nil, nil)); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.ConversationChat(ctx, AgentConversationChatReq{ProjectID: proj, ConversationID: th})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Turns) != 1 || len(res.Turns[0].Messages) == 0 {
		t.Fatalf("turns = %+v", res.Turns)
	}
	u := res.Turns[0].Messages[0]
	if u.Type != "user_message" || u.UserMessage == nil || u.UserMessage.Text != "show frame" || len(u.Media) != 1 {
		t.Fatalf("user message = %+v (media %v)", u, u.Media)
	}
	if m, _ := u.Media[0].(map[string]any); m["uri"] != "s3://cust-media/frames/1.png" {
		t.Fatalf("media = %v", u.Media)
	}
}
