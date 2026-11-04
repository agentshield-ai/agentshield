package store

import (
	"context"
	"path/filepath"
	"testing"
)

// Databases written before Weft's own ref kinds hold "op" and "call" refs in
// columns; they are rewritten once at Open (refkinds.go), content-addressed
// values are not, and both spellings keep resolving and matching.
func TestRefKindsMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "weft.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const p = "e/p"
	d, _, err := s.CreateObject(ctx, p, "f", map[string]any{"_type": "Op", "name": "f"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.StartCall(ctx, CallStart{ProjectID: p, ID: "c1", OpName: "f", StartedAt: "2026-01-01T00:00:00Z",
		Inputs: map[string]any{"self": "weft:///e/p/op/f:" + d}}); err != nil {
		t.Fatal(err)
	}
	// Write rows as an earlier version did.
	db := s.DB()
	for _, q := range []string{
		`UPDATE calls SET op_name = 'weft:///e/p/op/f:` + d + `', input_refs = '["weft:///e/p/op/f:` + d + `"]' WHERE id = 'c1'`,
		`INSERT INTO feedback(id, project_id, ref, feedback_type, payload, created_at, runnable_ref, call_ref)
			VALUES('f1', 'e/p', 'weft:///e/p/call/c1', 'weft.note', '{"note":"x"}', 1, 'weft:///e/p/op/f:` + d + `', 'weft:///e/p/call/judge')`,
		`INSERT INTO registry_versions(project_id, collection, version_index, ref, source_project_id, object_id, digest, created_at)
			VALUES('r/reg', 'c', 0, 'weft:///e/p/op/f:` + d + `', 'e/p', 'f', '` + d + `', 1)`,
		`DELETE FROM weft_migrations WHERE name = 'ref-kinds-1'`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fn := "weft:///e/p/function/f:" + d
	c, err := s.GetCall(ctx, p, "c1", false, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.OpName != fn || c.OpBaseName() != "f" {
		t.Fatalf("op_name = %q", c.OpName)
	}
	if c.Inputs["self"] != "weft:///e/p/op/f:"+d {
		t.Fatalf("inputs are user data and must not change: %v", c.Inputs)
	}
	if len(c.Feedback) != 1 || c.Feedback[0].Ref != "weft:///e/p/span/c1" || *c.Feedback[0].RunnableRef != fn ||
		*c.Feedback[0].CallRef != "weft:///e/p/span/judge" {
		t.Fatalf("feedback = %+v", c.Feedback)
	}
	var reg string
	if err := s.ReadDB().QueryRow(`SELECT ref FROM registry_versions WHERE project_id = 'r/reg'`).Scan(&reg); err != nil || reg != fn {
		t.Fatalf("registry ref = %q %v", reg, err)
	}
	// Filters match both spellings of a stored input ref.
	for _, ref := range []string{fn, "weft:///e/p/op/f:" + d} {
		calls, err := s.ListCalls(ctx, CallsQuery{ProjectID: p, Filter: &CallsFilter{InputRefs: []string{ref}}})
		if err != nil || len(calls) != 1 {
			t.Fatalf("input_refs %s = %d calls, %v", ref, len(calls), err)
		}
		calls, err = s.ListCalls(ctx, CallsQuery{ProjectID: p, Filter: &CallsFilter{OpNames: []string{ref}}})
		if err != nil || len(calls) != 1 {
			t.Fatalf("op_names %s = %d calls, %v", ref, len(calls), err)
		}
	}
	// Both spellings resolve.
	for _, ref := range []string{fn, "weft:///e/p/op/f:" + d} {
		v, err := s.ReadRef(ctx, ref)
		if err != nil || v.(map[string]any)["name"] != "f" {
			t.Fatalf("ReadRef(%s) = %v %v", ref, v, err)
		}
	}
	// New writes use the current names whatever the caller sent.
	fb, err := s.CreateFeedback(ctx, FeedbackCreate{ProjectID: p, Ref: "weft:///e/p/call/c1", FeedbackType: "weft.note",
		Payload: map[string]any{"note": "y"}, CallRef: ptr("weft:///e/p/call/x")})
	if err != nil || fb.Ref != "weft:///e/p/span/c1" || *fb.CallRef != "weft:///e/p/span/x" {
		t.Fatalf("new feedback = %+v %v", fb, err)
	}
}
