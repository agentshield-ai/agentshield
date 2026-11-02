package otlp

// Langfuse, Koog and OpenLit span layouts.
//
// Langfuse (langfuse-python/js v3+, and exporters that target Langfuse such
// as Koog's addLangfuseExporter): langfuse.observation.{input,output,type,
// model.name,model.parameters,usage_details,cost_details,level,
// status_message,prompt.name,prompt.version,metadata.*},
// langfuse.trace.{name,input,output,tags,metadata.*}, langfuse.session.id /
// langfuse.user.id (also session.id / user.id). Observation inputs and
// outputs win over input.value; trace inputs and outputs are a fallback for
// the span that carries them.
//
// Koog (JetBrains): GenAI spans (invoke_agent, chat, execute_tool) whose
// gen_ai.input/output.messages use Kotlin role names ("User", "Assistant")
// and emit an empty "tool" entry before each user message; node and
// subgraph spans carry koog.node.input/output and koog.subgraph.input/output.
// With Koog's Weave adapter, tool calls arrive as
// gen_ai.completion.N.tool_calls.M.function = {"name","arguments"} JSON.
//
// OpenLit: GenAI conventions plus gen_ai.usage.cost, gen_ai.client.token.usage,
// gen_ai.session.id / gen_ai.user.id and gen_ai.tool.input/output on tool
// spans; system instructions are repeated in gen_ai.input.messages.

