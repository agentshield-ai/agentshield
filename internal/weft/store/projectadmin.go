package store

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// Per-project admin settings (retention TTL, ingest sampling, sensitive-data
// redaction), the ingest hook that applies them, the retention purge and
// project deletion.

func init() {
	registerSchema(`
CREATE TABLE IF NOT EXISTS project_settings (
	project_id TEXT NOT NULL,
	key TEXT NOT NULL,
	value TEXT NOT NULL,
	updated_at INTEGER NOT NULL,
	updated_by TEXT,
	PRIMARY KEY (project_id, key)
);
CREATE TABLE IF NOT EXISTS ingest_dropped (
	project_id TEXT NOT NULL,
	id TEXT NOT NULL,
	dropped_at INTEGER NOT NULL,
	PRIMARY KEY (project_id, id)
);
CREATE INDEX IF NOT EXISTS ingest_dropped_at ON ingest_dropped(dropped_at);`)
	ingestHook = applyIngestSettings
}

// Setting keys.
const (
	SettingTTL           = "ttl"
	SettingIngestSample  = "ingest_sampling"
	SettingSensitiveData = "sensitive_data"
)

// TTLSettings is a project's retention policy.
type TTLSettings struct {
	// RetentionDays nil means keep forever.
	RetentionDays *int `json:"retention_days"`
}

// SamplingSettings is a project's ingest sampling policy.
type SamplingSettings struct {
	SampleRate float64 `json:"sample_rate"`
	// DryRun keeps every call but marks the ones that would have been dropped.
	DryRun bool `json:"dry_run"`
}

// RedactionPattern is a user-defined regex replaced at ingest.
type RedactionPattern struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
}

// SensitiveDataSettings is a project's ingest redaction policy.
type SensitiveDataSettings struct {
	// Policy is "off" or "pii-v1" (Weave's names).
	Policy string `json:"policy"`
	// Entities restricts the built-in detectors; empty means all.
	Entities       []string           `json:"entities"`
	CustomPatterns []RedactionPattern `json:"custom_patterns"`
}

// GetProjectSetting decodes a setting into out and reports whether it exists.
func (s *Store) GetProjectSetting(ctx context.Context, projectID, key string, out any) (bool, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return false, &InvalidError{Msg: err.Error()}
	}
	var raw string
	err = s.r.QueryRowContext(ctx, `SELECT value FROM project_settings WHERE project_id = ? AND key = ?`, pid, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), out)
}

// SetProjectSetting stores a setting; a nil value deletes it.
func (s *Store) SetProjectSetting(ctx context.Context, projectID, key string, val any, user *string) error {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return &InvalidError{Msg: err.Error()}
	}
	defer s.invalidateIngest(pid)
	if val == nil {
		_, err = s.w.ExecContext(ctx, `DELETE FROM project_settings WHERE project_id = ? AND key = ?`, pid, key)
		return err
	}
	b, err := json.Marshal(val)
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.touchProject(ctx, tx, pid); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO project_settings(project_id, key, value, updated_at, updated_by) VALUES(?,?,?,?,?)
			ON CONFLICT(project_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
			pid, key, string(b), nowMicros(), nullStr(user))
		return err
	})
}

// ---------------------------------------------------------------- ingest hook

// ingestPolicy is the compiled form of a project's ingest settings.
type ingestPolicy struct {
	sampling *SamplingSettings
	redactor *Redactor
}

var (
	ingestCacheMu sync.Mutex
	ingestCache   = map[*Store]map[string]*ingestPolicy{}
)

func (s *Store) invalidateIngest(projectID string) {
	ingestCacheMu.Lock()
	defer ingestCacheMu.Unlock()
	if projectID == "" {
		delete(ingestCache, s)
		return
	}
	delete(ingestCache[s], projectID)
}

