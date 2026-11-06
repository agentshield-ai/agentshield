package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// Identity and access management: users, teams (entities) and roles,
// role enforcement for every authenticated request, and the endpoints that
// manage them. Sessions and OIDC sign-in are in oidc.go; the audit log is
// in audit.go. See docs/weft/API.md "Users, teams and roles".
//
// Access model:
//   - Static --api-keys and managed admin keys are server admins.
//   - A managed key whose user_id names a user in the users table is
//     "user-bound": it takes that user's roles, narrowed by the key's project
//     list and read-only flag. Older keys keep their flag-based access
//     (all projects read-write, read-only, or admin).
//   - A user's role on project "e/p" is the stronger of their role in team
//     "e" and their direct role on the project. Server admins (users.admin)
//     are admin everywhere.
//   - Reads need viewer, writes need member, project settings, members and
//     delete need admin on the project; user/team management and provider
//     secrets need a server admin.

// IAMConfig configures users, sessions, SSO and the audit log.
type IAMConfig struct {
	// OIDC enables single sign-on for the web UI.
	OIDC *OIDCConfig
	// SessionKey signs session cookies (32+ bytes). Random per process when
	// empty, so sessions then do not survive a restart.
	SessionKey []byte
	// SessionTTL is the lifetime of a browser session (default 12h).
	SessionTTL time.Duration
	// SecureCookies forces the Secure attribute on cookies (it is also set
	// for TLS requests and https redirect URLs).
	SecureCookies bool
	// AuditRetentionDays keeps audit entries this long (0: 90 days; <0: forever).
	AuditRetentionDays int
}

func init() {
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		r.Get("/auth/me", s.authMe)
		r.Post("/users/create", handleIAM(s.userCreate))
		r.Get("/users/list", s.userList)
		r.Post("/users/list", s.userList)
		r.Post("/users/update", handleIAM(s.userUpdate))
		r.Post("/users/revoke_sessions", handleIAM(s.userRevokeSessions))
		r.Post("/teams/create", handleIAM(s.teamCreate))
		r.Get("/teams/list", s.teamList)
		r.Post("/teams/list", s.teamList)
		r.Post("/teams/delete", handleIAM(s.teamDelete))
		r.Post("/teams/members/set", handleIAM(s.teamMemberSet))
		r.Post("/teams/members/remove", handleIAM(s.teamMemberRemove))
		r.Post("/project/members/list", handleIAM(s.projectMembersList))
		r.Post("/project/members/set", handleIAM(s.projectMemberSet))
		r.Post("/project/members/remove", handleIAM(s.projectMemberRemove))
	})
	publicRouteExtensions = append(publicRouteExtensions, func(s *Server, r chi.Router) {
		r.Get("/auth/sso", s.ssoInfo)
		r.Get("/auth/oidc/login", s.oidcLogin)
		r.Get("/auth/oidc/callback", s.oidcCallback)
		r.Post("/auth/logout", s.logout)
	})
	startExtensions = append(startExtensions, startIAM)
}

// ---------------------------------------------------------------- per-server state

type iamState struct {
	sessionKey []byte

	mu    sync.Mutex
	ids   map[string]identity // user id -> cached identity
	audit *auditWriter
	oidc  *oidcClient
}

type identity struct {
	user    *store.User
	roles   *store.UserRoles
	loaded  time.Time
	missing bool
}

const identityTTL = 5 * time.Second

var iamStates sync.Map // *Server -> *iamState

func (s *Server) iam() *iamState {
	if st, ok := iamStates.Load(s); ok {
		return st.(*iamState)
	}
	key := s.cfg.IAM.SessionKey
	if len(key) < 32 {
		key = make([]byte, 32)
		_, _ = rand.Read(key)
	}
	st, _ := iamStates.LoadOrStore(s, &iamState{sessionKey: key, ids: map[string]identity{}})
	return st.(*iamState)
}

func startIAM(s *Server) func() {
	st := s.iam()
	st.audit = newAuditWriter(s)
	if s.cfg.IAM.OIDC != nil {
		st.oidc = newOIDCClient(*s.cfg.IAM.OIDC, s.log)
	}
	stopRetention := startAuditRetention(s)
	return func() {
		stopRetention()
		st.audit.close()
		iamStates.Delete(s)
	}
}

