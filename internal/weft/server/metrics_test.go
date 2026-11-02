package server

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// seedMetricCalls writes calls with known latency, usage, attributes and
// feedback so aggregates can be checked exactly.
//
//	op      model        latency  tokens(p/c)  env    score  error
//	answer  gpt-4o       100ms    10/5         prod   1      no
//	answer  gpt-4o       300ms    20/10        prod   0      yes
//	answer  claude-3-5   200ms    30/15        dev    1      no
//	search  -            400ms    -            dev    -      no
func seedMetricCalls(t *testing.T, e *testEnv, base time.Time) {
	t.Helper()
	type spec struct {
		op, model, env string
		latency        int
		prompt, compl  int
		score          *bool
		fail           bool
		offset         time.Duration
	}
	yes, no := true, false
	specs := []spec{
		{"answer", "gpt-4o", "prod", 100, 10, 5, &yes, false, 0},
		{"answer", "gpt-4o", "prod", 300, 20, 10, &no, true, 10 * time.Minute},
		{"answer", "claude-3-5-sonnet", "dev", 200, 30, 15, &yes, false, 70 * time.Minute},
		{"search", "", "dev", 400, 0, 0, nil, false, 75 * time.Minute},
	}
	for i, s := range specs {
		id := fmt.Sprintf("m%d", i)
		start := base.Add(s.offset)
		e.post("/call/start", map[string]any{"start": map[string]any{
			"project_id": pid, "id": id, "trace_id": "t" + id, "op_name": s.op, "started_at": start.Format(time.RFC3339Nano),
			"attributes": map[string]any{"env": s.env}, "inputs": map[string]any{"n": i + 1},
		}})
		end := map[string]any{"project_id": pid, "id": id, "ended_at": start.Add(time.Duration(s.latency) * time.Millisecond).Format(time.RFC3339Nano),
			"output": map[string]any{"len": float64(10 * (i + 1))}}
		if s.model != "" {
			end["summary"] = map[string]any{"usage": map[string]any{s.model: map[string]any{
				"requests": 1, "prompt_tokens": s.prompt, "completion_tokens": s.compl, "total_tokens": s.prompt + s.compl}}}
		}
		if s.fail {
			end["exception"] = "boom"
		}
		e.post("/call/end", map[string]any{"end": end})
		if s.score != nil {
			e.post("/feedback/create", map[string]any{"project_id": pid, "weave_ref": "weft:///" + pid + "/call/" + id,
				"feedback_type": "wandb.runnable.judge", "payload": map[string]any{"output": map[string]any{"correct": *s.score}}})
		}
	}
}

func num(t *testing.T, v any) float64 {
	t.Helper()
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("expected number, got %#v", v)
	}
	return f
}

func groupsByName(t *testing.T, res map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, g := range res["groups"].([]any) {
		gm := g.(map[string]any)
		name := "<null>"
		if s, ok := gm["group"].(string); ok {
			name = s
		}
		out[name] = gm
	}
	return out
}

func TestCallsMetricsAggregations(t *testing.T) {
	e := newEnv(t)
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	seedMetricCalls(t, e, base)
	res := e.post("/calls/metrics", map[string]any{
		"project_id": pid, "bucket_seconds": 3600,
		"metrics": []any{
			map[string]any{"name": "calls", "agg": "count"},
			map[string]any{"name": "lat_avg", "field": "latency_ms", "agg": "avg"},
			map[string]any{"name": "lat_p50", "field": "latency_ms", "agg": "p50"},
			map[string]any{"name": "lat_p99", "field": "latency_ms", "agg": "p99"},
			map[string]any{"name": "lat_min", "field": "latency_ms", "agg": "min"},
			map[string]any{"name": "lat_max", "field": "latency_ms", "agg": "max"},
			map[string]any{"name": "tokens", "field": "total_tokens", "agg": "sum"},
			map[string]any{"name": "prompt", "field": "prompt_tokens", "agg": "sum"},
			map[string]any{"name": "cost", "field": "cost", "agg": "sum"},
			map[string]any{"name": "err", "agg": "error_rate"},
			map[string]any{"name": "out_len", "field": "output.len", "agg": "sum"},
			map[string]any{"name": "in_n", "field": "inputs.n", "agg": "max"},
			map[string]any{"name": "correct", "field": "feedback.wandb.runnable.judge.output.correct", "agg": "avg"},
		},
	})
	tot := res["totals"].(map[string]any)
	want := map[string]float64{
		"calls": 4, "lat_avg": 250, "lat_p50": 200, "lat_p99": 400, "lat_min": 100, "lat_max": 400,
		"tokens": 90, "prompt": 60, "err": 0.25, "out_len": 100, "in_n": 4, "correct": 2.0 / 3,
	}
	for k, w := range want {
		if got := num(t, tot[k]); math.Abs(got-w) > 1e-9 {
			t.Errorf("total %s = %v, want %v", k, got, w)
		}
	}
	if num(t, tot["cost"]) <= 0 {
		t.Errorf("expected positive cost, got %v", tot["cost"])
	}
	if res["bucket_seconds"].(float64) != 3600 {
		t.Fatalf("bucket_seconds = %v", res["bucket_seconds"])
	}
	buckets := res["buckets"].([]any)
	if len(buckets) != 2 {
		t.Fatalf("want 2 hourly buckets, got %d: %v", len(buckets), buckets)
	}
	g := res["groups"].([]any)
	if len(g) != 1 || g[0].(map[string]any)["group"] != nil {
		t.Fatalf("ungrouped request should return one null group: %v", g)
	}
	series := g[0].(map[string]any)["series"].(map[string]any)
	calls := series["calls"].([]any)
	if num(t, calls[0]) != 2 || num(t, calls[1]) != 2 {
		t.Fatalf("calls per bucket = %v", calls)
	}
	tok := series["tokens"].([]any)
	if num(t, tok[0]) != 45 || num(t, tok[1]) != 45 {
		t.Fatalf("tokens per bucket = %v", tok)
	}
	lat := series["lat_avg"].([]any)
	if num(t, lat[0]) != 200 || num(t, lat[1]) != 300 {
		t.Fatalf("latency avg per bucket = %v", lat)
	}
}

