package store

import (
	"context"
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

// TestCachedTokenPricing covers the usage shapes of each provider and the
// audit's worked example (A11): 10,000 input tokens of which 9,000 are cache
// reads, plus 100 output, on claude-sonnet-4-5 costs $0.0072 (cache reads at the cache price),
// not $0.0315.
func TestCachedTokenPricing(t *testing.T) {
	cases := []struct {
		name, model          string
		usage                map[string]any
		prompt, cr, cw, comp float64
		cost                 float64
	}{
		{"audit A11: gross input with cache reads (Weft SDK convention)", "claude-sonnet-4-5",
			map[string]any{"requests": 1, "input_tokens": 10000, "output_tokens": 100, "cache_read_input_tokens": 9000},
			10000, 9000, 0, 100, 1000*3e-6 + 9000*0.3e-6 + 100*15e-6},
		{"anthropic native: input_tokens excludes cache reads", "claude-sonnet-4-5",
			map[string]any{"input_tokens": 1000, "output_tokens": 100, "cache_read_input_tokens": 9000, "cache_creation_input_tokens": 0},
			10000, 9000, 0, 100, 0.0072},
		{"anthropic native: cache write", "claude-sonnet-4-5",
			map[string]any{"input_tokens": 50, "output_tokens": 10, "cache_creation_input_tokens": 2000,
				"cache_creation": map[string]any{"ephemeral_5m_input_tokens": 2000}, "service_tier": "standard"},
			2050, 0, 2000, 10, 50*3e-6 + 2000*3.75e-6 + 10*15e-6},
		{"anthropic via an SDK integration (gross) with cache write", "claude-sonnet-4-5",
			map[string]any{"input_tokens": 2050, "output_tokens": 10, "cache_creation_input_tokens": 2000},
			2050, 0, 2000, 10, 50*3e-6 + 2000*3.75e-6 + 10*15e-6},
		{"anthropic native: reads and writes", "claude-opus-5-5",
			map[string]any{"input_tokens": 20, "output_tokens": 300, "cache_read_input_tokens": 40000, "cache_creation_input_tokens": 1500},
			41520, 40000, 1500, 300, 20*4e-6 + 40000*0.2e-6 + 1500*5e-6 + 300*20e-6},
		{"openai chat completions: prompt_tokens_details.cached_tokens", "gpt-4o-2024-08-06",
			map[string]any{"prompt_tokens": 2000, "completion_tokens": 100, "total_tokens": 2100,
				"prompt_tokens_details": map[string]any{"cached_tokens": 1536, "audio_tokens": 0}},
			2000, 1536, 0, 100, 464*2.5e-6 + 1536*1.25e-6 + 100*10e-6},
		{"openai responses: input_tokens_details.cached_tokens", "openai/gpt-4o",
			map[string]any{"input_tokens": 2000, "output_tokens": 100, "input_tokens_details": map[string]any{"cached_tokens": 1536},
				"output_tokens_details": map[string]any{"reasoning_tokens": 50}},
			2000, 1536, 0, 100, 464*2.5e-6 + 1536*1.25e-6 + 100*10e-6},
		{"openai-compatible cache_write_tokens", "claude-sonnet-4-5",
			map[string]any{"prompt_tokens": 2050, "completion_tokens": 10, "prompt_tokens_details": map[string]any{"cached_tokens": 0, "cache_write_tokens": 2000}},
			2050, 0, 2000, 10, 50*3e-6 + 2000*3.75e-6 + 10*15e-6},
		{"gemini usage_metadata (snake case)", "gemini-2.5-flash",
			map[string]any{"prompt_token_count": 10000, "candidates_token_count": 200, "total_token_count": 10200, "cached_content_token_count": 8000},
			10000, 8000, 0, 200, 2000*0.3e-6 + 8000*0.03e-6 + 200*2.5e-6},
		{"vertex usage_metadata (camel case)", "gemini/gemini-2.5-pro",
			map[string]any{"promptTokenCount": 10000, "candidatesTokenCount": 200, "totalTokenCount": 10200, "cachedContentTokenCount": 8000,
				"thoughtsTokenCount": 30},
			10000, 8000, 0, 200, 2000*1.25e-6 + 8000*0.125e-6 + 200*10e-6},
		{"gemini via an SDK integration (canonical)", "gemini-2.5-flash",
			map[string]any{"prompt_tokens": 10000, "completion_tokens": 200, "total_tokens": 10200, "cache_read_input_tokens": 8000},
			10000, 8000, 0, 200, 2000*0.3e-6 + 8000*0.03e-6 + 200*2.5e-6},
		{"otel genai semconv as mapped by otlp (input includes cache)", "claude-sonnet-4-5",
			map[string]any{"prompt_tokens": 10000, "completion_tokens": 100, "total_tokens": 10100, "cache_read_input_tokens": 9000,
				"cache_creation_input_tokens": 500},
			10000, 9000, 500, 100, 500*3e-6 + 9000*0.3e-6 + 500*3.75e-6 + 100*15e-6},
		{"bedrock converse: inputTokens excludes cache", "claude-sonnet-4-5",
			map[string]any{"inputTokens": 1000, "outputTokens": 100, "totalTokens": 10100, "cacheReadInputTokens": 9000},
			10000, 9000, 0, 100, 0.0072},
		{"no caching", "gpt-4o",
			map[string]any{"prompt_tokens": 1000, "completion_tokens": 100},
			1000, 0, 0, 100, 1000*2.5e-6 + 100*10e-6},
		{"model without cache prices bills cached tokens at the input price", "mistral-large-latest",
			map[string]any{"prompt_tokens": 1000, "completion_tokens": 0, "prompt_tokens_details": map[string]any{"cached_tokens": 500}},
			1000, 500, 0, 0, 1000 * 2e-6},
	}
	pb := &PriceBook{project: map[string][]Price{}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := NormalizeUsage(c.usage)
			tc := UsageCounts(m)
			if tc.Prompt != c.prompt || tc.CacheRead != c.cr || tc.CacheCreation != c.cw || tc.Completion != c.comp {
				t.Fatalf("counts = %+v (normalised %v)", tc, m)
			}
			cost, _, ok := pb.ModelCost(c.model, m, 0)
			if !ok {
				t.Fatalf("no price for %s", c.model)
			}
			if !approx(cost.Total(), c.cost) {
				t.Fatalf("cost = %.10f, want %.10f (%+v)", cost.Total(), c.cost, cost)
			}
			if !approx(cost.Input()+cost.Completion, cost.Total()) || cost.CacheRead > cost.Input() {
				t.Fatalf("breakdown inconsistent: %+v", cost)
			}
		})
	}
	if !approx(1000*3e-6+9000*0.3e-6+100*15e-6, 0.0072) {
		t.Fatal("worked example arithmetic")
	}
}

