package store

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"sort"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// Identity and access: users, teams (entities) with members, per-project
// roles and browser sessions. See docs/weft/API.md "Users, teams and roles".

func init() {
	registerSchema(`
CREATE TABLE IF NOT EXISTS users (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	email TEXT,
	admin INTEGER NOT NULL DEFAULT 0,
	oidc_subject TEXT,
	created_at INTEGER NOT NULL,
	disabled_at INTEGER,
	last_login_at INTEGER
);
CREATE UNIQUE INDEX IF NOT EXISTS users_email ON users(email) WHERE email IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS users_oidc ON users(oidc_subject) WHERE oidc_subject IS NOT NULL;
CREATE TABLE IF NOT EXISTS teams (
	name TEXT PRIMARY KEY,
	display_name TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS team_members (
	team TEXT NOT NULL,
	user_id TEXT NOT NULL,
	role TEXT NOT NULL,
	source TEXT NOT NULL DEFAULT 'manual',
	created_at INTEGER NOT NULL,
	PRIMARY KEY (team, user_id)
);
CREATE INDEX IF NOT EXISTS team_members_user ON team_members(user_id);
CREATE TABLE IF NOT EXISTS project_roles (
	project_id TEXT NOT NULL,
	user_id TEXT NOT NULL,
	role TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (project_id, user_id)
);
CREATE INDEX IF NOT EXISTS project_roles_user ON project_roles(user_id);
CREATE TABLE IF NOT EXISTS sessions (
	id_hash TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	csrf TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	revoked_at INTEGER,
	ip TEXT NOT NULL DEFAULT '',
	user_agent TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS sessions_user ON sessions(user_id);`)
	registerMigration(func(db *sql.DB) error {
		ok, err := HasColumn(db, "api_keys", "user_bound")
		if err != nil || ok {
			return err
		}
		_, err = db.Exec(`ALTER TABLE api_keys ADD COLUMN user_bound INTEGER NOT NULL DEFAULT 0`)
		return err
	})
}

// Role is an access level on a project or an entity (team).
type Role string

// Roles, weakest first.
const (
	RoleNone   Role = ""
	RoleViewer Role = "viewer" // read
	RoleMember Role = "member" // read and write
	RoleAdmin  Role = "admin"  // also manage members, keys and settings, delete the project
)

// Rank orders roles: none 0, viewer 1, member 2, admin 3.
func (r Role) Rank() int {
	switch r {
	case RoleViewer:
		return 1
	case RoleMember:
		return 2
	case RoleAdmin:
		return 3
	}
	return 0
}

// ParseRole validates a role name.
func ParseRole(s string) (Role, error) {
	switch r := Role(strings.ToLower(strings.TrimSpace(s))); r {
	case RoleViewer, RoleMember, RoleAdmin:
		return r, nil
	}
	return RoleNone, Invalidf(`role must be "viewer", "member" or "admin"`)
}

// MaxRole returns the stronger of two roles.
func MaxRole(a, b Role) Role {
	if b.Rank() > a.Rank() {
		return b
	}
	return a
}

// MinRole returns the weaker of two roles.
func MinRole(a, b Role) Role {
	if b.Rank() < a.Rank() {
		return b
	}
	return a
}

// User is a person (or service account) that owns API keys and sessions.
type User struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Email       *string `json:"email"`
	Admin       bool    `json:"admin"`
	SSO         bool    `json:"sso"`
	CreatedAt   string  `json:"created_at"`
	DisabledAt  *string `json:"disabled_at"`
	LastLoginAt *string `json:"last_login_at"`

	Subject string `json:"-"`
}

// Disabled reports whether the user may not sign in or use keys.
func (u *User) Disabled() bool { return u.DisabledAt != nil }

// UserCreate is the input of CreateUser.
type UserCreate struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Email   *string `json:"email"`
	Admin   bool    `json:"admin"`
	Subject string  `json:"-"`
}

// UserUpdate changes the given fields.
type UserUpdate struct {
	Name     *string `json:"name"`
	Email    *string `json:"email"`
	Admin    *bool   `json:"admin"`
	Disabled *bool   `json:"disabled"`
}

