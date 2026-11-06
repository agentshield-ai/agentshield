package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/compat"
	"github.com/agentshield-ai/agentshield/internal/weft/otlp"
	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/agentshield-ai/agentshield/internal/weft/weftlog"
	"github.com/go-chi/chi/v5"
)

// Administration: managed API keys (hashed, scoped), project settings
// (retention TTL, ingest sampling, sensitive-data redaction), project delete
// and the background retention purge.

func init() {
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		r.Get("/auth/principal", s.authPrincipal)
		r.Post("/api_keys/create", handleIAM(s.apiKeyCreate))
		r.Get("/api_keys/list", s.apiKeyList)
		r.Post("/api_keys/list", s.apiKeyList)
		r.Post("/api_keys/revoke", handleIAM(s.apiKeyRevoke))

		r.Post("/project/ttl_settings/read", handle(s.ttlRead))
		r.Post("/project/ttl_settings/update", handle(s.ttlUpdate))
		r.Post("/project/ttl_settings/purge", handle(s.ttlPurge))
		r.Post("/project/ingest_sampling_settings/read", handle(s.samplingRead))
		r.Post("/project/ingest_sampling_settings/update", handle(s.samplingUpdate))
		r.Get("/project/sensitive_data_settings", s.sensitiveRead)
		r.Post("/project/sensitive_data_settings/read", handle(s.sensitiveReadPost))
		r.Post("/project/sensitive_data_settings/update", handle(s.sensitiveUpdate))
		r.Post("/project/sensitive_data_settings/preview", handle(s.sensitivePreview))
		r.Post("/project/delete", handle(s.projectDelete))
	})
	startExtensions = append(startExtensions, startRetention)
}

// ---------------------------------------------------------------- principal

// principal is the caller authenticated by a managed key or a session
// cookie. Requests with a static --api-keys key, or to a server without
// auth, carry no principal and are treated as admin. See iam.go.
type principal struct {
	keyID    string
	user     string
	super    bool // admin key, or a key/session of a server-admin user
	readOnly bool
	projects map[string]bool // key scope; nil: all projects
	// rbac principals (user-bound keys and sessions) take their access from
	// the user's team and project roles.
	rbac    bool
	roles   *store.UserRoles
	method  string // "api_key" or "session"
	session *store.Session
}

type principalKey struct{}

func principalFrom(r *http.Request) *principal {
	p, _ := r.Context().Value(principalKey{}).(*principal)
	return p
}

// keyCache holds the active managed keys of one server.
type keyCache struct {
	mu        sync.Mutex
	loaded    bool
	keys      []store.APIKey
	hashes    [][]byte
	lastTouch map[string]time.Time
}

var keyCaches sync.Map // *Server -> *keyCache

func (s *Server) keyCache() *keyCache {
	c, _ := keyCaches.LoadOrStore(s, &keyCache{lastTouch: map[string]time.Time{}})
	return c.(*keyCache)
}

func (s *Server) activeKeys() ([]store.APIKey, [][]byte) {
	c := s.keyCache()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded {
		ks, err := s.store.ListAPIKeys(context.Background(), false)
		if err != nil {
			s.log.Error("loading API keys", "err", err)
			return nil, nil
		}
		c.keys, c.hashes, c.loaded = ks, make([][]byte, len(ks)), true
		for i, k := range ks {
			c.hashes[i] = []byte(k.Hash)
		}
	}
	return c.keys, c.hashes
}

func (s *Server) invalidateKeys() {
	c := s.keyCache()
	c.mu.Lock()
	c.loaded = false
	c.mu.Unlock()
}

// authRequired reports whether requests need a key: static keys or SSO are
// configured, or at least one managed key is active.
// AuthRequired reports whether requests must authenticate (static keys,
// managed keys or SSO).
func (s *Server) AuthRequired() bool { return s.authRequired() }

func (s *Server) authRequired() bool {
	if len(s.keys) > 0 || s.cfg.IAM.OIDC != nil {
		return true
	}
	ks, _ := s.activeKeys()
	return len(ks) > 0
}

