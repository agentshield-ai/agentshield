package store

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ChatMsg is one chat message normalised from any supported shape: OpenAI
// chat messages and completions, the Responses API, Anthropic content blocks,
// Gemini contents, and GenAI semantic-convention parts.
type ChatMsg struct {
	Role         string
	Text         string
	Reasoning    string
	ToolCalls    []ChatToolCall
	ToolCallID   string
	Media        []any // image/audio/blob parts, kept as given
	FinishReason string
	// Structured is true when the message has non-text parts, so its content
	// should be serialised as a JSON parts array.
	Structured bool
	Parts      []any
}

// ChatToolCall is a tool call requested by an assistant message.
type ChatToolCall struct {
	ID        string
	Name      string
	Arguments string
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

func jsonString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func isMessageMap(m map[string]any) bool {
	if m == nil {
		return false
	}
	if _, ok := m["role"].(string); ok {
		_, c := m["content"]
		_, p := m["parts"]
		_, t := m["tool_calls"]
		_, f := m["function_call"]
		return c || p || t || f
	}
	switch m["type"] {
	case "message", "function_call", "function_call_output":
		return true
	}
	return false
}

// messageList returns v as a list of message maps when it looks like one.
func messageList(v any) []map[string]any {
	if s, ok := v.(string); ok {
		t := strings.TrimSpace(s)
		if strings.HasPrefix(t, "[") {
			var x any
			if json.Unmarshal([]byte(t), &x) == nil {
				v = x
			}
		}
	}
	l, ok := v.([]any)
	if !ok || len(l) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(l))
	anyRole := false
	for _, x := range l {
		m := asMap(x)
		if !isMessageMap(m) {
			return nil
		}
		if _, ok := m["role"].(string); ok {
			anyRole = true
		}
		out = append(out, m)
	}
	if !anyRole {
		return nil
	}
	return out
}

// NormaliseMessage converts one message map.
func NormaliseMessage(m map[string]any) ChatMsg {
	var c ChatMsg
	switch m["type"] {
	case "function_call":
		c.Role = "assistant"
		c.ToolCalls = []ChatToolCall{{ID: jsonString(firstNonNilAny(m["call_id"], m["id"])), Name: jsonString(m["name"]), Arguments: jsonString(m["arguments"])}}
		return c
	case "function_call_output":
		c.Role = "tool"
		c.ToolCallID = jsonString(m["call_id"])
		c.Text = jsonString(m["output"])
		return c
	}
	c.Role, _ = m["role"].(string)
	switch c.Role {
	case "model":
		c.Role = "assistant"
	case "human":
		c.Role = "user"
	case "ai":
		c.Role = "assistant"
	case "":
		if m["type"] == "message" {
			c.Role = "assistant"
		}
	}
	c.ToolCallID = jsonString(m["tool_call_id"])
	c.FinishReason = jsonString(m["finish_reason"])
	var texts []string
	addParts := func(v any) {
		switch t := v.(type) {
		case nil:
		case string:
			if t != "" {
				texts = append(texts, t)
			}
		case []any:
			for _, p := range t {
				c.addPart(p, &texts)
			}
		default:
			c.addPart(t, &texts)
		}
	}
	addParts(m["parts"])
	if content, ok := m["content"]; ok {
		if c.Role == "tool" {
			if _, isStr := content.(string); !isStr {
				if _, isList := content.([]any); !isList && content != nil {
					texts = append(texts, jsonString(content))
					content = nil
				}
			}
		}
		addParts(content)
	}
	if tcs, ok := m["tool_calls"].([]any); ok {
		for _, x := range tcs {
			tc := asMap(x)
			fn := asMap(tc["function"])
			if fn == nil {
				fn = tc
			}
			c.ToolCalls = append(c.ToolCalls, ChatToolCall{ID: jsonString(tc["id"]), Name: jsonString(fn["name"]),
				Arguments: jsonString(firstNonNilAny(fn["arguments"], fn["input"]))})
		}
	}
	if fc := asMap(m["function_call"]); fc != nil {
		c.ToolCalls = append(c.ToolCalls, ChatToolCall{Name: jsonString(fc["name"]), Arguments: jsonString(fc["arguments"])})
	}
	c.Text = strings.Join(texts, "\n")
	if len(c.Media) > 0 || len(c.ToolCalls) > 0 {
		c.Structured = true
	}
	return c
}

func firstNonNilAny(vs ...any) any {
	for _, v := range vs {
		if v != nil {
			return v
		}
	}
	return nil
}