// identity returns a user and their roles, cached for a few seconds.
func (s *Server) identity(ctx context.Context, userID string) (*store.User, *store.UserRoles, error) {
	st := s.iam()
	st.mu.Lock()
	id, ok := st.ids[userID]
	st.mu.Unlock()
	if ok && time.Since(id.loaded) < identityTTL {
		if id.missing {
			return nil, nil, store.ErrNotFound
		}
		return id.user, id.roles, nil
	}
	u, err := s.store.GetUser(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		st.mu.Lock()
		st.ids[userID] = identity{missing: true, loaded: time.Now()}
		st.mu.Unlock()
		return nil, nil, err
	}
	if err != nil {
		return nil, nil, err
	}
	roles, err := s.store.GetUserRoles(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	st.mu.Lock()
	st.ids[userID] = identity{user: u, roles: roles, loaded: time.Now()}
	st.mu.Unlock()
	return u, roles, nil
}

// invalidateIdentities drops cached users and roles after an IAM change.
func (s *Server) invalidateIdentities() {
	st := s.iam()
	st.mu.Lock()
	st.ids = map[string]identity{}
	st.mu.Unlock()
	s.invalidateKeys()
}

// ---------------------------------------------------------------- authentication

// authenticate identifies the caller (static key, managed key or session
// cookie), enforces its access to the request and records it for the audit
// log. It returns the request to serve, or nil after writing 401/403.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) *http.Request {
	note := auditNoteFrom(r)
	if !s.authRequired() {
		if note != nil {
			note.authMethod = "none"
			if u := userFrom(r, nil); u != nil {
				note.actor = *u
			}
		}
		return r
	}
	presented := presentedKey(r)
	if presented != "" {
		k := []byte(presented)
		ok, user := false, ""
		for _, want := range s.keys {
			if subtle.ConstantTimeCompare(k, want.key) == 1 {
				ok, user = true, want.user
			}
		}
		if ok {
			if user != "" {
				r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, user))
			}
			if note != nil {
				note.authMethod, note.actor = "static_key", user
				if user == "" {
					note.actor = "static-key"
				}
			}
			return r
		}
		return s.dbKeyAuth(w, r, presented)
	}
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		return s.sessionAuth(w, r, c.Value)
	}
	return s.dbKeyAuth(w, r, "")
}

// finishAuth attaches a principal to the request and enforces its access.
func (s *Server) finishAuth(w http.ResponseWriter, r *http.Request, p *principal) *http.Request {
	ctx := context.WithValue(r.Context(), principalKey{}, p)
	ctx = context.WithValue(ctx, ctxKey{}, p.user)
	r = r.WithContext(ctx)
	if note := auditNoteFrom(r); note != nil {
		note.actor, note.authMethod = p.user, p.method
		if p.keyID != "" {
			k := p.keyID
			note.keyID = &k
		}
	}
	if err := s.prepareBody(r); err != nil {
		writeErr(w, err)
		return nil
	}
	if code, msg := s.authorize(w, r, p); code != 0 {
		writeError(w, code, msg)
		return nil
	}
	if !p.isAdmin() {
		r = r.WithContext(withProjectGuard(r.Context(), p, classify(r).need()))
	}
	if r.Method == http.MethodGet && r.URL.Path == "/projects" && !(p.isAdmin()) {
		s.listProjectsScoped(w, r, p)
		return nil
	}
	return r
}

// ---------------------------------------------------------------- authorization

type access int

const (
	accRead         access = iota // viewer on every named project
	accWrite                      // member on every named project
	accProjectAdmin               // admin on every named project
	accServerAdmin                // server admin
	accSelf                       // any authenticated caller; the handler checks
)

// serverAdminPaths need a server admin.
var serverAdminPaths = map[string]bool{"/secrets/set": true, "/secrets/delete": true, "/users/create": true, "/users/update": true,
	"/users/revoke_sessions": true, "/teams/create": true, "/teams/delete": true}

