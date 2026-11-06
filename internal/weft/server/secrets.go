package server

import (
	"context"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// Secrets: scoped, encrypted values (store/secrets.go) that objects name
// (custom provider keys, remote scorer and predict credentials, OAuth client
// secrets, webhook URLs) and the built-in provider routes read.
//
//	POST /api/v1/secrets/set     {"name", "value", "scope", "project_id"|"team", "allowed_hosts", "kind"}
//	POST /api/v1/secrets/delete  {"name", "scope", "project_id"|"team"}
//	GET  /api/v1/secrets/list    ?project_id=  (the secrets that project can see)
//
// The older /secrets/set and /secrets/delete (server admins only) take the
// same bodies (server scope by default); /secrets/list is the same as
// /api/v1/secrets/list.
//
// Who may manage what: server secrets and bucket credentials need a server
// admin; team secrets an admin of the team (or a server admin); project
// secrets a member of the project. Listing needs a server admin or the
// member role (on the named project, or on some project); a member sees only
// the secrets of the projects and teams they are a member of, and the server
// secrets projects may use (shared ones and the built-in provider keys).
// Values are never returned.

func init() {
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		r.Post("/api/v1/secrets/set", handle(s.secretsSet))
		r.Post("/api/v1/secrets/delete", handle(s.secretsDelete))
		r.Get("/api/v1/secrets/list", s.secretsList)
	})
}

type secretReq struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	// Scope is server, team or project; inferred from project_id / team
	// when omitted (server when neither is given).
	Scope     string `json:"scope"`
	ProjectID string `json:"project_id"`
	Team      string `json:"team"`
	// AllowedHosts restricts where the secret may be sent ("host",
	// "host:port", "*.domain"); omitted keeps the current list on replace.
	AllowedHosts *[]string `json:"allowed_hosts"`
	// Kind "bucket" stores reference-bucket credentials (server admins).
	Kind string `json:"kind"`
}

// secretTarget authorizes a write to the secret scope a request names and
// returns that scope in canonical form.
func (s *Server) secretTarget(r *http.Request, req *secretReq) (scope, scopeID string, err error) {
	scope = strings.TrimSpace(req.Scope)
	if scope == "" {
		switch {
		case req.ProjectID != "":
			scope = store.SecretScopeProject
		case req.Team != "":
			scope = store.SecretScopeTeam
		default:
			scope = store.SecretScopeServer
		}
	}
	switch scope {
	case store.SecretScopeServer:
		scopeID = ""
	case store.SecretScopeTeam:
		scopeID = req.Team
		if scopeID == "" && req.ProjectID != "" {
			// A team admin names one of the team's projects (which the
			// role check of the request can see).
			scopeID, _, _ = strings.Cut(req.ProjectID, "/")
		}
	case store.SecretScopeProject:
		scopeID = req.ProjectID
	}
	scope, scopeID, err = store.NormalizeSecretScope(scope, scopeID)
	if err != nil {
		return "", "", err
	}
	if req.Kind == store.SecretKindBucket && scope != store.SecretScopeServer {
		return "", "", store.Invalidf("bucket credentials are server secrets")
	}
	switch {
	case scope == store.SecretScopeServer || req.Kind == store.SecretKindBucket:
		if !callerIsAdmin(r) {
			return "", "", store.Forbiddenf("server secrets need a server admin; store a project secret (scope project) instead")
		}
	case scope == store.SecretScopeTeam:
		if callerTeamRole(r, scopeID) != store.RoleAdmin {
			return "", "", store.Forbiddenf("team secrets need an admin of team %s", scopeID)
		}
	default:
		if callerRole(r, scopeID).Rank() < store.RoleMember.Rank() || callerReadOnly(r) {
			return "", "", store.Forbiddenf("project secrets need the member role on %s", scopeID)
		}
	}
	return scope, scopeID, nil
}