func (c *ChatMsg) addPart(p any, texts *[]string) {
	if s, ok := p.(string); ok {
		if s != "" {
			*texts = append(*texts, s)
		}
		c.Parts = append(c.Parts, map[string]any{"type": "text", "content": s})
		return
	}
	m := asMap(p)
	if m == nil {
		return
	}
	c.Parts = append(c.Parts, m)
	typ, _ := m["type"].(string)
	switch typ {
	case "text", "input_text", "output_text":
		if s := jsonString(firstNonNilAny(m["text"], m["content"])); s != "" {
			*texts = append(*texts, s)
		}
		return
	case "reasoning", "thinking":
		s := jsonString(firstNonNilAny(m["content"], m["thinking"], m["text"]))
		if c.Reasoning != "" {
			c.Reasoning += "\n"
		}
		c.Reasoning += s
		return
	case "tool_call", "tool_use", "function_call":
		c.ToolCalls = append(c.ToolCalls, ChatToolCall{ID: jsonString(firstNonNilAny(m["id"], m["call_id"])), Name: jsonString(m["name"]),
			Arguments: jsonString(firstNonNilAny(m["arguments"], m["input"]))})
		return
	case "tool_call_response", "tool_result":
		if c.ToolCallID == "" {
			c.ToolCallID = jsonString(firstNonNilAny(m["id"], m["tool_use_id"]))
		}
		r := firstNonNilAny(m["response"], m["result"], m["content"])
		if l, ok := r.([]any); ok { // Anthropic tool_result content blocks
			var ts []string
			for _, x := range l {
				if xm := asMap(x); xm != nil && xm["type"] == "text" {
					ts = append(ts, jsonString(xm["text"]))
				}
			}
			if len(ts) == len(l) {
				r = strings.Join(ts, "\n")
			}
		}
		*texts = append(*texts, jsonString(r))
		return
	case "image", "image_url", "input_image", "blob", "uri", "file", "audio", "input_audio":
		c.Media = append(c.Media, m)
		return
	case "refusal":
		*texts = append(*texts, "Refusal: "+jsonString(m["refusal"]))
		return
	}
	// Gemini parts.
	if s, ok := m["text"].(string); ok {
		*texts = append(*texts, s)
		return
	}
	if fc := asMap(m["functionCall"]); fc != nil {
		c.ToolCalls = append(c.ToolCalls, ChatToolCall{ID: jsonString(fc["id"]), Name: jsonString(fc["name"]), Arguments: jsonString(fc["args"])})
		return
	}
	if fr := asMap(m["functionResponse"]); fr != nil {
		*texts = append(*texts, jsonString(fr["response"]))
		return
	}
	if m["inlineData"] != nil || m["fileData"] != nil || m["_type"] == "Image" || m["_type"] == "Audio" {
		c.Media = append(c.Media, m)
		return
	}
	if s, ok := m["content"].(string); ok && typ == "" {
		*texts = append(*texts, s)
	}
}

var inputMessageKeys = []string{"messages", "gen_ai.input.messages", "input_messages", "input", "contents", "history", "chat_history", "prompt"}

// InputMessages finds the chat messages in call inputs (nil when not chat-shaped).
func InputMessages(inputs map[string]any) []ChatMsg {
	var raw []map[string]any
	for _, k := range inputMessageKeys {
		if raw = messageList(inputs[k]); raw != nil {
			break
		}
		if m := asMap(inputs[k]); m != nil {
			if raw = messageList(m["messages"]); raw != nil {
				break
			}
		}
	}
	if raw == nil {
		if kw := asMap(inputs["kwargs"]); kw != nil {
			raw = messageList(kw["messages"])
		}
	}
	if raw == nil {
		return nil
	}
	out := make([]ChatMsg, 0, len(raw)+1)
	if sys := systemText(inputs["system"]); sys != "" {
		out = append(out, ChatMsg{Role: "system", Text: sys})
	}
	for _, m := range raw {
		out = append(out, NormaliseMessage(m))
	}
	return out
}

func systemText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var ts []string
		for _, x := range t {
			if m := asMap(x); m != nil {
				if s, ok := firstNonNilAny(m["text"], m["content"]).(string); ok {
					ts = append(ts, s)
				}
			} else if s, ok := x.(string); ok {
				ts = append(ts, s)
			}
		}
		return strings.Join(ts, "\n")
	}
	return ""
}

