package otlp

// GitHub Copilot CLI's built-in OpenTelemetry export, mapped onto the
// shape the coding-agent plugins produce (claudecode.go). Written from the
// documentation only (docs.github.com, Copilot CLI command reference,
// "OpenTelemetry monitoring"; VS Code's "Monitor agent usage with
// OpenTelemetry" for execute_hook): it has not been run against a real
// export.
//
//	invoke_agent           one per user message (the turn), gen_ai.conversation.id = session
//	├─ chat <model>        each model request, gen_ai.usage.*
//	├─ execute_tool <tool> each tool call, gen_ai.tool.call.id
//	│  └─ invoke_agent     a sub-agent (explore, task) under the tool call that started it
//	└─ execute_hook        a hook run (VS Code's harness; the CLI records hooks as span events)
//
// The spans already follow the GenAI conventions, which otlp.go reads; this
// adds what the conventions leave open: which invoke_agent is the turn, the
// agent's name ("copilot" for the default agent), the session on spans that
// do not carry gen_ai.conversation.id, and the invoke_agent token totals,
// which repeat the chat spans' and are not counted again.

import (
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

const (
	// CopilotAgent is the agent name of a Copilot CLI turn.
	CopilotAgent = "copilot"
	// copilotScope is Copilot's instrumentation scope (COPILOT_OTEL_SOURCE_NAME).
	copilotScope = "github.copilot"
)

// copilotServices are Copilot's documented service names: the CLI's
// default, the VS Code extension's, and the name vendor guides set.
var copilotServices = map[string]bool{"github-copilot": true, "copilot-chat": true, "copilot-cli": true}

// copilotDefaultAgents are names Copilot gives its default agent.
var copilotDefaultAgents = map[string]bool{"": true, "copilot": true, "copilotcli": true, "github copilot": true,
	"github-copilot": true, "github copilot chat": true, "github copilot cli": true}

// isCopilotScope reports spans of Copilot's own export: its service name or
// its instrumentation scope (either survives an OTEL_SERVICE_NAME change).
func isCopilotScope(resource map[string]any, scope string) bool {
	return copilotServices[aStr(resource, "service.name")] || scope == copilotScope
}

func copilotPromptNotExported() map[string]any {
	return map[string]any{promptNotExportedKey: "(prompt not exported: set OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true)"}
}

// mapCopilotSpan completes a converted Copilot span.
func mapCopilotSpan(c *store.CompleteCall, sp *tracepb.Span, resource map[string]any) {
	st, en := &c.Start, c.End
	o, _ := st.Attributes["otel"].(map[string]any)
	attrs, _ := o["attributes"].(map[string]any)
	events, _ := o["events"].([]map[string]any)
	user := st.Attributes
	setWeft := func(kind string) {
		w, _ := user["weft"].(map[string]any)
		if w == nil {
			w = map[string]any{}
		}
		w["span_kind"] = kind
		user["weft"] = w
	}
	display := func(d string) { st.DisplayName = &d }
	op := aStr(attrs, "gen_ai.operation.name")
	if op == "" {
		op = strings.Fields(sp.GetName() + " x")[0]
	}
	switch op {
	case "invoke_agent":
		// The turn is the top-level invocation (a root span, or the one
		// carrying server.address, which sub-agent invocations do not).
		topLevel := len(sp.GetParentSpanId()) == 0 || aStr(attrs, "server.address") != ""
		agent := aStr(attrs, "gen_ai.agent.name")
		if topLevel && copilotDefaultAgents[strings.ToLower(agent)] {
			agent = aStr(resource, "weft.agent.name")
			if agent == "" {
				agent = CopilotAgent
			}
		}
		if agent == "" {
			agent = "subagent"
		}
		setWeft("agent")
		user["gen_ai.operation.name"] = "invoke_agent"
		user["gen_ai.agent.name"] = agent
		display("invoke_agent " + agent)
		if topLevel {
			id := st.ID
			st.TurnID = &id
			st.ParentID = nil
		}
		// The turn's input is the user's message: the last user message of
		// gen_ai.input.messages (which can hold the history before it).
		if msgs, ok := st.Inputs["messages"].([]any); ok {
			for i := len(msgs) - 1; i >= 0; i-- {
				if m, ok := msgs[i].(map[string]any); ok && m["role"] == "user" {
					if t := contentText(m["content"]); t != "" {
						st.Inputs = userMessages(t)
					}
					break
				}
			}
		} else if _, ok := attrs["gen_ai.input.messages"]; !ok {
			st.Inputs = copilotPromptNotExported()
		}
		if en != nil {
			// Token totals of all the turn's model calls, which are
			// counted on the chat spans.
			delete(en.Summary, "usage")
		}
	case "chat":
		setWeft("llm")
	case "execute_tool":
		setWeft("tool")
		name := aStr(attrs, "gen_ai.tool.name")
		input := map[string]any{}
		for k, v := range st.Inputs {
			input[k] = v
		}
		// Shell and file tools name their command or file (content capture).
		for _, k := range []string{"command", "file_path", "mcp_server_name", "mcp_tool_name", "skill_name", "edit_type"} {
			if v := aStr(attrs, "github.copilot.tool.parameters."+k); v != "" {
				if _, ok := input[k]; !ok {
					input[k] = v
				}
			}
		}
		if len(input) > len(st.Inputs) && len(st.Inputs) == 0 {
			st.Inputs = input
		}
		if name != "" {
			display(toolDisplay(name, input))
		}
		if aStr(attrs, "gen_ai.tool.type") == "extension" {
			user["gen_ai.tool.type"] = "mcp"
		}
	case "execute_hook":
		setWeft("hook")
		hook := aStr(attrs, "copilot_chat.hook_type", "github.copilot.hook.type")
		if hook == "" {
			hook = "hook"
		}
		display("hook " + hook)
		if d := aStr(attrs, "github.copilot.hook.decision"); d != "" {
			user["github.copilot.hook.decision"] = d
		}
	}
	// Session errors are exception events with Copilot's own attributes.
	if en != nil && en.Exception != nil {
		for _, ev := range events {
			if ev["name"] != "exception" {
				continue
			}
			a, _ := ev["attributes"].(map[string]any)
			typ := aStr(a, "github.copilot.error_type")
			if typ == "" {
				continue
			}
			msg := *en.Exception
			if msg == "error" || msg == aStr(attrs, "error.type") {
				msg = strings.TrimPrefix(msg+": ", "error: ") + typ
				if code := aStr(a, "github.copilot.error_status_code"); code != "" {
					msg += " (status " + code + ")"
				}
				en.Exception = &msg
			}
			break
		}
	}
	if st.ThreadID != nil {
		user["gen_ai.conversation.id"] = *st.ThreadID
	}
}

// copilotSessions fills the session of Copilot spans that do not carry
// gen_ai.conversation.id (execute_tool, execute_hook) from their trace:
// each trace is one turn of one session.
func copilotSessions(calls []store.CompleteCall, from int) {
	byTrace := map[string]string{}
	for _, c := range calls[from:] {
		if c.Start.ThreadID != nil && *c.Start.ThreadID != "" {
			if _, ok := byTrace[c.Start.TraceID]; !ok {
				byTrace[c.Start.TraceID] = *c.Start.ThreadID
			}
		}
	}
	for i := from; i < len(calls); i++ {
		st := &calls[i].Start
		if st.ThreadID == nil {
			if s, ok := byTrace[st.TraceID]; ok {
				id := s
				st.ThreadID = &id
				st.Attributes["gen_ai.conversation.id"] = s
			}
		}
	}
}
