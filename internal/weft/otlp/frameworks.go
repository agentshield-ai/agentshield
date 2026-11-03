package otlp

// Framework-specific span layouts: Vercel AI SDK (ai.*), MLflow
// (mlflow.span*), Pydantic AI / Logfire, Google ADK (gcp.vertex.agent.*),
// LangChain / LangGraph through OpenInference, and GenAI tool spans.

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// decodeAny decodes a JSON-encoded attribute value of any type (MLflow
// encodes every attribute value, strings included); other values pass through.
func decodeAny(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	t := strings.TrimSpace(s)
	if t == "" {
		return s
	}
	switch t[0] {
	case '{', '[', '"':
		if d, err := refs.Decode([]byte(t)); err == nil {
			return d
		}
	}
	return s
}

func isTrue(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true") || t == "1"
	case json.Number:
		return t.String() == "1"
	}
	return false
}

func toJSONString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func spreadInputs(v any) map[string]any {
	v = decodeAny(v)
	if m, ok := v.(map[string]any); ok {
		return m
	}
	if v == nil {
		return nil
	}
	return map[string]any{"input": v}
}

// openInferenceLLM reports whether an OpenInference span is an LLM span with
// structured messages, which describe it better than input.value.
func openInferenceLLM(attrs map[string]any) bool {
	k, _ := attrs["openinference.span.kind"].(string)
	return strings.EqualFold(k, "LLM")
}

// preferredInputs runs before input.value.
func preferredInputs(attrs map[string]any) map[string]any {
	if in := langfuseInputs(attrs); in != nil { // langfuse.go
		return in
	}
	if openInferenceLLM(attrs) {
		if msgs := indexedMessages(attrs, "llm.input_messages.", "message."); msgs != nil {
			return map[string]any{"messages": messagesToAny(msgs)}
		}
	}
	return nil
}

// frameworkInputs runs after input.value and before the GenAI conventions.
func frameworkInputs(attrs map[string]any) map[string]any {
	if v, ok := attrs["mlflow.spanInputs"]; ok {
		return spreadInputs(v)
	}
	if in := koogInputs(attrs); in != nil { // langfuse.go
		return in
	}
	if in := langfuseTraceInputs(attrs); in != nil {
		return in
	}
	// GenAI / Pydantic AI / ADK / OpenLit tool spans.
	for _, k := range []string{"gen_ai.tool.call.arguments", "gen_ai.tool.input", "tool_arguments", "gcp.vertex.agent.tool_call_args", "ai.toolCall.args", "ai.toolCall.input"} {
		if v, ok := attrs[k]; ok {
			if in := spreadInputs(v); in != nil {
				return in
			}
		}
	}
	// Pydantic AI agent runs.
	if v, ok := attrs["pydantic_ai.all_messages"]; ok {
		if msgs := partsMessages(v); msgs != nil {
			in, _ := splitFinal(msgs)
			return map[string]any{"messages": in}
		}
	}
	if v, ok := attrs["all_messages_events"]; ok {
		if msgs := pydanticEvents(v); msgs != nil {
			in, _ := splitFinal(msgs)
			return map[string]any{"messages": in}
		}
	}
	// Vercel AI SDK.
	if v, ok := attrs["ai.prompt.messages"]; ok {
		if msgs := vercelMessages(decodeAny(v)); msgs != nil {
			return map[string]any{"messages": msgs}
		}
	}
	if v, ok := attrs["ai.prompt"]; ok {
		p := decodeAny(v)
		if m, ok := p.(map[string]any); ok {
			var msgs []any
			if s, ok := m["system"].(string); ok && s != "" {
				msgs = append(msgs, map[string]any{"role": "system", "content": s})
			}
			if l := vercelMessages(m["messages"]); l != nil {
				msgs = append(msgs, l...)
			} else if s, ok := m["prompt"].(string); ok {
				msgs = append(msgs, map[string]any{"role": "user", "content": s})
			} else if l := vercelMessages(m["prompt"]); l != nil {
				msgs = append(msgs, l...)
			}
			if len(msgs) > 0 {
				return map[string]any{"messages": msgs}
			}
			return m
		}
		if s, ok := p.(string); ok {
			return map[string]any{"messages": []any{map[string]any{"role": "user", "content": s}}}
		}
	}
	if v, ok := attrs["ai.value"]; ok { // ai.embed
		return map[string]any{"input": decodeAny(v)}
	}
	if v, ok := attrs["ai.values"]; ok { // ai.embedMany
		return map[string]any{"input": decodeAny(v)}
	}
	// Google ADK.
	if v, ok := attrs["gcp.vertex.agent.llm_request"]; ok {
		if req, ok := decodeAny(v).(map[string]any); ok && len(req) > 0 {
			in := map[string]any{}
			var msgs []any
			if cfg, ok := req["config"].(map[string]any); ok {
				if s := geminiText(firstNonNil(cfg["system_instruction"], cfg["systemInstruction"])); s != "" {
					msgs = append(msgs, map[string]any{"role": "system", "content": s})
				}
				if tools, ok := cfg["tools"]; ok {
					in["tools"] = tools
				}
			}
			msgs = append(msgs, geminiMessages(req["contents"])...)
			in["messages"] = msgs
			if m, ok := req["model"].(string); ok {
				in["model"] = m
			}
			return in
		}
	}
	return nil
}