// dbKeyAuth authenticates a managed key and enforces its scope. It returns
// the request carrying the principal, or nil after writing 401/403.
func (s *Server) dbKeyAuth(w http.ResponseWriter, r *http.Request, presented string) *http.Request {
	var match *store.APIKey
	if presented != "" {
		h := []byte(store.HashAPIKey(presented))
		keys, hashes := s.activeKeys()
		for i := range keys {
			if subtle.ConstantTimeCompare(h, hashes[i]) == 1 {
				match = &keys[i]
			}
		}
	}
	if match == nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="weft"`)
		writeError(w, http.StatusUnauthorized, "missing or invalid API key")
		return nil
	}
	p := &principal{keyID: match.ID, user: match.UserID, super: match.Admin, readOnly: match.ReadOnly, method: "api_key"}
	if !match.AllProjects {
		p.projects = map[string]bool{}
		for _, pid := range match.Projects {
			p.projects[pid] = true
		}
	}
	if match.UserBound {
		u, roles, err := s.identity(r.Context(), match.UserID)
		if err != nil || u.Disabled() {
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				writeErr(w, err)
				return nil
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="weft"`)
			writeError(w, http.StatusUnauthorized, "the user of this API key is disabled")
			return nil
		}
		p.rbac, p.roles, p.super = true, roles, u.Admin
	}
	s.touchKey(match.ID)
	return s.finishAuth(w, r, p)
}

// touchKey records last use at most once a minute per key.
func (s *Server) touchKey(id string) {
	c := s.keyCache()
	c.mu.Lock()
	last := c.lastTouch[id]
	if time.Since(last) < time.Minute {
		c.mu.Unlock()
		return
	}
	c.lastTouch[id] = time.Now()
	c.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.store.TouchAPIKey(ctx, id); err != nil {
			s.log.Debug("recording API key use", "err", err)
		}
	}()
}

// readSegments are final path segments of POST endpoints that only read.
// Nothing here may write or have side effects (POST /automations/check
// fires webhooks and records runs, so it needs write).
var readSegments = map[string]bool{
	"query": true, "read": true, "stream_query": true, "query_stats": true, "stats": true, "read_batch": true,
	"content": true, "compare": true, "compute": true, "status": true, "usage": true, "aggregate": true,
	"payload_schema": true, "query_stats_batch": true, "op_counts": true, "source_datasets_query": true,
	"search": true, "list": true, "projects_info": true, "export": true, "metrics": true, "graphql": true,
	"evaluation_status": true, "preview": true, "whoami": true, "principal": true,
}

func isReadRequest(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	case http.MethodPost:
		path := strings.TrimSuffix(r.URL.Path, "/")
		if isOTLPPath(path) { // ingest (otlp_logs.go), whatever its last segment
			return false
		}
		if strings.HasPrefix(path, "/api/v1/") {
			return nativeReadPaths[path] // native.go
		}
		return readSegments[path[strings.LastIndex(path, "/")+1:]]
	}
	return false
}

// projectFreePaths may be called by project-scoped keys without naming a project.
var projectFreePaths = map[string]bool{"/auth/check": true, "/auth/whoami": true, "/auth/principal": true, "/projects": true,
	"/llm/models": true, "/graphql": true, "/secrets/list": true, "/server_info": true, "/health": true,
	"/api/v1/help": true, "/api/v1/secrets/list": true} // secrets lists check access themselves (secrets.go)

// projectFreePath reports routes that need no project: projectFreePaths
// and the help guides (/api/v1/help/{name}).
func projectFreePath(path string) bool {
	return projectFreePaths[path] || strings.HasPrefix(path, "/api/v1/help/")
}

