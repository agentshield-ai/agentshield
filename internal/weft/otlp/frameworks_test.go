package otlp

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func attrsOf(m map[string]any) []*common.KeyValue {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []*common.KeyValue
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			out = append(out, strAttr(k, v))
		case int:
			out = append(out, intAttr(k, int64(v)))
		case bool:
			out = append(out, &common.KeyValue{Key: k, Value: &common.AnyValue{Value: &common.AnyValue_BoolValue{BoolValue: v}}})
		default:
			b, _ := json.Marshal(v)
			out = append(out, strAttr(k, string(b)))
		}
	}
	return out
}

// oneCall converts a single span with the given name and attributes.
func oneCall(t *testing.T, name string, attrs map[string]any) store.CompleteCall {
	t.Helper()
	req := &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
		TraceId: []byte("0123456789abcdef"), SpanId: []byte("spanid01"), Name: name,
		StartTimeUnixNano: 1_700_000_000_000_000_000, EndTimeUnixNano: 1_700_000_001_000_000_000,
		Attributes: attrsOf(attrs),
	}}}}}}}
	calls, err := ToCalls(req, "e/p")
	if err != nil || len(calls) != 1 {
		t.Fatalf("ToCalls: %v %d", err, len(calls))
	}
	return calls[0]
}

func msgsOf(t *testing.T, v any) []map[string]any {
	t.Helper()
	l, ok := v.([]any)
	if !ok {
		t.Fatalf("not a message list: %#v", v)
	}
	out := make([]map[string]any, len(l))
	for i, x := range l {
		out[i] = x.(map[string]any)
	}
	return out
}

func firstChoice(t *testing.T, out any) map[string]any {
	t.Helper()
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("output = %#v", out)
	}
	return m["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
}

func usageOf(c store.CompleteCall) map[string]any {
	u, _ := c.End.Summary["usage"].(map[string]any)
	return u
}

func num(v any) float64 {
	f, _ := store.ToFloat(v)
	return f
}

func TestVercelAISDK(t *testing.T) {
	c := oneCall(t, "ai.generateText.doGenerate", map[string]any{
		"ai.operationId": "ai.generateText.doGenerate", "ai.model.id": "gpt-4o-mini", "ai.model.provider": "openai.chat",
		"ai.prompt.messages": `[{"role":"system","content":"Be brief."},
			{"role":"user","content":[{"type":"text","text":"Weather?"},{"type":"image","image":"iVBORw0KGgo=","mimeType":"image/png"}]},
			{"role":"assistant","content":[{"type":"tool-call","toolCallId":"tc1","toolName":"weather","args":{"city":"Oslo"}}]},
			{"role":"tool","content":[{"type":"tool-result","toolCallId":"tc1","toolName":"weather","result":{"temp":3}}]}]`,
		"ai.response.text": "It is 3°C.", "ai.response.toolCalls": `[{"toolCallType":"function","toolCallId":"tc2","toolName":"alerts","args":"{}"}]`,
		"ai.response.finishReason": "stop", "ai.usage.promptTokens": 40, "ai.usage.completionTokens": 7,
	})
	msgs := msgsOf(t, c.Start.Inputs["messages"])
	if len(msgs) != 4 || msgs[0]["content"] != "Be brief." || c.Start.Inputs["model"] != "gpt-4o-mini" {
		t.Fatalf("inputs = %v", c.Start.Inputs)
	}
	user := msgs[1]["content"].([]any)
	img := user[1].(map[string]any)
	if img["type"] != "image_url" || img["image_url"].(map[string]any)["url"] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("image part = %v", img)
	}
	tc := msgs[2]["tool_calls"].([]any)[0].(map[string]any)
	if tc["id"] != "tc1" || tc["function"].(map[string]any)["arguments"] != `{"city":"Oslo"}` {
		t.Fatalf("tool call = %v", tc)
	}
	if msgs[3]["role"] != "tool" || msgs[3]["tool_call_id"] != "tc1" || msgs[3]["content"] != `{"temp":3}` {
		t.Fatalf("tool result = %v", msgs[3])
	}
	out := firstChoice(t, c.End.Output)
	if out["content"] != "It is 3°C." || out["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"] != "alerts" {
		t.Fatalf("output = %v", out)
	}
	u := usageOf(c)["gpt-4o-mini"].(map[string]any)
	if num(u["prompt_tokens"]) != 40 || num(u["completion_tokens"]) != 7 || num(u["total_tokens"]) != 47 {
		t.Fatalf("usage = %v", usageOf(c))
	}

	root := oneCall(t, "ai.generateText", map[string]any{"ai.operationId": "ai.generateText",
		"ai.prompt": `{"system":"You are a poet.","prompt":"A haiku"}`, "ai.response.text": "Sea",
		"ai.telemetry.metadata.sessionId": "s-1", "ai.telemetry.metadata.userId": "u-9"})
	rm := msgsOf(t, root.Start.Inputs["messages"])
	if len(rm) != 2 || rm[0]["role"] != "system" || rm[1]["content"] != "A haiku" || *root.Start.ThreadID != "s-1" || *root.Start.WBUserID != "u-9" {
		t.Fatalf("root = %+v", root.Start)
	}
	tool := oneCall(t, "ai.toolCall", map[string]any{"ai.operationId": "ai.toolCall", "ai.toolCall.name": "rhyme",
		"ai.toolCall.args": `{"word":"sea"}`, "ai.toolCall.result": `["tree"]`})
	if tool.Start.Inputs["word"] != "sea" || tool.End.Output.([]any)[0] != "tree" {
		t.Fatalf("tool = %v %v", tool.Start.Inputs, tool.End.Output)
	}
}

