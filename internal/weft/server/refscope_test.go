package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A key scoped to one project must not reach another project's data
// through refs held in its own project's values: refs nested in an object,
// a ref's path, expand_columns, evaluation datasets and scorers, compare
// rows, the adapter's refs route. Direct reads of the other project are
// refused already (admin.go); these are the indirect ones
// (store/refscope.go).

const (
	projA = "ea/pa"
	projB = "eb/pb"
)

type crossFixture struct {
	e          *testEnv
	scoped     string // key limited to projA
	tableB     string // table ref in projB
	rowDigestB string
	fileB      string
}

func newCrossFixture(t *testing.T, legacy bool) *crossFixture {
	t.Helper()
	var e *testEnv
	if legacy {
		e = newLegacyEnv(t, "root")
	} else {
		e = newEnv(t, "root")
	}
	f := &crossFixture{e: e}
	e.mustCode(200, "POST", "/api/v1/spans/start", "root", map[string]any{"project_id": projB, "id": "span-b", "name": "f",
		"inputs": map[string]any{"secret": "SPAN-B"}})
	e.mustCode(200, "POST", "/api/v1/objects/save", "root", map[string]any{"project_id": projB, "name": "secret",
		"value": map[string]any{"secret": "OBJ-B"}})
	var tb map[string]any
	_ = json.Unmarshal(e.mustCode(200, "POST", "/api/v1/tables/create", "root", map[string]any{"project_id": projB,
		"rows": []any{map[string]any{"secret": "ROW-B", "expected": "ROW-B"}}}), &tb)
	f.tableB = tb["ref"].(string)
	f.rowDigestB = tb["row_digests"].([]any)[0].(string)
	e.mustCode(200, "POST", "/api/v1/objects/save", "root", map[string]any{"project_id": projB, "name": "dsB",
		"value": map[string]any{"_type": "Dataset", "_class_name": "Dataset", "_bases": []any{"Object", "BaseModel"}, "rows": f.tableB}})
	e.mustCode(200, "POST", "/api/v1/objects/save", "root", map[string]any{"project_id": projB, "name": "exactB",
		"value": map[string]any{"_class_name": "ExactMatchScorer", "_bases": []any{"Scorer", "Object", "BaseModel"}}})

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("project_id", projB)
	fw, _ := mw.CreateFormFile("file", "x.txt")
	_, _ = fw.Write([]byte("FILE-B-SECRET"))
	_ = mw.Close()
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/files/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer root")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("upload: %v %v", err, resp)
	}
	var fu map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&fu)
	resp.Body.Close()
	f.fileB = fu["digest"].(string)

	f.scoped, _ = createKey(e, "root", map[string]any{"name": "scopedA", "projects": []any{projA}})
	// As the scoped key: a value in A that points into B.
	e.mustCode(200, "POST", "/api/v1/objects/save", f.scoped, map[string]any{"project_id": projA, "name": "pivot",
		"value": map[string]any{"x": "weft:///" + projB + "/object/secret:latest", "t": f.tableB,
			"s": "weft:///" + projB + "/span/span-b", "deep": "weft:///" + projA + "/object/pivot:latest/key/x"}})
	return f
}

func (f *crossFixture) readRefs(t *testing.T, key string, refs ...string) (int, []any, string) {
	t.Helper()
	code, out := f.e.admDo("POST", "/api/v1/refs/read", key, map[string]any{"refs": refs})
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	items, _ := m["items"].([]any)
	return code, items, string(out)
}

func TestNestedRefsStayInReadableProjects(t *testing.T) {
	f := newCrossFixture(t, false)
	pivot := "weft:///" + projA + "/object/pivot:latest"
	for _, tc := range []struct {
		name, ref string
	}{
		{"object through attr", pivot + "/attr/x/key/secret"},
		{"walk ending on an object ref", pivot + "/key/x"},
		{"table row by index", pivot + "/key/t/index/0"},
		{"table row by digest", pivot + "/key/t/id/" + f.rowDigestB},
		{"whole table", pivot + "/key/t"},
		{"span inputs", pivot + "/key/s/key/inputs"},
		{"whole span", pivot + "/key/s"},
		{"ref to a ref", pivot + "/key/deep/key/secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, items, out := f.readRefs(t, f.scoped, tc.ref)
			if code != 200 || len(items) != 1 || items[0] != nil {
				t.Fatalf("%s -> %d %s, want [null]", tc.ref, code, out)
			}
			if strings.Contains(out, "-B") {
				t.Fatalf("leaked project B data: %s", out)
			}
			// A caller who may read both projects still follows the ref.
			if code, items, out := f.readRefs(t, "root", tc.ref); code != 200 || items[0] == nil || !strings.Contains(out, "-B") {
				t.Fatalf("root %s -> %d %s", tc.ref, code, out)
			}
		})
	}
	// The pivot itself is readable (its refs are plain strings), and a
	// direct ref into B is refused.
	if code, items, _ := f.readRefs(t, f.scoped, pivot); code != 200 || items[0].(map[string]any)["x"] == nil {
		t.Fatalf("pivot read -> %d %v", code, items)
	}
	if code, _, out := f.readRefs(t, f.scoped, "weft:///"+projB+"/object/secret:latest"); code != 403 {
		t.Fatalf("direct B ref -> %d %s", code, out)
	}
}

