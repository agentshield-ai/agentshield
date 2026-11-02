package evals

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/llm"
	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// Additional server-runnable scorers. Field names follow the Weave SDK
// classes of the same name (model_id, threshold, system_prompt, …), plus the
// Weft aliases documented in docs/weft/API.md.

// scorerFunc computes a score; rec is the scorer's own call (parent for LLM
// calls) and attrs the attributes it was started with.
type scorerFunc func(e *Engine, ctx context.Context, projectID string, val, example map[string]any, output any, rec *callRec, attrs map[string]any) (any, error)

var extraScorers = map[string]scorerFunc{
	"RemoteScorer":            scoreRemote,
	"HallucinationFreeScorer": scoreHallucination,
	"SummarizationScorer":     scoreSummarization,
	"RelevanceScorer":         scoreRelevance,
	"ValidXMLScorer": func(_ *Engine, _ context.Context, _ string, _, _ map[string]any, out any, _ *callRec, _ map[string]any) (any, error) {
		return map[string]any{"xml_valid": validXML(out)}, nil
	},
	"JSONSchemaScorer":          scoreJSONSchema,
	"OpenAIModerationScorer":    scoreModeration,
	"EmbeddingSimilarityScorer": scoreEmbeddingSimilarity,
	"RuleScorer":                scoreRule,
}

func init() {
	for k := range extraScorers {
		RunnableScorers[k] = true
	}
}

// runnableScorerList is the sorted list used in error messages.
func runnableScorerList() string {
	names := make([]string, 0, len(RunnableScorers))
	for k := range RunnableScorers {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func strField(val map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := val[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func numField(val map[string]any, key string, def float64) float64 {
	if f, ok := store.ToFloat(val[key]); ok {
		return f
	}
	return def
}

// scoredCall returns the call being scored (monitors, /calls/score), if any.
func (e *Engine) scoredCall(ctx context.Context, projectID string, attrs map[string]any) *store.Call {
	w, _ := attrs["weft"].(map[string]any)
	id, _ := w["scored_call_id"].(string)
	if id == "" {
		return nil
	}
	c, err := e.Store.GetCall(ctx, projectID, id, false, false, false)
	if err != nil {
		return nil
	}
	return c
}

func inputsText(example map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := example[k]; ok && v != nil {
			return Stringify(v)
		}
	}
	b, _ := json.Marshal(example)
	return string(b)
}

// ---------------------------------------------------------------- LLM judge presets

// Default prompts of the SDK judge presets (sdk/python/src/weft/scorers.py).
// The SDKs serialise scoring_prompt explicitly; these apply when it is absent.
const hallucinationPrompt = "You are verifying an AI response against its input and context.\n\n" +
	"<input>\n{inputs}\n</input>\n\n<response>\n{output}\n</response>\n\n" +
	"Is every factual claim in the response supported by the input/context? " +
	`Reply with JSON: {{"hallucination_free": true|false, "reasoning": "..."}}`

const summarizationPrompt = "You are grading a summary of a source text.\n\n<source>\n{inputs}\n</source>\n\n" +
	"<summary>\n{output}\n</summary>\n\n" +
	"Rate how faithful, complete and concise the summary is from 0 (useless) to 1 (excellent). " +
	`Reply with JSON: {{"summarization_score": <number 0..1>, "is_faithful": true|false, "reasoning": "..."}}`

const relevancePrompt = "You are judging whether a response is relevant to the request.\n\n<request>\n{inputs}\n</request>\n\n" +
	"<response>\n{output}\n</response>\n\n" +
	`Reply with JSON: {{"is_relevant": true|false, "relevance_score": <number 0..1>, "reasoning": "..."}}`

// judgeScore01 maps a generic 1-5 "score" (as the offline mock/json model
// returns) to 0..1, to fill a preset's fields when the judge omitted them.
func judgeScore01(m map[string]any) (float64, bool) {
	f, ok := store.ToFloat(m["score"])
	if !ok {
		return 0, false
	}
	if f > 1 {
		f = (f - 1) / 4
	}
	return math.Max(0, math.Min(1, f)), true
}

// judgePreset runs a preset like LLMAsAJudgeScorer: the stored scoring_prompt
// (default: the SDK's) with {inputs} and {output}, in JSON mode. Missing
// result fields are derived from a generic {score|correct} reply so the
// offline mock/json model yields the documented shape.
func judgePreset(prompt, boolKey, scoreKey string) scorerFunc {
	return func(e *Engine, ctx context.Context, projectID string, val, example map[string]any, output any, rec *callRec, _ map[string]any) (any, error) {
		jv := map[string]any{}
		for k, v := range val {
			jv[k] = v
		}
		if strField(jv, "scoring_prompt") == "" {
			jv["scoring_prompt"] = prompt
		}
		if jv["model"] == nil || jv["model"] == "" {
			if m := strField(jv, "model_id"); m != "" {
				jv["model"] = m
			} else {
				jv["model"] = "gpt-4o-mini"
			}
		}
		jv["response_format"] = "json_object"
		res, err := e.judge(ctx, projectID, jv, example, output, rec)
		if err != nil {
			return nil, err
		}
		m, ok := res.(map[string]any)
		if !ok {
			return res, nil
		}
		s, hasScore := judgeScore01(m)
		if _, ok := m[boolKey].(bool); !ok {
			if c, ok := m["correct"].(bool); ok {
				m[boolKey] = c
			} else if hasScore {
				m[boolKey] = s >= 0.5
			}
		}
		if scoreKey != "" {
			if _, ok := store.ToFloat(m[scoreKey]); !ok && hasScore {
				m[scoreKey] = s
			}
		}
		if _, ok := m["reasoning"]; !ok {
			if r, ok := m["reason"].(string); ok {
				m["reasoning"] = r
			}
		}
		return m, nil
	}
}

// ---------------------------------------------------------------- format checks

func validXML(output any) bool {
	text := strings.TrimSpace(outputText(output))
	if text == "" {
		return false
	}
	dec := xml.NewDecoder(strings.NewReader(text))
	dec.Strict = true
	elements, depth := 0, 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return elements > 0 && depth == 0
		}
		if err != nil {
			return false
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 && elements > 0 {
				return false // more than one root element
			}
			elements++
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(t)) != "" {
				return false
			}
		}
	}
}

