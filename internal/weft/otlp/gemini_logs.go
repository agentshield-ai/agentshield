package otlp

// Google Gemini CLI's OpenTelemetry log events (GEMINI_TELEMETRY_* with an
// OTLP/HTTP endpoint) as Weft spans. A turn is the run of events from one
// gemini_cli.user_prompt to the next (store.LogEvent.Opens): Gemini's
// prompt_id is not unique per turn (a resumed non-interactive session
// reuses the session id), and a sub-agent's events carry
// prompt_id "<agent id>#<n>". Events are stored as they arrive and the
// turn's spans are rebuilt from all of them each time
// (store.IngestLogEvents), so batches may be split, re-sent or out of order.
//
// Event → span:
//
//	gemini_cli.user_prompt     the turn (invoke_agent gemini-cli): start, prompt text (log prompts on)
//	gemini_cli.api_response    chat <model>: tokens (input, cached, output, thoughts), start = time -
//	                           duration_ms, the reply text and function calls (response_text, log
//	                           prompts on); role utility_* marks side requests (routing, checks)
//	gemini_cli.api_error       chat <model>, failed (Gemini CLI retries it)
//	gemini_cli.api_request     a request in flight (no span of its own)
//	gemini_cli.tool_call       execute_tool <tool>: arguments (log prompts on), success, error,
//	                           decision (reject: "Rejected by the user"); gen_ai.tool.call.id from
//	                           the function call of the response that asked for it
//	gemini_cli.agent.start /   invoke_agent <sub-agent> (generalist, codebase_investigator, ...)
//	gemini_cli.agent.finish    under the invoke_agent call that ran it
//
// Gemini CLI exports no cost (Weft prices the tokens) and no tool output.
//
// The turn is stored once it is over: its last main request answered
// without a function call (or failed, or the user rejected a tool), or a
// later prompt started (then it was interrupted).

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
)

// Gemini CLI's resource service name (and instrumentation scope), its
// event-log source name and the agent name of a turn.
const (
	GeminiService   = "gemini-cli"
	GeminiLogSource = "gemini-cli"
	GeminiAgent     = "gemini-cli"
	geminiLogsNS    = "weft/gemini-cli/otel-logs"

	geminiPromptNotExported = "(prompt not exported: set GEMINI_TELEMETRY_LOG_PROMPTS=true)"
)

// geminiEvents are the events that describe a session; others (config,
// startup_stats, model_routing, file_operation, the gen_ai.client.*
// payload copies, ...) are ignored.
var geminiEvents = map[string]bool{"gemini_cli.user_prompt": true, "gemini_cli.api_request": true, "gemini_cli.api_response": true,
	"gemini_cli.api_error": true, "gemini_cli.tool_call": true, "gemini_cli.agent.start": true, "gemini_cli.agent.finish": true}

// geminiDropAttrs are attributes no span uses: identity, and request
// payloads (each request repeats the whole conversation).
var geminiDropAttrs = map[string]bool{"user.email": true, "installation.id": true, "event.name": true, "event.timestamp": true,
	"request_text": true, "gen_ai.input.messages": true, "gen_ai.system_instructions": true, "gen_ai.tool.definitions": true}

