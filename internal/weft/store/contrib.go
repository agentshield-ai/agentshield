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

// OwnContribution derives a call's own contribution from its summary and status.
func OwnContribution(summary map[string]any, ended, failed bool) Contribution {
	var c Contribution
	if u, ok := summary["usage"].(map[string]any); ok {
		for model, v := range u {
			fields, ok := v.(map[string]any)
			if !ok {
				continue
			}
			for k, x := range fields {
				if f, ok := toFloat(x); ok {
					c.addUsage(model, k, f)
				}
			}
		}
	}
	if ended {
		if failed {
			c.addStatus("error", 1)
		} else {
			c.addStatus("success", 1)
		}
	}
	return c
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