func (s *Server) secretsSet(ctx context.Context, r *http.Request, req *secretReq) (any, error) {
	scope, scopeID, err := s.secretTarget(r, req)
	if err != nil {
		return nil, err
	}
	w := store.SecretWrite{Name: req.Name, Value: req.Value, Scope: scope, ScopeID: scopeID, Kind: req.Kind, CreatedBy: userFrom(r, nil)}
	if req.AllowedHosts != nil {
		w.AllowedHosts = *req.AllowedHosts
		if w.AllowedHosts == nil {
			w.AllowedHosts = []string{}
		}
	}
	if err := s.store.PutSecret(ctx, w); err != nil {
		return nil, err
	}
	return map[string]any{"name": req.Name, "scope": scope, "scope_id": scopeID}, nil
}

func (s *Server) secretsDelete(ctx context.Context, r *http.Request, req *secretReq) (any, error) {
	scope, scopeID, err := s.secretTarget(r, req)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.DeleteScopedSecret(ctx, scope, scopeID, req.Name); err != nil {
		return nil, err
	}
	return empty, nil
}

// envSecretNames are environment variables reported by /secrets/list (the
// built-in provider routes read them).
var envSecretNames = []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GROQ_API_KEY",
	"MISTRAL_API_KEY", "DEEPSEEK_API_KEY", "XAI_API_KEY", "OPENROUTER_API_KEY", "TOGETHER_API_KEY", "OLLAMA_BASE_URL"}

type secretEntry struct {
	store.SecretInfo
	// Source is "db" (stored) or "env" (the server's environment).
	Source string `json:"source"`
}

func (s *Server) secretsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pid := strings.TrimSpace(r.URL.Query().Get("project_id"))
	f := store.SecretListFilter{}
	admin := callerIsAdmin(r)
	switch {
	case pid != "":
		npid, err := refs.NormalizeProject(pid)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		pid = npid
		if !admin && (callerRole(r, pid).Rank() < store.RoleMember.Rank()) {
			writeError(w, http.StatusForbidden, "listing secrets needs the member role on "+pid)
			return
		}
		team, _, _ := strings.Cut(pid, "/")
		f = store.SecretListFilter{Projects: []string{pid}, Teams: []string{team}, SharedServer: true, ServerNames: envSecretNames}
		if admin || s.serverSecretsOpen(ctx) {
			f.SharedServer, f.ServerNames = true, nil
			f.All = false
		}
	case admin:
		f.All = true
	default:
		mf, ok := callerMemberSecretScopes(r)
		if !ok {
			writeError(w, http.StatusForbidden, "listing secrets needs a server admin or the member role on a project")
			return
		}
		f = mf
	}
	infos, err := s.store.ListSecretInfo(ctx, f)
	if err != nil {
		writeErr(w, err)
		return
	}
	if pid != "" && (admin || s.serverSecretsOpen(ctx)) {
		// Every server secret is usable from this project (or manageable).
		all, err := s.store.ListSecretInfo(ctx, store.SecretListFilter{All: true})
		if err != nil {
			writeErr(w, err)
			return
		}
		for _, x := range all {
			if x.Scope == store.SecretScopeServer && (admin || x.Kind == store.SecretKindSecret) && !hasSecret(infos, x) {
				infos = append(infos, x)
			}
		}
	}
	out := []secretEntry{}
	stored := map[string]bool{}
	for _, x := range infos {
		if x.Scope == store.SecretScopeServer {
			stored[x.Name] = true
		}
		out = append(out, secretEntry{SecretInfo: x, Source: "db"})
	}
	for _, n := range envSecretNames {
		if !stored[n] && os.Getenv(n) != "" {
			out = append(out, secretEntry{SecretInfo: store.SecretInfo{Name: n, Scope: store.SecretScopeServer, Kind: store.SecretKindSecret,
				AllowedHosts: []string{}}, Source: "env"})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return store.SecretScopeRank(out[i].Scope) > store.SecretScopeRank(out[j].Scope)
	})
	writeJSON(w, map[string]any{"secrets": out})
}