func TestCallsMetricsGroupBy(t *testing.T) {
	e := newEnv(t)
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	seedMetricCalls(t, e, base)
	metrics := []any{
		map[string]any{"name": "calls", "agg": "count"},
		map[string]any{"name": "tokens", "field": "total_tokens", "agg": "sum"},
		map[string]any{"name": "err", "agg": "error_rate"},
	}
	byOp := groupsByName(t, e.post("/calls/metrics", map[string]any{"project_id": pid, "metrics": metrics, "group_by": "op_name"}))
	if len(byOp) != 2 || num(t, byOp["answer"]["count"]) != 3 || num(t, byOp["search"]["count"]) != 1 {
		t.Fatalf("by op = %v", byOp)
	}
	if num(t, byOp["answer"]["totals"].(map[string]any)["tokens"]) != 90 {
		t.Fatalf("answer tokens = %v", byOp["answer"]["totals"])
	}
	if e := num(t, byOp["answer"]["totals"].(map[string]any)["err"]); math.Abs(e-1.0/3) > 1e-9 {
		t.Fatalf("answer error rate = %v", e)
	}

	byModel := groupsByName(t, e.post("/calls/metrics", map[string]any{"project_id": pid, "metrics": metrics, "group_by": "model"}))
	if num(t, byModel["gpt-4o"]["count"]) != 2 || num(t, byModel["claude-3-5-sonnet"]["count"]) != 1 || num(t, byModel["<null>"]["count"]) != 1 {
		t.Fatalf("by model = %v", byModel)
	}

	byEnv := groupsByName(t, e.post("/calls/metrics", map[string]any{"project_id": pid, "metrics": metrics, "group_by": "attributes.env"}))
	if num(t, byEnv["prod"]["count"]) != 2 || num(t, byEnv["dev"]["count"]) != 2 {
		t.Fatalf("by attributes.env = %v", byEnv)
	}

	byStatus := groupsByName(t, e.post("/calls/metrics", map[string]any{"project_id": pid, "metrics": metrics, "group_by": "status"}))
	if num(t, byStatus["error"]["count"]) != 1 || num(t, byStatus["success"]["count"]) != 3 {
		t.Fatalf("by status = %v", byStatus)
	}

	// max_groups folds the tail into an "other" group.
	top := e.post("/calls/metrics", map[string]any{"project_id": pid, "metrics": metrics, "group_by": "inputs.n", "max_groups": 2})
	gs := top["groups"].([]any)
	if len(gs) != 3 || gs[2].(map[string]any)["other"] != true || num(t, gs[2].(map[string]any)["count"]) != 2 {
		t.Fatalf("max_groups = %v", gs)
	}

	// Filters and time range apply.
	filtered := e.post("/calls/metrics", map[string]any{"project_id": pid, "metrics": metrics, "filter": map[string]any{"op_names": []any{"answer"}},
		"start": base.Add(time.Hour).Format(time.RFC3339), "end": base.Add(2 * time.Hour).Format(time.RFC3339)})
	if num(t, filtered["totals"].(map[string]any)["calls"]) != 1 {
		t.Fatalf("filtered totals = %v", filtered["totals"])
	}
	if len(filtered["buckets"].([]any)) == 0 {
		t.Fatal("explicit range should produce buckets")
	}
}

func TestCallsMetricsValidation(t *testing.T) {
	e := newEnv(t)
	for _, body := range []map[string]any{
		{"project_id": pid, "metrics": []any{map[string]any{"agg": "median", "field": "latency_ms"}}},
		{"project_id": pid, "metrics": []any{map[string]any{"agg": "avg"}}},
		{"project_id": pid, "metrics": []any{map[string]any{"agg": "avg", "field": "nope.x"}}},
		{"project_id": pid, "group_by": "bogus"},
		{"project_id": pid, "metrics": []any{map[string]any{"name": "a", "agg": "count"}, map[string]any{"name": "a", "agg": "count"}}},
	} {
		if code, out := e.raw("/calls/metrics", body, nil); code != 400 {
			t.Errorf("%v -> %d %s, want 400", body, code, out)
		}
	}
	// Empty project: zero counts, no buckets.
	res := e.post("/calls/metrics", map[string]any{"project_id": pid})
	if num(t, res["totals"].(map[string]any)["count"]) != 0 || len(res["buckets"].([]any)) != 0 {
		t.Fatalf("empty = %v", res)
	}
}
