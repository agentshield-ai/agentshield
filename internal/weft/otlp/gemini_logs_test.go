package otlp

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// Fixtures: real Gemini CLI 0.67.0 exports (gemini -p against a local fake
// Gemini API server, GEMINI_TELEMETRY_OTLP_PROTOCOL=http, which is
// OTLP/HTTP JSON), with paths, the host and the installation id replaced
// and the request payloads (request_text, gen_ai.input.messages, system
// instructions), which Weft does not store, trimmed.
//
//	resume_json/    a turn running `echo hi`, then `gemini --resume latest` (a second
//	                process, same session and same prompt_id): a read_file the
//	                workspace check refuses
//	subagent_json/  invoke_agent runs the generalist sub-agent, which runs
//	                `cat note.txt` and calls complete_task
//	flaky_json/     each request first answered 503, retried
//	notool_json/    a shell command in non-interactive mode without --yolo: the
//	                tool is not registered
const (
	gmResumeSession = "54ab7461-9f16-48e3-ab35-d65df9f60c8e"
	gmSubSession    = "eeb9d4c9-b3a9-48b7-b1b6-0310d552743a"
)

func TestGeminiLogsRealRuns(t *testing.T) {
	t.Run("two turns, two processes", func(t *testing.T) {
		h := newCCHarness(t)
		if ignored := h.ingestFixture("gemini_cli/resume_json"); ignored == 0 {
			t.Fatal("noise events (config, startup_stats, gen_ai payload copies, ...) should be counted as ignored")
		}
		if s := h.sessions(); len(s) != 1 || s[gmResumeSession] != 8 {
			t.Fatalf("sessions = %v, want one with 8 spans", s)
		}
		calls := h.spans(gmResumeSession)
		turns := turnsOf(calls, "gemini_cli.turn")
		if len(turns) != 2 {
			t.Fatalf("turns = %d (both processes reuse prompt_id %s)", len(turns), gmResumeSession)
		}
		for i, turn := range turns {
			if turn.ParentID != nil || turn.EndedAt == nil || turn.Exception != nil || attr(turn, "gen_ai.agent.name") != "gemini-cli" ||
				attr(turn, "gen_ai.provider.name") != "gcp.gemini" || attr(turn, "gen_ai.request.model") != "gemini-2.5-flash" ||
				attr(turn, "weft/source") != store.SourceGeminiLogs || attr(turn, "gemini_cli/prompt_id") != gmResumeSession {
				t.Fatalf("turn %d = %+v %v", i, turn, turn.Attributes)
			}
			r, p, c, cr, _, cost := usage(turn)
			if r != 2 || p != 900+950 || c != (20+15)*2 || cr != 800 || cost <= 0 {
				t.Fatalf("turn %d usage: %v requests %v prompt %v completion %v cached $%v", i, r, p, c, cr, cost)
			}
		}
		if inputText(turns[0]) != "Run `echo hi` and tell me what it printed." || inputText(turns[1]) != "READ the note please." {
			t.Fatalf("prompts %q %q", inputText(turns[0]), inputText(turns[1]))
		}
		if out, _ := json.Marshal(turns[0].Output); !strings.Contains(string(out), "The command printed hi.") {
			t.Fatalf("turn output = %s", out)
		}
		shell := spanByName(t, calls, "run_shell_command: echo hi")
		if id, _ := attr(shell, "gen_ai.tool.call.id").(string); !strings.HasPrefix(id, "run_shell_command__run_shell_command_") ||
			shell.Exception != nil || deref(shell.ParentID) != turns[0].ID || shell.Inputs["command"] != "echo hi" {
			t.Fatalf("shell tool: id %v exc %v parent %v inputs %v", attr(shell, "gen_ai.tool.call.id"), shell.Exception, shell.ParentID, shell.Inputs)
		}
		var read *store.Call
		for _, c := range calls {
			if c.OpName == "gemini_cli.tool.read_file" {
				read = c
			}
		}
		if read == nil || read.Exception == nil || !strings.HasPrefix(*read.Exception, "Path not in workspace") || runStatus(turns[1]) != "failed_step" {
			t.Fatalf("refused read: %+v; turn status %q", read, runStatus(turns[1]))
		}
		asked := spanByName(t, calls, "→ run_shell_command")
		if out, _ := json.Marshal(asked.Output); !strings.Contains(string(out), `"tool_use"`) || !strings.Contains(string(out), "tool_calls") {
			t.Fatalf("request output = %s", out)
		}
	})
	t.Run("a sub-agent", func(t *testing.T) {
		h := newCCHarness(t)
		h.ingestFixture("gemini_cli/subagent_json")
		calls := h.spans(gmSubSession)
		turn := turnsOf(calls, "gemini_cli.turn")[0]
		invoke := spanByName(t, calls, "invoke_agent: Run `cat note.txt` and report its contents.")
		sub := spanByName(t, calls, "invoke_agent generalist")
		cat := spanByName(t, calls, "run_shell_command: cat note.txt")
		done := spanByName(t, calls, "complete_task")
		if deref(invoke.ParentID) != turn.ID || deref(sub.ParentID) != invoke.ID || deref(cat.ParentID) != sub.ID || deref(done.ParentID) != sub.ID {
			t.Fatalf("nesting: invoke under %v, sub-agent under %v, cat under %v", invoke.ParentID, sub.ParentID, cat.ParentID)
		}
		if attr(sub, "gen_ai.agent.name") != "generalist" || sub.EndedAt == nil || sub.Exception != nil || attr(sub, "gemini_cli/terminate_reason") != "GOAL" ||
			inputText(sub) != "Run `cat note.txt` and report its contents." {
			t.Fatalf("sub-agent = %v inputs %v", sub.Attributes, sub.Inputs)
		}
		if r, _, _, _, _, _ := usage(invoke); r != 2 {
			t.Fatalf("the sub-agent's 2 requests should roll up into invoke_agent: %v", r)
		}
		if r, _, _, _, _, _ := usage(turn); r != 4 || runStatus(turn) != "ok" {
			t.Fatalf("turn: %v requests, status %q", r, runStatus(turn))
		}
	})
	t.Run("retried API errors", func(t *testing.T) {
		h := newCCHarness(t)
		h.ingestFixture("gemini_cli/flaky_json")
		calls := h.spans(sessionOf(t, h))
		var failed int
		for _, c := range calls {
			if c.OpName == "gemini_cli.llm" && c.Exception != nil {
				if *c.Exception != "API error 503: UNAVAILABLE: temporary upstream failure" {
					t.Fatalf("failed request: %v", *c.Exception)
				}
				failed++
			}
		}
		turn := turnsOf(calls, "gemini_cli.turn")[0]
		if failed != 2 || turn.Exception != nil || turn.EndedAt == nil {
			t.Fatalf("%d failed requests; turn exc %v", failed, turn.Exception)
		}
	})
	t.Run("a tool that is not registered", func(t *testing.T) {
		h := newCCHarness(t)
		h.ingestFixture("gemini_cli/notool_json")
		calls := h.spans(sessionOf(t, h))
		tool := spanByName(t, calls, "run_shell_command: touch made-by-gemini.txt")
		if tool.Exception == nil || !strings.HasPrefix(*tool.Exception, `Tool "run_shell_command" not found`) || attr(tool, "gemini_cli/error_type") != "tool_not_registered" {
			t.Fatalf("tool exc %v attrs %v", tool.Exception, tool.Attributes["gemini_cli"])
		}
	})
}