// UserIDRe is the shape of user ids (also the default wb_user_id).
var UserIDRe = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,64}$`)

// TeamNameRe is the shape of team (entity) names.
var TeamNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

var emailRe = regexp.MustCompile(`^[^@\s]{1,64}@[^@\s]{1,255}$`)

func normEmail(e *string) (*string, error) {
	if e == nil {
		return nil, nil
	}
	v := strings.ToLower(strings.TrimSpace(*e))
	if v == "" {
		return nil, nil
	}
	if !emailRe.MatchString(v) {
		return nil, Invalidf("invalid email %q", v)
	}
	return &v, nil
}

const userCols = `id, name, email, admin, oidc_subject, created_at, disabled_at, last_login_at`

func scanUser(sc interface{ Scan(...any) error }) (*User, error) {
	var u User
	var email, subject sql.NullString
	var created int64
	var disabled, login sql.NullInt64
	if err := sc.Scan(&u.ID, &u.Name, &email, &u.Admin, &subject, &created, &disabled, &login); err != nil {
		return nil, err
	}
	u.Email = strPtr(email)
	u.Subject = subject.String
	u.SSO = subject.Valid
	u.CreatedAt = FormatMicros(created)
	u.DisabledAt = fmtNullMicros(disabled)
	u.LastLoginAt = fmtNullMicros(login)
	return &u, nil
}

func isUniqueErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// CreateUser adds a user.
func (s *Store) CreateUser(ctx context.Context, c UserCreate) (*User, error) {
	c.ID = strings.TrimSpace(c.ID)
	if !UserIDRe.MatchString(c.ID) {
		return nil, Invalidf("user id must match %s", UserIDRe)
	}
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		c.Name = c.ID
	}
	if len(c.Name) > 128 {
		return nil, Invalidf("name too long")
	}
	email, err := normEmail(c.Email)
	if err != nil {
		return nil, err
	}
	var subject any
	if c.Subject != "" {
		subject = c.Subject
	}
	_, err = s.w.ExecContext(ctx, `INSERT INTO users(id, name, email, admin, oidc_subject, created_at) VALUES(?,?,?,?,?,?)`,
		c.ID, c.Name, nullStr(email), c.Admin, subject, nowMicros())
	if isUniqueErr(err) {
		return nil, Invalidf("a user with this id or email already exists")
	}
	if err != nil {
		return nil, err
	}
	return s.GetUser(ctx, c.ID)
}

// GetUser reads a user by id.
func (s *Store) GetUser(ctx context.Context, id string) (*User, error) {
	u, err := scanUser(s.r.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// GetUserBySubject reads the user linked to an OIDC issuer|subject.
func (s *Store) GetUserBySubject(ctx context.Context, subject string) (*User, error) {
	u, err := scanUser(s.r.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE oidc_subject = ?`, subject))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// GetUserByEmail reads a user by (lower-cased) email.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	u, err := scanUser(s.r.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email = ?`, strings.ToLower(strings.TrimSpace(email))))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// ListUsers lists users by id.