// projectAdminPaths need the admin role on the named project.
var projectAdminPaths = map[string]bool{"/project/delete": true, "/project/ttl_settings/update": true, "/project/ttl_settings/purge": true,
	"/project/ingest_sampling_settings/update": true, "/project/sensitive_data_settings/update": true,
	"/project/members/set": true, "/project/members/remove": true, "/api/v1/spans/sessions/sources/restore": true}

// selfPrefixes are checked by their handlers (own keys, teams the caller
// administers, audit entries of projects the caller administers).
var selfPrefixes = []string{"/api_keys/", "/teams/list", "/teams/members/", "/users/list", "/audit/", "/auth/"}

func classify(r *http.Request) access {
	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case serverAdminPaths[path]:
		return accServerAdmin
	case projectAdminPaths[path]:
		return accProjectAdmin
	}
	for _, p := range selfPrefixes {
		if strings.HasPrefix(path, p) {
			return accSelf
		}
	}
	if isReadRequest(r) {
		return accRead
	}
	return accWrite
}

func (a access) role() store.Role {
	switch a {
	case accRead:
		return store.RoleViewer
	case accWrite:
		return store.RoleMember
	}
	return store.RoleAdmin
}

// need is the role a request of this class needs on every project it
// names. Self-service routes check their own rules; the projects they name
// must at least be readable.
func (a access) need() store.Role {
	if a == accSelf {
		return store.RoleViewer
	}
	return a.role()
}

// authorize returns 0 when p may make the request, or a status and reason.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, p *principal) (int, string) {
	acc := classify(r)
	legacy := !p.rbac && !p.super
	switch acc {
	case accServerAdmin:
		if !p.isAdmin() {
			if legacy {
				return http.StatusForbidden, "this API key is not an admin key"
			}
			return http.StatusForbidden, "this needs a server admin"
		}
		return 0, ""
	case accSelf:
		if legacy && strings.HasPrefix(r.URL.Path, "/api_keys/") {
			return http.StatusForbidden, "this API key is not an admin key"
		}
	case accProjectAdmin:
		if legacy {
			return http.StatusForbidden, "this API key is not an admin key"
		}
	}
	if acc != accSelf && p.readOnly && !isReadRequest(r) {
		return http.StatusForbidden, "this API key is read-only"
	}
	if p.isAdmin() {
		return 0, ""
	}
	pids, err := requestProjects(w, r)
	if err != nil {
		var re *requestError
		if errors.As(err, &re) {
			return re.code, re.msg
		}
		return http.StatusForbidden, err.Error()
	}
	if len(pids) == 0 {
		if acc == accSelf || projectFreePath(r.URL.Path) {
			return 0, ""
		}
		if p.projects != nil {
			return http.StatusForbidden, "this API key is limited to specific projects; the request must name a project"
		}
		if !p.rbac && acc != accProjectAdmin {
			return 0, "" // legacy all-project key
		}
		return http.StatusForbidden, "the request must name a project (project_id) so its role can be checked"
	}
	if note := auditNoteFrom(r); note != nil && len(note.projects) == 0 {
		note.projects = pids
	}
	need := acc.need()
	for _, pid := range pids {
		if note := auditNoteFrom(r); note != nil && (p.projects != nil && !p.projects[pid] || p.roleFor(pid).Rank() < need.Rank()) {
			note.projects = append([]string{pid}, without(pids, pid)...) // the refused project first
		}
		if p.projects != nil && !p.projects[pid] {
			return http.StatusForbidden, "this API key has no access to project " + pid
		}
		if have := p.roleFor(pid); have.Rank() < need.Rank() {
			if have == store.RoleNone {
				return http.StatusForbidden, fmt.Sprintf("no access to project %s (needs the %s role)", pid, need)
			}
			return http.StatusForbidden, fmt.Sprintf("the %s role on %s is needed; you have %s", need, pid, have)
		}
	}
	return 0, ""
}

