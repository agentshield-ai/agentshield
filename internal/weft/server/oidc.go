package server

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDC single sign-on for the web UI (authorization-code flow with PKCE)
// and the browser sessions it creates.
//
//   - GET /auth/oidc/login?return_to=/path redirects to the identity
//     provider with state, nonce and an S256 code challenge. They and the
//     PKCE verifier travel in a short-lived AES-GCM-sealed cookie.
//   - GET /auth/oidc/callback checks state, exchanges the code with the
//     verifier, verifies the ID token (issuer, audience, expiry, RS256/ES256
//     signature from the provider's JWKS) and nonce, applies the allowed
//     domains and group mappings, provisions or links the user and starts a
//     session.
//   - The session cookie (HttpOnly, SameSite=Lax, Secure on TLS) holds a
//     random id and its HMAC; only the id's SHA-256 is stored. A second,
//     script-readable cookie carries the session's CSRF token, which
//     cookie-authenticated non-GET requests must echo in X-Weft-CSRF.

// OIDCConfig configures single sign-on.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string // e.g. https://weft.example.com/auth/oidc/callback
	Scopes       []string
	// AllowedDomains limits sign-in to verified emails of these domains.
	AllowedDomains []string
	// GroupsClaim names the ID-token claim holding the user's groups (default "groups").
	GroupsClaim string
	// GroupRoles maps identity-provider groups to roles.
	GroupRoles []GroupRole
	// AdminEmails become server admins when they sign in with a verified email.
	AdminEmails []string
	// HTTPClient is used for discovery, JWKS and the token exchange (tests).
	// The issuer is operator configuration (--oidc-issuer), so these requests
	// do not go through the egress guard (an in-cluster IdP is common).
	HTTPClient *http.Client
}

// GroupRole maps a group to a role on a team (entity), or to server admin
// when Entity is empty.
type GroupRole struct {
	Group  string
	Role   store.Role
	Entity string
}

// ParseGroupRoles parses "group=admin" (server admin) and
// "group=role@entity" entries, comma-separated.
func ParseGroupRoles(s string) ([]GroupRole, error) {
	var out []GroupRole
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		g, spec, ok := strings.Cut(part, "=")
		g = strings.TrimSpace(g)
		if !ok || g == "" {
			return nil, fmt.Errorf("invalid group mapping %q: want group=admin or group=role@entity", part)
		}
		roleName, entity, hasEntity := strings.Cut(strings.TrimSpace(spec), "@")
		role, err := store.ParseRole(roleName)
		if err != nil {
			return nil, fmt.Errorf("invalid group mapping %q: %v", part, err)
		}
		if !hasEntity && role != store.RoleAdmin {
			return nil, fmt.Errorf("invalid group mapping %q: %s needs a team, e.g. %s=%s@my-team", part, role, g, role)
		}
		if hasEntity && !store.TeamNameRe.MatchString(entity) {
			return nil, fmt.Errorf("invalid group mapping %q: bad team name", part)
		}
		out = append(out, GroupRole{Group: g, Role: role, Entity: entity})
	}
	return out, nil
}

const (
	sessionCookie = "weft_session"
	csrfCookie    = "weft_csrf"
	stateCookie   = "weft_oidc"
	csrfHeader    = "X-Weft-CSRF"
)

// ---------------------------------------------------------------- client

type oidcClient struct {
	cfg OIDCConfig
	log *slog.Logger

	mu       sync.Mutex
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    *oauth2.Config
}

func newOIDCClient(cfg OIDCConfig, log *slog.Logger) *oidcClient {
	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = "groups"
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	}
	return &oidcClient{cfg: cfg, log: log}
}

func (c *oidcClient) withClient(ctx context.Context) context.Context {
	if c.cfg.HTTPClient != nil {
		ctx = oidc.ClientContext(ctx, c.cfg.HTTPClient)
		ctx = context.WithValue(ctx, oauth2.HTTPClient, c.cfg.HTTPClient)
	}
	return ctx
}