// preferredOutput runs before output.value.
func preferredOutput(attrs map[string]any) any {
	if out, ok := langfuseOutput(attrs); ok { // langfuse.go
		return out
	}
	if openInferenceLLM(attrs) {
		if msgs := indexedMessages(attrs, "llm.output_messages.", "message."); msgs != nil {
			return choices(messagesToAny(msgs), attrs)
		}
	}
	return nil
}

// frameworkOutput runs after output.value and before the GenAI conventions.
func frameworkOutput(attrs map[string]any) (any, bool) {
	if v, ok := attrs["mlflow.spanOutputs"]; ok {
		return decodeAny(v), true
	}
	if out, ok := koogOutput(attrs); ok { // langfuse.go
		return out, true
	}
	if out, ok := langfuseTraceOutput(attrs); ok {
		return out, true
	}
	for _, k := range []string{"gen_ai.tool.call.result", "gen_ai.tool.output", "tool_response", "gcp.vertex.agent.tool_response", "ai.toolCall.result", "ai.toolCall.output"} {
		if v, ok := attrs[k]; ok {
			return decodeAny(v), true
		}
	}
	if v, ok := attrs["final_result"]; ok { // Pydantic AI
		return decodeAny(v), true
	}
	if v, ok := attrs["pydantic_ai.all_messages"]; ok {
		if msgs := partsMessages(v); msgs != nil {
			if _, final := splitFinal(msgs); len(final) > 0 {
				return choices(final, attrs), true
			}
		}
	}
	// Vercel AI SDK.
	text, hasText := attrs["ai.response.text"].(string)
	tcs := vercelToolCalls(decodeAny(attrs["ai.response.toolCalls"]))
	reasoning := vercelReasoning(attrs)
	if hasText || len(tcs) > 0 || len(reasoning) > 0 {
		msg := map[string]any{"role": "assistant", "content": text}
		if len(reasoning) > 0 { // keep reasoning as thinking parts ahead of the reply
			parts := reasoning
			if text != "" {
				parts = append(parts, map[string]any{"type": "text", "text": text})
			}
			msg["content"] = parts
		}
		if len(tcs) > 0 {
			msg["tool_calls"] = tcs
		}
		if fr, ok := attrs["ai.response.finishReason"]; ok {
			msg["finish_reason"] = fr
		}
		out := choices([]any{msg}, attrs)
		if m := firstString(attrs, "ai.response.model", "ai.model.id"); m != "" {
			out["model"] = m
		}
		if id := firstString(attrs, "ai.response.id"); id != "" {
			out["id"] = id
		}
		return out, true
	}
	if v, ok := attrs["ai.response.object"]; ok {
		return decodeAny(v), true
	}
	if v, ok := attrs["ai.embedding"]; ok {
		return decodeAny(v), true
	}
	// Google ADK.
	if v, ok := attrs["gcp.vertex.agent.llm_response"]; ok {
		if resp, ok := decodeAny(v).(map[string]any); ok && len(resp) > 0 {
			var msgs []any
			if c, ok := resp["content"].(map[string]any); ok {
				msgs = geminiMessages([]any{c})
			}
			if len(msgs) > 0 {
				if fr, ok := resp["finish_reason"]; ok {
					if mm, ok := msgs[len(msgs)-1].(map[string]any); ok {
						mm["finish_reason"] = fr
					}
				}
				return choices(msgs, attrs), true
			}
			return resp, true
		}
	}
	return nil, false
}

