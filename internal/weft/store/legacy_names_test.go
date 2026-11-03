package store

import (
	"context"
	"strings"
	"testing"
)

// The one-time legacy-names migration renames columns, feedback types,
// legacy attribute keys and saved queries. It never rewrites user data:
// media a legacy client stored in its own encoding (inputs, outputs, object
// values) stays byte for byte as sent, which is why the UI keeps decoding
// that encoding whether or not the compatibility adapter is on
// (docs/weft/COMPATIBILITY.md).
func TestLegacyMigrationLeavesMediaValuesAlone(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	media := map[string]any{"_type": "CustomWeaveType", "weave_type": map[string]any{"type": "PIL.Image.Image"},
		"files": map[string]any{"image.png": "0123abcd"}, "load_op": "weave:///e/p/op/load_PIL.Image.Image:x"}
	st := CallStart{ProjectID: proj, ID: "m1", TraceID: "tm", OpName: "f", StartedAt: detTS(0),
		Inputs: map[string]any{"img": media}, Attributes: map[string]any{"weave": map[string]any{"kind": "llm"}}}
	if _, err := s.UpsertComplete(ctx, []CompleteCall{{Start: st, End: &CallEnd{EndedAt: detTS(1), Output: media, Summary: map[string]any{}}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateObject(ctx, proj, "pic", media, "", nil); err != nil {
		t.Fatal(err)
	}
	db := s.DB()
	read := func() (in, out, attrs, val string) {
		t.Helper()
		if err := db.QueryRow(`SELECT inputs, output, attributes FROM calls WHERE project_id = ? AND id = 'm1'`, proj).Scan(&in, &out, &attrs); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT val FROM objects WHERE project_id = ? AND object_id = 'pic'`, proj).Scan(&val); err != nil {
			t.Fatal(err)
		}
		return
	}
	in0, out0, _, val0 := read()
	for _, q := range []string{`DELETE FROM weft_migrations WHERE name IN ('legacy-names-1', 'legacy-objects-1')`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateLegacyNames(db); err != nil {
		t.Fatal(err)
	}
	if err := migrateLegacyObjects(db); err != nil {
		t.Fatal(err)
	}
	in1, out1, attrs1, val1 := read()
	if in1 != in0 || out1 != out0 || val1 != val0 {
		t.Fatalf("media rewritten:\n in  %s\n  -> %s\n out %s\n  -> %s\n val %s\n  -> %s", in0, in1, out0, out1, val0, val1)
	}
	if !strings.Contains(in1, `"CustomWeaveType"`) || !strings.Contains(in1, `"weave_type"`) {
		t.Fatalf("inputs = %s", in1)
	}
	// The legacy attribute key itself was renamed.
	if strings.Contains(attrs1, `"weave"`) || !strings.Contains(attrs1, `"span_kind":"llm"`) {
		t.Fatalf("attributes = %s", attrs1)
	}
}
