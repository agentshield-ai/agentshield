package store

import (
	"context"
	"fmt"
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

// The weft-names migration renames the fields Weft itself named op, call or
// thread: saved objects get a new version (the old one is kept and its
// aliases move), records are rewritten in place, and user text that happens
// to say "call" is left alone.
func TestWeftNamesMigration(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	objs := map[string]struct {
		cls string
		val map[string]any
	}{
		"mon": {"Monitor", map[string]any{"_class_name": "Monitor", "_bases": []any{"Object", "BaseModel"}, "name": "mon",
			"description": "call", "active": true, "op_names": []any{"call", "agent"}, "scopes": []any{"call", "session"}}},
		"auto": {"Automation", map[string]any{"_class_name": "Automation", "name": "auto", "trigger": map[string]any{"type": "error_rate",
			"op_names": []any{"agent"}, "min_calls": 3, "threshold": 0.5}, "action": map[string]any{"type": "webhook", "url": "http://x",
			"message": "{call_id} {total_calls}"}}},
		"view": {"SavedView", map[string]any{"_class_name": "SavedView", "name": "view", "view_type": "traces", "definition": map[string]any{
			"filter": map[string]any{"op_names": []any{"f"}, "thread_ids": []any{"t1"}, "call_ids": []any{"c1"}},
			"query":  map[string]any{"$expr": map[string]any{"$eq": []any{map[string]any{"$getField": "op_name"}, map[string]any{"$literal": "x"}}}}}}},
		"dash": {"Dashboard", map[string]any{"_class_name": "Dashboard", "name": "dash", "filter": map[string]any{"thread_ids": []any{"t"}},
			"panels": []any{map[string]any{"title": "thread", "group_by": "op_name"}, map[string]any{"group_by": "thread"}, map[string]any{"group_by": "model"}}}},
		"lb": {"Leaderboard", map[string]any{"_class_name": "Leaderboard", "name": "lb", "columns": []any{},
			"weft_dynamic": map[string]any{"evaluation_call_ids": []any{"r1"}}}},
		"spec": {"AnnotationSpec", map[string]any{"_class_name": "AnnotationSpec", "name": "spec", "field_schema": map[string]any{"type": "string"},
			"op_scope": []any{"weft:///ent/proj/function/f:abc"}}},
	}
	digests := map[string]string{}
	for name, o := range objs {
		d, _, err := s.CreateObject(ctx, proj, name, o.val, o.cls, nil)
		if err != nil {
			t.Fatal(name, err)
		}
		digests[name] = d
	}
	db := s.DB()
	if _, err := db.Exec(`INSERT INTO object_aliases(project_id, object_id, alias, digest) VALUES(?, 'mon', 'prod', ?)`, proj, digests["mon"]); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAutomationRun(ctx, &AutomationRun{ProjectID: proj, AutomationID: "auto", Status: "success",
		Event: map[string]any{"type": "error_rate", "total_calls": 4, "error_calls": 2, "op_names": []any{"agent"}, "call_id": "c1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateFeedback(ctx, FeedbackCreate{ProjectID: proj, Ref: "weft:///ent/proj/span/c1", FeedbackType: "weft.signal",
		Payload: map[string]any{"signal": "s", "scorer_call_id": "j1"}}); err != nil {
		t.Fatal(err)
	}
	for i, kind := range []string{"call", "call", "span"} {
		src := []string{"c1", "c2", "c2"}[i]
		if _, err := db.Exec(`INSERT INTO dataset_source_links(id, project_id, dataset_object_id, dataset_digest, row_digest, source_kind,
			source_id, source_trace_id, created_at, updated_at) VALUES(?, ?, 'ds', 'd', 'r', ?, ?, '', 1, 1)`, fmt.Sprint("l", i), proj, kind, src); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM weft_migrations WHERE name = 'weft-names-1'`); err != nil {
		t.Fatal(err)
	}
	if err := migrateWeftNames(db); err != nil {
		t.Fatal(err)
	}
	latest := func(name string) (string, int) {
		t.Helper()
		var val string
		var v int
		if err := db.QueryRow(`SELECT val, version_index FROM objects WHERE project_id = ? AND object_id = ? ORDER BY version_index DESC LIMIT 1`,
			proj, name).Scan(&val, &v); err != nil {
			t.Fatal(err)
		}
		return val, v
	}
	for name, want := range map[string][]string{
		"mon":  {`"names":["call","agent"]`, `"scopes":["span","session"]`, `"description":"call"`},
		"auto": {`"names":["agent"]`, `"min_spans":3`, `"message":"{call_id} {total_calls}"`},
		"view": {`"names":["f"]`, `"session_ids":["t1"]`, `"ids":["c1"]`, `"$getField":"op_name"`},
		"dash": {`"filter":{"session_ids":["t"]}`, `"group_by":"name","title":"thread"`, `"group_by":"session"`, `"group_by":"model"`},
		"lb":   {`"evaluation_run_ids":["r1"]`},
		"spec": {`"names":["weft:///ent/proj/function/f:abc"]`},
	} {
		val, v := latest(name)
		if v != 1 {
			t.Fatalf("%s: latest version %d, want a new version 1: %s", name, v, val)
		}
		for _, w := range want {
			if !strings.Contains(val, w) {
				t.Fatalf("%s: %s lacks %s", name, val, w)
			}
		}
		for _, old := range []string{`"op_names"`, `"op_scope"`, `"min_calls"`, `"thread_ids"`, `"call_ids"`, `"evaluation_call_ids"`} {
			if strings.Contains(val, old) {
				t.Fatalf("%s still has %s: %s", name, old, val)
			}
		}
	}
	var aliasDigest string
	if err := db.QueryRow(`SELECT digest FROM object_aliases WHERE project_id = ? AND object_id = 'mon' AND alias = 'prod'`, proj).Scan(&aliasDigest); err != nil {
		t.Fatal(err)
	}
	if aliasDigest == digests["mon"] {
		t.Fatal("the alias stayed on the old version")
	}
	var event, payload string
	if err := db.QueryRow(`SELECT event FROM automation_runs WHERE project_id = ?`, proj).Scan(&event); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(event, `"total_spans":4`) || !strings.Contains(event, `"span_id":"c1"`) || !strings.Contains(event, `"names"`) ||
		strings.Contains(event, "call") {
		t.Fatalf("event = %s", event)
	}
	if err := db.QueryRow(`SELECT payload FROM feedback WHERE project_id = ? AND feedback_type = 'weft.signal'`, proj).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"scorer_span_id":"j1"`) {
		t.Fatalf("payload = %s", payload)
	}
	var calls, spans int
	if err := db.QueryRow(`SELECT COUNT(*) FILTER (WHERE source_kind = 'call'), COUNT(*) FILTER (WHERE source_kind = 'span')
		FROM dataset_source_links WHERE project_id = ?`, proj).Scan(&calls, &spans); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || spans != 2 {
		t.Fatalf("links: %d call, %d span (want 0, 2: the duplicate c2 is dropped)", calls, spans)
	}
	// Running it again changes nothing.
	if err := migrateWeftNames(db); err != nil {
		t.Fatal(err)
	}
	if _, v := latest("mon"); v != 1 {
		t.Fatalf("second run published version %d", v)
	}
}
