package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestLinkToRegistry(t *testing.T) {
	e := newEnv(t)
	m1 := publish(e, "classifier", map[string]any{"_class_name": "MyModel", "_bases": []any{"Model", "Object", "BaseModel"}, "v": 1})
	m2 := publish(e, "classifier", map[string]any{"_class_name": "MyModel", "_bases": []any{"Model", "Object", "BaseModel"}, "v": 2})
	target := map[string]any{"portfolio_name": "Fraud Models", "entity_name": "acme", "project_name": "registry-model"}
	link := func(ref string, aliases ...string) float64 {
		t.Helper()
		al := []any{}
		for _, a := range aliases {
			al = append(al, a)
		}
		res := e.post("/link_to_registry", map[string]any{"ref": strings.Replace(ref, "weft:///", "weft:///", 1), "target": target, "aliases": al})
		return res["version_index"].(float64)
	}
	if v := link(m1, "production"); v != 0 {
		t.Fatalf("first link = %v", v)
	}
	if v := link(m2, "staging"); v != 1 {
		t.Fatalf("second link = %v", v)
	}
	// Re-linking is idempotent and moves the alias.
	if v := link(m2, "production"); v != 1 {
		t.Fatalf("re-link = %v", v)
	}
	q := e.post("/registry/query", map[string]any{"project_id": "acme/registry-model"})
	cols := q["collections"].([]any)
	if len(cols) != 1 {
		t.Fatalf("collections = %v", q)
	}
	c := cols[0].(map[string]any)
	vs := c["versions"].([]any)
	if c["name"] != "Fraud Models" || len(vs) != 2 {
		t.Fatalf("collection = %v", c)
	}
	newest := vs[0].(map[string]any)
	if newest["version"] != "v1" || newest["ref"] != m2 || toJSON(newest["aliases"]) != `["latest","production","staging"]` ||
		newest["base_object_class"] != "Model" || newest["source_project_id"] != pid {
		t.Fatalf("newest version = %v", newest)
	}
	if toJSON(vs[1].(map[string]any)["aliases"]) != `[]` {
		t.Fatalf("production should have moved off v0: %v", vs[1])
	}
	one := e.post("/registry/query", map[string]any{"project_id": "acme/registry-model", "portfolio_name": "Fraud Models", "alias": "v0"})
	if got := one["collections"].([]any)[0].(map[string]any)["versions"].([]any); len(got) != 1 || got[0].(map[string]any)["ref"] != m1 {
		t.Fatalf("v0 lookup = %v", one)
	}
	if code, _ := e.raw("/registry/query", map[string]any{"project_id": "acme/registry-model", "portfolio_name": "Fraud Models", "alias": "canary"}, nil); code != 404 {
		t.Fatalf("unknown alias -> %d", code)
	}
	for _, bad := range []map[string]any{
		{"ref": "weft:///acme/evals/object/missing:abc", "target": target},
		{"ref": m1, "target": target, "aliases": []any{"latest"}},
		{"ref": m1, "target": map[string]any{"portfolio_name": "x"}},
		{"ref": "weft:///acme/evals/call/abc", "target": target},
	} {
		if code, out := e.raw("/link_to_registry", bad, nil); code != 400 {
			t.Fatalf("%v -> %d %s", bad, code, out)
		}
	}
}

func TestLinkToRegistryChecksTargetProject(t *testing.T) {
	e := newEnv(t, "admin-key")
	m := publish(e, "m", map[string]any{"_class_name": "MyModel", "v": 1})
	created := e.post("/api_keys/create", map[string]any{"name": "scoped", "projects": []any{pid}})
	scoped, _ := created["secret"].(string)
	if scoped == "" {
		t.Skipf("managed keys unavailable: %v", created)
	}
	body := map[string]any{"ref": m, "target": map[string]any{"portfolio_name": "p", "entity_name": "other", "project_name": "registry"}}
	if code, out := e.raw("/link_to_registry", body, map[string]string{"Authorization": "Bearer " + scoped}); code != http.StatusForbidden {
		t.Fatalf("scoped key linking into another project -> %d %s", code, out)
	}
	body["target"] = map[string]any{"portfolio_name": "p", "entity_name": "acme", "project_name": "evals"}
	if code, out := e.raw("/link_to_registry", body, map[string]string{"Authorization": "Bearer " + scoped}); code != 200 {
		t.Fatalf("scoped key linking into its project -> %d %s", code, out)
	}
}

