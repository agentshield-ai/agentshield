package otlp

// OpenCode's built-in OpenTelemetry export (OTEL_EXPORTER_OTLP_ENDPOINT, plus
// "experimental": {"openTelemetry": true} in opencode.json for the AI SDK
// spans), mapped onto the shape the coding-agent plugins produce
// (claudecode.go):
//
//	invoke_agent opencode    SessionPrompt.prompt: one per user prompt (a turn)
//	├─ ai.streamText         an AI SDK call (chain), session.id = OpenCode's session
//	│  └─ chat <model>       ai.streamText.doStream: the model request, tokens
//	│     └─ <tool>: <arg>   ai.toolCall: the tool call, gen_ai.tool.call.id
//	└─ ...
//	   invoke_agent <type>   a sub-agent's prompt (task tool), under its task call,
//	                         in the sub-agent's own (child) session
//
// Captured from opencode-ai 1.19.5. OpenCode traces every Effect function
// (database queries, file system, plugin loading, config, HTTP), about a
// thousand spans per prompt; Weft keeps the turn and the AI SDK spans and
// drops the rest. Kept spans whose parent is dropped are re-parented: an
// AI SDK call to its turn, a turn to nothing (it is the run's top-level span)
// or, for a sub-agent, to the task call that started it. Because an
// export batch may hold a call without its turn (OTLP sends children
// first), the turn's and the tool calls' span ids are derived from ids
// both ends know (trace id, session id, tool-call id), not looked up.
//
// Tool.execute (OpenCode's own tool span) is dropped too: ai.toolCall
// describes the same call with its arguments and result.

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

const (
	// OpenCodeService is OpenCode's resource service.name.
	OpenCodeService = "opencode"
	// OpenCodeAgent is the agent name of an OpenCode turn.
	OpenCodeAgent = "opencode"
	// ocTurnSpan is the span OpenCode opens for each user prompt.
	ocTurnSpan = "SessionPrompt.prompt"
)

func isOpenCodeResource(resource map[string]any) bool {
	return aStr(resource, "service.name") == OpenCodeService
}

// ocID derives a 16-hex span id from ids both a span and its relatives know.
func ocID(kind string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte("opencode-" + kind))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// aiRootOps are the AI SDK operations OpenCode calls directly (their parent
// is an OpenCode span, which is dropped).
var aiRootOps = map[string]bool{"ai.streamText": true, "ai.generateText": true, "ai.streamObject": true,
	"ai.generateObject": true, "ai.embed": true, "ai.embedMany": true}

// ocSpan is one span of an OpenCode export with its attributes.
type ocSpan struct {
	sp     *tracepb.Span
	id     string
	parent string
	trace  string
	attrs  map[string]any
}

// openCodeBatch indexes the spans of one OpenCode resource in an export.
type openCodeBatch struct {
	byID     map[string]*ocSpan
	children map[string][]*ocSpan
	// toolArgs: tool-call id → the AI SDK's arguments of that call.
	toolArgs map[string]map[string]any
}

func newOpenCodeBatch(rs *tracepb.ResourceSpans) *openCodeBatch {
	b := &openCodeBatch{byID: map[string]*ocSpan{}, children: map[string][]*ocSpan{}, toolArgs: map[string]map[string]any{}}
	for _, ss := range rs.GetScopeSpans() {
		for _, sp := range ss.GetSpans() {
			s := &ocSpan{sp: sp, id: hex.EncodeToString(sp.GetSpanId()), parent: hex.EncodeToString(sp.GetParentSpanId()),
				trace: hex.EncodeToString(sp.GetTraceId()), attrs: KeyValues(sp.GetAttributes())}
			b.byID[s.id] = s
			if s.parent != "" {
				b.children[s.parent] = append(b.children[s.parent], s)
			}
			if sp.GetName() == "ai.toolCall" {
				if id := aStr(s.attrs, "ai.toolCall.id"); id != "" {
					b.toolArgs[id] = jsonMap(aStr(s.attrs, "ai.toolCall.args", "ai.toolCall.input"))
				}
			}
		}
	}
	return b
}

// keep reports the spans Weft stores: the turns and the AI SDK spans.
func (b *openCodeBatch) keep(sp *tracepb.Span) bool {
	n := sp.GetName()
	return n == ocTurnSpan || strings.HasPrefix(n, "ai.")
}

func ocSession(attrs map[string]any) string {
	return aStr(attrs, "session.id", "ai.telemetry.metadata.sessionId")
}