func TestGeminiLogsBatching(t *testing.T) {
	for _, dir := range []string{"gemini_cli/resume_json", "gemini_cli/subagent_json"} {
		all := loadLogFixture(t, dir).events(t, GeminiLogSource)
		ref := newCCHarness(t)
		ref.ingestFixture(dir)
		session := sessionOf(t, ref)
		snapshot := func(h *ccHarness) string {
			var lines []string
			for _, c := range h.spans(session) {
				r, p, comp, _, _, _ := usage(c)
				lines = append(lines, strings.Join([]string{c.ID, deref(c.ParentID), deref(c.DisplayName), c.StartedAt, deref(c.EndedAt),
					deref(c.Exception), runStatus(c), jsonStr(r), jsonStr(p), jsonStr(comp), jsonStr(attr(c, "gen_ai.tool.call.id"))}, "|"))
			}
			sort.Strings(lines)
			return strings.Join(lines, "\n")
		}
		want := snapshot(ref)
		check := func(t *testing.T, h *ccHarness) {
			t.Helper()
			if got := snapshot(h); got != want {
				t.Fatalf("spans differ from the in-order ingest:\n%s\n--- want\n%s", got, want)
			}
		}
		t.Run(dir+"/re-sent", func(t *testing.T) {
			h := newCCHarness(t)
			h.ingestFixture(dir)
			if n := h.ingestAs(GeminiLogSource, all); n != 0 {
				t.Fatalf("re-sent events stored again: %d", n)
			}
			check(t, h)
		})
		t.Run(dir+"/one event per batch", func(t *testing.T) {
			h := newCCHarness(t)
			for _, e := range all {
				h.ingestAs(GeminiLogSource, []store.LogEvent{e})
			}
			check(t, h)
		})
		t.Run(dir+"/reversed", func(t *testing.T) {
			h := newCCHarness(t)
			for i := len(all) - 1; i >= 0; i-- {
				h.ingestAs(GeminiLogSource, []store.LogEvent{all[i]})
			}
			check(t, h)
		})
	}
}

