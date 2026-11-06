package otlp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
)

// Fixtures: real Codex CLI 0.167.0 exports (codex exec against a local fake
// Responses API server, [otel] exporter = otlp-http), with paths and the
// host name replaced.
//
//	resume_json/      OTLP/HTTP JSON: a turn running `echo hi`, then `codex exec resume`
//	                  (a second process, same session): `ls /nonexistent-dir` (exit code 2)
//	                  and `echo second`
//	subagent_proto/   OTLP/HTTP protobuf: spawn_agent starts a sub-agent (a session of its
//	                  own) that runs `cat note.txt`; wait_agent collects its answer
//	flaky_json/       each request first answered 500, retried
//	escalate_json/    a command asking for escalated permissions, refused by the policy
const (
	cxResumeSession = "01a1b052-610e-7783-90bf-14c692b1f406"
	cxSubSession    = "01a1b052-6b2c-7e22-8cd4-1d46d63de0bf"
	cxSubAgent      = "01a1b052-6beb-7a22-bed2-c208f706a519"
)

// logFixture is a captured export: one file per request, in order.
type logFixture struct{ files []string }

func loadLogFixture(t *testing.T, dir string) logFixture {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join("testdata", dir, "*_logs.*"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatalf("no fixtures in %s", dir)
	}
	return logFixture{files: files}
}

func (f logFixture) batches(t *testing.T) []*collogs.ExportLogsServiceRequest {
	t.Helper()
	var out []*collogs.ExportLogsServiceRequest
	for _, p := range f.files {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		req, err := DecodeLogs(b, strings.HasSuffix(p, ".json"))
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		out = append(out, req)
	}
	return out
}

// events flattens a fixture to the events of one source, in export order.
func (f logFixture) events(t *testing.T, source string) []store.LogEvent {
	t.Helper()
	var out []store.LogEvent
	for _, req := range f.batches(t) {
		b, err := SessionLogEvents(req, ccProject)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b.Events[source][ccProject]...)
	}
	return out
}

func logSource(name string) store.LogSource {
	for _, s := range LogSources {
		if s.Name == name {
			return s
		}
	}
	panic(name)
}

// ingestAs stores events of one source.
func (h *ccHarness) ingestAs(source string, evs []store.LogEvent) int {
	h.t.Helper()
	_, added, err := h.s.IngestLogEvents(context.Background(), ccProject, logSource(source), evs)
	if err != nil {
		h.t.Fatal(err)
	}
	return added
}

// ingestExport stores a whole export the way POST /otel/v1/logs does.
func (h *ccHarness) ingestExport(req *collogs.ExportLogsServiceRequest) SessionLogs {
	h.t.Helper()
	b, err := SessionLogEvents(req, ccProject)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, src := range LogSources {
		if evs := b.Events[src.Name][ccProject]; len(evs) > 0 {
			h.ingestAs(src.Name, evs)
		}
	}
	return b
}

func (h *ccHarness) ingestFixture(dir string) int {
	h.t.Helper()
	ignored := 0
	for _, req := range loadLogFixture(h.t, dir).batches(h.t) {
		ignored += h.ingestExport(req).Ignored
	}
	return ignored
}

// sessions lists the sessions that have spans.
func (h *ccHarness) sessions() map[string]int {
	h.t.Helper()
	calls, err := h.s.ListCalls(context.Background(), store.CallsQuery{ProjectID: ccProject})
	if err != nil {
		h.t.Fatal(err)
	}
	out := map[string]int{}
	for _, c := range calls {
		out[deref(c.ThreadID)]++
	}
	return out
}

func turnsOf(calls []*store.Call, op string) []*store.Call {
	var out []*store.Call
	for _, c := range calls {
		if c.OpName == op {
			out = append(out, c)
		}
	}
	return out
}

func childrenOf(calls []*store.Call, parent string) []*store.Call {
	var out []*store.Call
	for _, c := range calls {
		if deref(c.ParentID) == parent {
			out = append(out, c)
		}
	}
	return out
}

func inputText(c *store.Call) string {
	msgs, _ := c.Inputs["messages"].([]any)
	if len(msgs) == 0 {
		return ""
	}
	s, _ := msgs[0].(map[string]any)["content"].(string)
	return s
}