func without(list []string, x string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

// roleFor is the caller's effective role on a project.
func (p *principal) roleFor(pid string) store.Role {
	if p.projects != nil && !p.projects[pid] {
		return store.RoleNone
	}
	var r store.Role
	switch {
	case p.super:
		r = store.RoleAdmin
	case !p.rbac:
		r = store.RoleMember
	default:
		r = p.roles.RoleFor(pid)
	}
	if p.readOnly {
		r = store.MinRole(r, store.RoleViewer)
	}
	return r
}

// refAccess is r's context with the caller's read access as its ref scope
// (store/refscope.go): refs nested in values, expanded columns and
// evaluation inputs resolve only in projects the caller may read. Open
// servers and static keys read every project.
func (s *Server) refAccess(r *http.Request) context.Context {
	p := principalFrom(r)
	if p == nil {
		return store.WithRefAccess(r.Context(), func(string) bool { return true })
	}
	return store.WithRefAccess(r.Context(), func(pid string) bool {
		return p.roleFor(pid).Rank() >= store.RoleViewer.Rank()
	})
}

// isAdmin reports server-admin powers: a static or admin key, or an
// unrestricted key or session of an admin user.
func (p *principal) isAdmin() bool {
	return p.super && p.projects == nil && !p.readOnly
}

// callerIsAdmin is true for server admins, static keys and open servers.
func callerIsAdmin(r *http.Request) bool {
	p := principalFrom(r)
	return p == nil || p.isAdmin()
}

// callerRole is the caller's role on a project (admin for static keys and open servers).
func callerRole(r *http.Request, pid string) store.Role {
	p := principalFrom(r)
	if p == nil {
		return store.RoleAdmin
	}
	return p.roleFor(pid)
}

// ---------------------------------------------------------------- errors

type forbiddenError struct{ msg string }

func (e *forbiddenError) Error() string { return e.msg }

func forbidden(format string, args ...any) error {
	return &forbiddenError{msg: fmt.Sprintf(format, args...)}
}

// handleIAM is handle with 403 for forbiddenError.
func handleIAM[T any](fn func(context.Context, *http.Request, *T) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req := new(T)
		if err := decodeBody(r, req); err != nil {
			writeErr(w, err)
			return
		}
		out, err := fn(r.Context(), r, req)
		var fe *forbiddenError
		switch {
		case errors.As(err, &fe):
			writeError(w, http.StatusForbidden, fe.msg)
		case err != nil:
			writeErr(w, err)
		default:
			writeJSON(w, out)
		}
	}
}

// ---------------------------------------------------------------- /auth/me

func (s *Server) authMe(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	out := map[string]any{"user": nil, "auth_required": s.authRequired(), "auth_method": "none", "server_admin": true,
		"roles": store.UserRoles{Entities: map[string]store.Role{}, Projects: map[string]store.Role{}}, "key_id": nil,
		"read_only": false, "projects": nil, "sso_enabled": s.cfg.IAM.OIDC != nil}
	if note := auditNoteFrom(r); note != nil && note.authMethod != "" {
		out["auth_method"] = note.authMethod
	}
	if u := userFrom(r, nil); u != nil {
		out["user_id"] = *u
	} else {
		out["user_id"] = "anonymous"
	}
	if p != nil {
		out["server_admin"], out["read_only"], out["auth_method"] = p.isAdmin(), p.readOnly, p.method
		if p.keyID != "" {
			out["key_id"] = p.keyID
		}
		if p.projects != nil {
			out["projects"] = sortedKeys(p.projects)
		}
		if p.rbac {
			if u, roles, err := s.identity(r.Context(), p.user); err == nil {
				out["user"], out["roles"] = u, roles
			}
		}
	}
	writeJSON(w, out)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------- users

type userCreateReq struct {
	store.UserCreate
}

func (s *Server) userCreate(ctx context.Context, r *http.Request, req *userCreateReq) (any, error) {
	u, err := s.store.CreateUser(ctx, req.UserCreate)
	if err != nil {
		return nil, err
	}
	s.invalidateIdentities()
	auditDetail(r, "created user %s (admin=%v)", u.ID, u.Admin)
	return map[string]any{"user": u}, nil
}

func (s *Server) userList(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IncludeDisabled bool `json:"include_disabled"`
	}
	if r.Method == http.MethodPost {
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, err)
			return
		}
	} else {
		req.IncludeDisabled = r.URL.Query().Get("include_disabled") == "true"
	}
	if !callerIsAdmin(r) {
		p := principalFrom(r)
		if p == nil || !p.rbac || p.readOnly || !p.roles.AdminOfAny() {
			writeError(w, http.StatusForbidden, "listing users needs a server admin or a team or project admin")
			return
		}
	}
	us, err := s.store.ListUsers(r.Context(), req.IncludeDisabled)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"users": us})
}