// vercelReasoning reads the AI SDK's ai.response.reasoning (a string, or a
// list of reasoning parts) as thinking parts.
func vercelReasoning(attrs map[string]any) []any {
	var out []any
	for _, k := range []string{"ai.response.reasoning", "ai.response.reasoningText"} {
		switch t := decodeAny(attrs[k]).(type) {
		case string:
			if strings.TrimSpace(t) != "" {
				out = append(out, map[string]any{"type": "thinking", "thinking": t})
			}
		case []any:
			for _, x := range t {
				m, _ := x.(map[string]any)
				if s := firstString(m, "text", "reasoning", "content"); s != "" {
					out = append(out, map[string]any{"type": "thinking", "thinking": s})
				} else if s, ok := x.(string); ok && s != "" {
					out = append(out, map[string]any{"type": "thinking", "thinking": s})
				}
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return out
}

// splitFinal separates trailing assistant messages (the run's answer) from
// the history that led to them.
func splitFinal(msgs []any) (history, final []any) {
	i := len(msgs)
	for i > 0 {
		m, _ := msgs[i-1].(map[string]any)
		if m == nil || m["role"] != "assistant" || m["tool_calls"] != nil {
			break
		}
		i--
	}
	if i == 0 { // nothing but assistant messages: keep them as the history
		return msgs, nil
	}
	return msgs[:i], msgs[i:]
}

// pydanticEvents converts Pydantic AI's all_messages_events (instrumentation v1).
func pydanticEvents(v any) []any {
	l, ok := decodeAny(v).([]any)
	if !ok || len(l) == 0 {
		return nil
	}
	out := make([]any, 0, len(l))
	for _, x := range l {
		ev, ok := x.(map[string]any)
		if !ok {
			continue
		}
		src := ev
		if m, ok := ev["message"].(map[string]any); ok {
			src = m
		}
		msg := map[string]any{}
		for _, k := range []string{"role", "content", "tool_calls", "name"} {
			if v, ok := src[k]; ok {
				msg[k] = v
			}
		}
		if id, ok := src["id"]; ok && msg["role"] == "tool" {
			msg["tool_call_id"] = id
		}
		if msg["role"] == nil {
			if n, _ := ev["event.name"].(string); strings.HasPrefix(n, "gen_ai.") {
				r := strings.TrimSuffix(strings.TrimPrefix(n, "gen_ai."), ".message")
				if r == "choice" {
					r = "assistant"
				}
				msg["role"] = r
			}
		}
		out = append(out, msg)
	}
	return out
}

// vercelMessages converts AI SDK prompt messages into chat messages.
func vercelMessages(v any) []any {
	l, ok := v.([]any)
	if !ok || len(l) == 0 {
		return nil
	}
	var out []any
	for _, x := range l {
		m, ok := x.(map[string]any)
		if !ok {
			return nil
		}
		role, _ := m["role"].(string)
		content, isList := m["content"].([]any)
		if !isList {
			out = append(out, map[string]any{"role": role, "content": m["content"]})
			continue
		}
		msg := map[string]any{"role": role}
		var parts, tcs []any
		for _, p := range content {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			switch pm["type"] {
			case "text":
				parts = append(parts, map[string]any{"type": "text", "text": pm["text"]})
			case "reasoning":
				parts = append(parts, map[string]any{"type": "thinking", "thinking": firstNonNil(pm["text"], pm["reasoning"])})
			case "image":
				parts = append(parts, imagePart(pm["image"], firstString(pm, "mimeType", "mediaType")))
			case "file":
				mt := firstString(pm, "mimeType", "mediaType")
				if strings.HasPrefix(mt, "image/") {
					parts = append(parts, imagePart(pm["data"], mt))
				} else {
					parts = append(parts, mediaPart(mt, "", toJSONString(pm["data"])))
				}
			case "tool-call":
				tcs = append(tcs, map[string]any{"id": pm["toolCallId"], "type": "function",
					"function": map[string]any{"name": pm["toolName"], "arguments": toJSONString(firstNonNil(pm["args"], pm["input"]))}})
			case "tool-result":
				out = append(out, map[string]any{"role": "tool", "tool_call_id": pm["toolCallId"], "name": pm["toolName"],
					"content": toJSONString(firstNonNil(pm["result"], pm["output"]))})
			default:
				parts = append(parts, pm)
			}
		}
		if len(parts) == 0 && len(tcs) == 0 {
			continue // tool-result-only message, already split out
		}
		msg["content"] = collapseParts(parts)
		if len(tcs) > 0 {
			msg["tool_calls"] = tcs
		}
		out = append(out, msg)
	}
	return out
}

func vercelToolCalls(v any) []any {
	l, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []any
	for _, x := range l {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, map[string]any{"id": m["toolCallId"], "type": "function",
			"function": map[string]any{"name": m["toolName"], "arguments": toJSONString(firstNonNil(m["args"], m["input"]))}})
	}
	return out
}

// collapseParts returns a plain string when every part is text.
func collapseParts(parts []any) any {
	var texts []string
	for _, p := range parts {
		m, _ := p.(map[string]any)
		if m == nil || m["type"] != "text" {
			return parts
		}
		s, _ := m["text"].(string)
		texts = append(texts, s)
	}
	return strings.Join(texts, "\n")
}

// imagePart builds an OpenAI-style image part from a URL, data URI or base64.
func imagePart(v any, mime string) map[string]any {
	s, _ := v.(string)
	if s != "" && !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") && !strings.HasPrefix(s, "data:") &&
		!refs.IsRef(s) {
		if mime == "" {
			mime = "image/png"
		}
		s = "data:" + mime + ";base64," + s
	}
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": s}}
}

// mediaPart is a non-image media value (audio, video, documents) as a data URI.
func mediaPart(mime, modality, data string) map[string]any {
	p := map[string]any{"type": "media", "mime_type": mime}
	if modality != "" {
		p["modality"] = modality
	}
	if data != "" && !strings.HasPrefix(data, "data:") && !strings.HasPrefix(data, "http") {
		data = "data:" + mime + ";base64," + data
	}
	p["data"] = data
	return p
}

func geminiText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		if parts, ok := t["parts"].([]any); ok {
			var ts []string
			for _, p := range parts {
				if pm, ok := p.(map[string]any); ok {
					if s, ok := pm["text"].(string); ok {
						ts = append(ts, s)
					}
				}
			}
			return strings.Join(ts, "\n")
		}
	case []any:
		var ts []string
		for _, x := range t {
			if s := geminiText(x); s != "" {
				ts = append(ts, s)
			}
		}
		return strings.Join(ts, "\n")
	}
	return ""
}