func TestGeminiLogsTurnCutOff(t *testing.T) {
	all := loadLogFixture(t, "gemini_cli/subagent_json").events(t, GeminiLogSource)
	// Up to the sub-agent's shell call: the invoke_agent call is still running.
	var cut []store.LogEvent
	for _, e := range all {
		cut = append(cut, e)
		if e.Name == "gemini_cli.tool_call" {
			break
		}
	}
	shift := time.Now().Add(-time.Minute).UnixMicro() - cut[0].TimeUS
	for i := range cut {
		cut[i].TimeUS += shift
	}
	h := newCCHarness(t)
	h.ingestAs(GeminiLogSource, cut)
	calls := h.spans(gmSubSession)
	if len(turnsOf(calls, "gemini_cli.turn")) != 0 {
		t.Fatal("an unfinished turn was stored")
	}
	sub := spanByName(t, calls, "invoke_agent generalist")
	if sub.EndedAt != nil || runStatus(sub) != "running" {
		t.Fatalf("running sub-agent: ended %v status %q", sub.EndedAt, runStatus(sub))
	}
	next := store.LogEvent{Key: "next", SessionID: gmSubSession, GroupID: "prompt:next", Opens: true, Name: "gemini_cli.user_prompt",
		TimeUS: time.Now().UnixMicro(), Attrs: map[string]any{"session.id": gmSubSession, "prompt": "next", "prompt_id": gmSubSession}}
	h.ingestAs(GeminiLogSource, []store.LogEvent{next})
	calls = h.spans(gmSubSession)
	turns := turnsOf(calls, "gemini_cli.turn")
	if len(turns) != 1 || turns[0].Exception == nil || !strings.Contains(*turns[0].Exception, "interrupted") {
		t.Fatalf("first turn after the next prompt: %v", turns)
	}
	if sub := spanByName(t, calls, "invoke_agent generalist"); sub.Exception == nil {
		t.Fatal("the unfinished sub-agent of a closed turn should be closed as failed")
	}
}

// Synthetic events in Gemini CLI's shapes (packages/core telemetry/types).
type gmScript struct {
	session string
	t0      time.Time
	n       int64
	events  []store.LogEvent
}

func newGmScript(session string) *gmScript {
	return &gmScript{session: session, t0: time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)}
}

func (s *gmScript) add(name string, attrs map[string]any) *gmScript {
	s.n++
	a := map[string]any{"session.id": s.session, "event.name": name, "prompt_id": s.session, "installation.id": "x", "user.email": "dev@example.com",
		"event.timestamp": s.t0.Add(time.Duration(s.n) * time.Second).Format("2006-01-02T15:04:05.000Z")}
	for k, v := range attrs {
		a[k] = v
	}
	e := store.LogEvent{Key: logEventKey(name, s.session, aStr(a, "event.timestamp"), a), SessionID: s.session, Name: name, Seq: s.n,
		TimeUS: logEventTime(a, 0, 0), Attrs: a}
	if name == "gemini_cli.user_prompt" {
		e.Opens, e.GroupID = true, "prompt:"+e.Key
	}
	s.events = append(s.events, e)
	return s
}