func (s *Store) ingestPolicyFor(ctx context.Context, tx *sql.Tx, projectID string) (*ingestPolicy, error) {
	ingestCacheMu.Lock()
	if p, ok := ingestCache[s][projectID]; ok {
		ingestCacheMu.Unlock()
		return p, nil
	}
	ingestCacheMu.Unlock()
	p := &ingestPolicy{}
	rows, err := tx.QueryContext(ctx, `SELECT key, value FROM project_settings WHERE project_id = ? AND key IN (?, ?)`,
		projectID, SettingIngestSample, SettingSensitiveData)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			return nil, err
		}
		switch k {
		case SettingIngestSample:
			var ss SamplingSettings
			if json.Unmarshal([]byte(v), &ss) == nil {
				p.sampling = &ss
			}
		case SettingSensitiveData:
			var sd SensitiveDataSettings
			if json.Unmarshal([]byte(v), &sd) == nil && sd.Policy != "" && sd.Policy != "off" {
				if r, err := NewRedactor(sd); err == nil {
					p.redactor = r
				}
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ingestCacheMu.Lock()
	if ingestCache[s] == nil {
		ingestCache[s] = map[string]*ingestPolicy{}
	}
	ingestCache[s][projectID] = p
	ingestCacheMu.Unlock()
	return p, nil
}

// SampleTrace reports whether a trace is kept at the given rate. The decision
// depends only on the trace id, so a root and all its descendants agree.
func SampleTrace(traceID string, rate float64) bool {
	if rate >= 1 {
		return true
	}
	if rate <= 0 {
		return false
	}
	// SHA-256 of the trace id: stable across processes and restarts, and
	// well mixed even for short, similar ids.
	sum := sha256.Sum256([]byte(traceID))
	h := binary.BigEndian.Uint64(sum[:8])
	return float64(h>>11)/float64(1<<53) < rate
}

// applyIngestSettings is the ingest hook: it drops calls of sampled-out
// traces and redacts sensitive data in the rest.
func applyIngestSettings(ctx context.Context, s *Store, tx *sql.Tx, st *CallStart, en *CallEnd) (bool, error) {
	pid := ""
	if st != nil {
		pid = st.ProjectID
	} else {
		pid = en.ProjectID
	}
	p, err := s.ingestPolicyFor(ctx, tx, pid)
	if err != nil {
		return false, err
	}
	if p.sampling != nil {
		if st != nil {
			if !SampleTrace(st.TraceID, p.sampling.SampleRate) {
				if p.sampling.DryRun {
					st.Attributes["weft_ingest_sampling"] = map[string]any{"would_drop": true, "sample_rate": p.sampling.SampleRate}
				} else {
					_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO ingest_dropped(project_id, id, dropped_at) VALUES(?,?,?)`, pid, st.ID, nowMicros())
					return true, err
				}
			}
		} else {
			var one int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM ingest_dropped WHERE project_id = ? AND id = ?`, pid, en.ID).Scan(&one)
			if err == nil {
				return true, nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return false, err
			}
		}
	}
	if p.redactor != nil {
		if st != nil {
			st.Inputs, _ = p.redactor.Value(st.Inputs).(map[string]any)
			st.Attributes, _ = p.redactor.Value(st.Attributes).(map[string]any)
			if st.Inputs == nil {
				st.Inputs = map[string]any{}
			}
			if st.Attributes == nil {
				st.Attributes = map[string]any{}
			}
		} else {
			en.Output = p.redactor.Value(en.Output)
			if en.Exception != nil {
				x := p.redactor.String(*en.Exception)
				en.Exception = &x
			}
		}
	}
	return false, nil
}

// ---------------------------------------------------------------- retention

// ProjectsWithTTL returns project ids with a retention policy and their days.
func (s *Store) ProjectsWithTTL(ctx context.Context) (map[string]int, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT project_id, value FROM project_settings WHERE key = ?`, SettingTTL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var pid, v string
		if err := rows.Scan(&pid, &v); err != nil {
			return nil, err
		}
		var t TTLSettings
		if json.Unmarshal([]byte(v), &t) == nil && t.RetentionDays != nil && *t.RetentionDays >= 1 {
			out[pid] = *t.RetentionDays
		}
	}
	return out, rows.Err()
}

// PurgeResult counts rows removed by a retention purge.
type PurgeResult struct {
	Calls    int64 `json:"calls"`
	Feedback int64 `json:"feedback"`
}

// PurgeBefore hard-deletes a project's calls that started before the cutoff,
// and feedback on calls created before it. Objects, tables and files are
// versioned assets and are kept.
func (s *Store) PurgeBefore(ctx context.Context, projectID string, cutoff time.Time) (PurgeResult, error) {
	var res PurgeResult
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return res, &InvalidError{Msg: err.Error()}
	}
	us := cutoff.UnixMicro()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `DELETE FROM calls WHERE project_id = ? AND started_at < ?`, pid, us)
		if err != nil {
			return err
		}
		res.Calls, _ = r.RowsAffected()
		r, err = tx.ExecContext(ctx, `DELETE FROM feedback WHERE project_id = ? AND created_at < ? AND weave_ref LIKE ?`,
			pid, us, "%/call/%")
		if err != nil {
			return err
		}
		res.Feedback, _ = r.RowsAffected()
		return nil
	})
	return res, err
}

// PurgeDroppedMarkers forgets sampled-out call ids older than the cutoff.
func (s *Store) PurgeDroppedMarkers(ctx context.Context, cutoff time.Time) error {
	_, err := s.w.ExecContext(ctx, `DELETE FROM ingest_dropped WHERE dropped_at < ?`, cutoff.UnixMicro())
	return err
}

// ---------------------------------------------------------------- delete

// DeleteProject hard-deletes every row of a project from every table that has
// a project_id column (including tables added by extensions).
func (s *Store) DeleteProject(ctx context.Context, projectID string) (map[string]int64, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	rows, err := s.w.QueryContext(ctx, `SELECT m.name FROM sqlite_master m WHERE m.type = 'table'
		AND EXISTS (SELECT 1 FROM pragma_table_info(m.name) p WHERE p.name = 'project_id')`)
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		for _, t := range tables {
			if strings.ContainsAny(t, `"`) {
				continue
			}
			r, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM "%s" WHERE project_id = ?`, t), pid)
			if err != nil {
				return fmt.Errorf("deleting from %s: %w", t, err)
			}
			if n, _ := r.RowsAffected(); n > 0 {
				counts[t] = n
			}
		}
		return nil
	})
	s.invalidateIngest(pid)
	return counts, err
}
