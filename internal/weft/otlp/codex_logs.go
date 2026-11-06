package otlp

// OpenAI Codex CLI's OpenTelemetry log events ([otel] exporter in
// config.toml) as Weft spans. Codex's events carry the session
// (conversation.id) but no turn id, so a turn is the run of events from one
// codex.user_prompt to the next (store.LogEvent.Opens). Its events are
// stored as they arrive and the turn's spans are rebuilt from all of them
// each time (store.IngestLogEvents), so batches may be split, re-sent or out
// of order.
//
// Event → span:
//
//	codex.user_prompt          the turn (invoke_agent codex): start, prompt text (log_user_prompt = true)
//	codex.api_request          chat <model>: one per HTTP request; start = time - duration_ms;
//	                           a 4xx/5xx answer is a failed chat span (Codex retries it)
//	codex.sse_event            response.completed: ends the oldest open request of its
//	(codex.websocket_event)    session with its tokens (input, cached, output, reasoning)
//	codex.tool_decision        approved: folded into the tool span; denied / abort: the tool
//	                           span, failed "Rejected by the user" (or by the approval policy)
//	codex.tool_result          execute_tool <tool>: gen_ai.tool.call.id = call_id, arguments,
//	                           output, success, start = time - duration_ms
//	codex.agent_response       the reply text, when Codex exports it
//	(sub-agent session)        a spawn_agent call starts a session of its own; its events are
//	                           stored in the parent's session (LinkChild) and shown as
//	                           invoke_agent codex-subagent under the spawn_agent call
//
// Codex does not export the model's reply text in its log events (only the
// tool calls and their outputs), nor a cost (Weft prices the tokens).
//
// The turn is stored once it is over: its last request finished without
// asking for a tool (or failed, or the user rejected a tool), or a later
// prompt started (then it was interrupted). Until then its spans hang
// under a turn that has not arrived, which Weft reads as running.

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
)

// CodexLogSource is the event-log source name of Codex's events.
const CodexLogSource = "codex"

const (
	codexLogsNS = "weft/codex/otel-logs"
	// CodexAgent is the agent name of a Codex turn (the weft-codex plugin's default).
	CodexAgent = "codex"
	// CodexSubagent is the agent name of a sub-agent Codex spawned.
	CodexSubagent          = "codex-subagent"
	codexPromptNotExported = "(prompt not exported: set log_user_prompt = true under [otel])"
)

// codexEvents are the events that describe a session; others
// (conversation_starts, startup_phase, turn_ttft, ...) are ignored.
var codexEvents = map[string]bool{"codex.user_prompt": true, "codex.api_request": true, "codex.websocket_request": true,
	"codex.sse_event": true, "codex.websocket_event": true, "codex.tool_decision": true, "codex.tool_result": true,
	"codex.agent_response": true}

// codexKeep reports a session event worth storing: a stream event only when
// it completes a response (with its token counts) or reports an error.
func codexKeep(name string, raw map[string]any) bool {
	if !codexEvents[name] {
		return false
	}
	if name == "codex.sse_event" || name == "codex.websocket_event" {
		kind := aStr(raw, "event.kind")
		_, tokens := raw["input_token_count"]
		return (kind == "response.completed" && tokens) || aStr(raw, "error.message") != "" || strings.Contains(kind, "failed") || kind == "error"
	}
	return true
}

// codexDropAttr reports attributes no span uses (account identity, auth
// state): they are not stored.
func codexDropAttr(k string) bool {
	return strings.HasPrefix(k, "auth.") || strings.HasPrefix(k, "user.") || k == "event.name" || k == "event.timestamp" || k == "terminal.type"
}

// CodexLogEvents extracts Codex session events from a logs export. The
// project is chosen as for traces (ProjectFor).
func CodexLogEvents(req *collogs.ExportLogsServiceRequest, headerProject string) (LogBatch, error) {
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
				session := aStr(raw, "conversation.id")
				switch {
				case !codexKeep(name, raw):
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
				out.Events[project] = append(out.Events[project], codexLogEvent(name, session, raw, resource, seq, lr.GetTimeUnixNano(), lr.GetObservedTimeUnixNano()))
			}
		}
	}
	return out, nil
}