// GeminiLogEvents extracts Gemini CLI session events from a logs export.
// The project is chosen as for traces (ProjectFor).
func GeminiLogEvents(req *collogs.ExportLogsServiceRequest, headerProject string) (LogBatch, error) {
	out := LogBatch{Events: map[string][]store.LogEvent{}}
	seq := int64(0)
	for _, rl := range req.GetResourceLogs() {
		resource := KeyValues(rl.GetResource().GetAttributes())
		var project string
		for _, sl := range rl.GetScopeLogs() {
			for _, lr := range sl.GetLogRecords() {
				seq++
				raw := KeyValues(lr.GetAttributes())
				name := aStr(raw, "event.name")
				session := aStr(raw, "session.id")
				if session == "" {
					session = aStr(resource, "session.id")
				}
				switch {
				case !geminiEvents[name]:
					out.ignore(IgnoredNoiseEvent, 1)
					continue
				case session == "":
					out.ignore(IgnoredNoSession, 1)
					continue
				}
				if project == "" {
					p, err := refs.NormalizeProject(ProjectFor(headerProject, resource))
					if err != nil {
						return out, err
					}
					project = p
				}
				key := logEventKey(name, session, aStr(raw, "event.timestamp"), raw)
				attrs := map[string]any{}
				for k, v := range raw {
					if !geminiDropAttrs[k] {
						attrs[k] = v
					}
				}
				if a := aStr(resource, "weft.agent.name", "gen_ai.agent.name"); a != "" {
					attrs["weft.agent.name"] = a
				}
				e := store.LogEvent{Key: key, SessionID: session, Name: name, TimeUS: logEventTime(raw, lr.GetTimeUnixNano(), lr.GetObservedTimeUnixNano()),
					Seq: seq, Attrs: attrs}
				if name == "gemini_cli.user_prompt" {
					e.Opens, e.GroupID = true, "prompt:"+key
				}
				out.Events[project] = append(out.Events[project], e)
			}
		}
	}
	return out, nil
}