func TestNormalizeUsageKeepsCanonicalFields(t *testing.T) {
	m := NormalizeUsage(map[string]any{"prompt_tokens": 100, "cache_read_input_tokens": 40,
		"prompt_tokens_details": map[string]any{"cached_tokens": 90}, "completion_tokens_details": map[string]any{"reasoning_tokens": 7}})
	if m["cache_read_input_tokens"] != 40 || m["reasoning_tokens"] != 7 || m["prompt_tokens"] != 100 {
		t.Fatalf("normalised = %v", m)
	}
	if _, ok := m["prompt_tokens_details"]; ok {
		t.Fatal("nested objects must not become numeric fields")
	}
	// Gemini names are renamed, not duplicated.
	g := NormalizeUsage(map[string]any{"prompt_token_count": 10, "candidates_token_count": 2, "total_token_count": 12})
	if len(g) != 3 || g["prompt_tokens"] != 10 || g["completion_tokens"] != 2 || g["total_tokens"] != 12 {
		t.Fatalf("gemini = %v", g)
	}
}

func TestSummaryCostsCacheFields(t *testing.T) {
	pb := &PriceBook{project: map[string][]Price{}}
	var u Contribution
	u.addSummaryUsage(map[string]any{"usage": map[string]any{"claude-sonnet-4-5": map[string]any{
		"requests": 1, "input_tokens": 10000, "output_tokens": 100, "cache_read_input_tokens": 9000}}})
	c := pb.Costs(u, 0)["claude-sonnet-4-5"].(map[string]any)
	want := map[string]float64{
		"prompt_token_cost": 3e-6, "completion_token_cost": 15e-6, "cache_read_input_token_cost": 0.3e-6, "cache_creation_input_token_cost": 3.75e-6,
		"prompt_tokens_total_cost": 0.003 + 0.0027, "completion_tokens_total_cost": 0.0015,
		"cache_read_input_tokens_total_cost": 0.0027, "cache_creation_input_tokens_total_cost": 0,
	}
	for k, v := range want {
		if got, _ := ToFloat(c[k]); !approx(got, v) {
			t.Errorf("%s = %v, want %v", k, c[k], v)
		}
	}
	if c["cache_read_input_tokens"] != int64(9000) || c["prompt_tokens"] != int64(10000) {
		t.Fatalf("token fields = %v", c)
	}
	p, _ := ToFloat(c["prompt_tokens_total_cost"])
	q, _ := ToFloat(c["completion_tokens_total_cost"])
	if !approx(p+q, 0.0072) || !approx(pb.TotalCost(u, 0), 0.0072) {
		t.Fatalf("total = %v / %v", p+q, pb.TotalCost(u, 0))
	}
}