func TestCrossProjectRefsInQueriesAndEvaluations(t *testing.T) {
	f := newCrossFixture(t, false)
	e, k := f.e, f.scoped
	objB := "weft:///" + projB + "/object/secret:latest"

	// expand_columns on an input that holds a ref into B leaves the ref.
	e.mustCode(200, "POST", "/api/v1/spans/start", k, map[string]any{"project_id": projA, "id": "pv1", "name": "pv",
		"inputs": map[string]any{"example": objB, "row": f.tableB + "/index/0"}})
	for _, path := range []string{"/api/v1/spans/query", "/api/v1/spans/stream", "/api/v1/spans/export"} {
		out := e.mustCode(200, "POST", path, k, map[string]any{"project_id": projA, "filter": map[string]any{"ids": []any{"pv1"}},
			"expand_columns": []any{"inputs.example", "inputs.row"}, "format": "jsonl"})
		if strings.Contains(string(out), "-B") || !strings.Contains(string(out), objB) {
			t.Errorf("%s expanded a ref into B: %s", path, out)
		}
	}

	// Evaluations refuse datasets, rows and scorers in B.
	model := e.mustCode(200, "POST", "/api/v1/objects/save", k, map[string]any{"project_id": projA, "name": "m",
		"value": map[string]any{"_class_name": "LLMStructuredCompletionModel", "_bases": []any{"Model", "Object", "BaseModel"},
			"llm_model_id": "mock/echo"}})
	var mref map[string]any
	_ = json.Unmarshal(model, &mref)
	save := func(name string, val map[string]any) string {
		var m map[string]any
		_ = json.Unmarshal(e.mustCode(200, "POST", "/api/v1/objects/save", k, map[string]any{"project_id": projA, "name": name, "value": val}), &m)
		return m["ref"].(string)
	}
	ds := func(rows string) map[string]any {
		return map[string]any{"_type": "Dataset", "_class_name": "Dataset", "_bases": []any{"Object", "BaseModel"}, "rows": rows}
	}
	evalOf := func(dataset string, scorers ...any) map[string]any {
		return map[string]any{"_type": "Evaluation", "_class_name": "Evaluation", "_bases": []any{"Object", "BaseModel"},
			"dataset": dataset, "scorers": scorers}
	}
	var tblA map[string]any
	_ = json.Unmarshal(e.mustCode(200, "POST", "/api/v1/tables/create", k, map[string]any{"project_id": projA,
		"rows": []any{map[string]any{"q": "a", "expected": "a"}}}), &tblA)
	dsA := save("dsA", ds(tblA["ref"].(string)))
	for _, tc := range []struct {
		name string
		eval map[string]any
	}{
		{"dataset rows in B", evalOf(save("dsRowsB", ds(f.tableB)))},
		{"dataset in B", evalOf("weft:///" + projB + "/object/dsB:latest")},
		{"scorer in B", evalOf(dsA, "weft:///"+projB+"/object/exactB:latest")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := save("ev", tc.eval)
			code, out := e.admDo("POST", "/api/v1/evaluations/run", k, map[string]any{"project_id": projA, "evaluation_ref": ev,
				"model_ref": mref["ref"]})
			if code != 403 || !strings.Contains(string(out), projB) {
				t.Fatalf("evaluations/run -> %d %s, want 403", code, out)
			}
		})
	}

	// Compare rows resolve example refs only in readable projects.
	e.mustCode(200, "POST", "/api/v1/spans/start", k, map[string]any{"project_id": projA, "id": "run1", "name": "Evaluation.evaluate"})
	e.mustCode(200, "POST", "/api/v1/spans/start", k, map[string]any{"project_id": projA, "id": "pas1", "parent_id": "run1",
		"name": "Evaluation.predict_and_score", "inputs": map[string]any{"example": f.tableB + "/id/" + f.rowDigestB}})
	out := e.mustCode(200, "POST", "/api/v1/evaluations/compare", k, map[string]any{"project_id": projA, "ids": []any{"run1"}})
	if strings.Contains(string(out), "ROW-B") {
		t.Errorf("compare leaked a row of B: %s", out)
	}

	// Content-addressed reads are per project.
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/v1/tables/query", map[string]any{"project_id": projA, "digest": strings.TrimPrefix(f.tableB, "weft:///"+projB+"/table/")}},
		{"POST", "/api/v1/tables/create", map[string]any{"project_id": projA, "row_digests": []any{f.rowDigestB}}},
		{"GET", "/api/v1/files/" + f.fileB + "?project_id=" + projA, nil},
		{"POST", "/api/v1/registry/link", map[string]any{"project_id": projA, "portfolio_name": "p", "ref": objB}},
	} {
		code, out := e.admDo(tc.method, tc.path, k, tc.body)
		if code == 200 || strings.Contains(string(out), "-B") {
			t.Fatalf("%s %s -> %d %s", tc.method, tc.path, code, out)
		}
	}
}

