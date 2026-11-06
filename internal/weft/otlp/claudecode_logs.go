package otlp

// Claude Code's OpenTelemetry log events (OTEL_LOGS_EXPORTER=otlp) as
// Weft spans. The events of one prompt (prompt.id) are stored as they
// arrive (store.IngestLogEvents) and the prompt's spans are rebuilt from all
// of them each time, so batches may be split, re-sent or out of order.
//
// Event → span:
//
//	user_prompt         the turn (invoke_agent claude-code): start, prompt text (OTEL_LOG_USER_PROMPTS)
//	api_request         chat <model>: tokens (cache included), reported cost, start = time - duration_ms
//	api_error           chat <model>, failed
//	assistant_response  the reply text of its request's chat span (and of the turn or sub-agent)
//	tool_decision       accept: folded into the tool span (start, decision source);
//	                    reject: the tool span, failed "Rejected by the user" (or by configuration, a hook)
//	tool_result         execute_tool <tool>: gen_ai.tool.call.id = tool_use_id, input from tool_input /
//	                    tool_parameters (OTEL_LOG_TOOL_DETAILS), success, start = time - duration_ms
//	subagent_completed  closes the sub-agent's invoke_agent span
//	(agent_id on any)   the event belongs to that sub-agent, under the Agent tool call that ran it
//
// The turn is stored once it is over: its last conversation request asked
// for no tool (or ended in an API error, or the user rejected a tool), or a
// later prompt started (then it was interrupted). Until then its spans hang
// under a turn that has not arrived, which Weft reads as running, and after
// store.OrphanRunTimeout without news as its steps' outcome: the same rule
// as an OTLP trace whose root has not been exported (store/run_status.go).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
)

// ClaudeCodeLogSource is the event-log source name of Claude Code's events.
const ClaudeCodeLogSource = "claude-code"

const ccLogsNS = "weft/claude-code/otel-logs"

// ccEvents are the events that describe a session; others (plugin_loaded,
// mcp_server_connection, managed_settings_resolved, ...) are ignored.
var ccEvents = map[string]bool{"user_prompt": true, "api_request": true, "api_error": true, "api_refusal": true,
	"tool_decision": true, "tool_result": true, "assistant_response": true, "subagent_completed": true}

// ccDropAttrs are standard attributes repeated on every event that no span
// uses (account identity); they are not stored.
var ccDropAttrs = map[string]bool{"user.email": true, "user.account_uuid": true, "user.account_id": true, "user.id": true,
	"organization.id": true, "ccr.session.id": true, "event.name": true, "event.timestamp": true, "event.sequence": true}

// LogBatch is the result of reading an OTLP logs export.
type LogBatch struct {
	// Events of Claude Code sessions, by project.
	Events map[string][]store.LogEvent
	// Ignored counts records that describe no session (other services,
	// other Claude Code events); IgnoredBy breaks it down by reason
	// (Ignored* in otlp.go).
	Ignored   int
	IgnoredBy map[string]int
}

// ignore counts n records not mapped, for reason.
func (b *LogBatch) ignore(reason string, n int) {
	if n == 0 {
		return
	}
	if b.IgnoredBy == nil {
		b.IgnoredBy = map[string]int{}
	}
	b.Ignored += n
	b.IgnoredBy[reason] += n
}