func TestCodexLogsRealRuns(t *testing.T) {
	t.Run("two turns, two processes (JSON)", func(t *testing.T) {
		h := newCCHarness(t)
		if ignored := h.ingestFixture("codex/resume_json"); ignored == 0 {
			t.Fatal("noise events (conversation_starts, sse deltas, ...) should be counted as ignored")
		}
		if s := h.sessions(); len(s) != 1 || s[cxResumeSession] != 10 {
			t.Fatalf("sessions = %v, want one with 10 spans", s)
		}
		calls := h.spans(cxResumeSession)
		turns := turnsOf(calls, "codex.turn")
		if len(turns) != 2 {
			t.Fatalf("turns = %d", len(turns))
		}
		want := []struct {
			prompt     string
			requests   float64
			prompt0    float64
			completion float64
			tools      map[string]string
		}{
			{"Run `echo hi`", 2, 1200 + 1300, 60, map[string]string{"exec_command: echo hi": "call_resp_1_0"}},
			{"TWO: run `ls /nonexistent-dir`", 3, 1200 + 1300 + 1400, 90,
				map[string]string{"exec_command: ls /nonexistent-dir": "call_resp_3_0", "exec_command: echo second": "call_resp_4_1"}},
		}
		for i, turn := range turns {
			w := want[i]
			if turn.ParentID != nil || turn.EndedAt == nil || turn.Exception != nil || runStatus(turn) != "ok" ||
				attr(turn, "gen_ai.agent.name") != "codex" || attr(turn, "gen_ai.provider.name") != "openai" ||
				attr(turn, "gen_ai.request.model") != "gpt-5.5" || attr(turn, "weft/source") != store.SourceCodexLogs ||
				attr(turn, "gen_ai.agent.version") != "0.167.0" || attr(turn, "codex/originator") != "codex_exec" {
				t.Fatalf("turn %d = %+v %v", i, turn, turn.Attributes)
			}
			if !strings.HasPrefix(inputText(turn), w.prompt) {
				t.Fatalf("turn %d input = %v", i, turn.Inputs)
			}
			// The session title is stored once per session, not in its spans.
			if attr(turn, "gen_ai.conversation.name") != nil {
				t.Fatalf("conversation name in the span = %v", attr(turn, "gen_ai.conversation.name"))
			}
			if got := conversationNames(t, h, cxResumeSession); len(got) != 1 || !got["Run `echo hi` and tell me what it printed."] {
				t.Fatalf("conversation names = %v", got)
			}
			r, p, c, cr, _, cost := usage(turn)
			if r != w.requests || p != w.prompt0 || c != w.completion || cr != 1000*(w.requests-1) || cost <= 0 {
				t.Fatalf("turn %d usage = %v requests %v prompt %v completion %v cached $%v", i, r, p, c, cr, cost)
			}
			kids := childrenOf(calls, turn.ID)
			if len(kids) != int(w.requests)+len(w.tools) {
				t.Fatalf("turn %d children = %d", i, len(kids))
			}
			for display, id := range w.tools {
				tool := spanByName(t, calls, display)
				if deref(tool.ParentID) != turn.ID || attr(tool, "gen_ai.tool.call.id") != id || tool.EndedAt == nil || tool.Exception != nil ||
					attr(tool, "gen_ai.operation.name") != "execute_tool" || tool.Output == nil {
					t.Fatalf("%s: parent %v call id %v ended %v exc %v output %v", display, tool.ParentID, attr(tool, "gen_ai.tool.call.id"), tool.EndedAt, tool.Exception, tool.Output)
				}
			}
		}
		ls := spanByName(t, calls, "exec_command: ls /nonexistent-dir")
		if attr(ls, "codex/exit_code") != json.Number("2") && attr(ls, "codex/exit_code") != float64(2) {
			t.Fatalf("exit code = %#v", attr(ls, "codex/exit_code"))
		}
		if cmd, _ := ls.Inputs["cmd"].(string); cmd != "ls /nonexistent-dir" {
			t.Fatalf("tool input = %v", ls.Inputs)
		}
		// The request that asked for a tool names it; the last one answered.
		var asked, answered int
		for _, c := range calls {
			if c.OpName == "codex.llm" {
				if strings.HasPrefix(deref(c.DisplayName), "→ exec_command") {
					asked++
				} else if deref(c.DisplayName) == "chat gpt-5.5" {
					answered++
				}
			}
		}
		if asked != 3 || answered != 2 {
			t.Fatalf("LLM spans: %d asked for a tool, %d answered", asked, answered)
		}
	})
	t.Run("a sub-agent (protobuf)", func(t *testing.T) {
		h := newCCHarness(t)
		h.ingestFixture("codex/subagent_proto")
		if s := h.sessions(); len(s) != 1 || s[cxSubSession] == 0 {
			t.Fatalf("sessions = %v: the sub-agent's own session should be folded into its parent's", s)
		}
		calls := h.spans(cxSubSession)
		turn := turnsOf(calls, "codex.turn")[0]
		spawn := spanByName(t, calls, "spawn_agent: Run `cat note.txt` and report its contents.")
		sub := spanByName(t, calls, "invoke_agent Pauli")
		cat := spanByName(t, calls, "exec_command: cat note.txt")
		wait := spanByName(t, calls, "wait_agent: "+cxSubAgent)
		if deref(spawn.ParentID) != turn.ID || deref(sub.ParentID) != spawn.ID || deref(cat.ParentID) != sub.ID || deref(wait.ParentID) != turn.ID {
			t.Fatalf("nesting: spawn under %v, sub-agent under %v, cat under %v, wait under %v", spawn.ParentID, sub.ParentID, cat.ParentID, wait.ParentID)
		}
		if attr(sub, "gen_ai.agent.name") != CodexSubagent || attr(sub, "gen_ai.agent.id") != cxSubAgent || sub.EndedAt == nil || sub.Exception != nil ||
			inputText(sub) != "Run `cat note.txt` and report its contents." {
			t.Fatalf("sub-agent = %v inputs %v", sub.Attributes, sub.Inputs)
		}
		if out, _ := json.Marshal(sub.Output); !strings.Contains(string(out), "note.txt says: hello") {
			t.Fatalf("sub-agent output = %s", out)
		}
		if r, _, _, _, _, _ := usage(spawn); r != 2 {
			t.Fatalf("the sub-agent's 2 requests should roll up into spawn_agent: %v", r)
		}
		if r, p, _, _, _, _ := usage(turn); r != 5 || p != 1200+1300+1400+1200+1300 {
			t.Fatalf("turn usage = %v requests, %v prompt", r, p)
		}
		if turn.EndedAt == nil || runStatus(turn) != "ok" {
			t.Fatalf("turn ended %v status %q", turn.EndedAt, runStatus(turn))
		}
	})
	t.Run("retried API errors", func(t *testing.T) {
		h := newCCHarness(t)
		h.ingestFixture("codex/flaky_json")
		calls := h.spans(sessionOf(t, h))
		var failed int
		for _, c := range calls {
			if c.OpName == "codex.llm" && c.Exception != nil {
				if *c.Exception != "API error 500: http 500" || attr(c, "codex/attempt") == nil {
					t.Fatalf("failed request: %v %v", *c.Exception, c.Attributes["codex"])
				}
				failed++
			}
		}
		turn := turnsOf(calls, "codex.turn")[0]
		if failed != 2 || turn.Exception != nil || turn.EndedAt == nil {
			t.Fatalf("%d failed requests; turn exc %v", failed, turn.Exception)
		}
		if r, _, _, _, _, _ := usage(turn); r != 2 {
			t.Fatalf("failed attempts carry no tokens: %v requests", r)
		}
	})
	t.Run("a command the policy refused", func(t *testing.T) {
		h := newCCHarness(t)
		h.ingestFixture("codex/escalate_json")
		calls := h.spans(sessionOf(t, h))
		tool := spanByName(t, calls, "exec_command: touch made-by-codex.txt")
		if tool.Exception == nil || !strings.HasPrefix(*tool.Exception, "approval policy is Never; reject command") {
			t.Fatalf("refused command exc = %v", tool.Exception)
		}
		if turn := turnsOf(calls, "codex.turn")[0]; runStatus(turn) != "failed_step" || turn.Exception != nil {
			t.Fatalf("turn status %q exc %v", runStatus(turn), turn.Exception)
		}
	})
}

