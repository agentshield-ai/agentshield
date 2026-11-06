package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// captureDouble records the Authorization header of every request.
type captureDouble struct {
	srv  *httptest.Server
	mu   sync.Mutex
	auth []string
}

func newCapture(t *testing.T) *captureDouble {
	c := &captureDouble{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.auth = append(c.auth, r.Header.Get("Authorization"))
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"captured"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *captureDouble) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.auth...)
}

func providerVal(base, key string) map[string]any {
	return map[string]any{"_type": "Provider", "_class_name": "Provider", "_bases": []any{"Object", "BaseModel"},
		"base_url": base, "api_key_name": key}
}

func complete(e *testEnv, key, project, model string) string {
	e.t.Helper()
	out := e.mustCode(200, "POST", "/api/v1/completions", key, map[string]any{"project_id": project,
		"inputs": map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": "hi"}}}})
	return string(out)
}

// E1/A4: on a server with non-admin principals, a member's custom provider
// (or automation) naming a server secret, bucket credentials or another
// project's secret gets an error and the destination sees nothing; a
// project secret works in its own project only.
func TestSecretScopesMemberCannotExfiltrate(t *testing.T) {
	f := newRBAC(t)
	e := f.e
	capture := newCapture(t)
	e.mustCode(200, "POST", "/secrets/set", "root", map[string]any{"name": "S3CREDS", "value": "server-secret-value"})
	e.mustCode(200, "POST", "/api/v1/secrets/set", "root", map[string]any{"name": "CUST_S3", "value": `{"access_key_id":"A","secret_access_key":"B"}`, "kind": "bucket"})
	e.mustCode(200, "POST", "/api/v1/secrets/set", f.dave, map[string]any{"name": "DAVE_KEY", "value": "dave-value", "project_id": "beta/app"})

	for _, name := range []string{"S3CREDS", "CUST_S3", "DAVE_KEY", "NOPE"} {
		pname := "steal" + strings.ToLower(strings.ReplaceAll(name, "_", ""))
		e.mustCode(200, "POST", "/api/v1/objects/save", f.bob, map[string]any{"project_id": "acme/app", "name": pname, "value": providerVal(capture.srv.URL, name)})
		out := complete(e, f.bob, "acme/app", pname+"/x")
		if strings.Contains(out, "server-secret-value") || strings.Contains(out, "dave-value") {
			t.Fatalf("%s: secret value in the response: %s", name, out)
		}
		// A server secret is refused outright; names the project cannot see
		// (bucket credentials, other projects' secrets) leave a local
		// endpoint keyless, as before.
		if name == "S3CREDS" && (!strings.Contains(out, `"error"`) || strings.Contains(out, "captured")) {
			t.Fatalf("%s: completion = %s", name, out)
		}
	}
	// Automations: a webhook URL held in a server secret is refused too.
	e.mustCode(200, "POST", "/secrets/set", "root", map[string]any{"name": "SLACK_HOOK", "value": capture.srv.URL + "/hook"})
	run := e.mustCode(200, "POST", "/automations/test", f.bob, map[string]any{"project_id": "acme/app", "val": automationVal("a",
		map[string]any{"type": "feedback_created"}, map[string]any{"type": "slack", "url_secret": "SLACK_HOOK"})})
	if !strings.Contains(string(run), "server secret") {
		t.Fatalf("automation with a server secret: %s", run)
	}
	for _, a := range capture.seen() {
		if a != "" {
			t.Fatalf("the capture double received credentials: %v", capture.seen())
		}
	}
	capture.mu.Lock()
	capture.auth = nil
	capture.mu.Unlock()

	// A project secret works in its project (and only there).
	e.mustCode(200, "POST", "/api/v1/secrets/set", f.bob, map[string]any{"name": "MY_KEY", "value": "bob-value", "project_id": "acme/app"})
	e.mustCode(200, "POST", "/api/v1/objects/save", f.bob, map[string]any{"project_id": "acme/app", "name": "mine", "value": providerVal(capture.srv.URL, "MY_KEY")})
	if out := complete(e, f.bob, "acme/app", "mine/x"); !strings.Contains(out, "captured") {
		t.Fatalf("project secret: %s", out)
	}
	if got := capture.seen(); len(got) != 1 || got[0] != "Bearer bob-value" {
		t.Fatalf("capture = %v", got)
	}
	e.mustCode(200, "POST", "/api/v1/objects/save", f.carol, map[string]any{"project_id": "acme/special", "name": "mine", "value": providerVal(capture.srv.URL, "MY_KEY")})
	// acme/special cannot see MY_KEY: the (local) endpoint is called
	// without a key.
	complete(e, f.carol, "acme/special", "mine/x")
	if got := capture.seen(); len(got) != 2 || got[1] != "" {
		t.Fatalf("another project used acme/app's secret: %v", got)
	}

	// A team secret (set by the team's admin) works in every project of the team.
	e.mustCode(403, "POST", "/api/v1/secrets/set", f.bob, map[string]any{"name": "TEAM_KEY", "value": "t", "scope": "team", "project_id": "acme/app"})
	e.mustCode(200, "POST", "/api/v1/secrets/set", f.alice, map[string]any{"name": "TEAM_KEY", "value": "team-value", "scope": "team", "project_id": "acme/app"})
	e.mustCode(200, "POST", "/api/v1/objects/save", f.carol, map[string]any{"project_id": "acme/special", "name": "teamp", "value": providerVal(capture.srv.URL, "TEAM_KEY")})
	if out := complete(e, f.carol, "acme/special", "teamp/x"); !strings.Contains(out, "captured") {
		t.Fatalf("team secret: %s", out)
	}

	// A server secret shared toward allowed hosts works there only.
	e.mustCode(200, "POST", "/api/v1/secrets/set", "root", map[string]any{"name": "SHARED", "value": "shared-value", "allowed_hosts": []any{"127.0.0.1"}})
	e.mustCode(200, "POST", "/api/v1/objects/save", f.bob, map[string]any{"project_id": "acme/app", "name": "shared", "value": providerVal(capture.srv.URL, "SHARED")})
	if out := complete(e, f.bob, "acme/app", "shared/x"); !strings.Contains(out, "captured") {
		t.Fatalf("shared server secret: %s", out)
	}
	e.mustCode(200, "POST", "/api/v1/objects/save", f.bob, map[string]any{"project_id": "acme/app", "name": "sharedlh",
		"value": providerVal(strings.Replace(capture.srv.URL, "127.0.0.1", "localhost", 1), "SHARED")})
	if out := complete(e, f.bob, "acme/app", "sharedlh/x"); !strings.Contains(out, "allowed hosts") {
		t.Fatalf("shared secret to another host: %s", out)
	}
	if got := capture.seen(); len(got) != 4 || got[2] != "Bearer team-value" || got[3] != "Bearer shared-value" {
		t.Fatalf("capture = %v", got)
	}

	// Who may manage which scope.
	e.mustCode(403, "POST", "/api/v1/secrets/set", f.bob, map[string]any{"name": "X", "value": "y"})
	e.mustCode(403, "POST", "/api/v1/secrets/set", f.bob, map[string]any{"name": "X", "value": "y", "project_id": "beta/app"})
	e.mustCode(403, "POST", "/api/v1/secrets/set", f.carol, map[string]any{"name": "X", "value": "y", "project_id": "acme/app"})
	e.mustCode(403, "POST", "/api/v1/secrets/delete", f.dave, map[string]any{"name": "MY_KEY", "project_id": "acme/app"})
	e.mustCode(400, "POST", "/api/v1/secrets/set", f.bob, map[string]any{"name": "X", "value": "y", "project_id": "acme/app", "kind": "bucket"})
	e.mustCode(403, "POST", "/api/v1/secrets/set", f.bob, map[string]any{"name": "X", "value": "y", "kind": "bucket"})
}

// E4: listing secrets needs a server admin or the member role on the named
// project, and shows only what that project can see.
func TestSecretsListScoped(t *testing.T) {
	f := newRBAC(t)
	e := f.e
	e.mustCode(200, "POST", "/secrets/set", "root", map[string]any{"name": "ADMIN_ONLY", "value": "v"})
	e.mustCode(200, "POST", "/secrets/set", "root", map[string]any{"name": "OPENAI_API_KEY", "value": "v"})
	e.mustCode(200, "POST", "/api/v1/secrets/set", "root", map[string]any{"name": "SHARED", "value": "v", "allowed_hosts": []any{"api.example.com"}})
	e.mustCode(200, "POST", "/api/v1/secrets/set", f.bob, map[string]any{"name": "APP_KEY", "value": "v", "project_id": "acme/app"})
	e.mustCode(200, "POST", "/api/v1/secrets/set", f.dave, map[string]any{"name": "DAVE_KEY", "value": "v", "project_id": "beta/app"})

	for _, path := range []string{"/secrets/list", "/api/v1/secrets/list"} {
		// Without a project (the handler checks; the request filter may
		// refuse first): only what the caller is a member of.
		if code, out := e.admDo("GET", path, f.carol, nil); code == 200 && strings.Contains(string(out), "APP_KEY") {
			t.Fatalf("%s for carol (viewer of acme/app): %s", path, out)
		}
		if code, out := e.admDo("GET", path, f.bob, nil); code == 200 &&
			(!strings.Contains(string(out), "APP_KEY") || strings.Contains(string(out), "DAVE_KEY") || strings.Contains(string(out), "ADMIN_ONLY")) {
			t.Fatalf("%s for bob: %s", path, out)
		} else if code != 200 && code != 403 {
			t.Fatalf("%s for bob: %d %s", path, code, out)
		}
		roKey, _ := createKey(e, "root", map[string]any{"name": "ro-" + strings.ReplaceAll(path, "/", ""), "user_id": "bob", "read_only": true})
		e.mustCode(403, "GET", path, roKey, nil)
		e.mustCode(403, "GET", path+"?project_id=acme/app", f.carol, nil) // viewer
		e.mustCode(403, "GET", path+"?project_id=acme/app", f.dave, nil)
		var res struct {
			Secrets []struct {
				Name, Scope, Source string
				AllowedHosts        []string `json:"allowed_hosts"`
			}
		}
		_ = json.Unmarshal(e.mustCode(200, "GET", path+"?project_id=acme/app", f.bob, nil), &res)
		names := map[string]string{}
		for _, s := range res.Secrets {
			names[s.Name] = s.Scope
		}
		if names["APP_KEY"] != "project" || names["SHARED"] != "server" || names["OPENAI_API_KEY"] != "server" {
			t.Fatalf("%s: %v", path, names)
		}
		if _, ok := names["ADMIN_ONLY"]; ok {
			t.Fatalf("%s: member sees an admin-only server secret: %v", path, names)
		}
		if _, ok := names["DAVE_KEY"]; ok {
			t.Fatalf("%s: member sees another project's secret: %v", path, names)
		}
		all := string(e.mustCode(200, "GET", path, "root", nil))
		for _, n := range []string{"ADMIN_ONLY", "APP_KEY", "DAVE_KEY", "SHARED"} {
			if !strings.Contains(all, n) {
				t.Fatalf("%s: admin list lacks %s: %s", path, n, all)
			}
		}
		if strings.Contains(all, `"value"`) {
			t.Fatalf("values listed: %s", all)
		}
	}
}

// Single-user servers (no auth, static or admin keys only): every object is
// a server admin's, so existing server secrets keep working for custom
// providers, as before scopes.
func TestServerSecretsOpenWhileOnlyAdmins(t *testing.T) {
	e := newEnv(t)
	capture := newCapture(t)
	e.post("/secrets/set", map[string]any{"name": "LEGACY_KEY", "value": "legacy"})
	publish(e, "legacy", providerVal(capture.srv.URL, "LEGACY_KEY"))
	resp := e.post("/api/v1/completions", map[string]any{"project_id": pid, "inputs": map[string]any{"model": "legacy/x",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}}}})
	if !strings.Contains(toJSON(resp), "captured") {
		t.Fatalf("completion = %v", resp)
	}
	if got := capture.seen(); len(got) != 1 || got[0] != "Bearer legacy" {
		t.Fatalf("capture = %v", got)
	}
}
