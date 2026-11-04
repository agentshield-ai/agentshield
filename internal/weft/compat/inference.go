package compat

import (
	"math"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// Model catalog endpoints, served from Weft's own model
// catalog: the built-in models of /llm/models with their list prices, plus
// the project's ProviderModel objects (custom providers and runtimes) when
// ?project_id= is given. GET /geolocate answers locally: Weft has no GeoIP
// database and no regional restrictions.

type catalogEntry struct {
	ID         string // playground id, e.g. openai/gpt-4o or myruntime/llama
	Provider   string
	Name       string // model name without the provider prefix
	Label      string
	Available  bool
	MaxTokens  int
	Prompt     float64 // USD per token
	Completion float64
	CacheRead  float64 // USD per cache-read token (Prompt when not priced separately)
	CacheWrite float64 // USD per cache-write token (Prompt when not priced separately)
	Priced     bool
	APIStyle   string
	Source     string // builtin, provider
	KeyName    string
}

var providerKeyNames = map[string]string{"openai": "OPENAI_API_KEY", "anthropic": "ANTHROPIC_API_KEY", "google": "GEMINI_API_KEY",
	"gemini": "GEMINI_API_KEY", "mistral": "MISTRAL_API_KEY", "deepseek": "DEEPSEEK_API_KEY", "xai": "XAI_API_KEY", "groq": "GROQ_API_KEY"}

func (s *Routes) catalogEntries(r *http.Request) []catalogEntry {
	ctx := r.Context()
	var out []catalogEntry
	for _, m := range s.llm.Models(ctx) {
		name := m.ID
		if _, after, ok := strings.Cut(m.ID, "/"); ok {
			name = after
		}
		e := catalogEntry{ID: m.ID, Provider: m.Provider, Name: name, Label: m.Label, Available: m.Available, MaxTokens: m.MaxTokens,
			APIStyle: "openai", Source: "builtin", KeyName: providerKeyNames[m.Provider]}
		if m.Provider == "anthropic" {
			e.APIStyle = "anthropic"
		}
		if d, ok := store.LookupDefaultPrice(name); ok && m.Provider != "mock" {
			e.Prompt, e.Completion, e.Priced = d.Prompt, d.Completion, true
			e.CacheRead, e.CacheWrite = d.CacheRead, d.CacheWrite
			if e.CacheRead == 0 {
				e.CacheRead = d.Prompt
			}
			if e.CacheWrite == 0 {
				e.CacheWrite = d.Prompt
			}
		}
		out = append(out, e)
	}
	if pid, err := project(r.URL.Query().Get("project_id")); err == nil {
		objs, err := s.store.QueryObjects(ctx, store.ObjsQuery{ProjectID: pid,
			Filter: &store.ObjsFilter{BaseObjectClasses: []string{"ProviderModel"}, LatestOnly: true}})
		if err == nil {
			keyOf := map[string]string{}
			for _, o := range objs {
				v, _ := o.Val.(map[string]any)
				provRef, _ := v["provider"].(string)
				name, _ := v["name"].(string)
				if name == "" {
					name = o.ObjectID
				}
				prov := provRef
				if pr, err := refs.Parse(provRef); err == nil {
					prov = pr.Name
					if _, seen := keyOf[prov]; !seen {
						if po, err := s.store.ReadObject(ctx, pr.ProjectID(), pr.Name, "latest"); err == nil {
							pv, _ := po.Val.(map[string]any)
							keyOf[prov], _ = pv["api_key_name"].(string)
						}
					}
				}
				mt := 4096
				if f, ok := store.ToFloat(v["max_tokens"]); ok {
					mt = int(f)
				}
				out = append(out, catalogEntry{ID: prov + "/" + name, Provider: prov, Name: name, Label: prov + "/" + name, Available: true,
					MaxTokens: mt, APIStyle: "openai", Source: "provider", KeyName: keyOf[prov]})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func centsPerBillion(usdPerToken float64) int64 { return int64(math.Round(usdPerToken * 1e11)) }

func (s *Routes) inferenceCatalog(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range s.catalogEntries(r) {
		status := "available"
		if !e.Available {
			status = "unavailable"
		}
		desc := "Served by Weft through the " + e.Provider + " provider."
		if e.Source == "provider" {
			desc = "Served by Weft through the project's custom provider or runtime " + e.Provider + "."
		}
		models = append(models, map[string]any{
			"provider": e.Provider, "id": e.ID, "idPlayground": e.ID, "idHuggingFace": "", "label": e.Label, "labelOpenRouter": e.Label,
			"status": status, "lifecycleStage": "general-availability", "availableIn": []string{}, "launchedQuarter": "", "launchDate": "",
			"descriptionShort": desc, "descriptionMedium": desc,
			"featureReasoning": false, "featureJsonMode": e.APIStyle == "openai", "featureStructuredOutput": e.APIStyle == "openai",
			"featureToolCalling": e.Provider != "mock", "featureLoRA": false, "featureTrainableServerlessRL": false,
			"parameterCountTotal": 0, "contextWindow": e.MaxTokens, "quantization": "bf16",
			"priceCentsPerBillionTokensInput": centsPerBillion(e.Prompt), "priceCentsPerBillionTokensCached": centsPerBillion(e.CacheRead),
			"priceCentsPerBillionTokensOutput": centsPerBillion(e.Completion), "isAvailableOpenRouter": false, "apiStyle": e.APIStyle,
			"modalities": []string{"text"}, "modalitiesInput": []string{"text"}, "modalitiesOutput": []string{"text"},
			"tags": []string{e.Provider, e.Source}, "likesHuggingFace": 0, "downloadsHuggingFace": 0, "license": "",
		})
	}
	writeJSON(w, map[string]any{"models": models})
}

func (s *Routes) inferenceModelsDev(w http.ResponseWriter, r *http.Request) {
	out := map[string]map[string]any{}
	for _, e := range s.catalogEntries(r) {
		p := out[e.Provider]
		if p == nil {
			env := []string{}
			if e.KeyName != "" {
				env = append(env, e.KeyName)
			}
			npm := "@ai-sdk/openai-compatible"
			if e.APIStyle == "anthropic" {
				npm = "@ai-sdk/anthropic"
			}
			p = map[string]any{"id": e.Provider, "name": e.Provider, "npm": npm, "env": env, "doc": "", "api": nil, "models": map[string]any{}}
			out[e.Provider] = p
		}
		m := map[string]any{"id": e.ID, "name": e.Label, "attachment": false, "reasoning": false, "tool_call": e.Provider != "mock",
			"structured_output": e.APIStyle == "openai", "temperature": true, "release_date": "", "last_updated": "", "open_weights": false,
			"limit":      map[string]any{"context": e.MaxTokens, "input": e.MaxTokens, "output": e.MaxTokens},
			"modalities": map[string]any{"input": []string{"text"}, "output": []string{"text"}}}
		if e.Priced {
			m["cost"] = map[string]any{"input": e.Prompt * 1e6, "output": e.Completion * 1e6, "cache_read": e.CacheRead * 1e6, "cache_write": e.CacheWrite * 1e6}
		}
		p["models"].(map[string]any)[e.ID] = m
	}
	writeJSON(w, out)
}

func (s *Routes) inferenceOpenRouter(w http.ResponseWriter, r *http.Request) {
	data := []map[string]any{}
	price := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	for _, e := range s.catalogEntries(r) {
		features := []string{}
		if e.Provider != "mock" {
			features = append(features, "tools")
		}
		if e.APIStyle == "openai" {
			features = append(features, "json_mode", "structured_outputs")
		}
		data = append(data, map[string]any{
			"id": e.ID, "hugging_face_id": "", "name": e.Label, "created": 0,
			"input_modalities": []string{"text"}, "output_modalities": []string{"text"}, "quantization": "bf16",
			"context_length": e.MaxTokens, "max_output_length": e.MaxTokens,
			"pricing": map[string]any{"prompt": price(e.Prompt), "completion": price(e.Completion), "image": "0", "request": "0",
				"input_cache_read": price(e.CacheRead)},
			"supported_sampling_parameters": []string{"temperature", "top_p", "frequency_penalty", "presence_penalty", "stop", "seed"},
			"supported_features":            features, "datacenters": []any{}, "deprecation_date": nil,
		})
	}
	writeJSON(w, map[string]any{"data": data})
}

func (s *Routes) inferenceNvidiaHardware(w http.ResponseWriter, r *http.Request) {
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	if model == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}
	hw := []map[string]any{}
	for _, e := range s.catalogEntries(r) {
		if !strings.EqualFold(e.Name, model) && !strings.EqualFold(e.ID, model) {
			continue
		}
		hw = append(hw, map[string]any{"id": "weft-" + e.Provider, "name": "Weft via " + e.Provider, "type": "serverless",
			"pricing": map[string]any{"cents_per_million_input_tokens": e.Prompt * 1e8, "cents_per_million_output_tokens": e.Completion * 1e8},
			"specs":   map[string]string{"provider": e.Provider, "model_id": e.ID}})
	}
	writeJSON(w, map[string]any{"hardware": hw})
}

func (s *Routes) geolocate(w http.ResponseWriter, r *http.Request) {
	ip := strings.TrimSpace(r.URL.Query().Get("ip"))
	if ip == "" {
		ip = r.RemoteAddr
		if h, _, err := net.SplitHostPort(ip); err == nil {
			ip = h
		}
	}
	if net.ParseIP(ip) == nil {
		writeError(w, http.StatusBadRequest, "invalid ip "+strconvQuote(ip))
		return
	}
	writeJSON(w, map[string]any{"ip": ip, "location": nil, "allowed": true})
}

func strconvQuote(s string) string { return strconv.Quote(s) }