func (s *Store) ListUsers(ctx context.Context, includeDisabled bool) ([]User, error) {
	q := `SELECT ` + userCols + ` FROM users`
	if !includeDisabled {
		q += ` WHERE disabled_at IS NULL`
	}
	rows, err := s.r.QueryContext(ctx, q+` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// UpdateUser changes a user. Disabling also revokes the user's sessions.
func (s *Store) UpdateUser(ctx context.Context, id string, up UserUpdate) (*User, error) {
	if _, err := s.GetUser(ctx, id); err != nil {
		return nil, err
	}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if up.Name != nil {
			n := strings.TrimSpace(*up.Name)
			if n == "" || len(n) > 128 {
				return Invalidf("name is required (max 128 characters)")
			}
			if _, err := tx.ExecContext(ctx, `UPDATE users SET name = ? WHERE id = ?`, n, id); err != nil {
				return err
			}
		}
		if up.Email != nil {
			e, err := normEmail(up.Email)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE users SET email = ? WHERE id = ?`, nullStr(e), id); isUniqueErr(err) {
				return Invalidf("another user has this email")
			} else if err != nil {
				return err
			}
		}
		if up.Admin != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE users SET admin = ? WHERE id = ?`, *up.Admin, id); err != nil {
				return err
			}
		}
		if up.Disabled != nil {
			if *up.Disabled {
				now := nowMicros()
				if _, err := tx.ExecContext(ctx, `UPDATE users SET disabled_at = COALESCE(disabled_at, ?) WHERE id = ?`, now, id); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now, id); err != nil {
					return err
				}
			} else if _, err := tx.ExecContext(ctx, `UPDATE users SET disabled_at = NULL WHERE id = ?`, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetUser(ctx, id)
}

// LinkUserSubject links a user to an OIDC issuer|subject and records a login.
func (s *Store) LinkUserSubject(ctx context.Context, id, subject string) error {
	_, err := s.w.ExecContext(ctx, `UPDATE users SET oidc_subject = ?, last_login_at = ? WHERE id = ?`, subject, nowMicros(), id)
	if isUniqueErr(err) {
		return Invalidf("this identity is linked to another user")
	}
	return err
}

// CountAdmins counts enabled server admins.
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE admin = 1 AND disabled_at IS NULL`).Scan(&n)
	return n, err
}

// ---------------------------------------------------------------- teams

// Team is an entity: the first part of a project id. Its members' roles
// apply to every project of the entity.
type Team struct {
	Name        string       `json:"name"`
	DisplayName string       `json:"display_name"`
	CreatedAt   string       `json:"created_at"`
	Members     []TeamMember `json:"members"`
}

// TeamMember is a user's role in a team. Source is "manual" or "sso"
// (synchronised from the identity provider's groups at each login).
type TeamMember struct {
	UserID string `json:"user_id"`
	Role   Role   `json:"role"`
	Source string `json:"source"`
}

// CreateTeam adds a team. Creating an existing team is an error.
func (s *Store) CreateTeam(ctx context.Context, name, display string) (*Team, error) {
	name = strings.TrimSpace(name)
	if !TeamNameRe.MatchString(name) {
		return nil, Invalidf("team name must match %s", TeamNameRe)
	}
	_, err := s.w.ExecContext(ctx, `INSERT INTO teams(name, display_name, created_at) VALUES(?,?,?)`, name, strings.TrimSpace(display), nowMicros())
	if isUniqueErr(err) {
		return nil, Invalidf("team %q already exists", name)
	}
	if err != nil {
		return nil, err
	}
	return s.GetTeam(ctx, name)
}

