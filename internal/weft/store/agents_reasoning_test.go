package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func jsonValue(t *testing.T, s string) any {
	t.Helper()
	var v any
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestExtractReasoningSources(t *testing.T) {
	cases := []struct {
		name    string
		model   bool
		attrs   string
		inputs  string
		output  string
		summary string
		want    []string // kind:source:text[:tokens]
	}{
		{"anthropic thinking and redacted blocks", true, `{}`, `{}`,
			`{"type":"message","role":"assistant","content":[{"type":"thinking","thinking":"I should search first","signature":"sig"},
			  {"type":"redacted_thinking","data":"Enc0ded"},{"type":"tool_use","id":"tu1","name":"search","input":{"q":"x"}}]}`, `{}`,
			[]string{"thinking:output:I should search first", "redacted:output:"}},
		{"anthropic history in the input", true, `{}`,
			`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"thinking","thinking":"earlier thought"},{"type":"text","text":"hello"}]}]}`,
			`{"role":"assistant","content":[{"type":"text","text":"done"}]}`, `{}`,
			[]string{"thinking:input:earlier thought"}},
		{"openai responses summaries with reasoning tokens", true, `{}`, `{}`,
			`{"output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Compare the two plans."},{"type":"summary_text","text":"Pick the cheaper one."}]},
			  {"type":"message","role":"assistant","content":[{"type":"output_text","text":"Plan B"}]}],
			  "usage":{"input_tokens":10,"output_tokens":300,"output_tokens_details":{"reasoning_tokens":256}}}`, `{}`,
			[]string{"summary:output:Compare the two plans.:256", "summary:output:Pick the cheaper one."}},
		{"openai responses encrypted reasoning only", true, `{}`, `{}`,
			`{"output":[{"type":"reasoning","id":"rs_2","summary":[],"encrypted_content":"gAAAA"}]}`,
			`{"usage":{"o3":{"prompt_tokens":5,"completion_tokens":50,"reasoning_tokens":40}}}`,
			[]string{"redacted:output::40"}},
		{"openai responses reasoning items sent back as input", true, `{}`,
			`{"input":[{"role":"user","content":"go"},{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"prior summary"}]}]}`,
			`{"output":[]}`, `{}`, []string{"summary:input:prior summary"}},
		{"gemini thought parts", true, `{}`, `{}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"Let me think about units.","thought":true},{"text":"42 km"}]}}]}`, `{}`,
			[]string{"thinking:output:Let me think about units."}},
		{"semconv reasoning part as mapped by OTLP ingest", true, `{}`, `{}`,
			`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"thinking","thinking":"semconv reasoning"},{"type":"text","text":"ok"}]}}]}`,
			`{}`, []string{"thinking:output:semconv reasoning"}},
		{"raw semconv reasoning part", true, `{}`, `{}`,
			`[{"role":"assistant","parts":[{"type":"reasoning","content":"raw part"},{"type":"text","content":"x"}]}]`, `{}`,
			[]string{"thinking:output:raw part"}},
		{"reasoning_content on chat messages", true, `{}`, `{}`,
			`{"choices":[{"message":{"role":"assistant","content":"4","reasoning_content":"2+2 is 4"}}]}`, `{}`,
			[]string{"thinking:output:2+2 is 4"}},
		{"weft.reasoning span events", false,
			`{"otel":{"events":[{"name":"weft.reasoning","time":"2026-05-01T12:00:00.5Z","attributes":{"weft.reasoning.text":"check the cache","weft.reasoning.kind":"summary","weft.reasoning.tokens":12}},
			  {"name":"exception","attributes":{}}]}}`, `{}`,
			`{"choices":[{"message":{"role":"assistant","content":[{"type":"thinking","thinking":"ignored: not a model span"}]}}]}`, `{}`,
			[]string{"summary:event:check the cache:12"}},
		{"weft.reasoning SDK attribute", false, `{"weft":{"reasoning":[{"text":"sdk step","kind":"thinking"},"plain string step"]}}`, `{}`, `null`, `{}`,
			[]string{"thinking:event:sdk step", "thinking:event:plain string step"}},
		{"no reasoning", true, `{}`, `{"messages":[{"role":"user","content":"hi"}]}`, `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`, `{}`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			attrs, _ := jsonValue(t, c.attrs).(map[string]any)
			inputs, _ := jsonValue(t, c.inputs).(map[string]any)
			summary, _ := jsonValue(t, c.summary).(map[string]any)
			got := extractReasoning(c.model, attrs, inputs, jsonValue(t, c.output), summary)
			var keys []string
			for _, p := range got {
				k := p.Kind + ":" + p.Source + ":" + p.Text
				if p.Tokens > 0 {
					k += ":" + jsonInt64(p.Tokens)
				}
				keys = append(keys, k)
				if p.Hash == "" {
					t.Fatal("missing hash")
				}
			}
			if strings.Join(keys, "|") != strings.Join(c.want, "|") {
				t.Fatalf("got %q\nwant %q", keys, c.want)
			}
			if c.name == "weft.reasoning span events" && got[0].AtUS == 0 {
				t.Fatal("event time not kept")
			}
		})
	}
}

