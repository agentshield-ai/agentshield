package server

import (
	"net/url"
	"testing"
	"time"
)

func TestTagsAndAliases(t *testing.T) {
	e := newEnv(t)
	v1 := e.post("/obj/create", map[string]any{"obj": map[string]any{"project_id": pid, "object_id": "prompt", "val": map[string]any{"v": 1}}})["digest"].(string)
	v2 := e.post("/obj/create", map[string]any{"obj": map[string]any{"project_id": pid, "object_id": "prompt", "val": map[string]any{"v": 2}}})["digest"].(string)

	e.admJSON("PUT", "/objs/prompt/versions/"+v1+"/tags", "", map[string]any{"project_id": pid, "tags": []any{"reviewed", "team:search"}})
	e.admJSON("PUT", "/objs/prompt/versions/latest/tags", "", map[string]any{"project_id": pid, "tags": []any{"candidate"}})
	if code, _ := e.admDo("PUT", "/objs/prompt/versions/"+v1+"/tags", "", map[string]any{"project_id": pid, "tags": []any{"bad\ntag"}}); code != 400 {
		t.Fatalf("invalid tag -> %d", code)
	}
	o := e.post("/obj/read", map[string]any{"project_id": pid, "object_id": "prompt", "digest": v1})["obj"].(map[string]any)
	if tags := o["tags"].([]any); len(tags) != 2 || tags[0] != "reviewed" {
		t.Fatalf("tags = %v", o["tags"])
	}
	tags := e.admJSON("GET", "/tags?project_id="+url.QueryEscape(pid), "", nil)["tags"].([]any)
	if len(tags) != 3 {
		t.Fatalf("project tags = %v", tags)
	}
	e.post("/objs/prompt/versions/"+v1+"/tags/remove", map[string]any{"project_id": pid, "tags": []any{"reviewed"}})
	objs := e.post("/objs/query", map[string]any{"project_id": pid})["objs"].([]any)
	for _, x := range objs {
		m := x.(map[string]any)
		if m["digest"] == v1 && len(m["tags"].([]any)) != 1 {
			t.Fatalf("after remove: %v", m["tags"])
		}
		if m["digest"] == v2 && m["tags"].([]any)[0] != "candidate" {
			t.Fatalf("latest tags: %v", m["tags"])
		}
	}

	e.admJSON("PUT", "/objs/prompt/aliases", "", map[string]any{"project_id": pid, "digest": v1, "aliases": []any{"prod", "stable"}})
	if o := e.post("/obj/read", map[string]any{"project_id": pid, "object_id": "prompt", "digest": "prod"})["obj"].(map[string]any); o["digest"] != v1 {
		t.Fatalf("alias resolves to %v", o["digest"])
	}
	al := e.admJSON("GET", "/aliases?project_id="+url.QueryEscape(pid), "", nil)["aliases"].([]any)
	if len(al) != 2 || al[0] != "prod" {
		t.Fatalf("aliases = %v", al)
	}
	e.post("/objs/prompt/aliases/remove", map[string]any{"project_id": pid, "aliases": []any{"stable"}})
	if al := e.admJSON("GET", "/aliases?project_id="+url.QueryEscape(pid), "", nil)["aliases"].([]any); len(al) != 1 {
		t.Fatalf("aliases after remove = %v", al)
	}
	if code, _ := e.admDo("PUT", "/objs/prompt/aliases", "", map[string]any{"project_id": pid, "digest": v1, "aliases": []any{"latest"}}); code != 400 {
		t.Fatalf("reserved alias -> %d", code)
	}
}

