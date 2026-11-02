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
		r.Post("/api_keys/create", handle(s.apiKeyCreate))
		r.Get("/api_keys/list", s.apiKeyList)
		r.Post("/api_keys/list", s.apiKeyList)
		r.Post("/api_keys/revoke", handle(s.apiKeyRevoke))

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

// principal is the caller authenticated by a managed key. Requests with a
// static --api-keys key, or to a server without auth, carry no principal and
// are treated as admin.
type principal struct {
	keyID    string
	user     string
	admin    bool
	readOnly bool
	projects map[string]bool // nil: all projects
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

// authRequired reports whether requests need a key: static keys are
// configured or at least one managed key is active.
func (s *Server) authRequired() bool {
	if len(s.keys) > 0 {
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
	p := &principal{keyID: match.ID, user: match.UserID, admin: match.Admin, readOnly: match.ReadOnly}
	if !match.AllProjects {
		p.projects = map[string]bool{}
		for _, pid := range match.Projects {
			p.projects[pid] = true
		}
	}
	s.touchKey(match.ID)
	ctx := context.WithValue(r.Context(), principalKey{}, p)
	ctx = context.WithValue(ctx, ctxKey{}, p.user)
	r = r.WithContext(ctx)
	if msg := s.checkScope(w, r, p); msg != "" {
		writeError(w, http.StatusForbidden, msg)
		return nil
	}
	if r.Method == http.MethodGet && r.URL.Path == "/projects" && p.projects != nil {
		s.listProjectsScoped(w, r, p)
		return nil
	}
	return r
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

// adminPaths need an admin principal.
var adminPaths = []string{"/api_keys/", "/secrets/set", "/secrets/delete", "/project/delete",
	"/project/ttl_settings/update", "/project/ttl_settings/purge", "/project/ingest_sampling_settings/update",
	"/project/sensitive_data_settings/update"}

func isAdminPath(path string) bool {
	for _, p := range adminPaths {
		if path == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(path, p)) {
			return true
		}
	}
	return false
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

func (s *Server) checkScope(w http.ResponseWriter, r *http.Request, p *principal) string {
	if !p.admin && isAdminPath(r.URL.Path) {
		return "this API key is not an admin key"
	}
	if p.readOnly && !isReadRequest(r) {
		return "this API key is read-only"
	}
	if p.projects == nil {
		return ""
	}
	pids, err := requestProjects(w, r)
	if err != nil {
		return err.Error()
	}
	if len(pids) == 0 {
		if projectFreePaths[r.URL.Path] {
			return ""
		}
		return "this API key is limited to specific projects; the request must name a project"
	}
	for _, pid := range pids {
		if !p.projects[pid] {
			return "this API key has no access to project " + pid
		}
	}
	return ""
}

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
		if p.projects[x.ProjectID] {
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
	if p := principalFrom(r); p != nil {
		out["admin"], out["read_only"], out["key_id"] = p.admin, p.readOnly, p.keyID
		if k, err := s.store.GetAPIKey(r.Context(), p.keyID); err == nil {
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

func (s *Server) apiKeyCreate(ctx context.Context, r *http.Request, req *apiKeyCreateReq) (any, error) {
	c := req.APIKeyCreate
	c.CreatedBy = userFrom(r, nil)
	if len(s.keys) == 0 && !c.Admin {
		// Without static keys, managed admin keys are the only way back in:
		// the first key must be one.
		hasAdmin := false
		ks, _ := s.activeKeys()
		for _, k := range ks {
			hasAdmin = hasAdmin || k.Admin
		}
		if !hasAdmin {
			return nil, store.Invalidf("the first API key must be an admin key: creating it turns authentication on, and only admin keys can manage keys")
		}
	}
	k, secret, err := s.store.CreateAPIKey(ctx, c)
	if err != nil {
		return nil, err
	}
	s.invalidateKeys()
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
	writeJSON(w, map[string]any{"keys": ks, "static_key_count": len(s.keys)})
}

type apiKeyRevokeReq struct {
	ID string `json:"id"`
}

func (s *Server) apiKeyRevoke(ctx context.Context, _ *http.Request, req *apiKeyRevokeReq) (any, error) {
	k, err := s.store.GetAPIKey(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if len(s.keys) == 0 && k.Admin && k.RevokedAt == nil {
		admins, others := 0, 0
		ks, _ := s.activeKeys()
		for _, x := range ks {
			if x.Admin {
				admins++
			} else {
				others++
			}
		}
		if admins == 1 && others > 0 {
			return nil, store.Invalidf("cannot revoke the last admin key while other keys are active: revoke them first or create another admin key")
		}
	}
	if err := s.store.RevokeAPIKey(ctx, req.ID); err != nil {
		return nil, err
	}
	s.invalidateKeys()
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
	return map[string]any{"policy": sd.Policy, "entities": sd.Entities, "custom_patterns": sd.CustomPatterns,
		"available_entities": store.RedactionEntities}, nil
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
	if sd.Policy == "off" && len(sd.Entities) == 0 && len(sd.CustomPatterns) == 0 {
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

// sensitivePreview redacts sample input with the given (unsaved) settings.
func (s *Server) sensitivePreview(_ context.Context, _ *http.Request, req *sensitivePreviewReq) (any, error) {
	if len(req.Text) > 1<<20 {
		return nil, store.Invalidf("text too long")
	}
	rd, err := store.NewRedactor(req.SensitiveDataSettings)
	if err != nil {
		return nil, err
	}
	return map[string]any{"text": rd.String(req.Text), "value": rd.Value(req.Value)}, nil
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
