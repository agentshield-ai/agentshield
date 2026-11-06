package store

import (
	"context"
	"math"
	"testing"
	"time"
)

// ccSpans is a turn with one tool call, as one Claude Code source sends it.
func ccSpans(source, session, prefix, toolCallID string, at time.Time) []CompleteCall {
	pid := "acme/cc"
	s := session
	turn := prefix + "-turn"
	attrs := func(op string, extra map[string]any) map[string]any {
		m := map[string]any{"gen_ai.operation.name": op, "weft": map[string]any{"source": source}}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	end := func(d time.Duration, usage map[string]any) *CallEnd {
		return &CallEnd{EndedAt: at.Add(d).Format(time.RFC3339Nano), Summary: usage}
	}
	tid := turn
	return []CompleteCall{
		{Start: CallStart{ProjectID: pid, ID: turn, TraceID: turn, OpName: "turn", ThreadID: &s, TurnID: &tid, StartedAt: at.Format(time.RFC3339Nano),
			Attributes: attrs("invoke_agent", map[string]any{"gen_ai.agent.name": "claude-code"})}, End: end(3*time.Second, nil)},
		{Start: CallStart{ProjectID: pid, ID: prefix + "-tool", TraceID: turn, ParentID: &tid, OpName: "tool", ThreadID: &s, StartedAt: at.Add(time.Second).Format(time.RFC3339Nano),
			Attributes: attrs("execute_tool", map[string]any{"gen_ai.tool.name": "Bash", "gen_ai.tool.call.id": toolCallID})}, End: end(2*time.Second, nil)},
		{Start: CallStart{ProjectID: pid, ID: prefix + "-llm", TraceID: turn, ParentID: &tid, OpName: "llm", ThreadID: &s, StartedAt: at.Format(time.RFC3339Nano),
			Attributes: attrs("chat", nil)}, End: end(time.Second, map[string]any{"usage": map[string]any{"m": map[string]any{"requests": 1, "prompt_tokens": 10, "completion_tokens": 5}}})},
	}
}

func liveIDs(t *testing.T, s *Store, session string) map[string]bool {
	t.Helper()
	calls, err := s.ListCalls(context.Background(), CallsQuery{ProjectID: "acme/cc", Filter: &CallsFilter{ThreadIDs: []string{session}}})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, c := range calls {
		out[c.ID] = true
	}
	return out
}

func TestSessionSourcePrecedence(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	at := time.Now().Add(-time.Minute).UTC()
	put := func(calls []CompleteCall) {
		t.Helper()
		if _, err := s.UpsertComplete(ctx, calls); err != nil {
			t.Fatal(err)
		}
	}
	want := func(step, prefix string) {
		t.Helper()
		got := liveIDs(t, s, "S")
		if len(got) != 3 || !got[prefix+"-turn"] || !got[prefix+"-tool"] || !got[prefix+"-llm"] {
			t.Fatalf("%s: live spans %v, want only %s's", step, got, prefix)
		}
	}
	det := func() *Detection {
		t.Helper()
		ds, _, err := s.QueryDetections(ctx, DetectionQuery{ProjectID: "acme/cc"})
		if err != nil || len(ds) != 1 {
			t.Fatalf("detections %v %v", ds, err)
		}
		return &ds[0]
	}

	// Log events first; a verdict links to their tool span by tool-use id.
	put(ccSpans(SourceClaudeCodeLogs, "S", "logs", "toolu_1", at))
	want("logs", "logs")
	if _, err := s.UpsertDetections(ctx, "acme/cc", []DetectionInput{{EventID: "e1", SessionID: "S", ToolCallID: "toolu_1", ToolName: "Bash",
		Action: DetectionBlock, CreatedAt: at.Add(time.Second).UnixMicro()}}); err != nil {
		t.Fatal(err)
	}
	if d := det(); deref(d.SpanID) != "logs-tool" || d.Link != LinkToolCallID {
		t.Fatalf("detection on logs: span %v link %s", d.SpanID, d.Link)
	}
	// Traces win over logs: the logs spans go, the verdict moves.
	put(ccSpans(SourceClaudeCodeTraces, "S", "traces", "toolu_1", at))
	want("traces replace logs", "traces")
	if d := det(); deref(d.SpanID) != "traces-tool" || d.Link != LinkToolCallID {
		t.Fatalf("detection after traces: span %v link %s", d.SpanID, d.Link)
	}
	// Logs re-sent later are dropped, with their ends (no stub rows).
	put(ccSpans(SourceClaudeCodeLogs, "S", "logs", "toolu_1", at))
	want("logs after traces", "traces")
	var n int
	if err := s.r.QueryRow(`SELECT COUNT(*) FROM calls WHERE project_id = 'acme/cc' AND id LIKE 'logs-%' AND deleted_at IS NULL`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("live logs rows after a drop: %d %v", n, err)
	}
	// The plugin wins over both.
	put(ccSpans(SourceClaudeCodePlugin, "S", "plugin", "toolu_1", at))
	want("plugin replaces traces", "plugin")
	if d := det(); deref(d.SpanID) != "plugin-tool" {
		t.Fatalf("detection after plugin: span %v", d.SpanID)
	}
	put(ccSpans(SourceClaudeCodeTraces, "S", "traces2", "toolu_1", at))
	put(ccSpans(SourceClaudeCodeLogs, "S", "logs2", "toolu_1", at))
	want("lower sources after the plugin", "plugin")

	// Another session, and spans with no ranked source, are not touched.
	put(ccSpans(SourceClaudeCodeLogs, "T", "t-logs", "toolu_2", at))
	put(ccSpans("my-sdk", "T", "t-sdk", "toolu_3", at))
	if got := liveIDs(t, s, "T"); len(got) != 6 {
		t.Fatalf("session T: %v", got)
	}

	// Batches through UpsertBatch (the plugin's /api/v1/spans/batch).
	var items []BatchItem
	for _, c := range ccSpans(SourceClaudeCodePlugin, "T", "t-plugin", "toolu_2", at) {
		st, en := c.Start, *c.End
		en.ProjectID, en.ID = st.ProjectID, st.ID
		items = append(items, BatchItem{Mode: "start", Start: &st}, BatchItem{Mode: "end", End: &en})
	}
	if _, err := s.UpsertBatch(ctx, items); err != nil {
		t.Fatal(err)
	}
	got := liveIDs(t, s, "T")
	if len(got) != 6 || got["t-logs-turn"] || !got["t-sdk-turn"] || !got["t-plugin-turn"] {
		t.Fatalf("session T after the plugin: %v", got)
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// A span sent again under another parent moves its usage with it.
func TestReparentMovesRollup(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	at := time.Now().UTC()
	ts := func(d time.Duration) string { return at.Add(d).Format(time.RFC3339Nano) }
	p := func(s string) *string { return &s }
	call := func(id string, parent *string, usage bool) CompleteCall {
		en := &CallEnd{EndedAt: ts(time.Second), Summary: map[string]any{}}
		if usage {
			en.Summary["usage"] = map[string]any{"m": map[string]any{"requests": 1, "prompt_tokens": 100, "completion_tokens": 10}}
		}
		return CompleteCall{Start: CallStart{ProjectID: "acme/cc", ID: id, TraceID: "R", ParentID: parent, OpName: id, StartedAt: ts(0)}, End: en}
	}
	if _, err := s.UpsertComplete(ctx, []CompleteCall{call("R", nil, false), call("A", p("R"), false), call("B", p("R"), false), call("C", p("A"), true)}); err != nil {
		t.Fatal(err)
	}
	tokens := func(id string) float64 {
		c, err := s.GetCall(ctx, "acme/cc", id, false, false, false)
		if err != nil {
			t.Fatal(err)
		}
		all, _ := c.Summary["usage"].(map[string]any)
		u, _ := all["m"].(map[string]any)
		f, _ := ToFloat(u["prompt_tokens"])
		return f
	}
	if tokens("A") != 100 || tokens("R") != 100 {
		t.Fatalf("before: A %v R %v", tokens("A"), tokens("R"))
	}
	if _, err := s.UpsertComplete(ctx, []CompleteCall{call("C", p("B"), true)}); err != nil {
		t.Fatal(err)
	}
	if tokens("A") != 0 || tokens("B") != 100 || tokens("R") != 100 {
		t.Fatalf("after: A %v B %v R %v", tokens("A"), tokens("B"), tokens("R"))
	}
}

func TestReportedCost(t *testing.T) {
	pb := &PriceBook{project: map[string][]Price{}}
	now := time.Now().UnixMicro()
	// Claude Code's own figures for one request (1-hour cache writes cost
	// more than the 5-minute price Weft lists).
	m := map[string]float64{"requests": 1, "prompt_tokens": 2 + 26220 + 21113, "completion_tokens": 110,
		"cache_read_input_tokens": 26220, "cache_creation_input_tokens": 21113, UsageReportedCost: 0.176356, UsageReportedRequests: 1}
	c, p, ok := pb.ModelCost("claude-opus-5-5", m, now)
	if !ok || p.PricingLevel != PricingReported || math.Abs(c.Total()-0.176356) > 1e-12 || c.CacheCreation <= 0 || c.Completion <= 0 {
		t.Fatalf("reported: %+v %v %v", c, p.PricingLevel, ok)
	}
	// A model Weft has no price for still costs what was reported.
	if c, _, ok := pb.ModelCost("no-such-model", m, now); !ok || math.Abs(c.Total()-0.176356) > 1e-12 {
		t.Fatalf("unpriced model: %+v %v", c, ok)
	}
	// A roll-up mixing reported and unreported requests is priced from tokens.
	mixed := map[string]float64{"requests": 2, "prompt_tokens": 1000, "completion_tokens": 10, UsageReportedCost: 1, UsageReportedRequests: 1}
	if _, p, _ := pb.ModelCost("claude-opus-5-5", mixed, now); p.PricingLevel != "default" {
		t.Fatalf("mixed entry priced at %q", p.PricingLevel)
	}
	// A price the project set wins.
	pb.project["claude-opus-5-5"] = []Price{{LLMID: "claude-opus-5-5", PromptTokenCost: 1e-6, CompletionTokenCost: 1e-6, PricingLevel: "project"}}
	if c, p, _ := pb.ModelCost("claude-opus-5-5", m, now); p.PricingLevel != "project" || math.Abs(c.Total()-(47335+110)*1e-6) > 1e-9 {
		t.Fatalf("project price: %+v %v", c, p.PricingLevel)
	}
}
