package evals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// Custom runtimes (PUT /v2/{entity}/{project}/runtimes/{name}) are stored as
// Provider objects so the playground, /completions and LLM judges can use
// them as "<runtime>/<runtime_id>". Each runtime ID is also a ProviderModel
// object. Weft adds a "predict" kind: an HTTP endpoint that serves a model's
// predict (see remotePredict), used by server-side evaluations.

// Runtime kinds.
const (
	RuntimeKindOpenAI  = "openai"
	RuntimeKindPredict = "predict"
)

// RuntimeID is one model ID served by a runtime.
type RuntimeID struct {
	ID        string `json:"id"`
	MaxTokens int    `json:"max_tokens"`
}

// Runtime is a registered custom runtime.
type Runtime struct {
	Name         string            `json:"name"`
	ObjectID     string            `json:"object_id"`
	Ref          string            `json:"ref"`
	Kind         string            `json:"kind"`
	BaseURL      string            `json:"base_url"`
	APIKeySecret *string           `json:"api_key_secret"`
	Headers      map[string]string `json:"headers"`
	RuntimeIDs   []RuntimeID       `json:"runtime_ids"`
}

// RuntimeObjectID is the Provider object id that stores runtime name.
func RuntimeObjectID(name string) string { return store.SanitizeObjectID(name) }

// RuntimeFromObject decodes a runtime from its Provider object.
func RuntimeFromObject(o *store.Obj) (*Runtime, bool) {
	v, _ := o.Val.(map[string]any)
	meta, ok := v["weft_runtime"].(map[string]any)
	if !ok {
		return nil, false
	}
	rt := &Runtime{ObjectID: o.ObjectID, Ref: o.Ref(), Headers: map[string]string{}, RuntimeIDs: []RuntimeID{}}
	rt.Name, _ = meta["name"].(string)
	rt.Kind, _ = meta["kind"].(string)
	if rt.Kind == "" {
		rt.Kind = RuntimeKindOpenAI
	}
	rt.BaseURL, _ = v["base_url"].(string)
	if s, ok := v["api_key_name"].(string); ok && s != "" {
		rt.APIKeySecret = &s
	}
	if h, ok := v["extra_headers"].(map[string]any); ok {
		for k, x := range h {
			rt.Headers[k] = fmt.Sprint(x)
		}
	}
	ids, _ := meta["runtime_ids"].([]any)
	for _, x := range ids {
		m, _ := x.(map[string]any)
		id, _ := m["id"].(string)
		mt := 4096
		if f, ok := store.ToFloat(m["max_tokens"]); ok {
			mt = int(f)
		}
		if id != "" {
			rt.RuntimeIDs = append(rt.RuntimeIDs, RuntimeID{ID: id, MaxTokens: mt})
		}
	}
	return rt, true
}

// LoadRuntime resolves a runtime by name or by a ref to its Provider object.
func (e *Engine) LoadRuntime(ctx context.Context, projectID, nameOrRef string) (*Runtime, error) {
	var o *store.Obj
	var err error
	if refs.IsRef(nameOrRef) {
		o, _, err = e.loadObject(ctx, refs.Normalize(nameOrRef))
	} else {
		o, err = e.Store.ReadObject(ctx, projectID, RuntimeObjectID(nameOrRef), "latest")
		if errors.Is(err, store.ErrNotFound) {
			err = store.Invalidf("runtime %q is not registered in %s", nameOrRef, projectID)
		}
	}
	if err != nil {
		return nil, err
	}
	rt, ok := RuntimeFromObject(o)
	if !ok {
		return nil, store.Invalidf("object %q is not a custom runtime", o.ObjectID)
	}
	return rt, nil
}

// predictor describes how a model runs on the server.
type predictor struct {
	kind     string // llm, remote
	opName   string
	llmModel map[string]any // model value passed to PredictWith (kind llm)
	url      string
	headers  map[string]string
	secret   string         // secret sent as a bearer token
	oauth    map[string]any // the model's auth_config; token_url selects OAuth client credentials (oauth.go)
	timeout  time.Duration
	extra    map[string]any // extra request fields (runtime_id)
}