// geminiMessages converts Gemini/ADK contents ({role, parts}) into chat messages.
func geminiMessages(v any) []any {
	l, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []any
	for _, x := range l {
		c, ok := x.(map[string]any)
		if !ok {
			continue
		}
		role, _ := c["role"].(string)
		if role == "model" {
			role = "assistant"
		}
		msg := map[string]any{"role": role}
		var parts, tcs []any
		pl, _ := c["parts"].([]any)
		for _, p := range pl {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if s, ok := pm["text"].(string); ok {
				if isTrue(pm["thought"]) {
					parts = append(parts, map[string]any{"type": "thinking", "thinking": s})
				} else {
					parts = append(parts, map[string]any{"type": "text", "text": s})
				}
				continue
			}
			if fc, ok := firstNonNil(pm["function_call"], pm["functionCall"]).(map[string]any); ok {
				tcs = append(tcs, map[string]any{"id": fc["id"], "type": "function",
					"function": map[string]any{"name": fc["name"], "arguments": toJSONString(fc["args"])}})
				continue
			}
			if fr, ok := firstNonNil(pm["function_response"], pm["functionResponse"]).(map[string]any); ok {
				out = append(out, map[string]any{"role": "tool", "tool_call_id": fr["id"], "name": fr["name"], "content": toJSONString(fr["response"])})
				continue
			}
			if d, ok := firstNonNil(pm["inline_data"], pm["inlineData"]).(map[string]any); ok {
				mt := firstString(d, "mime_type", "mimeType")
				data, _ := d["data"].(string)
				if strings.HasPrefix(mt, "image/") {
					parts = append(parts, imagePart(data, mt))
				} else {
					parts = append(parts, mediaPart(mt, "", data))
				}
				continue
			}
			if d, ok := firstNonNil(pm["file_data"], pm["fileData"]).(map[string]any); ok {
				mt := firstString(d, "mime_type", "mimeType")
				uri := firstString(d, "file_uri", "fileUri")
				if strings.HasPrefix(mt, "image/") {
					parts = append(parts, imagePart(uri, mt))
				} else {
					parts = append(parts, mediaPart(mt, "", uri))
				}
			}
		}
		if len(parts) == 0 && len(tcs) == 0 {
			continue
		}
		msg["content"] = collapseParts(parts)
		if len(tcs) > 0 {
			msg["tool_calls"] = tcs
		}
		out = append(out, msg)
	}
	return out
}