func sessionOf(t *testing.T, h *ccHarness) string {
	t.Helper()
	s := h.sessions()
	if len(s) != 1 {
		t.Fatalf("sessions = %v", s)
	}
	for id := range s {
		return id
	}
	return ""
}

// Re-sent, split and out-of-order batches give the same spans and totals,
// the sub-agent included.
func TestCodexLogsBatching(t *testing.T) {
	for _, dir := range []string{"codex/resume_json", "codex/subagent_proto"} {
		fx := loadLogFixture(t, dir)
		all := fx.events(t, CodexLogSource)
		ref := newCCHarness(t)
		ref.ingestFixture(dir)
		session := sessionOf(t, ref)
		snapshot := func(h *ccHarness) string {
			var lines []string
			for _, c := range h.spans(session) {
				r, p, comp, _, _, _ := usage(c)
				lines = append(lines, strings.Join([]string{c.ID, deref(c.ParentID), deref(c.DisplayName), c.StartedAt, deref(c.EndedAt),
					deref(c.Exception), runStatus(c), jsonStr(r), jsonStr(p), jsonStr(comp)}, "|"))
			}
			sort.Strings(lines)
			return strings.Join(lines, "\n")
		}
		want := snapshot(ref)
		check := func(t *testing.T, h *ccHarness) {
			t.Helper()
			if s := h.sessions(); len(s) != 1 {
				t.Fatalf("sessions = %v", s)
			}
			if got := snapshot(h); got != want {
				t.Fatalf("spans differ from the in-order ingest:\n%s\n--- want\n%s", got, want)
			}
		}
		t.Run(dir+"/re-sent", func(t *testing.T) {
			h := newCCHarness(t)
			h.ingestFixture(dir)
			h.ingestFixture(dir)
			if n := h.ingestAs(CodexLogSource, all); n != 0 {
				t.Fatalf("re-sent events stored again: %d", n)
			}
			check(t, h)
		})
		t.Run(dir+"/one event per batch", func(t *testing.T) {
			h := newCCHarness(t)
			for _, e := range all {
				h.ingestAs(CodexLogSource, []store.LogEvent{e})
			}
			check(t, h)
		})
		t.Run(dir+"/reversed", func(t *testing.T) {
			h := newCCHarness(t)
			for i := len(all) - 1; i >= 0; i-- {
				h.ingestAs(CodexLogSource, []store.LogEvent{all[i]})
			}
			check(t, h)
		})
	}
	t.Run("the sub-agent's events before the spawn that links them", func(t *testing.T) {
		fx := loadLogFixture(t, "codex/subagent_proto")
		var sub, rest []store.LogEvent
		for _, e := range fx.events(t, CodexLogSource) {
			if e.SessionID == cxSubAgent {
				sub = append(sub, e)
			} else {
				rest = append(rest, e)
			}
		}
		h := newCCHarness(t)
		h.ingestAs(CodexLogSource, sub)
		// Alone, the sub-agent reads as a session of its own.
		if s := h.sessions(); s[cxSubAgent] == 0 {
			t.Fatalf("sessions before the link = %v", s)
		}
		h.ingestAs(CodexLogSource, rest)
		if s := h.sessions(); len(s) != 1 || s[cxSubSession] != 10 {
			t.Fatalf("sessions after the link = %v (the sub-agent's own spans should be retired)", s)
		}
		calls := h.spans(cxSubSession)
		if sub := spanByName(t, calls, "invoke_agent Pauli"); deref(sub.ParentID) != spanByName(t, calls, "spawn_agent: Run `cat note.txt` and report its contents.").ID {
			t.Fatalf("sub-agent parent = %v", sub.ParentID)
		}
	})
}