// predictorFor decides how a model value is evaluated server-side:
//
//   - LLMStructuredCompletionModel: a chat completion (built-in, Provider or runtime model ids);
//   - any model with predict_url: POST {"inputs": example, ...} to that URL;
//   - any model with runtime (a runtime name or ref): a "predict" runtime is
//     called like predict_url; an OpenAI-compatible runtime gets a chat
//     completion for runtime_id built from the model's prompt fields.
func (e *Engine) predictorFor(ctx context.Context, projectID string, modelVal map[string]any) (*predictor, error) {
	class := className(modelVal)
	if class == "" {
		class = "Model"
	}
	op := class + ".predict"
	if class == "LLMStructuredCompletionModel" {
		return &predictor{kind: "llm", opName: op, llmModel: modelVal}, nil
	}
	timeout := time.Duration(numField(modelVal, "predict_timeout", 120) * float64(time.Second))
	if timeout <= 0 || timeout > 30*time.Minute {
		timeout = 120 * time.Second
	}
	if u := strField(modelVal, "predict_url"); u != "" {
		p := &predictor{kind: "remote", opName: op, url: u, headers: stringMap(modelVal["predict_headers"]), timeout: timeout}
		p.secret = strField(modelVal, "predict_api_key_secret")
		if ac, ok := modelVal["auth_config"].(map[string]any); ok && p.secret == "" {
			p.secret = strField(ac, "bearer_secret_name")
		}
		p.oauth, _ = modelVal["auth_config"].(map[string]any)
		return p, checkURL(p.url)
	}
	name := strField(modelVal, "runtime", "runtime_ref", "runtime_name")
	if name == "" {
		return nil, store.Invalidf("model class %q cannot run on the server: use an LLMStructuredCompletionModel, "+
			"give the model a predict_url, or bind it to a registered runtime with a runtime attribute", class)
	}
	rt, err := e.LoadRuntime(ctx, projectID, name)
	if err != nil {
		return nil, err
	}
	runtimeID := strField(modelVal, "runtime_id", "model_id")
	if runtimeID == "" && len(rt.RuntimeIDs) > 0 {
		runtimeID = rt.RuntimeIDs[0].ID
	}
	if rt.Kind == RuntimeKindPredict {
		p := &predictor{kind: "remote", opName: op, url: rt.BaseURL, headers: rt.Headers, timeout: timeout, extra: map[string]any{}}
		if rt.APIKeySecret != nil {
			p.secret = *rt.APIKeySecret
		}
		if runtimeID != "" {
			p.extra["runtime_id"] = runtimeID
		}
		p.oauth, _ = modelVal["auth_config"].(map[string]any)
		return p, checkURL(p.url)
	}
	if runtimeID == "" {
		return nil, store.Invalidf("runtime %q exposes no runtime_ids; set runtime_id on the model", rt.Name)
	}
	// An OpenAI-compatible runtime: reuse the LLMStructuredCompletionModel path.
	params := map[string]any{}
	if dp, ok := modelVal["default_params"].(map[string]any); ok {
		for k, v := range dp {
			params[k] = v
		}
	}
	if _, ok := params["messages_template"]; !ok {
		var tmpl []any
		if sp := strField(modelVal, "system_prompt", "system_message"); sp != "" {
			tmpl = append(tmpl, map[string]any{"role": "system", "content": sp})
		}
		if pt := strField(modelVal, "prompt_template", "prompt"); pt != "" {
			tmpl = append(tmpl, map[string]any{"role": "user", "content": pt})
		}
		if len(tmpl) > 0 && tmpl[len(tmpl)-1].(map[string]any)["role"] == "user" {
			params["messages_template"] = tmpl
		} else if len(tmpl) > 0 {
			// Only a system prompt: the example's text becomes the user message.
			params["system_prompt"] = tmpl[0].(map[string]any)["content"]
		}
	}
	for _, k := range []string{"temperature", "max_tokens", "top_p", "response_format"} {
		if v, ok := modelVal[k]; ok && v != nil {
			if _, set := params[k]; !set {
				params[k] = v
			}
		}
	}
	return &predictor{kind: "llm", opName: op, llmModel: map[string]any{
		"llm_model_id": rt.ObjectID + "/" + runtimeID, "default_params": params,
	}}, nil
}

func checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return store.Invalidf("predict URL %q must be an absolute http(s) URL", raw)
	}
	return nil
}

func stringMap(v any) map[string]string {
	out := map[string]string{}
	if m, ok := v.(map[string]any); ok {
		for k, x := range m {
			out[k] = fmt.Sprint(x)
		}
	}
	return out
}

// remotePredict POSTs {"inputs": example, "model_ref": ref, ...extra} as JSON
// and returns the response: a JSON object's "output" field when present,
// otherwise the whole JSON body, or the body text when it is not JSON.
// An OpenAI-style "usage" (with "model") in the response becomes the call's
// usage summary.
func (e *Engine) remotePredict(ctx context.Context, projectID string, p *predictor, modelRef string, example map[string]any) (any, map[string]any, error) {
	body := map[string]any{"inputs": example, "model_ref": modelRef, "project_id": projectID}
	for k, v := range p.extra {
		body[k] = v
	}
	b, err := json.Marshal(jsonSafe(body))
	if err != nil {
		return nil, nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, p.url, bytes.NewReader(b))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range p.headers {
		req.Header.Set(k, v)
	}
	if p.secret != "" {
		sv, ok := e.LLM.LookupSecret(ctx, p.secret)
		if !ok {
			return nil, nil, fmt.Errorf("secret %q is not set", p.secret)
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(sv))
	}
	if tok, err := e.oauthToken(ctx, p.oauth); err != nil {
		return nil, nil, err
	} else if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := remoteHTTP.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("calling predict endpoint: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("reading predict response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, nil, fmt.Errorf("predict endpoint returned %d: %.300s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	v, err := refs.Decode(bytes.TrimSpace(raw))
	if err != nil {
		return string(raw), nil, nil
	}
	var summary map[string]any
	if m, ok := v.(map[string]any); ok {
		if _, hasUsage := m["usage"].(map[string]any); hasUsage {
			model, _ := m["model"].(string)
			if model == "" {
				model = "remote"
			}
			summary = usageSummary(m, model)
		}
		if out, ok := m["output"]; ok {
			return out, summary, nil
		}
	}
	return v, summary, nil
}