func TestInferenceCatalogAndGeolocate(t *testing.T) {
	e := newEnv(t)
	code, res := e.api2Do(http.MethodPut, "/v2/acme/evals/runtimes/rt", map[string]any{"base_url": "http://127.0.0.1:1/v1",
		"runtime_ids": []any{map[string]any{"id": "tiny-llm", "max_tokens": 2048}}})
	if code != 200 {
		t.Fatalf("runtime -> %d %v", code, res)
	}
	_, cat := e.api2Do(http.MethodGet, "/inference/catalog/models?project_id="+pid, nil)
	models := cat["models"].([]any)
	var gpt, rt map[string]any
	for _, m := range models {
		mm := m.(map[string]any)
		switch mm["id"] {
		case "gpt-4o":
			gpt = mm
		case "rt/tiny-llm":
			rt = mm
		}
	}
	if gpt == nil || rt == nil {
		t.Fatalf("catalog missing models: %d entries", len(models))
	}
	if gpt["priceCentsPerBillionTokensInput"].(float64) != 250000 || gpt["idPlayground"] != "gpt-4o" || gpt["apiStyle"] != "openai" {
		t.Fatalf("gpt-4o entry = %v", gpt)
	}
	if rt["contextWindow"].(float64) != 2048 || rt["provider"] != "rt" {
		t.Fatalf("runtime entry = %v", rt)
	}
	for _, k := range []string{"provider", "id", "idPlayground", "idHuggingFace", "label", "labelOpenRouter", "status", "lifecycleStage",
		"availableIn", "launchedQuarter", "descriptionShort", "descriptionMedium", "launchDate", "featureReasoning", "featureJsonMode",
		"featureStructuredOutput", "featureToolCalling", "featureLoRA", "featureTrainableServerlessRL", "parameterCountTotal",
		"contextWindow", "quantization", "priceCentsPerBillionTokensInput", "priceCentsPerBillionTokensCached",
		"priceCentsPerBillionTokensOutput", "isAvailableOpenRouter", "apiStyle", "modalities", "modalitiesInput", "modalitiesOutput",
		"tags", "likesHuggingFace", "downloadsHuggingFace", "license"} {
		if _, ok := gpt[k]; !ok {
			t.Fatalf("catalog entry lacks required %s", k)
		}
	}
	_, md := e.api2Do(http.MethodGet, "/inference/modelsdev/models?project_id="+pid, nil)
	oa := md["openai"].(map[string]any)
	if oa["env"].([]any)[0] != "OPENAI_API_KEY" || oa["models"].(map[string]any)["gpt-4o"].(map[string]any)["cost"].(map[string]any)["input"].(float64) != 2.5 {
		t.Fatalf("models.dev openai = %v", oa)
	}
	if md["rt"].(map[string]any)["models"].(map[string]any)["rt/tiny-llm"] == nil {
		t.Fatalf("models.dev runtime provider = %v", md["rt"])
	}
	for _, p := range []string{"/inference/router/openrouter/models", "/inference/analysis/artificialanalysis/models"} {
		_, or := e.api2Do(http.MethodGet, p, nil)
		d := or["data"].([]any)
		if len(d) == 0 || d[0].(map[string]any)["pricing"].(map[string]any)["prompt"] == nil {
			t.Fatalf("%s = %v", p, or)
		}
	}
	_, hw := e.api2Do(http.MethodGet, "/inference/nvidia/v2/hardware?model=gpt-4o-mini", nil)
	h := hw["hardware"].([]any)
	if len(h) != 1 || h[0].(map[string]any)["pricing"].(map[string]any)["cents_per_million_input_tokens"].(float64) != 15 {
		t.Fatalf("hardware = %v", hw)
	}
	if code, _ := e.api2Do(http.MethodGet, "/inference/nvidia/v2/hardware", nil); code != 400 {
		t.Fatalf("hardware without model -> %d", code)
	}

	_, geo := e.api2Do(http.MethodGet, "/geolocate?ip=1.2.3.4", nil)
	if geo["ip"] != "1.2.3.4" || geo["allowed"] != true || geo["location"] != nil {
		t.Fatalf("geolocate = %v", geo)
	}
	_, self := e.api2Do(http.MethodGet, "/geolocate", nil)
	if self["ip"] != "127.0.0.1" {
		t.Fatalf("geolocate self = %v", self)
	}
	if code, _ := e.api2Do(http.MethodGet, "/geolocate?ip=not-an-ip", nil); code != 400 {
		t.Fatalf("bad ip -> %d", code)
	}
}
