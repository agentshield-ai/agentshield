package otlp

// Shared pieces of the Claude Code mappings (claudecode_logs.go for the
// log events, claudecode_traces.go for the traces beta). Both produce the
// spans the weft-claude-code plugin produces (plugins/weft-claude-code), so
// the session story, agents, teams and AgentShield matching read them the
// same way:
//
//	invoke_agent claude-code     one per prompt (a turn); session = Claude Code's session.id
//	├─ chat <model>              each API request, with tokens and cost
//	├─ execute_tool <tool>       each tool call, gen_ai.tool.call.id = the tool-use id
//	└─ invoke_agent <type>       a sub-agent (under its Agent tool call)

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// Claude Code's resource service name and its events' instrumentation scope.
const (
	ClaudeCodeService     = "claude-code"
	ClaudeCodeEventsScope = "com.anthropic.claude_code.events"
	// ClaudeCodeAgent is the agent name of a Claude Code turn (the plugin's default).
	ClaudeCodeAgent = "claude-code"
)

// promptNotExported stands in for a prompt Claude Code did not export. It
// is stored under an input key starting with "_", which the session story
// does not read as the user's message.
const (
	promptNotExportedKey = "_prompt"
	promptNotExported    = "(prompt not exported: set OTEL_LOG_USER_PROMPTS=1)"
)

// ccID derives a span id (UUID form) from stable Claude Code ids. ns keeps
// the ids of each source apart from each other and from the plugin's.
func ccID(ns string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(ns))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	x := hex.EncodeToString(h.Sum(nil))
	d, _ := strconv.ParseUint(x[16:17], 16, 8)
	v := d&3 | 8
	return fmt.Sprintf("%s-%s-4%s-%x%s-%s", x[0:8], x[8:12], x[13:16], v, x[17:20], x[20:32])
}

// redacted reports a content attribute Claude Code withheld.
func redacted(s string) bool {
	t := strings.TrimSpace(s)
	return t == "" || strings.EqualFold(t, "<REDACTED>") || strings.EqualFold(t, "[REDACTED]")
}

func aStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case json.Number:
			return v.String()
		case bool:
			return strconv.FormatBool(v)
		}
	}
	return ""
}

func aNum(m map[string]any, key string) (float64, bool) {
	switch v := m[key].(type) {
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil
	case nil:
		return 0, false
	default:
		return store.ToFloat(v)
	}
}

func aBool(m map[string]any, key string) (val, ok bool) {
	switch v := m[key].(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1":
			return true, true
		case "false", "0":
			return false, true
		}
	}
	return false, false
}

// jsonMap decodes a JSON-object attribute (tool_input, tool_parameters).
func jsonMap(s string) map[string]any {
	if s == "" {
		return nil
	}
	if m, ok := parseMaybeJSON(s).(map[string]any); ok {
		return m
	}
	return nil
}

func usToTime(us int64) string { return time.UnixMicro(us).UTC().Format(time.RFC3339Nano) }

func shortText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// toolDisplay names a tool span: the tool plus its most telling argument
// (as the plugins do).
func toolDisplay(name string, input map[string]any) string {
	for _, k := range []string{"command", "full_command", "cmd", "file_path", "path", "pattern", "url", "query", "description", "prompt"} {
		if v, ok := input[k]; ok && v != nil && v != "" {
			s, isStr := v.(string)
			if !isStr {
				b, _ := json.Marshal(v)
				s = string(b)
			}
			return name + ": " + shortText(s, 50)
		}
	}
	return name
}

// ccUsage is summary.usage for one Claude Code API request. The prompt
// count is gross (input + cache reads + cache writes), as Weft counts it;
// cost_usd is kept as the request's reported cost.
func ccUsage(model string, a map[string]any) map[string]any {
	in, _ := aNum(a, "input_tokens")
	out, _ := aNum(a, "output_tokens")
	cr, _ := aNum(a, "cache_read_tokens")
	cw, _ := aNum(a, "cache_creation_tokens")
	if in+out+cr+cw == 0 {
		return nil
	}
	e := map[string]any{"requests": 1, "prompt_tokens": in + cr + cw, "completion_tokens": out, "total_tokens": in + cr + cw + out}
	if cr > 0 {
		e["cache_read_input_tokens"] = cr
	}
	if cw > 0 {
		e["cache_creation_input_tokens"] = cw
	}
	if c, ok := aNum(a, "cost_usd"); ok && c >= 0 {
		e[store.UsageReportedCost] = c
		e[store.UsageReportedRequests] = 1
	}
	if model == "" {
		model = "unknown"
	}
	return map[string]any{"usage": map[string]any{model: e}}
}

// chatOutput is a chat-completion shaped output (content: text or blocks).
func chatOutput(model string, content any, finish any) map[string]any {
	out := map[string]any{"object": "chat.completion", "choices": []any{map[string]any{"index": 0,
		"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": finish}}}
	if model != "" {
		out["model"] = model
	}
	return out
}

func userMessages(text string) map[string]any {
	return map[string]any{"messages": []any{map[string]any{"role": "user", "content": text}}}
}

func assistantMessages(text string) map[string]any {
	return map[string]any{"messages": []any{map[string]any{"role": "assistant", "content": text}}}
}

// isAgentTool reports Claude Code's sub-agent tools.
func isAgentTool(name string) bool { return name == "Agent" || name == "Task" }

// subagentType reads a sub-agent's type from query_source
// ("agent:builtin:general-purpose", or query_source_safe
// "agent.builtin.general-purpose").
func subagentTypeFromSource(qs string) string {
	for _, sep := range []string{":", "."} {
		if rest, ok := strings.CutPrefix(qs, "agent"+sep); ok {
			if i := strings.Index(rest, sep); i >= 0 {
				return rest[i+1:]
			}
			return rest
		}
	}
	return ""
}

// mcpAttrs marks MCP tools as the plugin does.
func mcpAttrs(name string, attrs map[string]any) {
	if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
		attrs["gen_ai.tool.type"] = "mcp"
		if i := strings.Index(rest, "__"); i > 0 {
			attrs["mcp.server.name"] = rest[:i]
		}
	}
}