// callerMemberSecretScopes is the list filter for a non-admin caller who
// names no project: the secrets of every project and team where the caller
// has the member role, plus shared and built-in server secrets. ok is false
// when the caller is not a member anywhere.
func callerMemberSecretScopes(r *http.Request) (store.SecretListFilter, bool) {
	f := store.SecretListFilter{SharedServer: true, ServerNames: envSecretNames}
	p := principalFrom(r)
	if p == nil || p.readOnly {
		return f, false
	}
	member := func(role store.Role) bool { return role.Rank() >= store.RoleMember.Rank() }
	switch {
	case p.projects != nil:
		for pid := range p.projects {
			if member(p.roleFor(pid)) {
				f.Projects = append(f.Projects, pid)
				team, _, _ := strings.Cut(pid, "/")
				f.Teams = append(f.Teams, team)
			}
		}
	case !p.rbac:
		f.AllScoped = true // older all-project keys: member everywhere
	default:
		for team, role := range p.roles.Entities {
			if member(role) {
				f.Teams = append(f.Teams, team)
				f.ProjectsOfTeams = append(f.ProjectsOfTeams, team)
			}
		}
		for pid, role := range p.roles.Projects {
			if member(role) {
				f.Projects = append(f.Projects, pid)
				team, _, _ := strings.Cut(pid, "/")
				f.Teams = append(f.Teams, team)
			}
		}
	}
	return f, f.AllScoped || len(f.Projects) > 0 || len(f.Teams) > 0
}

func hasSecret(list []store.SecretInfo, x store.SecretInfo) bool {
	for _, y := range list {
		if y.Scope == x.Scope && y.ScopeID == x.ScopeID && y.Name == x.Name {
			return true
		}
	}
	return false
}

// callerTeamRole is the caller's role in a team (admin for server admins,
// static keys and open servers; none for project-limited keys).
func callerTeamRole(r *http.Request, team string) store.Role {
	p := principalFrom(r)
	switch {
	case p == nil || p.isAdmin():
		return store.RoleAdmin
	case p.projects != nil:
		return store.RoleNone
	case !p.rbac:
		return store.RoleMember // older all-project keys: member everywhere
	}
	role := p.roles.Entities[team]
	if p.readOnly {
		role = store.MinRole(role, store.RoleViewer)
	}
	return role
}

func callerReadOnly(r *http.Request) bool {
	p := principalFrom(r)
	return p != nil && p.readOnly
}

// serverSecretsOpen reports whether every principal of this server is a
// server admin: no SSO, no users other than enabled admins, and every active
// managed key a full admin key. Then every object is a server admin's, so
// projects' objects may use server secrets (the behaviour before scopes).
// Once a non-admin principal can exist, server secrets are only used by the
// built-in provider routes and toward their allowed hosts.
func (s *Server) serverSecretsOpen(ctx context.Context) bool {
	if s.cfg.IAM.OIDC != nil {
		return false
	}
	c := s.secretsOpenCache()
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.at) < 2*time.Second {
		return c.open
	}
	c.open, c.at = s.computeSecretsOpen(ctx), time.Now()
	return c.open
}

func (s *Server) computeSecretsOpen(ctx context.Context) bool {
	keys, _ := s.activeKeys()
	users, err := s.store.ListUsers(ctx, false)
	if err != nil {
		return false
	}
	admins := map[string]bool{}
	for _, u := range users {
		if !u.Admin {
			return false
		}
		admins[u.ID] = true
	}
	for _, k := range keys {
		full := k.AllProjects && !k.ReadOnly
		if k.UserBound {
			full = full && admins[k.UserID]
		} else {
			full = full && k.Admin
		}
		if !full {
			return false
		}
	}
	return true
}

type secretsOpenState struct {
	mu   sync.Mutex
	at   time.Time
	open bool
}

var secretsOpenStates sync.Map // *Server -> *secretsOpenState

func (s *Server) secretsOpenCache() *secretsOpenState {
	c, _ := secretsOpenStates.LoadOrStore(s, &secretsOpenState{})
	return c.(*secretsOpenState)
}

// validHeader reports whether k: v is a valid HTTP header field.
func validHeader(k, v string) bool {
	if k == "" || len(k) > 256 || len(v) > 8192 {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0) {
			return false
		}
	}
	for i := 0; i < len(v); i++ {
		if c := v[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}