// The adapter's refs route applies the same scope.
func TestCrossProjectRefsAdapterRoute(t *testing.T) {
	f := newCrossFixture(t, true)
	code, out := f.e.admDo("POST", "/refs/read_batch", f.scoped, map[string]any{"refs": []any{
		"weft:///" + projA + "/object/pivot:latest/attr/x/key/secret", "weft:///" + projA + "/object/pivot:latest/key/t/index/0"}})
	if code != 200 || strings.Contains(string(out), "-B") || !strings.Contains(string(out), `"vals":[null,null]`) {
		t.Fatalf("/refs/read_batch -> %d %s", code, out)
	}
}

// Without keys every project is readable, so nested refs into other
// projects still resolve.
func TestCrossProjectRefsOpenServer(t *testing.T) {
	e := newEnv(t)
	e.post("/api/v1/objects/save", map[string]any{"project_id": projB, "name": "secret", "value": map[string]any{"secret": "OBJ-B"}})
	e.post("/api/v1/objects/save", map[string]any{"project_id": projA, "name": "pivot",
		"value": map[string]any{"x": "weft:///" + projB + "/object/secret:latest"}})
	res := e.post("/api/v1/refs/read", map[string]any{"refs": []any{"weft:///" + projA + "/object/pivot:latest/key/x/key/secret"}})
	if res["items"].([]any)[0] != "OBJ-B" {
		t.Fatalf("open server nested ref = %v", res)
	}
}

// A monitor runs with no caller: it resolves its scorers only in its own
// project, even on an open server.
func TestMonitorScorersStayInProject(t *testing.T) {
	e := newEnv(t)
	scorer := func(p, name string) string {
		return e.post("/api/v1/objects/save", map[string]any{"project_id": p, "name": name, "value": map[string]any{
			"_class_name": "RegexScorer", "_bases": []any{"Scorer", "Object", "BaseModel"}, "pattern": "ok"}})["ref"].(string)
	}
	foreign, own := scorer(projB, "foreign"), scorer(projA, "own")
	e.post("/api/v1/objects/save", map[string]any{"project_id": projA, "name": "mon", "value": map[string]any{"_class_name": "Monitor",
		"_bases": []any{"Object", "BaseModel"}, "name": "mon", "active": true, "sampling_rate": 1, "names": []any{"answer"},
		"scorers": []any{foreign, own}}})
	e.post("/api/v1/spans/batch", map[string]any{"project_id": projA, "items": []any{
		startItem(map[string]any{"id": "m1", "name": "answer", "started_at": "2026-01-01T00:00:00Z"}),
		endItem(map[string]any{"id": "m1", "ended_at": "2026-01-01T00:00:01Z", "output": "ok"}),
	}})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		items := e.post("/api/v1/feedback/query", map[string]any{"project_id": projA})["items"].([]any)
		if len(items) > 0 {
			time.Sleep(200 * time.Millisecond) // both scorers run in one pass
			items = e.post("/api/v1/feedback/query", map[string]any{"project_id": projA})["items"].([]any)
			for _, it := range items {
				if ft := it.(map[string]any)["feedback_type"]; ft != "weft.scorer.own" {
					t.Fatalf("monitor ran a scorer of another project: %v", it)
				}
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("monitor did not score the span")
}