// ensure runs discovery (.well-known/openid-configuration) once it succeeds.
func (c *oidcClient) ensure(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.provider != nil {
		return nil
	}
	dctx, cancel := context.WithTimeout(c.withClient(ctx), 15*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(dctx, c.cfg.Issuer)
	if err != nil {
		return fmt.Errorf("OIDC discovery: %w", err)
	}
	c.provider = p
	c.verifier = p.Verifier(&oidc.Config{ClientID: c.cfg.ClientID, SupportedSigningAlgs: []string{oidc.RS256, oidc.ES256}})
	c.oauth = &oauth2.Config{ClientID: c.cfg.ClientID, ClientSecret: c.cfg.ClientSecret, Endpoint: p.Endpoint(),
		RedirectURL: c.cfg.RedirectURL, Scopes: c.cfg.Scopes}
	return nil
}

// ---------------------------------------------------------------- keys and cookies

func (s *Server) deriveKey(label string) []byte {
	m := hmac.New(sha256.New, s.iam().sessionKey)
	m.Write([]byte(label))
	return m.Sum(nil)
}

func (s *Server) signSession(id string) string {
	m := hmac.New(sha256.New, s.deriveKey("weft-session-cookie-v1"))
	m.Write([]byte(id))
	return id + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *Server) verifySession(v string) (string, bool) {
	id, _, ok := strings.Cut(v, ".")
	if !ok || id == "" || len(v) > 256 {
		return "", false
	}
	want := s.signSession(id)
	return id, subtle.ConstantTimeCompare([]byte(want), []byte(v)) == 1
}

func hashSessionID(id string) string {
	h := sha256.Sum256([]byte(id))
	return hex.EncodeToString(h[:])
}

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) seal(label string, plain []byte) (string, error) {
	block, err := aes.NewCipher(s.deriveKey(label))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(gcm.Seal(nonce, nonce, plain, []byte(label))), nil
}

func (s *Server) open(label, sealed string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(s.deriveKey(label))
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, errors.New("short")
	}
	return gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(label))
}

func (s *Server) secureCookies(r *http.Request) bool {
	if s.cfg.IAM.SecureCookies || r.TLS != nil {
		return true
	}
	o := s.cfg.IAM.OIDC
	return o != nil && strings.HasPrefix(o.RedirectURL, "https://")
}

func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, name, value, path string, maxAge int, httpOnly bool) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: path, MaxAge: maxAge, HttpOnly: httpOnly,
		Secure: s.secureCookies(r), SameSite: http.SameSiteLaxMode})
}

func (s *Server) sessionTTL() time.Duration {
	if d := s.cfg.IAM.SessionTTL; d > 0 {
		return d
	}
	return 12 * time.Hour
}

// StartSession creates a session for a user and sets its cookies.
func (s *Server) StartSession(w http.ResponseWriter, r *http.Request, userID string) error {
	id, csrf := randToken(32), randToken(32)
	ttl := s.sessionTTL()
	err := s.store.CreateSession(r.Context(), store.Session{IDHash: hashSessionID(id), UserID: userID, CSRF: csrf,
		ExpiresAt: time.Now().Add(ttl).UnixMicro(), IP: clientIP(r), UserAgent: r.UserAgent()})
	if err != nil {
		return err
	}
	s.setCookie(w, r, sessionCookie, s.signSession(id), "/", int(ttl.Seconds()), true)
	s.setCookie(w, r, csrfCookie, csrf, "/", int(ttl.Seconds()), false)
	return nil
}

func (s *Server) clearSessionCookies(w http.ResponseWriter, r *http.Request) {
	s.setCookie(w, r, sessionCookie, "", "/", -1, true)
	s.setCookie(w, r, csrfCookie, "", "/", -1, false)
}

