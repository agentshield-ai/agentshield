package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// A3: an ID token without email_verified does not link to (take over) an
// existing account, nor pass the allowed domains, unless the operator says
// the IdP only issues verified emails.
func TestOIDCAbsentEmailVerifiedIsUnverified(t *testing.T) {
	precreate := func(e *ssoEnv) {
		t.Helper()
		if _, err := e.s.store.CreateUser(t.Context(), store.UserCreate{ID: "ceo", Email: ptr("jane@example.com"), Admin: true}); err != nil {
			t.Fatal(err)
		}
	}
	// With allowed domains: refused.
	e := newSSOEnv(t, "RS256", func(c *OIDCConfig) { c.GroupRoles = nil })
	precreate(e)
	e.idp.claims = map[string]any{"email_verified": nil, "sub": "attacker-sub"}
	if dest, _ := e.login(nil); !strings.Contains(dest, "sso_error") {
		t.Fatalf("unverified email with allowed domains -> %s", dest)
	}
	// Without: a separate account, not the admin's.
	e = newSSOEnv(t, "RS256", func(c *OIDCConfig) { c.GroupRoles, c.AllowedDomains = nil, nil })
	precreate(e)
	e.idp.claims = map[string]any{"email_verified": nil, "sub": "attacker-sub"}
	if dest, _ := e.login(nil); strings.Contains(dest, "sso_error") {
		t.Fatalf("login -> %s", dest)
	}
	if _, me := e.me(); me["user"].(map[string]any)["id"] == "ceo" || me["server_admin"] == true {
		t.Fatalf("unverified email took over the admin account: %v", me)
	}
	if u, _ := e.s.store.GetUser(t.Context(), "ceo"); u.Subject != "" {
		t.Fatalf("admin account linked to %s", u.Subject)
	}
	// email_verified: "false" (string) is unverified too.
	e = newSSOEnv(t, "RS256", func(c *OIDCConfig) { c.GroupRoles, c.AllowedDomains = nil, nil })
	precreate(e)
	e.idp.claims = map[string]any{"email_verified": "false", "sub": "attacker-sub"}
	e.login(nil)
	if _, me := e.me(); me["user"].(map[string]any)["id"] == "ceo" {
		t.Fatal("email_verified=\"false\" linked the account")
	}
	// The operator's opt-in: the IdP only issues verified addresses.
	e = newSSOEnv(t, "RS256", func(c *OIDCConfig) { c.GroupRoles, c.AssumeEmailVerified = nil, true })
	if _, err := e.s.store.CreateUser(t.Context(), store.UserCreate{ID: "jdoe", Email: ptr("jane@example.com")}); err != nil {
		t.Fatal(err)
	}
	e.idp.claims = map[string]any{"email_verified": nil}
	if dest, _ := e.login(nil); strings.Contains(dest, "sso_error") {
		t.Fatalf("assumed-verified login -> %s", dest)
	}
	if _, me := e.me(); me["user"].(map[string]any)["id"] != "jdoe" {
		t.Fatalf("assumed-verified email did not link: %v", me)
	}
	// Explicitly verified still links (unchanged).
	e = newSSOEnv(t, "RS256", func(c *OIDCConfig) { c.GroupRoles = nil })
	if _, err := e.s.store.CreateUser(t.Context(), store.UserCreate{ID: "jdoe", Email: ptr("jane@example.com")}); err != nil {
		t.Fatal(err)
	}
	e.login(nil)
	if _, me := e.me(); me["user"].(map[string]any)["id"] != "jdoe" {
		t.Fatalf("verified email did not link: %v", me)
	}
}

// A5: return_to stays on this site, whatever browsers strip from URLs.
func TestSafeReturn(t *testing.T) {
	for in, want := range map[string]string{
		"/acme/app/traces?x=1":          "/acme/app/traces?x=1",
		"/\t/evil.example":              "/",
		"/\n/evil.example":              "/",
		"/\r/evil.example":              "/",
		"/\x00/evil.example":            "/",
		"/\x7f/evil.example":            "/",
		"//evil.example":                "/",
		"/\\evil.example":               "/",
		"https://evil.example/":         "/",
		"evil.example":                  "/",
		"/%09/evil.example":             "/%09/evil.example", // stays a path: browsers do not decode it
		"/a/b#frag":                     "/a/b#frag",
		"":                              "/",
		"/" + strings.Repeat("a", 2000): "/",
	} {
		if got := safeReturn(in); got != want {
			t.Errorf("safeReturn(%q) = %q, want %q", in, got, want)
		}
	}
	// End to end: the callback's redirect stays on site.
	e := newSSOEnv(t, "RS256", func(c *OIDCConfig) { c.GroupRoles = nil })
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(e.srv.URL + "/auth/oidc/login?return_to=" + url.QueryEscape("/\t/evil.example/landing"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var sealed string
	for _, ck := range resp.Cookies() {
		if ck.Name == stateCookie {
			sealed = ck.Value
		}
	}
	plain, err := e.s.open("weft-oidc-state-v1", sealed)
	if err != nil || !strings.Contains(string(plain), `"r":"/"`) {
		t.Fatalf("state return_to = %s %v", plain, err)
	}
}