func jsonStr(v any) string { b, _ := json.Marshal(v); return string(b) }

// A turn cut off mid-way has no turn span (its steps read as running); the
// next prompt closes it as interrupted.
func TestCodexLogsTurnCutOff(t *testing.T) {
	all := loadLogFixture(t, "codex/resume_json").events(t, CodexLogSource)
	// The first turn up to its tool call: the request that asked for it
	// has completed, the tool has run, the next request has not answered.
	var cut []store.LogEvent
	for _, e := range all {
		cut = append(cut, e)
		if e.Name == "codex.tool_result" {
			break
		}
	}
	shift := time.Now().Add(-time.Minute).UnixMicro() - cut[0].TimeUS
	for i := range cut {
		cut[i].TimeUS += shift
	}
	h := newCCHarness(t)
	h.ingestAs(CodexLogSource, cut)
	calls := h.spans(cxResumeSession)
	if len(turnsOf(calls, "codex.turn")) != 0 {
		t.Fatal("an unfinished turn was stored")
	}
	tool := spanByName(t, calls, "exec_command: echo hi")
	if runStatus(tool) != "running" {
		t.Fatalf("step of an open turn: status %q", runStatus(tool))
	}
	next := codexLogEvent("codex.user_prompt", cxResumeSession, map[string]any{"conversation.id": cxResumeSession, "prompt": "next",
		"event.timestamp": time.Now().UTC().Format(time.RFC3339Nano)}, nil, 1, 0, 0)
	h.ingestAs(CodexLogSource, []store.LogEvent{next})
	turns := turnsOf(h.spans(cxResumeSession), "codex.turn")
	if len(turns) != 1 || turns[0].Exception == nil || !strings.Contains(*turns[0].Exception, "interrupted") || runStatus(turns[0]) != "error" {
		t.Fatalf("first turn after the next prompt: %v", turns)
	}
}

