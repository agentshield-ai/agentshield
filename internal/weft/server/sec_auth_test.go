package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// Security regression tests for authentication, authorisation and request
// handling (reviews A1-A12, U4, D1, D2, D6, D8-D10).

// secEnv is a server with a static admin key ("root"), the compatibility
// adapter on, and these credentials on project m/own:
//
//	viewer   user-bound key of a viewer of m/own
//	member   user-bound key of a member of m/own
//	scoped   legacy key limited to m/own
//	scopedro legacy read-only key limited to m/own
type secEnv struct {
	*testEnv
	s     *Server
	creds map[string]string
}

func newSecEnv(t *testing.T, cfg Config) *secEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "weft.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.APIKeys = append(cfg.APIKeys, "root")
	s := New(cfg, st)
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		hs.Close()
		s.Close()
		_ = st.Close()
	})
	e := &secEnv{testEnv: &testEnv{t: t, srv: hs}, s: s, creds: map[string]string{}}
	for u, role := range map[string]string{"vic": "viewer", "mem": "member"} {
		e.admJSON("POST", "/users/create", "root", map[string]any{"id": u})
		e.admJSON("POST", "/project/members/set", "root", map[string]any{"project_id": "m/own", "user_id": u, "role": role})
	}
	e.creds["viewer"], _ = createKey(e.testEnv, "root", map[string]any{"name": "v", "user_id": "vic"})
	e.creds["member"], _ = createKey(e.testEnv, "root", map[string]any{"name": "m", "user_id": "mem"})
	e.creds["scoped"], _ = createKey(e.testEnv, "root", map[string]any{"name": "s", "user_id": "ci", "projects": []any{"m/own"}})
	e.creds["scopedro"], _ = createKey(e.testEnv, "root", map[string]any{"name": "sro", "user_id": "ci", "projects": []any{"m/own"}, "read_only": true})
	// The victim project, with a secret.
	e.admJSON("POST", "/api/v1/spans/start", "root", map[string]any{"project_id": "v/secret", "id": "vs1", "name": "op",
		"inputs": map[string]any{"pw": "TOPSECRET"}})
	return e
}