// genAIPart converts one GenAI semconv message part into a chat content part.
// ok is false for text parts, which the caller joins.
func genAIPart(pm map[string]any) (map[string]any, bool) {
	mt, _ := pm["mime_type"].(string)
	modality, _ := pm["modality"].(string)
	switch pm["type"] {
	case "reasoning", "thinking":
		return map[string]any{"type": "thinking", "thinking": firstNonNil(pm["content"], pm["thinking"], pm["text"])}, true
	case "blob":
		data, _ := pm["content"].(string)
		if modality == "image" || strings.HasPrefix(mt, "image/") {
			return imagePart(data, mt), true
		}
		return mediaPart(mt, modality, data), true
	case "uri":
		uri, _ := pm["uri"].(string)
		if modality == "image" || strings.HasPrefix(mt, "image/") {
			return imagePart(uri, mt), true
		}
		return mediaPart(mt, modality, uri), true
	case "file":
		p := map[string]any{"type": "media", "mime_type": mt, "file_id": pm["file_id"]}
		if modality != "" {
			p["modality"] = modality
		}
		return p, true
	}
	return pm, true
}

// langChainRoles maps LangChain message classes and types to chat roles.
var langChainRoles = map[string]string{
	"HumanMessage": "user", "HumanMessageChunk": "user", "human": "user",
	"AIMessage": "assistant", "AIMessageChunk": "assistant", "ai": "assistant",
	"SystemMessage": "system", "SystemMessageChunk": "system", "system": "system",
	"ToolMessage": "tool", "ToolMessageChunk": "tool", "tool": "tool", "FunctionMessage": "tool", "function": "tool",
}

// lcMessage converts a serialised LangChain message into a chat message.
func lcMessage(m map[string]any) (map[string]any, bool) {
	var role string
	src := m
	if m["lc"] != nil && m["type"] == "constructor" {
		ids, _ := m["id"].([]any)
		if len(ids) == 0 {
			return nil, false
		}
		cls, _ := ids[len(ids)-1].(string)
		r, ok := langChainRoles[cls]
		if !ok && cls != "ChatMessage" {
			return nil, false
		}
		role = r
		src, _ = m["kwargs"].(map[string]any)
		if src == nil {
			return nil, false
		}
		if cls == "ChatMessage" {
			role, _ = src["role"].(string)
		}
	} else {
		t, _ := m["type"].(string)
		r, ok := langChainRoles[t]
		if !ok || m["content"] == nil || m["role"] != nil {
			return nil, false
		}
		role = r
	}
	msg := map[string]any{"role": role, "content": normalizeLangChain(src["content"])}
	if n, ok := src["name"].(string); ok && n != "" {
		msg["name"] = n
	}
	if id, ok := src["tool_call_id"]; ok {
		msg["tool_call_id"] = id
	}
	var tcs []any
	if l, ok := src["tool_calls"].([]any); ok {
		for _, x := range l {
			tc, _ := x.(map[string]any)
			if tc == nil {
				continue
			}
			if fn, ok := tc["function"].(map[string]any); ok { // already OpenAI shaped
				tcs = append(tcs, map[string]any{"id": tc["id"], "type": "function", "function": fn})
				continue
			}
			tcs = append(tcs, map[string]any{"id": tc["id"], "type": "function",
				"function": map[string]any{"name": tc["name"], "arguments": toJSONString(tc["args"])}})
		}
	}
	if len(tcs) > 0 {
		msg["tool_calls"] = tcs
	}
	return msg, true
}

// normalizeLangChain rewrites serialised LangChain messages anywhere in v
// (as found in OpenInference input.value / output.value) into chat messages.
func normalizeLangChain(v any) any {
	return normLC(v, 0)
}

