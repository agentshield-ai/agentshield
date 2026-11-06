package otlp

// Claude Code's traces (beta: CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1,
// OTEL_TRACES_EXPORTER=otlp) mapped onto the plugin's model, from the
// span and attribute names Claude Code documents:
//
//	claude_code.interaction            the turn: invoke_agent claude-code (user_prompt, gated)
//	claude_code.llm_request            chat <model>: input/output/cache tokens, success, error
//	claude_code.tool                   execute_tool <tool_name>, gen_ai.tool.call.id = tool_use_id;
//	                                   the Agent (or Task) tool is the sub-agent's invoke_agent span,
//	                                   as in the plugin, with the sub-agent's spans nested under it
//	claude_code.tool.blocked_on_user   kept as a step; a rejected decision marks it failed
//	claude_code.tool.execution         kept as a step; success=false marks it failed
//
// The spans carry weft.source = claude-code-otel-traces, which ranks them
// below the plugin and above the log events for the same session
// (store/sources.go).

import (
	"encoding/hex"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// isClaudeCodeResource reports Claude Code's own resource.
func isClaudeCodeResource(resource map[string]any) bool {
	return aStr(resource, "service.name") == ClaudeCodeService
}

// claudeCodeSubagentNames finds, for each Agent tool span id of an export,
// the sub-agent type its child spans name (query_source_safe
// "agent.builtin.general-purpose", agent.name), for when the tool span's
// subagent_type is not exported (OTEL_LOG_TOOL_DETAILS off).
func claudeCodeSubagentNames(spans []*tracepb.Span) map[string]string {
	out := map[string]string{}
	for _, sp := range spans {
		if len(sp.GetParentSpanId()) == 0 || !strings.HasPrefix(sp.GetName(), "claude_code.") {
			continue
		}
		a := KeyValues(sp.GetAttributes())
		if aStr(a, "agent_id") == "" {
			continue
		}
		name := aStr(a, "agent.name")
		if name == "" {
			name = subagentTypeFromSource(aStr(a, "query_source_safe", "query_source"))
		}
		if name != "" {
			out[hex.EncodeToString(sp.GetParentSpanId())] = name
		}
	}
	return out
}

// mapClaudeCodeSpan rewrites a converted claude_code.* span into the
// plugin's shape. attrs are the span's OTel attributes, user the call's own
// attributes (st.Attributes), events its span events.
func mapClaudeCodeSpan(st *store.CallStart, en *store.CallEnd, spanName string, attrs, resource, user map[string]any, events []map[string]any, subagents map[string]string) {
	if !strings.HasPrefix(spanName, "claude_code.") {
		return
	}
	setWeft := func(kind string) {
		w, _ := user["weft"].(map[string]any)
		if w == nil {
			w = map[string]any{}
		}
		w["source"] = store.SourceClaudeCodeTraces
		if kind != "" {
			w["span_kind"] = kind
		}
		user["weft"] = w
	}
	display := func(s string) { st.DisplayName = &s }
	fail := func(msg string) {
		if en != nil && en.Exception == nil {
			en.Exception = &msg
		}
	}
	if st.ThreadID != nil {
		user["gen_ai.conversation.id"] = *st.ThreadID
	}
	switch spanName {
	case "claude_code.interaction":
		agent := aStr(resource, "weft.agent.name", "gen_ai.agent.name")
		if agent == "" {
			agent = ClaudeCodeAgent
		}
		setWeft("agent")
		user["gen_ai.operation.name"] = "invoke_agent"
		user["gen_ai.agent.name"] = agent
		user["gen_ai.provider.name"] = "anthropic"
		id := st.ID
		st.TurnID = &id
		display("invoke_agent " + agent)
		if p := aStr(attrs, "user_prompt"); !redacted(p) {
			st.Inputs = userMessages(p)
		} else {
			st.Inputs = map[string]any{promptNotExportedKey: promptNotExported}
		}
	case "claude_code.llm_request":
		model := aStr(attrs, "model", "gen_ai.request.model")
		user["gen_ai.provider.name"] = "anthropic"
		if model != "" {
			user["gen_ai.request.model"] = model
		}
		if purpose := sidePurposeOf(aStr(attrs, "query_source_safe", "query_source")); purpose != "" {
			// Beside the conversation (session title, compaction): tokens and
			// cost count, but it is not a conversation step.
			setWeft("chain")
			user["claude_code.purpose"] = purpose
			display(strings.TrimSpace(purpose + " " + model))
		} else {
			setWeft("llm")
			user["gen_ai.operation.name"] = "chat"
			if model != "" {
				display("chat " + model)
			}
		}
		if en != nil {
			if u := ccUsage(model, attrs); u != nil {
				en.Summary["usage"] = u["usage"]
			}
			if out := aStr(attrs, "response.model_output"); !redacted(out) {
				en.Output = chatOutput(model, out, aStr(attrs, "stop_reason"))
			}
			if ok, has := aBool(attrs, "success"); has && !ok {
				fail(strings.TrimSpace("API error " + strings.Join(nonEmpty(aStr(attrs, "status_code"), aStr(attrs, "error", "error_class")), ": ")))
			}
		}
	case "claude_code.tool":
		name := aStr(attrs, "tool_name")
		input := map[string]any{}
		if m := jsonMap(aStr(attrs, "tool_input")); m != nil {
			input = m
		}
		for _, k := range []string{"file_path", "full_command", "skill_name", "subagent_type"} {
			if v := aStr(attrs, k); v != "" {
				if _, ok := input[k]; !ok {
					input[k] = v
				}
			}
		}
		if c, ok := input["full_command"]; ok {
			if _, has := input["command"]; !has {
				input["command"] = c
				delete(input, "full_command")
			}
		}
		user["gen_ai.tool.name"] = name
		if id := aStr(attrs, "tool_use_id", "gen_ai.tool.call.id"); id != "" {
			user["gen_ai.tool.call.id"] = id
		}
		if isAgentTool(name) {
			typ := aStr(attrs, "subagent_type")
			if typ == "" {
				typ = subagents[st.ID]
			}
			if typ == "" {
				typ = "subagent"
			}
			setWeft("agent")
			user["gen_ai.operation.name"] = "invoke_agent"
			user["gen_ai.agent.name"] = typ
			display("invoke_agent " + typ)
			if p, ok := input["prompt"].(string); ok && p != "" {
				st.Inputs = userMessages(p)
			} else {
				st.Inputs = input
			}
		} else {
			setWeft("tool")
			user["gen_ai.operation.name"] = "execute_tool"
			mcpAttrs(name, user)
			display(toolDisplay(name, input))
			st.Inputs = input
		}
		if en != nil {
			for _, ev := range events {
				if ev["name"] != "tool.output" {
					continue
				}
				a, _ := ev["attributes"].(map[string]any)
				out := map[string]any{}
				for _, k := range []string{"output", "content", "diff"} {
					if v, ok := a[k]; ok {
						out[k] = v
					}
				}
				if len(out) == 1 {
					for _, v := range out {
						en.Output = v
					}
				} else if len(out) > 1 {
					en.Output = out
				}
			}
		}
	case "claude_code.tool.blocked_on_user":
		setWeft("")
		display("Waiting for permission")
		switch d := aStr(attrs, "decision"); d {
		case "reject", "cancelled":
			msg := "Rejected by the user"
			if d == "cancelled" {
				msg = "Cancelled by the user"
			}
			if src := aStr(attrs, "source"); src != "" {
				msg += " (" + src + ")"
			}
			fail(msg)
		}
	case "claude_code.tool.execution":
		setWeft("")
		display("Run")
		if ok, has := aBool(attrs, "success"); has && !ok {
			msg := aStr(attrs, "error", "error_class")
			if msg == "" {
				msg = "tool failed"
			}
			fail(msg)
		}
	default:
		setWeft("")
	}
}
