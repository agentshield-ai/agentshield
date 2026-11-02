package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// The audit log records mutating requests and admin actions. Its project
// column is deliberately not named project_id, so deleting a project keeps
// the record of who deleted it.

func init() {
	registerSchema(`
CREATE TABLE IF NOT EXISTS audit_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts INTEGER NOT NULL,
	actor TEXT NOT NULL,
	auth_method TEXT NOT NULL,
	key_id TEXT,
	method TEXT NOT NULL,
	path TEXT NOT NULL,
	project TEXT,
	object_ids TEXT NOT NULL DEFAULT '[]',
	call_ids TEXT NOT NULL DEFAULT '[]',
	status INTEGER NOT NULL,
	outcome TEXT NOT NULL,
	ip TEXT NOT NULL DEFAULT '',
	detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS audit_ts ON audit_log(ts);
CREATE INDEX IF NOT EXISTS audit_project ON audit_log(project, ts);
CREATE INDEX IF NOT EXISTS audit_actor ON audit_log(actor, ts);`)
}

// AuditEntry is one audit record.
type AuditEntry struct {
	ID         int64    `json:"id"`
	Time       string   `json:"ts"`
	TimeMicros int64    `json:"-"`
	Actor      string   `json:"actor"`
	AuthMethod string   `json:"auth_method"` // static_key, api_key, session, anonymous, none
	KeyID      *string  `json:"key_id"`
	Method     string   `json:"method"`
	Path       string   `json:"path"`
	Project    *string  `json:"project_id"`
	ObjectIDs  []string `json:"object_ids"`
	CallIDs    []string `json:"call_ids"`
	Status     int      `json:"status"`
	Outcome    string   `json:"outcome"` // success, denied, unauthenticated, failed
	IP         string   `json:"ip"`
	Detail     string   `json:"detail"`
}

// AuditFilter selects audit entries. Empty fields match everything.
type AuditFilter struct {
	Projects   []string // nil: any project (including none)
	Actor      string
	Method     string
	PathPrefix string
	Outcome    string
	Since      int64 // micros, inclusive
	Until      int64 // micros, exclusive
	Limit      int
	Offset     int
}

// InsertAudit appends entries.
func (s *Store) InsertAudit(ctx context.Context, entries []AuditEntry) error {
	if len(entries) == 0 {
		return nil
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		st, err := tx.PrepareContext(ctx, `INSERT INTO audit_log(ts, actor, auth_method, key_id, method, path, project, object_ids, call_ids, status, outcome, ip, detail)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer st.Close()
		for _, e := range entries {
			if e.TimeMicros == 0 {
				e.TimeMicros = nowMicros()
			}
			if _, err := st.ExecContext(ctx, e.TimeMicros, e.Actor, e.AuthMethod, nullStr(e.KeyID), e.Method, e.Path, nullStr(e.Project),
				mustJSON(nonNilStrs(e.ObjectIDs)), mustJSON(nonNilStrs(e.CallIDs)), e.Status, e.Outcome, e.IP, e.Detail); err != nil {
				return err
			}
		}
		return nil
	})
}

func nonNilStrs(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// QueryAudit returns matching entries, newest first, and the total match count.
func (s *Store) QueryAudit(ctx context.Context, f AuditFilter) ([]AuditEntry, int, error) {
	var where []string
	var args []any
	if f.Projects != nil {
		if len(f.Projects) == 0 {
			return []AuditEntry{}, 0, nil
		}
		where = append(where, `project IN (`+placeholders(len(f.Projects))+`)`)
		for _, p := range f.Projects {
			args = append(args, p)
		}
	}
	if f.Actor != "" {
		where = append(where, `actor = ?`)
		args = append(args, f.Actor)
	}
	if f.Method != "" {
		where = append(where, `method = ?`)
		args = append(args, strings.ToUpper(f.Method))
	}
	if f.PathPrefix != "" {
		where = append(where, `substr(path, 1, ?) = ?`)
		args = append(args, len(f.PathPrefix), f.PathPrefix)
	}
	if f.Outcome != "" {
		where = append(where, `outcome = ?`)
		args = append(args, f.Outcome)
	}
	if f.Since > 0 {
		where = append(where, `ts >= ?`)
		args = append(args, f.Since)
	}
	if f.Until > 0 {
		where = append(where, `ts < ?`)
		args = append(args, f.Until)
	}
	cond := ""
	if len(where) > 0 {
		cond = ` WHERE ` + strings.Join(where, ` AND `)
	}
	var total int
	if err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.Limit <= 0 || f.Limit > 10000 {
		f.Limit = 100
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	rows, err := s.r.QueryContext(ctx, `SELECT id, ts, actor, auth_method, key_id, method, path, project, object_ids, call_ids, status, outcome, ip, detail
		FROM audit_log`+cond+` ORDER BY ts DESC, id DESC LIMIT ? OFFSET ?`, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var key, project sql.NullString
		var objs, calls string
		if err := rows.Scan(&e.ID, &e.TimeMicros, &e.Actor, &e.AuthMethod, &key, &e.Method, &e.Path, &project, &objs, &calls,
			&e.Status, &e.Outcome, &e.IP, &e.Detail); err != nil {
			return nil, 0, err
		}
		e.Time = FormatMicros(e.TimeMicros)
		e.KeyID, e.Project = strPtr(key), strPtr(project)
		_ = json.Unmarshal([]byte(objs), &e.ObjectIDs)
		_ = json.Unmarshal([]byte(calls), &e.CallIDs)
		e.ObjectIDs, e.CallIDs = nonNilStrs(e.ObjectIDs), nonNilStrs(e.CallIDs)
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// PurgeAudit deletes entries older than cutoff (micros).
func (s *Store) PurgeAudit(ctx context.Context, cutoffMicros int64) (int64, error) {
	res, err := s.w.ExecContext(ctx, `DELETE FROM audit_log WHERE ts < ?`, cutoffMicros)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