import (
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// langfuseKinds maps langfuse.observation.type to Weft span kinds.
var langfuseKinds = map[string]string{
	"generation": "llm", "agent": "agent", "tool": "tool", "chain": "chain", "retriever": "retriever",
	"evaluator": "evaluator", "embedding": "embedding", "guardrail": "guardrail",
}

func langfuseType(attrs map[string]any) string {
	t, _ := attrs["langfuse.observation.type"].(string)
	return strings.ToLower(strings.TrimSpace(t))
}

// messageList reports whether v is a list of chat messages ({role, ...}).
func messageList(v any) ([]any, bool) {
	l, ok := v.([]any)
	if !ok || len(l) == 0 {
		return nil, false
	}
	for _, x := range l {
		m, ok := x.(map[string]any)
		if !ok {
			return nil, false
		}
		if _, ok := m["role"]; !ok {
			return nil, false
		}
	}
	return l, true
}

// langfuseValueInputs shapes a Langfuse input value as call inputs.
func langfuseValueInputs(v any, attrs map[string]any) map[string]any {
	v = decodeAny(v)
	var in map[string]any
	if msgs, ok := messageList(v); ok {
		if parts := partsMessages(msgs); parts != nil && hasParts(msgs) {
			msgs = parts
		}
		in = map[string]any{"messages": msgs}
	} else if m, ok := v.(map[string]any); ok {
		in = m
	} else if v == nil {
		return nil
	} else {
		in = map[string]any{"input": v}
	}
	if m := firstString(attrs, "langfuse.observation.model.name"); m != "" {
		if _, exists := in["model"]; !exists {
			in["model"] = m
		}
	}
	if p, ok := decodeAny(attrs["langfuse.observation.model.parameters"]).(map[string]any); ok {
		for k, x := range p {
			if _, exists := in[k]; !exists {
				in[k] = decodeAny(x)
			}
		}
	}
	return in
}

func hasParts(msgs []any) bool {
	for _, x := range msgs {
		if m, ok := x.(map[string]any); ok {
			if _, ok := m["parts"]; ok {
				return true
			}
		}
	}
	return false
}

// langfuseInputs reads langfuse.observation.input.
func langfuseInputs(attrs map[string]any) map[string]any {
	if v, ok := attrs["langfuse.observation.input"]; ok {
		return langfuseValueInputs(v, attrs)
	}
	return nil
}

// langfuseTraceInputs reads langfuse.trace.input (fallback).
func langfuseTraceInputs(attrs map[string]any) map[string]any {
	if v, ok := attrs["langfuse.trace.input"]; ok {
		return langfuseValueInputs(v, attrs)
	}
	return nil
}

// langfuseValueOutput shapes a Langfuse output; generations become chat
// completions so the chat view renders them.
func langfuseValueOutput(v any, attrs map[string]any) any {
	v = decodeAny(v)
	if langfuseType(attrs) != "generation" {
		return v
	}
	var msgs []any
	switch t := v.(type) {
	case string:
		msgs = []any{map[string]any{"role": "assistant", "content": t}}
	case map[string]any:
		if _, ok := t["choices"]; ok {
			return t
		}
		if _, ok := t["role"]; ok {
			msgs = []any{t}
		}
	case []any:
		if l, ok := messageList(t); ok {
			msgs = l
			if hasParts(l) {
				msgs = partsMessages(l)
			}
		}
	}
	if msgs == nil {
		return v
	}
	out := choices(msgs, attrs)
	if m := firstString(attrs, "langfuse.observation.model.name"); m != "" {
		if _, ok := out["model"]; !ok {
			out["model"] = m
		}
	}
	return out
}

func langfuseOutput(attrs map[string]any) (any, bool) {
	if v, ok := attrs["langfuse.observation.output"]; ok {
		return langfuseValueOutput(v, attrs), true
	}
	return nil, false
}

func langfuseTraceOutput(attrs map[string]any) (any, bool) {
	if v, ok := attrs["langfuse.trace.output"]; ok {
		return decodeAny(v), true
	}
	return nil, false
}

// langfuseUsage reads langfuse.observation.usage_details
// ({"input","output","total", cache and reasoning details, or OpenAI names}).
func langfuseUsage(attrs map[string]any, put func(name string, v any)) {
	ud, ok := decodeAny(attrs["langfuse.observation.usage_details"]).(map[string]any)
	if !ok {
		return
	}
	pick := func(keys ...string) any {
		for _, k := range keys {
			if v, ok := ud[k]; ok {
				return v
			}
		}
		return nil
	}
	// Langfuse usage types are disjoint ("input excludes any input_* values"),
	// while Weft's prompt_tokens is gross, so cache reads and writes are added
	// back into the prompt count.
	cr := pick("input_cached_tokens", "cache_read_input_tokens", "input_cache_read")
	cw := pick("cache_creation_input_tokens", "input_cache_creation")
	if in, ok := store.ToFloat(pick("input", "prompt_tokens", "input_tokens", "promptTokens")); ok {
		r, _ := store.ToFloat(cr)
		w, _ := store.ToFloat(cw)
		put("prompt_tokens", in+r+w)
	}
	put("completion_tokens", pick("output", "completion_tokens", "output_tokens", "completionTokens"))
	put("total_tokens", pick("total", "total_tokens", "totalTokens"))
	put("cache_read_input_tokens", cr)
	put("cache_creation_input_tokens", cw)
	put("reasoning_tokens", pick("output_reasoning_tokens", "reasoning_tokens"))
}

// koogInputs / koogOutput read Koog node and subgraph spans.
func koogInputs(attrs map[string]any) map[string]any {
	for _, k := range []string{"koog.node.input", "koog.subgraph.input"} {
		if v, ok := attrs[k]; ok {
			return spreadInputs(v)
		}
	}
	return nil
}

func koogOutput(attrs map[string]any) (any, bool) {
	for _, k := range []string{"koog.node.output", "koog.subgraph.output"} {
		if v, ok := attrs[k]; ok {
			return decodeAny(v), true
		}
	}
	return nil, false
}

// applySpanMeta fills call fields and Weft attributes from framework
// attributes: Langfuse session/user ids, trace name, observation type,
// level and status message, tags and prompt; OpenLit session/user ids.
func applySpanMeta(attrs map[string]any, st *store.CallStart, user map[string]any) {
	if st.ThreadID == nil {
		st.ThreadID = optString(attrs, "langfuse.session.id", "gen_ai.session.id")
	}
	if st.WBUserID == nil {
		st.WBUserID = optString(attrs, "langfuse.user.id", "gen_ai.user.id")
	}
	if st.DisplayName == nil && st.ParentID == nil {
		st.DisplayName = optString(attrs, "langfuse.trace.name")
	}
	if k := langfuseKinds[langfuseType(attrs)]; k != "" {
		if _, ok := attrs["gen_ai.operation.name"]; !ok {
			user["weft.span_kind"] = k
		}
	}
	lf := map[string]any{}
	for _, k := range []string{"level", "status_message", "prompt.name", "prompt.version", "type", "completion_start_time"} {
		if v, ok := attrs["langfuse.observation."+k]; ok {
			lf[k] = v
		}
	}
	if v, ok := attrs["langfuse.trace.tags"]; ok {
		lf["tags"] = decodeAny(v)
	}
	if v, ok := attrs["langfuse.observation.cost_details"]; ok {
		lf["cost_details"] = decodeAny(v)
	}
	meta := map[string]any{}
	for k, v := range attrs {
		if rest, ok := strings.CutPrefix(k, "langfuse.observation.metadata."); ok {
			meta[rest] = decodeAny(v)
		} else if rest, ok := strings.CutPrefix(k, "langfuse.trace.metadata."); ok {
			meta[rest] = decodeAny(v)
		}
	}
	if v, ok := attrs["langfuse.observation.metadata"]; ok {
		if m, ok := decodeAny(v).(map[string]any); ok {
			for k, x := range m {
				meta[k] = x
			}
		}
	}
	if len(meta) > 0 {
		lf["metadata"] = meta
	}
	if len(lf) > 0 {
		user["langfuse"] = lf
	}
}

// langfuseError turns langfuse.observation.level ERROR into an exception.
func langfuseError(attrs map[string]any) *string {
	if l, _ := attrs["langfuse.observation.level"].(string); strings.EqualFold(l, "ERROR") {
		msg := firstString(attrs, "langfuse.observation.status_message")
		if msg == "" {
			msg = "error"
		}
		return &msg
	}
	return nil
}

func startsWithSystem(msgs []any) bool {
	if len(msgs) == 0 {
		return false
	}
	m, _ := msgs[0].(map[string]any)
	r, _ := m["role"].(string)
	return strings.EqualFold(r, "system")
}
