package server

import (
	"net/http"
	"testing"
)

// seedStatsCalls records, in hour 10:00-11:00 of 2026-01-01: an agent root
// with two LLM children (gpt-4o and claude), and a failed root in 11:00-12:00.
func seedStatsCalls(e *testEnv) {
	start := func(id, parent, at string) map[string]any {
		s := map[string]any{"project_id": pid, "id": id, "op_name": "op-" + id, "started_at": at}
		if parent != "" {
			s["parent_id"], s["trace_id"] = parent, "r1"
		}
		return map[string]any{"mode": "start", "req": map[string]any{"start": s}}
	}
	end := func(id, at string, usage map[string]any, exc string) map[string]any {
		en := map[string]any{"project_id": pid, "id": id, "ended_at": at}
		if usage != nil {
			en["summary"] = map[string]any{"usage": usage}
		}
		if exc != "" {
			en["exception"] = exc
		}
		return map[string]any{"mode": "end", "req": map[string]any{"end": en}}
	}
	e.post("/call/upsert_batch", map[string]any{"batch": []any{
		start("r1", "", "2026-01-01T10:00:00Z"),
		start("c1", "r1", "2026-01-01T10:00:01Z"),
		end("c1", "2026-01-01T10:00:02Z", map[string]any{"gpt-4o": map[string]any{"requests": 1, "prompt_tokens": 1000, "completion_tokens": 100}}, ""),
		start("c2", "r1", "2026-01-01T10:00:03Z"),
		end("c2", "2026-01-01T10:00:04Z", map[string]any{"claude-3-5-haiku": map[string]any{"requests": 1, "input_tokens": 50, "output_tokens": 5, "cache_read_input_tokens": 7}}, ""),
		end("r1", "2026-01-01T10:00:05Z", nil, ""),
		start("r2", "", "2026-01-01T11:30:00Z"),
		end("r2", "2026-01-01T11:30:00.250Z", nil, "boom"),
	}})
}

