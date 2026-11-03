package store

// Reasoning steps (docs/weft/DESIGN.md §2.2): model reasoning extracted from
// model spans and weft.reasoning span events, stored in reasoning_steps by
// the agent-graph hook (agents_handoffs.go).
//
// Sources, all read from the stored call (so every ingest path is covered):
//   - Anthropic content blocks: thinking (kind=thinking) and
//     redacted_thinking (kind=redacted, no text);
//   - OpenAI Responses reasoning items: summary parts (kind=summary), raw
//     reasoning_text content (kind=thinking), encrypted only (kind=redacted);
//   - Gemini parts with thought=true (kind=thinking);
//   - GenAI semconv message parts of type reasoning or thinking, which OTLP
//     ingest keeps as {"type":"thinking"} content parts (kind=thinking);
//   - reasoning_content / reasoning strings on chat messages (DeepSeek, vLLM,
//     OpenRouter);
//   - weft.reasoning span events and the SDK's weft.reasoning call attribute.
//
// Steps come from what a model returned (source=output) and from the
// assistant turns it was sent (source=input). Input steps usually repeat an
// earlier output step; queries hide those duplicates per session.

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Reasoning step kinds.
const (
	ReasoningThinking = "thinking"
	ReasoningSummary  = "summary"
	ReasoningRedacted = "redacted"
)

// Reasoning step sources.
const (
	ReasoningFromInput  = "input"
	ReasoningFromOutput = "output"
	ReasoningFromEvent  = "event"
)

const (
	maxReasoningSteps = 200      // per span
	maxReasoningText  = 64 << 10 // per step
)

// reasoningPart is one extracted step before it is stored.
type reasoningPart struct {
	Kind, Text, Hash string
	Tokens           int64 // 0: unknown
	Source           string
	AtUS             int64 // 0: the span's start
}

func normReasoningKind(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "summary", "reasoning_summary", "summary_text":
		return ReasoningSummary
	case "redacted", "redacted_thinking", "encrypted":
		return ReasoningRedacted
	}
	return ReasoningThinking
}

func reasoningHash(kind, text string) string {
	h := sha256.Sum256([]byte(kind + "\x00" + text))
	return hex.EncodeToString(h[:12])
}

type reasoningCollector struct {
	source string
	out    []reasoningPart
}

func (c *reasoningCollector) add(kind, text, hashOf string) {
	if len(c.out) >= maxReasoningSteps {
		return
	}
	text = strings.TrimSpace(text)
	if kind != ReasoningRedacted && text == "" {
		return
	}
	if len(text) > maxReasoningText {
		text = truncateText(text, maxReasoningText)
	}
	if hashOf == "" {
		hashOf = text
	}
	c.out = append(c.out, reasoningPart{Kind: kind, Text: text, Hash: reasoningHash(kind, hashOf), Source: c.source})
}

func firstStr(vs ...any) string {
	for _, v := range vs {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// textsOf returns the text of each element of a parts list (strings, or maps
// with text/content).
func textsOf(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			} else if m := asMap(x); m != nil {
				if s := firstStr(m["text"], m["content"]); s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	}
	return nil
}

// walk collects reasoning from a model output, a message, or a list of
// content parts. It only descends into keys that hold messages or parts.
func (c *reasoningCollector) walk(v any, depth int) {
	if depth > 6 || v == nil {
		return
	}
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			c.walk(x, depth+1)
		}
		return
	case map[string]any:
		typ, _ := t["type"].(string)
		switch typ {
		case "thinking":
			c.add(ReasoningThinking, firstStr(t["thinking"], t["content"], t["text"]), "")
			return
		case "redacted_thinking":
			c.add(ReasoningRedacted, "", firstStr(t["data"], t["signature"]))
			return
		case "reasoning":
			before := len(c.out)
			for _, s := range textsOf(t["summary"]) {
				c.add(ReasoningSummary, s, "")
			}
			switch ct := t["content"].(type) {
			case string: // GenAI semconv reasoning part
				c.add(ReasoningThinking, ct, "")
			case []any: // Responses reasoning_text parts
				for _, s := range textsOf(ct) {
					c.add(ReasoningThinking, s, "")
				}
			}
			if s, ok := t["text"].(string); ok {
				c.add(ReasoningThinking, s, "")
			}
			if len(c.out) == before {
				if enc := firstStr(t["encrypted_content"], t["id"]); enc != "" {
					c.add(ReasoningRedacted, "", enc)
				}
			}
			return
		}
		if isTruthy(t["thought"]) { // Gemini thought part
			if s, ok := t["text"].(string); ok {
				c.add(ReasoningThinking, s, "")
			}
			return
		}
		if s, ok := t["reasoning_content"].(string); ok {
			c.add(ReasoningThinking, s, "")
		}
		if s, ok := t["reasoning"].(string); ok {
			c.add(ReasoningThinking, s, "")
		}
		for _, k := range []string{"choices", "message", "candidates", "content", "parts", "output", "messages"} {
			if x, ok := t[k]; ok {
				if _, isStr := x.(string); !isStr {
					c.walk(x, depth+1)
				}
			}
		}
	}
}

func isTruthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true")
	}
	return false
}

func assistantRole(m map[string]any) bool {
	switch r, _ := m["role"].(string); strings.ToLower(r) {
	case "assistant", "model", "ai":
		return true
	}
	return false
}

// walkInputs collects reasoning from the assistant turns of a model's input.
func (c *reasoningCollector) walkInputs(inputs map[string]any) {
	for _, k := range []string{"messages", "input", "contents", "history"} {
		l, ok := inputs[k].([]any)
		if !ok {
			continue
		}
		for _, x := range l {
			m := asMap(x)
			if m == nil {
				continue
			}
			if assistantRole(m) || m["type"] == "reasoning" || m["type"] == "thinking" {
				c.walk(m, 0)
			}
		}
	}
}

// spanReasoningTokens is the reasoning token count a span reports, from its
// usage summary or the provider response.
func spanReasoningTokens(summary map[string]any, output any) int64 {
	var total float64
	if u, ok := summary["usage"].(map[string]any); ok {
		for _, x := range u {
			if m := asMap(x); m != nil {
				if f, ok := NormalizeUsage(m)["reasoning_tokens"]; ok && f > 0 {
					total += f
				}
			}
		}
	}
	if total == 0 {
		if m := asMap(output); m != nil {
			if u := asMap(m["usage"]); u != nil {
				if f, ok := NormalizeUsage(u)["reasoning_tokens"]; ok && f > 0 {
					total = f
				}
			}
		}
	}
	return int64(total)
}

// reasoningEvents reads weft.reasoning span events (OTLP) and the SDK's
// weft.reasoning call attribute ([{text, kind, tokens, time}]).
func reasoningEvents(attributes map[string]any) []reasoningPart {
	c := &reasoningCollector{source: ReasoningFromEvent}
	addOne := func(a map[string]any, at string) {
		text := firstStr(a["weft.reasoning.text"], a["text"], a["content"], a["reasoning"])
		kind := normReasoningKind(firstStr(a["weft.reasoning.kind"], a["kind"]))
		n := len(c.out)
		c.add(kind, text, firstStr(a["data"]))
		if len(c.out) == n {
			return
		}
		p := &c.out[len(c.out)-1]
		if f, ok := toFloat(firstNonNilAny(a["weft.reasoning.tokens"], a["tokens"])); ok && f > 0 {
			p.Tokens = int64(f)
		}
		if at == "" {
			at = firstStr(a["time"], a["timestamp"])
		}
		if at != "" {
			if us, err := ParseTime(at); err == nil {
				p.AtUS = us
			}
		}
	}
	if o := asMap(attributes["otel"]); o != nil {
		if evs, ok := o["events"].([]any); ok {
			for _, x := range evs {
				ev := asMap(x)
				if ev == nil || ev["name"] != "weft.reasoning" {
					continue
				}
				at, _ := ev["time"].(string)
				addOne(asMap(ev["attributes"]), at)
			}
		}
	}
	var sdk any
	if w := asMap(attributes["weft"]); w != nil {
		sdk = w["reasoning"]
	}
	if sdk == nil {
		sdk = attributes["weft.reasoning"]
	}
	switch t := sdk.(type) {
	case []any:
		for _, x := range t {
			if m := asMap(x); m != nil {
				addOne(m, "")
			} else if s, ok := x.(string); ok {
				addOne(map[string]any{"text": s}, "")
			}
		}
	case map[string]any:
		addOne(t, "")
	case string:
		addOne(map[string]any{"text": t}, "")
	}
	return c.out
}

// ExtractReasoning returns the reasoning steps of one call. isModel marks a
// model (chat) span, whose inputs and output are searched; any span can carry
// weft.reasoning events.
func extractReasoning(isModel bool, attributes, inputs map[string]any, output any, summary map[string]any) []reasoningPart {
	var out []reasoningPart
	if isModel {
		in := &reasoningCollector{source: ReasoningFromInput}
		in.walkInputs(inputs)
		out = append(out, in.out...)
		o := &reasoningCollector{source: ReasoningFromOutput}
		o.walk(output, 0)
		if tok := spanReasoningTokens(summary, output); tok > 0 && len(o.out) > 0 {
			o.out[0].Tokens = tok
		}
		out = append(out, o.out...)
	}
	out = append(out, reasoningEvents(attributes)...)
	if len(out) > maxReasoningSteps {
		out = out[:maxReasoningSteps]
	}
	return out
}