// ---------------------------------------------------------------- session auth

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// sessionAuth authenticates a session cookie. Non-GET requests must carry
// the session's CSRF token in X-Weft-CSRF, and a foreign Origin is refused.
func (s *Server) sessionAuth(w http.ResponseWriter, r *http.Request, raw string) *http.Request {
	unauth := func(msg string) *http.Request {
		s.clearSessionCookies(w, r)
		writeError(w, http.StatusUnauthorized, msg)
		return nil
	}
	id, ok := s.verifySession(raw)
	if !ok {
		return unauth("invalid session; sign in again")
	}
	sess, err := s.store.GetSession(r.Context(), hashSessionID(id))
	if errors.Is(err, store.ErrNotFound) {
		return unauth("session expired or signed out; sign in again")
	}
	if err != nil {
		writeErr(w, err)
		return nil
	}
	u, roles, err := s.identity(r.Context(), sess.UserID)
	if err != nil || u.Disabled() {
		return unauth("this user is disabled")
	}
	if note := auditNoteFrom(r); note != nil {
		note.actor, note.authMethod = u.ID, "session"
	}
	if !isSafeMethod(r.Method) {
		got := r.Header.Get(csrfHeader)
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(sess.CSRF)) != 1 {
			writeError(w, http.StatusForbidden, "missing or invalid CSRF token (send the weft_csrf cookie value in X-Weft-CSRF)")
			return nil
		}
		if o := r.Header.Get("Origin"); o != "" && !sameOrigin(o, r) && !s.originAllowed(o) {
			writeError(w, http.StatusForbidden, "cross-origin request refused")
			return nil
		}
	}
	p := &principal{user: u.ID, super: u.Admin, rbac: true, roles: roles, method: "session", session: sess}
	return s.finishAuth(w, r, p)
}

func sameOrigin(origin string, r *http.Request) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// ---------------------------------------------------------------- handlers

func (s *Server) ssoInfo(w http.ResponseWriter, _ *http.Request) {
	out := map[string]any{"enabled": s.cfg.IAM.OIDC != nil}
	if o := s.cfg.IAM.OIDC; o != nil {
		out["login_url"] = "/auth/oidc/login"
		out["issuer"] = o.Issuer
	}
	writeJSON(w, out)
}

type oidcState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	ReturnTo string `json:"r"`
	Expires  int64  `json:"e"`
}

// safeReturn keeps redirects on this site: a path starting with one "/".
func safeReturn(v string) string {
	if v == "" || !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || strings.ContainsAny(v, "\\\r\n") || len(v) > 1024 {
		return "/"
	}
	return v
}

func (s *Server) oidcLogin(w http.ResponseWriter, r *http.Request) {
	c := s.iam().oidc
	if c == nil {
		writeError(w, http.StatusNotFound, "single sign-on is not configured")
		return
	}
	if err := c.ensure(r.Context()); err != nil {
		s.log.Error("OIDC discovery failed", "issuer", c.cfg.Issuer, "err", err)
		writeError(w, http.StatusBadGateway, "the identity provider is unavailable")
		return
	}
	st := oidcState{State: randToken(24), Nonce: randToken(24), Verifier: oauth2.GenerateVerifier(),
		ReturnTo: safeReturn(r.URL.Query().Get("return_to")), Expires: time.Now().Add(10 * time.Minute).Unix()}
	b, _ := json.Marshal(st)
	sealed, err := s.seal("weft-oidc-state-v1", b)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.setCookie(w, r, stateCookie, sealed, "/auth/oidc/", 600, true)
	http.Redirect(w, r, c.oauth.AuthCodeURL(st.State, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier)), http.StatusFound)
}

// loginError is a sign-in failure shown to the user (no internals).
type loginError struct{ msg string }