// ClaudeCodeLogEvents extracts Claude Code session events from a logs
// export. The project is chosen as for traces (ProjectFor): the project_id
// header, else the resource attribute weft.project, else default/default.
func ClaudeCodeLogEvents(req *collogs.ExportLogsServiceRequest, headerProject string) (LogBatch, error) {
	out := LogBatch{Events: map[string][]store.LogEvent{}}
	for _, rl := range req.GetResourceLogs() {
		resource := KeyValues(rl.GetResource().GetAttributes())
		isCC := aStr(resource, "service.name") == ClaudeCodeService
		var project string
		for _, sl := range rl.GetScopeLogs() {
			if !isCC && sl.GetScope().GetName() != ClaudeCodeEventsScope {
				out.ignore(IgnoredOtherService, len(sl.GetLogRecords()))
				continue
			}
			for _, lr := range sl.GetLogRecords() {
				raw := KeyValues(lr.GetAttributes())
				name := aStr(raw, "event.name")
				if name == "" {
					name = strings.TrimPrefix(aStr(map[string]any{"b": AnyValue(lr.GetBody())}, "b"), "claude_code.")
				}
				session, group := aStr(raw, "session.id"), aStr(raw, "prompt.id")
				switch {
				case !ccEvents[name]:
					out.ignore(IgnoredNoiseEvent, 1)
					continue
				case session == "" || group == "":
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
				out.Events[project] = append(out.Events[project], ccLogEvent(name, session, group, raw, resource, lr.GetTimeUnixNano(), lr.GetObservedTimeUnixNano()))
			}
		}
	}
	return out, nil
}

func ccLogEvent(name, session, group string, raw, resource map[string]any, timeNs, observedNs uint64) store.LogEvent {
	var us int64
	if t, err := time.Parse(time.RFC3339Nano, aStr(raw, "event.timestamp")); err == nil {
		us = t.UnixMicro()
	} else if timeNs > 0 {
		us = int64(timeNs / 1000)
	} else if observedNs > 0 {
		us = int64(observedNs / 1000)
	} else {
		us = time.Now().UnixMicro()
	}
	seq, _ := aNum(raw, "event.sequence")
	// The key covers the whole record, so a re-sent record is stored once
	// and two events never collide (event.sequence restarts per process).
	canon, _ := json.Marshal(raw)
	h := sha256.Sum256([]byte(name + "\x00" + session + "\x00" + aStr(raw, "event.timestamp") + "\x00" + string(canon)))
	attrs := map[string]any{}
	for k, v := range raw {
		if !ccDropAttrs[k] {
			attrs[k] = v
		}
	}
	if v := aStr(resource, "service.version"); v != "" {
		attrs["service.version"] = v
	}
	if a := aStr(resource, "weft.agent.name", "gen_ai.agent.name"); a != "" {
		attrs["weft.agent.name"] = a
	}
	return store.LogEvent{Key: hex.EncodeToString(h[:16]), SessionID: session, GroupID: group, Name: name, TimeUS: us, Seq: int64(seq), Attrs: attrs}
}

// ---------------------------------------------------------------- building

type ccTool struct {
	id                string
	name              string
	decision, result  *store.LogEvent
	agentID           string
	startUS, endUS    int64
	input             map[string]any
	rejected          bool
	rejectSource      string
	subagentType      string
	hasResult, failed bool
	errText           string
}

type ccLLM struct {
	key      string
	ev       *store.LogEvent // api_request, api_error or api_refusal
	response string
	agentID  string
	tools    []*ccTool
}

type ccAgent struct {
	id, typ, parentAgent string
	firstUS, lastUS      int64
	completed            *store.LogEvent
	response             string
	parentTool           *ccTool
}

func evStart(e *store.LogEvent) int64 {
	if d, ok := aNum(e.Attrs, "duration_ms"); ok && d > 0 {
		return e.TimeUS - int64(d*1000)
	}
	return e.TimeUS
}

// conversationRequest reports a request of the conversation itself (not a
// side request such as compaction, a session title or a suggestion).
func conversationRequest(e *store.LogEvent) bool {
	qs := aStr(e.Attrs, "query_source")
	return qs == "" || qs == "sdk" || qs == "main" || strings.HasPrefix(qs, "repl_main_thread")
}

// BuildClaudeCodeLogGroup builds one prompt's spans from its events.
func BuildClaudeCodeLogGroup(g store.LogGroup) []store.CompleteCall {
	sid := g.SessionID
	turnID := ccID(ccLogsNS, sid, "turn", g.GroupID)
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
	agentName := ClaudeCodeAgent
	convName := ""
	for _, e := range g.First {
		if e.Name == "user_prompt" {
			if t := aStr(e.Attrs, "prompt", "prompt_text"); !redacted(t) {
				convName = shortText(t, 80)
			}
			break
		}
	}

	var prompt *store.LogEvent
	tools := map[string]*ccTool{}
	var toolOrder []*ccTool
	llms := map[string]*ccLLM{}
	var llmOrder []*ccLLM
	agents := map[string]*ccAgent{}
	var agentOrder []*ccAgent
	responses := map[string]string{} // request id -> response text
	var lastMainResponse string
	firstUS, lastUS := evs[0].TimeUS, evs[0].TimeUS
	version := ""
	permissionMode := ""

	agentFor := func(id string, e *store.LogEvent) *ccAgent {
		if id == "" {
			return nil
		}
		a := agents[id]
		if a == nil {
			a = &ccAgent{id: id, firstUS: evStart(e), lastUS: e.TimeUS}
			agents[id] = a
			agentOrder = append(agentOrder, a)
		}
		if s := evStart(e); s < a.firstUS {
			a.firstUS = s
		}
		if e.TimeUS > a.lastUS {
			a.lastUS = e.TimeUS
		}
		if p := aStr(e.Attrs, "parent_agent_id"); p != "" {
			a.parentAgent = p
		}
		if t := aStr(e.Attrs, "agent.name"); t != "" && a.typ == "" {
			a.typ = t
		}
		if t := subagentTypeFromSource(aStr(e.Attrs, "query_source")); t != "" && a.typ == "" {
			a.typ = t
		}
		return a
	}
	toolFor := func(e *store.LogEvent) *ccTool {
		id := aStr(e.Attrs, "tool_use_id")
		if id == "" {
			id = "event:" + e.Key
		}
		t := tools[id]
		if t == nil {
			t = &ccTool{id: id, name: aStr(e.Attrs, "tool_name")}
			tools[id] = t
			toolOrder = append(toolOrder, t)
		}
		if t.name == "" {
			t.name = aStr(e.Attrs, "tool_name")
		}
		if a := aStr(e.Attrs, "agent_id"); a != "" {
			t.agentID = a
		}
		return t
	}

	for i := range evs {
		e := &evs[i]
		if s := evStart(e); s < firstUS {
			firstUS = s
		}
		if e.TimeUS > lastUS {
			lastUS = e.TimeUS
		}
		if v := aStr(e.Attrs, "service.version"); v != "" {
			version = v
		}
		if a := aStr(e.Attrs, "weft.agent.name"); a != "" {
			agentName = a
		}
		agentID := aStr(e.Attrs, "agent_id")
		ag := agentFor(agentID, e)
		switch e.Name {
		case "user_prompt":
			if prompt == nil {
				prompt = e
			}
		case "api_request", "api_error", "api_refusal":
			key := aStr(e.Attrs, "request_id", "client_request_id")
			if key == "" {
				key = "event:" + e.Key
			}
			l := llms[key]
			if l == nil {
				l = &ccLLM{key: key, agentID: agentID}
				llms[key] = l
				llmOrder = append(llmOrder, l)
			}
			// A request that failed and then succeeded is shown as its success.
			if l.ev == nil || e.Name == "api_request" {
				l.ev = e
			}
		case "assistant_response":
			text := aStr(e.Attrs, "response")
			if redacted(text) {
				text = ""
			}
			if rid := aStr(e.Attrs, "request_id"); rid != "" && text != "" {
				responses[rid] = text
			}
			if ag != nil {
				if text != "" {
					ag.response = text
				}
			} else if conversationRequest(e) && text != "" {
				lastMainResponse = text
			}
		case "tool_decision":
			t := toolFor(e)
			t.decision = e
			if strings.EqualFold(aStr(e.Attrs, "decision"), "reject") {
				t.rejected = true
				t.rejectSource = aStr(e.Attrs, "source")
			}
			if m := aStr(e.Attrs, "permission_mode"); m != "" && agentID == "" {
				permissionMode = m
			}
		case "tool_result":
			t := toolFor(e)
			t.result = e
		case "subagent_completed":
			if ag != nil {
				ag.completed = e
				if t := aStr(e.Attrs, "agent_type"); t != "" {
					ag.typ = t
				}
			}
		}
	}

	// Tools: timing, input, outcome.
	for _, t := range toolOrder {
		var params map[string]any
		if t.result != nil {
			t.hasResult = true
			t.startUS, t.endUS = evStart(t.result), t.result.TimeUS
			t.input = jsonMap(aStr(t.result.Attrs, "tool_input"))
			params = jsonMap(aStr(t.result.Attrs, "tool_parameters"))
			if ok, has := aBool(t.result.Attrs, "success"); has && !ok {
				t.failed = true
				t.errText = strings.TrimSpace(strings.Join(nonEmpty(aStr(t.result.Attrs, "error_type"), aStr(t.result.Attrs, "error")), ": "))
				if t.errText == "" {
					t.errText = "tool failed"
				}
			}
		}
		if t.decision != nil {
			if t.startUS == 0 || t.decision.TimeUS < t.startUS {
				t.startUS = t.decision.TimeUS
			}
			if params == nil {
				params = jsonMap(aStr(t.decision.Attrs, "tool_parameters"))
			}
			if t.endUS == 0 && t.rejected {
				t.endUS = t.decision.TimeUS
			}
		}
		if t.input == nil {
			t.input = map[string]any{}
			for k, v := range params {
				t.input[k] = v
			}
		}
		if st, ok := params["subagent_type"].(string); ok {
			t.subagentType = st
		} else if st, ok := t.input["subagent_type"].(string); ok {
			t.subagentType = st
		}
	}

	// Which request asked for which tool: the request id on the tool event,
	// else the first request of the same agent recorded at or after its
	// decision (Claude Code logs the decision while the response streams).
	byAgent := map[string][]*ccLLM{}
	for _, l := range llmOrder {
		byAgent[l.agentID] = append(byAgent[l.agentID], l)
	}
	for _, t := range toolOrder {
		var rid string
		for _, e := range []*store.LogEvent{t.result, t.decision} {
			if e != nil && rid == "" {
				rid = aStr(e.Attrs, "request_id")
			}
		}
		if l := llms[rid]; rid != "" && l != nil {
			l.tools = append(l.tools, t)
			continue
		}
		if rid == "" && t.decision != nil {
			for _, l := range byAgent[t.agentID] {
				if l.ev.TimeUS >= t.decision.TimeUS {
					l.tools = append(l.tools, t)
					break
				}
			}
		}
	}

	// Is the turn over?
	var lastConv *ccLLM
	for _, l := range byAgent[""] {
		if conversationRequest(l.ev) {
			lastConv = l
		}
	}
	var turnErr *string
	closed := false
	if lastConv != nil {
		later := false
		for _, t := range toolOrder {
			if t.agentID == "" && ((t.decision != nil && t.decision.TimeUS > lastConv.ev.TimeUS) || (t.result != nil && t.result.TimeUS > lastConv.ev.TimeUS)) {
				later = true
			}
		}
		switch {
		case later:
		case lastConv.ev.Name != "api_request":
			msg := "API error"
			if lastConv.ev.Name == "api_refusal" {
				msg = "The model refused"
			}
			if s := aStr(lastConv.ev.Attrs, "error"); s != "" {
				msg += ": " + s
			}
			turnErr, closed = &msg, true
		case len(lastConv.tools) == 0:
			closed = true
		}
	}
	if !closed {
		// The user stopped the turn by rejecting a tool.
		for i := len(toolOrder) - 1; i >= 0; i-- {
			t := toolOrder[i]
			if t.agentID != "" {
				continue
			}
			if t.rejected && strings.HasPrefix(t.rejectSource, "user_") && !t.hasResult {
				closed = true
			}
			break
		}
	}
	if !closed && g.Later {
		msg := "interrupted: a new prompt was submitted"
		turnErr, closed = &msg, true
	}

	common := func(attrs map[string]any) map[string]any {
		attrs["gen_ai.conversation.id"] = sid
		if convName != "" {
			attrs["gen_ai.conversation.name"] = convName
		}
		return attrs
	}
	weftAttrs := func(kind string) map[string]any {
		return map[string]any{"span_kind": kind, "source": store.SourceClaudeCodeLogs}
	}
	var out []store.CompleteCall
	add := func(id, parent, name, display string, startUS int64, attrs, inputs map[string]any, endUS int64, ended bool, output any, exc *string, summary map[string]any) {
		st := store.CallStart{ID: id, TraceID: turnID, OpName: name, StartedAt: usToTime(startUS), Attributes: attrs, Inputs: inputs,
			ReplaceDisplayName: true}
		if parent != "" {
			st.ParentID = &parent
		}
		if display != "" {
			st.DisplayName = &display
		}
		s, t := sid, turnID
		st.ThreadID, st.TurnID = &s, &t
		c := store.CompleteCall{Start: st}
		if ended {
			if endUS < startUS {
				endUS = startUS
			}
			if summary == nil {
				summary = map[string]any{}
			}
			c.End = &store.CallEnd{EndedAt: usToTime(endUS), Output: output, Exception: exc, Summary: summary}
		}
		out = append(out, c)
	}

	// Sub-agents: under the Agent tool call whose run holds their first event.
	agentSpan := func(id string) string { return ccID(ccLogsNS, sid, "agent", id) }
	for _, a := range agentOrder {
		var main, other []*ccTool
		for _, t := range toolOrder {
			if !isAgentTool(t.name) || t.startUS == 0 || t.agentID == a.id {
				continue
			}
			// Its run holds the sub-agent's first event (1 ms of slack:
			// the two are logged in the same instant).
			if t.startUS > a.firstUS+1000 || (t.hasResult && t.endUS < a.firstUS) {
				continue
			}
			switch {
			case a.parentAgent != "" && t.agentID != a.parentAgent:
			case t.agentID == "":
				main = append(main, t)
			default:
				other = append(other, t)
			}
		}
		cands := main
		if a.parentAgent != "" || len(cands) == 0 {
			cands = append(main, other...)
		}
		var best *ccTool
		for _, t := range cands {
			switch {
			case best == nil:
				best = t
			case a.typ != "" && (t.subagentType == a.typ) != (best.subagentType == a.typ):
				if t.subagentType == a.typ {
					best = t
				}
			case t.startUS > best.startUS:
				best = t
			}
		}
		a.parentTool = best
		if (a.typ == "" || a.typ == "custom") && best != nil && best.subagentType != "" {
			a.typ = best.subagentType
		}
		if a.typ == "" {
			a.typ = "subagent"
		}
	}

	// The turn.
	if closed {
		inputs := map[string]any{promptNotExportedKey: promptNotExported}
		cc := map[string]any{"prompt_id": g.GroupID, "prompt_exported": false}
		startUS := firstUS
		if prompt != nil {
			startUS = prompt.TimeUS
			if t := aStr(prompt.Attrs, "prompt", "prompt_text"); !redacted(t) {
				inputs = userMessages(t)
				cc["prompt_exported"] = true
			}
			if n, ok := aNum(prompt.Attrs, "prompt_length"); ok {
				cc["prompt_length"] = n
			}
			if c := aStr(prompt.Attrs, "command_name"); c != "" {
				cc["command_name"] = c
			}
		}
		if permissionMode != "" {
			cc["permission_mode"] = permissionMode
		}
		if version != "" {
			cc["version"] = version
		}
		attrs := common(map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": agentName,
			"gen_ai.provider.name": "anthropic", "weft": weftAttrs("agent"), "claude_code": cc})
		if lastConv != nil {
			if m := aStr(lastConv.ev.Attrs, "model"); m != "" {
				attrs["gen_ai.request.model"] = m
			}
		}
		var output any
		if lastMainResponse != "" {
			output = assistantMessages(lastMainResponse)
		}
		add(turnID, "", "claude_code.turn", "invoke_agent "+agentName, startUS, attrs, inputs, lastUS, true, output, turnErr, nil)
	}

	// Sub-agent spans.
	for _, a := range agentOrder {
		parent := turnID
		if a.parentTool != nil {
			parent = ccID(ccLogsNS, sid, "tool", a.parentTool.id)
		} else if a.parentAgent != "" {
			parent = agentSpan(a.parentAgent)
		}
		start := a.firstUS
		cc := map[string]any{"agent_id": a.id}
		if a.parentAgent != "" {
			cc["parent_agent_id"] = a.parentAgent
		}
		end, ended := a.lastUS, false
		var exc *string
		if a.completed != nil {
			end, ended = a.completed.TimeUS, true
			if s := evStart(a.completed); s < start {
				start = s
			}
			for _, k := range []string{"total_tokens", "total_tool_uses", "duration_ms", "model", "is_async", "agent.source"} {
				if v, ok := a.completed.Attrs[k]; ok {
					cc[strings.ReplaceAll(k, ".", "_")] = v
				}
			}
		} else if closed {
			msg := "sub-agent did not finish before the turn ended"
			ended, exc = true, &msg
		}
		inputs := map[string]any{}
		if a.parentTool != nil {
			if p, ok := a.parentTool.input["prompt"].(string); ok && p != "" {
				inputs = userMessages(p)
			}
			if d, ok := a.parentTool.input["description"].(string); ok && d != "" {
				inputs["description"] = d
			}
		}
		var output any
		if a.response != "" {
			output = assistantMessages(a.response)
		}
		attrs := common(map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": a.typ, "gen_ai.agent.id": a.id,
			"weft": weftAttrs("agent"), "claude_code": cc})
		add(agentSpan(a.id), parent, "claude_code.subagent", "invoke_agent "+a.typ, start, attrs, inputs, end, ended, output, exc, nil)
	}

	// Tool spans.
	for _, t := range toolOrder {
		parent := turnID
		if t.agentID != "" {
			parent = agentSpan(t.agentID)
		}
		cc := map[string]any{}
		for _, e := range []*store.LogEvent{t.decision, t.result} {
			if e == nil {
				continue
			}
			for _, k := range []string{"decision", "source", "tool_source", "permission_mode", "classifier_verdict", "classifier_category",
				"tool_input_size_bytes", "tool_result_size_bytes", "error_type", "duration_ms", "request_id", "decision_source", "mcp_server_scope"} {
				if v, ok := e.Attrs[k]; ok {
					name := k
					if k == "source" {
						name = "decision_source"
					}
					cc[name] = v
				}
			}
		}
		if t.subagentType != "" {
			cc["subagent_type"] = t.subagentType
		}
		attrs := common(map[string]any{"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": t.name, "weft": weftAttrs("tool"), "claude_code": cc})
		if !strings.HasPrefix(t.id, "event:") {
			attrs["gen_ai.tool.call.id"] = t.id
		}
		mcpAttrs(t.name, attrs)
		var exc *string
		ended := t.hasResult || t.rejected
		end := t.endUS
		switch {
		case t.rejected:
			msg := "Rejected by the user"
			switch {
			case t.rejectSource == "config":
				msg = "Rejected by the permission settings"
			case t.rejectSource == "hook":
				msg = "Rejected by a hook"
			case t.rejectSource == "user_abort":
				msg = "Rejected: the user dismissed the permission prompt"
			}
			exc = &msg
		case t.failed:
			exc = &t.errText
		case !t.hasResult && closed:
			msg := "tool did not finish before the turn ended"
			ended, end, exc = true, lastUS, &msg
		}
		display := toolDisplay(t.name, t.input)
		start := t.startUS
		if start == 0 {
			start = firstUS
		}
		add(ccID(ccLogsNS, sid, "tool", t.id), parent, "claude_code.tool."+t.name, display, start, attrs, t.input, end, ended, nil, exc, nil)
	}

	// LLM spans.
	for _, l := range llmOrder {
		e := l.ev
		parent := turnID
		if l.agentID != "" {
			parent = agentSpan(l.agentID)
		}
		model := aStr(e.Attrs, "model")
		cc := map[string]any{}
		for _, k := range []string{"request_id", "client_request_id", "query_source", "ttft_ms", "duration_ms", "speed", "effort", "cost_usd",
			"cache_creation_5m_tokens", "cache_creation_1h_tokens", "attempt", "status_code", "skill.name", "plugin.name", "mcp_server.name", "mcp_tool.name"} {
			if v, ok := e.Attrs[k]; ok {
				cc[strings.ReplaceAll(k, ".", "_")] = v
			}
		}
		attrs := common(map[string]any{"gen_ai.operation.name": "chat", "gen_ai.provider.name": "anthropic", "weft": weftAttrs("llm"), "claude_code": cc})
		if model != "" {
			attrs["gen_ai.request.model"] = model
		}
		if rid := aStr(e.Attrs, "request_id"); rid != "" {
			attrs["gen_ai.response.id"] = rid
		}
		text := responses[aStr(e.Attrs, "request_id")]
		var blocks []any
		var names []string
		if text != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": text})
		}
		for _, t := range l.tools {
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": t.id, "name": t.name, "input": t.input})
			names = append(names, t.name)
		}
		display := "chat " + model
		switch {
		case text != "":
			display = shortText(text, 60)
		case len(names) > 0:
			display = "→ " + strings.Join(names, ", ")
		}
		var output any
		var exc *string
		var summary map[string]any
		if e.Name == "api_request" {
			if len(blocks) > 0 {
				finish := "end_turn"
				if len(names) > 0 {
					finish = "tool_use"
				}
				output = chatOutput(model, blocks, finish)
			}
			summary = ccUsage(model, e.Attrs)
		} else {
			msg := "API error"
			if e.Name == "api_refusal" {
				msg = "The model refused"
			}
			if sc := aStr(e.Attrs, "status_code"); sc != "" {
				msg += " " + sc
			}
			if s := aStr(e.Attrs, "error"); s != "" {
				msg += ": " + s
			}
			exc = &msg
		}
		add(ccID(ccLogsNS, sid, "llm", l.key), parent, "claude_code.llm", display, evStart(e), attrs, map[string]any{"model": model}, e.TimeUS, true, output, exc, summary)
	}
	return out
}

func nonEmpty(xs ...string) []string {
	var out []string
	for _, x := range xs {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}
