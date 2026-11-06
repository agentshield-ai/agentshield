package otlp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/agentshield-ai/agentshield/internal/weft/weftlog"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"
)

// Fixtures: real Claude Code 2.1.318 exports (claude -p), with account
// identity and file paths replaced.
//
//	read_json/            OTLP/HTTP JSON: one prompt, a Read tool call, then the answer
//	bash_subagent_proto/  OTLP/HTTP protobuf: a Bash call, an Agent call running a
//	                      general-purpose sub-agent that does a Read, then the answer
const (
	readSession = "74deee12-92fa-4127-9157-1af0dced000d"
	readPrompt  = "dcf03ea3-89d8-438c-9255-c50e58f5b301"
	readTool    = "toolu_01DXUQ8ESEs5J12ZyQWjYQv6"
	bashSession = "0c36c29e-0bb8-4e24-b31e-a4d8110ad3a8"
	bashTool    = "toolu_0172cnpPm5jcXnzXfwJ2vEjT"
	agentTool   = "toolu_01TSBzcU6bpjNFNbF3frsVUK"
	subReadTool = "toolu_014BowxrJ12by6WJZoNoLfdU"
	subAgentID  = "a75aa194b3deb8de4"
	ccProject   = "acme/claude"
)

type ccFixture struct {
	files  []string
	isJSON bool
}

func loadFixture(t *testing.T, dir string) ccFixture {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join("testdata", "claude_code", dir, "*_logs.*"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatalf("no fixtures in %s", dir)
	}
	return ccFixture{files: files, isJSON: strings.HasSuffix(files[0], ".json")}
}

// batches decodes each exported request of a fixture.
func (f ccFixture) batches(t *testing.T) []*collogs.ExportLogsServiceRequest {
	t.Helper()
	var out []*collogs.ExportLogsServiceRequest
	for _, p := range f.files {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		req, err := DecodeLogs(b, f.isJSON)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		out = append(out, req)
	}
	return out
}

// events flattens a fixture to its session events, in export order.
func (f ccFixture) events(t *testing.T) []store.LogEvent {
	t.Helper()
	var out []store.LogEvent
	for _, req := range f.batches(t) {
		b, err := ClaudeCodeLogEvents(req, ccProject)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b.Events[ccProject]...)
	}
	return out
}

type ccHarness struct {
	t *testing.T
	s *store.Store
}

func newCCHarness(t *testing.T) *ccHarness {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "weft.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return &ccHarness{t: t, s: s}
}

func (h *ccHarness) ingest(evs []store.LogEvent) int {
	h.t.Helper()
	_, added, err := h.s.IngestLogEvents(context.Background(), ccProject, LogSources[0], evs)
	if err != nil {
		h.t.Fatal(err)
	}
	return added
}

func (h *ccHarness) ingestRequest(req *collogs.ExportLogsServiceRequest) LogBatch {
	h.t.Helper()
	b, err := ClaudeCodeLogEvents(req, ccProject)
	if err != nil {
		h.t.Fatal(err)
	}
	h.ingest(b.Events[ccProject])
	return b
}

// spans returns the session's stored spans by display name (op name for duplicates is a failure).
func (h *ccHarness) spans(session string) []*store.Call {
	h.t.Helper()
	calls, err := h.s.ListCalls(context.Background(), store.CallsQuery{ProjectID: ccProject, Filter: &store.CallsFilter{ThreadIDs: []string{session}},
		IncludeCosts: true, IncludeRunStatus: true})
	if err != nil {
		h.t.Fatal(err)
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].StartedAt < calls[j].StartedAt })
	return calls
}