// requestProjects finds every project a request names: the project_id query
// parameter and headers, the /v2/{entity}/{project}/ and
// /v1/entities/{entity}/projects/{project} paths of the adapter's routes,
// the projects a JSON body names (probeBody, projectguard.go), or a
// multipart form's project_id. It fails closed: a JSON body that is not
// exactly one value, or whose project-bearing fields have unexpected types,
// is refused (requestError 400), and so is a GET body naming a project
// (GET handlers never read bodies). The body is restored, decompressed.
func requestProjects(w http.ResponseWriter, r *http.Request) ([]string, error) {
	var raw []string
	add := func(v string) {
		if v != "" {
			raw = append(raw, v)
		}
	}
	note := auditNoteFrom(r)
	bodyFrom := -1 // index in raw of the first project the body named
	var body []byte
	refuse := func(err error) ([]string, error) {
		if note != nil {
			if len(body) > 0 && bodyFrom == len(raw) {
				probeLenient(body, add) // what the refused body named, for the audit log
			}
			// Refusals are audited with what the body named first.
			note.detail = "refused: " + err.Error()
			ordered := raw
			if bodyFrom >= 0 && bodyFrom < len(raw) {
				ordered = append(append([]string{}, raw[bodyFrom:]...), raw[:bodyFrom]...)
			}
			note.projects = normalizedProjects(ordered)
		}
		return nil, err
	}
	add(r.URL.Query().Get("project_id"))
	if isOTLPPath(r.URL.Path) {
		hp := otlpHeaderProject(r)
		if hp == "" {
			return nil, errors.New("this API key is limited to specific projects; send the project_id header")
		}
		add(hp)
	}
	if parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/"); len(parts) >= 3 && parts[0] == "v2" {
		add(parts[1] + "/" + parts[2])
	}
	// Bulk export: /v1/entities/{entity}/projects/{project}[:export|/exports/...]
	if parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/"); len(parts) >= 5 && parts[0] == "v1" && parts[1] == "entities" && parts[3] == "projects" {
		p, _, _ := strings.Cut(parts[4], ":")
		add(parts[2] + "/" + p)
	}
	ct := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "multipart/form-data"):
		r.Body = http.MaxBytesReader(w, r.Body, 65<<20)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			return refuse(badRequest("invalid multipart body"))
		}
		// Every spelling counts: form values are matched exactly, but be
		// strict about duplicates naming different projects.
		for _, v := range r.MultipartForm.Value["project_id"] {
			add(v)
		}
	case r.Body != nil && r.Body != http.NoBody && !isOTLPPath(r.URL.Path):
		var err error
		body, err = readBodyOnce(r)
		if err != nil {
			return refuse(err)
		}
		before := len(raw)
		bodyFrom = before
		if err := probeBody(body, add); err != nil {
			return refuse(err)
		}
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && len(raw) > before {
			return refuse(badRequest("GET requests must not carry a body naming a project; use the project_id query parameter"))
		}
	}
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, v := range raw {
		pid, err := refs.NormalizeProject(v)
		if err != nil {
			return refuse(badRequest("invalid project %q", clip(v, 200)))
		}
		if !seen[pid] {
			seen[pid] = true
			out = append(out, pid)
		}
	}
	return out, nil
}

// normalizedProjects keeps the valid project ids of raw, de-duplicated.
func normalizedProjects(raw []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range raw {
		if pid, err := refs.NormalizeProject(v); err == nil && !seen[pid] {
			seen[pid] = true
			out = append(out, pid)
		}
	}
	return out
}

// ---------------------------------------------------------------- request bodies

type bodyStateKey struct{}

// bodyState tracks, per authenticated request, whether prepareBody ran,
// the body it read, and the body-budget reservations held until the
// request ends (otlp.ReadBodyBudget).
type bodyState struct {
	prepared bool
	body     []byte // the decompressed JSON body, once read
	read     bool
	mu       sync.Mutex
	held     []*otlp.Reservation
}

// hold keeps res until the request ends (release).
func (st *bodyState) hold(res *otlp.Reservation) {
	st.mu.Lock()
	st.held = append(st.held, res)
	st.mu.Unlock()
}

func (st *bodyState) release() {
	st.mu.Lock()
	held := st.held
	st.held = nil
	st.mu.Unlock()
	for _, r := range held {
		r.Release()
	}
}

// decodeCostPerValue estimates the memory of one decoded JSON value, as
// the OTLP decoder does (otlp/limits.go).
const decodeCostPerValue = 160