// geminiSubagent is the sub-agent a prompt id belongs to ("<agent id>#<n>"),
// or "" for the main agent (whose interactive prompt ids are
// "<session>########<n>").
func geminiSubagent(promptID string) string {
	i := strings.IndexByte(promptID, '#')
	if i <= 0 {
		return ""
	}
	n := promptID[i+1:]
	if n == "" {
		return ""
	}
	for _, c := range n {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return promptID[:i]
}

// ---------------------------------------------------------------- building

type gmCall struct {
	name, id string
	args     any
	claimed  bool
}

type gmLLM struct {
	ev       *store.LogEvent // api_response or api_error
	agent    string
	role     string
	text     string
	calls    []*gmCall
	tools    []*gmTool
	failed   bool
	errorMsg string
}

type gmTool struct {
	ev             *store.LogEvent
	agent, name    string
	callID         string
	startUS, endUS int64
	input          map[string]any
	rejected       bool
	failed         bool
	errText        string
}

type gmAgent struct {
	id, name        string
	start, finish   *store.LogEvent
	firstUS, lastUS int64
	llms            []*gmLLM
	tools           []*gmTool
	requests        int // api_request events of conversation requests
	answered        int // their responses and errors
	parentTool      *gmTool
}

// conversational reports a request of an agent's conversation (not a side
// request such as model routing or a next-speaker check).
func geminiConversational(role string) bool {
	return role == "" || role == "main" || role == "subagent"
}

// GeminiLogTitle names a Gemini CLI session after its first prompt.
func GeminiLogTitle(first []store.LogEvent) string {
	for _, e := range first {
		if e.Name == "gemini_cli.user_prompt" {
			if t := aStr(e.Attrs, "prompt"); !redacted(t) {
				return shortText(t, 80)
			}
			return ""
		}
	}
	return ""
}

// BuildGeminiLogGroup builds one turn's spans from its events.
func BuildGeminiLogGroup(g store.LogGroup) []store.CompleteCall {
	sid := g.SessionID
	evs := append([]store.LogEvent(nil), g.Events...)
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].TimeUS != evs[j].TimeUS {
			return evs[i].TimeUS < evs[j].TimeUS
		}
		return evs[i].Seq < evs[j].Seq
	})
	if len(evs) == 0 {
		return nil
	}
	w := &spanWriter{session: sid, turnID: ccID(geminiLogsNS, sid, "turn", g.GroupID), source: store.SourceGeminiLogs}
	agentName := GeminiAgent
	provider := "gcp.gemini"
	agents := map[string]*gmAgent{}
	var agentOrder []*gmAgent
	var prompt *store.LogEvent
	var tools []*gmTool
	var llms []*gmLLM
	firstUS, lastUS := evStart(&evs[0]), evs[0].TimeUS
	agentFor := func(id string, e *store.LogEvent) *gmAgent {
		a := agents[id]
		if a == nil {
			a = &gmAgent{id: id, firstUS: evStart(e), lastUS: e.TimeUS}
			agents[id] = a
			agentOrder = append(agentOrder, a)
		}
		if s := evStart(e); s < a.firstUS {
			a.firstUS = s
		}
		if e.TimeUS > a.lastUS {
			a.lastUS = e.TimeUS
		}
		return a
	}
	agentFor("", &evs[0])
	for i := range evs {
		e := &evs[i]
		if s := evStart(e); s < firstUS {
			firstUS = s
		}
		if e.TimeUS > lastUS {
			lastUS = e.TimeUS
		}
		if a := aStr(e.Attrs, "weft.agent.name"); a != "" {
			agentName = a
		}
		if aStr(e.Attrs, "auth_type") == "vertex-ai" {
			provider = "gcp.vertex_ai"
		}
		id := geminiSubagent(aStr(e.Attrs, "prompt_id"))
		if strings.HasPrefix(e.Name, "gemini_cli.agent.") {
			id = aStr(e.Attrs, "agent_id")
		}
		ag := agentFor(id, e)
		role := aStr(e.Attrs, "role")
		switch e.Name {
		case "gemini_cli.user_prompt":
			if prompt == nil {
				prompt = e
			}
		case "gemini_cli.agent.start":
			ag.start = e
			ag.name = aStr(e.Attrs, "agent_name")
		case "gemini_cli.agent.finish":
			ag.finish = e
			if n := aStr(e.Attrs, "agent_name"); n != "" {
				ag.name = n
			}
		case "gemini_cli.api_request":
			if geminiConversational(role) {
				ag.requests++
			}
		case "gemini_cli.api_response", "gemini_cli.api_error":
			l := &gmLLM{ev: e, agent: ag.id, role: role}
			if geminiConversational(role) {
				ag.answered++
			}
			if e.Name == "gemini_cli.api_error" {
				l.failed = true
				l.errorMsg = geminiErrorText(aStr(e.Attrs, "error", "error.message"))
			} else if rt := aStr(e.Attrs, "response_text"); rt != "" {
				l.text, l.calls = geminiResponse(rt)
			}
			ag.llms = append(ag.llms, l)
			llms = append(llms, l)
		case "gemini_cli.tool_call":
			t := &gmTool{ev: e, agent: ag.id, name: aStr(e.Attrs, "function_name")}
			t.endUS = e.TimeUS
			t.startUS = evStart(e)
			if st, ok := aNum(e.Attrs, "start_time"); ok && st > 0 {
				t.startUS = int64(st * 1000)
			}
			if et, ok := aNum(e.Attrs, "end_time"); ok && et > 0 {
				t.endUS = int64(et * 1000)
			}
			t.input = jsonMap(aStr(e.Attrs, "function_args"))
			if t.input == nil {
				t.input = map[string]any{}
			}
			if strings.EqualFold(aStr(e.Attrs, "decision"), "reject") {
				t.rejected = true
			}
			if ok, has := aBool(e.Attrs, "success"); has && !ok {
				t.failed = true
				t.errText = aStr(e.Attrs, "error.message", "error")
				if t.errText == "" {
					t.errText = "tool failed"
				}
			}
			ag.tools = append(ag.tools, t)
			tools = append(tools, t)
		}
	}

	// Which response asked for which tool call (and its call id): the
	// agent's latest response at or before the call with an unclaimed
	// function call of that name, else its latest response.
	for _, t := range tools {
		ag := agents[t.agent]
		var asked *gmLLM
		for i := len(ag.llms) - 1; i >= 0 && asked == nil; i-- {
			l := ag.llms[i]
			if l.ev.TimeUS > t.startUS {
				continue
			}
			for _, c := range l.calls {
				if !c.claimed && c.name == t.name {
					c.claimed, t.callID, asked = true, c.id, l
					if t.input == nil || len(t.input) == 0 {
						if m, ok := c.args.(map[string]any); ok {
							t.input = m
						}
					}
					break
				}
			}
		}
		if asked == nil {
			for i := len(ag.llms) - 1; i >= 0; i-- {
				if l := ag.llms[i]; l.ev.TimeUS <= t.startUS && geminiConversational(l.role) && !l.failed {
					asked = l
					break
				}
			}
		}
		if asked != nil {
			asked.tools = append(asked.tools, t)
		}
	}

	// Is an agent's run over? No request is waiting, and its last
	// conversation response asked for no tool (or failed, or the user
	// rejected its last tool call); a sub-agent's finish event ends it.
	finished := func(a *gmAgent) (bool, *string) {
		if a == nil {
			return false, nil
		}
		if a.finish != nil {
			if r := aStr(a.finish.Attrs, "terminate_reason"); r != "" && r != "GOAL" {
				return true, strPtr("sub-agent stopped: " + r)
			}
			return true, nil
		}
		if a.requests > a.answered {
			return false, nil
		}
		var last *gmLLM
		for _, l := range a.llms {
			if geminiConversational(l.role) {
				last = l
			}
		}
		if last == nil {
			return false, nil
		}
		if len(a.tools) > 0 {
			lt := a.tools[len(a.tools)-1]
			if lt.rejected && lt.endUS >= last.ev.TimeUS {
				return true, nil
			}
		}
		switch {
		case len(last.tools) > 0 || len(last.calls) > 0:
			return false, nil
		case last.failed:
			msg := "API error"
			if last.errorMsg != "" {
				msg += ": " + last.errorMsg
			}
			return true, &msg
		}
		return true, nil
	}
	root := agents[""]
	closed, turnErr := finished(root)
	if !closed && g.Later {
		closed, turnErr = true, strPtr("interrupted: a new prompt was submitted")
	}

	// Sub-agents: under the invoke_agent call whose run holds their start.
	agentSpan := func(id string) string { return ccID(geminiLogsNS, sid, "agent", id) }
	toolSpan := func(t *gmTool) string { return ccID(geminiLogsNS, sid, "tool", t.ev.Key) }
	for _, a := range agentOrder {
		if a.id == "" {
			continue
		}
		at := a.firstUS
		if a.start != nil {
			at = a.start.TimeUS
		}
		for _, t := range tools {
			if t.agent == a.id || !(t.name == "invoke_agent" || (a.name != "" && t.name == a.name)) {
				continue
			}
			if t.startUS > at+1000 || t.endUS < at {
				continue
			}
			if want, _ := t.input["agent_name"].(string); want != "" && a.name != "" && want != a.name {
				continue
			}
			if a.parentTool == nil || t.startUS > a.parentTool.startUS {
				a.parentTool = t
			}
		}
	}
	parentOf := func(agent string) string {
		if agent == "" {
			return w.turnID
		}
		return agentSpan(agent)
	}

	// The turn.
	if closed {
		inputs := map[string]any{promptNotExportedKey: geminiPromptNotExported}
		gm := map[string]any{"prompt_exported": false}
		startUS := firstUS
		if prompt != nil {
			startUS = prompt.TimeUS
			if t := aStr(prompt.Attrs, "prompt"); !redacted(t) {
				inputs = userMessages(t)
				gm["prompt_exported"] = true
			}
			if n, ok := aNum(prompt.Attrs, "prompt_length"); ok {
				gm["prompt_length"] = n
			}
			if p := aStr(prompt.Attrs, "prompt_id"); p != "" {
				gm["prompt_id"] = p
			}
			if a := aStr(prompt.Attrs, "auth_type"); a != "" {
				gm["auth_type"] = a
			}
		}
		attrs := w.common(map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": agentName,
			"gen_ai.provider.name": provider, "weft": w.weft("agent"), "gemini_cli": gm})
		var output any
		for _, l := range root.llms {
			if geminiConversational(l.role) {
				if m := aStr(l.ev.Attrs, "model"); m != "" {
					attrs["gen_ai.request.model"] = m
				}
				if l.text != "" {
					output = assistantMessages(l.text)
				}
			}
		}
		w.add(w.turnID, "", "gemini_cli.turn", "invoke_agent "+agentName, startUS, attrs, inputs, lastUS, true, output, turnErr, nil)
	}

	// Sub-agent spans.
	for _, a := range agentOrder {
		if a.id == "" {
			continue
		}
		parent := w.turnID
		inputs := map[string]any{}
		if a.parentTool != nil {
			parent = toolSpan(a.parentTool)
			if p, ok := a.parentTool.input["prompt"].(string); ok && p != "" {
				inputs = userMessages(p)
			}
		}
		name := a.name
		if name == "" {
			name = "subagent"
		}
		done, exc := finished(a)
		end := a.lastUS
		if a.finish != nil {
			end = a.finish.TimeUS
		}
		if !done && closed {
			done, exc = true, strPtr("sub-agent did not finish before the turn ended")
		}
		gm := map[string]any{"agent_id": a.id}
		if a.finish != nil {
			for _, k := range []string{"terminate_reason", "turn_count", "duration_ms"} {
				if v, ok := a.finish.Attrs[k]; ok {
					gm[k] = v
				}
			}
		}
		var output any
		for _, l := range a.llms {
			if l.text != "" {
				output = assistantMessages(l.text)
			}
		}
		// A sub-agent hands its result back through complete_task.
		for _, t := range a.tools {
			if t.name != "complete_task" || len(t.input) == 0 {
				continue
			}
			var v any = t.input
			if len(t.input) == 1 || (len(t.input) == 2 && t.input["wait_for_previous"] != nil) {
				for k, x := range t.input {
					if k != "wait_for_previous" {
						v = x
					}
				}
			}
			if s, ok := v.(string); ok {
				output = assistantMessages(s)
			} else {
				output = assistantMessages(compactJSON(v))
			}
		}
		attrs := w.common(map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": name, "gen_ai.agent.id": a.id,
			"gen_ai.provider.name": provider, "weft": w.weft("agent"), "gemini_cli": gm})
		start := a.firstUS
		if a.start != nil && a.start.TimeUS < start {
			start = a.start.TimeUS
		}
		w.add(agentSpan(a.id), parent, "gemini_cli.subagent", "invoke_agent "+name, start, attrs, inputs, end, done, output, exc, nil)
	}

	// Tool spans.
	for _, t := range tools {
		gm := map[string]any{}
		for _, k := range []string{"decision", "tool_type", "content_length", "duration_ms", "error_type", "mcp_server_name", "extension_name", "prompt_id"} {
			if v, ok := t.ev.Attrs[k]; ok && v != nil && v != "" {
				gm[k] = v
			}
		}
		if md := jsonMap(aStr(t.ev.Attrs, "metadata")); md != nil {
			gm["metadata"] = md
		}
		attrs := w.common(map[string]any{"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": t.name, "weft": w.weft("tool"), "gemini_cli": gm})
		if t.callID != "" {
			attrs["gen_ai.tool.call.id"] = t.callID
		}
		if aStr(t.ev.Attrs, "tool_type") == "mcp" {
			attrs["gen_ai.tool.type"] = "mcp"
			if srv := aStr(t.ev.Attrs, "mcp_server_name"); srv != "" {
				attrs["mcp.server.name"] = srv
			}
		}
		var exc *string
		switch {
		case t.rejected:
			exc = strPtr("Rejected by the user")
		case t.failed:
			if strings.Contains(aStr(t.ev.Attrs, "error_type"), "policy") {
				exc = strPtr("Rejected by the policy: " + t.errText)
			} else {
				exc = &t.errText
			}
		}
		w.add(toolSpan(t), parentOf(t.agent), "gemini_cli.tool."+t.name, toolDisplay(t.name, t.input), t.startUS, attrs, t.input, t.endUS, true, nil, exc, nil)
	}

	// LLM spans.
	for _, l := range llms {
		e := l.ev
		m := aStr(e.Attrs, "model")
		gm := map[string]any{}
		for _, k := range []string{"role", "duration_ms", "status_code", "finish_reasons", "prompt_id", "error_type"} {
			if v, ok := e.Attrs[k]; ok && v != nil {
				gm[k] = v
			}
		}
		attrs := w.common(map[string]any{"gen_ai.operation.name": "chat", "gen_ai.provider.name": provider, "weft": w.weft("llm"), "gemini_cli": gm})
		if m != "" {
			attrs["gen_ai.request.model"] = m
		}
		var blocks []any
		var names []string
		if l.text != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": l.text})
		}
		for _, c := range l.calls {
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": c.id, "name": c.name, "input": c.args})
			names = append(names, c.name)
		}
		if len(l.calls) == 0 {
			for _, t := range l.tools {
				names = append(names, t.name)
			}
		}
		display := "chat " + m
		switch {
		case !geminiConversational(l.role):
			display += " (" + l.role + ")"
		case l.text != "":
			display = shortText(l.text, 60)
		case len(names) > 0:
			display = "→ " + strings.Join(names, ", ")
		}
		var output any
		var exc *string
		var summary map[string]any
		if l.failed {
			msg := "API error"
			if sc := aStr(e.Attrs, "status_code"); sc != "" {
				msg += " " + sc
			}
			if l.errorMsg != "" {
				msg += ": " + l.errorMsg
			}
			exc = &msg
		} else {
			if len(blocks) > 0 {
				finish := "stop"
				if len(l.calls) > 0 {
					finish = "tool_calls"
				}
				output = chatOutput(m, blocks, finish)
			}
			in, _ := aNum(e.Attrs, "input_token_count")
			tool, _ := aNum(e.Attrs, "tool_token_count")
			out, _ := aNum(e.Attrs, "output_token_count")
			th, _ := aNum(e.Attrs, "thoughts_token_count")
			cr, _ := aNum(e.Attrs, "cached_content_token_count")
			summary = usageSummary(m, in+tool, out+th, cr, 0, th)
		}
		w.add(ccID(geminiLogsNS, sid, "llm", e.Key), parentOf(l.agent), "gemini_cli.llm", display, evStart(e), attrs, map[string]any{"model": m}, e.TimeUS, true, output, exc, summary)
	}
	return w.out
}

