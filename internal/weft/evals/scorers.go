package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/agentshield-ai/agentshield/internal/weft/llm"
	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// RunnableScorers are scorer classes the server can execute.
var RunnableScorers = map[string]bool{
	"LLMAsAJudgeScorer": true, "ExactMatchScorer": true, "ContainsScorer": true,
	"RegexScorer": true, "JSONValidScorer": true, "LevenshteinScorer": true,
}

// Scorer is a loaded server-runnable scorer.
type Scorer struct {
	Ref  string
	Name string
	Val  map[string]any
}

func (e *Engine) loadScorer(ctx context.Context, ref string) (*Scorer, error) {
	o, val, err := e.loadObject(ctx, ref)
	if err != nil {
		return nil, err
	}
	cls := className(val)
	if !RunnableScorers[cls] {
		return nil, store.Invalidf("scorer %q (class %q) cannot run on the server; use one of %s", o.ObjectID, cls, runnableScorerList())
	}
	return &Scorer{Ref: o.Ref(), Name: o.ObjectID, Val: val}, nil
}

func outputText(output any) string {
	if m, ok := output.(map[string]any); ok {
		// Unwrap chat completion shaped outputs.
		if cs, ok := m["choices"].([]any); ok && len(cs) > 0 {
			if c, ok := cs[0].(map[string]any); ok {
				if msg, ok := c["message"].(map[string]any); ok {
					return llm.ContentText(msg["content"])
				}
			}
		}
	}
	return Stringify(output)
}

func targetValue(val map[string]any, example map[string]any) (any, error) {
	col, _ := val["target_column"].(string)
	if col == "" {
		col = "expected"
	}
	v, ok := example[col]
	if !ok {
		return nil, fmt.Errorf("example has no %q column", col)
	}
	return v, nil
}

// MaxLevenshteinRunes bounds each text LevenshteinScorer compares: the
// distance costs len(a)·len(b), and monitors run scorers on whatever spans
// their project receives.
const MaxLevenshteinRunes = 10000

// levenshtein is the edit distance of a and b, in O(min(len)) memory. It
// stops when ctx is done.
func levenshtein(ctx context.Context, a, b []rune) (int, error) {
	if len(b) > len(a) {
		a, b = b, a
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		if i%64 == 0 {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
		}
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)], nil
}

// scoreBuiltin computes a score for a non-LLM scorer.
func scoreBuiltin(ctx context.Context, cls string, val map[string]any, example map[string]any, output any) (any, error) {
	out := strings.TrimSpace(outputText(output))
	switch cls {
	case "ExactMatchScorer":
		t, err := targetValue(val, example)
		if err != nil {
			return nil, err
		}
		if s, ok := t.(string); ok {
			return out == strings.TrimSpace(s), nil
		}
		a, _ := refs.CanonicalJSON(t)
		b, _ := refs.CanonicalJSON(output)
		return string(a) == string(b) || out == Stringify(t), nil
	case "ContainsScorer":
		t, err := targetValue(val, example)
		if err != nil {
			return nil, err
		}
		target := Stringify(t)
		if ci, _ := val["case_insensitive"].(bool); ci {
			return strings.Contains(strings.ToLower(out), strings.ToLower(target)), nil
		}
		return strings.Contains(out, target), nil
	case "RegexScorer":
		p, _ := val["pattern"].(string)
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern: %w", err)
		}
		return re.MatchString(out), nil
	case "JSONValidScorer":
		switch output.(type) {
		case map[string]any, []any:
			return true, nil
		}
		return json.Valid([]byte(out)), nil
	case "LevenshteinScorer":
		t, err := targetValue(val, example)
		if err != nil {
			return nil, err
		}
		target := strings.TrimSpace(Stringify(t))
		if utf8.RuneCountInString(out) > MaxLevenshteinRunes || utf8.RuneCountInString(target) > MaxLevenshteinRunes {
			return nil, fmt.Errorf("LevenshteinScorer compares texts of at most %d characters", MaxLevenshteinRunes)
		}
		a, b := []rune(out), []rune(target)
		longest := max(len(a), len(b))
		if longest == 0 {
			return 1.0, nil
		}
		d, err := levenshtein(ctx, a, b)
		if err != nil {
			return nil, err
		}
		return 1 - float64(d)/float64(longest), nil
	}
	return nil, fmt.Errorf("unsupported scorer class %q", cls)
}

