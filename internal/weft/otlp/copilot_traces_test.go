package otlp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// The fixtures in testdata/copilot_cli are derived from GitHub's
// documentation of Copilot CLI's OpenTelemetry export, not captured from a
// real run (testdata/copilot_cli/README.md).

const copilotSession = "6f1c2d3e-4a5b-4c6d-8e7f-9a0b1c2d3e4f"

func TestCopilotCLITraces(t *testing.T) {
	cases := []struct {
		name, file string
		content    bool
	}{
		{"content on", "from_docs_content_on.json", true},
		{"content off", "from_docs_content_off.json", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTraceHarness(t)
			reqs := loadExports(t, "copilot_cli")
			var req *coltrace.ExportTraceServiceRequest
			for i, r := range reqs { // files sorted: content_off, content_on
				if (i == 1) == tc.content {
					req = r
				}
			}
			if n := h.ingest(req); n != 11 {
				t.Fatalf("stored %d spans, want 11", n)
			}
			all := h.all()
			for _, c := range all {
				if sval(c.ThreadID) != copilotSession {
					t.Errorf("%s in session %q", displayOf(c), sval(c.ThreadID))
				}
			}

			turns := findCalls(all, func(c *store.Call) bool { return displayOf(c) == "invoke_agent copilot" })
			if len(turns) != 2 {
				t.Fatalf("%d turns", len(turns))
			}
			for _, turn := range turns {
				d := store.DeriveSpan(turn.OpName, displayOf(turn), turn.ThreadID, turn.TurnID, turn.ID, turn.Attributes, turn.Inputs, nil)
				if d.Operation != store.OpInvokeAgent || !d.IsTurn || d.AgentName != "copilot" || turn.ParentID != nil {
					t.Errorf("turn derived as %+v", d)
				}
			}
			first, second := turns[0], turns[1]
			if tc.content {
				if m := msgsOf(t, first.Inputs["messages"]); len(m) != 1 || m[0]["content"] != "List the files here, then show README.md" {
					t.Errorf("turn input = %v", first.Inputs)
				}
			} else if _, ok := first.Inputs[promptNotExportedKey]; !ok {
				t.Errorf("turn input without content = %v", first.Inputs)
			}
			if second.Exception == nil || *second.Exception != "CAPIError: rate_limited (status 429)" {
				t.Errorf("failed turn exception = %v", second.Exception)
			}
			failedChat := findCalls(all, func(c *store.Call) bool { return c.ParentID != nil && *c.ParentID == second.ID })
			if len(failedChat) != 1 || failedChat[0].Exception == nil || *failedChat[0].Exception != "rate_limited" {
				t.Errorf("failed chat = %v", failedChat)
			}

			bashDisplay, viewDisplay := "bash", "view"
			if tc.content {
				bashDisplay, viewDisplay = "bash: ls", "view: README.md"
			}
			bash := byDisplay(t, all, bashDisplay)
			bi := store.DeriveSpan(bash.OpName, displayOf(bash), bash.ThreadID, nil, bash.ID, bash.Attributes, bash.Inputs, nil)
			if bi.Operation != store.OpExecuteTool || bi.ToolName != "bash" || bi.ToolCallID != "call_cp_bash_1" || bash.Exception != nil {
				t.Errorf("bash derived as %+v (%v)", bi, bash.Exception)
			}
			if tc.content && bash.Output != "README.md\nmain.go\n" {
				t.Errorf("bash output = %#v", bash.Output)
			}
			view := byDisplay(t, all, viewDisplay)
			if view.Exception == nil || *view.Exception != "README.md: no such file" {
				t.Errorf("view exception = %v", view.Exception)
			}

			task := byDisplay(t, all, "task")
			sub := byDisplay(t, all, "invoke_agent explore")
			sd := store.DeriveSpan(sub.OpName, displayOf(sub), sub.ThreadID, sub.TurnID, sub.ID, sub.Attributes, sub.Inputs, nil)
			if sub.ParentID == nil || *sub.ParentID != task.ID || sd.IsTurn || sd.AgentName != "explore" || sd.Operation != store.OpInvokeAgent {
				t.Errorf("sub-agent derived as %+v", sd)
			}

			s := h.session(copilotSession)
			if s.TurnCount != 2 || s.LLMCallCount != 5 || s.ToolCallCount != 3 || s.InputTokens != 10600 || s.OutputTokens != 480 ||
				s.Status != "error" || len(s.Agents) != 2 || s.Agents[0] != "copilot" {
				b, _ := json.Marshal(s)
				t.Errorf("session = %s", b)
			}
			var types []string
			for _, it := range h.timeline(copilotSession) {
				types = append(types, it.Type)
			}
			if j := strings.Join(types, ","); strings.Count(j, "turn") != 2 || strings.Count(j, "tool_call") < 2 || !strings.Contains(j, "handoff") {
				t.Errorf("timeline = %v", types)
			}
		})
	}
}

