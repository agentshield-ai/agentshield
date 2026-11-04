package legacy_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
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
// feedback ref column, the user and run columns, feedback types, legacy
// attribute keys of calls, and saved queries that use legacy names (a new
// version, with the aliases moved and the old version kept).
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
	// Saved queries written with legacy names.
	viewVal := map[string]any{"_class_name": "SavedView", "_bases": []any{"Object", "BaseModel"}, "label": "mine",
		"definition": map[string]any{"filter": map[string]any{"wb_user_ids": []any{"alice"}},
			"query":   map[string]any{"$expr": map[string]any{"$eq": []any{map[string]any{"$getField": "feedback.wandb.reaction.1.emoji"}, map[string]any{"$literal": "x"}}}},
			"sort_by": []any{map[string]any{"field": "wb_user_id", "direction": "asc"}}}}
	oldView, _, err := st.CreateObject(ctx, "e/p", "view", viewVal, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAlias(ctx, "e/p", "view", oldView, "prod"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateObject(ctx, "e/p", "auto", map[string]any{"_class_name": "Automation", "_bases": []any{"Object", "BaseModel"},
		"trigger": map[string]any{"type": "feedback_created", "scorer": "wandb.runnable.judge", "feedback_types": []any{"wandb.reaction.*"}},
		"action":  map[string]any{"type": "webhook", "url": "https://example.com"}}, "", nil); err != nil {
		t.Fatal(err)
	}
	// Turn the database back into the old shape.
	db := st.DB()
	for _, q := range []string{
		`ALTER TABLE feedback RENAME COLUMN ref TO ` + legacy.FeedbackRefColumn,
		`ALTER TABLE calls RENAME COLUMN user_id TO wb_user_id`,
		`ALTER TABLE calls RENAME COLUMN run_id TO wb_run_id`,
		`ALTER TABLE calls RENAME COLUMN run_step TO wb_run_step`,
		`ALTER TABLE calls RENAME COLUMN run_step_end TO wb_run_step_end`,
		`ALTER TABLE feedback RENAME COLUMN user_id TO wb_user_id`,
		`ALTER TABLE objects RENAME COLUMN user_id TO wb_user_id`,
		`ALTER TABLE registry_versions RENAME COLUMN user_id TO wb_user_id`,
		`UPDATE calls SET wb_user_id = 'alice', wb_run_id = 'e/p/run1', wb_run_step = 3 WHERE id = 'c1'`,
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
		if f.Ref != "weft:///e/p/span/c1" { // ref kinds are migrated too (store/refkinds.go)
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
	if c.UserID == nil || *c.UserID != "alice" || c.RunID == nil || *c.RunID != "e/p/run1" || c.RunStep == nil || *c.RunStep != 3 {
		t.Fatalf("user and run columns = %v %v %v", c.UserID, c.RunID, c.RunStep)
	}
	// The saved view has a new latest version with Weft's names; the alias
	// moved to it and the old version is kept.
	latest, err := st.ReadObject(ctx, "e/p", "view", "latest")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(latest.Val)
	if latest.VersionIndex != 1 || latest.Digest == oldView || strings.Contains(string(b), "wandb") || strings.Contains(string(b), "wb_") ||
		!strings.Contains(string(b), `"feedback.weft.reaction.emoji"`) || !strings.Contains(string(b), `"user_ids"`) || !strings.Contains(string(b), `"field":"user_id"`) {
		t.Fatalf("migrated view v%d = %s", latest.VersionIndex, b)
	}
	if prod, err := st.ReadObject(ctx, "e/p", "view", "prod"); err != nil || prod.Digest != latest.Digest {
		t.Fatalf("alias not moved: %v", err)
	}
	if old, err := st.ReadObject(ctx, "e/p", "view", oldView); err != nil || old.VersionIndex != 0 {
		t.Fatalf("old version lost: %v", err)
	}
	auto, err := st.ReadObject(ctx, "e/p", "auto", "latest")
	if err != nil {
		t.Fatal(err)
	}
	trig := auto.Val.(map[string]any)["trigger"].(map[string]any)
	if trig["scorer"] != "weft.scorer.judge" || trig["feedback_types"].([]any)[0] != "weft.reaction" {
		t.Fatalf("migrated automation trigger = %v", trig)
	}
	// The migrated query runs; the old names are not read at run time.
	q := latest.Val.(map[string]any)["definition"].(map[string]any)["query"]
	if n, _, err := st.CountCalls(ctx, "e/p", nil, q, ""); err != nil || n != 1 {
		t.Fatalf("migrated query = %d %v", n, err)
	}
	if n, _, _ := st.CountCalls(ctx, "e/p", nil, map[string]any{"$expr": map[string]any{"$eq": []any{
		map[string]any{"$getField": "feedback.wandb.reaction.1.emoji"}, map[string]any{"$literal": "x"}}}}, ""); n != 0 {
		t.Fatal("a legacy feedback type was translated outside the adapter")
	}
	if _, _, err := st.CountCalls(ctx, "e/p", nil, map[string]any{"$expr": map[string]any{"$eq": []any{
		map[string]any{"$getField": "wb_user_id"}, map[string]any{"$literal": "alice"}}}}, ""); err == nil {
		t.Fatal("a legacy field name was accepted outside the adapter")
	}
	// Opening again changes nothing.
	_ = st.Close()
	st2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if again, _ := st2.ReadObject(ctx, "e/p", "view", "latest"); again.VersionIndex != 1 {
		t.Fatalf("migration ran twice: v%d", again.VersionIndex)
	}
}

func TestJSONRewrite(t *testing.T) {
	rw := legacy.JSONRewrite{Key: legacy.FieldName, Value: legacy.QueryFieldName, Skip: func(k string) bool { return k == "inputs" }}
	in := `{"start": {"wb_user_id": "u", "inputs": {"wb_user_id": "keep", "x": ["summary.weave.status"]}, "n": 1.50,
 "q": {"$getField": "summary.weave.status"}, "s": "wb_run_id", "e": "a\"wb_user_id"}}` + "\n" + `{"wb_run_ids": ["r"]}`
	want := `{"start": {"user_id": "u", "inputs": {"wb_user_id": "keep", "x": ["summary.weave.status"]}, "n": 1.50,
 "q": {"$getField": "summary.weft.status"}, "s": "run_id", "e": "a\"wb_user_id"}}` + "\n" + `{"run_ids": ["r"]}`
	if got := string(rw.Apply([]byte(in))); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if n, ok := legacy.SavedQueryName("wandb.reaction.*"); !ok || n != "weft.reaction" {
		t.Fatalf("pattern %q", n)
	}
	if _, ok := legacy.SavedQueryName("weft.note"); ok {
		t.Fatal("current name mapped")
	}
}

func TestRewriteKeyedValues(t *testing.T) {
	m := map[string]map[string]string{"scopes": {"call": "span"}, "group_by": {"op_name": "name"}}
	for in, want := range map[string]string{
		`{"scopes": ["call", "session"], "description": "call"}`:                             `{"scopes": ["span", "session"], "description": "call"}`,
		`{"names":["call"],"scopes":[],"x":{"scopes":"call"}}`:                               `{"names":["call"],"scopes":[],"x":{"scopes":"span"}}`,
		`{"panels":[{"group_by":"op_name","title":"op_name"},{"n":1,"group_by":"op_name"}]}`: `{"panels":[{"group_by":"name","title":"op_name"},{"n":1,"group_by":"name"}]}`,
		`{"a":{"scopes":["call"]},"scopes":["call"]}`:                                        `{"a":{"scopes":["span"]},"scopes":["span"]}`,
		`{"scopes":["c\u0061ll"]}`:                                                           `{"scopes":["c\u0061ll"]}`, // escaped literals are left alone
		`not json`:                                                                           `not json`,
	} {
		if got := string(legacy.RewriteKeyedValues([]byte(in), m)); got != want {
			t.Errorf("RewriteKeyedValues(%s) = %s, want %s", in, got, want)
		}
	}
	got := string(legacy.RenameSavedObject("Monitor", []byte(`{"op_names":["call"],"scopes":["call"]}`)))
	if got != `{"names":["call"],"scopes":["span"]}` {
		t.Errorf("RenameSavedObject = %s", got)
	}
	if got := string(legacy.RenameSavedObject("Dataset", []byte(`{"op_names":1}`))); got != `{"op_names":1}` {
		t.Errorf("other class rewritten: %s", got)
	}
}