func normLC(v any, depth int) any {
	if depth > 8 {
		return v
	}
	switch t := v.(type) {
	case map[string]any:
		if m, ok := lcMessage(t); ok {
			return m
		}
		for k, x := range t {
			t[k] = normLC(x, depth+1)
		}
		return t
	case []any:
		// Chat model inputs are batches: {"messages": [[m1, m2]]}.
		if len(t) == 1 {
			if inner, ok := t[0].([]any); ok && len(inner) > 0 {
				if m, ok := inner[0].(map[string]any); ok {
					if _, isLC := lcMessage(m); isLC {
						t = inner
					}
				}
			}
		}
		for i := range t {
			t[i] = normLC(t[i], depth+1)
		}
		return t
	}
	return v
}

// frameworkUsage fills token usage from framework attributes the GenAI
// conventions do not cover.
func frameworkUsage(attrs map[string]any, set func(name string, keys ...string)) {
	set("prompt_tokens", "ai.usage.promptTokens", "ai.usage.inputTokens")
	set("completion_tokens", "ai.usage.completionTokens", "ai.usage.outputTokens")
	set("total_tokens", "ai.usage.totalTokens")
	set("reasoning_tokens", "gen_ai.usage.reasoning.output_tokens", "gen_ai.usage.reasoning_tokens", "llm.token_count.completion_details.reasoning",
		"ai.usage.reasoningTokens")
	set("cache_read_input_tokens", "gen_ai.usage.cache_read.input_tokens", "gen_ai.usage.cache_read_input_tokens",
		"llm.token_count.prompt_details.cache_read", "ai.usage.cachedInputTokens")
	// OTel GenAI semconv: gen_ai.usage.cache_read.input_tokens and
	// gen_ai.usage.cache_creation.input_tokens are included in
	// gen_ai.usage.input_tokens (the common convention).
	set("cache_creation_input_tokens", "gen_ai.usage.cache_creation.input_tokens", "gen_ai.usage.cache_write.input_tokens",
		"gen_ai.usage.cache_creation_input_tokens",
		"llm.token_count.prompt_details.cache_write")
}

// structuredUsage reads usage objects (MLflow token usage, ADK usage metadata).
func structuredUsage(attrs map[string]any, u map[string]any) {
	put := func(name string, v any) {
		if _, exists := u[name]; exists {
			return
		}
		if f, ok := store.ToFloat(v); ok {
			u[name] = json.Number(strconv.FormatFloat(f, 'f', -1, 64))
		}
	}
	langfuseUsage(attrs, put) // langfuse.go
	if m, ok := decodeAny(attrs["mlflow.chat.tokenUsage"]).(map[string]any); ok {
		put("prompt_tokens", m["input_tokens"])
		put("completion_tokens", m["output_tokens"])
		put("total_tokens", m["total_tokens"])
	}
	if resp, ok := decodeAny(attrs["gcp.vertex.agent.llm_response"]).(map[string]any); ok {
		um, _ := firstNonNil(resp["usage_metadata"], resp["usageMetadata"]).(map[string]any)
		if um != nil {
			put("prompt_tokens", firstNonNil(um["prompt_token_count"], um["promptTokenCount"]))
			put("completion_tokens", firstNonNil(um["candidates_token_count"], um["candidatesTokenCount"]))
			put("total_tokens", firstNonNil(um["total_token_count"], um["totalTokenCount"]))
			put("reasoning_tokens", firstNonNil(um["thoughts_token_count"], um["thoughtsTokenCount"]))
			put("cache_read_input_tokens", firstNonNil(um["cached_content_token_count"], um["cachedContentTokenCount"]))
		}
	}
}

// frameworkModel names the model for usage when GenAI attributes don't.
func frameworkModel(attrs map[string]any) string {
	if m := firstString(attrs, "ai.response.model", "ai.model.id", "langfuse.observation.model.name", "model_name"); m != "" {
		return m
	}
	if req, ok := decodeAny(attrs["gcp.vertex.agent.llm_request"]).(map[string]any); ok {
		if m, ok := req["model"].(string); ok {
			return m
		}
	}
	if in, ok := decodeAny(attrs["mlflow.spanInputs"]).(map[string]any); ok {
		if m, ok := in["model"].(string); ok {
			return m
		}
	}
	return ""
}

// frameworkDisplayName names Traceloop entities ("joke.workflow" → "joke").
func frameworkDisplayName(attrs map[string]any) *string {
	if _, ok := attrs["traceloop.span.kind"]; ok {
		if s := firstString(attrs, "traceloop.entity.name"); s != "" {
			return &s
		}
		if k, _ := attrs["traceloop.span.kind"].(string); k == "workflow" {
			return optString(attrs, "traceloop.workflow.name")
		}
	}
	return nil
}
