package store

import (
	"context"
	"database/sql"
	"math"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// Price is a per-token price for one model (Weave's CostQueryOutput).
//
// Cache prices follow Weave's cost fields: cache_read_input_token_cost and
// cache_creation_input_token_cost price the cache-read and cache-write
// subsets of the prompt tokens. A nil cache price means "not priced
// separately": those tokens are billed at prompt_token_cost.
type Price struct {
	ID                          string   `json:"id"`
	LLMID                       string   `json:"llm_id"`
	PromptTokenCost             float64  `json:"prompt_token_cost"`
	CompletionTokenCost         float64  `json:"completion_token_cost"`
	CacheReadInputTokenCost     *float64 `json:"cache_read_input_token_cost"`
	CacheCreationInputTokenCost *float64 `json:"cache_creation_input_token_cost"`
	PromptTokenCostUnit         string   `json:"prompt_token_cost_unit"`
	CompletionTokenCostUnit     string   `json:"completion_token_cost_unit"`
	EffectiveDate               string   `json:"effective_date"`
	ProviderID                  string   `json:"provider_id"`
	PricingLevel                string   `json:"pricing_level"`

	effectiveUS int64
}

// CacheReadRate is the price of a cache-read token (the prompt price when
// cache reads are not priced separately).
func (p Price) CacheReadRate() float64 {
	if p.CacheReadInputTokenCost != nil {
		return *p.CacheReadInputTokenCost
	}
	return p.PromptTokenCost
}

// CacheCreationRate is the price of a cache-write token (the prompt price
// when cache writes are not priced separately).
func (p Price) CacheCreationRate() float64 {
	if p.CacheCreationInputTokenCost != nil {
		return *p.CacheCreationInputTokenCost
	}
	return p.PromptTokenCost
}

// TokenCounts is one model's usage split into the token classes Weave prices.
// Prompt is the gross prompt count and includes CacheRead and CacheCreation
// (Weave's convention, and OpenAI's, Gemini's and the OTel GenAI
// conventions'); Uncached is what remains at the full input rate.
type TokenCounts struct {
	Requests, Prompt, Completion, Total float64
	CacheRead, CacheCreation            float64
}

// Uncached returns the prompt tokens that were neither read from nor written
// to a cache.
func (t TokenCounts) Uncached() float64 {
	return math.Max(t.Prompt-t.CacheRead-t.CacheCreation, 0)
}

// UsageCounts reads a normalised usage entry (see NormalizeUsage). Entries
// are normalised to gross prompts when parsed; if a stored roll-up still has
// more cache tokens than prompt tokens (data written before cache pricing),
// the prompt is taken to exclude them, as NormalizeUsage does.
func UsageCounts(m map[string]float64) TokenCounts {
	p, c, tot := UsageTokens(m)
	t := TokenCounts{Requests: m["requests"], Prompt: p, Completion: c, Total: tot,
		CacheRead: math.Max(m["cache_read_input_tokens"], 0), CacheCreation: math.Max(m["cache_creation_input_tokens"], 0)}
	if cache := t.CacheRead + t.CacheCreation; cache > t.Prompt {
		t.Prompt += cache
		if t.Total < t.Prompt+t.Completion {
			t.Total += cache
		}
	}
	return t
}

// CostBreakdown is the USD cost of one model's usage. Input is the whole
// input-side cost: Uncached + CacheRead + CacheCreation. The cache parts are
// contained in Input, so Total = Input + Completion; never add them again.
type CostBreakdown struct {
	Uncached, CacheRead, CacheCreation, Completion float64
}

// Input is the cost of all prompt tokens, cached or not.
func (c CostBreakdown) Input() float64 { return c.Uncached + c.CacheRead + c.CacheCreation }

// Total is the cost of the usage.
func (c CostBreakdown) Total() float64 { return c.Input() + c.Completion }

// Add accumulates o into c.
func (c *CostBreakdown) Add(o CostBreakdown) {
	c.Uncached += o.Uncached
	c.CacheRead += o.CacheRead
	c.CacheCreation += o.CacheCreation
	c.Completion += o.Completion
}

// Cost prices token counts:
//
//	(prompt - cache_read - cache_creation) * prompt_token_cost
//	+ cache_read * cache_read_input_token_cost
//	+ cache_creation * cache_creation_input_token_cost
//	+ completion * completion_token_cost
func (p Price) Cost(t TokenCounts) CostBreakdown {
	return CostBreakdown{Uncached: t.Uncached() * p.PromptTokenCost, CacheRead: t.CacheRead * p.CacheReadRate(),
		CacheCreation: t.CacheCreation * p.CacheCreationRate(), Completion: t.Completion * p.CompletionTokenCost}
}

// DefaultPrice describes a built-in model price (USD per token). CacheRead and
// CacheWrite are the provider's cache-read and cache-write (5-minute TTL for
// Anthropic) prices; 0 means the provider does not price them separately and
// those tokens cost Prompt.
type DefaultPrice struct {
	LLMID      string
	Provider   string
	Prompt     float64
	Completion float64
	CacheRead  float64
	CacheWrite float64
}

func (d DefaultPrice) price() Price {
	p := Price{ID: "default:" + d.LLMID, LLMID: d.LLMID, PromptTokenCost: d.Prompt, CompletionTokenCost: d.Completion,
		PromptTokenCostUnit: "USD", CompletionTokenCostUnit: "USD", EffectiveDate: "2024-01-01T00:00:00.000000Z",
		ProviderID: d.Provider, PricingLevel: "default"}
	if d.CacheRead > 0 {
		v := d.CacheRead
		p.CacheReadInputTokenCost = &v
	}
	if d.CacheWrite > 0 {
		v := d.CacheWrite
		p.CacheCreationInputTokenCost = &v
	}
	return p
}

const perM = 1e-6

// DefaultPrices are list prices for common models, matched by longest prefix.
// Columns: model, provider, input, output, cache read, cache write (USD per
// token; a 0 cache price falls back to the input price).
//
// Sources (checked 2026-11-02): Anthropic https://platform.claude.com/docs/en/about-claude/pricing
// (cache write = 5-minute TTL, 1.25x input; cache read 0.1x input, 0.05x on
// Opus 5.5 and Sonnet 5.5, 0.025x on Fable 5.1 and Mythos 5.1); OpenAI
// https://developers.openai.com/api/docs/pricing ("cached input"; OpenAI does
// not charge for cache writes); Google https://ai.google.dev/gemini-api/docs/pricing
// ("context caching", text, prompts <= 200K tokens; cache storage is billed
// per hour and is not a token price). Retired models keep their last
// published prices. Mistral and DeepSeek publish no cache prices for these ids.
var DefaultPrices = []DefaultPrice{
	{"gpt-5", "openai", 1.25 * perM, 10 * perM, 0.125 * perM, 0},
	{"gpt-5-mini", "openai", 0.25 * perM, 2 * perM, 0.025 * perM, 0},
	{"gpt-5-nano", "openai", 0.05 * perM, 0.4 * perM, 0.005 * perM, 0},
	{"gpt-5-pro", "openai", 15 * perM, 120 * perM, 0, 0},
	{"gpt-5.1", "openai", 1.25 * perM, 10 * perM, 0.125 * perM, 0},
	{"gpt-5.2", "openai", 1.75 * perM, 14 * perM, 0.175 * perM, 0},
	{"gpt-5.4", "openai", 2.5 * perM, 15 * perM, 0.25 * perM, 0},
	{"gpt-5.4-mini", "openai", 0.75 * perM, 4.5 * perM, 0.075 * perM, 0},
	{"gpt-5.4-nano", "openai", 0.2 * perM, 1.25 * perM, 0.02 * perM, 0},
	{"gpt-5.5", "openai", 5 * perM, 30 * perM, 0.5 * perM, 0},
	{"gpt-4.1", "openai", 2 * perM, 8 * perM, 0.5 * perM, 0},
	{"gpt-4.1-mini", "openai", 0.4 * perM, 1.6 * perM, 0.1 * perM, 0},
	{"gpt-4.1-nano", "openai", 0.1 * perM, 0.4 * perM, 0.025 * perM, 0},
	{"gpt-4o", "openai", 2.5 * perM, 10 * perM, 1.25 * perM, 0},
	{"gpt-4o-mini", "openai", 0.15 * perM, 0.6 * perM, 0.075 * perM, 0},
	{"gpt-4-turbo", "openai", 10 * perM, 30 * perM, 0, 0},
	{"gpt-4", "openai", 30 * perM, 60 * perM, 0, 0},
	{"gpt-3.5-turbo", "openai", 0.5 * perM, 1.5 * perM, 0, 0},
	{"o1", "openai", 15 * perM, 60 * perM, 7.5 * perM, 0},
	{"o1-mini", "openai", 1.1 * perM, 4.4 * perM, 0.55 * perM, 0},
	{"o3", "openai", 2 * perM, 8 * perM, 0.5 * perM, 0},
	{"o3-mini", "openai", 1.1 * perM, 4.4 * perM, 0.55 * perM, 0},
	{"o4-mini", "openai", 1.1 * perM, 4.4 * perM, 0.275 * perM, 0},
	{"text-embedding-3-small", "openai", 0.02 * perM, 0, 0, 0},
	{"text-embedding-3-large", "openai", 0.13 * perM, 0, 0, 0},
	{"claude-3-haiku", "anthropic", 0.25 * perM, 1.25 * perM, 0.03 * perM, 0.3 * perM},
	{"claude-3-5-haiku", "anthropic", 0.8 * perM, 4 * perM, 0.08 * perM, 1 * perM},
	{"claude-haiku-4-5", "anthropic", 1 * perM, 5 * perM, 0.1 * perM, 1.25 * perM},
	{"claude-3-sonnet", "anthropic", 3 * perM, 15 * perM, 0.3 * perM, 3.75 * perM},
	{"claude-3-5-sonnet", "anthropic", 3 * perM, 15 * perM, 0.3 * perM, 3.75 * perM},
	{"claude-3-7-sonnet", "anthropic", 3 * perM, 15 * perM, 0.3 * perM, 3.75 * perM},
	{"claude-sonnet-4", "anthropic", 3 * perM, 15 * perM, 0.3 * perM, 3.75 * perM},
	{"claude-sonnet-4-5", "anthropic", 3 * perM, 15 * perM, 0.3 * perM, 3.75 * perM},
	{"claude-3-opus", "anthropic", 15 * perM, 75 * perM, 1.5 * perM, 18.75 * perM},
	{"claude-opus-4", "anthropic", 15 * perM, 75 * perM, 1.5 * perM, 18.75 * perM},
	{"claude-opus-4-1", "anthropic", 15 * perM, 75 * perM, 1.5 * perM, 18.75 * perM},
	{"claude-opus-4-5", "anthropic", 5 * perM, 25 * perM, 0.5 * perM, 6.25 * perM},
	// Claude 4.6+ and the Claude 5 family (Anthropic first-party list prices;
	// Haiku 5.5 is the price for prompts up to 100K tokens).
	{"claude-opus-4-6", "anthropic", 5 * perM, 25 * perM, 0.5 * perM, 6.25 * perM},
	{"claude-opus-4-7", "anthropic", 5 * perM, 25 * perM, 0.5 * perM, 6.25 * perM},
	{"claude-opus-4-8", "anthropic", 5 * perM, 25 * perM, 0.5 * perM, 6.25 * perM},
	{"claude-sonnet-4-6", "anthropic", 3 * perM, 15 * perM, 0.3 * perM, 3.75 * perM},
	{"claude-opus-5", "anthropic", 5 * perM, 25 * perM, 0.5 * perM, 6.25 * perM},
	{"claude-opus-5-5", "anthropic", 4 * perM, 20 * perM, 0.2 * perM, 5 * perM},
	{"claude-sonnet-5", "anthropic", 2 * perM, 10 * perM, 0.2 * perM, 2.5 * perM},
	{"claude-sonnet-5-5", "anthropic", 2 * perM, 10 * perM, 0.1 * perM, 2.5 * perM},
	{"claude-haiku-5-5", "anthropic", 0.1 * perM, 0.5 * perM, 0.01 * perM, 0.125 * perM},
	{"claude-fable-5", "anthropic", 10 * perM, 50 * perM, 1 * perM, 12.5 * perM},
	{"claude-fable-5-1", "anthropic", 10 * perM, 50 * perM, 0.25 * perM, 12.5 * perM},
	{"claude-mythos-5", "anthropic", 10 * perM, 50 * perM, 1 * perM, 12.5 * perM},
	{"claude-mythos-5-1", "anthropic", 10 * perM, 50 * perM, 0.25 * perM, 12.5 * perM},
	{"gemini-1.5-flash", "google", 0.075 * perM, 0.3 * perM, 0, 0},
	{"gemini-1.5-pro", "google", 1.25 * perM, 5 * perM, 0, 0},
	{"gemini-2.0-flash", "google", 0.1 * perM, 0.4 * perM, 0.025 * perM, 0},
	{"gemini-2.5-flash", "google", 0.3 * perM, 2.5 * perM, 0.03 * perM, 0},
	{"gemini-2.5-flash-lite", "google", 0.1 * perM, 0.4 * perM, 0.01 * perM, 0},
	{"gemini-2.5-pro", "google", 1.25 * perM, 10 * perM, 0.125 * perM, 0},
	{"gemini-3-flash", "google", 0.5 * perM, 3 * perM, 0.05 * perM, 0},
	{"gemini-3.1-pro", "google", 2 * perM, 12 * perM, 0.2 * perM, 0},
	{"gemini-3.1-flash-lite", "google", 0.25 * perM, 1.5 * perM, 0.025 * perM, 0},
	{"mistral-large", "mistral", 2 * perM, 6 * perM, 0, 0},
	{"mistral-small", "mistral", 0.2 * perM, 0.6 * perM, 0, 0},
	{"deepseek-chat", "deepseek", 0.27 * perM, 1.1 * perM, 0, 0},
	{"deepseek-reasoner", "deepseek", 0.55 * perM, 2.19 * perM, 0, 0},
	{"mock/echo", "mock", 0, 0, 0, 0},
	{"mock/json", "mock", 0, 0, 0, 0},
}

// LookupDefaultPrice finds the built-in price for a model by longest prefix,
// ignoring a leading "provider/" segment.
func LookupDefaultPrice(model string) (DefaultPrice, bool) {
	m := strings.ToLower(model)
	candidates := []string{m}
	if i := strings.IndexByte(m, '/'); i >= 0 {
		candidates = append(candidates, m[i+1:])
	}
	var best DefaultPrice
	found := false
	for _, cand := range candidates {
		for _, p := range DefaultPrices {
			if strings.HasPrefix(cand, p.LLMID) && (!found || len(p.LLMID) > len(best.LLMID)) {
				best, found = p, true
			}
		}
	}
	return best, found
}

// PriceBook resolves prices for a project: project overrides first, then defaults.
type PriceBook struct {
	project map[string][]Price // sorted by effective date desc
}

// LoadPriceBook loads project prices.
func (s *Store) LoadPriceBook(ctx context.Context, projectID string) (*PriceBook, error) {
	prices, err := s.projectPrices(ctx, projectID)
	if err != nil {
		return nil, err
	}
	pb := &PriceBook{project: map[string][]Price{}}
	for _, p := range prices {
		pb.project[p.LLMID] = append(pb.project[p.LLMID], p)
	}
	for k := range pb.project {
		ps := pb.project[k]
		sort.Slice(ps, func(i, j int) bool { return ps[i].effectiveUS > ps[j].effectiveUS })
	}
	return pb, nil
}

func (pb *PriceBook) lookup(model string, atUS int64) (Price, bool) {
	for _, key := range []string{model, strings.ToLower(model)} {
		for _, p := range pb.project[key] {
			if p.effectiveUS <= atUS {
				return p, true
			}
		}
	}
	if d, ok := LookupDefaultPrice(model); ok {
		p := d.price()
		p.ID = ""
		return p, true
	}
	return Price{}, false
}

// Lookup returns the price in effect for model at atUS: the newest project
// price with an effective date at or before atUS, else the built-in price.
func (pb *PriceBook) Lookup(model string, atUS int64) (Price, bool) { return pb.lookup(model, atUS) }

// ModelCost prices one model's usage entry.
func (pb *PriceBook) ModelCost(model string, m map[string]float64, atUS int64) (CostBreakdown, Price, bool) {
	p, ok := pb.lookup(model, atUS)
	if !ok {
		return CostBreakdown{}, Price{}, false
	}
	return p.Cost(UsageCounts(m)), p, true
}

// Costs computes per-model costs for usage at time atUS, in the shape of
// summary.weave.costs. prompt_tokens_total_cost is the cost of all prompt
// tokens; cache_read_input_tokens_total_cost and
// cache_creation_input_tokens_total_cost are the parts of it spent on cache
// reads and writes, so the call's cost is prompt_tokens_total_cost +
// completion_tokens_total_cost.
func (pb *PriceBook) Costs(usage Contribution, atUS int64) map[string]any {
	out := map[string]any{}
	for model, m := range usage.Usage {
		p, ok := pb.lookup(model, atUS)
		if !ok {
			continue
		}
		t := UsageCounts(m)
		c := p.Cost(t)
		out[model] = map[string]any{
			"requests":                               numberValue(t.Requests),
			"prompt_tokens":                          numberValue(t.Prompt),
			"completion_tokens":                      numberValue(t.Completion),
			"total_tokens":                           numberValue(t.Total),
			"cache_read_input_tokens":                numberValue(t.CacheRead),
			"cache_creation_input_tokens":            numberValue(t.CacheCreation),
			"prompt_token_cost":                      p.PromptTokenCost,
			"completion_token_cost":                  p.CompletionTokenCost,
			"cache_read_input_token_cost":            p.CacheReadRate(),
			"cache_creation_input_token_cost":        p.CacheCreationRate(),
			"prompt_tokens_total_cost":               c.Input(),
			"completion_tokens_total_cost":           c.Completion,
			"cache_read_input_tokens_total_cost":     c.CacheRead,
			"cache_creation_input_tokens_total_cost": c.CacheCreation,
			"prompt_token_cost_unit":                 p.PromptTokenCostUnit,
			"completion_token_cost_unit":             p.CompletionTokenCostUnit,
			"effective_date":                         p.EffectiveDate,
			"provider_id":                            p.ProviderID,
			"pricing_level":                          p.PricingLevel,
		}
	}
	return out
}

// CostBreakdown sums the priced cost of usage across models; ok is false
// when no model in usage has a price.
func (pb *PriceBook) CostBreakdown(usage Contribution, atUS int64) (total CostBreakdown, ok bool) {
	for model, m := range usage.Usage {
		if c, _, priced := pb.ModelCost(model, m, atUS); priced {
			total.Add(c)
			ok = true
		}
	}
	return total, ok
}

// TotalCost sums the cost of usage in USD.
func (pb *PriceBook) TotalCost(usage Contribution, atUS int64) float64 {
	c, _ := pb.CostBreakdown(usage, atUS)
	return c.Total()
}

// CostInput is one entry of /cost/create.
// Omitted cache prices are stored as NULL and bill cache tokens at the prompt
// price (Weave's API defaults them to 0; see docs/weft/API.md).
type CostInput struct {
	PromptTokenCost             float64  `json:"prompt_token_cost"`
	CompletionTokenCost         float64  `json:"completion_token_cost"`
	CacheReadInputTokenCost     *float64 `json:"cache_read_input_token_cost"`
	CacheCreationInputTokenCost *float64 `json:"cache_creation_input_token_cost"`
	PromptTokenCostUnit         string   `json:"prompt_token_cost_unit"`
	CompletionTokenCostUnit     string   `json:"completion_token_cost_unit"`
	EffectiveDate               string   `json:"effective_date"`
	ProviderID                  string   `json:"provider_id"`
}

// CreateCosts stores project prices and returns [id, llm_id] pairs.
func (s *Store) CreateCosts(ctx context.Context, projectID string, costs map[string]CostInput) ([][2]string, error) {
	var out [][2]string
	keys := make([]string, 0, len(costs))
	for k := range costs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	err := s.withTx(ctx, func(tx *txT) error {
		if err := s.touchProject(ctx, tx, projectID); err != nil {
			return err
		}
		for _, llm := range keys {
			c := costs[llm]
			if strings.TrimSpace(llm) == "" || len(llm) > 256 {
				return Invalidf("invalid llm id %q", llm)
			}
			if c.PromptTokenCost < 0 || c.CompletionTokenCost < 0 || (c.CacheReadInputTokenCost != nil && *c.CacheReadInputTokenCost < 0) ||
				(c.CacheCreationInputTokenCost != nil && *c.CacheCreationInputTokenCost < 0) {
				return Invalidf("costs must be non-negative")
			}
			eff := nowMicros()
			if c.EffectiveDate != "" {
				v, err := ParseTime(c.EffectiveDate)
				if err != nil {
					return err
				}
				eff = v
			}
			unitP, unitC := c.PromptTokenCostUnit, c.CompletionTokenCostUnit
			if unitP == "" {
				unitP = "USD"
			}
			if unitC == "" {
				unitC = "USD"
			}
			provider := c.ProviderID
			if provider == "" {
				if d, ok := LookupDefaultPrice(llm); ok {
					provider = d.Provider
				} else {
					provider = "default"
				}
			}
			id := uuid.NewString()
			if _, err := tx.ExecContext(ctx, `INSERT INTO llm_prices(id, project_id, llm_id, provider_id, prompt_token_cost, completion_token_cost,
				cache_read_input_token_cost, cache_creation_input_token_cost,
				prompt_token_cost_unit, completion_token_cost_unit, effective_date, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
				id, projectID, llm, provider, c.PromptTokenCost, c.CompletionTokenCost, nullFloat(c.CacheReadInputTokenCost),
				nullFloat(c.CacheCreationInputTokenCost), unitP, unitC, eff, nowMicros()); err != nil {
				return err
			}
			out = append(out, [2]string{id, llm})
		}
		return nil
	})
	return out, err
}

func priceField(path string) (string, []any, bool, error) {
	switch path {
	case "id", "llm_id", "provider_id", "prompt_token_cost", "completion_token_cost", "prompt_token_cost_unit", "completion_token_cost_unit",
		"cache_read_input_token_cost", "cache_creation_input_token_cost":
		return "p." + path, nil, false, nil
	case "pricing_level_id":
		return "p.project_id", nil, false, nil
	case "effective_date":
		return "p.effective_date", nil, true, nil
	case "pricing_level":
		return "'project'", nil, false, nil
	}
	return "", nil, false, unknownField(path)
}

func (s *Store) projectPrices(ctx context.Context, projectID string) ([]Price, error) {
	return s.QueryCosts(ctx, projectID, nil, 0, 0)
}

// QueryCosts lists project prices matching query.
func (s *Store) QueryCosts(ctx context.Context, projectID string, query any, limit, offset int) ([]Price, error) {
	where := "p.project_id = ?"
	args := []any{projectID}
	if query != nil {
		qs, qa, err := compileQuery(query, priceField)
		if err != nil {
			return nil, err
		}
		if qs != "" {
			where += " AND (" + qs + ")"
			args = append(args, qa...)
		}
	}
	if limit <= 0 {
		limit = 10000
	}
	args = append(args, limit, offset)
	rows, err := s.r.QueryContext(ctx, `SELECT p.id, p.llm_id, p.provider_id, p.prompt_token_cost, p.completion_token_cost,
		p.cache_read_input_token_cost, p.cache_creation_input_token_cost, p.prompt_token_cost_unit, p.completion_token_cost_unit, p.effective_date FROM llm_prices p WHERE `+where+
		` ORDER BY p.llm_id, p.effective_date DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Price{}
	for rows.Next() {
		var p Price
		var cr, cw sql.NullFloat64
		if err := rows.Scan(&p.ID, &p.LLMID, &p.ProviderID, &p.PromptTokenCost, &p.CompletionTokenCost, &cr, &cw,
			&p.PromptTokenCostUnit, &p.CompletionTokenCostUnit, &p.effectiveUS); err != nil {
			return nil, err
		}
		if cr.Valid {
			p.CacheReadInputTokenCost = &cr.Float64
		}
		if cw.Valid {
			p.CacheCreationInputTokenCost = &cw.Float64
		}
		p.EffectiveDate = FormatMicros(p.effectiveUS)
		p.PricingLevel = "project"
		out = append(out, p)
	}
	return out, rows.Err()
}

// DefaultPriceList returns built-in prices in API form.
func DefaultPriceList() []Price {
	out := make([]Price, 0, len(DefaultPrices))
	for _, d := range DefaultPrices {
		out = append(out, d.price())
	}
	return out
}

// PurgeCosts deletes project prices by id and/or query.
func (s *Store) PurgeCosts(ctx context.Context, projectID string, ids []string, query any) error {
	return s.withTx(ctx, func(tx *txT) error {
		if len(ids) > 0 {
			q, a := inList("id", ids)
			if _, err := tx.ExecContext(ctx, "DELETE FROM llm_prices WHERE project_id = ? AND "+q, append([]any{projectID}, a...)...); err != nil {
				return err
			}
		}
		if query != nil {
			qs, qa, err := compileQuery(query, priceField)
			if err != nil {
				return err
			}
			if qs != "" {
				if _, err := tx.ExecContext(ctx, "DELETE FROM llm_prices AS p WHERE p.project_id = ? AND ("+qs+")", append([]any{projectID}, qa...)...); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func nullFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

// llm_prices gained Weave's cache-read and cache-write prices; NULL means
// "bill at the prompt price".
func init() {
	registerMigration(func(db *sql.DB) error {
		for _, col := range []string{"cache_read_input_token_cost", "cache_creation_input_token_cost"} {
			ok, err := HasColumn(db, "llm_prices", col)
			if err != nil {
				return err
			}
			if !ok {
				if _, err := db.Exec(`ALTER TABLE llm_prices ADD COLUMN ` + col + ` REAL`); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