func TestMLflowSpans(t *testing.T) {
	c := oneCall(t, "retrieve", map[string]any{"mlflow.spanType": `"RETRIEVER"`, "mlflow.spanInputs": `{"query":"weft"}`,
		"mlflow.spanOutputs": `"three documents"`, "mlflow.chat.tokenUsage": `{"input_tokens":5,"output_tokens":2,"total_tokens":7}`})
	if c.Start.Inputs["query"] != "weft" || c.End.Output != "three documents" {
		t.Fatalf("mlflow = %v %v", c.Start.Inputs, c.End.Output)
	}
	if u := usageOf(c); u == nil || num(u["unknown"].(map[string]any)["total_tokens"]) != 7 {
		t.Fatalf("mlflow usage = %v", u)
	}
	s := oneCall(t, "fn", map[string]any{"mlflow.spanInputs": `"plain"`})
	if s.Start.Inputs["input"] != "plain" {
		t.Fatalf("scalar input = %v", s.Start.Inputs)
	}
}

func TestPydanticAI(t *testing.T) {
	run := oneCall(t, "agent run", map[string]any{"agent_name": "support", "model_name": "gpt-4o",
		"pydantic_ai.all_messages": `[{"role":"system","parts":[{"type":"text","content":"Be kind."}]},
			{"role":"user","parts":[{"type":"text","content":"Hi"}]},
			{"role":"assistant","parts":[{"type":"tool_call","id":"c1","name":"lookup","arguments":{"q":"hi"}}]},
			{"role":"user","parts":[{"type":"tool_call_response","id":"c1","name":"lookup","result":"ok"}]},
			{"role":"assistant","parts":[{"type":"text","content":"Hello!"}]}]`,
		"final_result": "Hello!", "gen_ai.usage.input_tokens": 12, "gen_ai.usage.output_tokens": 3})
	msgs := msgsOf(t, run.Start.Inputs["messages"])
	if len(msgs) != 4 || msgs[3]["role"] != "tool" || msgs[3]["content"] != "ok" || msgs[2]["tool_calls"] == nil {
		t.Fatalf("history = %v", msgs)
	}
	if run.End.Output != "Hello!" || usageOf(run)["gpt-4o"] == nil {
		t.Fatalf("output %v usage %v", run.End.Output, usageOf(run))
	}
	tool := oneCall(t, "running tool", map[string]any{"gen_ai.tool.name": "lookup", "tool_arguments": `{"q":"hi"}`, "tool_response": "ok"})
	if tool.Start.Inputs["q"] != "hi" || tool.End.Output != "ok" {
		t.Fatalf("tool = %v %v", tool.Start.Inputs, tool.End.Output)
	}
	old := oneCall(t, "agent run", map[string]any{"all_messages_events": `[{"role":"user","content":"Hi","event.name":"gen_ai.user.message"},
		{"event.name":"gen_ai.choice","message":{"role":"assistant","content":"Hey"}}]`})
	om := msgsOf(t, old.Start.Inputs["messages"])
	if len(om) != 1 || om[0]["content"] != "Hi" {
		t.Fatalf("v1 events = %v", om)
	}
}