type userUpdateReq struct {
	ID string `json:"id"`
	store.UserUpdate
}

func (s *Server) userUpdate(ctx context.Context, r *http.Request, req *userUpdateReq) (any, error) {
	if p := principalFrom(r); p != nil && p.user == req.ID {
		if (req.Admin != nil && !*req.Admin) || (req.Disabled != nil && *req.Disabled) {
			return nil, store.Invalidf("you cannot remove your own admin role or disable yourself")
		}
	}
	u, err := s.store.UpdateUser(ctx, req.ID, req.UserUpdate)
	if err != nil {
		return nil, err
	}
	s.invalidateIdentities()
	auditDetail(r, "updated user %s (admin=%v disabled=%v)", u.ID, u.Admin, u.Disabled())
	return map[string]any{"user": u}, nil
}

type userIDReq struct {
	ID string `json:"id"`
}

func (s *Server) userRevokeSessions(ctx context.Context, r *http.Request, req *userIDReq) (any, error) {
	if _, err := s.store.GetUser(ctx, req.ID); err != nil {
		return nil, err
	}
	n, err := s.store.RevokeUserSessions(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	auditDetail(r, "revoked %d session(s) of %s", n, req.ID)
	return map[string]any{"revoked": n}, nil
}

// ---------------------------------------------------------------- teams

type teamCreateReq struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

func (s *Server) teamCreate(ctx context.Context, r *http.Request, req *teamCreateReq) (any, error) {
	t, err := s.store.CreateTeam(ctx, req.Name, req.DisplayName)
	if err != nil {
		return nil, err
	}
	auditDetail(r, "created team %s", t.Name)
	return map[string]any{"team": t}, nil
}

func (s *Server) teamList(w http.ResponseWriter, r *http.Request) {
	var names []string
	if !callerIsAdmin(r) {
		names = []string{}
		if p := principalFrom(r); p != nil && p.rbac {
			for t := range p.roles.Entities {
				names = append(names, t)
			}
		}
	}
	ts, err := s.store.ListTeams(r.Context(), names)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"teams": ts})
}

type teamReq struct {
	Name string `json:"name"`
}

func (s *Server) teamDelete(ctx context.Context, r *http.Request, req *teamReq) (any, error) {
	if err := s.store.DeleteTeam(ctx, req.Name); err != nil {
		return nil, err
	}
	s.invalidateIdentities()
	auditDetail(r, "deleted team %s", req.Name)
	return map[string]any{}, nil
}

