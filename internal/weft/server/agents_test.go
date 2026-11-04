package server

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// otlpAttr encodes one OTLP JSON attribute.
func otlpAttr(k string, v any) map[string]any {
	var val map[string]any
	switch t := v.(type) {
	case string:
		val = map[string]any{"stringValue": t}
	case int:
		val = map[string]any{"intValue": strconv.Itoa(t)}
	case float64:
		val = map[string]any{"doubleValue": t}
	case bool:
		val = map[string]any{"boolValue": t}
	default:
		b, _ := json.Marshal(t)
		val = map[string]any{"stringValue": string(b)}
	}
	return map[string]any{"key": k, "value": val}
}

type testSpan struct {
	trace, id, parent, name string
	startMS, durMS          int
	attrs                   map[string]any
	err                     string
}

func otlpBody(base time.Time, spans []testSpan) []byte {
	var out []any
	for _, s := range spans {
		var attrs []any
		for k, v := range s.attrs {
			attrs = append(attrs, otlpAttr(k, v))
		}
		start := base.Add(time.Duration(s.startMS) * time.Millisecond)
		sp := map[string]any{"traceId": s.trace, "spanId": s.id, "name": s.name, "kind": 1, "attributes": attrs,
			"startTimeUnixNano": strconv.FormatInt(start.UnixNano(), 10),
			"endTimeUnixNano":   strconv.FormatInt(start.Add(time.Duration(s.durMS)*time.Millisecond).UnixNano(), 10)}
		if s.parent != "" {
			sp["parentSpanId"] = s.parent
		}
		if s.err != "" {
			sp["status"] = map[string]any{"code": 2, "message": s.err}
		}
		out = append(out, sp)
	}
	b, _ := json.Marshal(map[string]any{"resourceSpans": []any{map[string]any{
		"resource":   map[string]any{"attributes": []any{otlpAttr("service.name", "agent-svc")}},
		"scopeSpans": []any{map[string]any{"scope": map[string]any{"name": "test"}, "spans": out}}}}})
	return b
}

func hexID(prefix string, n int) string {
	s := strings.Repeat("0", n) + fmt.Sprintf("%x", prefix)
	return s[len(s)-n:]
}

func genaiMsgs(role, text string) string {
	b, _ := json.Marshal([]any{map[string]any{"role": role, "parts": []any{map[string]any{"type": "text", "content": text}}}})
	return string(b)
}