// Synthetic events in Codex's shapes (Codex 0.167.0's otel/src/events).
type cxScript struct {
	session string
	t0      time.Time
	n       int64
	events  []store.LogEvent
}

func newCxScript(session string) *cxScript {
	return &cxScript{session: session, t0: time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)}
}

func (s *cxScript) add(name string, attrs map[string]any) *cxScript {
	s.n++
	a := map[string]any{"conversation.id": s.session, "event.name": name, "model": "gpt-5.5", "app.version": "0.167.0",
		"event.timestamp": s.t0.Add(time.Duration(s.n) * time.Second).Format("2006-01-02T15:04:05.000Z")}
	for k, v := range attrs {
		a[k] = v
	}
	s.events = append(s.events, codexLogEvent(name, aStr(a, "conversation.id"), a, map[string]any{"service.name": "codex_exec"}, s.n, 0, 0))
	return s
}

func (s *cxScript) request(status int) *cxScript {
	return s.add("codex.api_request", map[string]any{"http.response.status_code": status, "duration_ms": 200, "attempt": 0})
}

func (s *cxScript) completed(in int) *cxScript {
	return s.add("codex.sse_event", map[string]any{"event.kind": "response.completed", "input_token_count": in, "output_token_count": 7,
		"cached_token_count": 0, "reasoning_token_count": 2})
}

func TestCodexLogsGatesAndOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		script func(s *cxScript)
		check  func(t *testing.T, calls []*store.Call)
	}{
		{
			name: "prompt not exported",
			script: func(s *cxScript) {
				s.add("codex.user_prompt", map[string]any{"prompt": "[REDACTED]", "prompt_length": 12}).request(200).completed(100)
			},
			check: func(t *testing.T, calls []*store.Call) {
				turn := turnsOf(calls, "codex.turn")[0]
				if turn.Inputs[promptNotExportedKey] != codexPromptNotExported || store.InputText(turn.Inputs) != "" ||
					attr(turn, "codex/prompt_exported") != false || attr(turn, "gen_ai.conversation.name") != nil {
					t.Fatalf("turn inputs %v attrs %v", turn.Inputs, turn.Attributes)
				}
			},
		},
		{
			name: "tool denied by the user",
			script: func(s *cxScript) {
				s.add("codex.user_prompt", map[string]any{"prompt": "delete the build dir"}).request(200).
					add("codex.tool_decision", map[string]any{"tool_name": "exec_command", "call_id": "call_x", "decision": "denied", "source": "User"}).
					completed(100)
			},
			check: func(t *testing.T, calls []*store.Call) {
				var tool *store.Call
				for _, c := range calls {
					if c.OpName == "codex.tool.exec_command" {
						tool = c
					}
				}
				if tool == nil || tool.Exception == nil || *tool.Exception != "Rejected by the user" || attr(tool, "gen_ai.tool.call.id") != "call_x" ||
					attr(tool, "codex/decision") != "denied" || attr(tool, "codex/decision_source") != "User" {
					t.Fatalf("denied tool = %+v", tool)
				}
				turn := turnsOf(calls, "codex.turn")
				if len(turn) != 1 || turn[0].Exception != nil || runStatus(turn[0]) != "failed_step" {
					t.Fatalf("turn after a denial: %v", turn)
				}
			},
		},
		{
			name: "the last request failed",
			script: func(s *cxScript) {
				s.add("codex.user_prompt", map[string]any{"prompt": "hi"}).
					add("codex.api_request", map[string]any{"http.response.status_code": 429, "error.message": "rate limited", "duration_ms": 30})
			},
			check: func(t *testing.T, calls []*store.Call) {
				turn := turnsOf(calls, "codex.turn")
				if len(turn) != 1 || turn[0].Exception == nil || *turn[0].Exception != "API error: 429: rate limited" || runStatus(turn[0]) != "error" {
					t.Fatalf("turn = %v", turn)
				}
			},
		},
		{
			name: "a running tool keeps the turn open",
			script: func(s *cxScript) {
				s.add("codex.user_prompt", map[string]any{"prompt": "build"}).request(200).
					add("codex.tool_decision", map[string]any{"tool_name": "exec_command", "call_id": "call_b", "decision": "approved", "source": "Config"}).
					completed(100)
			},
			check: func(t *testing.T, calls []*store.Call) {
				if len(turnsOf(calls, "codex.turn")) != 0 {
					t.Fatal("turn stored while its tool runs")
				}
				for _, c := range calls {
					if c.OpName == "codex.tool.exec_command" && (c.EndedAt != nil || runStatus(c) != "running") {
						t.Fatalf("running tool: ended %v status %q", c.EndedAt, runStatus(c))
					}
				}
			},
		},
		{
			name: "a request waiting for its answer keeps the turn open",
			script: func(s *cxScript) {
				s.add("codex.user_prompt", map[string]any{"prompt": "hi"}).request(200)
			},
			check: func(t *testing.T, calls []*store.Call) {
				if len(turnsOf(calls, "codex.turn")) != 0 || len(calls) != 1 || calls[0].EndedAt != nil {
					t.Fatalf("calls = %v", calls)
				}
			},
		},
		{
			name: "an MCP tool",
			script: func(s *cxScript) {
				s.add("codex.user_prompt", map[string]any{"prompt": "search"}).request(200).
					add("codex.tool_result", map[string]any{"tool_name": "docs__search", "call_id": "call_m", "success": "true", "duration_ms": 40,
						"arguments": `{"query":"otel"}`, "output": "3 results", "mcp_server": "docs"}).completed(100).request(200).completed(120)
			},
			check: func(t *testing.T, calls []*store.Call) {
				tool := spanByName(t, calls, "docs__search: otel")
				if attr(tool, "gen_ai.tool.type") != "mcp" || attr(tool, "mcp.server.name") != "docs" || tool.Output != "3 results" {
					t.Fatalf("MCP tool = %v output %v", tool.Attributes, tool.Output)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newCCHarness(t)
			s := newCxScript("cx-" + strings.ReplaceAll(tc.name, " ", "-"))
			tc.script(s)
			h.ingestAs(CodexLogSource, s.events)
			tc.check(t, h.spans(s.session))
		})
	}
}

