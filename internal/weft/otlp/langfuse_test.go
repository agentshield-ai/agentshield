package otlp

import (
	"fmt"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// Langfuse SDK v3 span attributes (langfuse/_client/attributes.py).
func TestLangfuseAttributes(t *testing.T) {
	gen := oneCall(t, "openai-chat", map[string]any{
		"langfuse.observation.type":             "generation",
		"langfuse.observation.input":            `[{"role":"system","content":"Be terse."},{"role":"user","content":"Capital of France?"}]`,
		"langfuse.observation.output":           `{"role":"assistant","content":"Paris."}`,
		"langfuse.observation.model.name":       "gpt-4o-mini",
		"langfuse.observation.model.parameters": `{"temperature":0.2,"max_tokens":64}`,
		// Langfuse usage types are disjoint: input excludes input_cached_tokens and total sums them.
		"langfuse.observation.usage_details":  `{"input":8,"output":3,"total":15,"input_cached_tokens":4}`,
		"langfuse.observation.cost_details":   `{"total":0.0001}`,
		"langfuse.observation.prompt.name":    "capitals",
		"langfuse.observation.prompt.version": 2,
		"langfuse.observation.metadata.team":  `"geo"`,
		"langfuse.trace.name":                 "geo-qa",
		"langfuse.trace.tags":                 `["prod","geo"]`,
		"langfuse.session.id":                 "sess-42",
		"langfuse.user.id":                    "u-7",
		"input.value":                         "ignored: the Langfuse input wins",
	})
	msgs := msgsOf(t, gen.Start.Inputs["messages"])
	if len(msgs) != 2 || msgs[1]["content"] != "Capital of France?" {
		t.Fatalf("inputs = %v", gen.Start.Inputs)
	}
	if gen.Start.Inputs["model"] != "gpt-4o-mini" || num(gen.Start.Inputs["temperature"]) != 0.2 {
		t.Fatalf("model/params = %v", gen.Start.Inputs)
	}
	if out := firstChoice(t, gen.End.Output); out["content"] != "Paris." {
		t.Fatalf("output = %v", gen.End.Output)
	}
	u := usageOf(gen)["gpt-4o-mini"].(map[string]any)
	if num(u["prompt_tokens"]) != 12 || num(u["completion_tokens"]) != 3 || num(u["total_tokens"]) != 15 || num(u["cache_read_input_tokens"]) != 4 {
		t.Fatalf("usage = %v", usageOf(gen))
	}
	if *gen.Start.ThreadID != "sess-42" || *gen.Start.UserID != "u-7" || *gen.Start.DisplayName != "geo-qa" {
		t.Fatalf("ids = %v %v %v", *gen.Start.ThreadID, *gen.Start.UserID, *gen.Start.DisplayName)
	}
	if op := store.DeriveSpan(gen.Start.OpName, "", nil, nil, "x", gen.Start.Attributes, gen.Start.Inputs, nil).Operation; op != store.OpChat {
		t.Fatalf("agents index operation = %q", op)
	}
	if gen.Start.Attributes["weft.span_kind"] != "llm" {
		t.Fatalf("kind = %v", gen.Start.Attributes["weft.span_kind"])
	}
	lf := gen.Start.Attributes["langfuse"].(map[string]any)
	if lf["prompt.name"] != "capitals" || lf["metadata"].(map[string]any)["team"] != "geo" || len(lf["tags"].([]any)) != 2 {
		t.Fatalf("langfuse attrs = %v", lf)
	}

	// A plain span: dict input, string output; trace input/output as fallback; level ERROR.
	span := oneCall(t, "retrieve-docs", map[string]any{
		"langfuse.observation.type": "retriever", "langfuse.observation.input": `{"query":"weft"}`,
		"langfuse.observation.output": `["doc1","doc2"]`,
		"langfuse.observation.level":  "ERROR", "langfuse.observation.status_message": "index offline",
	})
	if span.Start.Inputs["query"] != "weft" || len(span.End.Output.([]any)) != 2 || span.Start.Attributes["weft.span_kind"] != "retriever" {
		t.Fatalf("span = %v %v", span.Start.Inputs, span.End.Output)
	}
	if span.End.Exception == nil || *span.End.Exception != "index offline" {
		t.Fatalf("exception = %v", span.End.Exception)
	}
	root := oneCall(t, "pipeline", map[string]any{"langfuse.trace.input": `"What is Weft?"`, "langfuse.trace.output": `{"answer":"a tracer"}`})
	if root.Start.Inputs["input"] != "What is Weft?" || root.End.Output.(map[string]any)["answer"] != "a tracer" || root.End.Exception != nil {
		t.Fatalf("trace io = %v %v", root.Start.Inputs, root.End.Output)
	}
}

// OpenLit (openlit/instrumentation/openai) chat span: GenAI conventions with
// system instructions repeated in gen_ai.input.messages, OpenLit's cost and
// client token usage, and a tool span.
func TestOpenLitFixture(t *testing.T) {
	c := oneCall(t, "chat gpt-4o-mini", map[string]any{
		"gen_ai.operation.name": "chat", "gen_ai.system": "openai", "gen_ai.provider.name": "openai",
		"gen_ai.request.model": "gpt-4o-mini", "gen_ai.response.model": "gpt-4o-mini-2024-07-18",
		"gen_ai.request.temperature": 0.7, "gen_ai.request.max_tokens": 100, "gen_ai.request.is_stream": false,
		"gen_ai.response.id": "chatcmpl-1", "gen_ai.response.finish_reasons": `["stop"]`, "gen_ai.output.type": "text",
		"gen_ai.system_instructions": `[{"type":"text","content":"You are helpful."}]`,
		"gen_ai.input.messages": `[{"role":"system","parts":[{"type":"text","content":"You are helpful."}]},
			{"role":"user","parts":[{"type":"text","content":"Hi"}]}]`,
		"gen_ai.output.messages":    `[{"role":"assistant","parts":[{"type":"text","content":"Hello!"}],"finish_reason":"stop"}]`,
		"gen_ai.usage.input_tokens": 9, "gen_ai.usage.output_tokens": 3, "gen_ai.client.token.usage": 12,
		"gen_ai.usage.cost": 0.0000032, "gen_ai.server.ttft": 0.21, "gen_ai.application_name": "demo",
		"gen_ai.environment": "dev", "gen_ai.session.id": "chat-9", "gen_ai.user.id": "eve",
	})
	msgs := msgsOf(t, c.Start.Inputs["messages"])
	if len(msgs) != 2 || msgs[0]["role"] != "system" || msgs[1]["content"] != "Hi" {
		t.Fatalf("messages (system must not repeat) = %v", msgs)
	}
	if c.Start.Inputs["model"] != "gpt-4o-mini" || fmt.Sprint(c.Start.Inputs["temperature"]) != "0.7" {
		t.Fatalf("inputs = %v", c.Start.Inputs)
	}
	out := firstChoice(t, c.End.Output)
	if out["content"] != "Hello!" || out["role"] != "assistant" {
		t.Fatalf("output = %v", c.End.Output)
	}
	u := usageOf(c)["gpt-4o-mini-2024-07-18"].(map[string]any)
	if num(u["prompt_tokens"]) != 9 || num(u["completion_tokens"]) != 3 || num(u["total_tokens"]) != 12 {
		t.Fatalf("usage = %v", usageOf(c))
	}
	if *c.Start.ThreadID != "chat-9" || *c.Start.UserID != "eve" {
		t.Fatalf("session/user = %v %v", *c.Start.ThreadID, *c.Start.UserID)
	}
	tool := oneCall(t, "execute_tool get_weather", map[string]any{"gen_ai.operation.name": "execute_tool",
		"gen_ai.tool.name": "get_weather", "gen_ai.tool.input": `{"city":"Oslo"}`, "gen_ai.tool.output": `{"temp":3}`})
	if tool.Start.Inputs["city"] != "Oslo" || tool.End.Output.(map[string]any)["temp"] == nil {
		t.Fatalf("tool = %v %v", tool.Start.Inputs, tool.End.Output)
	}
}

// Koog (ai.koog.agents.features.opentelemetry): GenAI messages with Kotlin
// role names and empty tool entries, node spans, and the indexed-attribute adapter's
// indexed prompt/completion attributes.
func TestKoogFixture(t *testing.T) {
	chat := oneCall(t, "chat gpt-4o", map[string]any{
		"gen_ai.operation.name": "chat", "gen_ai.provider.name": "openai", "gen_ai.request.model": "gpt-4o",
		"gen_ai.conversation.id": "run-123", "gen_ai.agent.id": "weather-agent",
		"gen_ai.input.messages": `[{"role":"System","parts":[{"type":"text","content":"You answer weather questions."}]},
			{"role":"tool","parts":[]},
			{"role":"User","parts":[{"type":"text","content":"Weather in Oslo?"}]},
			{"role":"Assistant","parts":[{"type":"tool_call","id":"call_1","name":"weather","arguments":{"city":"Oslo"}}]},
			{"role":"tool","parts":[{"type":"tool_call_response","id":"call_1","result":"3°C"}]},
			{"role":"User","parts":[]}]`,
		"gen_ai.output.messages":    `[{"role":"Assistant","parts":[{"type":"text","content":"It is 3°C in Oslo."}]}]`,
		"gen_ai.usage.input_tokens": 50, "gen_ai.usage.output_tokens": 9,
	})
	msgs := msgsOf(t, chat.Start.Inputs["messages"])
	roles := []string{}
	for _, m := range msgs {
		roles = append(roles, m["role"].(string))
	}
	if len(msgs) != 5 || roles[0] != "system" || roles[1] != "user" || roles[2] != "assistant" || roles[3] != "tool" || roles[4] != "user" {
		t.Fatalf("roles = %v (%v)", roles, msgs)
	}
	tc := msgs[2]["tool_calls"].([]any)[0].(map[string]any)
	if tc["id"] != "call_1" || tc["function"].(map[string]any)["arguments"] != `{"city":"Oslo"}` || msgs[3]["tool_call_id"] != "call_1" {
		t.Fatalf("tool call/result = %v / %v", tc, msgs[3])
	}
	if out := firstChoice(t, chat.End.Output); out["role"] != "assistant" || out["content"] != "It is 3°C in Oslo." {
		t.Fatalf("output = %v", chat.End.Output)
	}
	if *chat.Start.ThreadID != "run-123" {
		t.Fatalf("thread = %v", chat.Start.ThreadID)
	}
	node := oneCall(t, "node nodeCallLLM", map[string]any{"koog.node.id": "nodeCallLLM", "koog.event.id": "e1",
		"koog.node.input": `"Weather in Oslo?"`, "koog.node.output": `{"content":"It is 3°C"}`})
	if node.Start.Inputs["input"] != "Weather in Oslo?" || node.End.Output.(map[string]any)["content"] != "It is 3°C" {
		t.Fatalf("node = %v %v", node.Start.Inputs, node.End.Output)
	}
	tool := oneCall(t, "execute_tool weather", map[string]any{"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "weather",
		"gen_ai.tool.call.id": "call_1", "gen_ai.tool.call.arguments": `{"city":"Oslo"}`, "gen_ai.tool.call.result": `"3°C"`,
		"koog.tool.call.status": "success"})
	if tool.Start.Inputs["city"] != "Oslo" || tool.End.Output != "3°C" {
		t.Fatalf("tool = %v %v", tool.Start.Inputs, tool.End.Output)
	}

	// Indexed-attribute adapter: gen_ai.prompt.N / gen_ai.completion.N, tool calls as function JSON.
	indexed := oneCall(t, "chat gpt-4o", map[string]any{
		"gen_ai.operation.name": "chat", "gen_ai.request.model": "gpt-4o",
		"gen_ai.prompt.0.role": "system", "gen_ai.prompt.0.content": "You answer weather questions.",
		"gen_ai.prompt.1.role": "user", "gen_ai.prompt.1.content": "Weather in Oslo?",
		"gen_ai.completion.0.role": "assistant", "gen_ai.completion.0.finish_reason": "tool_calls",
		"gen_ai.completion.0.tool_calls.0.function": `{"name":"weather","arguments":"{\"city\":\"Oslo\"}"}`,
		"gen_ai.completion.0.tool_calls.0.id":       "call_1", "gen_ai.completion.0.tool_calls.0.type": "function",
		"gen_ai.usage.prompt_tokens": 50, "gen_ai.usage.completion_tokens": 9,
	})
	if len(msgsOf(t, indexed.Start.Inputs["messages"])) != 2 {
		t.Fatalf("indexed inputs = %v", indexed.Start.Inputs)
	}
	wtc := firstChoice(t, indexed.End.Output)["tool_calls"].([]any)[0].(map[string]any)
	if wtc["id"] != "call_1" || wtc["function"].(map[string]any)["name"] != "weather" ||
		wtc["function"].(map[string]any)["arguments"] != `{"city":"Oslo"}` {
		t.Fatalf("indexed tool call = %v", wtc)
	}
	if u := usageOf(indexed)["gpt-4o"].(map[string]any); num(u["total_tokens"]) != 59 {
		t.Fatalf("indexed usage = %v", usageOf(indexed))
	}
}

// verl (verl/utils/rollout_trace.py) with trace.backend=mlflow: rollout ops
// become MLflow spans named by the method's qualified name, with
// span.set_inputs / set_outputs and prompt/response token ids plus decoded
// text (token2text). verl's other tracing backend writes through another
// product's client; that path is covered by the compatibility adapter's
// tests (package compat).
func TestVerlMLflowFixture(t *testing.T) {
	c := oneCall(t, "ToolAgentLoop.run", map[string]any{
		"mlflow.spanType":    `"UNKNOWN"`,
		"mlflow.spanInputs":  `{"sampling_params":{"temperature":1.0},"raw_prompt":[{"role":"user","content":"2+2?"}]}`,
		"mlflow.spanOutputs": `{"prompt_ids":[1,2,3],"response_ids":[4,5],"num_turns":2,"prompt_text":"2+2?","response_text":"4"}`,
	})
	if c.Start.OpName != "ToolAgentLoop.run" || c.Start.Inputs["raw_prompt"] == nil {
		t.Fatalf("inputs = %v", c.Start.Inputs)
	}
	out := c.End.Output.(map[string]any)
	if out["response_text"] != "4" || num(out["num_turns"]) != 2 {
		t.Fatalf("output = %v", out)
	}
}
