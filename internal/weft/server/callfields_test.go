package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestOptionalCallSchemaFields(t *testing.T) {
	e := newEnv(t)
	started := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	ts := started.Format(time.RFC3339Nano)
	e.post("/call/start", map[string]any{"start": map[string]any{"project_id": pid, "id": "root", "trace_id": "t1", "op_name": "agent",
		"started_at": ts, "inputs": map[string]any{"q": "hello"}, "user_id": "alice", "run_id": "run-1", "run_step": 5}})
	e.post("/call/start", map[string]any{"start": map[string]any{"project_id": pid, "id": "child", "trace_id": "t1", "parent_id": "root",
		"op_name": "llm", "started_at": ts, "inputs": map[string]any{"prompt": "a longer prompt"}, "user_id": "alice"}})
	e.post("/call/end", map[string]any{"end": map[string]any{"project_id": pid, "id": "child", "ended_at": ts, "output": "x", "summary": map[string]any{}}})
	e.post("/call/end", map[string]any{"end": map[string]any{"project_id": pid, "id": "root", "ended_at": ts, "output": "done",
		"summary": map[string]any{}, "run_step_end": 9}})
	// The batch path carries both steps.
	e.post("/call/upsert_batch", map[string]any{"batch": []any{
		map[string]any{"mode": "start", "req": map[string]any{"start": map[string]any{"project_id": pid, "id": "c2", "op_name": "op",
			"trace_id": "t2", "started_at": ts, "inputs": map[string]any{}, "run_id": "run-1", "run_step": 11, "user_id": "svc/robot"}}},
		map[string]any{"mode": "end", "req": map[string]any{"end": map[string]any{"project_id": pid, "id": "c2", "ended_at": ts,
			"summary": map[string]any{}, "run_step_end": 12}}}}})

	read := func(id string, extra map[string]any) map[string]any {
		body := map[string]any{"project_id": pid, "id": id}
		for k, v := range extra {
			body[k] = v
		}
		return e.post("/call/read", body)["call"].(map[string]any)
	}
	c := read("root", nil)
	if c["run_step"] != float64(5) || c["run_step_end"] != float64(9) {
		t.Fatalf("steps = %v %v", c["run_step"], c["run_step_end"])
	}
	for _, k := range []string{"expire_at", "run_step", "run_step_end"} {
		if _, ok := c[k]; !ok {
			t.Fatalf("%s missing from %v", k, c)
		}
	}
	if c["expire_at"] != nil {
		t.Fatalf("expire_at without TTL = %v", c["expire_at"])
	}
	if _, ok := c["username"]; ok {
		t.Fatal("username returned without include_usernames")
	}
	if _, ok := c["total_storage_size_bytes"]; ok {
		t.Fatal("total_storage_size_bytes returned without include_total_storage_size")
	}
	c2 := read("c2", nil)
	if c2["run_step"] != float64(11) || c2["run_step_end"] != float64(12) {
		t.Fatalf("batch steps = %v %v", c2["run_step"], c2["run_step_end"])
	}

	// Storage sizes: the root carries the trace total, children none.
	c = read("root", map[string]any{"include_storage_size": true, "include_total_storage_size": true, "include_usernames": true})
	child := read("child", map[string]any{"include_storage_size": true, "include_total_storage_size": true})
	total, own, childOwn := c["total_storage_size_bytes"].(float64), c["storage_size_bytes"].(float64), child["storage_size_bytes"].(float64)
	if total != own+childOwn || childOwn == 0 {
		t.Fatalf("total %v, own %v, child %v", total, own, childOwn)
	}
	if _, ok := child["total_storage_size_bytes"]; ok {
		t.Fatalf("child has a trace total: %v", child)
	}
	if c["username"] != "alice" {
		t.Fatalf("username = %v", c["username"])
	}

	// expire_at follows the project's retention.
	e.post("/project/ttl_settings/update", map[string]any{"project_id": pid, "retention_days": 30})
	c = read("root", nil)
	if c["expire_at"] != started.Add(30*24*time.Hour).Format("2006-01-02T15:04:05.000000Z") {
		t.Fatalf("expire_at = %v", c["expire_at"])
	}

	// calls/stream_query honours the same flags; an id that is not a handle has no username.
	code, b := e.raw("/calls/stream_query", map[string]any{"project_id": pid, "include_usernames": true, "include_total_storage_size": true,
		"sort_by": []any{map[string]any{"field": "started_at", "direction": "asc"}}}, nil)
	if code != 200 {
		t.Fatalf("stream_query -> %d %s", code, b)
	}
	got := map[string]map[string]any{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		var m map[string]any
		_ = json.Unmarshal(sc.Bytes(), &m)
		got[m["id"].(string)] = m
	}
	if got["root"]["username"] != "alice" || got["root"]["total_storage_size_bytes"] == nil || got["root"]["expire_at"] == nil {
		t.Fatalf("root = %v", got["root"])
	}
	if _, ok := got["c2"]["username"]; ok {
		t.Fatalf("c2 username = %v", got["c2"]["username"])
	}
}
