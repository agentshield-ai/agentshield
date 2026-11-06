package otlp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
)

// The fixtures in testdata/opencode are real OpenCode (opencode-ai 1.19.5)
// trace exports, one file per export request as OpenCode batched them,
// captured with "experimental": {"openTelemetry": true} against a local
// OpenAI-compatible test double, then sanitised: paths and the loopback
// host replaced, OpenCode's system prompt and tool descriptions shortened,
// HTTP headers dropped, and most of OpenCode's internal spans (database,
// file system, plugins) left out except a sample.

const ocProject = "e/opencode"

// traceHarness stores OTLP trace exports in a temporary Weft store.
type traceHarness struct {
	t *testing.T
	s *store.Store
}

func newTraceHarness(t *testing.T) *traceHarness {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "weft.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return &traceHarness{t: t, s: s}
}

// loadExports reads a fixture directory's export requests in file order.
func loadExports(t *testing.T, dir string) []*coltrace.ExportTraceServiceRequest {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", dir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixture in %s: %v", dir, err)
	}
	sort.Strings(files)
	var out []*coltrace.ExportTraceServiceRequest
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		req, err := Decode(b, true)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		out = append(out, req)
	}
	return out
}

func (h *traceHarness) ingest(reqs ...*coltrace.ExportTraceServiceRequest) int {
	h.t.Helper()
	n := 0
	for _, req := range reqs {
		calls, err := ToCalls(req, ocProject)
		if err != nil {
			h.t.Fatal(err)
		}
		if _, err := h.s.UpsertComplete(context.Background(), calls); err != nil {
			h.t.Fatal(err)
		}
		n += len(calls)
	}
	return n
}

func (h *traceHarness) all() []*store.Call {
	h.t.Helper()
	calls, err := h.s.ListCalls(context.Background(), store.CallsQuery{ProjectID: ocProject})
	if err != nil {
		h.t.Fatal(err)
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].StartedAt < calls[j].StartedAt })
	return calls
}

func (h *traceHarness) session(id string) *store.SessionSummary {
	h.t.Helper()
	s, err := h.s.GetAgentSession(context.Background(), ocProject, id)
	if err != nil {
		h.t.Fatalf("session %s: %v", id, err)
	}
	return s
}

func (h *traceHarness) timeline(id string) []*store.TimelineItem {
	h.t.Helper()
	items, _, err := h.s.SessionTimeline(context.Background(), store.SessionTimelineQuery{ProjectID: ocProject, SessionID: id, Limit: 500})
	if err != nil {
		h.t.Fatal(err)
	}
	return items
}