func (e *loginError) Error() string { return e.msg }

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	c := s.iam().oidc
	if c == nil {
		writeError(w, http.StatusNotFound, "single sign-on is not configured")
		return
	}
	s.setCookie(w, r, stateCookie, "", "/auth/oidc/", -1, true)
	actor, returnTo := "", "/"
	fail := func(err error) {
		msg := "sign-in failed"
		var le *loginError
		if errors.As(err, &le) {
			msg = le.msg
		} else {
			s.log.Warn("OIDC sign-in failed", "err", err)
		}
		s.recordAudit(store.AuditEntry{Actor: orAnon(actor), AuthMethod: "sso", Method: r.Method, Path: r.URL.Path,
			Status: http.StatusUnauthorized, Outcome: "unauthenticated", IP: clientIP(r), Detail: "SSO sign-in failed: " + msg})
		http.Redirect(w, r, "/?sso_error="+url.QueryEscape(msg), http.StatusFound)
	}
	if e := r.URL.Query().Get("error"); e != "" {
		fail(&loginError{"the identity provider returned an error: " + clip(e, 100)})
		return
	}
	ck, err := r.Cookie(stateCookie)
	if err != nil {
		fail(&loginError{"sign-in state is missing or expired; try again"})
		return
	}
	plain, err := s.open("weft-oidc-state-v1", ck.Value)
	var st oidcState
	if err != nil || json.Unmarshal(plain, &st) != nil || time.Now().Unix() > st.Expires {
		fail(&loginError{"sign-in state is invalid or expired; try again"})
		return
	}
	returnTo = st.ReturnTo
	if q := r.URL.Query().Get("state"); q == "" || subtle.ConstantTimeCompare([]byte(q), []byte(st.State)) != 1 {
		fail(&loginError{"sign-in state does not match; try again"})
		return
	}
	if err := c.ensure(r.Context()); err != nil {
		fail(err)
		return
	}
	ctx, cancel := context.WithTimeout(c.withClient(r.Context()), 30*time.Second)
	defer cancel()
	tok, err := c.oauth.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		fail(fmt.Errorf("token exchange: %w", err))
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		fail(&loginError{"the identity provider returned no ID token"})
		return
	}
	idt, err := c.verifier.Verify(ctx, rawID)
	if err != nil {
		fail(&loginError{"the ID token is invalid: " + idTokenProblem(err)})
		return
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(st.Nonce)) != 1 {
		fail(&loginError{"the ID token nonce does not match; try again"})
		return
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		fail(err)
		return
	}
	u, err := s.provisionSSOUser(r.Context(), c, idt.Issuer+"|"+idt.Subject, claims)
	if u != nil {
		actor = u.ID
	} else if e, _ := claims["email"].(string); e != "" {
		actor = clip(e, 128)
	}
	if err != nil {
		fail(err)
		return
	}
	if err := s.StartSession(w, r, u.ID); err != nil {
		fail(err)
		return
	}
	s.recordAudit(store.AuditEntry{Actor: u.ID, AuthMethod: "sso", Method: r.Method, Path: r.URL.Path, Status: http.StatusFound,
		Outcome: "success", IP: clientIP(r), Detail: "signed in with SSO"})
	http.Redirect(w, r, returnTo, http.StatusFound)
}

func idTokenProblem(err error) string {
	var exp *oidc.TokenExpiredError
	msg := err.Error()
	switch {
	case errors.As(err, &exp):
		return "it has expired"
	case strings.Contains(msg, "audience"):
		return "it was issued for another client (audience)"
	case strings.Contains(msg, "issuer"):
		return "it was issued by another issuer"
	case strings.Contains(msg, "signature") || strings.Contains(msg, "signing"):
		return "its signature does not verify"
	}
	return "verification failed"
}

func orAnon(v string) string {
	if v == "" {
		return "anonymous"
	}
	return v
}

func clip(v string, n int) string {
	if len(v) > n {
		return v[:n]
	}
	return v
}

var idCleanRe = regexp.MustCompile(`[^A-Za-z0-9_.@-]+`)

