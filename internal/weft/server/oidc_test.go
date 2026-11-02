package server

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// fakeIdP is a minimal OpenID provider: discovery, JWKS, an authorize
// endpoint that approves at once, and a token endpoint that checks PKCE and
// issues signed ID tokens. Fields tweak the next token.
type fakeIdP struct {
	srv      *httptest.Server
	rsaKey   *rsa.PrivateKey
	ecKey    *ecdsa.PrivateKey
	alg      string // RS256 or ES256
	clientID string

	mu       sync.Mutex
	codes    map[string][2]string // code -> nonce, challenge
	claims   map[string]any       // merged into the ID token
	audience string               // overrides aud
	expired  bool
	nonce    string // overrides nonce
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func newFakeIdP(t *testing.T, alg string) *fakeIdP {
	t.Helper()
	idp := &fakeIdP{alg: alg, clientID: "weft-ui", codes: map[string][2]string{}}
	var err error
	if idp.rsaKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		t.Fatal(err)
	}
	if idp.ecKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		u := idp.srv.URL
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": u, "authorization_endpoint": u + "/authorize", "token_endpoint": u + "/token",
			"jwks_uri": u + "/jwks", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256", "ES256"}})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		e := big.NewInt(int64(idp.rsaKey.E)).Bytes()
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{
			map[string]any{"kty": "RSA", "kid": "rsa1", "use": "sig", "alg": "RS256", "n": b64(idp.rsaKey.N.Bytes()), "e": b64(e)},
			map[string]any{"kty": "EC", "kid": "ec1", "use": "sig", "alg": "ES256", "crv": "P-256",
				"x": b64(idp.ecKey.X.FillBytes(make([]byte, 32))), "y": b64(idp.ecKey.Y.FillBytes(make([]byte, 32)))},
		}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != idp.clientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
			q.Get("code_challenge") == "" || q.Get("nonce") == "" || !strings.Contains(q.Get("scope"), "openid") {
			http.Error(w, "bad authorize request", 400)
			return
		}
		code := randToken(12)
		idp.mu.Lock()
		idp.codes[code] = [2]string{q.Get("nonce"), q.Get("code_challenge")}
		idp.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id, sec, ok := r.BasicAuth()
		if !ok {
			id, sec = r.Form.Get("client_id"), r.Form.Get("client_secret")
		}
		w.Header().Set("Content-Type", "application/json")
		if id != idp.clientID || sec != "idp-secret" {
			w.WriteHeader(401)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_client"})
			return
		}
		idp.mu.Lock()
		c, ok := idp.codes[r.Form.Get("code")]
		delete(idp.codes, r.Form.Get("code"))
		idp.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || b64(sum[:]) != c[1] {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 300, "id_token": idp.sign(t, c[0])})
	})
	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)
	return idp
}

func (idp *fakeIdP) sign(t *testing.T, nonce string) string {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	now := time.Now()
	claims := map[string]any{"iss": idp.srv.URL, "sub": "sub-123", "aud": idp.clientID, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"nonce": nonce, "email": "jane@example.com", "email_verified": true, "name": "Jane Doe", "preferred_username": "jane"}
	if idp.audience != "" {
		claims["aud"] = idp.audience
	}
	if idp.expired {
		claims["iat"], claims["exp"] = now.Add(-2*time.Hour).Unix(), now.Add(-time.Hour).Unix()
	}
	if idp.nonce != "" {
		claims["nonce"] = idp.nonce
	}
	for k, v := range idp.claims {
		claims[k] = v
	}
	kid := "rsa1"
	if idp.alg == "ES256" {
		kid = "ec1"
	}
	hdr, _ := json.Marshal(map[string]any{"alg": idp.alg, "kid": kid, "typ": "JWT"})
	body, _ := json.Marshal(claims)
	input := b64(hdr) + "." + b64(body)
	digest := sha256.Sum256([]byte(input))
	var sig []byte
	var err error
	if idp.alg == "ES256" {
		r, s, e := ecdsa.Sign(rand.Reader, idp.ecKey, digest[:])
		err = e
		sig = append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	} else {
		sig, err = rsa.SignPKCS1v15(rand.Reader, idp.rsaKey, crypto.SHA256, digest[:])
	}
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + b64(sig)
}

type ssoEnv struct {
	*testEnv
	s      *Server
	idp    *fakeIdP
	client *http.Client
}

func newSSOEnv(t *testing.T, alg string, tweak func(*OIDCConfig)) *ssoEnv {
	t.Helper()
	idp := newFakeIdP(t, alg)
	st, err := store.Open(filepath.Join(t.TempDir(), "weft.db"))
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewUnstartedServer(nil)
	hs.Start()
	oc := &OIDCConfig{Issuer: idp.srv.URL, ClientID: idp.clientID, ClientSecret: "idp-secret", RedirectURL: hs.URL + "/auth/oidc/callback",
		AllowedDomains: []string{"example.com"}, GroupRoles: []GroupRole{{Group: "weft-admins", Role: store.RoleAdmin},
			{Group: "ml", Role: store.RoleMember, Entity: "acme"}}}
	if tweak != nil {
		tweak(oc)
	}
	s := New(Config{IAM: IAMConfig{OIDC: oc}}, st)
	hs.Config.Handler = s.Handler()
	t.Cleanup(func() {
		hs.Close()
		s.Close()
		_ = st.Close()
	})
	return &ssoEnv{testEnv: &testEnv{t: t, srv: hs}, s: s, idp: idp}
}