func (s *gmScript) response(text string, calls ...string) *gmScript {
	var parts []any
	if text != "" {
		parts = append(parts, map[string]any{"text": text})
	}
	for _, c := range calls {
		parts = append(parts, map[string]any{"functionCall": map[string]any{"name": c, "args": map[string]any{"command": "make"}, "id": c + "_1"}})
	}
	rt, _ := json.Marshal([]any{map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": parts}}}}})
	s.add("gemini_cli.api_request", map[string]any{"model": "gemini-2.5-pro", "role": "main"})
	return s.add("gemini_cli.api_response", map[string]any{"model": "gemini-2.5-pro", "role": "main", "duration_ms": 500, "input_token_count": 100,
		"output_token_count": 10, "thoughts_token_count": 5, "cached_content_token_count": 0, "response_text": string(rt)})
}

func TestGeminiLogsGatesAndOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		script func(s *gmScript)
		check  func(t *testing.T, calls []*store.Call)
	}{
		{
			name: "prompt and payloads not exported",
			script: func(s *gmScript) {
				s.add("gemini_cli.user_prompt", map[string]any{"prompt_length": 12}).
					add("gemini_cli.api_request", map[string]any{"model": "gemini-2.5-pro", "role": "main"}).
					add("gemini_cli.api_response", map[string]any{"model": "gemini-2.5-pro", "role": "main", "duration_ms": 500, "input_token_count": 100, "output_token_count": 10})
			},
			check: func(t *testing.T, calls []*store.Call) {
				turn := turnsOf(calls, "gemini_cli.turn")
				if len(turn) != 1 || turn[0].Inputs[promptNotExportedKey] != geminiPromptNotExported || turn[0].Output != nil {
					t.Fatalf("turn = %v", turn)
				}
			},
		},
		{
			name: "identity is not stored",
			script: func(s *gmScript) {
				s.add("gemini_cli.user_prompt", map[string]any{"prompt": "hi"}).response("hello")
			},
			check: func(t *testing.T, calls []*store.Call) {
				for _, c := range calls {
					if b, _ := json.Marshal(c.Attributes); strings.Contains(string(b), "dev@example.com") {
						t.Fatalf("%s carries the account email", deref(c.DisplayName))
					}
				}
			},
		},
		{
			name: "tool rejected by the user",
			script: func(s *gmScript) {
				s.add("gemini_cli.user_prompt", map[string]any{"prompt": "build"}).response("", "run_shell_command").
					add("gemini_cli.tool_call", map[string]any{"function_name": "run_shell_command", "decision": "reject", "success": false,
						"duration_ms": 0, "error": "Tool call cancelled by user", "function_args": `{"command":"make"}`})
			},
			check: func(t *testing.T, calls []*store.Call) {
				tool := spanByName(t, calls, "run_shell_command: make")
				if tool.Exception == nil || *tool.Exception != "Rejected by the user" || attr(tool, "gen_ai.tool.call.id") != "run_shell_command_1" {
					t.Fatalf("rejected tool: exc %v id %v", tool.Exception, attr(tool, "gen_ai.tool.call.id"))
				}
				turn := turnsOf(calls, "gemini_cli.turn")
				if len(turn) != 1 || turn[0].Exception != nil || runStatus(turn[0]) != "failed_step" {
					t.Fatalf("turn after a rejection: %v", turn)
				}
			},
		},
		{
			name: "the last request failed",
			script: func(s *gmScript) {
				s.add("gemini_cli.user_prompt", map[string]any{"prompt": "hi"}).
					add("gemini_cli.api_request", map[string]any{"model": "gemini-2.5-pro", "role": "main"}).
					add("gemini_cli.api_error", map[string]any{"model": "gemini-2.5-pro", "role": "main", "duration_ms": 30, "status_code": 429,
						"error": `{"error":{"code":429,"message":"quota exhausted","status":"RESOURCE_EXHAUSTED"}}`})
			},
			check: func(t *testing.T, calls []*store.Call) {
				turn := turnsOf(calls, "gemini_cli.turn")
				if len(turn) != 1 || turn[0].Exception == nil || *turn[0].Exception != "API error: RESOURCE_EXHAUSTED: quota exhausted" || runStatus(turn[0]) != "error" {
					t.Fatalf("turn = %v", turn)
				}
			},
		},
		{
			name: "a function call not yet run keeps the turn open",
			script: func(s *gmScript) {
				s.add("gemini_cli.user_prompt", map[string]any{"prompt": "build"}).response("", "run_shell_command")
			},
			check: func(t *testing.T, calls []*store.Call) {
				if len(turnsOf(calls, "gemini_cli.turn")) != 0 {
					t.Fatal("turn stored while its tool runs")
				}
			},
		},
		{
			name: "a request in flight keeps the turn open",
			script: func(s *gmScript) {
				s.add("gemini_cli.user_prompt", map[string]any{"prompt": "build"}).response("", "run_shell_command").
					add("gemini_cli.tool_call", map[string]any{"function_name": "run_shell_command", "decision": "accept", "success": true, "duration_ms": 900}).
					add("gemini_cli.api_request", map[string]any{"model": "gemini-2.5-pro", "role": "main"})
			},
			check: func(t *testing.T, calls []*store.Call) {
				if len(turnsOf(calls, "gemini_cli.turn")) != 0 {
					t.Fatal("turn stored while a request is in flight")
				}
			},
		},
		{
			name: "side requests do not end the turn",
			script: func(s *gmScript) {
				s.add("gemini_cli.user_prompt", map[string]any{"prompt": "build"}).response("", "run_shell_command").
					add("gemini_cli.tool_call", map[string]any{"function_name": "run_shell_command", "decision": "accept", "success": true, "duration_ms": 900}).
					add("gemini_cli.api_response", map[string]any{"model": "gemini-2.5-flash-lite", "role": "utility_loop_detector", "duration_ms": 50,
						"input_token_count": 10, "output_token_count": 1})
			},
			check: func(t *testing.T, calls []*store.Call) {
				if len(turnsOf(calls, "gemini_cli.turn")) != 0 {
					t.Fatal("a loop check closed the turn")
				}
				spanByName(t, calls, "chat gemini-2.5-flash-lite (utility_loop_detector)")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newCCHarness(t)
			s := newGmScript("gm-" + strings.ReplaceAll(tc.name, " ", "-"))
			tc.script(s)
			// Through the extractor, as the server does, so dropped attributes are dropped.
			req := scriptExport(s.events)
			h.ingestExport(req)
			tc.check(t, h.spans(s.session))
		})
	}
}