func parseJSONOutput(output any) (any, bool) {
	switch output.(type) {
	case map[string]any, []any:
		return output, true
	}
	text := strings.TrimSpace(outputText(output))
	text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(text, "```json"), "```"), "```"))
	v, err := refs.Decode([]byte(text))
	if err != nil {
		return nil, false
	}
	return v, true
}

func scoreJSONSchema(_ *Engine, _ context.Context, _ string, val, _ map[string]any, output any, _ *callRec, _ map[string]any) (any, error) {
	schema := val["schema"]
	if s, ok := schema.(string); ok {
		v, err := refs.Decode([]byte(s))
		if err != nil {
			return nil, fmt.Errorf("schema is not valid JSON: %w", err)
		}
		schema = v
	}
	sm, ok := schema.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("JSONSchemaScorer needs a schema object")
	}
	v, ok := parseJSONOutput(output)
	if !ok {
		return false, nil
	}
	return len(validateSchema(sm, v, "$", nil)) == 0, nil
}

// validateSchema checks the commonly used JSON Schema keywords: type, enum,
// const, properties, required, additionalProperties, items, min/maxItems,
// min/maxLength, pattern, minimum/maximum, exclusiveMinimum/Maximum,
// anyOf, oneOf, allOf and not.
func validateSchema(schema map[string]any, v any, path string, errs []string) []string {
	add := func(format string, args ...any) { errs = append(errs, path+": "+fmt.Sprintf(format, args...)) }
	if t, ok := schema["type"]; ok {
		types := []string{}
		switch tt := t.(type) {
		case string:
			types = append(types, tt)
		case []any:
			for _, x := range tt {
				if s, ok := x.(string); ok {
					types = append(types, s)
				}
			}
		}
		match := false
		for _, ty := range types {
			if jsonTypeMatches(ty, v) {
				match = true
				break
			}
		}
		if !match {
			add("expected %s, got %s", strings.Join(types, " or "), jsonTypeName(v))
			return errs
		}
	}
	if en, ok := schema["enum"].([]any); ok {
		found := false
		for _, x := range en {
			if jsonEqual(x, v) {
				found = true
				break
			}
		}
		if !found {
			add("value is not one of the allowed values")
		}
	}
	if c, ok := schema["const"]; ok && !jsonEqual(c, v) {
		add("value does not equal const")
	}
	switch x := v.(type) {
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				if k, ok := r.(string); ok {
					if _, has := x[k]; !has {
						add("missing required property %q", k)
					}
				}
			}
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if ps, ok := props[k].(map[string]any); ok {
				errs = validateSchema(ps, x[k], path+"."+k, errs)
				continue
			}
			switch ap := schema["additionalProperties"].(type) {
			case bool:
				if !ap {
					add("unexpected property %q", k)
				}
			case map[string]any:
				errs = validateSchema(ap, x[k], path+"."+k, errs)
			}
		}
	case []any:
		if n, ok := store.ToFloat(schema["minItems"]); ok && float64(len(x)) < n {
			add("expected at least %v items", n)
		}
		if n, ok := store.ToFloat(schema["maxItems"]); ok && float64(len(x)) > n {
			add("expected at most %v items", n)
		}
		if is, ok := schema["items"].(map[string]any); ok {
			for i, it := range x {
				errs = validateSchema(is, it, fmt.Sprintf("%s[%d]", path, i), errs)
			}
		}
	case string:
		n := float64(len([]rune(x)))
		if m, ok := store.ToFloat(schema["minLength"]); ok && n < m {
			add("shorter than %v characters", m)
		}
		if m, ok := store.ToFloat(schema["maxLength"]); ok && n > m {
			add("longer than %v characters", m)
		}
		if p, ok := schema["pattern"].(string); ok {
			if re, err := regexp.Compile(p); err == nil && !re.MatchString(x) {
				add("does not match pattern %q", p)
			}
		}
	default:
		if f, ok := store.ToFloat(v); ok && jsonTypeName(v) == "number" {
			if m, ok := store.ToFloat(schema["minimum"]); ok && f < m {
				add("less than minimum %v", m)
			}
			if m, ok := store.ToFloat(schema["maximum"]); ok && f > m {
				add("greater than maximum %v", m)
			}
			if m, ok := store.ToFloat(schema["exclusiveMinimum"]); ok && f <= m {
				add("not greater than %v", m)
			}
			if m, ok := store.ToFloat(schema["exclusiveMaximum"]); ok && f >= m {
				add("not less than %v", m)
			}
		}
	}
	if all, ok := schema["allOf"].([]any); ok {
		for _, s := range all {
			if sm, ok := s.(map[string]any); ok {
				errs = validateSchema(sm, v, path, errs)
			}
		}
	}
	countMatches := func(list []any) int {
		n := 0
		for _, s := range list {
			if sm, ok := s.(map[string]any); ok && len(validateSchema(sm, v, path, nil)) == 0 {
				n++
			}
		}
		return n
	}
	if anyOf, ok := schema["anyOf"].([]any); ok && countMatches(anyOf) == 0 {
		add("matches none of anyOf")
	}
	if oneOf, ok := schema["oneOf"].([]any); ok && countMatches(oneOf) != 1 {
		add("must match exactly one of oneOf")
	}
	if not, ok := schema["not"].(map[string]any); ok && len(validateSchema(not, v, path, nil)) == 0 {
		add("must not match the schema in not")
	}
	return errs
}

