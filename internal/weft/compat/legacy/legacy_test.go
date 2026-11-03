package legacy_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/compat/legacy"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

func TestNames(t *testing.T) {
	for in, want := range map[string]string{
		"wandb.reaction.1": "weft.reaction", "wandb.note.1": "weft.note", "wandb.runnable.judge": "weft.scorer.judge",
		"wandb.annotation.stars": "weft.annotation.stars", "wandb.agent_monitor": "weft.signal",
		"wandb.agent_user_feedback": "weft.user_feedback", "custom": "custom", "weft.note": "weft.note",
	} {
		if got := legacy.FeedbackType(in); got != want {
			t.Errorf("FeedbackType(%q) = %q, want %q", in, got, want)
		}
		if in != want && legacy.FeedbackTypeOut(want) != in {
			t.Errorf("FeedbackTypeOut(%q) = %q, want %q", want, legacy.FeedbackTypeOut(want), in)
		}
	}
	for in, want := range map[string]string{
		"wandb.reaction.*": "weft.reaction", "wandb.runnable.*": "weft.scorer.*", "wandb.annotation.*": "weft.annotation.*",
		"wandb.*": "weft.*", "wandb.note.1": "weft.note", "mine.*": "mine.*",
	} {
		if got := legacy.FeedbackTypePattern(in); got != want {
			t.Errorf("FeedbackTypePattern(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"summary.weave.status":                       "summary.weft.status",
		"feedback.wandb.reaction.1.emoji":            "feedback.weft.reaction.emoji",
		"feedback.wandb.runnable.judge.output.score": "feedback.weft.scorer.judge.output.score",
		"feedback.wandb.note.1":                      "feedback.weft.note",
		"output.x":                                   "output.x",
	} {
		if got := legacy.QueryField(in); got != want {
			t.Errorf("QueryField(%q) = %q, want %q", in, got, want)
		}
	}
	attrs := map[string]any{
		"weave.eval.run_id": "r", "wandb.thread_id": "th", "weft.thread_id": "mine",
		"weave": map[string]any{"kind": "llm", "client_version": "1", "eval": map[string]any{"run_id": "r2"}},
		"otel":  map[string]any{"attributes": map[string]any{"wandb.is_turn": true}},
	}
	if !legacy.RewriteAttributes(attrs) {
		t.Fatal("nothing rewritten")
	}
	w := attrs["weft"].(map[string]any)
	if attrs["weft.eval.run_id"] != "r" || attrs["weft.thread_id"] != "mine" || attrs["wandb.thread_id"] != nil ||
		w["span_kind"] != "llm" || w["eval"].(map[string]any)["run_id"] != "r2" ||
		attrs["weave"].(map[string]any)["client_version"] != "1" || attrs["weave"].(map[string]any)["kind"] != nil ||
		attrs["otel"].(map[string]any)["attributes"].(map[string]any)["weft.is_turn"] != true {
		t.Fatalf("attributes = %v", attrs)
	}
}

// A database written before the rename is migrated once at Open: the
// feedback ref column, feedback types and legacy attribute keys of calls.
func TestStoreMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "weft.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.StartCall(ctx, store.CallStart{ProjectID: "e/p", ID: "c1", TraceID: "t1", OpName: "op",
		StartedAt: "2026-01-01T00:00:00Z", Inputs: map[string]any{}, Attributes: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	// Turn the database back into the old shape.
	db := st.DB()
	for _, q := range []string{
		`ALTER TABLE feedback RENAME COLUMN ref TO ` + legacy.FeedbackRefColumn,
		`DELETE FROM weft_migrations`,
		`INSERT INTO feedback(id, project_id, ` + legacy.FeedbackRefColumn + `, feedback_type, payload, created_at) VALUES
			('f1', 'e/p', 'weft:///e/p/call/c1', 'wandb.reaction.1', '{"emoji":"x"}', 1),
			('f2', 'e/p', 'weft:///e/p/call/c1', 'wandb.runnable.judge', '{"output":1}', 2),
			('f3', 'e/p', 'weft:///e/p/call/c1', 'custom.type', '{}', 3)`,
		`UPDATE calls SET attributes = '{"weave":{"eval":{"run_id":"r1"},"kind":"llm"},"otel":{"attributes":{"wandb.is_turn":true}}}' WHERE id = 'c1'`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_ = st.Close()

	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fbs, err := st.QueryFeedback(ctx, "e/p", nil, nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range fbs {
		got[f.ID] = f.FeedbackType
		if f.Ref != "weft:///e/p/call/c1" {
			t.Fatalf("ref = %q", f.Ref)
		}
	}
	if got["f1"] != "weft.reaction" || got["f2"] != "weft.scorer.judge" || got["f3"] != "custom.type" {
		t.Fatalf("types = %v", got)
	}
	var c *store.Call
	if err := st.QueryCalls(ctx, store.CallsQuery{ProjectID: "e/p"}, func(x *store.Call) error { c = x; return nil }); err != nil || c == nil {
		t.Fatalf("read call: %v", err)
	}
	w, _ := c.Attributes["weft"].(map[string]any)
	if w["span_kind"] != "llm" || w["eval"].(map[string]any)["run_id"] != "r1" || c.Attributes["weave"] != nil {
		t.Fatalf("attributes = %v", c.Attributes)
	}
	// A query saved with legacy field names still works.
	n, _, err := st.CountCalls(ctx, "e/p", nil, map[string]any{"$expr": map[string]any{"$eq": []any{
		map[string]any{"$getField": "feedback.wandb.reaction.1.emoji"}, map[string]any{"$literal": "x"}}}}, "")
	if err != nil || n != 1 {
		t.Fatalf("legacy query = %d %v", n, err)
	}
}