func TestCallsStatsMetricBuckets(t *testing.T) {
	e := newEnv(t)
	seedStatsCalls(e)
	res := e.post("/calls/stats", map[string]any{"project_id": pid, "start": "2026-01-01T10:00:00Z", "end": "2026-01-01T12:00:00Z",
		"granularity": 3600,
		"usage_metrics": []any{map[string]any{"metric": "input_tokens", "aggregations": []any{"sum"}},
			map[string]any{"metric": "total_cost", "aggregations": []any{"sum"}},
			map[string]any{"metric": "cache_read_input_tokens"}},
		"call_metrics": []any{map[string]any{"metric": "latency_ms", "aggregations": []any{"avg", "max"}, "percentiles": []any{50, 99.5}},
			map[string]any{"metric": "call_count"}, map[string]any{"metric": "error_count", "aggregations": []any{"sum", "count_true"}}},
	})
	for _, k := range []string{"start", "end", "granularity", "timezone", "usage_buckets", "call_buckets"} {
		if _, ok := res[k]; !ok {
			t.Fatalf("missing %s: %v", k, res)
		}
	}
	if res["granularity"].(float64) != 3600 || res["timezone"] != "UTC" || res["buckets"] != nil {
		t.Fatalf("stats header = %v", res)
	}
	cb := res["call_buckets"].([]any)
	if len(cb) != 2 {
		t.Fatalf("call buckets = %v", cb)
	}
	b0, b1 := cb[0].(map[string]any), cb[1].(map[string]any)
	if b0["timestamp"] != "2026-01-01T10:00:00Z" || b0["count"].(float64) != 3 || b0["sum_call_count"].(float64) != 3 ||
		b0["sum_error_count"].(float64) != 0 || b0["max_latency_ms"].(float64) != 5000 {
		t.Fatalf("bucket 0 = %v", b0)
	}
	if _, ok := b0["p50_latency_ms"]; !ok {
		t.Fatalf("percentile key missing: %v", b0)
	}
	if _, ok := b0["p99.5_latency_ms"]; !ok {
		t.Fatalf("fractional percentile key missing: %v", b0)
	}
	if b1["sum_error_count"].(float64) != 1 || b1["count_true_error_count"].(float64) != 1 || b1["avg_latency_ms"].(float64) != 250 {
		t.Fatalf("bucket 1 = %v", b1)
	}
	ub := res["usage_buckets"].([]any)
	if len(ub) != 4 { // 2 buckets x 2 models
		t.Fatalf("usage buckets = %v", ub)
	}
	byKey := map[string]map[string]any{}
	for _, u := range ub {
		um := u.(map[string]any)
		byKey[um["timestamp"].(string)+"|"+um["model"].(string)] = um
	}
	g := byKey["2026-01-01T10:00:00Z|gpt-4o"]
	if g == nil || g["sum_input_tokens"].(float64) != 1000 || g["count"].(float64) != 1 {
		t.Fatalf("gpt-4o bucket = %v", g)
	}
	if c, _ := g["sum_total_cost"].(float64); c < 0.0034 || c > 0.0036 { // 1000*2.5e-6 + 100*1e-5
		t.Fatalf("gpt-4o cost = %v", g["sum_total_cost"])
	}
	cl := byKey["2026-01-01T10:00:00Z|claude-3-5-haiku"]
	if cl == nil || cl["sum_input_tokens"].(float64) != 50 || cl["sum_cache_read_input_tokens"].(float64) != 7 {
		t.Fatalf("claude bucket = %v", cl)
	}
	empty := byKey["2026-01-01T11:00:00Z|gpt-4o"]
	if empty == nil || empty["sum_input_tokens"].(float64) != 0 || empty["count"].(float64) != 0 {
		t.Fatalf("empty bucket = %v", empty)
	}

	// Timezone alignment and a validation error.
	tz := e.post("/calls/stats", map[string]any{"project_id": pid, "start": "2026-01-01T00:00:00Z", "end": "2026-01-03T00:00:00Z",
		"granularity": 86400, "timezone": "America/New_York", "call_metrics": []any{map[string]any{"metric": "call_count"}}})
	first := tz["call_buckets"].([]any)[0].(map[string]any)["timestamp"]
	if first != "2025-12-31T00:00:00-05:00" {
		t.Fatalf("tz-aligned first bucket = %v", first)
	}
	if code, out := e.raw("/calls/stats", map[string]any{"project_id": pid, "start": "2026-01-01T00:00:00Z",
		"call_metrics": []any{map[string]any{"metric": "tokens"}}}, nil); code != 400 {
		t.Fatalf("unknown metric -> %d %s", code, out)
	}

	// Weft's own request keeps Weft's shape.
	weft := e.post("/calls/stats", map[string]any{"project_id": pid, "start": "2026-01-01T10:00:00Z", "end": "2026-01-01T12:00:00Z", "bucket_seconds": 3600})
	if weft["buckets"] == nil || weft["usage_buckets"] != nil {
		t.Fatalf("Weft-shape stats = %v", weft)
	}
}

func TestQueryStatsHasMoreAndFeedbackTotalCount(t *testing.T) {
	e := newEnv(t)
	seedStatsCalls(e)
	all := e.post("/calls/query_stats", map[string]any{"project_id": pid})
	if all["count"].(float64) != 4 || all["has_more"] != false {
		t.Fatalf("query_stats = %v", all)
	}
	lim := e.post("/calls/query_stats", map[string]any{"project_id": pid, "limit": 2})
	if lim["count"].(float64) != 2 || lim["has_more"] != true {
		t.Fatalf("limited query_stats = %v", lim)
	}
	for i, id := range []string{"r1", "r1", "r2"} {
		e.post("/feedback/create", map[string]any{"project_id": pid, "ref": "weft:///" + pid + "/call/" + id,
			"feedback_type": "weft.reaction", "payload": map[string]any{"emoji": []string{"👍", "👎", "👍"}[i]}})
	}
	fq := e.post("/feedback/query", map[string]any{"project_id": pid, "limit": 1})
	if len(fq["result"].([]any)) != 1 || fq["total_count"].(float64) != 3 {
		t.Fatalf("feedback query = %v", fq)
	}
	filtered := e.post("/feedback/query", map[string]any{"project_id": pid, "query": map[string]any{"$expr": map[string]any{
		"$eq": []any{map[string]any{"$getField": "ref"}, map[string]any{"$literal": "weft:///" + pid + "/call/r2"}}}}})
	if filtered["total_count"].(float64) != 1 {
		t.Fatalf("filtered feedback total = %v", filtered)
	}
}

func TestVersionIsPublicJSON(t *testing.T) {
	e := newEnv(t, "k1")
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/version", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("/version -> %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if v := e.post("/version", nil); v["version"] != Version {
		// GET via the helper (nil body) with the key.
		t.Fatalf("version = %v", v)
	}
}