func jsonTypeName(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	default:
		if _, ok := store.ToFloat(x); ok {
			return "number"
		}
	}
	return fmt.Sprintf("%T", v)
}

func jsonTypeMatches(ty string, v any) bool {
	got := jsonTypeName(v)
	if ty == "integer" {
		f, ok := store.ToFloat(v)
		return ok && got == "number" && f == math.Trunc(f)
	}
	return ty == got
}

func jsonEqual(a, b any) bool {
	x, _ := refs.CanonicalJSON(a)
	y, _ := refs.CanonicalJSON(b)
	return bytes.Equal(x, y)
}

// ---------------------------------------------------------------- provider-backed

func scoreModeration(e *Engine, ctx context.Context, projectID string, val, _ map[string]any, output any, _ *callRec, _ map[string]any) (any, error) {
	res, err := e.LLM.Moderate(ctx, projectID, strField(val, "model_id", "model"), outputText(output))
	if err != nil {
		return nil, err
	}
	cats := map[string]any{}
	for k, v := range res.Categories {
		if v && !strings.ContainsAny(k, "/-") {
			cats[k] = true
		}
	}
	return map[string]any{"flagged": res.Flagged, "categories": cats}, nil
}

func scoreEmbeddingSimilarity(e *Engine, ctx context.Context, projectID string, val, example map[string]any, output any, _ *callRec, _ map[string]any) (any, error) {
	col := strField(val, "target_column")
	if col == "" {
		col = "expected"
	}
	target, ok := example[col]
	if !ok {
		return nil, fmt.Errorf("example has no %q column", col)
	}
	model := strField(val, "model", "model_id")
	if model == "" {
		model = "text-embedding-3-small"
	}
	res, err := e.LLM.Embed(ctx, projectID, model, []string{textOf(output), textOf(target)})
	if err != nil {
		return nil, err
	}
	return llm.CosineSimilarity(res.Embeddings[0], res.Embeddings[1]), nil
}