// readJSONBody reads r's body (decompressed, at most bodyLimit(r) bytes,
// at a bounded compression ratio) through the server-wide body budget and
// reserves its estimated decoded size too (otlp.ReadBodyBudget: waits,
// then 503). The reservation is held until the request ends when it runs
// under auth; otherwise release must be called.
func readJSONBody(r *http.Request) (body []byte, release func(), err error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, func() {}, nil
	}
	limit := bodyLimit(r)
	body, res, err := otlp.ReadBodyBudget(r.Context(), http.MaxBytesReader(nil, r.Body, limit+1), r.Header.Get("Content-Encoding"), limit)
	if err != nil {
		code := otlp.BodyStatus(err)
		if code == http.StatusBadRequest {
			return nil, func() {}, badRequest("reading body: %v", err)
		}
		return nil, func() {}, &requestError{code: code, msg: fmt.Sprintf("request body refused: %v (at most %d MiB here)", err, limit>>20)}
	}
	if err := res.Grow(r.Context(), otlp.JSONValues(body)*decodeCostPerValue); err != nil {
		res.Release()
		return nil, func() {}, &requestError{code: otlp.BodyStatus(err), msg: err.Error()}
	}
	if st, _ := r.Context().Value(bodyStateKey{}).(*bodyState); st != nil {
		st.hold(res)
		return body, func() {}, nil
	}
	return body, res.Release, nil
}

// prepareBody runs once the caller is authenticated and before its
// projects are authorised: the compatibility adapter translates the body
// of its routes, and a mutating JSON body is kept for the audit log's ids.
// Nothing is read for a request that fails authentication.
func (s *Server) prepareBody(r *http.Request) error {
	st, _ := r.Context().Value(bodyStateKey{}).(*bodyState)
	if st == nil || st.prepared {
		return nil
	}
	st.prepared = true
	if s.compat != nil {
		if err := compat.RewriteRequestBody(r, bodyLimit(r)); err != nil {
			if code := otlp.BodyStatus(err); code != http.StatusBadRequest {
				return &requestError{code: code, msg: err.Error()}
			}
			return badRequest("reading body: %v", err)
		}
	}
	note := auditNoteFrom(r)
	if note == nil || isReadRequest(r) || isOTLPPath(r.URL.Path) || r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "json") {
		return nil
	}
	body, err := readBodyOnce(r)
	if err != nil {
		return err
	}
	if len(body) <= maxAuditBody {
		note.body = body
	}
	return nil
}

// readBodyOnce reads the request body (decompressed, at most bodyLimit
// bytes) and puts it back, once per request.
func readBodyOnce(r *http.Request) ([]byte, error) {
	st, _ := r.Context().Value(bodyStateKey{}).(*bodyState)
	if st != nil && st.read {
		return st.body, nil
	}
	body, release, err := readJSONBody(r)
	defer release()
	if err != nil {
		return nil, err
	}
	r.Header.Del("Content-Encoding")
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	if st != nil {
		st.body, st.read = body, true
	}
	return body, nil
}

// authorizeRole checks the caller has at least role need on project pid,
// whatever the route (open servers, static keys and admins have it).
func (s *Server) authorizeRole(r *http.Request, pid string, need store.Role) error {
	p := principalFrom(r)
	if p == nil {
		return nil
	}
	n, err := refs.NormalizeProject(pid)
	if err != nil {
		return &store.InvalidError{Msg: err.Error()}
	}
	if p.readOnly && need.Rank() > store.RoleViewer.Rank() {
		return store.Forbiddenf("this API key is read-only")
	}
	if p.isAdmin() {
		return nil
	}
	return (&projectGuard{p: p, need: need}).check(n)
}

func (s *Server) listProjectsScoped(w http.ResponseWriter, r *http.Request, p *principal) {
	ps, err := s.store.ListProjects(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := ps[:0]
	for _, x := range ps {
		if p.roleFor(x.ProjectID).Rank() > 0 {
			out = append(out, x)
		}
	}
	writeJSON(w, map[string]any{"projects": out})
}

func (s *Server) authPrincipal(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"auth_required": s.authRequired(), "admin": true, "read_only": false, "projects": nil, "key_id": nil,
		"key_name": nil, "user_id": "anonymous"}
	if u := userFrom(r, nil); u != nil {
		out["user_id"] = *u
	}
	out["rbac"], out["auth_method"] = false, "none"
	if note := auditNoteFrom(r); note != nil && note.authMethod != "" {
		out["auth_method"] = note.authMethod
	}
	if p := principalFrom(r); p != nil {
		out["admin"], out["read_only"], out["rbac"], out["auth_method"] = p.isAdmin(), p.readOnly, p.rbac, p.method
		if p.keyID != "" {
			out["key_id"] = p.keyID
		}
		if k, err := s.store.GetAPIKey(r.Context(), p.keyID); p.keyID != "" && err == nil {
			out["key_name"] = k.Name
		}
		if p.projects != nil {
			ps := make([]string, 0, len(p.projects))
			for pid := range p.projects {
				ps = append(ps, pid)
			}
			out["projects"] = ps
		}
	} else if len(s.keys) > 0 {
		out["key_name"] = "static key (--api-keys)"
	}
	writeJSON(w, out)
}

