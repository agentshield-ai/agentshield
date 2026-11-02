package store

import (
	"context"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// Price is a per-token price for one model.
type Price struct {
	ID                      string  `json:"id"`
	LLMID                   string  `json:"llm_id"`
	PromptTokenCost         float64 `json:"prompt_token_cost"`
	CompletionTokenCost     float64 `json:"completion_token_cost"`
	PromptTokenCostUnit     string  `json:"prompt_token_cost_unit"`
	CompletionTokenCostUnit string  `json:"completion_token_cost_unit"`
	EffectiveDate           string  `json:"effective_date"`
	ProviderID              string  `json:"provider_id"`
	PricingLevel            string  `json:"pricing_level"`

	effectiveUS int64
}

// DefaultPrice describes a built-in model price (USD per token).
type DefaultPrice struct {
	LLMID      string
	Provider   string
	Prompt     float64
	Completion float64
}

const perM = 1e-6

// DefaultPrices are list prices for common models, matched by longest prefix.
var DefaultPrices = []DefaultPrice{
	{"gpt-5", "openai", 1.25 * perM, 10 * perM},
	{"gpt-5-mini", "openai", 0.25 * perM, 2 * perM},
	{"gpt-5-nano", "openai", 0.05 * perM, 0.4 * perM},
	{"gpt-4.1", "openai", 2 * perM, 8 * perM},
	{"gpt-4.1-mini", "openai", 0.4 * perM, 1.6 * perM},
	{"gpt-4.1-nano", "openai", 0.1 * perM, 0.4 * perM},
	{"gpt-4o", "openai", 2.5 * perM, 10 * perM},
	{"gpt-4o-mini", "openai", 0.15 * perM, 0.6 * perM},
	{"gpt-4-turbo", "openai", 10 * perM, 30 * perM},
	{"gpt-4", "openai", 30 * perM, 60 * perM},
	{"gpt-3.5-turbo", "openai", 0.5 * perM, 1.5 * perM},
	{"o1", "openai", 15 * perM, 60 * perM},
	{"o1-mini", "openai", 1.1 * perM, 4.4 * perM},
	{"o3", "openai", 2 * perM, 8 * perM},
	{"o3-mini", "openai", 1.1 * perM, 4.4 * perM},
	{"o4-mini", "openai", 1.1 * perM, 4.4 * perM},
	{"text-embedding-3-small", "openai", 0.02 * perM, 0},
	{"text-embedding-3-large", "openai", 0.13 * perM, 0},
	{"claude-3-haiku", "anthropic", 0.25 * perM, 1.25 * perM},
	{"claude-3-5-haiku", "anthropic", 0.8 * perM, 4 * perM},
	{"claude-haiku-4-5", "anthropic", 1 * perM, 5 * perM},
	{"claude-3-sonnet", "anthropic", 3 * perM, 15 * perM},
	{"claude-3-5-sonnet", "anthropic", 3 * perM, 15 * perM},
	{"claude-3-7-sonnet", "anthropic", 3 * perM, 15 * perM},
	{"claude-sonnet-4", "anthropic", 3 * perM, 15 * perM},
	{"claude-sonnet-4-5", "anthropic", 3 * perM, 15 * perM},
	{"claude-3-opus", "anthropic", 15 * perM, 75 * perM},
	{"claude-opus-4", "anthropic", 15 * perM, 75 * perM},
	{"claude-opus-4-1", "anthropic", 15 * perM, 75 * perM},
	{"claude-opus-4-5", "anthropic", 5 * perM, 25 * perM},
	// Claude 4.6+ and the Claude 5 family (Anthropic first-party list prices,
	// USD per token; Haiku 5.5 is the price for prompts up to 100K tokens).
	{"claude-opus-4-6", "anthropic", 5 * perM, 25 * perM},
	{"claude-opus-4-7", "anthropic", 5 * perM, 25 * perM},
	{"claude-opus-4-8", "anthropic", 5 * perM, 25 * perM},
	{"claude-sonnet-4-6", "anthropic", 3 * perM, 15 * perM},
	{"claude-opus-5", "anthropic", 5 * perM, 25 * perM},
	{"claude-opus-5-5", "anthropic", 4 * perM, 20 * perM},
	{"claude-sonnet-5", "anthropic", 2 * perM, 10 * perM},
	{"claude-sonnet-5-5", "anthropic", 2 * perM, 10 * perM},
	{"claude-haiku-5-5", "anthropic", 0.1 * perM, 0.5 * perM},
	{"claude-fable-5", "anthropic", 10 * perM, 50 * perM},
	{"claude-fable-5-1", "anthropic", 10 * perM, 50 * perM},
	{"claude-mythos-5-1", "anthropic", 10 * perM, 50 * perM},
	{"gemini-1.5-flash", "google", 0.075 * perM, 0.3 * perM},
	{"gemini-1.5-pro", "google", 1.25 * perM, 5 * perM},
	{"gemini-2.0-flash", "google", 0.1 * perM, 0.4 * perM},
	{"gemini-2.5-flash", "google", 0.3 * perM, 2.5 * perM},
	{"gemini-2.5-pro", "google", 1.25 * perM, 10 * perM},
	{"mistral-large", "mistral", 2 * perM, 6 * perM},
	{"mistral-small", "mistral", 0.2 * perM, 0.6 * perM},
	{"deepseek-chat", "deepseek", 0.27 * perM, 1.1 * perM},
	{"deepseek-reasoner", "deepseek", 0.55 * perM, 2.19 * perM},
	{"mock/echo", "mock", 0, 0},
	{"mock/json", "mock", 0, 0},
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
		return Price{LLMID: d.LLMID, PromptTokenCost: d.Prompt, CompletionTokenCost: d.Completion,
			PromptTokenCostUnit: "USD", CompletionTokenCostUnit: "USD", ProviderID: d.Provider,
			PricingLevel: "default", EffectiveDate: "2024-01-01T00:00:00.000000Z"}, true
	}
	return Price{}, false
}

