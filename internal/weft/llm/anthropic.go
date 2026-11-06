package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

const anthropicVersion = "2023-06-01"

func anthropicBase() string {
	if v := os.Getenv("ANTHROPIC_BASE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://api.anthropic.com"
}

// toAnthropicContent converts OpenAI message content to Anthropic blocks.
func toAnthropicContent(c any) []any {
	switch t := c.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []any{map[string]any{"type": "text", "text": t}}
	case []any:
		var out []any
		for _, p := range t {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			switch pm["type"] {
			case "text":
				out = append(out, map[string]any{"type": "text", "text": pm["text"]})
			case "image_url":
				iu, _ := pm["image_url"].(map[string]any)
				url, _ := iu["url"].(string)
				if strings.HasPrefix(url, "data:") {
					meta, data, _ := strings.Cut(strings.TrimPrefix(url, "data:"), ",")
					media := strings.TrimSuffix(meta, ";base64")
					out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": media, "data": data}})
				} else if url != "" {
					out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": url}})
				}
			default:
				out = append(out, pm)
			}
		}
		return out
	case nil:
		return nil
	}
	b, _ := json.Marshal(c)
	return []any{map[string]any{"type": "text", "text": string(b)}}
}

// toAnthropic converts OpenAI chat inputs to an Anthropic Messages request.
func toAnthropic(model string, inputs map[string]any) map[string]any {
	var system []string
	var msgs []map[string]any
	appendMsg := func(role string, blocks []any) {
		if len(blocks) == 0 {
			return
		}
		if n := len(msgs); n > 0 && msgs[n-1]["role"] == role {
			msgs[n-1]["content"] = append(msgs[n-1]["content"].([]any), blocks...)
			return
		}
		msgs = append(msgs, map[string]any{"role": role, "content": blocks})
	}
	list, _ := inputs["messages"].([]any)
	for _, m := range list {
		mm, _ := m.(map[string]any)
		role, _ := mm["role"].(string)
		switch role {
		case "system", "developer":
			system = append(system, ContentText(mm["content"]))
		case "assistant":
			blocks := toAnthropicContent(mm["content"])
			tcs, _ := mm["tool_calls"].([]any)
			for _, tc := range tcs {
				tm, _ := tc.(map[string]any)
				fn, _ := tm["function"].(map[string]any)
				var input any = map[string]any{}
				if s, ok := fn["arguments"].(string); ok && s != "" {
					_ = json.Unmarshal([]byte(s), &input)
				}
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": tm["id"], "name": fn["name"], "input": input})
			}
			appendMsg("assistant", blocks)
		case "tool":
			appendMsg("user", []any{map[string]any{"type": "tool_result", "tool_use_id": mm["tool_call_id"], "content": ContentText(mm["content"])}})
		default:
			appendMsg("user", toAnthropicContent(mm["content"]))
		}
	}
	if rf, ok := inputs["response_format"]; ok && rf != nil {
		t := ""
		switch v := rf.(type) {
		case string:
			t = v
		case map[string]any:
			t, _ = v["type"].(string)
		}
		if t == "json_object" || t == "json_schema" {
			system = append(system, "Respond with a single JSON object and nothing else.")
		}
	}
	body := map[string]any{"model": model, "messages": msgs, "max_tokens": 4096}
	if len(system) > 0 {
		body["system"] = strings.Join(system, "\n\n")
	}
	if v, ok := inputs["max_tokens"]; ok && v != nil {
		body["max_tokens"] = v
	}
	for _, k := range []string{"temperature", "top_p", "top_k"} {
		if v, ok := inputs[k]; ok && v != nil {
			body[k] = v
		}
	}
	switch s := inputs["stop"].(type) {
	case string:
		body["stop_sequences"] = []any{s}
	case []any:
		if len(s) > 0 {
			body["stop_sequences"] = s
		}
	}
	if tools, ok := inputs["tools"].([]any); ok && len(tools) > 0 {
		var out []any
		for _, t := range tools {
			tm, _ := t.(map[string]any)
			fn, _ := tm["function"].(map[string]any)
			if fn == nil {
				continue
			}
			schema := fn["parameters"]
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			out = append(out, map[string]any{"name": fn["name"], "description": fn["description"], "input_schema": schema})
		}
		body["tools"] = out
	}
	switch tc := inputs["tool_choice"].(type) {
	case string:
		switch tc {
		case "auto":
			body["tool_choice"] = map[string]any{"type": "auto"}
		case "required":
			body["tool_choice"] = map[string]any{"type": "any"}
		}
	case map[string]any:
		if fn, ok := tc["function"].(map[string]any); ok {
			body["tool_choice"] = map[string]any{"type": "tool", "name": fn["name"]}
		}
	}
	return body
}

var stopReasons = map[string]string{"end_turn": "stop", "stop_sequence": "stop", "max_tokens": "length", "tool_use": "tool_calls"}

