package otlp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// ccTraceSpan is one span of a synthetic Claude Code traces (beta) export,
// built from the span and attribute names Claude Code documents (the beta
// produced no traces in our runs, so it could not be captured).
type ccTraceSpan struct {
	id, parent, name string
	startMs, endMs   int
	attrs            map[string]any
	events           []map[string]any
}

func otlpAttrs(m map[string]any) []any {
	var out []any
	for k, v := range m {
		var val map[string]any
		switch x := v.(type) {
		case string:
			val = map[string]any{"stringValue": x}
		case bool:
			val = map[string]any{"boolValue": x}
		case int:
			val = map[string]any{"intValue": fmt.Sprint(x)}
		case float64:
			val = map[string]any{"doubleValue": x}
		}
		out = append(out, map[string]any{"key": k, "value": val})
	}
	return out
}

func ccTracesBody(session string, spans []ccTraceSpan) []byte {
	const base = int64(1793952300000) // ms
	var list []any
	for _, s := range spans {
		attrs := map[string]any{"session.id": session, "span.type": s.name, "user.email": "dev@example.com"}
		for k, v := range s.attrs {
			attrs[k] = v
		}
		sp := map[string]any{"traceId": "5b8efff798038103d269b633813fc60c", "spanId": s.id, "name": s.name,
			"startTimeUnixNano": fmt.Sprint((base + int64(s.startMs)) * 1e6), "endTimeUnixNano": fmt.Sprint((base + int64(s.endMs)) * 1e6),
			"attributes": otlpAttrs(attrs)}
		if s.parent != "" {
			sp["parentSpanId"] = s.parent
		}
		var evs []any
		for _, e := range s.events {
			evs = append(evs, map[string]any{"name": e["name"], "timeUnixNano": fmt.Sprint((base + int64(s.endMs)) * 1e6),
				"attributes": otlpAttrs(e["attributes"].(map[string]any))})
		}
		if evs != nil {
			sp["events"] = evs
		}
		list = append(list, sp)
	}
	b, _ := json.Marshal(map[string]any{"resourceSpans": []any{map[string]any{
		"resource":   map[string]any{"attributes": otlpAttrs(map[string]any{"service.name": "claude-code", "service.version": "2.1.318"})},
		"scopeSpans": []any{map[string]any{"scope": map[string]any{"name": "com.anthropic.claude_code.tracing"}, "spans": list}}}}})
	return b
}