func (s *Store) ensureTeam(ctx context.Context, tx *sql.Tx, name string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO teams(name, display_name, created_at) VALUES(?, '', ?) ON CONFLICT(name) DO NOTHING`, name, nowMicros())
	return err
}

// GetTeam reads a team with its members.
func (s *Store) GetTeam(ctx context.Context, name string) (*Team, error) {
	ts, err := s.ListTeams(ctx, []string{name})
	if err != nil {
		return nil, err
	}
	if len(ts) == 0 {
		return nil, ErrNotFound
	}
	return &ts[0], nil
}

// ListTeams lists teams (all when names is nil) with members.
func (s *Store) ListTeams(ctx context.Context, names []string) ([]Team, error) {
	if names != nil && len(names) == 0 {
		return []Team{}, nil
	}
	q := `SELECT name, display_name, created_at FROM teams`
	var args []any
	if names != nil {
		q += ` WHERE name IN (` + placeholders(len(names)) + `)`
		for _, n := range names {
			args = append(args, n)
		}
	}
	rows, err := s.r.QueryContext(ctx, q+` ORDER BY name`, args...)
	if err != nil {
		return nil, err
	}
	out := []Team{}
	idx := map[string]int{}
	for rows.Next() {
		var t Team
		var created int64
		if err := rows.Scan(&t.Name, &t.DisplayName, &created); err != nil {
			rows.Close()
			return nil, err
		}
		t.CreatedAt = FormatMicros(created)
		t.Members = []TeamMember{}
		idx[t.Name] = len(out)
		out = append(out, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	mrows, err := s.r.QueryContext(ctx, `SELECT team, user_id, role, source FROM team_members ORDER BY team, user_id`)
	if err != nil {
		return nil, err
	}
	defer mrows.Close()
	for mrows.Next() {
		var team string
		var m TeamMember
		if err := mrows.Scan(&team, &m.UserID, &m.Role, &m.Source); err != nil {
			return nil, err
		}
		if i, ok := idx[team]; ok {
			out[i].Members = append(out[i].Members, m)
		}
	}
	return out, mrows.Err()
}

// DeleteTeam removes a team and its memberships (projects are kept).
func (s *Store) DeleteTeam(ctx context.Context, name string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM teams WHERE name = ?`, name)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM team_members WHERE team = ?`, name)
		return err
	})
}

// SetTeamMember adds a user to a team or changes their role.
func (s *Store) SetTeamMember(ctx context.Context, team, userID string, role Role) error {
	if role.Rank() == 0 {
		return Invalidf("invalid role")
	}
	if _, err := s.GetTeam(ctx, team); err != nil {
		return err
	}
	if _, err := s.GetUser(ctx, userID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return Invalidf("unknown user %q", userID)
		}
		return err
	}
	_, err := s.w.ExecContext(ctx, `INSERT INTO team_members(team, user_id, role, source, created_at) VALUES(?,?,?, 'manual', ?)
		ON CONFLICT(team, user_id) DO UPDATE SET role = excluded.role, source = 'manual'`, team, userID, string(role), nowMicros())
	return err
}

// RemoveTeamMember removes a user from a team.
func (s *Store) RemoveTeamMember(ctx context.Context, team, userID string) error {
	res, err := s.w.ExecContext(ctx, `DELETE FROM team_members WHERE team = ? AND user_id = ?`, team, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SyncSSOMemberships replaces a user's identity-provider-derived team roles
// (source "sso") with roles; manual memberships are kept. Teams are created
// as needed.
func (s *Store) SyncSSOMemberships(ctx context.Context, userID string, roles map[string]Role) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM team_members WHERE user_id = ? AND source = 'sso'`, userID); err != nil {
			return err
		}
		teams := make([]string, 0, len(roles))
		for t := range roles {
			teams = append(teams, t)
		}
		sort.Strings(teams)
		for _, t := range teams {
			if err := s.ensureTeam(ctx, tx, t); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO team_members(team, user_id, role, source, created_at) VALUES(?,?,?, 'sso', ?)
				ON CONFLICT(team, user_id) DO NOTHING`, t, userID, string(roles[t]), nowMicros()); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---------------------------------------------------------------- project roles

// ProjectRole is a user's role on one project.
type ProjectRole struct {
	ProjectID string `json:"project_id"`
	UserID    string `json:"user_id"`
	Role      Role   `json:"role"`
}

// SetProjectRole grants a user a role on a project.
func (s *Store) SetProjectRole(ctx context.Context, projectID, userID string, role Role) error {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return &InvalidError{Msg: err.Error()}
	}
	if role.Rank() == 0 {
		return Invalidf("invalid role")
	}
	if _, err := s.GetUser(ctx, userID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return Invalidf("unknown user %q", userID)
		}
		return err
	}
	_, err = s.w.ExecContext(ctx, `INSERT INTO project_roles(project_id, user_id, role, created_at) VALUES(?,?,?,?)
		ON CONFLICT(project_id, user_id) DO UPDATE SET role = excluded.role`, pid, userID, string(role), nowMicros())
	return err
}

// RemoveProjectRole removes a user's project role.
func (s *Store) RemoveProjectRole(ctx context.Context, projectID, userID string) error {
	res, err := s.w.ExecContext(ctx, `DELETE FROM project_roles WHERE project_id = ? AND user_id = ?`, projectID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListProjectRoles lists the direct roles on a project.
func (s *Store) ListProjectRoles(ctx context.Context, projectID string) ([]ProjectRole, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT project_id, user_id, role FROM project_roles WHERE project_id = ? ORDER BY user_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectRole{}
	for rows.Next() {
		var p ProjectRole
		if err := rows.Scan(&p.ProjectID, &p.UserID, &p.Role); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UserRoles are the roles a user holds, by entity (team) and by project.
type UserRoles struct {
	Entities map[string]Role `json:"entities"`
	Projects map[string]Role `json:"projects"`
}

// RoleFor is the user's effective role on a project: the stronger of the
// team (entity) role and the direct project role.
func (u *UserRoles) RoleFor(projectID string) Role {
	if u == nil {
		return RoleNone
	}
	ent, _, _ := strings.Cut(projectID, "/")
	return MaxRole(u.Entities[ent], u.Projects[projectID])
}

// AdminOfAny reports whether the user administers any team or project.
func (u *UserRoles) AdminOfAny() bool {
	if u == nil {
		return false
	}
	for _, r := range u.Entities {
		if r == RoleAdmin {
			return true
		}
	}
	for _, r := range u.Projects {
		if r == RoleAdmin {
			return true
		}
	}
	return false
}

// GetUserRoles loads a user's team and project roles.
func (s *Store) GetUserRoles(ctx context.Context, userID string) (*UserRoles, error) {
	out := &UserRoles{Entities: map[string]Role{}, Projects: map[string]Role{}}
	rows, err := s.r.QueryContext(ctx, `SELECT team, role FROM team_members WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t string
		var r Role
		if err := rows.Scan(&t, &r); err != nil {
			rows.Close()
			return nil, err
		}
		out.Entities[t] = MaxRole(out.Entities[t], r)
	}
	rows.Close()
	rows, err = s.r.QueryContext(ctx, `SELECT project_id, role FROM project_roles WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		var r Role
		if err := rows.Scan(&p, &r); err != nil {
			return nil, err
		}
		out.Projects[p] = MaxRole(out.Projects[p], r)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- sessions

// Session is a signed-in browser. Only the SHA-256 of the session id is stored.
type Session struct {
	IDHash    string `json:"-"`
	UserID    string `json:"user_id"`
	CSRF      string `json:"-"`
	CreatedAt int64  `json:"-"`
	ExpiresAt int64  `json:"-"`
	IP        string `json:"ip"`
	UserAgent string `json:"user_agent"`
}

// CreateSession stores a session.
func (s *Store) CreateSession(ctx context.Context, x Session) error {
	if len(x.UserAgent) > 256 {
		x.UserAgent = x.UserAgent[:256]
	}
	_, err := s.w.ExecContext(ctx, `INSERT INTO sessions(id_hash, user_id, csrf, created_at, expires_at, ip, user_agent) VALUES(?,?,?,?,?,?,?)`,
		x.IDHash, x.UserID, x.CSRF, nowMicros(), x.ExpiresAt, x.IP, x.UserAgent)
	return err
}

// GetSession reads an active (unexpired, unrevoked) session.
func (s *Store) GetSession(ctx context.Context, idHash string) (*Session, error) {
	var x Session
	err := s.r.QueryRowContext(ctx, `SELECT id_hash, user_id, csrf, created_at, expires_at, ip, user_agent FROM sessions
		WHERE id_hash = ? AND revoked_at IS NULL AND expires_at > ?`, idHash, nowMicros()).
		Scan(&x.IDHash, &x.UserID, &x.CSRF, &x.CreatedAt, &x.ExpiresAt, &x.IP, &x.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &x, err
}

// RevokeSession ends one session.
func (s *Store) RevokeSession(ctx context.Context, idHash string) error {
	_, err := s.w.ExecContext(ctx, `UPDATE sessions SET revoked_at = COALESCE(revoked_at, ?) WHERE id_hash = ?`, nowMicros(), idHash)
	return err
}

// RevokeUserSessions ends every session of a user and returns how many were active.
func (s *Store) RevokeUserSessions(ctx context.Context, userID string) (int64, error) {
	res, err := s.w.ExecContext(ctx, `UPDATE sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL AND expires_at > ?`,
		nowMicros(), userID, nowMicros())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PurgeSessions deletes sessions that expired or were revoked before cutoff.
func (s *Store) PurgeSessions(ctx context.Context, cutoffMicros int64) error {
	_, err := s.w.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ? OR (revoked_at IS NOT NULL AND revoked_at < ?)`, cutoffMicros, cutoffMicros)
	return err
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}
