package server

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/evals"
	"github.com/agentshield-ai/agentshield/internal/weft/llm"
	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// Custom runtimes: PUT /v2/{entity}/{project}/runtimes/{runtime_name}
// registers an endpoint (Weave: an OpenAI-compatible base URL with model IDs;
// Weft also accepts kind "predict" for an HTTP endpoint that serves a model's
// predict). The runtime is stored as a Provider object named after the
// runtime plus one ProviderModel per runtime ID, so "<runtime>/<id>" works in
// the playground, /completions/create, LLM judges and LLMStructuredCompletionModel,
// and a Model with runtime=<name> can be evaluated by /evaluate_model.

func init() {
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		const p = "/v2/{entity}/{project}"
		r.Put(p+"/runtimes/{runtime_name}", s.runtimeApply)
		r.Get(p+"/runtimes", s.runtimeList)
	})
}

var runtimeNameRe = regexp.MustCompile(`^[^\s:]*(?::[^\s:]+)*:?$`)

type runtimeApplyBody struct {
	BaseURL      string            `json:"base_url"`
	APIKeySecret *string           `json:"api_key_secret"`
	Headers      map[string]string `json:"headers"`
	RuntimeIDs   []struct {
		ID        string `json:"id"`
		MaxTokens *int64 `json:"max_tokens"`
	} `json:"runtime_ids"`
	// Kind is a Weft extension: "openai" (default) or "predict".
	Kind string `json:"kind"`
}

func runtimeRes(rt *evals.Runtime) map[string]any {
	ids := []map[string]any{}
	for _, id := range rt.RuntimeIDs {
		ids = append(ids, map[string]any{"id": id.ID, "max_tokens": id.MaxTokens, "playground_id": rt.ObjectID + "/" + id.ID})
	}
	headers := rt.Headers
	if headers == nil {
		headers = map[string]string{}
	}
	return map[string]any{"name": rt.Name, "base_url": rt.BaseURL, "api_key_secret": rt.APIKeySecret, "headers": headers,
		"runtime_ids": ids, "kind": rt.Kind, "ref": rt.Ref}
}

func (s *Server) runtimeApply(w http.ResponseWriter, r *http.Request) {
	pid, err := v2Project(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body runtimeApplyBody
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	rt, err := s.applyRuntime(r.Context(), pid, pathParam(r, "runtime_name"), &body, userFrom(r, nil))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, runtimeRes(rt))
}

func (s *Server) applyRuntime(ctx context.Context, pid, name string, body *runtimeApplyBody, user *string) (*evals.Runtime, error) {
	if name == "" || len(name) > 128 || !runtimeNameRe.MatchString(name) {
		return nil, store.Invalidf("invalid runtime name %q", name)
	}
	objID := evals.RuntimeObjectID(name)
	if llm.IsBuiltinProvider(objID) {
		return nil, store.Invalidf("runtime name %q is reserved for a built-in provider", name)
	}
	u, err := url.Parse(strings.TrimSpace(body.BaseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, store.Invalidf("base_url must be an absolute http(s) URL")
	}
	kind := body.Kind
	if kind == "" {
		kind = evals.RuntimeKindOpenAI
	}
	if kind != evals.RuntimeKindOpenAI && kind != evals.RuntimeKindPredict {
		return nil, store.Invalidf("kind must be %q or %q", evals.RuntimeKindOpenAI, evals.RuntimeKindPredict)
	}
	if body.RuntimeIDs == nil {
		return nil, store.Invalidf("runtime_ids is required")
	}
	if len(body.RuntimeIDs) > 500 {
		return nil, store.Invalidf("too many runtime_ids (max 500)")
	}
	if body.APIKeySecret != nil && *body.APIKeySecret == "" {
		body.APIKeySecret = nil
	}
	ids := []any{}
	seen := map[string]bool{}
	for _, rid := range body.RuntimeIDs {
		id := strings.TrimSpace(rid.ID)
		if id == "" || strings.ContainsAny(id, " \t\r\n") {
			return nil, store.Invalidf("runtime id %q must be non-empty without whitespace", rid.ID)
		}
		if seen[id] {
			return nil, store.Invalidf("duplicate runtime id %q", id)
		}
		seen[id] = true
		mt := int64(4096)
		if rid.MaxTokens != nil {
			if *rid.MaxTokens <= 0 {
				return nil, store.Invalidf("max_tokens of %q must be positive", id)
			}
			mt = *rid.MaxTokens
		}
		ids = append(ids, map[string]any{"id": id, "max_tokens": mt})
	}
	headers := map[string]any{}
	for k, v := range body.Headers {
		headers[k] = v
	}
	apiKey := ""
	if body.APIKeySecret != nil {
		apiKey = *body.APIKeySecret
	}
	val := map[string]any{
		"_type": "Provider", "_class_name": "Provider", "_bases": []any{"Object", "BaseModel"},
		"name": name, "base_url": strings.TrimRight(u.String(), "/"), "api_key_name": apiKey,
		"extra_headers": headers, "return_type": "openai",
		"weft_runtime": map[string]any{"name": name, "kind": kind, "runtime_ids": ids},
	}
	digest, _, err := s.store.CreateObject(ctx, pid, objID, val, "Provider", user)
	if err != nil {
		return nil, err
	}
	provRef := refs.ObjectRef(pid, objID, digest, false)
	// Sync ProviderModel objects: one per runtime ID, others of this runtime removed.
	existing, err := s.store.QueryObjects(ctx, store.ObjsQuery{ProjectID: pid,
		Filter: &store.ObjsFilter{BaseObjectClasses: []string{"ProviderModel"}, LatestOnly: true}})
	if err != nil {
		return nil, err
	}
	keep := map[string]bool{}
	if kind == evals.RuntimeKindOpenAI {
		for _, x := range ids {
			m := x.(map[string]any)
			pmID := store.SanitizeObjectID(objID + "-" + m["id"].(string))
			keep[pmID] = true
			pm := map[string]any{"_type": "ProviderModel", "_class_name": "ProviderModel", "_bases": []any{"Object", "BaseModel"},
				"name": m["id"], "provider": provRef, "max_tokens": m["max_tokens"], "mode": "chat"}
			if _, _, err := s.store.CreateObject(ctx, pid, pmID, pm, "ProviderModel", user); err != nil {
				return nil, err
			}
		}
	}
	for _, o := range existing {
		v, _ := o.Val.(map[string]any)
		pr, _ := v["provider"].(string)
		if p, err := refs.Parse(pr); err == nil && p.Name == objID && !keep[o.ObjectID] {
			if _, err := s.store.DeleteObject(ctx, pid, o.ObjectID, nil); err != nil {
				return nil, err
			}
		}
	}
	o, err := s.store.ReadObject(ctx, pid, objID, digest)
	if err != nil {
		return nil, err
	}
	rt, _ := evals.RuntimeFromObject(o)
	return rt, nil
}

// runtimeList is a Weft addition: GET /v2/{entity}/{project}/runtimes.
func (s *Server) runtimeList(w http.ResponseWriter, r *http.Request) {
	pid, err := v2Project(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	objs, err := s.store.QueryObjects(r.Context(), store.ObjsQuery{ProjectID: pid,
		Filter: &store.ObjsFilter{BaseObjectClasses: []string{"Provider"}, LatestOnly: true}})
	if err != nil {
		writeErr(w, err)
		return
	}
	out := []map[string]any{}
	for _, o := range objs {
		if rt, ok := evals.RuntimeFromObject(o); ok {
			out = append(out, runtimeRes(rt))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
	writeJSON(w, map[string]any{"runtimes": out})
}