// textOf is the text compared by text scorers (chat outputs are unwrapped).
func textOf(v any) string { return outputText(v) }

// RemoteScorer POSTs {"output", "inputs"} to url (Weave's endpoint_url is
// accepted too) and uses the JSON response (or its text) as the score.
func scoreRemote(e *Engine, ctx context.Context, projectID string, val, example map[string]any, output any, _ *callRec, _ map[string]any) (any, error) {
	target := strField(val, "url", "endpoint_url")
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("RemoteScorer needs an http(s) url")
	}
	timeout := numField(val, "timeout", 30)
	if timeout <= 0 || timeout > 600 {
		timeout = 30
	}
	body := map[string]any{"output": output, "inputs": example}
	headers := map[string]string{}
	if name := strField(val, "headers_secret"); name != "" {
		sv, ok := e.LLM.LookupSecret(ctx, name)
		if !ok {
			return nil, fmt.Errorf("secret %q is not set", name)
		}
		var hm map[string]any
		if json.Unmarshal([]byte(sv), &hm) == nil {
			for k, v := range hm {
				headers[k] = fmt.Sprint(v)
			}
		} else {
			headers["Authorization"] = "Bearer " + strings.TrimSpace(sv)
		}
	}
	if ac, ok := val["auth_config"].(map[string]any); ok {
		if name := strField(ac, "bearer_secret_name"); name != "" {
			sv, ok := e.LLM.LookupSecret(ctx, name)
			if !ok {
				return nil, fmt.Errorf("secret %q is not set", name)
			}
			headers["Authorization"] = "Bearer " + strings.TrimSpace(sv)
		}
	}
	b, err := json.Marshal(jsonSafe(body))
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout*float64(time.Second)))
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, target, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := remoteHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("remote scorer: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("remote scorer returned %d: %.300s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	v, err := refs.Decode(bytes.TrimSpace(raw))
	if err != nil {
		return string(raw), nil
	}
	return v, nil
}

var remoteHTTP = &http.Client{}

// ---------------------------------------------------------------- rule-based signals

// RuleScorerRules are the supported RuleScorer rules.
var RuleScorerRules = []string{"exception", "latency_gt", "empty_output", "tool_error", "invalid_json", "pii_detected"}

var piiPatterns = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"email", regexp.MustCompile(`(?i)\b[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}\b`)},
	{"ssn", regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)},
	{"credit_card", regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`)},
	{"phone", regexp.MustCompile(`(?:\+\d{1,3}[ .-]?)?\(?\b\d{3}\)?[ .-]\d{3}[ .-]\d{4}\b`)},
	{"ip_address", regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|1?\d?\d)\b`)},
}

func luhn(s string) bool {
	sum, n := 0, 0
	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c < '0' || c > '9' {
			continue
		}
		d := int(c - '0')
		if n%2 == 1 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		n++
	}
	return n >= 13 && sum%10 == 0
}

// DetectPII returns the kinds of PII found in text (sorted, unique).
func DetectPII(text string) []string {
	var kinds []string
	for _, p := range piiPatterns {
		for _, m := range p.re.FindAllString(text, 20) {
			if p.kind == "credit_card" && !luhn(m) {
				continue
			}
			kinds = append(kinds, p.kind)
			break
		}
	}
	return kinds
}