func TestCompatExtraEndpoints(t *testing.T) {
	e := newEnv(t)
	start := time.Now().Add(-time.Hour)
	// Calls with usage: root -> child (LLM).
	root := "u1"
	batch := []any{
		map[string]any{"mode": "start", "req": map[string]any{"start": map[string]any{"project_id": pid, "id": root, "op_name": "agent",
			"started_at": start.Format(time.RFC3339Nano), "thread_id": "th1"}}},
		map[string]any{"mode": "start", "req": map[string]any{"start": map[string]any{"project_id": pid, "id": "u2", "trace_id": root, "parent_id": root,
			"op_name": "llm", "started_at": start.Format(time.RFC3339Nano)}}},
		map[string]any{"mode": "end", "req": map[string]any{"end": map[string]any{"project_id": pid, "id": "u2", "ended_at": start.Add(time.Second).Format(time.RFC3339Nano),
			"summary": map[string]any{"usage": map[string]any{"gpt-4o": map[string]any{"requests": 1, "prompt_tokens": 100, "completion_tokens": 20}}}}}},
	}
	e.post("/call/upsert_batch", map[string]any{"batch": batch})
	u := e.post("/calls/usage", map[string]any{"project_id": pid, "call_ids": []any{root}, "include_costs": true})
	item := u["call_usage"].(map[string]any)[root].(map[string]any)["gpt-4o"].(map[string]any)
	if item["prompt_tokens"] != float64(100) || item["total_tokens"] != float64(120) || item["prompt_tokens_total_cost"] == nil {
		t.Fatalf("calls/usage = %v", u)
	}
	if un := u["unfinished_call_ids"].([]any); len(un) != 1 || un[0] != root {
		t.Fatalf("root is still running: %v", un)
	}
	tu := e.post("/trace/usage", map[string]any{"project_id": pid, "filter": map[string]any{"trace_ids": []any{root}}})
	if len(tu["call_usage"].(map[string]any)) != 2 {
		t.Fatalf("trace/usage = %v", tu)
	}

	// threads/stream_query
	code, body := e.raw("/threads/stream_query", map[string]any{"project_id": pid}, nil)
	if th := jsonl(t, body); code != 200 || len(th) != 1 || th[0]["thread_id"] != "th1" {
		t.Fatalf("threads/stream_query = %d %s", code, body)
	}

	// Tables and files.
	tbl := e.post("/table/create", map[string]any{"table": map[string]any{"project_id": pid, "rows": []any{map[string]any{"a": 1}, map[string]any{"a": 2}}}})
	rows := tbl["row_digests"].([]any)
	d := e.post("/table/create_from_digests", map[string]any{"project_id": pid, "row_digests": []any{rows[1], rows[0], rows[1]}})["digest"].(string)
	if code, _ := e.raw("/table/create_from_digests", map[string]any{"project_id": pid, "row_digests": []any{"nope"}}, nil); code != 400 {
		t.Fatalf("unknown row digest -> %d", code)
	}
	if code, _ := e.raw("/table/create_from_digests", map[string]any{"project_id": pid, "row_digests": []any{rows[0]}, "expected_digest": "x"}, nil); code != 400 {
		t.Fatalf("bad expected digest -> %d", code)
	}
	st := e.post("/table/query_stats_batch", map[string]any{"project_id": pid, "digests": []any{d, tbl["digest"], "missing"}, "include_storage_size": true})
	ts := st["tables"].([]any)
	if len(ts) != 2 || ts[0].(map[string]any)["count"] != float64(3) || ts[0].(map[string]any)["storage_size_bytes"].(float64) <= 0 {
		t.Fatalf("query_stats_batch = %v", st)
	}
	if fs := e.post("/files/query_stats", map[string]any{"project_id": pid}); fs["total_size_bytes"] != float64(0) {
		t.Fatalf("files/query_stats = %v", fs)
	}

	// Feedback analytics.
	callRef := "weft:///" + pid + "/call/" + root
	scorer := "weft:///" + pid + "/object/judge:abc"
	monitor := "weft:///" + pid + "/object/mon:v1"
	for i, score := range []any{0.2, 0.8, 1.0} {
		e.post("/feedback/create", map[string]any{"project_id": pid, "ref": callRef, "feedback_type": "weft.scorer.judge",
			"payload": map[string]any{"output": map[string]any{"score": score, "passed": i > 0, "label": "ok"}}, "runnable_ref": scorer, "trigger_ref": monitor})
	}
	e.post("/feedback/create", map[string]any{"project_id": pid, "ref": callRef, "feedback_type": "weft.note", "payload": map[string]any{"note": "n"}})
	from := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	fs := e.post("/feedback/stats", map[string]any{"project_id": pid, "start": from, "feedback_type": "weft.scorer.judge", "granularity": 3600,
		"metrics": []any{map[string]any{"json_path": "output.score", "value_type": "numeric", "aggregations": []any{"avg", "max"}, "percentiles": []any{50}},
			map[string]any{"json_path": "output.passed", "value_type": "boolean"}}})
	ws := fs["window_stats"].(map[string]any)
	if avg := ws["output_score"].(map[string]any)["avg"].(float64); avg < 0.66 || avg > 0.67 {
		t.Fatalf("avg = %v (%v)", avg, fs)
	}
	if ws["output_score"].(map[string]any)["p50"] != 0.8 || ws["output_passed"].(map[string]any)["count_true"] != float64(2) {
		t.Fatalf("window stats = %v", ws)
	}
	total := 0.0
	for _, b := range fs["buckets"].([]any) {
		total += b.(map[string]any)["count"].(float64)
	}
	if total != 3 || fs["granularity"] != float64(3600) {
		t.Fatalf("buckets = %v", fs["buckets"])
	}
	ag := e.post("/feedback/aggregate", map[string]any{"project_id": pid, "after_ms": time.Now().Add(-time.Hour).UnixMilli(),
		"before_ms": time.Now().Add(time.Minute).UnixMilli(), "feedback_types": []any{"weft.scorer."}, "group_by": []any{"scorer_id"}, "monitor_ids": []any{"weft:///" + pid + "/object/mon:*"}})
	bs := ag["buckets"].([]any)
	if len(bs) != 1 {
		t.Fatalf("aggregate = %v", ag)
	}
	b0 := bs[0].(map[string]any)
	if b0["total_count"] != float64(3) || b0["group"].(map[string]any)["scorer_id"] != "judge" || b0["rating_counts"].(map[string]any)["score"] != float64(3) ||
		b0["tag_counts"].(map[string]any)["passed"] != float64(2) {
		t.Fatalf("aggregate bucket = %v", b0)
	}
	ps := e.post("/feedback/payload_schema", map[string]any{"project_id": pid, "start": from})
	types := map[string]string{}
	for _, p := range ps["paths"].([]any) {
		m := p.(map[string]any)
		types[m["json_path"].(string)] = m["value_type"].(string)
	}
	if types["output.score"] != "numeric" || types["output.passed"] != "boolean" || types["output.label"] != "categorical" || types["note"] != "categorical" {
		t.Fatalf("payload schema = %v", ps)
	}

	// Evaluation status (tagged union) for an unknown call.
	if s := e.post("/evaluations/status", map[string]any{"project_id": pid, "call_id": "nope"})["status"].(map[string]any); s["code"] != "not_found" {
		t.Fatalf("status = %v", s)
	}

	// Dataset lineage.
	dsTbl := e.post("/table/create", map[string]any{"table": map[string]any{"project_id": pid, "rows": []any{map[string]any{"q": "a"}, map[string]any{"q": "b"}}}})
	dsRows := dsTbl["row_digests"].([]any)
	dsDigest := e.post("/obj/create", map[string]any{"obj": map[string]any{"project_id": pid, "object_id": "golden", "val": map[string]any{"_type": "Dataset",
		"_class_name": "Dataset", "_bases": []any{"Object", "BaseModel"}, "rows": "weft:///" + pid + "/table/" + dsTbl["digest"].(string)}}})["digest"].(string)
	link := e.post("/dataset_sources/link", map[string]any{"project_id": pid, "dataset_object_id": "golden", "dataset_digest": "latest", "include_created_status": true,
		"links": []any{
			map[string]any{"row_digest": dsRows[0], "sources": []any{map[string]any{"source_id": "u2", "source_kind": "call", "source_trace_id": root}}, "link_metadata": map[string]any{"why": "good"}},
			map[string]any{"row_digest": dsRows[1], "sources": []any{map[string]any{"source_id": "u2", "source_kind": "call", "source_trace_id": root},
				map[string]any{"source_id": "conv-9", "source_kind": "conversation", "source_trace_id": "x"}}},
		}})
	entries := link["entries"].([]any)
	if len(entries) != 3 || entries[0].(map[string]any)["created"] != true {
		t.Fatalf("link = %v", link)
	}
	again := e.post("/dataset_sources/link", map[string]any{"project_id": pid, "dataset_object_id": "golden", "dataset_digest": dsDigest, "include_created_status": true,
		"links": []any{map[string]any{"row_digest": dsRows[0], "sources": []any{map[string]any{"source_id": "u2", "source_kind": "call", "source_trace_id": root}}}}})
	if en := again["entries"].([]any)[0].(map[string]any); en["created"] != false || en["link_id"] != entries[0].(map[string]any)["link_id"] {
		t.Fatalf("relink = %v", again)
	}
	q := e.post("/dataset_sources/query", map[string]any{"project_id": pid, "dataset_object_id": "golden", "source_kinds": []any{"call"}})
	links := q["links"].([]any)
	if len(links) != 2 || links[0].(map[string]any)["source_display_name"] != "llm" || links[0].(map[string]any)["link_metadata"].(map[string]any)["why"] != "good" {
		t.Fatalf("query = %v", q)
	}
	ms := e.post("/dataset_sources/source_datasets_query", map[string]any{"project_id": pid,
		"sources": []any{map[string]any{"source_id": "u2", "source_kind": "call", "source_trace_id": root}}})["memberships"].([]any)
	if len(ms) != 1 || ms[0].(map[string]any)["row_digests_total_count"] != float64(2) || ms[0].(map[string]any)["dataset_object_id"] != "golden" {
		t.Fatalf("memberships = %v", ms)
	}
	del := e.post("/dataset_sources/link_delete", map[string]any{"project_id": pid, "link_ids": []any{entries[0].(map[string]any)["link_id"], "missing"}})
	de := del["entries"].([]any)
	if de[0].(map[string]any)["deleted"] != true || de[1].(map[string]any)["deleted"] != false {
		t.Fatalf("link_delete = %v", del)
	}
	if n := len(e.post("/dataset_sources/query", map[string]any{"project_id": pid, "dataset_object_id": "golden"})["links"].([]any)); n != 2 {
		t.Fatalf("links after delete = %d", n)
	}
	if n := len(e.post("/dataset_sources/query", map[string]any{"project_id": pid, "dataset_object_id": "golden", "include_deleted": true})["links"].([]any)); n != 3 {
		t.Fatalf("links incl. deleted = %d", n)
	}
}