// Costs computes per-model costs for usage at time atUS.
func (pb *PriceBook) Costs(usage Contribution, atUS int64) map[string]any {
	out := map[string]any{}
	for model, m := range usage.Usage {
		p, ok := pb.lookup(model, atUS)
		if !ok {
			continue
		}
		prompt, completion, total := UsageTokens(m)
		out[model] = map[string]any{
			"requests":                     numberValue(m["requests"]),
			"prompt_tokens":                numberValue(prompt),
			"completion_tokens":            numberValue(completion),
			"total_tokens":                 numberValue(total),
			"prompt_token_cost":            p.PromptTokenCost,
			"completion_token_cost":        p.CompletionTokenCost,
			"prompt_tokens_total_cost":     prompt * p.PromptTokenCost,
			"completion_tokens_total_cost": completion * p.CompletionTokenCost,
			"prompt_token_cost_unit":       p.PromptTokenCostUnit,
			"completion_token_cost_unit":   p.CompletionTokenCostUnit,
			"effective_date":               p.EffectiveDate,
			"provider_id":                  p.ProviderID,
			"pricing_level":                p.PricingLevel,
		}
	}
	return out
}

// TotalCost sums the cost of usage in USD.
func (pb *PriceBook) TotalCost(usage Contribution, atUS int64) float64 {
	var t float64
	for model, m := range usage.Usage {
		p, ok := pb.lookup(model, atUS)
		if !ok {
			continue
		}
		prompt, completion, _ := UsageTokens(m)
		t += prompt*p.PromptTokenCost + completion*p.CompletionTokenCost
	}
	return t
}

// CostInput is one entry of /cost/create.
type CostInput struct {
	PromptTokenCost         float64 `json:"prompt_token_cost"`
	CompletionTokenCost     float64 `json:"completion_token_cost"`
	PromptTokenCostUnit     string  `json:"prompt_token_cost_unit"`
	CompletionTokenCostUnit string  `json:"completion_token_cost_unit"`
	EffectiveDate           string  `json:"effective_date"`
	ProviderID              string  `json:"provider_id"`
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
			if c.PromptTokenCost < 0 || c.CompletionTokenCost < 0 {
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
				prompt_token_cost_unit, completion_token_cost_unit, effective_date, created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
				id, projectID, llm, provider, c.PromptTokenCost, c.CompletionTokenCost, unitP, unitC, eff, nowMicros()); err != nil {
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
	case "id", "llm_id", "provider_id", "prompt_token_cost", "completion_token_cost", "prompt_token_cost_unit", "completion_token_cost_unit":
		return "p." + path, nil, false, nil
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
		p.prompt_token_cost_unit, p.completion_token_cost_unit, p.effective_date FROM llm_prices p WHERE `+where+
		` ORDER BY p.llm_id, p.effective_date DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Price{}
	for rows.Next() {
		var p Price
		if err := rows.Scan(&p.ID, &p.LLMID, &p.ProviderID, &p.PromptTokenCost, &p.CompletionTokenCost,
			&p.PromptTokenCostUnit, &p.CompletionTokenCostUnit, &p.effectiveUS); err != nil {
			return nil, err
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
		out = append(out, Price{ID: "default:" + d.LLMID, LLMID: d.LLMID, PromptTokenCost: d.Prompt, CompletionTokenCost: d.Completion,
			PromptTokenCostUnit: "USD", CompletionTokenCostUnit: "USD", EffectiveDate: "2024-01-01T00:00:00.000000Z",
			ProviderID: d.Provider, PricingLevel: "default"})
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