// OutputMessages finds assistant messages in a call output (nil when none).
func OutputMessages(output any) []ChatMsg {
	switch t := output.(type) {
	case nil:
		return nil
	case string:
		if t == "" {
			return nil
		}
		return []ChatMsg{{Role: "assistant", Text: t}}
	case []any:
		if raw := messageList(t); raw != nil {
			return normAll(raw)
		}
		return nil
	}
	m := asMap(output)
	if m == nil {
		return nil
	}
	if cs, ok := m["choices"].([]any); ok && len(cs) > 0 {
		var out []ChatMsg
		for _, x := range cs {
			c := asMap(x)
			if msg := asMap(c["message"]); msg != nil {
				cm := NormaliseMessage(msg)
				if cm.Role == "" {
					cm.Role = "assistant"
				}
				if fr := jsonString(c["finish_reason"]); fr != "" {
					cm.FinishReason = fr
				}
				out = append(out, cm)
			} else if s, ok := c["text"].(string); ok {
				out = append(out, ChatMsg{Role: "assistant", Text: s})
			}
		}
		return out
	}
	if cands, ok := m["candidates"].([]any); ok && len(cands) > 0 { // Gemini
		var out []ChatMsg
		for _, x := range cands {
			if content := asMap(asMap(x)["content"]); content != nil {
				cm := NormaliseMessage(content)
				cm.Role = "assistant"
				out = append(out, cm)
			}
		}
		return out
	}
	if _, ok := m["content"].([]any); ok && (m["role"] == "assistant" || m["type"] == "message") { // Anthropic
		cm := NormaliseMessage(m)
		cm.Role = "assistant"
		cm.FinishReason = jsonString(m["stop_reason"])
		return []ChatMsg{cm}
	}
	if items, ok := m["output"].([]any); ok { // Responses API
		var raw []map[string]any
		for _, x := range items {
			if im := asMap(x); im != nil && (im["type"] == "message" || im["type"] == "function_call") {
				raw = append(raw, im)
			}
		}
		if len(raw) > 0 {
			return normAll(raw)
		}
	}
	if isMessageMap(m) {
		return []ChatMsg{NormaliseMessage(m)}
	}
	for _, k := range []string{"messages", "gen_ai.output.messages", "output_messages"} {
		if raw := messageList(m[k]); raw != nil {
			return normAll(raw)
		}
	}
	if msg := asMap(m["message"]); msg != nil && msg["role"] != nil {
		return []ChatMsg{NormaliseMessage(msg)}
	}
	return nil
}

func normAll(raw []map[string]any) []ChatMsg {
	out := make([]ChatMsg, 0, len(raw))
	for _, m := range raw {
		out = append(out, NormaliseMessage(m))
	}
	return out
}

var textInputKeys = []string{"user_message", "message", "question", "query", "input", "prompt", "text", "content", "task", "request", "user_input"}

// InputText is a readable text for non-chat inputs: a single prominent
// string argument, else compact JSON of the arguments.
func InputText(inputs map[string]any) string {
	for _, k := range textInputKeys {
		if s, ok := inputs[k].(string); ok && s != "" {
			return s
		}
	}
	rest := map[string]any{}
	for k, v := range inputs {
		if k == "self" || k == "model" || strings.HasPrefix(k, "_") {
			continue
		}
		rest[k] = v
	}
	if len(rest) == 0 {
		return ""
	}
	if len(rest) == 1 {
		for _, v := range rest {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return compactJSON(rest)
}

// OutputText is a readable text for an output that is not chat-shaped.
func OutputText(output any) string {
	switch t := output.(type) {
	case nil:
		return ""
	case string:
		return t
	case map[string]any:
		for _, k := range []string{"output", "answer", "response", "text", "content", "result", "final_result", "final_output"} {
			if s, ok := t[k].(string); ok && s != "" {
				return s
			}
		}
	}
	return compactJSON(output)
}

func compactJSON(v any) string {
	b, err := json.Marshal(v) // map keys are sorted
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// lastUserText returns the text of the last user message.
func lastUserText(msgs []ChatMsg) (string, *ChatMsg) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Text, &msgs[i]
		}
	}
	return "", nil
}

// assistantText joins the text of assistant messages.
func assistantText(msgs []ChatMsg) (text, reasoning string) {
	var ts, rs []string
	for _, m := range msgs {
		if m.Role != "assistant" && m.Role != "" {
			continue
		}
		if m.Text != "" {
			ts = append(ts, m.Text)
		}
		if m.Reasoning != "" {
			rs = append(rs, m.Reasoning)
		}
	}
	return strings.Join(ts, "\n"), strings.Join(rs, "\n")
}

// messageContent renders a message as span message content: plain
// text for simple messages, a JSON parts array for structured ones.
func messageContent(m ChatMsg) string {
	if !m.Structured || len(m.Parts) == 0 {
		if m.Text == "" && len(m.ToolCalls) > 0 {
			parts := make([]any, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				parts = append(parts, map[string]any{"type": "tool_call", "id": tc.ID, "name": tc.Name, "arguments": tc.Arguments})
			}
			return jsonString(parts)
		}
		return m.Text
	}
	return jsonString(m.Parts)
}

func truncateText(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