// ---------------------------------------------------------------- API keys

type apiKeyCreateReq struct {
	store.APIKeyCreate
}

// keyIsAdmin reports whether a key has server-admin powers.
func (s *Server) keyIsAdmin(ctx context.Context, k store.APIKey) bool {
	if !k.UserBound {
		return k.Admin
	}
	if !k.AllProjects || k.ReadOnly {
		return false
	}
	u, _, err := s.identity(ctx, k.UserID)
	return err == nil && u.Admin && !u.Disabled()
}

// apiKeyCreate: server admins create keys for anyone; other users (sessions
// and user-bound keys) create keys for themselves, no broader than the
// credential they use. A key whose user_id names a user is bound to it.
func (s *Server) apiKeyCreate(ctx context.Context, r *http.Request, req *apiKeyCreateReq) (any, error) {
	c := req.APIKeyCreate
	c.CreatedBy = userFrom(r, nil)
	p := principalFrom(r)
	if p != nil && !p.isAdmin() {
		if c.UserID != "" && c.UserID != p.user {
			return nil, forbidden("you can only create API keys for yourself")
		}
		c.UserID = p.user
		if c.Admin {
			return nil, forbidden("only server admins can create admin keys")
		}
		if p.readOnly {
			return nil, forbidden("a read-only credential cannot create API keys")
		}
		if p.projects != nil {
			ps, err := normalizeProjects(c.Projects)
			if err != nil {
				return nil, err
			}
			if len(ps) == 0 {
				return nil, forbidden("this API key is limited to projects; list the new key's projects")
			}
			for _, pid := range ps {
				if !p.projects[pid] {
					return nil, forbidden("this API key has no access to project %s", pid)
				}
			}
		}
	}
	if c.UserID != "" {
		if u, _, err := s.identity(ctx, c.UserID); err == nil {
			if u.Disabled() {
				return nil, store.Invalidf("user %s is disabled", u.ID)
			}
			c.UserBound = true
		}
	}
	if len(s.keys) == 0 && !c.Admin {
		// Without static keys, admin keys (or keys of admin users) are the
		// only way back in: the first key must be one.
		hasAdmin := false
		ks, _ := s.activeKeys()
		for _, k := range ks {
			hasAdmin = hasAdmin || s.keyIsAdmin(ctx, k)
		}
		if !hasAdmin && !s.keyIsAdmin(ctx, store.APIKey{UserBound: c.UserBound, UserID: c.UserID, AllProjects: len(c.Projects) == 0, ReadOnly: c.ReadOnly}) {
			return nil, store.Invalidf("the first API key must be an admin key: creating it turns authentication on, and only admin keys can manage keys")
		}
	}
	k, secret, err := s.store.CreateAPIKey(ctx, c)
	if err != nil {
		return nil, err
	}
	s.invalidateKeys()
	auditDetail(r, "created API key %s (%s) for %s", k.Name, k.ID, k.UserID)
	return map[string]any{"key": k, "secret": secret}, nil
}

func (s *Server) apiKeyList(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IncludeRevoked bool `json:"include_revoked"`
	}
	if r.Method == http.MethodPost {
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, err)
			return
		}
	} else {
		req.IncludeRevoked = r.URL.Query().Get("include_revoked") == "true"
	}
	ks, err := s.store.ListAPIKeys(r.Context(), req.IncludeRevoked)
	if err != nil {
		writeErr(w, err)
		return
	}
	if p := principalFrom(r); p != nil && !p.isAdmin() {
		own := ks[:0]
		for _, k := range ks {
			if k.UserBound && k.UserID == p.user {
				own = append(own, k)
			}
		}
		ks = own
	}
	writeJSON(w, map[string]any{"keys": ks, "static_key_count": len(s.keys)})
}