// geminiResponse reads the reply text and function calls from an
// api_response's response_text (the response, or the list of streamed
// chunks, as JSON). Thought parts are left out.
func geminiResponse(rt string) (string, []*gmCall) {
	var v any
	dec := json.NewDecoder(strings.NewReader(rt))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return "", nil
	}
	chunks, ok := v.([]any)
	if !ok {
		chunks = []any{v}
	}
	var text strings.Builder
	var calls []*gmCall
	for _, ch := range chunks {
		m, _ := ch.(map[string]any)
		cands, _ := m["candidates"].([]any)
		if len(cands) == 0 {
			continue
		}
		c0, _ := cands[0].(map[string]any)
		content, _ := c0["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		for _, p := range parts {
			pm, _ := p.(map[string]any)
			if th, _ := pm["thought"].(bool); th {
				continue
			}
			if s, ok := pm["text"].(string); ok {
				text.WriteString(s)
			}
			if fc, ok := pm["functionCall"].(map[string]any); ok {
				c := &gmCall{args: fc["args"]}
				c.name, _ = fc["name"].(string)
				c.id, _ = fc["id"].(string)
				calls = append(calls, c)
			}
		}
	}
	return strings.TrimSpace(text.String()), calls
}

// geminiErrorText shortens an API error ({"error":{"code":…,"message":…}}).
func geminiErrorText(s string) string {
	if m := jsonMap(s); m != nil {
		if e, ok := m["error"].(map[string]any); ok {
			if msg, ok := e["message"].(string); ok && msg != "" {
				if st, ok := e["status"].(string); ok && st != "" {
					return st + ": " + msg
				}
				return msg
			}
		}
	}
	return shortText(s, 300)
}