func codexLogEvent(name, session string, raw, resource map[string]any, seq int64, timeNs, observedNs uint64) store.LogEvent {
	key := logEventKey(name, session, aStr(raw, "event.timestamp"), raw)
	attrs := map[string]any{}
	for k, v := range raw {
		if !codexDropAttr(k) {
			attrs[k] = v
		}
	}
	if v := aStr(resource, "service.version"); v != "" {
		attrs["service.version"] = v
	}
	if a := aStr(resource, "weft.agent.name", "gen_ai.agent.name"); a != "" {
		attrs["weft.agent.name"] = a
	}
	e := store.LogEvent{Key: key, SessionID: session, Name: name, TimeUS: logEventTime(raw, timeNs, observedNs), Seq: seq, Attrs: attrs}
	switch name {
	case "codex.user_prompt":
		e.Opens, e.GroupID = true, "prompt:"+key
	case "codex.tool_result":
		if aStr(raw, "tool_name") == "spawn_agent" {
			e.LinkChild = spawnedAgent(aStr(raw, "output"))
		}
	}
	return e
}

var agentIDPattern = regexp.MustCompile(`"agent_id"\s*:\s*"([^"]+)"`)

// spawnedAgent reads the session id of the agent a spawn_agent call started
// from its output ({"agent_id":"…","nickname":"…"}).
func spawnedAgent(output string) string {
	if m := jsonMap(output); m != nil {
		if id, ok := m["agent_id"].(string); ok {
			return id
		}
	}
	if m := agentIDPattern.FindStringSubmatch(output); m != nil {
		return m[1]
	}
	return ""
}

// ---------------------------------------------------------------- building

type cxTool struct {
	id, name, agent  string
	decision, result *store.LogEvent
	startUS, endUS   int64
	input            map[string]any
	output           any
	rejected         bool
	rejectMsg        string
	failed           bool
	errText          string
	child, nickname  string
}

// firstUS is when the tool call was first seen.
func (t *cxTool) firstUS() int64 {
	first := int64(0)
	if t.result != nil {
		first = evStart(t.result)
	}
	if t.decision != nil && (first == 0 || t.decision.TimeUS < first) {
		first = t.decision.TimeUS
	}
	return first
}

type cxLLM struct {
	agent     string
	req, done *store.LogEvent
	failed    bool
	errText   string
	tools     []*cxTool
}

type cxAgent struct {
	id              string
	prompt          *store.LogEvent
	firstUS, lastUS int64
	response        string
	llms, pending   []*cxLLM
	tools           []*cxTool
}