// sessionFromPath reads the session id from an OpenCode HTTP route
// (/session/<id>/message, /session/<id>/prompt_async, ...).
func sessionFromPath(p string) string {
	rest, ok := strings.CutPrefix(p, "/session/")
	if !ok {
		return ""
	}
	if i := strings.IndexByte(rest, '/'); i > 0 {
		rest = rest[:i]
	} else {
		return ""
	}
	if strings.HasPrefix(rest, ":") {
		return ""
	}
	return rest
}

// turnContext finds, for a SessionPrompt.prompt span, its session and,
// for a sub-agent's prompt (run by the task tool), the task's tool-call id.
func (b *openCodeBatch) turnContext(s *ocSpan) (session, taskCall string, nested bool) {
	// Ancestors: the task tool that runs a sub-agent, or the HTTP request
	// that carries the session id in its path.
	for p, n := b.byID[s.parent], 0; p != nil && n < 64; p, n = b.byID[p.parent], n+1 {
		name := p.sp.GetName()
		if name == ocTurnSpan {
			nested = true
			break
		}
		if name == "Tool.execute" || strings.HasPrefix(name, "TaskTool.") {
			nested = true
			if name == "Tool.execute" {
				taskCall = aStr(p.attrs, "tool.call_id")
			}
			if taskCall != "" {
				break
			}
			continue
		}
		if session == "" && !nested {
			session = sessionFromPath(aStr(p.attrs, "url.path"))
		}
	}
	if nested {
		session = ""
	}
	if session == "" {
		session = b.descendantSession(s)
	}
	return session, taskCall, nested
}

// descendantSession is the session of the first span under s that names
// one, without entering nested prompts or tool executions (a task tool's
// spans name the parent session, its sub-agent's the child's).
func (b *openCodeBatch) descendantSession(s *ocSpan) string {
	queue := append([]*ocSpan(nil), b.children[s.id]...)
	for i := 0; i < len(queue) && i < 4096; i++ {
		c := queue[i]
		if c.sp.GetName() == ocTurnSpan {
			continue
		}
		if id := ocSession(c.attrs); id != "" {
			return id
		}
		queue = append(queue, b.children[c.id]...)
	}
	return ""
}

// turnMessages reads the turn's prompt and answer from the last model call
// under it in this batch (title generation excluded).
func (b *openCodeBatch) turnMessages(s *ocSpan) (prompt, answer string) {
	var last *ocSpan
	queue := append([]*ocSpan(nil), b.children[s.id]...)
	for i := 0; i < len(queue) && i < 4096; i++ {
		c := queue[i]
		name := c.sp.GetName()
		if name == ocTurnSpan || name == "SessionPrompt.ensureTitle" || name == "Tool.execute" {
			continue
		}
		if aiRootOps[name] {
			if last == nil || c.sp.GetEndTimeUnixNano() > last.sp.GetEndTimeUnixNano() {
				last = c
			}
			continue
		}
		queue = append(queue, b.children[c.id]...)
	}
	if last == nil {
		return "", ""
	}
	if in := frameworkInputs(last.attrs); in != nil {
		msgs, _ := in["messages"].([]any)
		for i := len(msgs) - 1; i >= 0; i-- {
			m, _ := msgs[i].(map[string]any)
			if m["role"] == "user" {
				prompt = contentText(m["content"])
				break
			}
		}
	}
	if fr := aStr(last.attrs, "ai.response.finishReason"); fr == "" || fr == "stop" {
		answer = aStr(last.attrs, "ai.response.text")
	}
	return prompt, answer
}

// titleCall reports the model call OpenCode makes to title a new session
// (under SessionPrompt.ensureTitle): it is kept, so its tokens count, but
// not told as a step of the conversation.
func (b *openCodeBatch) titleCall(s *ocSpan) bool {
	for p, n := s, 0; p != nil && n < 16; p, n = b.byID[p.parent], n+1 {
		name := p.sp.GetName()
		if name == "SessionPrompt.ensureTitle" {
			return true
		}
		if name == ocTurnSpan || name == "SessionProcessor.process" {
			return false
		}
		if strings.HasPrefix(name, "ai.") {
			for _, k := range []string{"ai.prompt.messages", "ai.prompt"} {
				if strings.Contains(shortText(aStr(p.attrs, k), 400), "You are a title generator") {
					return true
				}
			}
		}
	}
	return false
}