// scorerVars builds template variables for judge prompts.
func scorerVars(example map[string]any, output any) map[string]any {
	vars := map[string]any{}
	for k, v := range example {
		vars[k] = v
	}
	b, _ := json.Marshal(example)
	vars["inputs"] = string(b)
	vars["input"] = string(b)
	if _, ok := example["input"]; ok {
		vars["input"] = example["input"]
	}
	vars["output"] = outputText(output)
	return vars
}

// RunScorer executes a scorer and records its call (child of parent when given).
func (e *Engine) RunScorer(ctx context.Context, projectID string, sc *Scorer, example map[string]any, output any, parent *callRec, attrs map[string]any) (any, *callRec, error) {
	inputs := map[string]any{"self": sc.Ref, "output": output}
	for k, v := range example {
		if _, exists := inputs[k]; !exists {
			inputs[k] = v
		}
	}
	rec, err := e.startCall(ctx, projectID, sc.Name+".score", parent, inputs, attrs, nil)
	if err != nil {
		return nil, nil, err
	}
	cls := className(sc.Val)
	var score any
	var scoreErr error
	if cls == "LLMAsAJudgeScorer" {
		score, scoreErr = e.judge(ctx, projectID, sc.Val, example, output, rec)
	} else if fn, ok := extraScorers[cls]; ok {
		score, scoreErr = fn(e, ctx, projectID, sc.Val, example, output, rec, attrs)
	} else {
		score, scoreErr = scoreBuiltin(ctx, cls, sc.Val, example, output)
	}
	if err := rec.end(ctx, score, nil, scoreErr); err != nil {
		return nil, rec, err
	}
	return score, rec, scoreErr
}

func (e *Engine) judge(ctx context.Context, projectID string, val map[string]any, example map[string]any, output any, rec *callRec) (any, error) {
	prompt, _ := val["scoring_prompt"].(string)
	if prompt == "" {
		prompt = "Rate how well the output answers the input on a 1-5 scale.\n\nInput: {inputs}\n\nOutput: {output}\n\nRespond with JSON {\"score\": <1-5>, \"reasoning\": \"...\"}."
	}
	params := map[string]any{}
	var model string
	switch m := val["model"].(type) {
	case string:
		if refs.IsRef(m) {
			_, mv, err := e.loadObject(ctx, m)
			if err != nil {
				return nil, err
			}
			model, _ = mv["llm_model_id"].(string)
			if dp, ok := mv["default_params"].(map[string]any); ok {
				for _, k := range []string{"temperature", "max_tokens", "top_p"} {
					if v, ok := dp[k]; ok {
						params[k] = v
					}
				}
			}
		} else {
			model = m
		}
	case map[string]any:
		model, _ = m["llm_model_id"].(string)
	}
	if model == "" {
		return nil, fmt.Errorf("judge has no model")
	}
	inputs := map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": Render(prompt, scorerVars(example, output))}}}
	for k, v := range params {
		inputs[k] = v
	}
	rf, _ := val["response_format"].(string)
	if rf == "" {
		rf = "json_object"
	}
	if rf == "json_object" {
		inputs["response_format"] = map[string]any{"type": "json_object"}
	}
	resp, _, err := e.TrackedCompletion(ctx, projectID, rec, inputs, true)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(outputText(resp))
	if rf == "json_object" || strings.HasPrefix(text, "{") {
		t := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(text, "```json"), "```"), "```")
		if v, err := refs.Decode([]byte(strings.TrimSpace(t))); err == nil {
			return v, nil
		}
	}
	return text, nil
}