// ccTraceSpans is a turn with a Read call, a rejected Bash call, a failed
// Edit and an Agent call running a sub-agent, in documented names.
func ccTraceSpans(prompt string) []ccTraceSpan {
	return []ccTraceSpan{
		{id: "0000000000000001", name: "claude_code.interaction", startMs: 0, endMs: 9000,
			attrs: map[string]any{"user_prompt": prompt, "user_prompt_length": 20, "interaction.sequence": 1, "interaction.duration_ms": 9000}},
		{id: "0000000000000002", parent: "0000000000000001", name: "claude_code.llm_request", startMs: 10, endMs: 2000,
			attrs: map[string]any{"model": "claude-opus-5-5", "gen_ai.system": "anthropic", "gen_ai.request.model": "claude-opus-5-5",
				"input_tokens": 2, "output_tokens": 110, "cache_read_tokens": 26220, "cache_creation_tokens": 21113, "request_id": "req_1",
				"gen_ai.response.id": "req_1", "success": true, "stop_reason": "tool_use", "duration_ms": 1990, "ttft_ms": 900}},
		{id: "0000000000000003", parent: "0000000000000001", name: "claude_code.tool", startMs: 2000, endMs: 2100,
			attrs:  map[string]any{"tool_name": "Read", "tool_use_id": "toolu_read", "gen_ai.tool.call.id": "toolu_read", "file_path": "/repo/note.txt", "duration_ms": 100},
			events: []map[string]any{{"name": "tool.output", "attributes": map[string]any{"content": "hello\n"}}}},
		{id: "0000000000000004", parent: "0000000000000003", name: "claude_code.tool.execution", startMs: 2010, endMs: 2090,
			attrs: map[string]any{"tool_use_id": "toolu_read", "gen_ai.tool.call.id": "toolu_read", "success": true}},
		{id: "0000000000000005", parent: "0000000000000001", name: "claude_code.tool", startMs: 2200, endMs: 4000,
			attrs: map[string]any{"tool_name": "Bash", "tool_use_id": "toolu_bash", "gen_ai.tool.call.id": "toolu_bash", "full_command": "rm -rf /"}},
		{id: "0000000000000006", parent: "0000000000000005", name: "claude_code.tool.blocked_on_user", startMs: 2200, endMs: 4000,
			attrs: map[string]any{"decision": "reject", "source": "user_reject", "duration_ms": 1800}},
		{id: "0000000000000007", parent: "0000000000000001", name: "claude_code.tool", startMs: 4100, endMs: 4200,
			attrs: map[string]any{"tool_name": "Edit", "tool_use_id": "toolu_edit", "gen_ai.tool.call.id": "toolu_edit"}},
		{id: "0000000000000008", parent: "0000000000000007", name: "claude_code.tool.execution", startMs: 4110, endMs: 4190,
			attrs: map[string]any{"tool_use_id": "toolu_edit", "success": false, "error": "Error:ENOENT", "error_class": "Error_ENOENT"}},
		{id: "0000000000000009", parent: "0000000000000001", name: "claude_code.tool", startMs: 4300, endMs: 8000,
			attrs: map[string]any{"tool_name": "Agent", "tool_use_id": "toolu_agent", "gen_ai.tool.call.id": "toolu_agent"}},
		{id: "000000000000000a", parent: "0000000000000009", name: "claude_code.llm_request", startMs: 4400, endMs: 6000,
			attrs: map[string]any{"model": "claude-haiku-5-5", "gen_ai.request.model": "claude-haiku-5-5", "input_tokens": 10, "output_tokens": 20,
				"agent_id": "a1", "query_source_safe": "agent.builtin.general-purpose", "success": true}},
		{id: "000000000000000b", parent: "0000000000000009", name: "claude_code.tool", startMs: 6000, endMs: 6100,
			attrs: map[string]any{"tool_name": "Grep", "tool_use_id": "toolu_grep", "gen_ai.tool.call.id": "toolu_grep", "agent_id": "a1"}},
		{id: "000000000000000c", parent: "0000000000000001", name: "claude_code.llm_request", startMs: 8100, endMs: 8900,
			attrs: map[string]any{"model": "claude-opus-5-5", "input_tokens": 2, "output_tokens": 4, "success": false, "status_code": 529,
				"error": "Overloaded", "attempt": 3}},
	}
}