func TestCopilotCLIDetectionAttaches(t *testing.T) {
	h := newTraceHarness(t)
	ctx := context.Background()
	h.ingest(loadExports(t, "copilot_cli")[0]) // content off: the default
	if _, err := h.s.UpsertDetections(ctx, ocProject, []store.DetectionInput{{EventID: "ev-cp-1", SessionID: copilotSession,
		ToolName: "bash", ToolCallID: "call_cp_bash_1", Action: "block", Mode: "enforce", EventSource: "copilot",
		Rules: []store.DetectionRule{{ID: "r1", Title: "shell", Level: "high"}}}}); err != nil {
		t.Fatal(err)
	}
	items, _, err := h.s.QueryDetections(ctx, store.DetectionQuery{ProjectID: ocProject, ToolCallID: "call_cp_bash_1"})
	if err != nil || len(items) != 1 {
		t.Fatalf("detections: %v %d", err, len(items))
	}
	if d := items[0]; d.SpanID == nil || *d.SpanID != "1000000000000003" || d.Link != store.LinkToolCallID {
		t.Errorf("detection span %v link %s", d.SpanID, d.Link)
	}
}

// Copilot is recognised by its instrumentation scope when OTEL_SERVICE_NAME
// renames the service, and an execute_hook span (VS Code's harness) is a step.
func TestCopilotScopeAndHooks(t *testing.T) {
	req := &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: nil,
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{
			{TraceId: []byte("0123456789abcdef"), SpanId: []byte("turn0001"), Name: "invoke_agent",
				StartTimeUnixNano: 1_700_000_000_000_000_000, EndTimeUnixNano: 1_700_000_002_000_000_000,
				Attributes: attrsOf(map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.conversation.id": "c-1",
					"gen_ai.usage.input_tokens": 100, "gen_ai.usage.output_tokens": 5})},
			{TraceId: []byte("0123456789abcdef"), SpanId: []byte("hook0001"), ParentSpanId: []byte("turn0001"), Name: "execute_hook PreToolUse",
				StartTimeUnixNano: 1_700_000_000_100_000_000, EndTimeUnixNano: 1_700_000_000_200_000_000,
				Attributes: attrsOf(map[string]any{"gen_ai.operation.name": "execute_hook", "copilot_chat.hook_type": "PreToolUse",
					"github.copilot.hook.decision": "block"})},
		}}},
	}}}
	req.ResourceSpans[0].ScopeSpans[0].Scope = nil
	// Without the scope it is not Copilot's.
	calls, err := ToCalls(req, "e/p")
	if err != nil || calls[0].Start.TurnID != nil {
		t.Fatalf("unscoped span mapped: %v %v", err, calls[0].Start.TurnID)
	}
	req.ResourceSpans[0].ScopeSpans[0].Scope = scopeNamed("github.copilot")
	calls, err = ToCalls(req, "e/p")
	if err != nil {
		t.Fatal(err)
	}
	turn, hook := calls[0], calls[1]
	if turn.Start.TurnID == nil || sval(turn.Start.DisplayName) != "invoke_agent copilot" || turn.End.Summary["usage"] != nil {
		t.Errorf("turn = %+v %v", turn.Start, turn.End.Summary)
	}
	if sval(hook.Start.DisplayName) != "hook PreToolUse" || sval(hook.Start.ThreadID) != "c-1" || hook.End.Exception != nil {
		t.Errorf("hook = %v %v %v", sval(hook.Start.DisplayName), sval(hook.Start.ThreadID), hook.End.Exception)
	}
}

// The AI SDK's wrapper spans repeat their model calls' usage; only the
// model calls count it. error.type marks a GenAI span failed when the
// status is unset.
func TestVercelWrapperUsageAndErrorType(t *testing.T) {
	usage := map[string]any{"ai.usage.inputTokens": 10, "ai.usage.outputTokens": 2}
	wrapper := oneCall(t, "ai.streamText", mergeAttrs(map[string]any{"ai.operationId": "ai.streamText", "ai.model.id": "m"}, usage))
	if wrapper.End.Summary["usage"] != nil {
		t.Errorf("wrapper usage = %v", wrapper.End.Summary["usage"])
	}
	inner := oneCall(t, "ai.streamText.doStream", mergeAttrs(map[string]any{"ai.operationId": "ai.streamText.doStream", "ai.model.id": "m"}, usage))
	if usageOf(inner)["m"] == nil {
		t.Errorf("doStream usage = %v", inner.End.Summary)
	}
	failed := oneCall(t, "chat m", map[string]any{"gen_ai.operation.name": "chat", "error.type": "timeout"})
	if failed.End.Exception == nil || *failed.End.Exception != "timeout" {
		t.Errorf("error.type exception = %v", failed.End.Exception)
	}
	other := oneCall(t, "GET /x", map[string]any{"error.type": "404"}) // not a GenAI span: status decides
	if other.End.Exception != nil {
		t.Errorf("non-GenAI error.type = %v", *other.End.Exception)
	}
}

func scopeNamed(name string) *common.InstrumentationScope {
	return &common.InstrumentationScope{Name: name}
}

func mergeAttrs(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
