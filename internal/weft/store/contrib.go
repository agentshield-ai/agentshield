package store

import (
	"encoding/json"
	"math"
	"sort"
)

// Contribution is the roll-up unit: LLM usage per model and status counts.
type Contribution struct {
	Usage        map[string]map[string]float64 `json:"usage,omitempty"`
	StatusCounts map[string]float64            `json:"status_counts,omitempty"`
}

// ParseContribution decodes a stored contribution; bad input yields zero.
func ParseContribution(s string) Contribution {
	var c Contribution
	if s == "" || s == "{}" {
		return c
	}
	_ = json.Unmarshal([]byte(s), &c)
	return c
}

// TotalContribution is a call's own contribution plus its descendants'.
// Calls from clients that already roll descendants into their summary
// (cumulative, X-Weft-Summary: cumulative) report their total directly;
// for those the summary is authoritative once the call has ended.
func TotalContribution(summary map[string]any, ended, failed, cumulative bool, rollup Contribution) Contribution {
	if cumulative && ended {
		var c Contribution
		c.addSummaryUsage(summary)
		if sc, ok := summary["status_counts"].(map[string]any); ok {
			for k, x := range sc {
				if f, ok := toFloat(x); ok {
					c.addStatus(k, f)
				}
			}
		} else if failed {
			c.addStatus("error", 1)
		} else {
			c.addStatus("success", 1)
		}
		return c
	}
	t := OwnContribution(summary, ended, failed)
	if cumulative { // running: only descendants are known
		t = Contribution{}
	}
	t.Add(rollup)
	return t
}

// OwnContribution derives a call's own contribution from its summary and status.
func OwnContribution(summary map[string]any, ended, failed bool) Contribution {
	var c Contribution
	c.addSummaryUsage(summary)
	if ended {
		if failed {
			c.addStatus("error", 1)
		} else {
			c.addStatus("success", 1)
		}
	}
	return c
}

// addSummaryUsage adds summary.usage, normalised per model (NormalizeUsage).
func (c *Contribution) addSummaryUsage(summary map[string]any) {
	u, ok := summary["usage"].(map[string]any)
	if !ok {
		return
	}
	for model, v := range u {
		if fields, ok := v.(map[string]any); ok {
			for k, f := range NormalizeUsage(fields) {
				c.addUsage(model, k, f)
			}
		}
	}
}

// NormalizeUsage flattens one model's usage entry into numeric fields with
// canonical cache names, so cached tokens can be priced:
//
//   - cache_read_input_tokens and cache_creation_input_tokens are the
//     cache-read and cache-write subsets of the prompt, and prompt_tokens /
//     input_tokens is the gross prompt.
//   - OpenAI: prompt_tokens_details.cached_tokens (Chat Completions) and
//     input_tokens_details.cached_tokens (Responses) are already included in
//     the prompt count and become cache_read_input_tokens; cache_write_tokens
//     (OpenAI-compatible providers) becomes cache_creation_input_tokens.
//     Nested reasoning_tokens are lifted too.
//   - Gemini/Vertex usage_metadata names (prompt_token_count,
//     candidates_token_count, total_token_count, cached_content_token_count,
//     thoughts_token_count, and their camelCase forms) become prompt_tokens,
//     completion_tokens, total_tokens, cache_read_input_tokens and
//     reasoning_tokens.
//   - Bedrock Converse (inputTokens, cacheReadInputTokens,
//     cacheWriteInputTokens): inputTokens excludes cached tokens, so they are
//     added to the prompt.
//   - Anthropic's native usage reports input_tokens without the cache reads
//     and writes. Producers should send the gross count (Weft's SDK
//     integrations and harness plugins do). When an entry cannot be
//     gross (cache reads + writes exceed the prompt count), the cache counts
//     are added to the prompt; otherwise the entry is taken as gross.
//
// Existing canonical fields always win over derived ones.
func NormalizeUsage(fields map[string]any) map[string]float64 {
	out := map[string]float64{}
	for k, x := range fields {
		if f, ok := toFloat(x); ok {
			out[k] = f
		}
	}
	setDefault := func(k string, v any) {
		if _, exists := out[k]; exists {
			return
		}
		if f, ok := toFloat(v); ok {
			out[k] = f
		}
	}
	for _, dk := range []string{"prompt_tokens_details", "input_tokens_details"} {
		if d, ok := fields[dk].(map[string]any); ok {
			setDefault("cache_read_input_tokens", d["cached_tokens"])
			setDefault("cache_creation_input_tokens", d["cache_write_tokens"])
		}
	}
	for _, dk := range []string{"completion_tokens_details", "output_tokens_details"} {
		if d, ok := fields[dk].(map[string]any); ok {
			setDefault("reasoning_tokens", d["reasoning_tokens"])
		}
	}
	rename := func(to string, from ...string) {
		for _, f := range from {
			if v, ok := out[f]; ok {
				if _, exists := out[to]; !exists {
					out[to] = v
				}
				delete(out, f)
			}
		}
	}
	_, hasPrompt := out["prompt_tokens"]
	_, hasInput := out["input_tokens"]
	if !hasPrompt && !hasInput {
		rename("prompt_tokens", "prompt_token_count", "promptTokenCount")
	}
	_, hasCompletion := out["completion_tokens"]
	_, hasOutput := out["output_tokens"]
	if !hasCompletion && !hasOutput {
		rename("completion_tokens", "candidates_token_count", "candidatesTokenCount")
	}
	rename("total_tokens", "total_token_count", "totalTokenCount")
	rename("cache_read_input_tokens", "cached_content_token_count", "cachedContentTokenCount")
	rename("reasoning_tokens", "thoughts_token_count", "thoughtsTokenCount")

	// Bedrock Converse: inputTokens excludes cache reads and writes.
	if in, ok := out["inputTokens"]; ok && !hasPrompt && !hasInput {
		cr, cw := out["cacheReadInputTokens"], out["cacheWriteInputTokens"]
		out["prompt_tokens"] = in + cr + cw
		delete(out, "inputTokens")
		rename("cache_read_input_tokens", "cacheReadInputTokens")
		rename("cache_creation_input_tokens", "cacheWriteInputTokens")
		rename("completion_tokens", "outputTokens")
		rename("total_tokens", "totalTokens")
	}

	// An entry whose cache counts exceed its prompt cannot be gross: it is
	// Anthropic-native (input_tokens excludes the cache). Make it gross.
	cache := math.Max(out["cache_read_input_tokens"], 0) + math.Max(out["cache_creation_input_tokens"], 0)
	prompt := out["prompt_tokens"] + out["input_tokens"]
	if cache > 0 && cache > prompt {
		key := "input_tokens"
		if _, ok := out["input_tokens"]; !ok {
			if _, ok := out["prompt_tokens"]; ok {
				key = "prompt_tokens"
			}
		}
		out[key] += cache
		completion := out["completion_tokens"] + out["output_tokens"]
		if t, ok := out["total_tokens"]; ok && t < prompt+cache+completion {
			out["total_tokens"] = t + cache
		}
	}
	return out
}