func TestClaudeCodeTracesMapping(t *testing.T) {
	for _, tc := range []struct {
		name, prompt string
		wantInputs   func(map[string]any) bool
	}{
		{"prompt exported", "list the files please", func(in map[string]any) bool {
			m, _ := in["messages"].([]any)
			return len(m) == 1 && m[0].(map[string]any)["content"] == "list the files please"
		}},
		{"prompt redacted", "<REDACTED>", func(in map[string]any) bool { return in[promptNotExportedKey] == promptNotExported }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := Decode(ccTracesBody("sess-tr", ccTraceSpans(tc.prompt)), true)
			if err != nil {
				t.Fatal(err)
			}
			calls, err := ToCalls(req, "acme/cc")
			if err != nil {
				t.Fatal(err)
			}
			by := map[string]store.CompleteCall{}
			for _, c := range calls {
				by[c.Start.ID] = c
				if deref(c.Start.ThreadID) != "sess-tr" {
					t.Fatalf("%s: session %v", c.Start.OpName, c.Start.ThreadID)
				}
				w, _ := c.Start.Attributes["weft"].(map[string]any)
				if w["source"] != store.SourceClaudeCodeTraces {
					t.Fatalf("%s: weft.source %v", c.Start.OpName, w)
				}
			}
			get := func(id string) (store.CompleteCall, map[string]any) {
				c := by[id]
				return c, c.Start.Attributes
			}
			turn, ta := get("0000000000000001")
			if ta["gen_ai.operation.name"] != "invoke_agent" || ta["gen_ai.agent.name"] != "claude-code" || deref(turn.Start.TurnID) != turn.Start.ID ||
				deref(turn.Start.DisplayName) != "invoke_agent claude-code" || !tc.wantInputs(turn.Start.Inputs) {
				t.Fatalf("turn: %v inputs %v", ta, turn.Start.Inputs)
			}
			llm, la := get("0000000000000002")
			u, _ := llm.End.Summary["usage"].(map[string]any)["claude-opus-5-5"].(map[string]any)
			if la["gen_ai.operation.name"] != "chat" || u["prompt_tokens"] != float64(2+26220+21113) || u["cache_read_input_tokens"] != float64(26220) ||
				u["completion_tokens"] != float64(110) || deref(llm.Start.DisplayName) != "chat claude-opus-5-5" {
				t.Fatalf("llm: %v usage %v", la, u)
			}
			read, ra := get("0000000000000003")
			if ra["gen_ai.operation.name"] != "execute_tool" || ra["gen_ai.tool.name"] != "Read" || ra["gen_ai.tool.call.id"] != "toolu_read" ||
				deref(read.Start.DisplayName) != "Read: /repo/note.txt" || read.End.Output != "hello\n" {
				t.Fatalf("read: %v display %q output %v", ra, deref(read.Start.DisplayName), read.End.Output)
			}
			blocked, _ := get("0000000000000006")
			if blocked.End.Exception == nil || *blocked.End.Exception != "Rejected by the user (user_reject)" {
				t.Fatalf("blocked_on_user exc = %v", blocked.End.Exception)
			}
			bash, _ := get("0000000000000005")
			if deref(bash.Start.DisplayName) != "Bash: rm -rf /" {
				t.Fatalf("bash display %q", deref(bash.Start.DisplayName))
			}
			exec, _ := get("0000000000000008")
			if exec.End.Exception == nil || *exec.End.Exception != "Error:ENOENT" {
				t.Fatalf("execution exc = %v", exec.End.Exception)
			}
			agent, aa := get("0000000000000009")
			if aa["gen_ai.operation.name"] != "invoke_agent" || aa["gen_ai.agent.name"] != "general-purpose" || aa["gen_ai.tool.call.id"] != "toolu_agent" ||
				deref(agent.Start.DisplayName) != "invoke_agent general-purpose" {
				t.Fatalf("agent: %v", aa)
			}
			sub, _ := get("000000000000000a")
			if deref(sub.Start.ParentID) != "0000000000000009" {
				t.Fatalf("sub-agent LLM parent %v", sub.Start.ParentID)
			}
			failed, _ := get("000000000000000c")
			if failed.End.Exception == nil || !strings.HasPrefix(*failed.End.Exception, "API error 529: Overloaded") {
				t.Fatalf("failed llm exc = %v", failed.End.Exception)
			}
			// Spans of other services are left alone.
			info := store.DeriveSpan(turn.Start.OpName, deref(turn.Start.DisplayName), turn.Start.ThreadID, turn.Start.TurnID, turn.Start.ID,
				turn.Start.Attributes, turn.Start.Inputs, nil)
			if info.Operation != store.OpInvokeAgent || !info.IsTurn || info.AgentName != "claude-code" || info.ConversationID != "sess-tr" {
				t.Fatalf("derived turn = %+v", info)
			}
			tool := store.DeriveSpan(read.Start.OpName, deref(read.Start.DisplayName), read.Start.ThreadID, nil, read.Start.ID, read.Start.Attributes, read.Start.Inputs, nil)
			if tool.Operation != store.OpExecuteTool || tool.ToolName != "Read" || tool.ToolCallID != "toolu_read" {
				t.Fatalf("derived tool = %+v", tool)
			}
		})
	}
}

func TestClaudeCodeTracesOnlyForClaudeCode(t *testing.T) {
	body := []byte(strings.ReplaceAll(string(ccTracesBody("s", ccTraceSpans("hi"))), `"claude-code"`, `"other-service"`))
	req, err := Decode(body, true)
	if err != nil {
		t.Fatal(err)
	}
	calls, err := ToCalls(req, "acme/cc")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		if _, ok := c.Start.Attributes["weft"]; ok {
			t.Fatalf("%s of another service was mapped as Claude Code", c.Start.OpName)
		}
	}
}