type apiKeyRevokeReq struct {
	ID string `json:"id"`
}

func (s *Server) apiKeyRevoke(ctx context.Context, r *http.Request, req *apiKeyRevokeReq) (any, error) {
	k, err := s.store.GetAPIKey(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if p := principalFrom(r); p != nil && !p.isAdmin() {
		if !(k.UserBound && k.UserID == p.user) {
			return nil, store.ErrNotFound
		}
		if err := noBroaderThan(p, k); err != nil {
			return nil, err
		}
	}
	if len(s.keys) == 0 && s.keyIsAdmin(ctx, *k) && k.RevokedAt == nil {
		admins, others := 0, 0
		ks, _ := s.activeKeys()
		for _, x := range ks {
			if s.keyIsAdmin(ctx, x) {
				admins++
			} else {
				others++
			}
		}
		if admins == 1 && others > 0 && s.cfg.IAM.OIDC == nil {
			return nil, store.Invalidf("cannot revoke the last admin key while other keys are active: revoke them first or create another admin key")
		}
	}
	if err := s.store.RevokeAPIKey(ctx, req.ID); err != nil {
		return nil, err
	}
	s.invalidateKeys()
	auditDetail(r, "revoked API key %s (%s) of %s", k.Name, k.ID, k.UserID)
	k, err = s.store.GetAPIKey(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"key": k}, nil
}

// noBroaderThan refuses to let credential p manage key k when k reaches
// more than p does: a read-only credential cannot revoke a read-write key,
// a project-scoped one a key of other or all projects.
func noBroaderThan(p *principal, k *store.APIKey) error {
	if p.readOnly && !k.ReadOnly {
		return forbidden("a read-only credential cannot revoke a read-write key")
	}
	if p.projects != nil {
		if k.AllProjects {
			return forbidden("this API key is limited to projects; it cannot revoke a key for all projects")
		}
		for _, pid := range k.Projects {
			if !p.projects[pid] {
				return forbidden("this API key has no access to project %s", pid)
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------- project settings

type ttlUpdateReq struct {
	ProjectID     string  `json:"project_id"`
	RetentionDays *int    `json:"retention_days"`
	UserID        *string `json:"user_id"`
}

func (s *Server) ttlRead(ctx context.Context, _ *http.Request, req *projectReq) (any, error) {
	var t store.TTLSettings
	if _, err := s.store.GetProjectSetting(ctx, req.ProjectID, store.SettingTTL, &t); err != nil {
		return nil, err
	}
	return map[string]any{"retention_days": t.RetentionDays}, nil
}

func (s *Server) ttlUpdate(ctx context.Context, r *http.Request, req *ttlUpdateReq) (any, error) {
	var val any
	if req.RetentionDays != nil {
		if *req.RetentionDays < 1 || *req.RetentionDays > 36500 {
			return nil, store.Invalidf("retention_days must be null or between 1 and 36500")
		}
		val = store.TTLSettings{RetentionDays: req.RetentionDays}
	}
	if err := s.store.SetProjectSetting(ctx, req.ProjectID, store.SettingTTL, val, userFrom(r, req.UserID)); err != nil {
		return nil, err
	}
	return map[string]any{"retention_days": req.RetentionDays}, nil
}

// ttlPurge applies the retention policy to one project now.
func (s *Server) ttlPurge(ctx context.Context, _ *http.Request, req *projectReq) (any, error) {
	var t store.TTLSettings
	if _, err := s.store.GetProjectSetting(ctx, req.ProjectID, store.SettingTTL, &t); err != nil {
		return nil, err
	}
	if t.RetentionDays == nil {
		return map[string]any{"purged": store.PurgeResult{}, "retention_days": nil}, nil
	}
	res, err := s.store.PurgeBefore(ctx, req.ProjectID, time.Now().AddDate(0, 0, -*t.RetentionDays))
	if err != nil {
		return nil, err
	}
	return map[string]any{"purged": res, "retention_days": t.RetentionDays}, nil
}

func (s *Server) samplingRead(ctx context.Context, _ *http.Request, req *projectReq) (any, error) {
	ss := store.SamplingSettings{SampleRate: 1}
	if _, err := s.store.GetProjectSetting(ctx, req.ProjectID, store.SettingIngestSample, &ss); err != nil {
		return nil, err
	}
	return ss, nil
}

type samplingUpdateReq struct {
	ProjectID  string   `json:"project_id"`
	SampleRate *float64 `json:"sample_rate"`
	DryRun     bool     `json:"dry_run"`
	UserID     *string  `json:"user_id"`
}

func (s *Server) samplingUpdate(ctx context.Context, r *http.Request, req *samplingUpdateReq) (any, error) {
	ss := store.SamplingSettings{SampleRate: 1, DryRun: req.DryRun}
	if req.SampleRate != nil {
		if *req.SampleRate < 0 || *req.SampleRate > 1 {
			return nil, store.Invalidf("sample_rate must be between 0 and 1")
		}
		ss.SampleRate = *req.SampleRate
	}
	var val any = ss
	if ss.SampleRate >= 1 && !ss.DryRun {
		val = nil // the default: keep everything
	}
	if err := s.store.SetProjectSetting(ctx, req.ProjectID, store.SettingIngestSample, val, userFrom(r, req.UserID)); err != nil {
		return nil, err
	}
	return ss, nil
}

func (s *Server) sensitiveSettings(ctx context.Context, projectID string) (map[string]any, error) {
	sd := store.SensitiveDataSettings{Policy: "off"}
	if _, err := s.store.GetProjectSetting(ctx, projectID, store.SettingSensitiveData, &sd); err != nil {
		return nil, err
	}
	if sd.Entities == nil {
		sd.Entities = []string{}
	}
	if sd.CustomPatterns == nil {
		sd.CustomPatterns = []store.RedactionPattern{}
	}
	return sensitiveView(sd, s.store.HasPIIAnalyzer()), nil
}

// sensitiveView is the read shape: the policy fields plus the NER settings.
// available_entities lists the NER entities only when the server has a PII
// analyzer (--pii-analyzer-url); ner_active says whether they apply now.
func sensitiveView(sd store.SensitiveDataSettings, analyzer bool) map[string]any {
	avail := append([]*store.RedactionEntity{}, store.RedactionEntities...)
	if analyzer {
		avail = append(avail, store.NEREntities...)
	}
	ner := map[string]any{"enabled": true, "score_threshold": 0.5, "language": "en"}
	if sd.NER != nil {
		if sd.NER.Enabled != nil {
			ner["enabled"] = *sd.NER.Enabled
		}
		if sd.NER.ScoreThreshold != nil {
			ner["score_threshold"] = *sd.NER.ScoreThreshold
		}
		if sd.NER.Language != "" {
			ner["language"] = sd.NER.Language
		}
	}
	active := false
	nerEntities := []string{}
	if rd, err := store.NewRedactor(sd); err == nil {
		nerEntities = append(nerEntities, rd.NEREntities()...)
		active = analyzer && sd.Policy != "off" && sd.Policy != "" && len(nerEntities) > 0
	}
	return map[string]any{"policy": sd.Policy, "entities": sd.Entities, "custom_patterns": sd.CustomPatterns,
		"available_entities": avail, "ner": ner, "ner_available": analyzer, "ner_active": active, "ner_entities": nerEntities}
}

func (s *Server) sensitiveRead(w http.ResponseWriter, r *http.Request) {
	out, err := s.sensitiveSettings(r.Context(), r.URL.Query().Get("project_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) sensitiveReadPost(ctx context.Context, _ *http.Request, req *projectReq) (any, error) {
	return s.sensitiveSettings(ctx, req.ProjectID)
}

type sensitiveUpdateReq struct {
	ProjectID string `json:"project_id"`
	store.SensitiveDataSettings
	UserID *string `json:"user_id"`
}

func (s *Server) sensitiveUpdate(ctx context.Context, r *http.Request, req *sensitiveUpdateReq) (any, error) {
	sd := req.SensitiveDataSettings
	switch sd.Policy {
	case "", "off":
		sd.Policy = "off"
	case "pii-v1":
	default:
		return nil, store.Invalidf(`policy must be "off" or "pii-v1"`)
	}
	if _, err := store.NewRedactor(sd); err != nil {
		return nil, err
	}
	var val any = sd
	if sd.Policy == "off" && len(sd.Entities) == 0 && len(sd.CustomPatterns) == 0 && sd.NER == nil {
		val = nil
	}
	if err := s.store.SetProjectSetting(ctx, req.ProjectID, store.SettingSensitiveData, val, userFrom(r, req.UserID)); err != nil {
		return nil, err
	}
	return s.sensitiveSettings(ctx, req.ProjectID)
}

type sensitivePreviewReq struct {
	store.SensitiveDataSettings
	Text  string `json:"text"`
	Value any    `json:"value"`
}

// sensitivePreview redacts sample input with the given (unsaved) settings,
// NER entities included when the server has a PII analyzer.
func (s *Server) sensitivePreview(ctx context.Context, _ *http.Request, req *sensitivePreviewReq) (any, error) {
	if len(req.Text) > 1<<20 {
		return nil, store.Invalidf("text too long")
	}
	text, value, ner, err := s.store.RedactPreview(ctx, req.SensitiveDataSettings, req.Text, req.Value)
	if err != nil {
		return nil, err
	}
	return map[string]any{"text": text, "value": value, "ner_applied": ner}, nil
}

type projectDeleteReq struct {
	ProjectID string `json:"project_id"`
	// Confirm must repeat the project id, to guard against accidents.
	Confirm string `json:"confirm"`
}

func (s *Server) projectDelete(ctx context.Context, _ *http.Request, req *projectDeleteReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	if req.Confirm != "" {
		if c, err := refs.NormalizeProject(req.Confirm); err != nil || c != pid {
			return nil, store.Invalidf("confirm must match the project id")
		}
	}
	counts, err := s.store.DeleteProject(ctx, pid)
	if err != nil {
		return nil, err
	}
	s.engine.InvalidateMonitors(pid)
	return map[string]any{"project_id": pid, "deleted": counts}, nil
}

// ---------------------------------------------------------------- retention

// RunRetention purges data older than each project's retention TTL.
func (s *Server) RunRetention(ctx context.Context) (map[string]store.PurgeResult, error) {
	ttls, err := s.store.ProjectsWithTTL(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]store.PurgeResult{}
	for pid, days := range ttls {
		res, err := s.store.PurgeBefore(ctx, pid, time.Now().AddDate(0, 0, -days))
		if err != nil {
			return out, err
		}
		if res.Spans > 0 || res.Feedback > 0 {
			s.log.Info("retention purge", "project", pid, "retention_days", days, "spans", res.Spans, "feedback", res.Feedback)
		}
		out[pid] = res
	}
	if err := s.store.PurgeDroppedMarkers(ctx, time.Now().Add(-24*time.Hour)); err != nil {
		return out, err
	}
	return out, nil
}

// retentionInterval is how often the purge runs (WEFT_RETENTION_INTERVAL, a Go duration).
func retentionInterval() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("WEFT_RETENTION_INTERVAL")); err == nil && d >= time.Second {
		return d
	}
	return time.Hour
}

func startRetention(s *Server) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		first := time.NewTimer(30 * time.Second)
		defer first.Stop()
		tick := time.NewTicker(retentionInterval())
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-first.C:
			case <-tick.C:
			}
			start := time.Now()
			s.log.Debug("retention purge started")
			res, err := s.RunRetention(ctx)
			switch {
			case err != nil && ctx.Err() == nil:
				s.log.Error("retention purge failed", "err", weftlog.Err(err), "duration_ms", time.Since(start).Milliseconds())
			case err == nil:
				var spans, feedback int64
				for _, r := range res {
					spans += r.Spans
					feedback += r.Feedback
				}
				s.log.Debug("retention purge finished", "projects", len(res), "spans", spans, "feedback", feedback,
					"duration_ms", time.Since(start).Milliseconds())
			}
		}
	}()
	return func() {
		cancel()
		<-done
		keyCaches.Delete(s)
	}
}