func fromAnthropic(resp map[string]any) map[string]any {
	var texts []string
	var tcs []any
	blocks, _ := resp["content"].([]any)
	for _, b := range blocks {
		bm, _ := b.(map[string]any)
		switch bm["type"] {
		case "text":
			if s, ok := bm["text"].(string); ok {
				texts = append(texts, s)
			}
		case "tool_use":
			args, _ := json.Marshal(bm["input"])
			tcs = append(tcs, map[string]any{"id": bm["id"], "type": "function", "function": map[string]any{"name": bm["name"], "arguments": string(args)}})
		}
	}
	msg := map[string]any{"role": "assistant", "content": strings.Join(texts, "")}
	if len(tcs) > 0 {
		msg["tool_calls"] = tcs
	}
	sr, _ := resp["stop_reason"].(string)
	out := map[string]any{
		"id": resp["id"], "object": "chat.completion", "created": time.Now().Unix(), "model": resp["model"],
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": stopReasons[sr]}},
	}
	if u, ok := resp["usage"].(map[string]any); ok {
		in, _ := store.ToFloat(u["input_tokens"])
		outT, _ := store.ToFloat(u["output_tokens"])
		out["usage"] = openAIUsage(in, outT, u)
	}
	return out
}

// openAIUsage converts Anthropic usage to the OpenAI shape. Anthropic's
// input_tokens excludes cache reads and writes; OpenAI's prompt_tokens
// includes them and reports them under prompt_tokens_details, which is how
// Weft prices cached tokens.
func openAIUsage(in, outT float64, u map[string]any) map[string]any {
	cr, _ := store.ToFloat(u["cache_read_input_tokens"])
	cw, _ := store.ToFloat(u["cache_creation_input_tokens"])
	prompt := in + cr + cw
	usage := map[string]any{"prompt_tokens": int(prompt), "completion_tokens": int(outT), "total_tokens": int(prompt + outT)}
	if cr > 0 || cw > 0 {
		usage["prompt_tokens_details"] = map[string]any{"cached_tokens": int(cr), "cache_write_tokens": int(cw)}
	}
	return usage
}

func (c *Client) anthropicComplete(ctx context.Context, rt Route, inputs map[string]any, emit func(map[string]any) error) (map[string]any, error) {
	// The Anthropic base URL comes only from the operator's environment.
	rt = anthropicRoute(rt.project, rt.Model)
	key, ok, err := c.routeKey(ctx, rt)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("no API key configured: set the ANTHROPIC_API_KEY secret")
	}
	body := toAnthropic(rt.Model, inputs)
	limit := MaxCompletionResponseBytes
	if emit != nil {
		body["stream"] = true
		limit = MaxStreamBytes
	}
	headers := map[string]string{"x-api-key": key, "anthropic-version": anthropicVersion}
	resp, err := c.post(ctx, rt, "/v1/messages", headers, body, limit)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if emit == nil {
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, decodeErr("provider response", err)
		}
		return fromAnthropic(out), nil
	}
	var id, model string
	var inTokens float64
	var startUsage map[string]any
	toolIdx := map[int]int{}
	nextTool := 0
	err = readSSE(resp.Body, func(event, data string) error {
		var ev map[string]any
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil
		}
		switch ev["type"] {
		case "message_start":
			m, _ := ev["message"].(map[string]any)
			id, _ = m["id"].(string)
			model, _ = m["model"].(string)
			u, _ := m["usage"].(map[string]any)
			inTokens, _ = store.ToFloat(u["input_tokens"])
			startUsage = u
			return emit(chunk(id, model, map[string]any{"role": "assistant", "content": ""}, nil, nil))
		case "content_block_start":
			cb, _ := ev["content_block"].(map[string]any)
			if cb["type"] == "tool_use" {
				idx, _ := store.ToFloat(ev["index"])
				toolIdx[int(idx)] = nextTool
				nextTool++
				return emit(chunk(id, model, map[string]any{"tool_calls": []any{map[string]any{
					"index": toolIdx[int(idx)], "id": cb["id"], "type": "function",
					"function": map[string]any{"name": cb["name"], "arguments": ""}}}}, nil, nil))
			}
		case "content_block_delta":
			d, _ := ev["delta"].(map[string]any)
			switch d["type"] {
			case "text_delta":
				return emit(chunk(id, model, map[string]any{"content": d["text"]}, nil, nil))
			case "input_json_delta":
				idx, _ := store.ToFloat(ev["index"])
				return emit(chunk(id, model, map[string]any{"tool_calls": []any{map[string]any{
					"index": toolIdx[int(idx)], "function": map[string]any{"arguments": d["partial_json"]}}}}, nil, nil))
			}
		case "message_delta":
			d, _ := ev["delta"].(map[string]any)
			sr, _ := d["stop_reason"].(string)
			u, _ := ev["usage"].(map[string]any)
			outT, _ := store.ToFloat(u["output_tokens"])
			// message_delta usage is cumulative and may repeat the cache counts.
			cu := map[string]any{}
			for k, v := range startUsage {
				cu[k] = v
			}
			for _, k := range []string{"cache_read_input_tokens", "cache_creation_input_tokens"} {
				if v, ok := u[k]; ok && v != nil {
					cu[k] = v
				}
			}
			return emit(chunk(id, model, map[string]any{}, stopReasons[sr], openAIUsage(inTokens, outT, cu)))
		case "error":
			e, _ := ev["error"].(map[string]any)
			return fmt.Errorf("provider stream error: %v", e["message"])
		}
		return nil
	})
	return nil, err
}