// Costs on a call with descendants must count the roll-up once.
func TestRootCostsNotDoubled(t *testing.T) {
	e := newEnv(t)
	e.post("/call/upsert_batch", map[string]any{"batch": []any{
		map[string]any{"mode": "start", "req": map[string]any{"start": map[string]any{"project_id": pid, "id": "r", "op_name": "agent", "started_at": "2026-01-01T00:00:00Z"}}},
		map[string]any{"mode": "start", "req": map[string]any{"start": map[string]any{"project_id": pid, "id": "c", "trace_id": "r", "parent_id": "r", "op_name": "llm", "started_at": "2026-01-01T00:00:00Z"}}},
		map[string]any{"mode": "end", "req": map[string]any{"end": map[string]any{"project_id": pid, "id": "c", "ended_at": "2026-01-01T00:00:01Z",
			"summary": map[string]any{"usage": map[string]any{"gpt-4o": map[string]any{"requests": 1, "prompt_tokens": 1000, "completion_tokens": 0}}}}}},
		map[string]any{"mode": "end", "req": map[string]any{"end": map[string]any{"project_id": pid, "id": "r", "ended_at": "2026-01-01T00:00:02Z"}}},
	}})
	c := e.post("/call/read", map[string]any{"project_id": pid, "id": "r", "include_costs": true})["call"].(map[string]any)
	cost := c["summary"].(map[string]any)["weft"].(map[string]any)["costs"].(map[string]any)["gpt-4o"].(map[string]any)
	if cost["prompt_tokens"] != float64(1000) || cost["requests"] != float64(1) {
		t.Fatalf("root costs double-count the roll-up: %v", cost)
	}
}