func isEmptyOutput(output any) bool {
	switch v := output.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(v) == ""
	case []any:
		return len(v) == 0
	case map[string]any:
		if len(v) == 0 {
			return true
		}
		if cs, ok := v["choices"].([]any); ok && len(cs) > 0 {
			c, _ := cs[0].(map[string]any)
			msg, _ := c["message"].(map[string]any)
			if tc, ok := msg["tool_calls"].([]any); ok && len(tc) > 0 {
				return false
			}
			return strings.TrimSpace(llm.ContentText(msg["content"])) == ""
		}
	}
	return false
}

var toolErrorRe = regexp.MustCompile(`(?i)\b(error|exception|traceback|failed|failure)\b`)

// toolMessageError finds a tool-result message that reports an error.
func toolMessageError(v any) string {
	switch x := v.(type) {
	case map[string]any:
		if x["role"] == "tool" || x["type"] == "tool_result" || x["type"] == "function_call_output" {
			if b, _ := x["is_error"].(bool); b {
				return "tool result marked is_error"
			}
			text := Stringify(x["content"])
			if text == "" {
				text = Stringify(x["output"])
			}
			if toolErrorRe.MatchString(text) {
				return "tool result reports an error"
			}
		}
		for _, k := range []string{"messages", "content", "choices", "message", "input"} {
			if r := toolMessageError(x[k]); r != "" {
				return r
			}
		}
	case []any:
		for _, it := range x {
			if r := toolMessageError(it); r != "" {
				return r
			}
		}
	}
	return ""
}

func scoreRule(e *Engine, ctx context.Context, projectID string, val, example map[string]any, output any, _ *callRec, attrs map[string]any) (any, error) {
	if expr, ok := val["rule"].(map[string]any); ok {
		// SDK form: a query-language operation on {output, inputs, <columns>}.
		doc := map[string]any{}
		for k, v := range example {
			doc[k] = v
		}
		doc["inputs"] = example
		doc["output"] = output
		v, err := EvalExpr(expr, doc)
		if err != nil {
			return nil, err
		}
		if th, ok := store.ToFloat(val["threshold"]); ok && val["threshold"] != nil {
			if _, isBool := v.(bool); !isBool {
				if n, isNum := store.ToFloat(v); isNum {
					return n >= th, nil
				}
			}
		}
		return v, nil
	}
	rule := strField(val, "rule")
	call := e.scoredCall(ctx, projectID, attrs)
	res := map[string]any{"rule": rule}
	flag := func(f bool, value any, reason string) (any, error) {
		res["flagged"] = f
		res["value"] = value
		if reason != "" {
			res["reason"] = reason
		}
		return res, nil
	}
	switch rule {
	case "exception":
		if call != nil && call.Exception != nil {
			return flag(true, *call.Exception, "call raised an exception")
		}
		return flag(false, nil, "")
	case "latency_gt":
		th := numField(val, "threshold", 10)
		if call == nil || call.LatencyMS() == nil {
			return flag(false, nil, "latency unknown")
		}
		sec := *call.LatencyMS() / 1000
		if sec > th {
			return flag(true, sec, fmt.Sprintf("latency %.2fs > %gs", sec, th))
		}
		return flag(false, sec, "")
	case "empty_output":
		if isEmptyOutput(output) {
			return flag(true, nil, "output is empty")
		}
		return flag(false, nil, "")
	case "tool_error":
		if call != nil && call.Status() == "descendant_error" {
			return flag(true, nil, "a child call raised an exception")
		}
		if r := toolMessageError(output); r != "" {
			return flag(true, nil, r)
		}
		if r := toolMessageError(map[string]any(example)); r != "" {
			return flag(true, nil, r)
		}
		return flag(false, nil, "")
	case "invalid_json":
		if _, ok := parseJSONOutput(output); !ok {
			return flag(true, nil, "output is not valid JSON")
		}
		return flag(false, nil, "")
	case "pii_detected":
		kinds := DetectPII(outputText(output))
		list := make([]any, len(kinds))
		for i, k := range kinds {
			list[i] = k
		}
		if len(kinds) > 0 {
			return flag(true, list, "output contains "+strings.Join(kinds, ", "))
		}
		return flag(false, list, "")
	}
	return nil, fmt.Errorf("RuleScorer rule must be one of %s", strings.Join(RuleScorerRules, ", "))
}