// contentText joins the text of a message content (a string or parts).
func contentText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var out []string
		for _, p := range t {
			if m, ok := p.(map[string]any); ok {
				if s, ok := m["text"].(string); ok && s != "" {
					out = append(out, s)
				}
			} else if s, ok := p.(string); ok {
				out = append(out, s)
			}
		}
		return strings.Join(out, "\n")
	}
	return ""
}

// mapSpan rewrites a kept OpenCode span (converted by spanToCall) into the
// plugin's shape.
func (b *openCodeBatch) mapSpan(c *store.CompleteCall, sp *tracepb.Span, resource map[string]any) {
	st, en := &c.Start, c.End
	s := b.byID[st.ID]
	if s == nil {
		return
	}
	user := st.Attributes
	attrs := s.attrs
	setWeft := func(kind string) {
		w, _ := user["weft"].(map[string]any)
		if w == nil {
			w = map[string]any{}
		}
		if kind != "" {
			w["span_kind"] = kind
		}
		user["weft"] = w
	}
	display := func(d string) { st.DisplayName = &d }
	setThread := func(id string) {
		if id != "" {
			st.ThreadID = &id
			user["gen_ai.conversation.id"] = id
		}
	}
	name := sp.GetName()
	switch {
	case name == ocTurnSpan:
		session, taskCall, nested := b.turnContext(s)
		if session != "" {
			st.ID = ocID("turn", s.trace, session)
		}
		setThread(session)
		turn := st.ID
		st.TurnID = &turn
		st.ParentID = nil
		agent := aStr(resource, "weft.agent.name", "gen_ai.agent.name")
		if agent == "" {
			agent = OpenCodeAgent
		}
		if nested {
			agent = "subagent"
			if args := b.toolArgs[taskCall]; args != nil {
				if t := aStr(args, "subagent_type"); t != "" {
					agent = t
				}
			}
			if taskCall != "" {
				p := ocID("tool", s.trace, taskCall)
				st.ParentID = &p
			}
		}
		setWeft("agent")
		user["gen_ai.operation.name"] = "invoke_agent"
		user["gen_ai.agent.name"] = agent
		display("invoke_agent " + agent)
		prompt, answer := b.turnMessages(s)
		if prompt != "" {
			st.Inputs = userMessages(prompt)
		} else {
			st.Inputs = map[string]any{promptNotExportedKey: "(prompt not exported)"}
		}
		if en != nil && answer != "" {
			en.Output = assistantMessages(answer)
		}
	case aiRootOps[name]:
		st.ParentID = nil
		if session := ocSession(attrs); session != "" {
			p := ocID("turn", s.trace, session)
			st.ParentID = &p
		}
		if b.titleCall(s) {
			display("session title")
		}
	case strings.HasSuffix(name, ".doStream") || strings.HasSuffix(name, ".doGenerate"):
		model := aStr(attrs, "gen_ai.request.model", "ai.model.id")
		if model != "" {
			user["gen_ai.request.model"] = model
		}
		if b.titleCall(s) {
			// A model call, but not part of the conversation.
			setWeft("chain")
			user["opencode.purpose"] = "session_title"
			display("session title " + model)
			break
		}
		setWeft("llm")
		user["gen_ai.operation.name"] = "chat"
		if model != "" {
			display("chat " + model)
		}
	case name == "ai.toolCall":
		tool := aStr(attrs, "ai.toolCall.name")
		callID := aStr(attrs, "ai.toolCall.id")
		if callID != "" {
			st.ID = ocID("tool", s.trace, callID)
			user["gen_ai.tool.call.id"] = callID
		}
		setWeft("tool")
		user["gen_ai.operation.name"] = "execute_tool"
		user["gen_ai.tool.name"] = tool
		mcpAttrs(tool, user)
		input := map[string]any{}
		for k, v := range st.Inputs {
			input[k] = v
		}
		if fp, ok := input["filePath"]; ok {
			if _, has := input["file_path"]; !has {
				input["file_path"] = fp
			}
		}
		display(toolDisplay(tool, input))
		if en != nil {
			if m, ok := en.Output.(map[string]any); ok {
				// OpenCode's tool result: {title, metadata, output}; the
				// output text is what the model saw.
				if out, ok := m["output"]; ok {
					if md, ok := m["metadata"].(map[string]any); ok {
						user["opencode.tool.metadata"] = md
					}
					en.Output = out
				}
			}
		}
	}
	if _, ok := user["gen_ai.conversation.id"]; !ok && st.ThreadID != nil {
		user["gen_ai.conversation.id"] = *st.ThreadID
	}
}