// provisionSSOUser finds, links or creates the user for an identity, after
// applying the allowed domains and group mappings, and syncs SSO roles.
func (s *Server) provisionSSOUser(ctx context.Context, c *oidcClient, subject string, claims map[string]any) (*store.User, error) {
	email, _ := claims["email"].(string)
	email = strings.ToLower(strings.TrimSpace(email))
	verified, hasVerified := claims["email_verified"].(bool)
	if !hasVerified {
		if v, ok := claims["email_verified"].(string); ok {
			verified, hasVerified = v == "true", true
		}
	}
	emailOK := email != "" && (!hasVerified || verified)
	groups := claimStrings(claims[c.cfg.GroupsClaim])

	domainOK := false
	if len(c.cfg.AllowedDomains) > 0 {
		if !emailOK {
			return nil, &loginError{"a verified email address is required to sign in"}
		}
		_, dom, _ := strings.Cut(email, "@")
		for _, d := range c.cfg.AllowedDomains {
			if strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(d), "@"), dom) {
				domainOK = true
			}
		}
		if !domainOK {
			return nil, &loginError{"accounts of this email domain may not sign in"}
		}
	}
	serverAdmin, adminMapped := false, false
	teamRoles := map[string]store.Role{}
	matched := false
	for _, gr := range c.cfg.GroupRoles {
		if gr.Entity == "" {
			adminMapped = true
		}
		for _, g := range groups {
			if g != gr.Group {
				continue
			}
			matched = true
			if gr.Entity == "" {
				serverAdmin = true
			} else {
				teamRoles[gr.Entity] = store.MaxRole(teamRoles[gr.Entity], gr.Role)
			}
		}
	}
	for _, a := range c.cfg.AdminEmails {
		if emailOK && hasVerified && verified && strings.EqualFold(strings.TrimSpace(a), email) {
			serverAdmin, matched = true, true
		}
	}
	if len(c.cfg.AllowedDomains) == 0 && len(c.cfg.GroupRoles) > 0 && !matched {
		return nil, &loginError{"you are not in a group that may use this server"}
	}

	u, err := s.store.GetUserBySubject(ctx, subject)
	if errors.Is(err, store.ErrNotFound) {
		u = nil
		// Link an account an admin created for this email, when the email is trusted.
		if emailOK && (verified || domainOK) {
			if x, err := s.store.GetUserByEmail(ctx, email); err == nil && x.Subject == "" {
				if err := s.store.LinkUserSubject(ctx, x.ID, subject); err != nil {
					return nil, err
				}
				u = x
			} else if err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
		}
		if u == nil {
			u, err = s.createSSOUser(ctx, subject, email, emailOK, claims)
			if err != nil {
				return nil, err
			}
		}
	} else if err != nil {
		return nil, err
	} else if err := s.store.LinkUserSubject(ctx, u.ID, subject); err != nil {
		return nil, err
	}
	if u.Disabled() {
		return u, &loginError{"this account is disabled"}
	}
	if (adminMapped && u.Admin != serverAdmin) || (serverAdmin && !u.Admin) {
		if _, err := s.store.UpdateUser(ctx, u.ID, store.UserUpdate{Admin: &serverAdmin}); err != nil {
			return u, err
		}
	}
	if len(c.cfg.GroupRoles) > 0 {
		if err := s.store.SyncSSOMemberships(ctx, u.ID, teamRoles); err != nil {
			return u, err
		}
	}
	s.invalidateIdentities()
	return s.store.GetUser(ctx, u.ID)
}

func (s *Server) createSSOUser(ctx context.Context, subject, email string, emailOK bool, claims map[string]any) (*store.User, error) {
	base, _ := claims["preferred_username"].(string)
	if base == "" && email != "" {
		base, _, _ = strings.Cut(email, "@")
	}
	base = strings.Trim(idCleanRe.ReplaceAllString(base, "-"), "-.")
	if base == "" {
		base = "user"
	}
	if len(base) > 48 {
		base = base[:48]
	}
	name, _ := claims["name"].(string)
	if name == "" {
		name = base
	}
	var em *string
	if emailOK {
		if _, err := s.store.GetUserByEmail(ctx, email); errors.Is(err, store.ErrNotFound) {
			em = &email
		}
	}
	for i := 0; i < 20; i++ {
		id := base
		if i > 0 {
			id = fmt.Sprintf("%s-%d", base, i+1)
		}
		if _, err := s.store.GetUser(ctx, id); err == nil {
			continue
		}
		u, err := s.store.CreateUser(ctx, store.UserCreate{ID: id, Name: clip(name, 128), Email: em, Subject: subject})
		if err == nil {
			_ = s.store.LinkUserSubject(ctx, u.ID, subject)
			return u, nil
		}
		var inv *store.InvalidError
		if !errors.As(err, &inv) {
			return nil, err
		}
	}
	return nil, &loginError{"could not create an account; ask an admin"}
}

func claimStrings(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, y := range x {
			if s, ok := y.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// logout ends the browser session and clears its cookies.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie(sessionCookie); err == nil {
		if id, ok := s.verifySession(ck.Value); ok {
			if sess, err := s.store.GetSession(r.Context(), hashSessionID(id)); err == nil {
				got := r.Header.Get(csrfHeader)
				if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(sess.CSRF)) != 1 {
					writeError(w, http.StatusForbidden, "missing or invalid CSRF token")
					return
				}
				if err := s.store.RevokeSession(r.Context(), sess.IDHash); err != nil {
					writeErr(w, err)
					return
				}
				s.recordAudit(store.AuditEntry{Actor: sess.UserID, AuthMethod: "session", Method: r.Method, Path: r.URL.Path,
					Status: http.StatusOK, Outcome: "success", IP: clientIP(r), Detail: "signed out"})
			}
		}
	}
	s.clearSessionCookies(w, r)
	writeJSON(w, map[string]any{"ok": true})
}