func TestProjectCachePrices(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	f := func(v float64) *float64 { return &v }
	_, err := s.CreateCosts(ctx, proj, map[string]CostInput{
		"my-model":      {PromptTokenCost: 2e-6, CompletionTokenCost: 8e-6, CacheReadInputTokenCost: f(0.5e-6), CacheCreationInputTokenCost: f(2.5e-6)},
		"no-cache-rate": {PromptTokenCost: 1e-6, CompletionTokenCost: 1e-6},
		"free-cache":    {PromptTokenCost: 1e-6, CompletionTokenCost: 1e-6, CacheReadInputTokenCost: f(0)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateCosts(ctx, proj, map[string]CostInput{"bad": {PromptTokenCost: 1, CacheReadInputTokenCost: f(-1)}}); err == nil {
		t.Fatal("negative cache price accepted")
	}
	ps, err := s.QueryCosts(ctx, proj, map[string]any{"$expr": map[string]any{"$gt": []any{
		map[string]any{"$getField": "cache_read_input_token_cost"}, map[string]any{"$literal": 0}}}}, 0, 0)
	if err != nil || len(ps) != 1 || ps[0].LLMID != "my-model" || *ps[0].CacheCreationInputTokenCost != 2.5e-6 {
		t.Fatalf("query by cache price = %+v, %v", ps, err)
	}
	all, _ := s.QueryCosts(ctx, proj, nil, 0, 0)
	for _, p := range all {
		if p.LLMID == "no-cache-rate" && (p.CacheReadInputTokenCost != nil || p.CacheCreationInputTokenCost != nil) {
			t.Fatalf("omitted cache prices should be null: %+v", p)
		}
	}
	pb, err := s.LoadPriceBook(ctx, proj)
	if err != nil {
		t.Fatal(err)
	}
	m := NormalizeUsage(map[string]any{"prompt_tokens": 1000, "completion_tokens": 10, "cache_read_input_tokens": 600, "cache_creation_input_tokens": 100})
	at := nowMicros() + 1
	for model, want := range map[string]float64{
		"my-model":      300*2e-6 + 600*0.5e-6 + 100*2.5e-6 + 10*8e-6,
		"no-cache-rate": 1000*1e-6 + 10*1e-6, // cache tokens at the prompt price
		"free-cache":    300*1e-6 + 0 + 100*1e-6 + 10*1e-6,
	} {
		c, _, ok := pb.ModelCost(model, m, at)
		if !ok || !approx(c.Total(), want) {
			t.Errorf("%s: %+v (want %v)", model, c, want)
		}
	}
	// A project price overrides the default for a known model, cache rates included.
	if _, err := s.CreateCosts(ctx, proj, map[string]CostInput{"claude-sonnet-4-5": {PromptTokenCost: 3e-6, CompletionTokenCost: 15e-6}}); err != nil {
		t.Fatal(err)
	}
	pb, _ = s.LoadPriceBook(ctx, proj)
	c, p, _ := pb.ModelCost("claude-sonnet-4-5", NormalizeUsage(map[string]any{"input_tokens": 10000, "output_tokens": 100, "cache_read_input_tokens": 9000}), nowMicros()+1)
	if p.PricingLevel != "project" || !approx(c.Total(), 10000*3e-6+100*15e-6) {
		t.Fatalf("project override without cache rates = %+v %+v", p, c)
	}
}

func TestDefaultPricesCacheRates(t *testing.T) {
	for _, d := range DefaultPrices {
		if d.CacheRead < 0 || d.CacheWrite < 0 || (d.CacheRead > 0 && d.CacheRead > d.Prompt) {
			t.Errorf("%s: cache read %v vs prompt %v", d.LLMID, d.CacheRead, d.Prompt)
		}
		if d.Provider == "anthropic" && d.LLMID != "claude-3-haiku" { // Claude 3 Haiku: published $0.30 / $0.03
			// Anthropic: 5-minute cache writes cost 1.25x input; reads 0.1x, 0.05x or 0.025x.
			if !approx(d.CacheWrite, d.Prompt*1.25) {
				t.Errorf("%s: cache write %v, want %v", d.LLMID, d.CacheWrite, d.Prompt*1.25)
			}
			r := d.CacheRead / d.Prompt
			if !(approx(r, 0.1) || approx(r, 0.05) || approx(r, 0.025)) {
				t.Errorf("%s: cache read ratio %v", d.LLMID, r)
			}
		}
		if d.Provider == "openai" && d.CacheWrite != 0 {
			t.Errorf("%s: OpenAI does not price cache writes", d.LLMID)
		}
	}
	for model, want := range map[string]float64{"claude-sonnet-4-5": 0.3e-6, "claude-opus-5-5": 0.2e-6, "claude-sonnet-5-5": 0.1e-6,
		"claude-fable-5-1": 0.25e-6, "claude-fable-5": 1e-6, "claude-haiku-4-5": 0.1e-6, "gpt-5": 0.125e-6, "gpt-4.1-mini": 0.1e-6,
		"gemini-2.5-flash": 0.03e-6, "gemini-2.5-flash-lite": 0.01e-6} {
		d, ok := LookupDefaultPrice(model)
		if !ok || d.LLMID != model || !approx(d.CacheRead, want) {
			t.Errorf("%s: %+v", model, d)
		}
	}
}