func jsonInt64(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// reasoningSession: agent A thinks, calls a tool (linked by tool call id),
// thinks again and delegates to agent B; the second model call re-sends the
// first thinking block in its history.
func reasoningSession() []gs {
	thinking1 := map[string]any{"role": "assistant", "content": []any{
		map[string]any{"type": "thinking", "thinking": "I need the weather first."},
		map[string]any{"type": "tool_use", "id": "tu_1", "name": "get_weather", "input": map[string]any{"city": "Oslo"}}}}
	return []gs{
		{id: "A", name: "invoke_agent planner", start: 0, dur: 2000, attrs: agentAttrs("planner", "rs1"),
			events: []any{map[string]any{"name": "weft.reasoning", "time": graphBase.Add(5_000_000).Format("2006-01-02T15:04:05.000000Z"),
				"attributes": map[string]any{"weft.reasoning.text": "Plan: weather, then a writer."}}}},
		{id: "c1", parent: "A", name: "chat claude", start: 10, dur: 100, attrs: map[string]any{"gen_ai.operation.name": "chat"},
			inputs: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "Write about Oslo weather"}}}, output: thinking1},
		{id: "t1", parent: "A", name: "execute_tool get_weather", start: 120, dur: 50, attrs: map[string]any{"gen_ai.operation.name": "execute_tool",
			"gen_ai.tool.name": "get_weather", "gen_ai.tool.call.id": "tu_1"}},
		{id: "c2", parent: "A", name: "chat claude", start: 200, dur: 100, attrs: map[string]any{"gen_ai.operation.name": "chat"},
			inputs: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "Write about Oslo weather"}, thinking1,
				map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": "rain"}}}}},
			output: map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "thinking", "thinking": "A writer should take it from here."},
				map[string]any{"type": "text", "text": "Handing over."}}}},
		{id: "B", parent: "A", name: "invoke_agent writer", start: 400, dur: 500, attrs: agentAttrs("writer", "")},
		{id: "c3", parent: "B", name: "chat claude", start: 410, dur: 100, attrs: map[string]any{"gen_ai.operation.name": "chat"},
			output: map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "thinking", "thinking": "Short and vivid."},
				map[string]any{"type": "text", "text": "Oslo is wet."}}}},
	}
}

func TestReasoningStepsLinkToNextAction(t *testing.T) {
	for oi, o := range ingestOrders {
		t.Run(o.name, func(t *testing.T) {
			st := openTestStore(t)
			pid := "acme/r" + string(rune('a'+oi))
			o.write(t, st, pid, reasoningSession())
			steps, total, err := st.SessionReasoning(context.Background(), SessionReasoningQuery{ProjectID: pid, SessionID: "rs1"})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, s := range steps {
				next := ""
				if s.NextSpanID != nil {
					next = *s.NextSpanID
				}
				got = append(got, s.AgentName+"|"+s.Source+"|"+s.Text+"|"+next)
			}
			want := []string{
				"planner|event|Plan: weather, then a writer.|t1",
				"planner|output|I need the weather first.|t1",
				"planner|output|A writer should take it from here.|B",
				"writer|output|Short and vivid.|",
			}
			if total != 4 || strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("total %d\n%s", total, strings.Join(got, "\n"))
			}
			for i, s := range steps {
				if s.Index != i || s.SessionID != "rs1" || s.StartedAt == nil {
					t.Fatalf("step %d: %+v", i, s)
				}
			}
			all, n, _ := st.SessionReasoning(context.Background(), SessionReasoningQuery{ProjectID: pid, SessionID: "rs1", IncludeDuplicates: true})
			if n != 5 {
				t.Fatalf("with duplicates: %d", n)
			}
			dup := 0
			for _, s := range all {
				if s.Duplicate {
					dup++
					if s.Source != ReasoningFromInput || s.Index != -1 {
						t.Fatalf("duplicate %+v", s)
					}
				}
			}
			if dup != 1 {
				t.Fatalf("duplicates %d", dup)
			}
			if w, _, _ := st.SessionReasoning(context.Background(), SessionReasoningQuery{ProjectID: pid, SessionID: "rs1", Agent: "writer"}); len(w) != 1 {
				t.Fatalf("agent filter: %d", len(w))
			}
		})
	}
}