// login runs the browser flow with a fresh cookie jar. It returns the final
// redirect target and the jar. mutate may rewrite the callback URL.
func (e *ssoEnv) login(mutate func(*url.URL)) (string, http.CookieJar) {
	e.t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	e.client = c
	get := func(u string) *http.Response {
		resp, err := c.Get(u)
		if err != nil {
			e.t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	r1 := get(e.srv.URL + "/auth/oidc/login?return_to=/acme/app/traces")
	if r1.StatusCode != 302 || !strings.HasPrefix(r1.Header.Get("Location"), e.idp.srv.URL+"/authorize") {
		e.t.Fatalf("login -> %d %s", r1.StatusCode, r1.Header.Get("Location"))
	}
	r2 := get(r1.Header.Get("Location"))
	if r2.StatusCode != 302 {
		e.t.Fatalf("authorize -> %d", r2.StatusCode)
	}
	cb, _ := url.Parse(r2.Header.Get("Location"))
	if mutate != nil {
		mutate(cb)
	}
	r3 := get(cb.String())
	if r3.StatusCode != 302 {
		e.t.Fatalf("callback -> %d", r3.StatusCode)
	}
	return r3.Header.Get("Location"), jar
}

func (e *ssoEnv) me() (int, map[string]any) {
	resp, err := e.client.Get(e.srv.URL + "/auth/me")
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func sessionOf(jar http.CookieJar, base string) string {
	u, _ := url.Parse(base)
	for _, c := range jar.Cookies(u) {
		if c.Name == sessionCookie {
			return c.Value
		}
	}
	return ""
}

func TestOIDCLogin(t *testing.T) {
	e := newSSOEnv(t, "RS256", nil)
	e.idp.claims = map[string]any{"groups": []any{"ml", "other"}}
	if info := e.admJSON("GET", "/auth/sso", "", nil); info["enabled"] != true {
		t.Fatalf("sso info = %v", info)
	}
	if code, _ := e.admDo("GET", "/projects", "", nil); code != 401 {
		t.Fatalf("SSO should turn auth on: %d", code)
	}
	dest, jar := e.login(nil)
	if dest != "/acme/app/traces" || sessionOf(jar, e.srv.URL) == "" {
		t.Fatalf("valid login -> %s", dest)
	}
	code, me := e.me()
	u, _ := me["user"].(map[string]any)
	if code != 200 || u["id"] != "jane" || u["email"] != "jane@example.com" || u["sso"] != true || me["auth_method"] != "session" ||
		me["roles"].(map[string]any)["entities"].(map[string]any)["acme"] != "member" || me["server_admin"] != false {
		t.Fatalf("me after login = %d %v", code, me)
	}
	// Signing in again maps to the same user; group changes are synced.
	e.idp.claims = map[string]any{"groups": []any{"weft-admins"}}
	e.login(nil)
	_, me = e.me()
	if me["user"].(map[string]any)["id"] != "jane" || me["server_admin"] != true || len(me["roles"].(map[string]any)["entities"].(map[string]any)) != 0 {
		t.Fatalf("second login = %v", me)
	}
	if us := e.s.store; us != nil {
		all, _ := us.ListUsers(t.Context(), true)
		if len(all) != 1 {
			t.Fatalf("users = %v", all)
		}
	}
	// The sign-in is audited.
	e.s.FlushAudit()
	if ents, _, _ := e.s.store.QueryAudit(t.Context(), store.AuditFilter{PathPrefix: "/auth/oidc/callback", Outcome: "success"}); len(ents) != 2 || ents[0].Actor != "jane" {
		t.Fatalf("audit = %v", ents)
	}
}

func TestOIDCRejections(t *testing.T) {
	e := newSSOEnv(t, "RS256", nil)
	cases := []struct {
		name   string
		setup  func()
		mutate func(*url.URL)
		want   string
	}{
		{"wrong audience", func() { e.idp.audience = "someone-else" }, nil, "audience"},
		{"expired token", func() { e.idp.expired = true }, nil, "expired"},
		{"wrong nonce", func() { e.idp.nonce = "replayed" }, nil, "nonce"},
		{"wrong state", nil, func(u *url.URL) {
			q := u.Query()
			q.Set("state", "forged")
			u.RawQuery = q.Encode()
		}, "state"},
		{"disallowed domain", func() { e.idp.claims = map[string]any{"email": "eve@evil.example"} }, nil, "domain"},
		{"unverified email", func() { e.idp.claims = map[string]any{"email_verified": false} }, nil, "verified email"},
		{"forged code", nil, func(u *url.URL) {
			q := u.Query()
			q.Set("code", "nope")
			u.RawQuery = q.Encode()
		}, "sign-in failed"},
	}
	for _, c := range cases {
		e.idp.audience, e.idp.expired, e.idp.nonce, e.idp.claims = "", false, "", nil
		if c.setup != nil {
			c.setup()
		}
		dest, jar := e.login(c.mutate)
		u, _ := url.Parse(dest)
		if !strings.HasPrefix(dest, "/?sso_error=") || !strings.Contains(u.Query().Get("sso_error"), c.want) {
			t.Errorf("%s: redirected to %q", c.name, dest)
		}
		if sessionOf(jar, e.srv.URL) != "" {
			t.Errorf("%s: a session was created", c.name)
		}
		if code, _ := e.me(); code != 401 {
			t.Errorf("%s: /auth/me -> %d", c.name, code)
		}
	}
	all, _ := e.s.store.ListUsers(t.Context(), true)
	if len(all) != 0 {
		t.Fatalf("rejected logins provisioned users: %v", all)
	}
	// Callback without the state cookie.
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).
		Get(e.srv.URL + "/auth/oidc/callback?code=x&state=y")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "sso_error") {
		t.Fatalf("callback without state cookie -> %s", loc)
	}
	// Open redirects are refused.
	for _, rt := range []string{"//evil.example/x", "https://evil.example", "/\\evil.example"} {
		if got := safeReturn(rt); got != "/" {
			t.Errorf("safeReturn(%q) = %q", rt, got)
		}
	}
	e.s.FlushAudit()
	if ents, _, _ := e.s.store.QueryAudit(t.Context(), store.AuditFilter{Outcome: "unauthenticated", PathPrefix: "/auth/oidc/"}); len(ents) < len(cases) {
		t.Fatalf("failed sign-ins audited: %d", len(ents))
	}
}

func TestOIDCES256AndLinking(t *testing.T) {
	e := newSSOEnv(t, "ES256", func(c *OIDCConfig) { c.GroupRoles = nil })
	// An admin pre-created this user; SSO links to it by verified email.
	if _, err := e.s.store.CreateUser(t.Context(), store.UserCreate{ID: "jdoe", Email: ptr("jane@example.com")}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.store.SetProjectRole(t.Context(), "acme/app", "jdoe", store.RoleViewer); err != nil {
		t.Fatal(err)
	}
	dest, _ := e.login(nil)
	if dest != "/acme/app/traces" {
		t.Fatalf("ES256 login -> %s", dest)
	}
	_, me := e.me()
	if me["user"].(map[string]any)["id"] != "jdoe" {
		t.Fatalf("not linked: %v", me)
	}
	// The session's roles apply: viewer can read but not write acme/app.
	jar := e.client.Jar
	u, _ := url.Parse(e.srv.URL)
	var csrf string
	for _, c := range jar.Cookies(u) {
		if c.Name == csrfCookie {
			csrf = c.Value
		}
	}
	req, _ := http.NewRequest("POST", e.srv.URL+"/call/start", strings.NewReader(`{"start":{"project_id":"acme/app","op_name":"x"}}`))
	req.Header.Set(csrfHeader, csrf)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("viewer session write -> %d", resp.StatusCode)
	}
	// Disabled users cannot sign in.
	if _, err := e.s.store.UpdateUser(t.Context(), "jdoe", store.UserUpdate{Disabled: ptrBool(true)}); err != nil {
		t.Fatal(err)
	}
	if dest, _ := e.login(nil); !strings.Contains(dest, "disabled") {
		t.Fatalf("disabled login -> %s", dest)
	}
}

func TestParseGroupRoles(t *testing.T) {
	got, err := ParseGroupRoles("admins=admin, ml=member@acme,ro=viewer@acme")
	if err != nil || len(got) != 3 || got[0].Entity != "" || got[1].Entity != "acme" || got[2].Role != store.RoleViewer {
		t.Fatalf("got %v %v", got, err)
	}
	for _, bad := range []string{"x=member", "x=owner@acme", "=admin", "x"} {
		if _, err := ParseGroupRoles(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func ptr(s string) *string { return &s }
func ptrBool(b bool) *bool { return &b }

// TestPublicRoutesStayPublic: routes registered through publicRouteExtensions
// (SSO, /version, /geolocate) need no key or session even with keys and SSO on.
func TestPublicRoutesStayPublic(t *testing.T) {
	e := newSSOEnv(t, "RS256", nil)
	for _, p := range []string{"/version", "/geolocate", "/auth/sso", "/health", "/server_info"} {
		if code, out := e.admDo("GET", p, "", nil); code != 200 {
			t.Errorf("GET %s without credentials -> %d %s", p, code, out)
		}
	}
	if code, _ := e.admDo("GET", "/projects", "", nil); code != 401 {
		t.Fatalf("/projects should need auth: %d", code)
	}
	e2, _ := newIAMEnv(t, IAMConfig{})
	if code, _ := e2.admDo("GET", "/version", "", nil); code != 200 {
		t.Fatalf("/version with static keys -> %d", code)
	}
}