func sval(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func displayOf(c *store.Call) string {
	if c.DisplayName != nil {
		return *c.DisplayName
	}
	return c.OpName
}

func findCalls(calls []*store.Call, pred func(*store.Call) bool) []*store.Call {
	var out []*store.Call
	for _, c := range calls {
		if pred(c) {
			out = append(out, c)
		}
	}
	return out
}

func byDisplay(t *testing.T, calls []*store.Call, prefix string) *store.Call {
	t.Helper()
	found := findCalls(calls, func(c *store.Call) bool { return strings.HasPrefix(displayOf(c), prefix) })
	if len(found) != 1 {
		var names []string
		for _, c := range calls {
			names = append(names, displayOf(c))
		}
		t.Fatalf("%d spans named %q in %v", len(found), prefix, names)
	}
	return found[0]
}

// stripAISDKContent removes what the AI SDK records only with
// recordInputs / recordOutputs on (OpenCode does not turn them off; this is
// what an export without content looks like).
func stripAISDKContent(reqs []*coltrace.ExportTraceServiceRequest) {
	drop := map[string]bool{"ai.prompt": true, "ai.prompt.messages": true, "ai.prompt.tools": true, "ai.prompt.toolChoice": true,
		"ai.response.text": true, "ai.response.toolCalls": true, "ai.response.reasoning": true, "ai.toolCall.args": true, "ai.toolCall.result": true}
	for _, req := range reqs {
		for _, rs := range req.GetResourceSpans() {
			for _, ss := range rs.GetScopeSpans() {
				for _, sp := range ss.GetSpans() {
					kept := sp.Attributes[:0]
					for _, kv := range sp.GetAttributes() {
						if !drop[kv.GetKey()] {
							kept = append(kept, kv)
						}
					}
					sp.Attributes = kept
				}
			}
		}
	}
}

func TestOpenCodeTraces(t *testing.T) {
	type tool struct {
		display, name, callID, failure string
	}
	cases := []struct {
		name, dir, session, prompt, answer string
		content                            bool
		reverse                            bool // ingest the exports in reverse order
		llmCalls                           int
		inTokens, outTokens                int64
		tool                               tool
		failedStep                         bool
	}{
		{name: "bash, content on", dir: "opencode/bash", session: "ses_e4fbb5f1fffemM4NV5xlsfOW0t", content: true,
			prompt: `"Run echo hi with the bash tool and tell me what it printed"`, answer: "The command printed: hi",
			llmCalls: 2, inTokens: 3600, outTokens: 72,
			tool: tool{display: "bash: echo hi", name: "bash", callID: "call_mock_0001"}},
		{name: "bash, content off", dir: "opencode/bash", session: "ses_e4fbb5f1fffemM4NV5xlsfOW0t",
			llmCalls: 2, inTokens: 3600, outTokens: 72,
			tool: tool{display: "bash", name: "bash", callID: "call_mock_0001"}},
		{name: "bash, exports out of order", dir: "opencode/bash", session: "ses_e4fbb5f1fffemM4NV5xlsfOW0t", content: true, reverse: true,
			prompt: `"Run echo hi with the bash tool and tell me what it printed"`, answer: "The command printed: hi",
			llmCalls: 2, inTokens: 3600, outTokens: 72,
			tool: tool{display: "bash: echo hi", name: "bash", callID: "call_mock_0001"}},
		{name: "failed read", dir: "opencode/read_missing_file", session: "ses_e4fb5948effeP6dsGx6xzPQbsv", content: true,
			prompt: `"Read missing.txt and tell me what it says"`, llmCalls: 2, inTokens: 3600, outTokens: 72, failedStep: true,
			tool: tool{display: "read: /home/dev/project/missing.txt", name: "read", callID: "call_mock_0002", failure: "File not found: /home/dev/project/missing.txt"}},
		{name: "failed read, content off", dir: "opencode/read_missing_file", session: "ses_e4fb5948effeP6dsGx6xzPQbsv",
			llmCalls: 2, inTokens: 3600, outTokens: 72, failedStep: true,
			tool: tool{display: "read", name: "read", callID: "call_mock_0002", failure: "File not found"}},
		{name: "permission rejected", dir: "opencode/permission_rejected", session: "ses_e4fb641d2ffekABZ4Um8ptsDBw", content: true,
			prompt:   `"Read the file /definitely/not/here.txt and tell me what it says"`,
			llmCalls: 1, inTokens: 2400, outTokens: 48, failedStep: true,
			tool: tool{display: "read: /definitely/not/here.txt", name: "read", callID: "call_mock_0002", failure: "The user rejected permission"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTraceHarness(t)
			reqs := loadExports(t, tc.dir)
			if !tc.content {
				stripAISDKContent(reqs)
			}
			if tc.reverse {
				for i, j := 0, len(reqs)-1; i < j; i, j = i+1, j-1 {
					reqs[i], reqs[j] = reqs[j], reqs[i]
				}
			}
			h.ingest(reqs...)
			all := h.all()

			// Only the turn and the AI SDK spans are kept, and every
			// parent they name is stored (none dangles).
			ids := map[string]bool{}
			for _, c := range all {
				ids[c.ID] = true
				if !strings.HasPrefix(c.OpName, "ai.") && c.OpName != ocTurnSpan {
					t.Errorf("OpenCode internal span kept: %s", c.OpName)
				}
			}
			for _, c := range all {
				if c.ParentID != nil && !ids[*c.ParentID] {
					t.Errorf("%s: parent %s not stored", displayOf(c), *c.ParentID)
				}
			}

			turn := byDisplay(t, all, "invoke_agent opencode")
			if turn.ParentID != nil || turn.TurnID == nil || *turn.TurnID != turn.ID || sval(turn.ThreadID) != tc.session {
				t.Fatalf("turn = parent %v turn %v thread %v", turn.ParentID, turn.TurnID, sval(turn.ThreadID))
			}
			if turn.ID != ocID("turn", turn.TraceID, tc.session) {
				t.Errorf("turn id %s is not derived from its trace and session", turn.ID)
			}
			info := store.DeriveSpan(turn.OpName, displayOf(turn), turn.ThreadID, turn.TurnID, turn.ID, turn.Attributes, turn.Inputs, nil)
			if info.Operation != store.OpInvokeAgent || info.AgentName != "opencode" || !info.IsTurn {
				t.Errorf("turn derived as %+v", info)
			}
			if tc.prompt != "" {
				if m := msgsOf(t, turn.Inputs["messages"]); m[0]["content"] != tc.prompt {
					t.Errorf("turn input = %v", turn.Inputs)
				}
			} else if _, ok := turn.Inputs[promptNotExportedKey]; !ok {
				t.Errorf("turn input without content = %v", turn.Inputs)
			}
			if tc.answer != "" {
				if out, _ := turn.Output.(map[string]any); out == nil || msgsOf(t, out["messages"])[0]["content"] != tc.answer {
					t.Errorf("turn output = %v", turn.Output)
				}
			}

			// Model calls: chat <model> under an ai.streamText under the turn.
			chats := findCalls(all, func(c *store.Call) bool { return displayOf(c) == "chat mock-model" })
			if len(chats) != tc.llmCalls {
				t.Errorf("%d chat spans, want %d", len(chats), tc.llmCalls)
			}
			for _, c := range append(chats, findCalls(all, func(c *store.Call) bool { return c.OpName == "ai.streamText" })...) {
				if sval(c.ThreadID) != tc.session {
					t.Errorf("%s in session %q", displayOf(c), sval(c.ThreadID))
				}
			}
			for _, c := range findCalls(all, func(c *store.Call) bool { return c.OpName == "ai.streamText" }) {
				if c.ParentID == nil || *c.ParentID != turn.ID {
					t.Errorf("ai.streamText %s not under the turn", c.ID)
				}
			}
			// The session title request is kept but is not a conversation step.
			title := byDisplay(t, all, "session title mock-model")
			if d := store.DeriveSpan(title.OpName, displayOf(title), title.ThreadID, nil, title.ID, title.Attributes, title.Inputs, nil); d.Operation == store.OpChat {
				t.Errorf("title request counted as a chat step")
			}

			// The tool call.
			tl := byDisplay(t, all, tc.tool.display)
			if tl.ID != ocID("tool", tl.TraceID, tc.tool.callID) {
				t.Errorf("tool span id %s is not derived from its call id", tl.ID)
			}
			ti := store.DeriveSpan(tl.OpName, displayOf(tl), tl.ThreadID, nil, tl.ID, tl.Attributes, tl.Inputs, nil)
			if ti.Operation != store.OpExecuteTool || ti.ToolName != tc.tool.name || ti.ToolCallID != tc.tool.callID {
				t.Errorf("tool derived as %+v", ti)
			}
			if tc.tool.failure != "" {
				if tl.Exception == nil || !strings.Contains(*tl.Exception, tc.tool.failure) {
					t.Errorf("tool exception = %v", tl.Exception)
				}
			} else if tl.Exception != nil {
				t.Errorf("tool failed: %s", *tl.Exception)
			}
			if tc.content && tc.tool.failure == "" && tl.Output != "hi\n" {
				t.Errorf("tool output = %#v", tl.Output)
			}

			// The session as the UI shows it.
			s := h.session(tc.session)
			if len(s.Agents) != 1 || s.Agents[0] != "opencode" || s.TurnCount != 1 || s.LLMCallCount != tc.llmCalls || s.ToolCallCount != 1 ||
				s.InputTokens != tc.inTokens || s.OutputTokens != tc.outTokens || s.Status != "ok" || s.FailedStep != tc.failedStep {
				b, _ := json.Marshal(s)
				t.Errorf("session = %s", b)
			}
			var types []string
			for _, it := range h.timeline(tc.session) {
				if it.AgentName != "opencode" {
					t.Errorf("timeline %s step by %q", it.Type, it.AgentName)
				}
				types = append(types, it.Type)
			}
			if !strings.HasPrefix(strings.Join(types, ","), "turn,") || !strings.Contains(strings.Join(types, ","), "tool_call") {
				t.Errorf("timeline = %v", types)
			}
		})
	}
}

func TestOpenCodeTaskSubagent(t *testing.T) {
	const parent, child = "ses_e4fb5fc4dffeZiXziU6vnouYv7", "ses_e4fb5f75affeetCr9uDnAh0Sc3"
	h := newTraceHarness(t)
	h.ingest(loadExports(t, "opencode/task_subagent")...)
	all := h.all()

	task := byDisplay(t, all, "task: Say hi")
	if sval(task.ThreadID) != parent || task.Attributes["gen_ai.tool.call.id"] != "call_mock_0002" {
		t.Fatalf("task call = %s %v", sval(task.ThreadID), task.Attributes["gen_ai.tool.call.id"])
	}
	sub := byDisplay(t, all, "invoke_agent general")
	if sub.ParentID == nil || *sub.ParentID != task.ID || sval(sub.ThreadID) != child || sub.TurnID == nil || *sub.TurnID != sub.ID {
		t.Fatalf("sub-agent turn = parent %v thread %s", sub.ParentID, sval(sub.ThreadID))
	}
	if m := msgsOf(t, sub.Inputs["messages"]); m[0]["content"] != "Run echo hi with bash and report the output" {
		t.Errorf("sub-agent input = %v", sub.Inputs)
	}
	bash := byDisplay(t, all, "bash: echo hi")
	if sval(bash.ThreadID) != child {
		t.Errorf("sub-agent's bash call in %s", sval(bash.ThreadID))
	}

	p := h.session(parent)
	if p.TurnCount != 1 || p.ToolCallCount != 1 || p.Agents[0] != "opencode" {
		t.Errorf("parent session = %+v", p)
	}
	c := h.session(child)
	if c.TurnCount != 1 || c.ToolCallCount != 1 || c.LLMCallCount != 2 || c.HandoffCount != 1 {
		t.Errorf("child session = %+v", c)
	}
	found := false
	for _, a := range c.Agents {
		found = found || a == "general"
	}
	if !found {
		t.Errorf("child session agents = %v", c.Agents)
	}
}

// An AgentShield detection for an OpenCode tool call attaches to its span
// by the tool-call id, whichever arrives first.
func TestOpenCodeDetectionAttaches(t *testing.T) {
	for _, detFirst := range []bool{false, true} {
		h := newTraceHarness(t)
		ctx := context.Background()
		det := store.DetectionInput{EventID: "ev-oc-1", SessionID: "ses_e4fbb5f1fffemM4NV5xlsfOW0t", ToolName: "bash",
			ToolCallID: "call_mock_0001", Action: "require_approval", Mode: "enforce", EventSource: "opencode",
			Rules: []store.DetectionRule{{ID: "r1", Title: "shell", Level: "medium"}}}
		if detFirst {
			if _, err := h.s.UpsertDetections(ctx, ocProject, []store.DetectionInput{det}); err != nil {
				t.Fatal(err)
			}
		}
		h.ingest(loadExports(t, "opencode/bash")...)
		if !detFirst {
			if _, err := h.s.UpsertDetections(ctx, ocProject, []store.DetectionInput{det}); err != nil {
				t.Fatal(err)
			}
		}
		items, _, err := h.s.QueryDetections(ctx, store.DetectionQuery{ProjectID: ocProject, ToolCallID: "call_mock_0001"})
		if err != nil || len(items) != 1 {
			t.Fatalf("detections: %v %d", err, len(items))
		}
		tool := byDisplay(t, h.all(), "bash: echo hi")
		if d := items[0]; d.SpanID == nil || *d.SpanID != tool.ID || d.Link != store.LinkToolCallID {
			t.Errorf("detection first=%v: span %v link %s, want %s", detFirst, d.SpanID, d.Link, tool.ID)
		}
	}
}

func TestOpenCodeOnlyForOpenCode(t *testing.T) {
	// The same spans from another service are not filtered or rewritten.
	reqs := loadExports(t, "opencode/bash")
	n := 0
	for _, req := range reqs {
		for _, rs := range req.GetResourceSpans() {
			for _, kv := range rs.GetResource().GetAttributes() {
				if kv.GetKey() == "service.name" {
					kv.GetValue().Value = &common.AnyValue_StringValue{StringValue: "something-else"}
				}
			}
			for _, ss := range rs.GetScopeSpans() {
				n += len(ss.GetSpans())
			}
		}
	}
	got := 0
	for _, req := range reqs {
		calls, err := ToCalls(req, ocProject)
		if err != nil {
			t.Fatal(err)
		}
		got += len(calls)
		for _, c := range calls {
			if c.Start.DisplayName != nil && strings.HasPrefix(*c.Start.DisplayName, "invoke_agent") {
				t.Errorf("non-OpenCode span mapped: %s", *c.Start.DisplayName)
			}
		}
	}
	if got != n {
		t.Errorf("kept %d of %d spans", got, n)
	}
}
