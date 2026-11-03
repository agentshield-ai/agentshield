package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Optional call fields:
//
//   - run_step / run_step_end: the step of the linked run (run_id)
//     when the call started and ended, accepted on /call/start, /call/end and
//     /call/upsert_batch, stored in their own columns.
//   - expire_at: started_at plus the project's retention_days
//     (/project/ttl_settings), null when the project keeps calls forever. It
//     is derived when read, so changing the TTL moves it for existing calls,
//     which matches the purge (calls whose started_at is older than the
//     retention are deleted).
//   - total_storage_size_bytes (include_total_storage_size): the summed
//     storage_size_bytes of every call in the trace, on trace roots only.
//   - username (include_usernames): the Weft user id (handle) user_id
//     names. A user_id with no user row is returned as is when it is a
//     valid handle (static-key "user=key" names), otherwise null.

func init() {
	registerMigration(func(db *sql.DB) error {
		for _, col := range []string{"run_step", "run_step_end"} {
			ok, err := HasColumn(db, "calls", col)
			if err != nil {
				return err
			}
			if !ok {
				if _, err := db.Exec(`ALTER TABLE calls ADD COLUMN ` + col + ` INTEGER`); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func int64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	x := v.Int64
	return &x
}

// setRunStep stores a run step column when the client sent one.
func setRunStep(ctx context.Context, tx *sql.Tx, projectID, id, col string, v *int64) error {
	if v == nil {
		return nil
	}
	if col != "run_step" && col != "run_step_end" {
		return Invalidf("bad run step column")
	}
	_, err := tx.ExecContext(ctx, `UPDATE calls SET `+col+` = ? WHERE project_id = ? AND id = ?`, *v, projectID, id)
	return err
}

// enrichCallFields fills expire_at, total_storage_size_bytes and username.
func (s *Store) enrichCallFields(ctx context.Context, projectID string, calls []*Call, q CallsQuery) error {
	if len(calls) == 0 {
		return nil
	}
	var ttl TTLSettings
	if ok, err := s.GetProjectSetting(ctx, projectID, SettingTTL, &ttl); err != nil {
		return err
	} else if ok && ttl.RetentionDays != nil && *ttl.RetentionDays >= 1 {
		d := time.Duration(*ttl.RetentionDays) * 24 * time.Hour
		for _, c := range calls {
			e := FormatMicros(c.startedUS + d.Microseconds())
			c.ExpireAt = &e
		}
	}
	if q.IncludeTotalStorageSize {
		var traces []string
		seen := map[string]bool{}
		for _, c := range calls {
			if c.ParentID == nil && !seen[c.TraceID] {
				seen[c.TraceID] = true
				traces = append(traces, c.TraceID)
			}
		}
		sizes := map[string]int64{}
		for len(traces) > 0 {
			n := min(len(traces), 500)
			cond, args := inList("trace_id", traces[:n])
			traces = traces[n:]
			rows, err := s.r.QueryContext(ctx, `SELECT trace_id, COALESCE(SUM(storage_size), 0) FROM calls
				WHERE project_id = ? AND `+cond+` GROUP BY trace_id`, append([]any{projectID}, args...)...)
			if err != nil {
				return err
			}
			for rows.Next() {
				var t string
				var sz int64
				if err := rows.Scan(&t, &sz); err != nil {
					rows.Close()
					return err
				}
				sizes[t] = sz
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		for _, c := range calls {
			if c.ParentID == nil {
				if sz, ok := sizes[c.TraceID]; ok {
					v := sz
					c.TotalStorageSizeBytes = &v
				}
			}
		}
	}
	if q.IncludeUsernames {
		names := map[string]*string{}
		for _, c := range calls {
			if c.UserID == nil {
				continue
			}
			id := *c.UserID
			name, ok := names[id]
			if !ok {
				name = s.resolveUsername(ctx, id)
				names[id] = name
			}
			c.Username = name
		}
	}
	return nil
}

func (s *Store) resolveUsername(ctx context.Context, id string) *string {
	if u, err := s.GetUser(ctx, id); err == nil && u != nil {
		v := u.ID
		return &v
	}
	if UserIDRe.MatchString(id) && !strings.Contains(id, "/") {
		v := id
		return &v
	}
	return nil
}

// GetCallWith reads one call with the include_* options of q (project id,
// costs, feedback, storage sizes, usernames).
func (s *Store) GetCallWith(ctx context.Context, q CallsQuery, id string) (*Call, error) {
	one := 1
	q.Filter = &CallsFilter{CallIDs: []string{id}}
	q.Limit, q.Offset, q.IncludeDeleted = &one, 0, true
	q.Query, q.Search, q.SortBy = nil, "", nil
	calls, err := s.ListCalls(ctx, q)
	if err != nil {
		return nil, err
	}
	if len(calls) == 0 {
		return nil, ErrNotFound
	}
	return calls[0], nil
}