func spanByName(t *testing.T, calls []*store.Call, display string) *store.Call {
	t.Helper()
	var found *store.Call
	for _, c := range calls {
		if c.DisplayName != nil && *c.DisplayName == display {
			if found != nil {
				t.Fatalf("two spans named %q", display)
			}
			found = c
		}
	}
	if found == nil {
		var names []string
		for _, c := range calls {
			names = append(names, deref(c.DisplayName))
		}
		t.Fatalf("no span %q in %v", display, names)
	}
	return found
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func attr(c *store.Call, path string) any {
	var v any = c.Attributes
	for _, p := range strings.Split(path, "/") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

// usage sums a call's rolled-up usage (summary.usage after the roll-up).
func usage(c *store.Call) (requests, prompt, completion, cacheRead, cacheWrite, cost float64) {
	u, _ := c.Summary["usage"].(map[string]any)
	for _, e := range u {
		m, _ := e.(map[string]any)
		f := func(k string) float64 { x, _ := store.ToFloat(m[k]); return x }
		requests += f("requests")
		prompt += f("prompt_tokens")
		completion += f("completion_tokens")
		cacheRead += f("cache_read_input_tokens")
		cacheWrite += f("cache_creation_input_tokens")
	}
	if w, ok := c.Summary["weft"].(map[string]any); ok {
		costs, _ := w["costs"].(map[string]any)
		for _, e := range costs {
			m, _ := e.(map[string]any)
			p, _ := store.ToFloat(m["prompt_tokens_total_cost"])
			o, _ := store.ToFloat(m["completion_tokens_total_cost"])
			cost += p + o
		}
	}
	return
}

func runStatus(c *store.Call) string {
	w, _ := c.Summary["weft"].(map[string]any)
	s, _ := w["run_status"].(string)
	return s
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// The two real runs.
func TestClaudeCodeLogsRealRuns(t *testing.T) {
	cases := []struct {
		name, dir, session string
		spans              int
		requests           float64
		prompt, completion float64
		cost               float64
		tools              map[string]string // display -> tool-use id
		prompt0            string
		answer             string
	}{
		{name: "read (JSON)", dir: "read_json", session: readSession, spans: 4, requests: 2,
			prompt: 2 + 26220 + 21113 + 2 + 47333 + 1011, completion: 110 + 4, cost: 0.176356 + 0.0176426,
			tools:   map[string]string{"Read: /home/dev/project/note.txt": readTool},
			prompt0: "Read note.txt with the Read tool and tell me its contents in one word.", answer: "hello"},
		{name: "bash and sub-agent (protobuf)", dir: "bash_subagent_proto", session: bashSession, spans: 10, requests: 5,
			prompt:     (2 + 27275 + 20096) + (2 + 47371 + 970) + (2 + 20036 + 17441) + (2 + 37477 + 1561) + (2 + 48341 + 537),
			completion: 74 + 219 + 110 + 61 + 4, cost: 0.167711 + 0.0216222 + 0.09342020000000001 + 0.016528400000000002 + 0.0140522,
			tools:   map[string]string{"Bash: echo hi": bashTool, "Agent: Read note.txt": agentTool, "Read: /home/dev/project/note.txt": subReadTool},
			prompt0: "First run 'echo hi' with the Bash tool.", answer: "hello"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newCCHarness(t)
			ignored := 0
			for _, req := range loadFixture(t, tc.dir).batches(t) {
				ignored += h.ingestRequest(req).Ignored
			}
			if ignored == 0 {
				t.Fatal("noise events (plugin_loaded, mcp_server_connection, ...) should be counted as ignored")
			}
			calls := h.spans(tc.session)
			if len(calls) != tc.spans {
				t.Fatalf("spans = %d, want %d", len(calls), tc.spans)
			}
			turn := spanByName(t, calls, "invoke_agent claude-code")
			if turn.ParentID != nil || deref(turn.TurnID) != turn.ID || attr(turn, "gen_ai.operation.name") != "invoke_agent" ||
				attr(turn, "gen_ai.agent.name") != "claude-code" || attr(turn, "weft/source") != store.SourceClaudeCodeLogs {
				t.Fatalf("turn = %+v", turn)
			}
			if turn.EndedAt == nil || turn.Exception != nil || runStatus(turn) != "ok" {
				t.Fatalf("turn should be finished ok: ended %v exc %v status %q", turn.EndedAt, turn.Exception, runStatus(turn))
			}
			msgs, _ := turn.Inputs["messages"].([]any)
			if len(msgs) != 1 || !strings.HasPrefix(msgs[0].(map[string]any)["content"].(string), tc.prompt0) {
				t.Fatalf("turn input = %v", turn.Inputs)
			}
			if out, _ := json.Marshal(turn.Output); !strings.Contains(string(out), `"content":"`+tc.answer+`"`) {
				t.Fatalf("turn output = %s", out)
			}
			for _, c := range calls {
				if deref(c.ThreadID) != tc.session || deref(c.TurnID) != turn.ID || c.TraceID != turn.ID {
					t.Fatalf("%s: session %v turn %v trace %s", deref(c.DisplayName), c.ThreadID, c.TurnID, c.TraceID)
				}
			}
			for display, id := range tc.tools {
				tool := spanByName(t, calls, display)
				if attr(tool, "gen_ai.tool.call.id") != id || tool.EndedAt == nil || tool.Exception != nil {
					t.Fatalf("%s: call id %v ended %v exc %v", display, attr(tool, "gen_ai.tool.call.id"), tool.EndedAt, tool.Exception)
				}
			}
			req, prompt, completion, _, _, cost := usage(turn)
			if req != tc.requests || prompt != tc.prompt || completion != tc.completion {
				t.Fatalf("turn usage = %v requests, %v prompt, %v completion; want %v, %v, %v", req, prompt, completion, tc.requests, tc.prompt, tc.completion)
			}
			if !near(cost, tc.cost) {
				t.Fatalf("turn cost = %.10f, want the reported %.10f", cost, tc.cost)
			}
		})
	}
}

func TestClaudeCodeLogsSubagentNesting(t *testing.T) {
	h := newCCHarness(t)
	for _, req := range loadFixture(t, "bash_subagent_proto").batches(t) {
		h.ingestRequest(req)
	}
	calls := h.spans(bashSession)
	turn := spanByName(t, calls, "invoke_agent claude-code")
	agentCall := spanByName(t, calls, "Agent: Read note.txt")
	sub := spanByName(t, calls, "invoke_agent general-purpose")
	read := spanByName(t, calls, "Read: /home/dev/project/note.txt")
	if deref(agentCall.ParentID) != turn.ID || deref(sub.ParentID) != agentCall.ID || deref(read.ParentID) != sub.ID {
		t.Fatalf("nesting: Agent under %v, sub-agent under %v, Read under %v", agentCall.ParentID, sub.ParentID, read.ParentID)
	}
	if attr(sub, "gen_ai.agent.name") != "general-purpose" || attr(sub, "gen_ai.agent.id") != subAgentID || sub.EndedAt == nil {
		t.Fatalf("sub-agent = %v", sub.Attributes)
	}
	var subLLMs, mainLLMs int
	for _, c := range calls {
		if c.OpName != "claude_code.llm" {
			continue
		}
		switch deref(c.ParentID) {
		case sub.ID:
			subLLMs++
		case turn.ID:
			mainLLMs++
		default:
			t.Fatalf("LLM span %s under %v", deref(c.DisplayName), c.ParentID)
		}
	}
	if subLLMs != 2 || mainLLMs != 3 {
		t.Fatalf("LLM spans: %d in the sub-agent, %d in the turn", subLLMs, mainLLMs)
	}
	// The sub-agent's own two requests roll up into the Agent call, once.
	if r, _, _, _, _, cost := usage(agentCall); r != 2 || !near(cost, 0.09342020000000001+0.016528400000000002) {
		t.Fatalf("Agent call usage = %v requests, $%v", r, cost)
	}
	in, _ := sub.Inputs["messages"].([]any)
	if len(in) != 1 || !strings.Contains(in[0].(map[string]any)["content"].(string), "report its exact contents") {
		t.Fatalf("sub-agent input = %v", sub.Inputs)
	}
}

// Re-sent, split and out-of-order batches give the same spans and totals.
func TestClaudeCodeLogsBatching(t *testing.T) {
	fx := loadFixture(t, "bash_subagent_proto")
	all := fx.events(t)
	want := func(t *testing.T, h *ccHarness) {
		t.Helper()
		calls := h.spans(bashSession)
		if len(calls) != 10 {
			t.Fatalf("spans = %d, want 10", len(calls))
		}
		turn := spanByName(t, calls, "invoke_agent claude-code")
		if r, p, c, _, _, cost := usage(turn); r != 5 || p != 221115 || c != 468 || !near(cost, 0.313334) {
			t.Fatalf("turn usage = %v %v %v $%.8f", r, p, c, cost)
		}
		agentCall := spanByName(t, calls, "Agent: Read note.txt")
		if r, _, _, _, _, _ := usage(agentCall); r != 2 {
			t.Fatalf("Agent call requests = %v (counted twice or lost on a re-parent)", r)
		}
		sub := spanByName(t, calls, "invoke_agent general-purpose")
		if deref(sub.ParentID) != agentCall.ID || turn.Exception != nil || turn.EndedAt == nil {
			t.Fatalf("sub-agent parent %v; turn exc %v ended %v", sub.ParentID, turn.Exception, turn.EndedAt)
		}
	}
	t.Run("re-sent", func(t *testing.T) {
		h := newCCHarness(t)
		for _, req := range fx.batches(t) {
			h.ingestRequest(req)
		}
		for _, req := range fx.batches(t) {
			h.ingestRequest(req)
		}
		if n := h.ingest(all); n != 0 {
			t.Fatalf("re-sent events stored again: %d", n)
		}
		want(t, h)
	})
	t.Run("one event per batch", func(t *testing.T) {
		h := newCCHarness(t)
		for _, e := range all {
			h.ingest([]store.LogEvent{e})
		}
		want(t, h)
	})
	t.Run("reversed", func(t *testing.T) {
		h := newCCHarness(t)
		for i := len(all) - 1; i >= 0; i-- {
			h.ingest([]store.LogEvent{all[i]})
		}
		want(t, h)
	})
	t.Run("sub-agent before its Agent call", func(t *testing.T) {
		h := newCCHarness(t)
		var sub, rest []store.LogEvent
		for _, e := range all {
			if aStr(e.Attrs, "agent_id") != "" {
				sub = append(sub, e)
			} else {
				rest = append(rest, e)
			}
		}
		h.ingest(sub)
		// Without its Agent call the sub-agent hangs under the (open) turn.
		calls := h.spans(bashSession)
		s := spanByName(t, calls, "invoke_agent general-purpose")
		if deref(s.ParentID) != ccID(ccLogsNS, bashSession, "turn", aStr(all[0].Attrs, "prompt.id")) {
			t.Fatalf("sub-agent parent before the Agent call = %v", s.ParentID)
		}
		h.ingest(rest)
		want(t, h)
	})
}

// A turn whose events stop mid-way (the process died, or it is still
// running) has no turn span: its steps hang under a turn that has not
// arrived, which reads as running. A later prompt closes it as interrupted.
func TestClaudeCodeLogsTurnCutOff(t *testing.T) {
	reqs := loadFixture(t, "bash_subagent_proto").batches(t)
	var cut []store.LogEvent
	for _, req := range reqs[:6] { // up to the sub-agent's Read decision
		b, err := ClaudeCodeLogEvents(req, ccProject)
		if err != nil {
			t.Fatal(err)
		}
		cut = append(cut, b.Events[ccProject]...)
	}
	// With the capture's own (old) times, the run-status rule has already
	// given up waiting (store.OrphanRunTimeout): the steps' outcome shows.
	old := newCCHarness(t)
	old.ingest(cut)
	for _, c := range old.spans(bashSession) {
		if c.OpName == "claude_code.turn" && (c.EndedAt != nil || runStatus(c) == "running") {
			t.Fatalf("open turn long after the last event: ended %v, run status %q, want its steps' outcome", c.EndedAt, runStatus(c))
		}
	}
	// As if the events had just arrived: running.
	shift := time.Now().Add(-time.Minute).UnixMicro() - cut[0].TimeUS
	for i := range cut {
		cut[i].TimeUS += shift
	}
	h := newCCHarness(t)
	h.ingest(cut)
	calls := h.spans(bashSession)
	turnID := ""
	for _, c := range calls {
		if c.OpName == "claude_code.turn" && c.EndedAt != nil {
			t.Fatal("an unfinished turn was stored as ended")
		}
		turnID = deref(c.TurnID)
	}
	agentCall := spanByName(t, calls, "Agent")
	if agentCall.EndedAt != nil {
		t.Fatal("the Agent call has not finished")
	}
	// The open turn is running while its events are recent.
	if open := spanByName(t, calls, "invoke_agent claude-code"); open.ID != turnID || runStatus(open) != "running" {
		t.Fatalf("open turn %s (want %s): run status %q, want running", open.ID, turnID, runStatus(open))
	}

	// The user submits another prompt: the first turn is closed as interrupted.
	// It is logged once, at info, when it closes, although each batch of
	// the next turn rebuilds it (weftlog).
	var logBuf bytes.Buffer
	logCtx := weftlog.WithLogger(context.Background(), slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	logCtx, stats := store.WithIngestStats(logCtx)
	next := ccLogEvent("user_prompt", bashSession, "prompt-2", map[string]any{"session.id": bashSession, "prompt.id": "prompt-2",
		"event.timestamp": time.Now().UTC().Format(time.RFC3339Nano), "prompt": "NEXT-PROMPT-CANARY"}, nil, 0, 0)
	if _, _, err := h.s.IngestLogEvents(logCtx, ccProject, LogSources[0], []store.LogEvent{next}); err != nil {
		t.Fatal(err)
	}
	more := ccLogEvent("tool_decision", bashSession, "prompt-2", map[string]any{"session.id": bashSession, "prompt.id": "prompt-2",
		"event.timestamp": time.Now().UTC().Format(time.RFC3339Nano), "tool_name": "Glob", "decision": "accept", "tool_use_id": "toolu_x"}, nil, 0, 0)
	if _, _, err := h.s.IngestLogEvents(logCtx, ccProject, LogSources[0], []store.LogEvent{more}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(logBuf.String(), "level=INFO msg=\"turn closed as interrupted"); n != 1 || stats.TurnsInterrupted != 1 ||
		!strings.Contains(logBuf.String(), "session_id="+bashSession) || strings.Count(logBuf.String(), "msg=\"turn rebuilt\"") != 4 {
		t.Fatalf("interrupted lines = %d, stats %d:\n%s", n, stats.TurnsInterrupted, logBuf.String())
	}
	if strings.Contains(logBuf.String(), "NEXT-PROMPT-CANARY") {
		t.Fatalf("prompt text in the log:\n%s", logBuf.String())
	}
	calls = h.spans(bashSession)
	var turn, second *store.Call
	for _, c := range calls {
		switch {
		case c.ID == turnID:
			turn = c
		case c.OpName == "claude_code.turn":
			second = c
		}
	}
	if turn == nil || second == nil || second.EndedAt != nil {
		t.Fatalf("turns after the next prompt: first %v, second %v (want stored, open)", turn, second)
	}
	if turn.Exception == nil || !strings.Contains(*turn.Exception, "interrupted") {
		t.Fatalf("first turn after the next prompt: id %s exc %v", turn.ID, turn.Exception)
	}
	agentCall = spanByName(t, calls, "Agent")
	sub := spanByName(t, calls, "invoke_agent general-purpose")
	if agentCall.Exception == nil || sub.Exception == nil {
		t.Fatalf("unfinished steps of a closed turn: Agent exc %v, sub-agent exc %v", agentCall.Exception, sub.Exception)
	}
	if runStatus(turn) != "error" {
		t.Fatalf("interrupted turn status = %q", runStatus(turn))
	}
}

// Synthetic events in Claude Code's documented shapes.
type ccScript struct {
	session, prompt string
	t0              time.Time
	n               int
	events          []store.LogEvent
}

func newScript(session, prompt string) *ccScript {
	return &ccScript{session: session, prompt: prompt, t0: time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)}
}

func (s *ccScript) add(name string, attrs map[string]any) *ccScript {
	s.n++
	a := map[string]any{"session.id": s.session, "prompt.id": s.prompt, "event.name": name,
		"event.timestamp": s.t0.Add(time.Duration(s.n) * time.Second).Format("2006-01-02T15:04:05.000Z"), "event.sequence": json.Number(itoa(s.n))}
	for k, v := range attrs {
		a[k] = v
	}
	s.events = append(s.events, ccLogEvent(name, s.session, s.prompt, a, map[string]any{"service.name": "claude-code"}, 0, 0))
	return s
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestClaudeCodeLogsGatesAndOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		script func(s *ccScript)
		check  func(t *testing.T, calls []*store.Call)
	}{
		{
			name: "prompt and response not exported",
			script: func(s *ccScript) {
				s.add("user_prompt", map[string]any{"prompt_length": "12", "prompt": "<REDACTED>"}).
					add("api_request", map[string]any{"model": "claude-opus-5-5", "input_tokens": 3, "output_tokens": 5, "request_id": "req_1", "duration_ms": 900, "cost_usd": 0.001}).
					add("assistant_response", map[string]any{"response_length": 5, "response": "<REDACTED>", "request_id": "req_1"})
			},
			check: func(t *testing.T, calls []*store.Call) {
				turn := spanByName(t, calls, "invoke_agent claude-code")
				if _, ok := turn.Inputs["messages"]; ok || turn.Inputs[promptNotExportedKey] != promptNotExported {
					t.Fatalf("turn inputs = %v", turn.Inputs)
				}
				if store.InputText(turn.Inputs) != "" {
					t.Fatalf("the placeholder reads as the user's message: %q", store.InputText(turn.Inputs))
				}
				if turn.Output != nil || attr(turn, "gen_ai.conversation.name") != nil || attr(turn, "claude_code/prompt_exported") != false {
					t.Fatalf("turn output %v name %v", turn.Output, attr(turn, "gen_ai.conversation.name"))
				}
				if llm := spanByName(t, calls, "chat claude-opus-5-5"); llm.Output != nil || turn.EndedAt == nil {
					t.Fatalf("LLM output %v; turn ended %v", llm.Output, turn.EndedAt)
				}
			},
		},
		{
			name: "MCP tools keep their real name",
			script: func(s *ccScript) {
				s.add("user_prompt", map[string]any{"prompt": "look at the page"}).
					add("tool_decision", map[string]any{"decision": "accept", "source": "config", "tool_name": "mcp_tool", "tool_use_id": "toolu_m", "request_id": "req_1",
						"tool_parameters": `{"mcp_server_name":"claude-in-chrome","mcp_tool_name":"navigate"}`}).
					add("tool_result", map[string]any{"tool_name": "mcp_tool", "tool_use_id": "toolu_m", "success": "true", "duration_ms": "40", "request_id": "req_1",
						"tool_parameters": `{"mcp_server_name":"claude-in-chrome","mcp_tool_name":"navigate"}`, "tool_input": `{"url":"http://localhost:6345/"}`}).
					add("api_request", map[string]any{"model": "claude-opus-5-5", "input_tokens": 3, "output_tokens": 5, "request_id": "req_1", "duration_ms": 500})
			},
			check: func(t *testing.T, calls []*store.Call) {
				tool := spanByName(t, calls, "mcp__claude-in-chrome__navigate: http://localhost:6345/")
				if attr(tool, "gen_ai.tool.name") != "mcp__claude-in-chrome__navigate" || attr(tool, "mcp.server.name") != "claude-in-chrome" || attr(tool, "gen_ai.tool.type") != "mcp" {
					t.Fatalf("MCP tool attrs = %v", tool.Attributes)
				}
				if llm := spanByName(t, calls, "→ mcp__claude-in-chrome__navigate"); llm == nil {
					t.Fatal("the request should name the MCP tool")
				}
			},
		},
		{
			name: "session title request is not a reply",
			script: func(s *ccScript) {
				s.add("user_prompt", map[string]any{"prompt": "review this codebase"}).
					add("api_request", map[string]any{"model": "claude-haiku-5-5", "input_tokens": 40, "output_tokens": 8, "request_id": "req_t",
						"duration_ms": 300, "cost_usd": 0.0002, "query_source": "generate_session_title"}).
					add("assistant_response", map[string]any{"response": `{"title": "Codebase review"}`, "request_id": "req_t", "query_source": "generate_session_title"}).
					add("api_request", map[string]any{"model": "claude-opus-5-5", "input_tokens": 3, "output_tokens": 5, "request_id": "req_1",
						"duration_ms": 900, "cost_usd": 0.001, "query_source": "repl_main_thread"}).
					add("assistant_response", map[string]any{"response": "Done.", "request_id": "req_1", "query_source": "repl_main_thread"})
			},
			check: func(t *testing.T, calls []*store.Call) {
				title := spanByName(t, calls, "session title claude-haiku-5-5")
				if d := store.DeriveSpan(title.OpName, displayOf(title), title.ThreadID, nil, title.ID, title.Attributes, title.Inputs, nil); d.Operation == store.OpChat {
					t.Fatalf("title request counted as a chat step: %+v", d)
				}
				if _, _, _, _, _, cost := usage(title); !near(cost, 0.0002) {
					t.Fatalf("title request cost = %v", cost)
				}
				turn := spanByName(t, calls, "invoke_agent claude-code")
				if b, _ := json.Marshal(turn.Output); strings.Contains(string(b), "Codebase review") || !strings.Contains(string(b), "Done.") {
					t.Fatalf("turn output = %s", b)
				}
				if _, _, _, _, _, cost := usage(turn); !near(cost, 0.0012) {
					t.Fatalf("turn cost = %v, want both requests", cost)
				}
			},
		},
		{
			name: "tool rejected by the user",
			script: func(s *ccScript) {
				s.add("user_prompt", map[string]any{"prompt": "delete the build dir"}).
					add("tool_decision", map[string]any{"decision": "reject", "source": "user_reject", "tool_name": "Bash", "tool_use_id": "toolu_x",
						"request_id": "req_1", "tool_parameters": `{"full_command":"rm -rf build"}`}).
					add("api_request", map[string]any{"model": "claude-opus-5-5", "input_tokens": 3, "output_tokens": 5, "request_id": "req_1", "duration_ms": 1500})
			},
			check: func(t *testing.T, calls []*store.Call) {
				tool := spanByName(t, calls, "Bash: rm -rf build")
				if tool.Exception == nil || *tool.Exception != "Rejected by the user" || attr(tool, "claude_code/decision_source") != "user_reject" ||
					attr(tool, "claude_code/decision") != "reject" || attr(tool, "gen_ai.tool.call.id") != "toolu_x" {
					t.Fatalf("rejected tool: exc %v attrs %v", tool.Exception, tool.Attributes["claude_code"])
				}
				turn := spanByName(t, calls, "invoke_agent claude-code")
				if turn.EndedAt == nil || turn.Exception != nil || runStatus(turn) != "failed_step" {
					t.Fatalf("turn after a rejection: ended %v exc %v status %q", turn.EndedAt, turn.Exception, runStatus(turn))
				}
				if llm := spanByName(t, calls, "→ Bash"); llm == nil {
					t.Fatal("the request that asked for the tool should name it")
				}
			},
		},
		{
			name: "API error ends the turn",
			script: func(s *ccScript) {
				s.add("user_prompt", map[string]any{"prompt": "hi"}).
					add("api_error", map[string]any{"model": "claude-opus-5-5", "error": "Overloaded", "status_code": 529, "attempt": 10,
						"duration_ms": 30000, "client_request_id": "c1"})
			},
			check: func(t *testing.T, calls []*store.Call) {
				llm := spanByName(t, calls, "chat claude-opus-5-5")
				if llm.Exception == nil || *llm.Exception != "API error 529: Overloaded" {
					t.Fatalf("LLM exc = %v", llm.Exception)
				}
				turn := spanByName(t, calls, "invoke_agent claude-code")
				if turn.Exception == nil || *turn.Exception != "API error: Overloaded" || runStatus(turn) != "error" {
					t.Fatalf("turn exc %v status %q", turn.Exception, runStatus(turn))
				}
			},
		},
		{
			name: "a failed tool",
			script: func(s *ccScript) {
				s.add("user_prompt", map[string]any{"prompt": "cat x"}).
					add("tool_decision", map[string]any{"decision": "accept", "source": "config", "tool_name": "Read", "tool_use_id": "toolu_r", "request_id": "req_1"}).
					add("api_request", map[string]any{"model": "claude-opus-5-5", "input_tokens": 3, "output_tokens": 5, "request_id": "req_1", "duration_ms": 500}).
					add("tool_result", map[string]any{"tool_name": "Read", "tool_use_id": "toolu_r", "success": "false", "duration_ms": "3",
						"error_type": "Error:ENOENT", "request_id": "req_1", "tool_input": `{"file_path":"x"}`}).
					add("api_request", map[string]any{"model": "claude-opus-5-5", "input_tokens": 3, "output_tokens": 5, "request_id": "req_2", "duration_ms": 500})
			},
			check: func(t *testing.T, calls []*store.Call) {
				tool := spanByName(t, calls, "Read: x")
				if tool.Exception == nil || *tool.Exception != "Error:ENOENT" {
					t.Fatalf("tool exc = %v", tool.Exception)
				}
				turn := spanByName(t, calls, "invoke_agent claude-code")
				if turn.Exception != nil || runStatus(turn) != "failed_step" {
					t.Fatalf("turn exc %v status %q", turn.Exception, runStatus(turn))
				}
			},
		},
		{
			name: "waiting for a tool keeps the turn open",
			script: func(s *ccScript) {
				s.add("user_prompt", map[string]any{"prompt": "build"}).
					add("tool_decision", map[string]any{"decision": "accept", "source": "user_temporary", "tool_name": "Bash", "tool_use_id": "toolu_b", "request_id": "req_1"}).
					add("api_request", map[string]any{"model": "claude-opus-5-5", "input_tokens": 3, "output_tokens": 5, "request_id": "req_1", "duration_ms": 500})
			},
			check: func(t *testing.T, calls []*store.Call) {
				// The turn is stored open (provisional) so its steps have a parent.
				turn := spanByName(t, calls, "invoke_agent claude-code")
				if turn.EndedAt != nil || attr(turn, "weft/provisional") != true || runStatus(turn) != "running" {
					t.Fatalf("open turn: ended %v provisional %v status %q", turn.EndedAt, attr(turn, "weft/provisional"), runStatus(turn))
				}
				if tool := spanByName(t, calls, "Bash"); tool.EndedAt != nil || runStatus(tool) != "running" {
					t.Fatalf("running tool: ended %v status %q", tool.EndedAt, runStatus(tool))
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newCCHarness(t)
			s := newScript("sess-"+strings.ReplaceAll(tc.name, " ", "-"), "p1")
			tc.script(s)
			h.ingest(s.events)
			tc.check(t, h.spans(s.session))
		})
	}
}

func TestClaudeCodeLogsIgnoresOtherServices(t *testing.T) {
	body := []byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"my-app"}}]},
	 "scopeLogs":[{"scope":{"name":"app"},"logRecords":[{"timeUnixNano":"1700000000000000000","body":{"stringValue":"hello"},
	 "attributes":[{"key":"event.name","value":{"stringValue":"api_request"}},{"key":"session.id","value":{"stringValue":"s"}},
	 {"key":"prompt.id","value":{"stringValue":"p"}}]}]}]}]}`)
	req, err := DecodeLogs(body, true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ClaudeCodeLogEvents(req, "")
	if err != nil || len(b.Events) != 0 || b.Ignored != 1 {
		t.Fatalf("events %v ignored %d err %v", b.Events, b.Ignored, err)
	}
}

func TestClaudeCodeLogsProjectSelection(t *testing.T) {
	req := loadFixture(t, "read_json").batches(t)[2] // holds the user_prompt
	for _, tc := range []struct{ header, resource, want string }{
		{"", "", "default/default"},
		{"", "team/cc", "team/cc"},
		{"hdr/proj", "team/cc", "hdr/proj"},
	} {
		r := req
		if tc.resource != "" {
			r = cloneWithResourceAttr(req, "weft.project", tc.resource)
		}
		got, err := ClaudeCodeLogEvents(r, tc.header)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Events) != 1 || len(got.Events[tc.want]) == 0 {
			t.Fatalf("header %q resource %q: projects %v", tc.header, tc.resource, keys(got.Events))
		}
	}
}

func cloneWithResourceAttr(req *collogs.ExportLogsServiceRequest, key, value string) *collogs.ExportLogsServiceRequest {
	r := proto.Clone(req).(*collogs.ExportLogsServiceRequest)
	for _, rl := range r.GetResourceLogs() {
		rl.Resource.Attributes = append(rl.Resource.Attributes, &common.KeyValue{Key: key,
			Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: value}}})
	}
	return r
}

func keys(m map[string][]store.LogEvent) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A background sub-agent outlives the turn that started it, and its later
// events carry the next prompt's id. It stays under its Agent call in the
// first turn, whichever order the batches arrive in.
func TestClaudeCodeLogsBackgroundSubagent(t *testing.T) {
	const session = "sess-bg"
	t0 := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Millisecond)
	n := 0
	ev := func(prompt, name string, attrs map[string]any) store.LogEvent {
		n++
		a := map[string]any{"session.id": session, "prompt.id": prompt, "event.name": name,
			"event.timestamp": t0.Add(time.Duration(n) * time.Second).Format("2006-01-02T15:04:05.000Z"), "event.sequence": json.Number(itoa(n))}
		for k, v := range attrs {
			a[k] = v
		}
		return ccLogEvent(name, session, prompt, a, map[string]any{"service.name": "claude-code"}, 0, 0)
	}
	sub := map[string]any{"agent_id": "a1", "query_source": "agent:builtin:general-purpose"}
	with := func(m map[string]any, more map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range m {
			out[k] = v
		}
		for k, v := range more {
			out[k] = v
		}
		return out
	}
	first := []store.LogEvent{
		ev("p1", "user_prompt", map[string]any{"prompt": "review in the background"}),
		ev("p1", "tool_decision", map[string]any{"decision": "accept", "tool_name": "Agent", "tool_use_id": "toolu_a", "request_id": "req_1"}),
		ev("p1", "tool_result", map[string]any{"tool_name": "Agent", "tool_use_id": "toolu_a", "success": "true", "duration_ms": "2", "request_id": "req_1",
			"tool_input": `{"prompt":"review","subagent_type":"general-purpose","run_in_background":true}`}),
		ev("p1", "api_request", map[string]any{"model": "claude-opus-5-5", "input_tokens": 3, "output_tokens": 5, "request_id": "req_1", "duration_ms": 900, "query_source": "sdk"}),
		ev("p1", "api_request", with(sub, map[string]any{"model": "claude-opus-5-5", "input_tokens": 3, "output_tokens": 5, "request_id": "req_s1", "duration_ms": 500})),
		ev("p1", "api_request", map[string]any{"model": "claude-opus-5-5", "input_tokens": 3, "output_tokens": 5, "request_id": "req_2", "duration_ms": 500, "query_source": "sdk"}),
	}
	second := []store.LogEvent{
		ev("p2", "user_prompt", map[string]any{"prompt": "and another thing"}),
		ev("p2", "api_request", with(sub, map[string]any{"model": "claude-opus-5-5", "input_tokens": 3, "output_tokens": 5, "request_id": "req_s2", "duration_ms": 500})),
		ev("p2", "subagent_completed", with(sub, map[string]any{"agent_type": "general-purpose", "duration_ms": 5000})),
		ev("p2", "api_request", map[string]any{"model": "claude-opus-5-5", "input_tokens": 3, "output_tokens": 5, "request_id": "req_3", "duration_ms": 500, "query_source": "sdk"}),
	}
	check := func(t *testing.T, h *ccHarness) {
		t.Helper()
		calls := h.spans(session)
		agentCall := spanByName(t, calls, "Agent: review")
		s := spanByName(t, calls, "invoke_agent general-purpose")
		if deref(s.ParentID) != agentCall.ID || deref(s.TurnID) != deref(agentCall.TurnID) || s.EndedAt == nil || s.Exception != nil {
			t.Fatalf("background sub-agent: parent %s (Agent call %s), turn %s vs %s, ended %v, exc %v",
				deref(s.ParentID), agentCall.ID, deref(s.TurnID), deref(agentCall.TurnID), s.EndedAt, s.Exception)
		}
		kids := 0
		for _, c := range calls {
			if deref(c.ParentID) == s.ID {
				kids++
			}
		}
		if kids != 2 {
			t.Fatalf("sub-agent has %d model calls, want 2", kids)
		}
	}
	t.Run("in order", func(t *testing.T) {
		h := newCCHarness(t)
		h.ingest(first)
		// Still running when the turn has ended: not marked as failed.
		calls := h.spans(session)
		if s := spanByName(t, calls, "invoke_agent general-purpose"); s.Exception != nil {
			t.Fatalf("running background sub-agent marked failed: %s", *s.Exception)
		}
		h.ingest(second)
		check(t, h)
	})
	t.Run("later batch first", func(t *testing.T) {
		h := newCCHarness(t)
		h.ingest(second)
		h.ingest(first)
		check(t, h)
	})
}