// send makes a request with a raw body.
func (e *secEnv) send(method, path, key string, body []byte, headers map[string]string) (int, []byte) {
	e.t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// publicRoute reports routes outside the authenticated group.
func publicRoute(pattern string) bool {
	switch pattern {
	case "/health", "/api/v1/server", "/auth/sso", "/auth/oidc/login", "/auth/oidc/callback", "/auth/logout",
		"/server_info", "/version", "/geolocate": // the adapter's public routes
		return true
	}
	return strings.HasPrefix(pattern, "/{entity}/{project}/")
}

var routeParamRe = regexp.MustCompile(`\{[^}]+\}`)

// fillRoute turns a route pattern into a path naming project m/own.
func fillRoute(pattern string) string {
	p := strings.ReplaceAll(pattern, "{entity}", "m")
	p = strings.ReplaceAll(p, "{project}", "own")
	p = routeParamRe.ReplaceAllString(p, "x")
	p = strings.ReplaceAll(p, "/*", "/x")
	return p
}

// A1: whatever the route and credential, a body naming another project in
// a way the probe used to skip (type confusion, trailing data, GET bodies)
// is refused with 400 or 403 and audited with that project, and never
// reaches the handler.
func TestRouteCredentialBodyMatrix(t *testing.T) {
	e := newSecEnv(t, Config{Compat: true})
	// Positive control: each credential can use its own project.
	for name, key := range e.creds {
		if code, out := e.send("POST", "/api/v1/spans/query", key, []byte(`{"project_id":"m/own"}`), nil); code != 200 {
			t.Fatalf("%s on its own project -> %d %s", name, code, out)
		}
	}
	type route struct{ method, pattern string }
	var routes []route
	if err := chi.Walk(e.s.Handler().(chi.Routes), func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !publicRoute(pattern) {
			routes = append(routes, route{method, pattern})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].pattern+routes[i].method < routes[j].pattern+routes[j].method })
	if len(routes) < 200 {
		t.Fatalf("only %d authenticated routes found", len(routes))
	}
	variants := []string{
		`{"project_id":%q,"refs":1}`,
		`{"project_id":%q,"start":"x"}`,
		`{"project_id":%q,"items":{}}`,
		`{"project_id":%q,"batch":"x"}`,
		`{"project_id":%q,"variables":[]}`,
		`{"project_id":%q,"project_ids":"x"}`,
		`{"project_id":%q}x`,
		`{"PROJECT_ID":%q,"project_id":"m/own"}`,
	}
	n := 0
	victims := map[string]string{}
	for _, rt := range routes {
		for cname, key := range e.creds {
			// The same request naming only its own project: when that is
			// refused as well, the refusal is not about the victim and
			// need not name it in the audit log.
			base, _ := e.send(rt.method, fillRoute(rt.pattern)+"?project_id=m/own", key, []byte(`{"project_id":"m/own"}`), nil)
			refusedAnyway := base == 400 || base == 403 || base == 401
			for vi, v := range variants {
				n++
				victim := fmt.Sprintf("v/p%d", n)
				body := []byte(fmt.Sprintf(v, victim))
				path := fillRoute(rt.pattern) + "?project_id=m/own"
				code, out := e.send(rt.method, path, key, body, nil)
				if code != 400 && code != 403 && code != 413 {
					t.Errorf("%s %s as %s, variant %d -> %d %s", rt.method, rt.pattern, cname, vi, code, bytes.TrimSpace(out))
				}
				if bytes.Contains(out, []byte("TOPSECRET")) {
					t.Errorf("%s %s as %s leaked the victim's data", rt.method, rt.pattern, cname)
				}
				if !refusedAnyway {
					victims[victim] = fmt.Sprintf("%s %s as %s variant %d -> %d", rt.method, rt.pattern, cname, vi, code)
				}
			}
		}
	}
	t.Logf("%d routes, %d requests", len(routes), n)
	// Every refusal is audited naming the project it was refused for.
	e.s.FlushAudit()
	rows, err := e.s.store.DB().Query(`SELECT DISTINCT project FROM audit_log WHERE project IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		seen[p] = true
	}
	rows.Close()
	missing := 0
	for v, where := range victims {
		if !seen[v] {
			missing++
			if missing < 40 {
				t.Errorf("no audit entry naming %s (%s)", v, where)
			}
		}
	}
	if missing > 0 {
		t.Errorf("%d of %d refusals not audited with their project", missing, len(victims))
	}
}

// A1 repro: the type-confusion and trailing-data bypasses of the probe.
func TestProbeBypassRepro(t *testing.T) {
	e := newSecEnv(t, Config{})
	k := e.creds["scopedro"]
	for _, body := range []string{`{"project_id":"v/secret"}`, `{"project_id":"v/secret","refs":1}`, `{"project_id":"v/secret"}x`,
		`{"project_id":"v/secret","start":"x"}`, `{"Project_Id":"v/secret"}`} {
		code, out := e.send("POST", "/api/v1/spans/query?project_id=m/own", k, []byte(body), nil)
		if (code != 400 && code != 403) || bytes.Contains(out, []byte("TOPSECRET")) {
			t.Errorf("%s -> %d %s", body, code, out)
		}
	}
	// Self-grant through members/set is refused too.
	code, _ := e.send("POST", "/project/members/set?project_id=m/own", e.creds["member"],
		[]byte(`{"project_id":"v/secret","user_id":"mem","role":"admin","refs":1}`), nil)
	if code != 400 && code != 403 {
		t.Fatalf("members/set self-grant -> %d", code)
	}
	// Admins get strict decoding too: trailing data is refused.
	if code, out := e.send("POST", "/api/v1/spans/query", "root", []byte(`{"project_id":"m/own"}{"x":1}`), nil); code != 400 {
		t.Fatalf("trailing data as admin -> %d %s", code, out)
	}
}

// A1 defence in depth: the guard checks project_id fields at any depth of
// the decoded request, whatever the probe saw.
func TestProjectGuardChecksDecodedFields(t *testing.T) {
	p := &principal{user: "u", rbac: true, roles: &store.UserRoles{Entities: map[string]store.Role{},
		Projects: map[string]store.Role{"m/own": store.RoleMember}}}
	g := &projectGuard{p: p, need: store.RoleViewer}
	type inner struct {
		ProjectID *string `json:"project_id"`
	}
	type req struct {
		ProjectID string            `json:"project_id"`
		Nested    []inner           `json:"nested"`
		Map       map[string]*inner `json:"map"`
		Data      map[string]any    `json:"data"` // user data: not walked
		IDs       []string          `json:"project_ids"`
	}
	own, other := "m/own", "v/secret"
	ok := req{ProjectID: own, Nested: []inner{{&own}}, Data: map[string]any{"project_id": other}}
	if err := g.checkValue(&ok); err != nil {
		t.Fatalf("own project refused: %v", err)
	}
	for name, bad := range map[string]req{
		"nested": {ProjectID: own, Nested: []inner{{&other}}},
		"map":    {ProjectID: own, Map: map[string]*inner{"a": {&other}}},
		"ids":    {ProjectID: own, IDs: []string{own, other}},
		"top":    {ProjectID: other},
	} {
		if err := g.checkValue(&bad); err == nil {
			t.Errorf("%s: other project accepted", name)
		}
	}
}

// A8: GET bodies are never read by handlers, and a GET body naming a
// project is refused.
func TestGetBodyIgnoredAndRefused(t *testing.T) {
	e := newSecEnv(t, Config{})
	q := e.admJSON("POST", "/annotation_queues", "root", map[string]any{"project_id": "v/secret", "name": "vq"})
	id := q["id"].(string)
	// Victim queue through a GET body: refused.
	code, out := e.send("GET", "/annotation_queues/"+id+"?project_id=m/own", e.creds["viewer"], []byte(`{"project_id":"v/secret"}`), nil)
	if code != 400 || bytes.Contains(out, []byte("vq")) {
		t.Fatalf("GET body naming the victim -> %d %s", code, out)
	}
	// An admin's GET body is ignored: the query parameter decides.
	code, _ = e.send("GET", "/annotation_queues/"+id+"?project_id=m/own", "root", []byte(`{"project_id":"v/secret"}`), nil)
	if code != 404 {
		t.Fatalf("admin GET with body -> %d, want 404 (queue not in m/own)", code)
	}
}

// A9: the adapter's GraphQL upsertModel creates projects: it needs write
// access to them.
func TestCompatGraphQLUpsertNeedsWrite(t *testing.T) {
	e := newSecEnv(t, Config{Compat: true})
	q := `{"query":"mutation upsertModel { upsertModel { model { name } } }","variables":{"entity":"%s","name":"%s"}}`
	for _, c := range []string{"viewer", "scopedro"} {
		code, out := e.send("POST", "/graphql", e.creds[c], []byte(fmt.Sprintf(q, "m", "own")), nil)
		if code != 403 {
			t.Errorf("%s upsertModel on its own project -> %d %s", c, code, out)
		}
	}
	code, out := e.send("POST", "/graphql", e.creds["scoped"], []byte(fmt.Sprintf(q, "v", "newproj")), nil)
	if code != 403 {
		t.Errorf("scoped upsertModel on another project -> %d %s", code, out)
	}
	ps, _ := e.s.store.ListProjects(context.Background())
	for _, p := range ps {
		if p.ProjectID == "v/newproj" {
			t.Fatal("project created by a key without access")
		}
	}
	if code, out := e.send("POST", "/graphql", e.creds["member"], []byte(fmt.Sprintf(q, "m", "own")), nil); code != 200 {
		t.Fatalf("member upsertModel -> %d %s", code, out)
	}
}

// A7: the adapter's link redirects never leave the site.
func TestCompatLinkRedirectStaysOnSite(t *testing.T) {
	e := newSecEnv(t, Config{Compat: true})
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, p := range []string{"//evil.example/weave", "//evil.example/weave/traces", "//evil.example/r/call/x"} {
		resp, err := c.Get(e.srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, "//") || strings.Contains(loc, "evil.example") {
			t.Errorf("%s -> %d Location %q", p, resp.StatusCode, loc)
		}
	}
	resp, err := c.Get(e.srv.URL + "/m/own/weave/traces")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Location") != "/m/own/traces" {
		t.Fatalf("normal link -> %q", resp.Header.Get("Location"))
	}
}

// A10: a narrower credential cannot revoke a broader key; read-only keys
// cannot create keys.
func TestKeysNoBroaderThanCaller(t *testing.T) {
	e := newSecEnv(t, Config{})
	e.admJSON("POST", "/users/create", "root", map[string]any{"id": "al"})
	e.admJSON("POST", "/project/members/set", "root", map[string]any{"project_id": "m/own", "user_id": "al", "role": "member"})
	_, rw := createKey(e.testEnv, "root", map[string]any{"name": "rw", "user_id": "al"})
	ro, _ := createKey(e.testEnv, "root", map[string]any{"name": "ro", "user_id": "al", "read_only": true})
	sc, _ := createKey(e.testEnv, "root", map[string]any{"name": "sc", "user_id": "al", "projects": []any{"m/own"}})
	e.mustCode(403, "POST", "/api_keys/revoke", ro, map[string]any{"id": rw["id"]})
	e.mustCode(403, "POST", "/api_keys/revoke", sc, map[string]any{"id": rw["id"]})
	e.mustCode(403, "POST", "/api_keys/create", ro, map[string]any{"name": "x", "read_only": true})
	// Equal or narrower keys can be revoked (a leaked key revokes itself).
	ro2, ro2k := createKey(e.testEnv, "root", map[string]any{"name": "ro2", "user_id": "al", "read_only": true})
	e.mustCode(200, "POST", "/api_keys/revoke", ro2, map[string]any{"id": ro2k["id"]})
}

// A11: the authenticated user is recorded, not the one the request names.
func TestAttributionUsesAuthenticatedUser(t *testing.T) {
	e := newSecEnv(t, Config{})
	k := e.creds["member"]
	e.mustCode(200, "POST", "/api/v1/spans/start", k, map[string]any{"project_id": "m/own", "id": "a1", "name": "op", "user_id": "bob"})
	code, out := e.send("GET", "/api/v1/spans/a1?project_id=m/own", k, nil, map[string]string{"X-Weft-User": "bob"})
	if code != 200 || !strings.Contains(string(out), `"user_id":"mem"`) {
		t.Fatalf("span user -> %d %s", code, out)
	}
	e.mustCode(200, "POST", "/api/v1/feedback/create", k, map[string]any{"project_id": "m/own", "ref": "weft:///m/own/span/a1",
		"feedback_type": "weft.note", "payload": map[string]any{"note": "x"}, "user_id": "bob"})
	fb := e.admJSON("POST", "/api/v1/feedback/query", "root", map[string]any{"project_id": "m/own"})
	items, _ := fb["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["user_id"] != "mem" {
		t.Fatalf("feedback user = %v", fb)
	}
	// Without authentication the request's user stands (open servers).
	open := newEnv(t)
	open.post("/api/v1/spans/start", map[string]any{"project_id": "m/own", "id": "o1", "name": "op", "user_id": "carol"})
	if c := open.post("/api/v1/spans/query", map[string]any{"project_id": "m/own"}); !strings.Contains(fmt.Sprint(c), "carol") {
		t.Fatalf("open server user: %v", c)
	}
}

// A11/D9: X-Forwarded-For is believed only from --trusted-proxies.
func TestClientAddressTrustedProxies(t *testing.T) {
	for _, tc := range []struct {
		proxies string
		want    string
	}{{"", "127.0.0.1"}, {"127.0.0.1/32", "203.0.113.9"}} {
		px, err := ParseTrustedProxies(tc.proxies)
		if err != nil {
			t.Fatal(err)
		}
		e := newSecEnv(t, Config{TrustedProxies: px})
		e.send("POST", "/api/v1/spans/start", "nope", []byte(`{}`), map[string]string{"X-Forwarded-For": "203.0.113.9", "X-Real-IP": "203.0.113.9"})
		e.s.FlushAudit()
		entries, _, err := e.s.store.QueryAudit(context.Background(), store.AuditFilter{Outcome: "unauthenticated", Limit: 10})
		if err != nil || len(entries) == 0 {
			t.Fatalf("no audit entry: %v", err)
		}
		if entries[0].IP != tc.want {
			t.Errorf("proxies %q: audit IP %s, want %s", tc.proxies, entries[0].IP, tc.want)
		}
	}
}

// A2/U4: Host allowlist, Origin check and JSON content type.
func TestNetGuard(t *testing.T) {
	e := newSecEnv(t, Config{EnforceHosts: true, AllowedHosts: []string{"weft.example.com", ".corp.example"}, CORSOrigins: []string{"https://ui.example"}})
	do := func(method, host, origin, ctype, path, body string) int {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body == "" {
			req = httptest.NewRequest(method, path, nil)
		}
		req.Host = host
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		req.Header.Set("Authorization", "Bearer root")
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	const q = `{"project_id":"m/own"}`
	for _, tc := range []struct {
		name                           string
		method, host, origin, ct, body string
		path                           string
		want                           int
	}{
		{"rebinding host", "GET", "rebind.attacker.example:6345", "", "", "", "/projects", 421},
		{"rebinding host post", "POST", "rebind.attacker.example", "", "application/json", q, "/api/v1/spans/query", 421},
		{"loopback name", "GET", "localhost:6345", "", "", "", "/projects", 200},
		{"ip", "GET", "10.1.2.3:6345", "", "", "", "/projects", 200},
		{"ipv6", "GET", "[::1]:6345", "", "", "", "/projects", 200},
		{"allowed host", "GET", "weft.example.com", "", "", "", "/projects", 200},
		{"allowed suffix", "GET", "a.corp.example", "", "", "", "/projects", 200},
		{"suffix is not the domain itself", "GET", "evilcorp.example", "", "", "", "/projects", 421},
		{"cross-site post", "POST", "localhost:6345", "https://evil.example", "application/json", q, "/api/v1/spans/query", 403},
		{"cross-site null origin", "POST", "localhost:6345", "null", "application/json", q, "/api/v1/spans/query", 403},
		{"same-origin post", "POST", "localhost:6345", "http://localhost:6345", "application/json", q, "/api/v1/spans/query", 200},
		{"cors origin post", "POST", "localhost:6345", "https://ui.example", "application/json", q, "/api/v1/spans/query", 200},
		{"cross-site get is fine", "GET", "localhost:6345", "https://evil.example", "", "", "/projects", 200},
		{"no origin (sdk)", "POST", "localhost:6345", "", "application/json", q, "/api/v1/spans/query", 200},
		{"text/plain", "POST", "localhost:6345", "", "text/plain", q, "/api/v1/spans/query", 415},
		{"form", "POST", "localhost:6345", "", "application/x-www-form-urlencoded", q, "/api/v1/spans/query", 415},
		{"no content type", "POST", "localhost:6345", "", "", q, "/api/v1/spans/query", 415},
		{"json charset", "POST", "localhost:6345", "", "application/json; charset=utf-8", q, "/api/v1/spans/query", 200},
		{"otlp text/plain", "POST", "localhost:6345", "", "text/plain", "{}", "/otel/v1/traces", 415},
		{"otlp protobuf", "POST", "localhost:6345", "", "application/x-protobuf", "", "/otel/v1/metrics", 200},
	} {
		if got := do(tc.method, tc.host, tc.origin, tc.ct, tc.path, tc.body); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
}

// U4/A2 repro: a cross-site no-cors page against an open server can
// neither save a Provider nor call completions.
func TestCrossSiteNoCorsRefusedOnOpenServer(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/api/v1/objects/save", "/api/v1/completions", "/secrets/set"} {
		code, _ := e.raw(path, []byte(`{"project_id":"x/y","name":"p","value":{}}`), map[string]string{"Content-Type": "text/plain", "Origin": "http://localhost:6807"})
		if code != 403 && code != 415 {
			t.Errorf("%s from another origin -> %d", path, code)
		}
		code, _ = e.raw(path, []byte(`{"project_id":"x/y","name":"p","value":{}}`), map[string]string{"Content-Type": "text/plain"})
		if code != 415 {
			t.Errorf("%s as text/plain -> %d", path, code)
		}
	}
}

// D1: an unauthenticated request's body is not read (no decompression,
// no parse) before it is refused; nor with the adapter on.
func TestUnauthenticatedBodyNotRead(t *testing.T) {
	for _, compatOn := range []bool{false, true} {
		e := newSecEnv(t, Config{Compat: compatOn})
		paths := []string{"/api/v1/objects/save", "/project/members/set"}
		if compatOn {
			paths = append(paths, "/obj/create")
		}
		for _, path := range paths {
			body := &countingReader{r: strings.NewReader(strings.Repeat(" ", 1<<20) + "{}")}
			req := httptest.NewRequest("POST", path, body)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Content-Encoding", "gzip")
			rec := httptest.NewRecorder()
			e.s.Handler().ServeHTTP(rec, req)
			if rec.Code != 401 {
				t.Fatalf("compat=%v %s -> %d", compatOn, path, rec.Code)
			}
			if body.n != 0 {
				t.Errorf("compat=%v %s: %d body bytes read before authentication", compatOn, path, body.n)
			}
		}
	}
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// D6: unauthenticated requests cannot grow the audit log without bound.
func TestAuditUnauthenticatedFloodBounded(t *testing.T) {
	e := newSecEnv(t, Config{})
	long := "/api/v1/objects/" + strings.Repeat("A", 100<<10) + "/aliases"
	for i := 0; i < 200; i++ {
		req := httptest.NewRequest("POST", long, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatalf("-> %d", rec.Code)
		}
	}
	e.s.FlushAudit()
	entries, total, err := e.s.store.QueryAudit(context.Background(), store.AuditFilter{Outcome: "unauthenticated", Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if total > unauthPerMinute+1 {
		t.Fatalf("%d unauthenticated entries recorded, want at most %d", total, unauthPerMinute+1)
	}
	for _, en := range entries {
		if len(en.Path) > maxAuditPath {
			t.Fatalf("audit path of %d bytes", len(en.Path))
		}
	}
	// The next minute's first entry summarises what was suppressed.
	var l auditLimiter
	now := time.Now()
	for i := 0; i < unauthPerMinute+5; i++ {
		l.allow("1.2.3.4", now)
	}
	ok, sum := l.allow("1.2.3.4", now.Add(61*time.Second))
	if !ok || sum == nil || !strings.Contains(sum.Detail, "5 more") {
		t.Fatalf("summary = %v %v", ok, sum)
	}
}

// Suspicion: the help guides need no project for scoped keys and viewers
// (shield status takes the project it reports on).
func TestProjectFreeHelpAndShieldStatus(t *testing.T) {
	e := newSecEnv(t, Config{})
	for _, p := range []string{"/api/v1/help", "/api/v1/help/setup.md", "/api/v1/shield/status?project_id=m/own"} {
		for _, c := range []string{"viewer", "scoped"} {
			if code, out := e.send("GET", p, e.creds[c], nil, nil); code != 200 {
				t.Errorf("%s as %s -> %d %s", p, c, code, out)
			}
		}
	}
}

// Suspicion: OTLP audit entries name the project the data went to (the
// project_id header), not X-Weft-Project.
func TestAuditOTLPProjectHeader(t *testing.T) {
	e := newSecEnv(t, Config{})
	code, out := e.send("POST", "/otel/v1/traces", "root", []byte(`{"resourceSpans":[]}`),
		map[string]string{"project_id": "m/own", "X-Weft-Project": "v/secret"})
	if code != 200 {
		t.Fatalf("otlp -> %d %s", code, out)
	}
	e.s.FlushAudit()
	entries, _, _ := e.s.store.QueryAudit(context.Background(), store.AuditFilter{PathPrefix: "/otel/", Limit: 10})
	if len(entries) != 1 || entries[0].Project == nil || *entries[0].Project != "m/own" {
		t.Fatalf("otlp audit = %+v", entries)
	}
}

// Request bodies are capped per route: small for reads.
func TestBodyLimits(t *testing.T) {
	e := newSecEnv(t, Config{})
	big, _ := json.Marshal(map[string]any{"project_id": "m/own", "search": strings.Repeat("x", maxReadBody+10)})
	if code, out := e.send("POST", "/api/v1/spans/query", "root", big, nil); code != 413 {
		t.Fatalf("5 MiB query -> %d %.200s", code, out)
	}
	if code, out := e.send("POST", "/api/v1/spans/query", e.creds["viewer"], big, nil); code != 413 {
		t.Fatalf("5 MiB query as viewer -> %d %.200s", code, out)
	}
	if code, out := e.send("POST", "/api/v1/objects/save", "root", big, nil); code == 413 {
		t.Fatalf("5 MiB object save -> %d %.200s", code, out)
	}
}

// A6/D8: a span may only claim a ranked session source on the ingest path
// that produces it, and outranked spans are hidden, not dropped.
func TestSourceClaimsAndHiding(t *testing.T) {
	e := newSecEnv(t, Config{})
	k := e.creds["member"]
	sess := "S1"
	logSpan := func(id string) map[string]any {
		return map[string]any{"span": map[string]any{"id": id, "trace_id": id, "name": "turn", "session_id": sess,
			"started_at": time.Now().UTC().Format(time.RFC3339Nano), "ended_at": time.Now().UTC().Format(time.RFC3339Nano),
			"inputs": map[string]any{"p": "x"}, "attributes": map[string]any{"weft": map[string]any{"source": "claude-code-otel-logs"}}}}
	}
	// Native log spans written by a trusted internal path (as the log rebuild does).
	ctx := context.Background()
	st := e.s.store
	put := func(id, src string) {
		s := sess
		if _, err := st.UpsertComplete(ctx, []store.CompleteCall{{Start: store.CallStart{ProjectID: "m/own", ID: id, TraceID: id, OpName: "op",
			ThreadID: &s, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Inputs: map[string]any{"a": 1},
			Attributes: map[string]any{"weft": map[string]any{"source": src}}}}}); err != nil {
			t.Fatal(err)
		}
	}
	put("native1", store.SourceClaudeCodeLogs)
	live := func() map[string]bool {
		out := map[string]bool{}
		for _, x := range e.admJSON("POST", "/api/v1/spans/query", "root", map[string]any{"project_id": "m/own",
			"filter": map[string]any{"session_ids": []any{sess}}})["items"].([]any) {
			out[x.(map[string]any)["id"].(string)] = true
		}
		return out
	}
	decoy := map[string]any{"span": map[string]any{"id": "decoy", "trace_id": "decoy", "name": "turn", "session_id": sess,
		"inputs": map[string]any{"p": "x"}, "output": "x", "attributes": map[string]any{"weft": map[string]any{"source": "weft-claude-code"}}}}
	// Via the span API without the plugin header: no effect.
	e.mustCode(200, "POST", "/api/v1/spans/batch", k, map[string]any{"project_id": "m/own", "items": []any{decoy}})
	if l := live(); !l["native1"] || !l["decoy"] {
		t.Fatalf("unclaimed plugin source hid native spans: %v", l)
	}
	sp := e.admJSON("GET", "/api/v1/spans/decoy?project_id=m/own", "root", nil)
	if w, _ := sp["attributes"].(map[string]any)["weft"].(map[string]any); w["claimed_source"] != "weft-claude-code" || w["source"] != nil {
		t.Fatalf("decoy attributes = %v", sp["attributes"])
	}
	// Claiming a native source on the span API has no effect either.
	_ = logSpan
	traces := map[string]any{"span": map[string]any{"id": "fake-traces", "trace_id": "ft", "name": "turn", "session_id": sess,
		"attributes": map[string]any{"weft": map[string]any{"source": "claude-code-otel-traces"}}}}
	e.mustCode(200, "POST", "/api/v1/spans/batch", k, map[string]any{"project_id": "m/own", "items": []any{traces}})
	if l := live(); !l["native1"] {
		t.Fatalf("native source claimed on the span API hid log spans: %v", l)
	}
	// Generic OTLP claiming the plugin source: no effect.
	otlpBody := `{"resourceSpans":[{"resource":{},"scopeSpans":[{"spans":[{"traceId":"5b8efff798038103d269b633813fc60c","spanId":"aaaaaaaaaaaaaaaa","name":"x",
	  "startTimeUnixNano":"1700000000000000000","endTimeUnixNano":"1700000001000000000","attributes":[
	  {"key":"session.id","value":{"stringValue":"S1"}},{"key":"weft.source","value":{"stringValue":"weft-claude-code"}},
	  {"key":"weft.attributes.weft","value":{"kvlistValue":{"values":[{"key":"source","value":{"stringValue":"weft-claude-code"}}]}}}]}]}]}]}`
	if code, out := e.send("POST", "/otel/v1/traces", k, []byte(otlpBody), map[string]string{"project_id": "m/own"}); code != 200 {
		t.Fatalf("otlp -> %d %s", code, out)
	}
	if l := live(); !l["native1"] {
		t.Fatalf("OTLP plugin claim hid log spans: %v", l)
	}
	// The plugin (with its header) does take over; the native spans are
	// hidden, and native spans arriving later are stored hidden.
	plugin := map[string]any{"span": map[string]any{"id": "plug1", "trace_id": "plug1", "name": "turn", "session_id": sess,
		"inputs": map[string]any{"p": "x"}, "attributes": map[string]any{"weft": map[string]any{"source": "weft-claude-code"}}}}
	code, out := e.send("POST", "/api/v1/spans/batch", k, mustJSON(map[string]any{"project_id": "m/own", "items": []any{plugin}}),
		map[string]string{"X-Weft-Source": "weft-claude-code"})
	if code != 200 {
		t.Fatalf("plugin batch -> %d %s", code, out)
	}
	if l := live(); l["native1"] || !l["plug1"] {
		t.Fatalf("after the plugin: %v", l)
	}
	put("native2", store.SourceClaudeCodeLogs)
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM calls WHERE project_id = 'm/own' AND id IN ('native1','native2') AND deleted_at IS NOT NULL
		AND json_extract(attributes, '$.weft.hidden_by_source') IS NOT NULL`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("hidden native spans stored: %d %v", n, err)
	}
	// The replacement is audited.
	e.s.FlushAudit()
	entries, _, _ := st.QueryAudit(ctx, store.AuditFilter{Limit: 100})
	found := false
	for _, en := range entries {
		found = found || strings.Contains(en.Detail, "now shown from weft-claude-code")
	}
	if !found {
		t.Fatal("source replacement not audited")
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