// BuildCodexLogGroup builds one turn's spans from its events.
func BuildCodexLogGroup(g store.LogGroup) []store.CompleteCall {
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
	w := &spanWriter{session: sid, turnID: ccID(codexLogsNS, sid, "turn", g.GroupID), source: store.SourceCodexLogs}
	for _, e := range g.First {
		if e.Name == "codex.user_prompt" && aStr(e.Attrs, "conversation.id") == sid {
			if t := aStr(e.Attrs, "prompt"); !redacted(t) {
				w.convName = shortText(t, 80)
			}
			break
		}
	}

	agentName := CodexAgent
	agents := map[string]*cxAgent{}
	var agentOrder []*cxAgent
	tools := map[string]*cxTool{}
	var toolOrder []*cxTool
	firstUS, lastUS := evStart(&evs[0]), evs[0].TimeUS
	version, originator := "", ""
	agentFor := func(e *store.LogEvent) *cxAgent {
		id := aStr(e.Attrs, "conversation.id")
		if id == "" {
			id = sid
		}
		a := agents[id]
		if a == nil {
			a = &cxAgent{id: id, firstUS: evStart(e), lastUS: e.TimeUS}
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
	for i := range evs {
		e := &evs[i]
		if s := evStart(e); s < firstUS {
			firstUS = s
		}
		if e.TimeUS > lastUS {
			lastUS = e.TimeUS
		}
		if v := aStr(e.Attrs, "app.version", "service.version"); v != "" {
			version = v
		}
		if o := aStr(e.Attrs, "originator"); o != "" {
			originator = o
		}
		if a := aStr(e.Attrs, "weft.agent.name"); a != "" {
			agentName = a
		}
		ag := agentFor(e)
		switch e.Name {
		case "codex.user_prompt":
			if ag.prompt == nil {
				ag.prompt = e
			}
		case "codex.api_request", "codex.websocket_request":
			l := &cxLLM{agent: ag.id, req: e}
			code, _ := aNum(e.Attrs, "http.response.status_code")
			if msg := aStr(e.Attrs, "error.message"); code >= 400 || msg != "" {
				l.failed, l.done = true, e
				l.errText = strings.TrimSpace(strings.Join(nonEmpty(aStr(e.Attrs, "http.response.status_code"), msg), ": "))
			} else {
				ag.pending = append(ag.pending, l)
			}
			ag.llms = append(ag.llms, l)
		case "codex.sse_event", "codex.websocket_event":
			// A completion without its request (not arrived yet) waits for it.
			if len(ag.pending) == 0 {
				continue
			}
			l := ag.pending[0]
			ag.pending = ag.pending[1:]
			l.done = e
			if msg := aStr(e.Attrs, "error.message"); msg != "" || aStr(e.Attrs, "event.kind") != "response.completed" {
				l.failed = true
				l.errText = strings.TrimSpace(strings.Join(nonEmpty(aStr(e.Attrs, "event.kind"), msg), ": "))
			}
		case "codex.tool_decision", "codex.tool_result":
			id := aStr(e.Attrs, "call_id")
			if id == "" {
				id = "event:" + e.Key
			}
			t := tools[id]
			if t == nil {
				t = &cxTool{id: id, agent: ag.id}
				tools[id] = t
				toolOrder = append(toolOrder, t)
				ag.tools = append(ag.tools, t)
			}
			if n := aStr(e.Attrs, "tool_name"); n != "" {
				t.name = n
			}
			if e.Name == "codex.tool_decision" {
				t.decision = e
			} else {
				t.result = e
			}
		case "codex.agent_response":
			if s := aStr(e.Attrs, "response", "response_text", "message"); !redacted(s) {
				ag.response = s
			}
		}
	}

	// Tools: timing, input, outcome; and which request asked for them (the
	// agent's last request recorded at or before the call).
	for _, t := range toolOrder {
		if t.result != nil {
			t.startUS, t.endUS = evStart(t.result), t.result.TimeUS
			args := aStr(t.result.Attrs, "arguments")
			if m := jsonMap(args); m != nil {
				t.input = m
			} else if args != "" {
				t.input = map[string]any{"input": args}
			}
			out := aStr(t.result.Attrs, "output")
			if out != "" {
				t.output = parseMaybeJSON(out)
			}
			if ok, has := aBool(t.result.Attrs, "success"); has && !ok {
				t.failed = true
				t.errText = shortText(out, 300)
				if t.errText == "" {
					t.errText = "tool failed"
				}
			}
			if t.name == "spawn_agent" {
				t.child = spawnedAgent(out)
				if m := jsonMap(out); m != nil {
					t.nickname, _ = m["nickname"].(string)
				}
			}
		}
		if t.decision != nil {
			if t.startUS == 0 || t.decision.TimeUS < t.startUS {
				t.startUS = t.decision.TimeUS
			}
			d := strings.ToLower(aStr(t.decision.Attrs, "decision"))
			if strings.HasPrefix(d, "denied") || d == "abort" || d == "timed_out" {
				t.rejected = true
				switch src := strings.ToLower(aStr(t.decision.Attrs, "source")); {
				case d == "abort":
					t.rejectMsg = "Rejected: the user aborted the turn"
				case d == "timed_out":
					t.rejectMsg = "Rejected: the approval request timed out"
				case src == "config":
					t.rejectMsg = "Rejected by the approval policy"
				case strings.Contains(src, "review"):
					t.rejectMsg = "Rejected by the automatic reviewer"
				default:
					t.rejectMsg = "Rejected by the user"
				}
				if t.endUS == 0 {
					t.endUS = t.decision.TimeUS
				}
			}
		}
		if t.input == nil {
			t.input = map[string]any{}
		}
		ag := agents[t.agent]
		var asked *cxLLM
		for _, l := range ag.llms {
			if l.req != nil && l.req.TimeUS <= t.firstUS() {
				asked = l
			}
		}
		if asked != nil {
			asked.tools = append(asked.tools, t)
		}
	}

	// Is an agent's run over? Its last request finished asking for no tool,
	// or failed, or the user rejected its last tool call.
	finished := func(a *cxAgent) (bool, *string) {
		if a == nil || len(a.pending) > 0 || len(a.llms) == 0 {
			return false, nil
		}
		last := a.llms[len(a.llms)-1]
		if len(a.tools) > 0 {
			lt := a.tools[len(a.tools)-1]
			if lt.rejected && lt.result == nil && lt.firstUS() >= last.req.TimeUS {
				return true, nil
			}
		}
		switch {
		case len(last.tools) > 0:
			return false, nil
		case last.failed:
			msg := "API error"
			if last.errText != "" {
				msg += ": " + last.errText
			}
			return true, &msg
		}
		return last.done != nil, nil
	}
	root := agents[sid]
	closed, turnErr := finished(root)
	if !closed && g.Later {
		closed, turnErr = true, strPtr("interrupted: a new prompt was submitted")
	}

	// Sub-agents: under the spawn_agent call that started them.
	spawnOf := map[string]*cxTool{}
	for _, t := range toolOrder {
		if t.child != "" {
			spawnOf[t.child] = t
		}
	}
	agentSpan := func(id string) string { return ccID(codexLogsNS, sid, "agent", id) }
	parentOf := func(agent string) string {
		if agent == sid {
			return w.turnID
		}
		return agentSpan(agent)
	}

	// The turn.
	model := ""
	if root != nil {
		for _, l := range root.llms {
			if m := aStr(l.req.Attrs, "model"); m != "" {
				model = m
			}
		}
	}
	if closed {
		inputs := map[string]any{promptNotExportedKey: codexPromptNotExported}
		cx := map[string]any{"prompt_exported": false}
		startUS := firstUS
		if root != nil && root.prompt != nil {
			startUS = root.prompt.TimeUS
			if t := aStr(root.prompt.Attrs, "prompt"); !redacted(t) {
				inputs = userMessages(t)
				cx["prompt_exported"] = true
			}
			if n, ok := aNum(root.prompt.Attrs, "prompt_length"); ok {
				cx["prompt_length"] = n
			}
		}
		if originator != "" {
			cx["originator"] = originator
		}
		if version != "" {
			cx["version"] = version
		}
		attrs := w.common(map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": agentName,
			"gen_ai.provider.name": "openai", "weft": w.weft("agent"), "codex": cx})
		if version != "" {
			attrs["gen_ai.agent.version"] = version
		}
		if model != "" {
			attrs["gen_ai.request.model"] = model
		}
		var output any
		if root != nil && root.response != "" {
			output = assistantMessages(root.response)
		}
		w.add(w.turnID, "", "codex.turn", "invoke_agent "+agentName, startUS, attrs, inputs, lastUS, true, output, turnErr, nil)
	}

	// Sub-agent spans.
	for _, a := range agentOrder {
		if a.id == sid {
			continue
		}
		parent := w.turnID
		nickname := ""
		if sp := spawnOf[a.id]; sp != nil {
			parent = ccID(codexLogsNS, sid, "tool", sp.id)
			nickname = sp.nickname
		}
		for _, t := range a.tools {
			if n := aStr(firstAttrs(t), "agent_name"); n != "" && n != "/root" && nickname == "" {
				nickname = n
			}
		}
		done, exc := finished(a)
		end := a.lastUS
		if !done && closed {
			done, exc = true, strPtr("sub-agent did not finish before the turn ended")
		}
		inputs := map[string]any{}
		if a.prompt != nil {
			if t := aStr(a.prompt.Attrs, "prompt"); !redacted(t) {
				inputs = userMessages(t)
			}
		}
		var output any
		if a.response != "" {
			output = assistantMessages(a.response)
		} else if text := waitResult(toolOrder, a.id); text != "" {
			output = assistantMessages(text)
		}
		cx := map[string]any{"agent_id": a.id}
		if nickname != "" {
			cx["nickname"] = nickname
		}
		display := "invoke_agent " + CodexSubagent
		if nickname != "" {
			display = "invoke_agent " + nickname
		}
		attrs := w.common(map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": CodexSubagent, "gen_ai.agent.id": a.id,
			"gen_ai.provider.name": "openai", "weft": w.weft("agent"), "codex": cx})
		w.add(agentSpan(a.id), parent, "codex.subagent", display, a.firstUS, attrs, inputs, end, done, output, exc, nil)
	}

	// Tool spans.
	for _, t := range toolOrder {
		cx := map[string]any{}
		for _, e := range []*store.LogEvent{t.decision, t.result} {
			if e == nil {
				continue
			}
			for _, k := range []string{"decision", "source", "tool_namespace", "duration_ms", "output_truncated", "mcp_server_origin", "tool_result_seq"} {
				if v, ok := e.Attrs[k]; ok && v != "" {
					name := k
					if k == "source" {
						name = "decision_source"
					}
					cx[name] = v
				}
			}
		}
		if s, ok := t.output.(string); ok {
			if m := exitCodePattern.FindStringSubmatch(s); m != nil {
				cx["exit_code"] = json.Number(m[1])
			}
		}
		attrs := w.common(map[string]any{"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": t.name, "weft": w.weft("tool"), "codex": cx})
		if !strings.HasPrefix(t.id, "event:") {
			attrs["gen_ai.tool.call.id"] = t.id
		}
		if srv := aStr(firstAttrs(t), "mcp_server"); srv != "" {
			attrs["gen_ai.tool.type"] = "mcp"
			attrs["mcp.server.name"] = srv
		}
		var exc *string
		ended := t.result != nil || t.rejected
		end := t.endUS
		switch {
		case t.rejected:
			exc = &t.rejectMsg
		case t.failed:
			exc = &t.errText
		case t.result == nil && closed:
			ended, end, exc = true, lastUS, strPtr("tool did not finish before the turn ended")
		}
		start := t.startUS
		if start == 0 {
			start = firstUS
		}
		w.add(ccID(codexLogsNS, sid, "tool", t.id), parentOf(t.agent), "codex.tool."+t.name, codexToolDisplay(t.name, t.input), start, attrs, t.input, end, ended, t.output, exc, nil)
	}

	// LLM spans: one per request that was recorded.
	for _, a := range agentOrder {
		for _, l := range a.llms {
			e := l.req
			m := aStr(e.Attrs, "model")
			cx := map[string]any{}
			for _, k := range []string{"attempt", "http.response.status_code", "duration_ms", "endpoint"} {
				if v, ok := e.Attrs[k]; ok {
					cx[strings.ReplaceAll(k, ".", "_")] = v
				}
			}
			if e.Name == "codex.websocket_request" {
				cx["transport"] = "websocket"
			}
			var summary map[string]any
			if l.done != nil && !l.failed {
				for _, k := range []string{"ttft_ms", "service_tier", "model_reasoning_effort"} {
					if v, ok := l.done.Attrs[k]; ok {
						cx[k] = v
					}
				}
				in, _ := aNum(l.done.Attrs, "input_token_count")
				out, _ := aNum(l.done.Attrs, "output_token_count")
				cr, _ := aNum(l.done.Attrs, "cached_token_count")
				cw, _ := aNum(l.done.Attrs, "cache_write_token_count")
				rs, _ := aNum(l.done.Attrs, "reasoning_token_count")
				summary = usageSummary(m, in, out, cr, cw, rs)
			}
			attrs := w.common(map[string]any{"gen_ai.operation.name": "chat", "gen_ai.provider.name": "openai", "weft": w.weft("llm"), "codex": cx})
			if m != "" {
				attrs["gen_ai.request.model"] = m
			}
			var blocks []any
			var names []string
			for _, t := range l.tools {
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": t.id, "name": t.name, "input": t.input})
				names = append(names, t.name)
			}
			display := "chat " + m
			if len(names) > 0 {
				display = "→ " + strings.Join(names, ", ")
			}
			var output any
			var exc *string
			end, ended := e.TimeUS, false
			switch {
			case l.failed:
				msg := "API error"
				if l.errText != "" {
					msg += " " + l.errText
				}
				exc, ended = &msg, true
				if l.done != nil {
					end = l.done.TimeUS
				}
			case l.done != nil:
				ended, end = true, l.done.TimeUS
				if len(blocks) > 0 {
					output = chatOutput(m, blocks, "tool_calls")
				}
			case closed:
				ended, end, exc = true, lastUS, strPtr("request did not complete before the turn ended")
			}
			w.add(ccID(codexLogsNS, sid, "llm", e.Key), parentOf(a.id), "codex.llm", display, evStart(e), attrs, map[string]any{"model": m}, end, ended, output, exc, summary)
		}
	}
	return w.out
}

