package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestEvalLinksOf(t *testing.T) {
	run := "run-1"
	pasParent := func(string) string { return "run-from-pas" }
	cases := []struct {
		name  string
		op    string
		attrs map[string]any
		want  []evalLink
	}{
		{"nested weft.eval (Python SDK)", "weft:///e/p/op/turn:abc", map[string]any{"weft": map[string]any{"eval": map[string]any{"run_id": run, "predict_and_score_call_id": "pas-1"}}},
			[]evalLink{{kind: evalLinkAttr, target: "span", runID: run, pasID: "pas-1"}}},
		{"flat OTel attributes", "invoke_agent bot", map[string]any{"otel": map[string]any{"attributes": map[string]any{"weft.eval.run_id": run, "weft.eval.predict_and_score_call_id": "pas-2"}}},
			[]evalLink{{kind: evalLinkAttr, target: "span", runID: run, pasID: "pas-2"}}},
		{"prediction only resolves its run", "x", map[string]any{"weft.eval.predict_and_score_call_id": "pas-3"},
			[]evalLink{{kind: evalLinkAttr, target: "span", runID: "run-from-pas", pasID: "pas-3"}}},
		{"plain call", "x", map[string]any{"weft": map[string]any{"span_kind": "agent"}}, nil},
		{"prediction links", "weft:///e/p/op/Evaluation.predict_and_score:abc", map[string]any{"weft": map[string]any{
			"linked_calls":            []any{map[string]any{"call_id": "c1", "trace_id": "t1"}, "weft:///e/p/call/c2", "c3"},
			"genai_span_ref":          []any{map[string]any{"trace_id": "t1", "span_id": "c1"}, map[string]any{"trace_id": "t4", "span_id": "c4"}},
			"linked_conversation_ids": []any{"conv-1"},
		}}, []evalLink{
			{kind: evalLinkSpan, target: "c1", runID: "parent-run", pasID: "span", traceID: "t1"},
			{kind: evalLinkSpan, target: "c2", runID: "parent-run", pasID: "span"},
			{kind: evalLinkSpan, target: "c3", runID: "parent-run", pasID: "span"},
			{kind: evalLinkSpan, target: "c4", runID: "parent-run", pasID: "span", traceID: "t4"},
			{kind: evalLinkConversation, target: "conv-1", runID: "parent-run", pasID: "span"},
		}},
		{"v2 prediction names its run", "Evaluation.predict_and_score", map[string]any{"weft": map[string]any{"evaluation_run_id": "v2-run", "genai_span_ref": []any{map[string]any{"span_id": "c9"}}}},
			[]evalLink{{kind: evalLinkSpan, target: "c9", runID: "v2-run", pasID: "span"}}},
		{"links on a non-prediction call are ignored", "Model.predict", map[string]any{"weft": map[string]any{"linked_calls": []any{"c1"}}}, nil},
	}
	parent := "parent-run"
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := evalLinksOf(c.op, "span", &parent, c.attrs, pasParent)
			if len(got) != len(c.want) {
				t.Fatalf("links = %+v, want %+v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("link %d = %+v, want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestEvalLinksIndexAndBackfill(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p := "e/p"
	parent := "run"
	start := func(id, op string, parentID *string, attrs map[string]any) {
		t.Helper()
		if err := st.StartCall(ctx, CallStart{ProjectID: p, ID: id, TraceID: "tr-" + id, ParentID: parentID, OpName: op,
			StartedAt: "2026-01-01T00:00:00Z", Attributes: attrs, Inputs: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
	}
	start("run", "Evaluation.evaluate", nil, nil)
	start("pas", "Evaluation.predict_and_score", &parent, map[string]any{"weft": map[string]any{"linked_calls": []any{"turn"}}})
	start("turn", "agent_turn", nil, map[string]any{"weft": map[string]any{"eval": map[string]any{"run_id": "run", "predict_and_score_call_id": "pas"}}})
	if n, err := st.evalLinkCount(ctx, p); err != nil || n != 2 {
		t.Fatalf("indexed links = %d, %v", n, err)
	}
	m, err := st.evalMembership(ctx, p, []string{"run"}, nil)
	if err != nil || len(m["turn"]) != 1 || m["turn"][0] != (evalRef{"run", "pas"}) {
		t.Fatalf("membership = %v, %v", m, err)
	}
	// Drop the index and its marker: the backfill rebuilds it once.
	if _, err := st.w.Exec(`DELETE FROM agent_eval_links`); err != nil {
		t.Fatal(err)
	}
	if err := backfillEvalLinks(st.w); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.evalLinkCount(ctx, p); n != 2 {
		t.Fatalf("backfilled links = %d", n)
	}
	if _, err := st.w.Exec(`DELETE FROM agent_eval_links WHERE project_id != ''`); err != nil {
		t.Fatal(err)
	}
	if err := backfillEvalLinks(st.w); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.evalLinkCount(ctx, p); n != 0 {
		t.Fatalf("backfill ran twice: %d links", n)
	}
}