// One source per Codex session: the weft-codex plugin wins over the log
// events, and an AgentShield verdict follows the tool call id.
func TestCodexLogsPluginAndVerdicts(t *testing.T) {
	ctx := context.Background()
	h := newCCHarness(t)
	h.ingestFixture("codex/resume_json")
	tool := spanByName(t, h.spans(cxResumeSession), "exec_command: echo hi")
	at, _ := time.Parse(time.RFC3339Nano, tool.StartedAt)
	if _, err := h.s.UpsertDetections(ctx, ccProject, []store.DetectionInput{{EventID: "e1", SessionID: cxResumeSession, ToolCallID: "call_resp_1_0",
		ToolName: "Bash", Action: store.DetectionBlock, CreatedAt: at.UnixMicro()}}); err != nil {
		t.Fatal(err)
	}
	det := func() *store.Detection {
		t.Helper()
		ds, _, err := h.s.QueryDetections(ctx, store.DetectionQuery{ProjectID: ccProject})
		if err != nil || len(ds) != 1 {
			t.Fatalf("detections %v %v", ds, err)
		}
		return &ds[0]
	}
	if d := det(); deref(d.SpanID) != tool.ID || d.Link != store.LinkToolCallID {
		t.Fatalf("verdict on the log span: %v %s", d.SpanID, d.Link)
	}

	// The plugin's spans for the same session (as plugins/weft-codex sends them).
	sess, turnID := cxResumeSession, "plugin-turn"
	plugin := func(id, op string, parent *string, extra map[string]any) store.CompleteCall {
		attrs := map[string]any{"gen_ai.operation.name": op, "gen_ai.conversation.id": sess, "weft": map[string]any{"source": store.SourceCodexPlugin}}
		for k, v := range extra {
			attrs[k] = v
		}
		return store.CompleteCall{Start: store.CallStart{ProjectID: ccProject, ID: id, TraceID: turnID, ParentID: parent, OpName: op,
			ThreadID: &sess, TurnID: &turnID, StartedAt: tool.StartedAt, Attributes: attrs},
			End: &store.CallEnd{EndedAt: deref(tool.EndedAt), Summary: map[string]any{}}}
	}
	if _, err := h.s.UpsertComplete(ctx, []store.CompleteCall{plugin(turnID, "invoke_agent", nil, map[string]any{"gen_ai.agent.name": "codex"}),
		plugin("plugin-tool", "execute_tool", &turnID, map[string]any{"gen_ai.tool.name": "exec_command", "gen_ai.tool.call.id": "call_resp_1_0"})}); err != nil {
		t.Fatal(err)
	}
	if s := h.sessions(); s[cxResumeSession] != 2 {
		t.Fatalf("after the plugin: %v spans, want only the plugin's 2", s)
	}
	if d := det(); deref(d.SpanID) != "plugin-tool" || d.Link != store.LinkToolCallID {
		t.Fatalf("verdict after the plugin: %v %s", d.SpanID, d.Link)
	}
	// Log events re-sent later are dropped.
	all := loadLogFixture(t, "codex/resume_json").events(t, CodexLogSource)
	for i := range all {
		all[i].Key += "-again"
	}
	h.ingestAs(CodexLogSource, all)
	if s := h.sessions(); s[cxResumeSession] != 2 {
		t.Fatalf("logs after the plugin: %v", s)
	}
}

func TestLogHarnessDispatch(t *testing.T) {
	body := []byte(`{"resourceLogs":[
	 {"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"my-app"}}]},
	  "scopeLogs":[{"scope":{"name":"app"},"logRecords":[{"attributes":[{"key":"event.name","value":{"stringValue":"codex.user_prompt"}},
	   {"key":"conversation.id","value":{"stringValue":"c"}}]}]}]},
	 {"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"codex_cli_rs"}}]},
	  "scopeLogs":[{"scope":{"name":"codex_otel.log_only"},"logRecords":[
	   {"attributes":[{"key":"event.name","value":{"stringValue":"codex.user_prompt"}},{"key":"conversation.id","value":{"stringValue":"c"}},
	    {"key":"event.timestamp","value":{"stringValue":"2026-11-06T09:00:00.000Z"}},{"key":"prompt","value":{"stringValue":"hi"}}]},
	   {"attributes":[{"key":"event.name","value":{"stringValue":"codex.startup_phase"}},{"key":"conversation.id","value":{"stringValue":"c"}}]}]}]},
	 {"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"gemini-cli"}}]},
	  "scopeLogs":[{"scope":{"name":"gemini-cli"},"logRecords":[
	   {"attributes":[{"key":"event.name","value":{"stringValue":"gemini_cli.user_prompt"}},{"key":"session.id","value":{"stringValue":"g"}},
	    {"key":"prompt_id","value":{"stringValue":"g"}}]},
	   {"attributes":[{"key":"event.name","value":{"stringValue":"gemini_cli.config"}},{"key":"session.id","value":{"stringValue":"g"}}]}]}]}]}`)
	req, err := DecodeLogs(body, true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := SessionLogEvents(req, "")
	if err != nil {
		t.Fatal(err)
	}
	cx, gm := b.Events[CodexLogSource]["default/default"], b.Events[GeminiLogSource]["default/default"]
	if len(cx) != 1 || !cx[0].Opens || len(gm) != 1 || !gm[0].Opens || b.Ignored != 3 || len(b.Events[ClaudeCodeLogSource]) != 0 {
		t.Fatalf("codex %v gemini %v ignored %d", cx, gm, b.Ignored)
	}
}