// AgentShield's Gemini hook sends no tool call id (Gemini CLI's hook payload
// has none): a verdict links by session, tool name and time.
func TestGeminiLogsVerdictByTime(t *testing.T) {
	ctx := context.Background()
	h := newCCHarness(t)
	h.ingestFixture("gemini_cli/resume_json")
	shell := spanByName(t, h.spans(gmResumeSession), "run_shell_command: echo hi")
	at, _ := time.Parse(time.RFC3339Nano, shell.StartedAt)
	if _, err := h.s.UpsertDetections(ctx, ccProject, []store.DetectionInput{{EventID: "e1", SessionID: gmResumeSession, ToolName: "run_shell_command",
		Action: store.DetectionBlock, CreatedAt: at.Add(-50 * time.Millisecond).UnixMicro()}}); err != nil {
		t.Fatal(err)
	}
	ds, _, err := h.s.QueryDetections(ctx, store.DetectionQuery{ProjectID: ccProject})
	if err != nil || len(ds) != 1 || deref(ds[0].SpanID) != shell.ID || ds[0].Link != store.LinkNearestTime {
		t.Fatalf("detections %+v %v", ds, err)
	}
}

// scriptExport wraps synthetic Gemini CLI events in a logs export.
func scriptExport(evs []store.LogEvent) *collogs.ExportLogsServiceRequest {
	var recs []*logspb.LogRecord
	for _, e := range evs {
		var kvs []*common.KeyValue
		keys := make([]string, 0, len(e.Attrs))
		for k := range e.Attrs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			var v *common.AnyValue
			switch x := e.Attrs[k].(type) {
			case string:
				v = &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: x}}
			case bool:
				v = &common.AnyValue{Value: &common.AnyValue_BoolValue{BoolValue: x}}
			case int:
				v = &common.AnyValue{Value: &common.AnyValue_IntValue{IntValue: int64(x)}}
			default:
				v = &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: jsonStr(x)}}
			}
			kvs = append(kvs, &common.KeyValue{Key: k, Value: v})
		}
		recs = append(recs, &logspb.LogRecord{Attributes: kvs})
	}
	return &collogs.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource:  &resourcepb.Resource{Attributes: []*common.KeyValue{{Key: "service.name", Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: GeminiService}}}}},
		ScopeLogs: []*logspb.ScopeLogs{{Scope: &common.InstrumentationScope{Name: GeminiService}, LogRecords: recs}},
	}}}
}
