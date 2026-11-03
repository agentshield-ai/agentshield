package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/otlp"
	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
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
var readSegments = map[string]bool{
	"query": true, "read": true, "stream_query": true, "query_stats": true, "stats": true, "read_batch": true,
	"content": true, "compare": true, "compute": true, "status": true, "usage": true, "aggregate": true,
	"payload_schema": true, "query_stats_batch": true, "op_counts": true, "source_datasets_query": true,
	"search": true, "list": true, "projects_info": true, "export": true, "metrics": true, "graphql": true,
	"evaluation_status": true, "preview": true, "check": true, "whoami": true, "principal": true,
}

func isReadRequest(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	case http.MethodPost:
		path := strings.TrimSuffix(r.URL.Path, "/")
		return readSegments[path[strings.LastIndex(path, "/")+1:]]
	}
	return false
}

// projectFreePaths may be called by project-scoped keys without naming a project.
var projectFreePaths = map[string]bool{"/auth/check": true, "/auth/whoami": true, "/auth/principal": true, "/projects": true,
	"/llm/models": true, "/graphql": true, "/secrets/list": true, "/server_info": true, "/health": true}

// requestProjects finds every project a request names: the project_id query
// parameter and headers, the /v2/{entity}/{project}/ path, and project_id
// fields and refs at the top level of a JSON body (and inside the
// start/end/obj/table wrappers and batch items). The body is restored.
func requestProjects(w http.ResponseWriter, r *http.Request) ([]string, error) {
	var raw []string
	add := func(v string) {
		if v != "" {
			raw = append(raw, v)
		}
	}
	add(r.URL.Query().Get("project_id"))
	if r.URL.Path == "/otel/v1/traces" {
		hp := r.Header.Get("project_id")
		if hp == "" {
			hp = r.Header.Get("X-Weft-Project")
		}
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
			return nil, errors.New("invalid multipart body")
		}
		add(r.FormValue("project_id"))
	case r.Body != nil && r.Method != http.MethodGet && r.URL.Path != "/otel/v1/traces":
		body, err := otlp.ReadBody(http.MaxBytesReader(nil, r.Body, maxJSONBody), r.Header.Get("Content-Encoding"))
		if err != nil {
			return nil, errors.New("reading body: " + err.Error())
		}
		r.Header.Del("Content-Encoding")
		r.Body = io.NopCloser(bytes.NewReader(body))
		probeBody(body, add)
	}
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, v := range raw {
		pid, err := refs.NormalizeProject(v)
		if err != nil {
			return nil, errors.New("invalid project " + v)
		}
		if !seen[pid] {
			seen[pid] = true
			out = append(out, pid)
		}
	}
	return out, nil
}

type projectHolder struct {
	ProjectID *string `json:"project_id"`
}

type scopeProbe struct {
	ProjectID  *string        `json:"project_id"`
	ProjectIDs []string       `json:"project_ids"`
	Start      *projectHolder `json:"start"`
	End        *projectHolder `json:"end"`
	Obj        *projectHolder `json:"obj"`
	Table      *projectHolder `json:"table"`
	Batch      []struct {
		ProjectID *string `json:"project_id"`
		Req       struct {
			Start *projectHolder `json:"start"`
			End   *projectHolder `json:"end"`
		} `json:"req"`
	} `json:"batch"`
	Refs      []string       `json:"refs"`
	Variables map[string]any `json:"variables"`
}

func probeBody(body []byte, add func(string)) {
	var p scopeProbe
	if json.Unmarshal(body, &p) != nil {
		return
	}
	holder := func(h *projectHolder) {
		if h != nil && h.ProjectID != nil {
			add(*h.ProjectID)
		}
	}
	if p.ProjectID != nil {
		add(*p.ProjectID)
	}
	for _, x := range p.ProjectIDs {
		add(x)
	}
	holder(p.Start)
	holder(p.End)
	holder(p.Obj)
	holder(p.Table)
	for _, b := range p.Batch {
		if b.ProjectID != nil {
			add(*b.ProjectID)
		}
		holder(b.Req.Start)
		holder(b.Req.End)
	}
	addRef := func(v string) {
		if refs.IsRef(v) {
			if rr, err := refs.Parse(v); err == nil {
				add(rr.ProjectID())
			}
		}
	}
	for _, x := range p.Refs {
		addRef(x)
	}
	if ent, ok := p.Variables["entity"].(string); ok {
		if name, ok := p.Variables["name"].(string); ok && ent != "" && name != "" {
			add(ent + "/" + name)
		}
	}
	// Top-level ref-valued fields (evaluation_ref, model, scorer_refs, ...).
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return
	}
	for k, v := range top {
		if k == "refs" || len(v) == 0 {
			continue
		}
		switch v[0] {
		case '"':
			var str string
			if json.Unmarshal(v, &str) == nil {
				addRef(str)
			}
		case '[':
			var arr []any
			if json.Unmarshal(v, &arr) == nil {
				for _, x := range arr {
					if str, ok := x.(string); ok {
						addRef(str)
					}
				}
			}
		}
	}
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
		if p.readOnly && !c.ReadOnly {
			return nil, forbidden("a read-only credential can only create read-only keys")
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
	if p := principalFrom(r); p != nil && !p.isAdmin() && !(k.UserBound && k.UserID == p.user) {
		return nil, store.ErrNotFound
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

// ---------------------------------------------------------------- project settings

type ttlUpdateReq struct {
	ProjectID     string  `json:"project_id"`
	RetentionDays *int    `json:"retention_days"`
	WBUserID      *string `json:"wb_user_id"`
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
	if err := s.store.SetProjectSetting(ctx, req.ProjectID, store.SettingTTL, val, userFrom(r, req.WBUserID)); err != nil {
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
	WBUserID   *string  `json:"wb_user_id"`
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
	if err := s.store.SetProjectSetting(ctx, req.ProjectID, store.SettingIngestSample, val, userFrom(r, req.WBUserID)); err != nil {
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
	WBUserID *string `json:"wb_user_id"`
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
	if err := s.store.SetProjectSetting(ctx, req.ProjectID, store.SettingSensitiveData, val, userFrom(r, req.WBUserID)); err != nil {
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
		if res.Calls > 0 || res.Feedback > 0 {
			s.log.Info("retention purge", "project", pid, "retention_days", days, "calls", res.Calls, "feedback", res.Feedback)
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
			if _, err := s.RunRetention(ctx); err != nil && ctx.Err() == nil {
				s.log.Error("retention purge failed", "err", err)
			}
		}
	}()
	return func() {
		cancel()
		<-done
		keyCaches.Delete(s)
	}
}