func (c *Contribution) addUsage(model, key string, v float64) {
	if v == 0 {
		return
	}
	if c.Usage == nil {
		c.Usage = map[string]map[string]float64{}
	}
	m := c.Usage[model]
	if m == nil {
		m = map[string]float64{}
		c.Usage[model] = m
	}
	m[key] += v
	if math.Abs(m[key]) < 1e-12 {
		delete(m, key)
		if len(m) == 0 {
			delete(c.Usage, model)
		}
	}
}

func (c *Contribution) addStatus(key string, v float64) {
	if v == 0 {
		return
	}
	if c.StatusCounts == nil {
		c.StatusCounts = map[string]float64{}
	}
	c.StatusCounts[key] += v
	if math.Abs(c.StatusCounts[key]) < 1e-12 {
		delete(c.StatusCounts, key)
	}
}

// Add adds o into c.
func (c *Contribution) Add(o Contribution) {
	for model, m := range o.Usage {
		for k, v := range m {
			c.addUsage(model, k, v)
		}
	}
	for k, v := range o.StatusCounts {
		c.addStatus(k, v)
	}
}

// Sub subtracts o from c.
func (c *Contribution) Sub(o Contribution) {
	for model, m := range o.Usage {
		for k, v := range m {
			c.addUsage(model, k, -v)
		}
	}
	for k, v := range o.StatusCounts {
		c.addStatus(k, -v)
	}
}

// Clone deep-copies c.
func (c Contribution) Clone() Contribution {
	var out Contribution
	out.Add(c)
	return out
}

// IsZero reports whether c carries nothing.
func (c Contribution) IsZero() bool { return len(c.Usage) == 0 && len(c.StatusCounts) == 0 }

// JSON encodes c.
func (c Contribution) JSON() string {
	b, _ := json.Marshal(c)
	return string(b)
}

// UsageTokens returns prompt, completion and total tokens for one usage entry,
// accepting both OpenAI and Anthropic style key names.
func UsageTokens(m map[string]float64) (prompt, completion, total float64) {
	prompt = m["prompt_tokens"] + m["input_tokens"]
	completion = m["completion_tokens"] + m["output_tokens"]
	total = m["total_tokens"]
	if total == 0 {
		total = prompt + completion
	}
	return
}

// Tokens returns total tokens across models.
func (c Contribution) Tokens() float64 {
	var t float64
	for _, m := range c.Usage {
		_, _, tot := UsageTokens(m)
		t += tot
	}
	return t
}

// UsageMap renders usage as JSON-ready values, with integral numbers as ints.
func (c Contribution) UsageMap() map[string]any {
	out := map[string]any{}
	models := make([]string, 0, len(c.Usage))
	for m := range c.Usage {
		models = append(models, m)
	}
	sort.Strings(models)
	for _, model := range models {
		fields := map[string]any{}
		for k, v := range c.Usage[model] {
			fields[k] = numberValue(v)
		}
		out[model] = fields
	}
	return out
}

func numberValue(v float64) any {
	if v == math.Trunc(v) && math.Abs(v) < 1<<53 {
		return int64(v)
	}
	return v
}