func TestGoogleADK(t *testing.T) {
	llm := oneCall(t, "call_llm", map[string]any{"gen_ai.system": "gcp.vertex.agent", "gcp.vertex.agent.session_id": "sess-1",
		"gcp.vertex.agent.llm_request": `{"model":"gemini-2.0-flash","config":{"system_instruction":"Help."},
			"contents":[{"role":"user","parts":[{"text":"Weather?"}]},
			{"role":"model","parts":[{"function_call":{"id":"f1","name":"weather","args":{"city":"Rome"}}}]},
			{"role":"user","parts":[{"function_response":{"id":"f1","name":"weather","response":{"temp":20}}}]}]}`,
		"gcp.vertex.agent.llm_response": `{"content":{"role":"model","parts":[{"text":"20°C"}]},"finish_reason":"STOP",
			"usage_metadata":{"prompt_token_count":30,"candidates_token_count":4,"total_token_count":34}}`})
	msgs := msgsOf(t, llm.Start.Inputs["messages"])
	if len(msgs) != 4 || msgs[0]["role"] != "system" || msgs[2]["role"] != "assistant" || msgs[3]["role"] != "tool" ||
		llm.Start.Inputs["model"] != "gemini-2.0-flash" || *llm.Start.ThreadID != "sess-1" {
		t.Fatalf("adk inputs = %v", llm.Start.Inputs)
	}
	if out := firstChoice(t, llm.End.Output); out["content"] != "20°C" {
		t.Fatalf("adk output = %v", out)
	}
	if u := usageOf(llm)["gemini-2.0-flash"].(map[string]any); num(u["prompt_tokens"]) != 30 || num(u["completion_tokens"]) != 4 {
		t.Fatalf("adk usage = %v", usageOf(llm))
	}
	tool := oneCall(t, "execute_tool weather", map[string]any{"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "weather",
		"gcp.vertex.agent.tool_call_args": `{"city":"Rome"}`, "gcp.vertex.agent.tool_response": `{"temp":20}`})
	if tool.Start.Inputs["city"] != "Rome" || num(tool.End.Output.(map[string]any)["temp"]) != 20 {
		t.Fatalf("adk tool = %v %v", tool.Start.Inputs, tool.End.Output)
	}
}

func TestGenAIToolAndTurnAttributes(t *testing.T) {
	c := oneCall(t, "execute_tool search", map[string]any{"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "search",
		"gen_ai.tool.call.id": "call_9", "gen_ai.tool.call.arguments": `{"q":"otel"}`, "gen_ai.tool.call.result": `{"hits":2}`,
		"wandb.is_turn": true, "wandb.thread_id": "th-1"})
	if c.Start.Inputs["q"] != "otel" || num(c.End.Output.(map[string]any)["hits"]) != 2 {
		t.Fatalf("tool = %v %v", c.Start.Inputs, c.End.Output)
	}
	if c.Start.TurnID == nil || *c.Start.TurnID != c.Start.ID || *c.Start.ThreadID != "th-1" {
		t.Fatalf("turn = %v thread = %v", c.Start.TurnID, c.Start.ThreadID)
	}
	notTurn := oneCall(t, "x", map[string]any{"wandb.is_turn": false})
	if notTurn.Start.TurnID != nil {
		t.Fatal("is_turn=false made a turn")
	}
	args := oneCall(t, "execute_tool echo", map[string]any{"gen_ai.tool.call.arguments": "plain text"})
	if args.Start.Inputs["input"] != "plain text" {
		t.Fatalf("string args = %v", args.Start.Inputs)
	}
}

func TestGenAIMediaAndReasoningParts(t *testing.T) {
	c := oneCall(t, "chat gpt-4o", map[string]any{"gen_ai.request.model": "gpt-4o",
		"gen_ai.input.messages": `[{"role":"user","parts":[{"type":"text","content":"What is this?"},
			{"type":"blob","modality":"image","mime_type":"image/jpeg","content":"/9j/4AAQ"},
			{"type":"uri","modality":"image","uri":"https://example.com/cat.png"},
			{"type":"blob","modality":"audio","mime_type":"audio/wav","content":"UklGRg=="}]}]`,
		"gen_ai.output.messages": `[{"role":"assistant","parts":[{"type":"reasoning","content":"Looks feline."},{"type":"text","content":"A cat."}],"finish_reason":"stop"}]`,
		"gen_ai.usage.input_tokens": 100, "gen_ai.usage.output_tokens": 20, "gen_ai.usage.reasoning.output_tokens": 8,
		"gen_ai.usage.cache_read.input_tokens": 50})
	parts := msgsOf(t, c.Start.Inputs["messages"])[0]["content"].([]any)
	if len(parts) != 4 {
		t.Fatalf("parts = %v", parts)
	}
	if u := parts[1].(map[string]any)["image_url"].(map[string]any)["url"]; u != "data:image/jpeg;base64,/9j/4AAQ" {
		t.Fatalf("blob image = %v", parts[1])
	}
	if u := parts[2].(map[string]any)["image_url"].(map[string]any)["url"]; u != "https://example.com/cat.png" {
		t.Fatalf("uri image = %v", parts[2])
	}
	if a := parts[3].(map[string]any); a["type"] != "media" || a["data"] != "data:audio/wav;base64,UklGRg==" {
		t.Fatalf("audio = %v", a)
	}
	out := firstChoice(t, c.End.Output)
	oc := out["content"].([]any)
	if oc[0].(map[string]any)["type"] != "thinking" || oc[1].(map[string]any)["text"] != "A cat." {
		t.Fatalf("output parts = %v", oc)
	}
	u := usageOf(c)["gpt-4o"].(map[string]any)
	if num(u["reasoning_tokens"]) != 8 || num(u["cache_read_input_tokens"]) != 50 || num(u["total_tokens"]) != 120 {
		t.Fatalf("usage = %v", u)
	}
}

