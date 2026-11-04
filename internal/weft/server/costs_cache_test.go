package server

import (
	"math"
	"testing"
	"time"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestCachedCostsConsistent checks that cached-token pricing reaches every
// cost surface and that roll-ups count each LLM call once: per-call
// summary.weft.costs, /api/v1/spans/stats (both shapes),
// /api/v1/spans/metrics, /api/v1/spans/sessions and the Agents views.
func TestCachedCostsConsistent(t *testing.T) {
	e := newEnv(t)
	const sonnet = "claude-sonnet-4-5"
	usage := func(m map[string]any) map[string]any { return map[string]any{"usage": m} }
	startCall := func(id, parent string, extra map[string]any) map[string]any {
		st := map[string]any{"project_id": pid, "id": id, "trace_id": "tr", "name": "op-" + id, "started_at": "2026-03-01T10:00:00Z", "inputs": map[string]any{}}
		if parent != "" {
			st["parent_id"] = parent
		}
		for k, v := range extra {
			st[k] = v
		}
		return map[string]any{"start": st}
	}
	endCall := func(id string, summary map[string]any) map[string]any {
		return map[string]any{"end": map[string]any{"project_id": pid, "id": id,
			"ended_at": "2026-03-01T10:00:01Z", "summary": summary}}
	}
	// Children end before the root, as OTLP exporters send them.
	e.post("/api/v1/spans/batch", map[string]any{"items": []any{
		startCall("root", "", map[string]any{"session_id": "th-1"}),
		startCall("a", "root", nil), startCall("b", "root", nil), startCall("c", "root", nil),
		// Anthropic-native usage: input_tokens excludes the 9,000 cache reads.
		endCall("a", usage(map[string]any{sonnet: map[string]any{"requests": 1, "input_tokens": 1000, "output_tokens": 100, "cache_read_input_tokens": 9000}})),
		// The audit's example in gross form: 10,000 input of which 9,000 cache reads.
		endCall("b", usage(map[string]any{sonnet: map[string]any{"requests": 1, "input_tokens": 10000, "output_tokens": 100, "cache_read_input_tokens": 9000}})),
		// OpenAI: cached_tokens nested in prompt_tokens_details and included in prompt_tokens.
		endCall("c", usage(map[string]any{"gpt-4o": map[string]any{"requests": 1, "prompt_tokens": 2000, "completion_tokens": 100, "total_tokens": 2100,
			"prompt_tokens_details": map[string]any{"cached_tokens": 1536}}})),
		endCall("root", map[string]any{}),
	}})
	const perSonnet = 0.0072
	gpt := 464*2.5e-6 + 1536*1.25e-6 + 100*10e-6
	total := 2*perSonnet + gpt

	// Per-call summary.weft.costs.
	b := e.span("b", "costs")
	bc := b["summary"].(map[string]any)["weft"].(map[string]any)["costs"].(map[string]any)[sonnet].(map[string]any)
	if !near(bc["prompt_tokens_total_cost"].(float64)+bc["completion_tokens_total_cost"].(float64), perSonnet) ||
		!near(bc["cache_read_input_tokens_total_cost"].(float64), 9000*0.3e-6) || bc["cache_read_input_token_cost"].(float64) != 0.3e-6 {
		t.Fatalf("audit example costs = %v", bc)
	}
	root := e.span("root", "costs")
	costs := root["summary"].(map[string]any)["weft"].(map[string]any)["costs"].(map[string]any)
	rs := costs[sonnet].(map[string]any)
	if rs["prompt_tokens"].(float64) != 20000 || rs["cache_read_input_tokens"].(float64) != 18000 ||
		!near(rs["prompt_tokens_total_cost"].(float64)+rs["completion_tokens_total_cost"].(float64), 2*perSonnet) {
		t.Fatalf("root sonnet costs = %v", rs)
	}
	rg := costs["gpt-4o"].(map[string]any)
	if rg["cache_read_input_tokens"].(float64) != 1536 || !near(rg["prompt_tokens_total_cost"].(float64)+rg["completion_tokens_total_cost"].(float64), gpt) {
		t.Fatalf("root gpt costs = %v", rg)
	}

	// Span stats, time series: each LLM call counted once.
	st := e.post("/api/v1/spans/stats", map[string]any{"project_id": pid, "start": "2026-03-01T00:00:00Z", "end": "2026-03-02T00:00:00Z", "bucket_seconds": 3600})
	if c := st["totals"].(map[string]any)["cost_usd"].(float64); !near(c, total) {
		t.Fatalf("stats cost_usd = %v, want %v", c, total)
	}
	// Span stats, metric buckets.
	ws := e.post("/api/v1/spans/stats", map[string]any{"project_id": pid, "start": "2026-03-01T00:00:00Z", "end": "2026-03-02T00:00:00Z", "granularity": 86400,
		"usage_metrics": []any{map[string]any{"metric": "total_cost"}, map[string]any{"metric": "input_cost"}, map[string]any{"metric": "output_cost"},
			map[string]any{"metric": "cache_read_input_tokens"}}})
	var wt, wi, wo, wcr float64
	for _, x := range ws["usage_buckets"].([]any) {
		m := x.(map[string]any)
		wt += m["sum_total_cost"].(float64)
		wi += m["sum_input_cost"].(float64)
		wo += m["sum_output_cost"].(float64)
		wcr += m["sum_cache_read_input_tokens"].(float64)
	}
	if !near(wt, total) || !near(wi+wo, total) || wcr != 18000+1536 {
		t.Fatalf("bucket stats total=%v input=%v output=%v cache_read=%v, want %v", wt, wi, wo, wcr, total)
	}

	// Span metrics (dashboards).
	mr := e.post("/api/v1/spans/metrics", map[string]any{"project_id": pid, "metrics": []any{map[string]any{"name": "cost", "field": "cost", "agg": "sum"}}})
	if c := mr["totals"].(map[string]any)["cost"].(float64); !near(c, total) {
		t.Fatalf("metrics cost = %v, want %v", c, total)
	}

	// Sessions on spans.
	th := e.post("/api/v1/spans/sessions", map[string]any{"project_id": pid})
	var threadCost float64
	for _, x := range th["items"].([]any) {
		if m := x.(map[string]any); m["session_id"] == "th-1" {
			threadCost = m["cost_usd"].(float64)
		}
	}
	if !near(threadCost, total) {
		t.Fatalf("thread cost = %v, want %v (%v)", threadCost, total, th)
	}
}

// TestAgentSpanCachedCosts sends GenAI semconv spans (cache counts included
// in gen_ai.usage.input_tokens) and checks the Agents views.
func TestAgentSpanCachedCosts(t *testing.T) {
	e := newEnv(t)
	base := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	tr := hexID("cache", 32)
	spans := []testSpan{
		{tr, hexID("llm1", 16), hexID("ag", 16), "chat claude-sonnet-4-5", 10, 300, map[string]any{
			"gen_ai.operation.name": "chat", "gen_ai.request.model": "claude-sonnet-4-5", "gen_ai.provider.name": "anthropic",
			"gen_ai.usage.input_tokens": 10000, "gen_ai.usage.output_tokens": 100,
			"gen_ai.usage.cache_read.input_tokens": 9000, "gen_ai.usage.cache_creation.input_tokens": 0}, ""},
		{tr, hexID("llm2", 16), hexID("ag", 16), "chat claude-sonnet-4-5", 400, 300, map[string]any{
			"gen_ai.operation.name": "chat", "gen_ai.request.model": "claude-sonnet-4-5", "gen_ai.provider.name": "anthropic",
			"gen_ai.usage.input_tokens": 2050, "gen_ai.usage.output_tokens": 10, "gen_ai.usage.cache_creation.input_tokens": 2000}, ""},
		{tr, hexID("ag", 16), "", "invoke_agent cacher", 0, 1000, map[string]any{
			"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "cacher", "gen_ai.conversation.id": "conv-cache"}, ""},
	}
	if code, out := e.raw("/agents/otel/v1/traces", otlpBody(base, spans), map[string]string{"project_id": pid}); code != 200 {
		t.Fatalf("otlp: %d %s", code, out)
	}
	llm1 := 1000*3e-6 + 9000*0.3e-6 + 100*15e-6
	llm2 := 50*3e-6 + 2000*3.75e-6 + 10*15e-6
	res := e.post("/agents/spans/query", map[string]any{"project_id": pid, "include_costs": true, "limit": 50})
	var sum float64
	for _, x := range res["spans"].([]any) {
		sp := x.(map[string]any)
		if sp["operation_name"] != "chat" {
			continue
		}
		in, _ := sp["input_cost_usd"].(float64)
		out, _ := sp["output_cost_usd"].(float64)
		tot, _ := sp["total_cost_usd"].(float64)
		cr, _ := sp["cache_read_cost_usd"].(float64)
		cw, _ := sp["cache_creation_cost_usd"].(float64)
		if !near(in+out, tot) || cr > in || cw > in {
			t.Fatalf("span costs inconsistent: %v", sp)
		}
		if sp["cache_read_input_tokens"] != nil && !near(cr, 9000*0.3e-6) {
			t.Fatalf("cache read cost = %v", sp)
		}
		sum += tot
	}
	if !near(sum, llm1+llm2) {
		t.Fatalf("span cost sum = %v, want %v (%v)", sum, llm1+llm2, res)
	}
	ag := e.post("/agents/query", map[string]any{"project_id": pid, "include_costs": true})
	var agentCost float64
	for _, x := range ag["agents"].([]any) {
		if a := x.(map[string]any); a["agent_name"] == "cacher" {
			agentCost, _ = a["total_cost_usd"].(float64)
		}
	}
	if !near(agentCost, llm1+llm2) {
		t.Fatalf("agent total_cost_usd = %v, want %v (%v)", agentCost, llm1+llm2, ag)
	}
	chat := e.post("/agents/conversations/chat", map[string]any{"project_id": pid, "conversation_id": "conv-cache", "include_costs": true})
	if c, _ := chat["total_cost_usd"].(float64); !near(c, llm1+llm2) {
		t.Fatalf("conversation total_cost_usd = %v, want %v", chat["total_cost_usd"], llm1+llm2)
	}
}
