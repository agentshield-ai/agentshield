package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolve(t *testing.T) {
	c := New(func(context.Context, string) (string, bool) { return "", false }, nil)
	cases := map[string]string{
		"gpt-4o-mini": "openai", "o3-mini": "openai", "claude-sonnet-4-5": "anthropic", "anthropic/claude-x": "anthropic",
		"gemini-2.5-flash": "openai", "mock/echo": "mock", "groq/llama-3.1-8b": "openai",
	}
	for model, kind := range cases {
		rt, err := c.Resolve(context.Background(), "e/p", model)
		if err != nil || rt.Kind != kind {
			t.Errorf("%s -> %+v, %v; want %s", model, rt, err, kind)
		}
	}
	if _, err := c.Resolve(context.Background(), "e/p", "who-knows"); err == nil {
		t.Error("expected error for unroutable model")
	}
}

func TestAnthropicConversion(t *testing.T) {
	body := toAnthropic("claude-x", map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "be brief"},
			map[string]any{"role": "user", "content": "weather?"},
			map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "t1", "type": "function",
				"function": map[string]any{"name": "get_weather", "arguments": `{"city":"Oslo"}`}}}},
			map[string]any{"role": "tool", "tool_call_id": "t1", "content": "sunny"},
		},
		"tools":           []any{map[string]any{"type": "function", "function": map[string]any{"name": "get_weather", "parameters": map[string]any{"type": "object"}}}},
		"response_format": map[string]any{"type": "json_object"},
		"stop":            []any{"END"},
	})
	if body["system"] != "be brief\n\nRespond with a single JSON object and nothing else." {
		t.Fatalf("system = %q", body["system"])
	}
	msgs := body["messages"].([]map[string]any)
	if len(msgs) != 3 || msgs[1]["role"] != "assistant" || msgs[2]["role"] != "user" {
		t.Fatalf("messages = %v", msgs)
	}
	tu := msgs[1]["content"].([]any)[0].(map[string]any)
	if tu["type"] != "tool_use" || tu["input"].(map[string]any)["city"] != "Oslo" {
		t.Fatalf("tool_use = %v", tu)
	}
	if body["tools"].([]any)[0].(map[string]any)["input_schema"] == nil || body["stop_sequences"] == nil {
		t.Fatalf("tools/stop missing: %v", body)
	}
	out := fromAnthropic(map[string]any{"id": "m1", "model": "claude-x", "stop_reason": "tool_use",
		"content": []any{map[string]any{"type": "text", "text": "ok"}, map[string]any{"type": "tool_use", "id": "t2", "name": "f", "input": map[string]any{"a": 1}}},
		"usage":   map[string]any{"input_tokens": 5.0, "output_tokens": 7.0}})
	ch := out["choices"].([]any)[0].(map[string]any)
	if ch["finish_reason"] != "tool_calls" || out["usage"].(map[string]any)["total_tokens"] != 12 {
		t.Fatalf("converted = %v", out)
	}
}

func TestOpenAIStreamAccumulates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != true {
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`)
			return
		}
		for _, d := range []string{
			`{"id":"c1","choices":[{"index":0,"delta":{"content":"Hel"}}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{"content":"lo"}}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"t","function":{"name":"f","arguments":"{\"a\""}}]}}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":1}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", d)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	secrets := map[string]string{"OPENAI_API_KEY": "k", "OPENAI_BASE_URL": srv.URL}
	c := New(func(_ context.Context, n string) (string, bool) { v, ok := secrets[n]; return v, ok }, nil)
	n := 0
	final, err := c.Stream(context.Background(), "e/p", map[string]any{"model": "gpt-4o", "messages": []any{}}, func(map[string]any) error { n++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	msg := final["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	tc := msg["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if msg["content"] != "Hello" || tc["arguments"] != `{"a":1}` || n != 5 || final["usage"] == nil {
		t.Fatalf("final = %v (chunks %d)", final, n)
	}
	secrets["OPENAI_API_KEY"] = "wrong"
	if _, err := c.Complete(context.Background(), "e/p", map[string]any{"model": "gpt-4o"}); err == nil || err.Error() != "provider returned 401: bad key" {
		t.Fatalf("expected provider error, got %v", err)
	}
}

// Anthropic's input_tokens excludes cache reads and writes; the OpenAI-shaped
// usage Weft records must carry the gross prompt and the cache split.
func TestAnthropicCacheUsage(t *testing.T) {
	out := fromAnthropic(map[string]any{"id": "m1", "model": "claude-sonnet-4-5", "stop_reason": "end_turn",
		"content": []any{map[string]any{"type": "text", "text": "hi"}},
		"usage":   map[string]any{"input_tokens": 1000.0, "output_tokens": 100.0, "cache_read_input_tokens": 9000.0, "cache_creation_input_tokens": 50.0}})
	u := out["usage"].(map[string]any)
	d, _ := u["prompt_tokens_details"].(map[string]any)
	if u["prompt_tokens"] != 10050 || u["total_tokens"] != 10150 || d["cached_tokens"] != 9000 || d["cache_write_tokens"] != 50 {
		t.Fatalf("usage = %v", u)
	}
	plain := fromAnthropic(map[string]any{"content": []any{}, "usage": map[string]any{"input_tokens": 5.0, "output_tokens": 1.0}})["usage"].(map[string]any)
	if _, ok := plain["prompt_tokens_details"]; ok || plain["prompt_tokens"] != 5 {
		t.Fatalf("plain usage = %v", plain)
	}
}