func TestOpenInferenceLangChain(t *testing.T) {
	lc := `{"messages":[[{"lc":1,"type":"constructor","id":["langchain","schema","messages","SystemMessage"],"kwargs":{"content":"Be terse.","type":"system"}},
		{"lc":1,"type":"constructor","id":["langchain","schema","messages","HumanMessage"],"kwargs":{"content":"Hi","type":"human"}},
		{"lc":1,"type":"constructor","id":["langchain","schema","messages","AIMessage"],"kwargs":{"content":"","type":"ai",
		 "tool_calls":[{"name":"search","args":{"q":"x"},"id":"t1","type":"tool_call"}]}},
		{"lc":1,"type":"constructor","id":["langchain","schema","messages","ToolMessage"],"kwargs":{"content":"found","tool_call_id":"t1","type":"tool"}}]]}`
	chain := oneCall(t, "LangGraph", map[string]any{"openinference.span.kind": "CHAIN", "input.value": lc,
		"output.value": `{"messages":[{"type":"ai","content":"Done"}]}`})
	msgs := msgsOf(t, chain.Start.Inputs["messages"])
	if len(msgs) != 4 || msgs[0]["role"] != "system" || msgs[1]["content"] != "Hi" || msgs[3]["role"] != "tool" || msgs[3]["tool_call_id"] != "t1" {
		t.Fatalf("langchain messages = %v", msgs)
	}
	if fn := msgs[2]["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any); fn["name"] != "search" || fn["arguments"] != `{"q":"x"}` {
		t.Fatalf("tool call = %v", msgs[2])
	}
	if om := msgsOf(t, chain.End.Output.(map[string]any)["messages"]); om[0]["role"] != "assistant" || om[0]["content"] != "Done" {
		t.Fatalf("output = %v", chain.End.Output)
	}
	// LLM spans prefer llm.input_messages / llm.output_messages over raw input.value.
	llm := oneCall(t, "ChatOpenAI", map[string]any{"openinference.span.kind": "LLM", "input.value": lc, "output.value": `{"generations":[]}`,
		"llm.input_messages.0.message.role": "user", "llm.input_messages.0.message.content": "Hi",
		"llm.output_messages.0.message.role": "assistant", "llm.output_messages.0.message.content": "Hello",
		"llm.model_name": "gpt-4o-mini"})
	if m := msgsOf(t, llm.Start.Inputs["messages"]); len(m) != 1 || m[0]["content"] != "Hi" {
		t.Fatalf("llm inputs = %v", llm.Start.Inputs)
	}
	if out := firstChoice(t, llm.End.Output); out["content"] != "Hello" {
		t.Fatalf("llm output = %v", llm.End.Output)
	}
}

func TestTraceloopDisplayName(t *testing.T) {
	c := oneCall(t, "joke_generator.workflow", map[string]any{"traceloop.span.kind": "workflow", "traceloop.entity.name": "joke_generator",
		"traceloop.workflow.name": "joke_generator"})
	if c.Start.DisplayName == nil || *c.Start.DisplayName != "joke_generator" || c.Start.OpName != "joke_generator.workflow" {
		t.Fatalf("display = %v op = %v", c.Start.DisplayName, c.Start.OpName)
	}
	w := oneCall(t, "wf", map[string]any{"traceloop.span.kind": "workflow", "traceloop.workflow.name": "pipeline"})
	if w.Start.DisplayName == nil || *w.Start.DisplayName != "pipeline" {
		t.Fatalf("workflow display = %v", w.Start.DisplayName)
	}
	if plain := oneCall(t, "x", map[string]any{"traceloop.workflow.name": "pipeline"}); plain.Start.DisplayName != nil {
		t.Fatal("workflow name applied to a non-traceloop span")
	}
}

func TestSpanKindsReachIndex(t *testing.T) {
	// The span kind attributes stay in attributes.otel for the agents index.
	c := oneCall(t, "planner", map[string]any{"openinference.span.kind": "AGENT", "agent.name": "planner"})
	d := store.DeriveSpan(c.Start.OpName, "", c.Start.ThreadID, c.Start.TurnID, c.Start.ID, c.Start.Attributes, c.Start.Inputs, c.End.Summary)
	if d.Operation != store.OpInvokeAgent || d.AgentName != "planner" {
		t.Fatalf("derived = %+v", d)
	}
	if !strings.HasPrefix(c.Start.ID, "7370") { // hex of "spanid01"
		t.Fatalf("id = %s", c.Start.ID)
	}
}
