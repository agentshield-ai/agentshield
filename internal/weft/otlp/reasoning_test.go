package otlp

import (
	"encoding/json"
	"testing"
)

// thinkingParts returns the thinking texts in the first choice of a mapped output.
func thinkingParts(t *testing.T, out any) (thinking []string, text string) {
	t.Helper()
	m, _ := out.(map[string]any)
	cs, _ := m["choices"].([]any)
	if len(cs) == 0 {
		b, _ := json.Marshal(out)
		t.Fatalf("no choices in %s", b)
	}
	msg, _ := cs[0].(map[string]any)["message"].(map[string]any)
	parts, ok := msg["content"].([]any)
	if !ok {
		b, _ := json.Marshal(msg)
		t.Fatalf("content is not a parts list: %s", b)
	}
	for _, p := range parts {
		pm, _ := p.(map[string]any)
		switch pm["type"] {
		case "thinking":
			s, _ := pm["thinking"].(string)
			thinking = append(thinking, s)
		case "text":
			s, _ := pm["text"].(string)
			text += s
		}
	}
	return thinking, text
}

func TestReasoningMapping(t *testing.T) {
	cases := []struct {
		name  string
		attrs map[string]any
		want  string
		text  string
	}{
		{"GenAI semconv reasoning part", map[string]any{"gen_ai.operation.name": "chat",
			"gen_ai.output.messages": `[{"role":"assistant","parts":[{"type":"reasoning","content":"weigh both"},{"type":"text","content":"B"}]}]`},
			"weigh both", "B"},
		{"GenAI semconv thinking part", map[string]any{"gen_ai.operation.name": "chat",
			"gen_ai.output.messages": `[{"role":"assistant","parts":[{"type":"thinking","content":"think more"},{"type":"text","content":"C"}]}]`},
			"think more", "C"},
		{"OpenInference flattened reasoning content", map[string]any{"openinference.span.kind": "LLM",
			"llm.output_messages.0.message.role":                            "assistant",
			"llm.output_messages.0.message.contents.0.message_content.type": "reasoning",
			"llm.output_messages.0.message.contents.0.message_content.text": "check units",
			"llm.output_messages.0.message.contents.1.message_content.type": "text",
			"llm.output_messages.0.message.contents.1.message_content.text": "42 km"},
			"check units", "42 km"},
		{"Vercel AI SDK ai.response.reasoning", map[string]any{"ai.operationId": "ai.generateText.doGenerate",
			"ai.response.text": "Done.", "ai.response.reasoning": "first list the files"}, "first list the files", "Done."},
		{"Google ADK thought part", map[string]any{"gcp.vertex.agent.llm_request": `{"contents":[]}`,
			"gcp.vertex.agent.llm_response": `{"content":{"role":"model","parts":[{"text":"plan the call","thought":true},{"text":"calling"}]}}`},
			"plan the call", "calling"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			call := oneCall(t, "chat", c.attrs)
			th, text := thinkingParts(t, call.End.Output)
			if len(th) != 1 || th[0] != c.want || text != c.text {
				t.Fatalf("thinking %q text %q", th, text)
			}
		})
	}
	// Without reasoning, flattened contents still join as text, in order.
	call := oneCall(t, "chat", map[string]any{"openinference.span.kind": "LLM",
		"llm.output_messages.0.message.role":                            "assistant",
		"llm.output_messages.0.message.contents.1.message_content.text": "second",
		"llm.output_messages.0.message.contents.0.message_content.text": "first"})
	msg := call.End.Output.(map[string]any)["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != "first\nsecond" {
		t.Fatalf("content %#v", msg["content"])
	}
}