type teamMemberReq struct {
	Team   string `json:"team"`
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// checkTeamAdmin allows server admins and admins of the team.
func checkTeamAdmin(r *http.Request, team string) error {
	if callerIsAdmin(r) {
		return nil
	}
	p := principalFrom(r)
	if p != nil && p.rbac && !p.readOnly && p.projects == nil && p.roles.Entities[team] == store.RoleAdmin {
		return nil
	}
	return forbidden("managing team %s needs a server admin or a team admin", team)
}

func (s *Server) teamMemberSet(ctx context.Context, r *http.Request, req *teamMemberReq) (any, error) {
	if err := checkTeamAdmin(r, req.Team); err != nil {
		return nil, err
	}
	role, err := store.ParseRole(req.Role)
	if err != nil {
		return nil, err
	}
	if err := s.store.SetTeamMember(ctx, req.Team, req.UserID, role); err != nil {
		return nil, err
	}
	s.invalidateIdentities()
	auditDetail(r, "set %s as %s of team %s", req.UserID, role, req.Team)
	t, err := s.store.GetTeam(ctx, req.Team)
	return map[string]any{"team": t}, err
}

func (s *Server) teamMemberRemove(ctx context.Context, r *http.Request, req *teamMemberReq) (any, error) {
	if err := checkTeamAdmin(r, req.Team); err != nil {
		return nil, err
	}
	if err := s.store.RemoveTeamMember(ctx, req.Team, req.UserID); err != nil {
		return nil, err
	}
	s.invalidateIdentities()
	auditDetail(r, "removed %s from team %s", req.UserID, req.Team)
	t, err := s.store.GetTeam(ctx, req.Team)
	return map[string]any{"team": t}, err
}

// ---------------------------------------------------------------- project members

// ProjectMember is one row of /project/members/list.
type ProjectMember struct {
	UserID      string     `json:"user_id"`
	Name        string     `json:"name"`
	Email       *string    `json:"email"`
	Role        store.Role `json:"role"`         // effective role
	ProjectRole store.Role `json:"project_role"` // direct grant on the project ("" when none)
	TeamRole    store.Role `json:"team_role"`    // role in the entity's team ("" when none)
	ServerAdmin bool       `json:"server_admin"`
	Disabled    bool       `json:"disabled"`
}

func (s *Server) projectMembersList(ctx context.Context, r *http.Request, req *projectReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	if callerRole(r, pid).Rank() < store.RoleViewer.Rank() {
		return nil, forbidden("no access to project %s", pid)
	}
	ent, _, _ := strings.Cut(pid, "/")
	byUser := map[string]*ProjectMember{}
	get := func(id string) *ProjectMember {
		if m, ok := byUser[id]; ok {
			return m
		}
		m := &ProjectMember{UserID: id}
		byUser[id] = m
		return m
	}
	prs, err := s.store.ListProjectRoles(ctx, pid)
	if err != nil {
		return nil, err
	}
	for _, x := range prs {
		get(x.UserID).ProjectRole = x.Role
	}
	if t, err := s.store.GetTeam(ctx, ent); err == nil {
		for _, m := range t.Members {
			get(m.UserID).TeamRole = m.Role
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	users, err := s.store.ListUsers(ctx, true)
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		if u.Admin {
			get(u.ID).ServerAdmin = true
		}
		if m, ok := byUser[u.ID]; ok {
			m.Name, m.Email, m.Disabled = u.Name, u.Email, u.Disabled()
		}
	}
	out := make([]ProjectMember, 0, len(byUser))
	for _, m := range byUser {
		m.Role = store.MaxRole(m.ProjectRole, m.TeamRole)
		if m.ServerAdmin {
			m.Role = store.RoleAdmin
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return map[string]any{"project_id": pid, "entity": ent, "members": out, "can_manage": callerRole(r, pid) == store.RoleAdmin}, nil
}

type projectMemberReq struct {
	ProjectID string `json:"project_id"`
	UserID    string `json:"user_id"`
	Role      string `json:"role"`
}

func (s *Server) projectMemberSet(ctx context.Context, r *http.Request, req *projectMemberReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	role, err := store.ParseRole(req.Role)
	if err != nil {
		return nil, err
	}
	if err := s.store.SetProjectRole(ctx, pid, req.UserID, role); err != nil {
		return nil, err
	}
	s.invalidateIdentities()
	auditDetail(r, "set %s as %s of %s", req.UserID, role, pid)
	return map[string]any{"project_id": pid, "user_id": req.UserID, "role": role}, nil
}

func (s *Server) projectMemberRemove(ctx context.Context, r *http.Request, req *projectMemberReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	if err := s.store.RemoveProjectRole(ctx, pid, req.UserID); err != nil {
		return nil, err
	}
	s.invalidateIdentities()
	auditDetail(r, "removed %s from %s", req.UserID, pid)
	return map[string]any{}, nil
}

// normalizeProjects validates and de-duplicates project ids.
func normalizeProjects(in []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, p := range in {
		pid, err := refs.NormalizeProject(strings.TrimSpace(p))
		if err != nil {
			return nil, &store.InvalidError{Msg: err.Error()}
		}
		if !seen[pid] {
			seen[pid] = true
			out = append(out, pid)
		}
	}
	return out, nil
}