var exitCodePattern = regexp.MustCompile(`Process exited with code (-?\d+)`)

func firstAttrs(t *cxTool) map[string]any {
	if t.result != nil {
		return t.result.Attrs
	}
	if t.decision != nil {
		return t.decision.Attrs
	}
	return nil
}

// waitResult is a sub-agent's final message as a wait_agent call reported it
// ({"status":{"<agent id>":{"completed":"…"}}}).
func waitResult(tools []*cxTool, agent string) string {
	for i := len(tools) - 1; i >= 0; i-- {
		t := tools[i]
		if t.name != "wait_agent" {
			continue
		}
		m, ok := t.output.(map[string]any)
		if !ok {
			continue
		}
		st, _ := m["status"].(map[string]any)
		if s, ok := st[agent].(map[string]any); ok {
			if c, ok := s["completed"].(string); ok {
				return c
			}
		}
	}
	return ""
}

// codexToolDisplay names a tool span: the tool plus its most telling argument.
func codexToolDisplay(name string, input map[string]any) string {
	switch name {
	case "spawn_agent", "send_input":
		if s, ok := input["message"].(string); ok && s != "" {
			return name + ": " + shortText(s, 50)
		}
	case "wait_agent":
		if ts, ok := input["targets"].([]any); ok && len(ts) > 0 {
			var ids []string
			for _, t := range ts {
				if s, ok := t.(string); ok {
					ids = append(ids, s)
				}
			}
			return name + ": " + shortText(strings.Join(ids, ", "), 50)
		}
	}
	return toolDisplay(name, input)
}