// seedAgentData sends a two-turn GenAI agent conversation (children before
// parents, as exporters do), a Vercel AI SDK trace and an OpenInference trace.
func seedAgentData(t *testing.T, e *testEnv, base time.Time) {
	t.Helper()
	t1, t2, tv, toi := hexID("t1", 32), hexID("t2", 32), hexID("tv", 32), hexID("toi", 32)
	toolCall := `[{"role":"assistant","parts":[{"type":"tool_call","id":"call_1","name":"get_weather","arguments":{"city":"Paris"}}]}]`
	turn1 := []testSpan{
		{t1, hexID("c1", 16), hexID("a1", 16), "chat gpt-4o", 10, 400, map[string]any{
			"gen_ai.operation.name": "chat", "gen_ai.request.model": "gpt-4o", "gen_ai.provider.name": "openai",
			"gen_ai.input.messages": genaiMsgs("user", "What's the weather in Paris?"), "gen_ai.output.messages": toolCall,
			"gen_ai.usage.input_tokens": 120, "gen_ai.usage.output_tokens": 15}, ""},
		{t1, hexID("x1", 16), hexID("a1", 16), "execute_tool get_weather", 420, 200, map[string]any{
			"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "get_weather", "gen_ai.tool.call.id": "call_1",
			"gen_ai.tool.call.arguments": `{"city":"Paris"}`, "gen_ai.tool.call.result": `{"temp_c":18,"sky":"sunny"}`}, ""},
		{t1, hexID("c2", 16), hexID("a1", 16), "chat gpt-4o", 630, 500, map[string]any{
			"gen_ai.operation.name": "chat", "gen_ai.request.model": "gpt-4o", "gen_ai.provider.name": "openai",
			"gen_ai.input.messages":     genaiMsgs("user", "What's the weather in Paris?"),
			"gen_ai.output.messages":    genaiMsgs("assistant", "It is 18°C and sunny in Paris."),
			"gen_ai.usage.input_tokens": 160, "gen_ai.usage.output_tokens": 12}, ""},
		{t1, hexID("a1", 16), "", "invoke_agent travel-agent", 0, 1200, map[string]any{
			"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "travel-agent", "gen_ai.agent.version": "v2",
			"gen_ai.conversation.id": "conv-1", "gen_ai.input.messages": genaiMsgs("user", "What's the weather in Paris?"),
			"gen_ai.output.messages": genaiMsgs("assistant", "It is 18°C and sunny in Paris."), "tenant": "acme", "priority": 3}, ""},
	}
	turn2 := []testSpan{
		{t2, hexID("a2", 16), "", "invoke_agent travel-agent", 60_000, 1500, map[string]any{
			"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "travel-agent", "gen_ai.agent.version": "v2",
			"gen_ai.conversation.id": "conv-1", "gen_ai.input.messages": genaiMsgs("user", "And tomorrow in Paris?"),
			"gen_ai.output.messages": genaiMsgs("assistant", "Tomorrow looks rainy."), "tenant": "acme"}, ""},
		{t2, hexID("c3", 16), hexID("a2", 16), "chat gpt-4o", 60_010, 300, map[string]any{
			"gen_ai.operation.name": "chat", "gen_ai.request.model": "gpt-4o", "gen_ai.input.messages": genaiMsgs("user", "And tomorrow in Paris?"),
			"gen_ai.output.messages": strings.ReplaceAll(toolCall, "call_1", "call_2"), "gen_ai.usage.input_tokens": 200, "gen_ai.usage.output_tokens": 10}, ""},
		{t2, hexID("x2", 16), hexID("a2", 16), "execute_tool get_weather", 60_320, 100, map[string]any{
			"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "get_weather", "gen_ai.tool.call.id": "call_2"}, "upstream timeout"},
		{t2, hexID("c4", 16), hexID("a2", 16), "chat gpt-4o", 60_430, 600, map[string]any{
			"gen_ai.operation.name": "chat", "gen_ai.request.model": "gpt-4o", "gen_ai.output.messages": genaiMsgs("assistant", "Tomorrow looks rainy."),
			"gen_ai.usage.input_tokens": 220, "gen_ai.usage.output_tokens": 8}, ""},
	}
	vercel := []testSpan{
		{tv, hexID("v1", 16), "", "ai.generateText", 120_000, 2000, map[string]any{
			"ai.operationId": "ai.generateText", "operation.name": "ai.generateText haiku", "ai.model.id": "gpt-4o-mini",
			"ai.prompt": `{"system":"You are a poet.","prompt":"Write a haiku about the sea"}`, "ai.response.text": "Waves fold into foam",
			"ai.telemetry.metadata.sessionId": "vercel-session"}, ""},
		{tv, hexID("v2", 16), hexID("v1", 16), "ai.generateText.doGenerate", 120_010, 900, map[string]any{
			"ai.operationId": "ai.generateText.doGenerate", "ai.model.id": "gpt-4o-mini", "ai.model.provider": "openai.chat",
			"ai.prompt.messages":       `[{"role":"system","content":"You are a poet."},{"role":"user","content":[{"type":"text","text":"Write a haiku about the sea"}]}]`,
			"ai.response.toolCalls":    `[{"toolCallType":"function","toolCallId":"tc1","toolName":"rhyme","args":"{\"word\":\"sea\"}"}]`,
			"ai.response.finishReason": "tool-calls", "ai.usage.promptTokens": 30, "ai.usage.completionTokens": 9}, ""},
		{tv, hexID("v3", 16), hexID("v1", 16), "ai.toolCall", 120_920, 50, map[string]any{
			"ai.operationId": "ai.toolCall", "ai.toolCall.name": "rhyme", "ai.toolCall.id": "tc1",
			"ai.toolCall.args": `{"word":"sea"}`, "ai.toolCall.result": `["free","tree"]`}, ""},
		{tv, hexID("v4", 16), hexID("v1", 16), "ai.generateText.doGenerate", 121_000, 900, map[string]any{
			"ai.operationId": "ai.generateText.doGenerate", "ai.model.id": "gpt-4o-mini", "ai.model.provider": "openai.chat",
			"ai.prompt.messages": `[{"role":"user","content":"Write a haiku about the sea"},{"role":"assistant","content":[{"type":"tool-call","toolCallId":"tc1","toolName":"rhyme","args":{"word":"sea"}}]},{"role":"tool","content":[{"type":"tool-result","toolCallId":"tc1","toolName":"rhyme","result":["free","tree"]}]}]`,
			"ai.response.text":   "Waves fold into foam", "ai.usage.promptTokens": 50, "ai.usage.completionTokens": 12}, ""},
	}
	lcIn := `{"messages":[{"lc":1,"type":"constructor","id":["langchain","schema","messages","HumanMessage"],"kwargs":{"content":"Find papers on RAG","type":"human"}}]}`
	openinference := []testSpan{
		{toi, hexID("o1", 16), "", "research-agent", 180_000, 3000, map[string]any{
			"openinference.span.kind": "AGENT", "agent.name": "research-agent", "session.id": "sess-9",
			"input.value": lcIn, "input.mime_type": "application/json", "output.value": "Found 3 papers.", "metadata": `{"team":"search"}`}, ""},
		{toi, hexID("o2", 16), hexID("o1", 16), "ChatOpenAI", 180_100, 1200, map[string]any{
			"openinference.span.kind": "LLM", "llm.model_name": "gpt-4o-mini", "llm.provider": "openai", "input.value": lcIn,
			"llm.input_messages.0.message.role": "user", "llm.input_messages.0.message.content": "Find papers on RAG",
			"llm.output_messages.0.message.role": "assistant", "llm.output_messages.0.message.content": "Found 3 papers.",
			"llm.token_count.prompt": 40, "llm.token_count.completion": 6}, ""},
		{toi, hexID("o3", 16), hexID("o1", 16), "arxiv_search", 181_400, 800, map[string]any{
			"openinference.span.kind": "TOOL", "tool.name": "arxiv_search", "input.value": `{"query":"RAG"}`, "output.value": `["p1","p2","p3"]`}, ""},
	}
	var all []testSpan
	all = append(append(append(append(all, turn1...), turn2...), vercel...), openinference...)
	// The first batch goes to the agents OTLP alias.
	if code, out := e.raw("/agents/otel/v1/traces", otlpBody(base, all[:4]), map[string]string{"project_id": pid}); code != 200 {
		t.Fatalf("agents otel ingest: %d %s", code, out)
	}
	if code, out := e.raw("/otel/v1/traces", otlpBody(base, all[4:]), map[string]string{"project_id": pid}); code != 200 {
		t.Fatalf("otel ingest: %d %s", code, out)
	}
}

func findAgent(rows []any, name string) map[string]any {
	for _, r := range rows {
		if m := r.(map[string]any); m["agent_name"] == name {
			return m
		}
	}
	return nil
}

func TestAgentsQueryAndVersions(t *testing.T) {
	e := newEnv(t)
	seedAgentData(t, e, time.Now().Add(-time.Hour))
	res := e.post("/agents/query", map[string]any{"project_id": pid, "include_costs": true})
	agents := res["agents"].([]any)
	if res["total_count"].(float64) != 2 {
		t.Fatalf("agents = %v", res)
	}
	ta := findAgent(agents, "travel-agent")
	if ta == nil {
		t.Fatalf("travel-agent missing: %v", agents)
	}
	// Children inherit the agent even though they arrived before their parents.
	if ta["span_count"].(float64) != 8 || ta["invocation_count"].(float64) != 2 || ta["error_count"].(float64) != 1 {
		t.Fatalf("travel-agent stats = %v", ta)
	}
	if ta["total_input_tokens"].(float64) != 700 || ta["total_output_tokens"].(float64) != 45 || ta["total_cost_usd"] == nil {
		t.Fatalf("travel-agent usage = %v", ta)
	}
	if ta["conversation_count"].(float64) != 1 || ta["version_count"].(float64) != 1 || ta["latency_ms_p50"] == nil {
		t.Fatalf("travel-agent extras = %v", ta)
	}
	if ra := findAgent(agents, "research-agent"); ra == nil || ra["invocation_count"].(float64) != 1 || ra["span_count"].(float64) != 3 {
		t.Fatalf("research-agent = %v", ra)
	}
	// Name filter and sorting.
	res = e.post("/agents/query", map[string]any{"project_id": pid, "filters": map[string]any{"agent_name": "TRAVEL"},
		"sort_by": []any{map[string]any{"field": "span_count", "direction": "desc"}}})
	if res["total_count"].(float64) != 1 {
		t.Fatalf("filtered agents = %v", res)
	}
	if code, _ := e.raw("/agents/query", map[string]any{"project_id": pid, "sort_by": []any{map[string]any{"field": "nope"}}}, nil); code != 400 {
		t.Fatalf("bad sort accepted: %d", code)
	}

	v := e.post("/agents/agent-versions/query", map[string]any{"project_id": pid, "agent_name": "travel-agent"})
	vs := v["versions"].([]any)
	if len(vs) != 1 || vs[0].(map[string]any)["agent_version"] != "v2" || vs[0].(map[string]any)["invocation_count"].(float64) != 2 {
		t.Fatalf("versions = %v", v)
	}
}

func TestAgentSpansQuery(t *testing.T) {
	e := newEnv(t)
	seedAgentData(t, e, time.Now().Add(-time.Hour))
	eq := func(f string, v any) map[string]any {
		return map[string]any{"$eq": []any{map[string]any{"$getField": f}, map[string]any{"$literal": v}}}
	}
	res := e.post("/agents/spans/query", map[string]any{"project_id": pid, "query": map[string]any{"$expr": eq("operation_name", "execute_tool")},
		"include_details": true, "sort_by": []any{map[string]any{"field": "started_at", "direction": "asc"}}})
	spans := res["spans"].([]any)
	if res["total_count"].(float64) != 4 || len(spans) != 4 {
		t.Fatalf("tool spans = %v", res["total_count"])
	}
	names := []string{}
	for _, s := range spans {
		names = append(names, s.(map[string]any)["tool_name"].(string))
	}
	if strings.Join(names, ",") != "get_weather,get_weather,rhyme,arxiv_search" {
		t.Fatalf("tool names = %v", names)
	}
	first := spans[0].(map[string]any)
	if first["tool_call_arguments"] != `{"city":"Paris"}` || !strings.Contains(first["tool_call_result"].(string), "sunny") ||
		first["agent_name"] != "travel-agent" || first["conversation_id"] != "conv-1" || first["status_code"] != "OK" {
		t.Fatalf("tool span = %v", first)
	}
	if spans[1].(map[string]any)["status_code"] != "ERROR" || spans[1].(map[string]any)["status_message"] != "upstream timeout" {
		t.Fatalf("errored tool span = %v", spans[1])
	}

	// LLM spans from all three conventions, with models and messages.
	res = e.post("/agents/spans/query", map[string]any{"project_id": pid, "include_details": true, "include_costs": true,
		"query": map[string]any{"$expr": eq("operation_name", "chat")}})
	if res["total_count"].(float64) != 7 {
		t.Fatalf("chat spans = %v", res["total_count"])
	}
	for _, s := range res["spans"].([]any) {
		m := s.(map[string]any)
		if m["request_model"] == nil || m["input_tokens"] == nil {
			t.Fatalf("chat span missing model/tokens: %v", m)
		}
	}

	// Grouped by tool for one agent, with a filtered measure.
	res = e.post("/agents/spans/query", map[string]any{"project_id": pid,
		"query":    map[string]any{"$expr": map[string]any{"$and": []any{eq("agent_name", "travel-agent"), eq("operation_name", "execute_tool")}}},
		"group_by": []any{map[string]any{"key": "tool_name", "source": "field"}},
		"measures": []any{map[string]any{"alias": "failures", "aggregation": "count", "filter": map[string]any{"$expr": eq("is_error", true)}},
			map[string]any{"alias": "avg_ms", "aggregation": "avg", "value": map[string]any{"key": "duration_ms"}}}})
	groups := res["groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("groups = %v", res)
	}
	g := groups[0].(map[string]any)
	if g["group_keys"].(map[string]any)["tool_name"] != "get_weather" || g["span_count"].(float64) != 2 || g["error_count"].(float64) != 1 ||
		g["metrics"].(map[string]any)["failures"].(float64) != 1 || g["metrics"].(map[string]any)["avg_ms"].(float64) != 150 {
		t.Fatalf("tool group = %v", g)
	}

	// Conversations as groups carry first/last message previews.
	res = e.post("/agents/spans/query", map[string]any{"project_id": pid, "include_costs": true,
		"group_by": []any{map[string]any{"key": "conversation.id", "alias": "conversation"}}, "sort_by": []any{map[string]any{"field": "first_seen"}}})
	groups = res["groups"].([]any)
	if len(groups) != 3 {
		t.Fatalf("conversation groups = %d %v", len(groups), res)
	}
	c := groups[0].(map[string]any)
	if c["group_keys"].(map[string]any)["conversation"] != "conv-1" || c["invocation_count"].(float64) != 2 ||
		c["first_message"].(map[string]any)["text"] != "What's the weather in Paris?" ||
		c["last_message"].(map[string]any)["text"] != "Tomorrow looks rainy." || c["total_cost_usd"] == nil {
		t.Fatalf("conversation group = %v", c)
	}
	if vg := groups[1].(map[string]any); vg["group_keys"].(map[string]any)["conversation"] != "vercel-session" {
		t.Fatalf("vercel session group = %v", vg)
	}

	// Custom attributes: schema, filter and typed maps.
	sch := e.post("/agents/spans/custom-attrs/schema", map[string]any{"project_id": pid})
	keys := map[string]string{}
	for _, a := range sch["attributes"].([]any) {
		m := a.(map[string]any)
		keys[m["key"].(string)] = m["source"].(string)
	}
	if keys["tenant"] != "custom_attrs_string" || keys["priority"] != "custom_attrs_int" || keys["metadata.team"] != "custom_attrs_string" {
		t.Fatalf("custom attrs = %v", sch)
	}
	if _, reserved := keys["gen_ai.agent.name"]; reserved {
		t.Fatal("reserved attribute listed as custom")
	}
	res = e.post("/agents/spans/query", map[string]any{"project_id": pid, "query": map[string]any{"$expr": eq("custom_attrs_string.tenant", "acme")}})
	if res["total_count"].(float64) != 2 || res["spans"].([]any)[0].(map[string]any)["custom_attrs_string"].(map[string]any)["tenant"] != "acme" {
		t.Fatalf("custom attr filter = %v", res)
	}
}

func TestAgentSpanStats(t *testing.T) {
	e := newEnv(t)
	base := time.Now().Add(-time.Hour)
	seedAgentData(t, e, base)
	res := e.post("/agents/spans/stats", map[string]any{"project_id": pid, "start": base.Add(-time.Minute).Format(time.RFC3339),
		"end": base.Add(10 * time.Minute).Format(time.RFC3339), "granularity": 60,
		"group_by": []any{map[string]any{"key": "agent_name"}},
		"metrics": []any{
			map[string]any{"alias": "latency", "value": map[string]any{"key": "duration_ms"}, "value_type": "number", "percentiles": []any{50, 95}},
			map[string]any{"alias": "in_tok", "value": map[string]any{"key": "input_tokens"}, "value_type": "number", "aggregations": []any{"sum"}},
			map[string]any{"alias": "err", "value": map[string]any{"key": "is_error"}, "value_type": "boolean", "aggregations": []any{"count_true"}},
		}})
	if res["bucket_type"] != "time" || res["granularity"].(float64) != 60 {
		t.Fatalf("stats header = %v", res)
	}
	cols := map[string]string{}
	for _, c := range res["columns"].([]any) {
		m := c.(map[string]any)
		cols[m["name"].(string)] = m["role"].(string)
	}
	if cols["timestamp"] != "time" || cols["agent_name"] != "group" || cols["latency_p95"] != "metric" || cols["in_tok_sum"] != "metric" {
		t.Fatalf("columns = %v", cols)
	}
	var travelTok, travelErr float64
	for _, r := range res["rows"].([]any) {
		m := r.(map[string]any)
		if m["agent_name"] == "travel-agent" {
			travelTok += m["in_tok_sum"].(float64)
			travelErr += m["err_count_true"].(float64)
		}
	}
	if travelTok != 700 || travelErr != 1 {
		t.Fatalf("travel-agent stats rows: tokens %v errors %v (%v)", travelTok, travelErr, res["rows"])
	}
	// Numeric buckets: a latency histogram.
	res = e.post("/agents/spans/stats", map[string]any{"project_id": pid, "start": base.Add(-time.Minute).Format(time.RFC3339),
		"bucket_by": map[string]any{"type": "number", "alias": "latency", "bins": 4, "value": map[string]any{"key": "duration_ms"}}})
	var total float64
	for _, r := range res["rows"].([]any) {
		total += r.(map[string]any)["count"].(float64)
	}
	if res["bucket_type"] != "number" || len(res["rows"].([]any)) != 4 || total != 15 {
		t.Fatalf("histogram = %v", res)
	}
	if code, _ := e.raw("/agents/spans/stats", map[string]any{"project_id": pid}, nil); code != 400 {
		t.Fatalf("missing start accepted: %d", code)
	}
}

func chatTypes(turn map[string]any) string {
	var ts []string
	for _, m := range turn["messages"].([]any) {
		ts = append(ts, m.(map[string]any)["type"].(string))
	}
	return strings.Join(ts, ",")
}

func TestAgentConversationsAndChat(t *testing.T) {
	e := newEnv(t)
	seedAgentData(t, e, time.Now().Add(-time.Hour))
	res := e.post("/agents/conversations/chat", map[string]any{"project_id": pid, "conversation_id": "conv-1", "include_feedback": true})
	turns := res["turns"].([]any)
	if res["total_turns"].(float64) != 2 || len(turns) != 2 || res["has_more"] != false || res["total_cost_usd"] == nil {
		t.Fatalf("conversation = %v", res)
	}
	t1 := turns[0].(map[string]any)
	if got := chatTypes(t1); got != "user_message,assistant_message,tool_call,assistant_message" {
		t.Fatalf("turn 1 timeline = %s", got)
	}
	msgs := t1["messages"].([]any)
	if msgs[0].(map[string]any)["user_message"].(map[string]any)["text"] != "What's the weather in Paris?" {
		t.Fatalf("user message = %v", msgs[0])
	}
	if req := msgs[1].(map[string]any)["assistant_message"].(map[string]any); req["text"] != "" || req["requested_tools"].([]any)[0] != "get_weather" {
		t.Fatalf("tool request = %v", req)
	}
	tc := msgs[2].(map[string]any)["tool_call"].(map[string]any)
	if tc["tool_name"] != "get_weather" || tc["tool_arguments"] != `{"city":"Paris"}` || tc["duration_ms"].(float64) != 200 {
		t.Fatalf("tool call = %v", tc)
	}
	am := msgs[3].(map[string]any)["assistant_message"].(map[string]any)
	if am["text"] != "It is 18°C and sunny in Paris." || am["model"] != "gpt-4o" || am["input_tokens"].(float64) != 160 {
		t.Fatalf("assistant = %v", am)
	}
	if t1["agent_name"] != "travel-agent" || t1["total_input_tokens"].(float64) != 280 || t1["total_duration_ms"].(float64) != 1200 ||
		t1["status_code"] != "OK" || t1["feedback"] == nil {
		t.Fatalf("turn 1 = %v", t1)
	}
	t2 := turns[1].(map[string]any)
	if got := chatTypes(t2); got != "user_message,assistant_message,tool_call,assistant_message" {
		t.Fatalf("turn 2 timeline = %s", got)
	}
	if t2["messages"].([]any)[2].(map[string]any)["status_code"] != "ERROR" {
		t.Fatalf("failed tool not marked: %v", t2["messages"])
	}
	// Paging: offset skips the most recent turns.
	res = e.post("/agents/conversations/chat", map[string]any{"project_id": pid, "conversation_id": "conv-1", "limit": 1})
	if len(res["turns"].([]any)) != 1 || res["has_more"] != true ||
		res["turns"].([]any)[0].(map[string]any)["messages"].([]any)[0].(map[string]any)["user_message"].(map[string]any)["text"] != "And tomorrow in Paris?" {
		t.Fatalf("paged conversation = %v", res)
	}

	// Vercel trace as a chat; the model-requested tool call is only shown on request.
	vt := hexID("tv", 32)
	tr := e.post("/agents/traces/chat", map[string]any{"project_id": pid, "trace_id": vt})
	if got := chatTypes(tr); got != "user_message,assistant_message,tool_call,assistant_message" {
		t.Fatalf("vercel timeline = %s (%v)", got, tr["messages"])
	}
	if tr["messages"].([]any)[0].(map[string]any)["user_message"].(map[string]any)["text"] != "Write a haiku about the sea" ||
		tr["total_input_tokens"].(float64) != 80 || tr["provider"] != "openai" {
		t.Fatalf("vercel chat = %v", tr)
	}
	// OpenInference trace: LangChain input messages are normalised.
	oi := e.post("/agents/traces/chat", map[string]any{"project_id": pid, "trace_id": hexID("toi", 32)})
	if got := chatTypes(oi); got != "user_message,assistant_message,tool_call" {
		t.Fatalf("openinference timeline = %s", got)
	}
	if oi["messages"].([]any)[0].(map[string]any)["user_message"].(map[string]any)["text"] != "Find papers on RAG" {
		t.Fatalf("openinference user = %v", oi["messages"])
	}
	if code, _ := e.raw("/agents/traces/chat", map[string]any{"project_id": pid, "trace_id": "missing"}, nil); code != 404 {
		t.Fatalf("missing trace: %d", code)
	}

	// Span minimap for conversations.
	cs := e.post("/agents/conversations/spans", map[string]any{"project_id": pid, "conversation_ids": []any{"conv-1", "nope"}})
	convs := cs["conversations"].([]any)
	if len(convs) != 2 || len(convs[0].(map[string]any)["spans"].([]any)) != 8 || len(convs[1].(map[string]any)["spans"].([]any)) != 0 {
		t.Fatalf("conversation spans = %v", cs)
	}
	sp0 := convs[0].(map[string]any)["spans"].([]any)[0].(map[string]any)
	if sp0["operation_name"] != "invoke_agent" || sp0["status"] != "OK" {
		t.Fatalf("first span = %v", sp0)
	}
}

func TestAgentConversationFeedbackMarkers(t *testing.T) {
	e := newEnv(t)
	seedAgentData(t, e, time.Now().Add(-time.Hour))
	ref := spanRef(hexID("a1", 16))
	e.feedback(map[string]any{"ref": ref, "feedback_type": "weft.reaction", "payload": map[string]any{"emoji": "👍"}})
	// A ref with the older kind name ("call") is read as a span ref.
	e.feedback(map[string]any{"ref": "weft:///" + pid + "/call/" + hexID("a1", 16), "feedback_type": "weft.scorer.helpful", "payload": map[string]any{"output": 0.8}})
	cs := e.post("/agents/conversations/spans", map[string]any{"project_id": pid, "conversation_ids": []any{"conv-1"}})
	fb := cs["conversations"].([]any)[0].(map[string]any)["spans_feedback"].([]any)
	if len(fb) != 2 {
		t.Fatalf("feedback markers = %v", fb)
	}
	types := map[string]map[string]any{}
	for _, f := range fb {
		m := f.(map[string]any)
		types[m["feedback_type"].(string)] = m
	}
	if types["weft.user_feedback"]["tags"].([]any)[0] != "👍" || types["weft.signal"]["ratings"].([]any)[0].(map[string]any)["value"].(float64) != 0.8 ||
		types["weft.signal"]["trace_id"] != hexID("t1", 32) {
		t.Fatalf("feedback markers = %v", fb)
	}
	chat := e.post("/agents/conversations/chat", map[string]any{"project_id": pid, "conversation_id": "conv-1", "include_feedback": true})
	t1 := chat["turns"].([]any)[0].(map[string]any)
	if len(t1["feedback"].([]any)) != 2 || t1["feedback"].([]any)[1].(map[string]any)["scorer_ratings"].(map[string]any)["helpful"].(float64) != 0.8 {
		t.Fatalf("turn feedback = %v", t1["feedback"])
	}
}

func TestAgentSearch(t *testing.T) {
	e := newEnv(t)
	seedAgentData(t, e, time.Now().Add(-time.Hour))
	res := e.post("/agents/search", map[string]any{"project_id": pid, "query": "paris"})
	results := res["results"].([]any)
	if res["total_conversations"].(float64) != 1 || len(results) != 1 {
		t.Fatalf("search = %v", res)
	}
	r := results[0].(map[string]any)
	if r["conversation_id"] != "conv-1" || r["agent_name"] != "travel-agent" {
		t.Fatalf("result = %v", r)
	}
	// The same user message recurs in several spans' inputs; it is reported once.
	seen := map[string]int{}
	for _, m := range r["matched_messages"].([]any) {
		mm := m.(map[string]any)
		seen[mm["content_preview"].(string)]++
		if mm["content_digest"] == "" || mm["span_id"] == "" {
			t.Fatalf("message = %v", mm)
		}
	}
	if seen["What's the weather in Paris?"] != 1 || seen["It is 18°C and sunny in Paris."] != 1 {
		t.Fatalf("matched = %v", seen)
	}
	res = e.post("/agents/search", map[string]any{"project_id": pid, "query": "paris", "roles": []any{"assistant"}})
	if n := len(res["results"].([]any)[0].(map[string]any)["matched_messages"].([]any)); n != 1 {
		t.Fatalf("role filter: %d", n)
	}
	res = e.post("/agents/search", map[string]any{"project_id": pid, "query": "nothing-matches-this"})
	if len(res["results"].([]any)) != 0 {
		t.Fatalf("unexpected results %v", res)
	}
}

// SDK calls become agent spans through weft.span_kind and thread/turn ids.
func TestAgentsFromSDKCalls(t *testing.T) {
	e := newEnv(t)
	now := time.Now().UTC()
	ts := func(ms int) string { return now.Add(time.Duration(ms) * time.Millisecond).Format(time.RFC3339Nano) }
	thread := "thread-sdk"
	start := func(id, parent, op string, ms int, attrs, inputs map[string]any) map[string]any {
		s := map[string]any{"project_id": pid, "id": id, "trace_id": "tr-" + id[:2], "name": op, "started_at": ts(ms), "attributes": attrs, "inputs": inputs,
			"session_id": thread}
		if parent != "" {
			s["parent_id"] = parent
			s["trace_id"] = "tr-" + parent[:2]
		} else {
			s["turn_id"] = id
		}
		return map[string]any{"start": s}
	}
	end := func(id string, ms int, out any, usage map[string]any) map[string]any {
		en := map[string]any{"project_id": pid, "id": id, "ended_at": ts(ms), "output": out, "summary": map[string]any{}}
		if usage != nil {
			en["summary"] = map[string]any{"usage": usage}
		}
		return map[string]any{"end": en}
	}
	e.post("/api/v1/spans/batch", map[string]any{"items": []any{
		start("t1-root", "", "support_bot", 0, map[string]any{"weft": map[string]any{"span_kind": "agent", "color": "purple"}, "gen_ai.agent.version": "1.2"}, map[string]any{"question": "Reset my password"}),
		start("t1-llm", "t1-root", "openai.chat.completions.create", 10, map[string]any{},
			map[string]any{"model": "gpt-4o-mini", "messages": []any{map[string]any{"role": "user", "content": "Reset my password"}}}),
		end("t1-llm", 300, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "Use the reset link."}}}},
			map[string]any{"gpt-4o-mini": map[string]any{"prompt_tokens": 20, "completion_tokens": 5, "requests": 1}}),
		start("t1-tool", "t1-root", "lookup_user", 310, map[string]any{"weft": map[string]any{"span_kind": "tool"}}, map[string]any{"email": "a@b.c"}),
		end("t1-tool", 320, map[string]any{"found": true}, nil),
		end("t1-root", 400, "Use the reset link.", nil),
	}})
	res := e.post("/agents/query", map[string]any{"project_id": pid})
	a := findAgent(res["agents"].([]any), "support_bot")
	if a == nil || a["span_count"].(float64) != 3 || a["invocation_count"].(float64) != 1 || a["total_input_tokens"].(float64) != 20 {
		t.Fatalf("sdk agent = %v", res)
	}
	tools := e.post("/agents/spans/query", map[string]any{"project_id": pid, "group_by": []any{map[string]any{"key": "operation_name"}}})
	ops := map[string]float64{}
	for _, g := range tools["groups"].([]any) {
		m := g.(map[string]any)
		ops[fmt.Sprint(m["group_keys"].(map[string]any)["operation_name"])] = m["span_count"].(float64)
	}
	if ops["invoke_agent"] != 1 || ops["chat"] != 1 || ops["execute_tool"] != 1 {
		t.Fatalf("operations = %v", ops)
	}
	chat := e.post("/agents/conversations/chat", map[string]any{"project_id": pid, "conversation_id": thread})
	turn := chat["turns"].([]any)[0].(map[string]any)
	if got := chatTypes(turn); got != "user_message,assistant_message,tool_call" {
		t.Fatalf("sdk timeline = %s", got)
	}
	if turn["messages"].([]any)[0].(map[string]any)["user_message"].(map[string]any)["text"] != "Reset my password" || turn["agent_version"] != "1.2" {
		t.Fatalf("sdk turn = %v", turn)
	}
	// Deleting the turn removes it from agent views.
	e.post("/api/v1/spans/delete", map[string]any{"project_id": pid, "ids": []any{"t1-root"}})
	res = e.post("/agents/query", map[string]any{"project_id": pid})
	if findAgent(res["agents"].([]any), "support_bot") != nil {
		t.Fatalf("deleted agent still listed: %v", res)
	}
}

