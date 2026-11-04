package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// do sends a request with any method; body may be nil.
func (e *testEnv) do(method, path string, body any, user string) (int, []byte) {
	e.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if e.key != "" {
		req.Header.Set("Authorization", "Bearer "+e.key)
	}
	if user != "" {
		req.Header.Set("X-Weft-User", user)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (e *testEnv) doJSON(method, path string, body any, user string) map[string]any {
	e.t.Helper()
	code, out := e.do(method, path, body, user)
	if code != 200 {
		e.t.Fatalf("%s %s -> %d: %s", method, path, code, out)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		e.t.Fatalf("%s: bad JSON %s", path, out)
	}
	return m
}

func makeCalls(t *testing.T, e *testEnv, ids ...string) {
	t.Helper()
	for _, id := range ids {
		e.startSpan(map[string]any{"id": id, "trace_id": id, "name": "answer",
			"started_at": "2025-01-01T00:00:00Z", "inputs": map[string]any{"question": "q-" + id}})
		e.endSpan(map[string]any{"id": id, "ended_at": "2025-01-01T00:00:01Z", "output": "a-" + id, "summary": map[string]any{}})
	}
}

func TestAnnotationQueues(t *testing.T) {
	e := newEnv(t)
	makeCalls(t, e, "c1", "c2", "c3")
	spec := publish(e, "quality", map[string]any{"_class_name": "AnnotationSpec", "_bases": []any{"Object", "BaseModel"}, "name": "quality",
		"field_schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 5}})

	created := e.post("/annotation_queues", map[string]any{"project_id": pid, "name": "Review", "description": "weekly", "scorer_refs": []any{spec}})
	qid := created["id"].(string)
	if qid == "" {
		t.Fatal("no id")
	}
	if code, _ := e.raw("/annotation_queues", map[string]any{"project_id": pid, "name": "", "scorer_refs": []any{}}, nil); code != 400 {
		t.Fatalf("empty name accepted: %d", code)
	}

	// read / update
	q := e.doJSON("GET", "/annotation_queues/"+qid+"?project_id="+pid, nil, "")["queue"].(map[string]any)
	if q["name"] != "Review" || q["created_by"] != "anonymous" || len(q["scorer_refs"].([]any)) != 1 {
		t.Fatalf("read: %v", q)
	}
	upd := e.doJSON("PUT", "/annotation_queues/"+qid, map[string]any{"project_id": pid, "name": "Review 2"}, "")["queue"].(map[string]any)
	if upd["name"] != "Review 2" || upd["description"] != "weekly" || len(upd["scorer_refs"].([]any)) != 1 {
		t.Fatalf("update: %v", upd)
	}

	// query streams NDJSON
	code, body := e.raw("/annotation_queues/query", map[string]any{"project_id": pid, "name": "review"}, nil)
	if code != 200 {
		t.Fatalf("query: %d %s", code, body)
	}
	lines := 0
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil || m["id"] != qid {
			t.Fatalf("bad line %q", sc.Text())
		}
		lines++
	}
	if lines != 1 {
		t.Fatalf("expected one queue, got %d", lines)
	}

	// items
	add := e.post("/annotation_queues/"+qid+"/items", map[string]any{"project_id": pid, "call_ids": []any{"c1", "c2", "c1"}, "display_fields": []any{"inputs.question", "output"}})
	if add["added_count"].(float64) != 2 || add["duplicates"].(float64) != 1 {
		t.Fatalf("add: %v", add)
	}
	add = e.post("/annotation_queues/"+qid+"/items", map[string]any{"project_id": pid, "call_ids": []any{"c2", "c3"}, "display_fields": []any{}})
	if add["added_count"].(float64) != 1 || add["duplicates"].(float64) != 1 {
		t.Fatalf("add again: %v", add)
	}
	if code, _ := e.raw("/annotation_queues/"+qid+"/items", map[string]any{"project_id": pid, "call_ids": []any{"nope"}, "display_fields": []any{}}, nil); code != 400 {
		t.Fatalf("unknown call accepted: %d", code)
	}
	items := e.post("/annotation_queues/"+qid+"/items/query", map[string]any{"project_id": pid, "include_position": true})["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("items: %v", items)
	}
	first := items[0].(map[string]any)
	if first["call_id"] != "c1" || first["annotation_state"] != "unstarted" || first["position_in_queue"].(float64) != 1 || first["call_op_name"] != "answer" {
		t.Fatalf("first item: %v", first)
	}
	if df := first["display_fields"].([]any); len(df) != 2 {
		t.Fatalf("display fields: %v", df)
	}

	// progress is per annotator
	itemID := first["id"].(string)
	prog := e.doJSON("POST", "/annotation_queues/"+qid+"/items/"+itemID+"/progress", map[string]any{"project_id": pid, "queue_id": qid, "annotation_state": "completed"}, "alice")
	if it := prog["item"].(map[string]any); it["annotation_state"] != "completed" || it["annotator_user_id"] != "alice" {
		t.Fatalf("progress: %v", prog)
	}
	if code, _ := e.do("POST", "/annotation_queues/"+qid+"/items/"+itemID+"/progress", map[string]any{"project_id": pid, "annotation_state": "bogus"}, "alice"); code != 400 {
		t.Fatalf("bad state accepted: %d", code)
	}
	e.doJSON("POST", "/annotation_queues/"+qid+"/items/"+items[1].(map[string]any)["id"].(string)+"/progress", map[string]any{"project_id": pid, "annotation_state": "skipped"}, "bob")

	alice := e.doJSON("POST", "/annotation_queues/"+qid+"/items/query", map[string]any{"project_id": pid, "filter": map[string]any{"annotation_states": []any{"unstarted"}}}, "alice")["items"].([]any)
	if len(alice) != 2 {
		t.Fatalf("alice should have 2 unstarted, got %d", len(alice))
	}
	bob := e.doJSON("POST", "/annotation_queues/"+qid+"/items/query", map[string]any{"project_id": pid, "filter": map[string]any{"annotation_states": []any{"unstarted"}}}, "bob")["items"].([]any)
	if len(bob) != 2 || bob[0].(map[string]any)["call_id"] != "c1" {
		t.Fatalf("bob unstarted: %v", bob)
	}
	byCall := e.post("/annotation_queues/"+qid+"/items/query", map[string]any{"project_id": pid, "filter": map[string]any{"call_id": "c3"}})["items"].([]any)
	if len(byCall) != 1 {
		t.Fatalf("filter by call: %v", byCall)
	}

	stats := e.post("/annotation_queues/stats", map[string]any{"project_id": pid, "queue_ids": []any{qid}})["stats"].([]any)
	st := stats[0].(map[string]any)
	if st["total_items"].(float64) != 3 || st["completed_items"].(float64) != 2 {
		t.Fatalf("stats: %v", st)
	}

	// feedback carries queue_id and is queryable by it
	fb := e.post("/api/v1/feedback/create", map[string]any{"project_id": pid, "ref": spanRef("c1"), "feedback_type": "weft.annotation.quality",
		"payload": map[string]any{"value": 4}, "annotation_ref": spec, "queue_id": qid})
	res := e.post("/api/v1/feedback/query", map[string]any{"project_id": pid, "query": map[string]any{"$expr": map[string]any{"$eq": []any{
		map[string]any{"$getField": "queue_id"}, map[string]any{"$literal": qid}}}}})["items"].([]any)
	if len(res) != 1 || res[0].(map[string]any)["id"] != fb["id"] || res[0].(map[string]any)["queue_id"] != qid {
		t.Fatalf("feedback by queue: %v", res)
	}

	// an item can be removed, and its call added again
	before := e.post("/annotation_queues/"+qid+"/items/query", map[string]any{"project_id": pid})["items"].([]any)
	victim := before[0].(map[string]any)
	if code, out := e.do("DELETE", "/annotation_queues/"+qid+"/items/"+victim["id"].(string)+"?project_id="+pid, nil, ""); code != 200 {
		t.Fatalf("delete item: %d %s", code, out)
	}
	if code, _ := e.do("DELETE", "/annotation_queues/"+qid+"/items/"+victim["id"].(string)+"?project_id="+pid, nil, ""); code != 404 {
		t.Fatalf("second delete: %d", code)
	}
	after := e.post("/annotation_queues/"+qid+"/items/query", map[string]any{"project_id": pid})["items"].([]any)
	if len(after) != len(before)-1 {
		t.Fatalf("items after delete = %d, before %d", len(after), len(before))
	}
	re := e.post("/annotation_queues/"+qid+"/items", map[string]any{"project_id": pid, "call_ids": []any{victim["call_id"]}})
	if re["added_count"].(float64) != 1 {
		t.Fatalf("re-add after delete: %v", re)
	}

	// delete is soft and hides the queue
	del := e.doJSON("DELETE", "/annotation_queues/"+qid+"?project_id="+pid, nil, "")["queue"].(map[string]any)
	if del["deleted_at"] == nil {
		t.Fatalf("delete: %v", del)
	}
	if code, _ := e.do("GET", "/annotation_queues/"+qid+"?project_id="+pid, nil, ""); code != 404 {
		t.Fatalf("deleted queue readable: %d", code)
	}
	_, body = e.raw("/annotation_queues/query", map[string]any{"project_id": pid}, nil)
	if strings.TrimSpace(string(body)) != "" {
		t.Fatalf("deleted queue listed: %s", body)
	}
}