// Spans emitted inside an op carry a link to it (weft.parent_call.id /
// .trace_id, as the Weft SDKs' TypeSafe integration stamps them); agent
// spans expose it as parent_call_id / parent_call_trace_id.
func TestAgentSpansParentCallLink(t *testing.T) {
	e := newEnv(t)
	base := time.Now().Add(-time.Minute)
	tr := hexID("lnk", 32)
	body := otlpBody(base, []testSpan{
		{trace: tr, id: hexID("t1", 16), name: "invoke_agent typesafe SDK", durMS: 50, attrs: map[string]any{
			"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "typesafe SDK",
			"weft.parent_call.trace_id": "op-trace-1", "weft.parent_call.id": "op-call-1"}},
		{trace: tr, id: hexID("c1", 16), parent: hexID("t1", 16), name: "chat jev-latest", startMS: 5, durMS: 40, attrs: map[string]any{
			"gen_ai.operation.name": "chat", "gen_ai.provider.name": "typesafe", "gen_ai.request.model": "jev-latest",
			"weft.parent_call.trace_id": "op-trace-1", "weft.parent_call.id": "op-call-1"}},
		{trace: hexID("other", 32), id: hexID("t2", 16), name: "invoke_agent other", durMS: 10, attrs: map[string]any{
			"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "other"}},
	})
	if code, out := e.raw("/otel/v1/traces", body, map[string]string{"project_id": pid}); code != 200 {
		t.Fatalf("otlp -> %d %s", code, out)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	query := func(field, v string) []any {
		res := e.post("/agents/spans/query", map[string]any{"project_id": pid,
			"query": map[string]any{"$expr": map[string]any{"$eq": []any{map[string]any{"$getField": field}, map[string]any{"$literal": v}}}}})
		return res["spans"].([]any)
	}
	spans := query("parent_call_id", "op-call-1")
	if len(spans) != 2 {
		t.Fatalf("spans of op-call-1 = %v", spans)
	}
	for _, s := range spans {
		m := s.(map[string]any)
		if m["parent_call_trace_id"] != "op-trace-1" || m["trace_id"] == "op-trace-1" {
			t.Fatalf("span = %v", m)
		}
	}
	// An SDK-recorded span (nested attributes), as the Weft SDKs send it.
	e.startSpan(map[string]any{"id": "sdk-llm-weft", "trace_id": "sdk-trace-3",
		"name": "chat", "started_at": now, "inputs": map[string]any{},
		"attributes": map[string]any{"weft": map[string]any{"span_kind": "llm", "parent_call": map[string]any{"trace_id": "op-trace-3", "id": "op-call-3"}}}})
	if s := query("weft.parent_call.id", "op-call-3"); len(s) != 1 || s[0].(map[string]any)["span_id"] != "sdk-llm-weft" ||
		s[0].(map[string]any)["parent_call_trace_id"] != "op-trace-3" {
		t.Fatalf("weft.parent_call link = %v", s)
	}
	// Unlinked spans have null link fields.
	res := e.post("/agents/spans/query", map[string]any{"project_id": pid, "query": map[string]any{"$expr": map[string]any{
		"$eq": []any{map[string]any{"$getField": "agent_name"}, map[string]any{"$literal": "other"}}}}})
	if s := res["spans"].([]any); len(s) != 1 || s[0].(map[string]any)["parent_call_id"] != nil {
		t.Fatalf("unlinked = %v", s)
	}
}
