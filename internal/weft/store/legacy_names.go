package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/compat/legacy"
)

// Databases written before Weft had its own names (package compat/legacy)
// are migrated once at Open:
//
//   - columns get their current names: the feedback table's ref column, and
//     the user and run columns of calls, feedback, objects and registry
//     versions (user_id, run_id, run_step, run_step_end);
//   - feedback types get their current names (weft.reaction, weft.note,
//     weft.scorer.<name>, weft.annotation.<name>, weft.signal,
//     weft.user_feedback);
//   - legacy attribute keys of stored calls (eval links, parent-call links,
//     span kinds, turn markers) are renamed to their weft.* equivalents.
//
// Saved queries (saved views, dashboards, monitors, leaderboards and
// automations) are content-addressed objects, so they are not edited in
// place: the latest version of one that uses old names gets a new version
// with Weft's names, and the aliases of the old version move to it; the old
// version is kept (migrateLegacyObjects). After that nothing outside the
// compatibility adapter reads the old names.

const legacyNamesMigration = "legacy-names-1"

func migrateLegacyNames(db *sql.DB) error {
	for _, c := range legacy.ColumnRenames() {
		hasOld, err := HasColumn(db, c.Table, c.Old)
		if err != nil {
			return err
		}
		if !hasOld {
			continue
		}
		hasNew, err := HasColumn(db, c.Table, c.New)
		if err != nil {
			return err
		}
		if hasNew { // a column added by a later schema statement, still empty
			if _, err := db.Exec(`UPDATE "` + c.Table + `" SET "` + c.New + `" = "` + c.Old + `" WHERE "` + c.New + `" IS NULL`); err != nil {
				return fmt.Errorf("copying %s.%s: %w", c.Table, c.Old, err)
			}
			if _, err := db.Exec(`ALTER TABLE "` + c.Table + `" DROP COLUMN "` + c.Old + `"`); err != nil {
				return fmt.Errorf("dropping %s.%s: %w", c.Table, c.Old, err)
			}
			continue
		}
		if _, err := db.Exec(`ALTER TABLE "` + c.Table + `" RENAME COLUMN "` + c.Old + `" TO "` + c.New + `"`); err != nil {
			return fmt.Errorf("renaming %s.%s: %w", c.Table, c.Old, err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS weft_migrations (name TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	var done int
	if err := db.QueryRow(`SELECT COUNT(*) FROM weft_migrations WHERE name = ?`, legacyNamesMigration).Scan(&done); err != nil {
		return err
	}
	if done > 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, rn := range legacy.FeedbackTypeRenames() {
		if rn.Prefix {
			_, err = tx.Exec(`UPDATE feedback SET feedback_type = ? || substr(feedback_type, ?) WHERE substr(feedback_type, 1, ?) = ?`,
				rn.New, len(rn.Old)+1, len(rn.Old), rn.Old)
		} else {
			_, err = tx.Exec(`UPDATE feedback SET feedback_type = ? WHERE feedback_type = ?`, rn.New, rn.Old)
		}
		if err != nil {
			return fmt.Errorf("renaming feedback types: %w", err)
		}
	}
	if err := migrateLegacyAttributes(tx); err != nil {
		return fmt.Errorf("renaming call attributes: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO weft_migrations(name, applied_at) VALUES(?, ?)`, legacyNamesMigration, time.Now().UnixMicro()); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateLegacyAttributes renames legacy attribute keys of stored calls.
func migrateLegacyAttributes(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT project_id, id, attributes FROM calls WHERE ` + legacy.AttributeFilterSQL("attributes"))
	if err != nil {
		return err
	}
	type upd struct{ pid, id, attrs string }
	var updates []upd
	for rows.Next() {
		var pid, id string
		var raw sql.NullString
		if err := rows.Scan(&pid, &id, &raw); err != nil {
			rows.Close()
			return err
		}
		attrs := decodeMap(raw.String)
		if !legacy.RewriteAttributes(attrs) {
			continue
		}
		enc, err := encodeJSON(attrs)
		if err != nil {
			continue
		}
		updates = append(updates, upd{pid, id, enc})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, u := range updates {
		if _, err := tx.Exec(`UPDATE calls SET attributes = ? WHERE project_id = ? AND id = ?`, u.attrs, u.pid, u.id); err != nil {
			return err
		}
	}
	return nil
}

func init() { registerMigration(migrateLegacyObjects) }

// savedQueryClasses are the object classes whose values hold call queries,
// field names or feedback types.
var savedQueryClasses = []string{"SavedView", "Dashboard", "Monitor", "Leaderboard", "Automation"}

// migrateLegacyObjects publishes, once, a new version of every saved query
// whose latest version uses legacy names (see the comment at the top).
func migrateLegacyObjects(db *sql.DB) error {
	return runOnce(db, "legacy-objects-1", func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT o.project_id, o.object_id, o.digest, o.kind, o.base_object_class, o.leaf_object_class, o.val, o.user_id
			FROM objects o WHERE o.deleted_at IS NULL AND o.base_object_class IN (?, ?, ?, ?, ?)
				AND o.version_index = (SELECT MAX(o2.version_index) FROM objects o2
					WHERE o2.project_id = o.project_id AND o2.object_id = o.object_id AND o2.deleted_at IS NULL)`,
			savedQueryClasses[0], savedQueryClasses[1], savedQueryClasses[2], savedQueryClasses[3], savedQueryClasses[4])
		if err != nil {
			return err
		}
		type ver struct {
			pid, oid, digest, kind, val string
			base, leaf, user            sql.NullString
		}
		var vs []ver
		for rows.Next() {
			var v ver
			if err := rows.Scan(&v.pid, &v.oid, &v.digest, &v.kind, &v.base, &v.leaf, &v.val, &v.user); err != nil {
				rows.Close()
				return err
			}
			vs = append(vs, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rw := legacy.JSONRewrite{Key: legacy.SavedQueryName, Value: legacy.SavedQueryName}
		for _, v := range vs {
			raw := rw.Apply([]byte(v.val))
			if string(raw) == v.val {
				continue
			}
			_, digest, canon, err := prepValue(nil, raw)
			if err != nil {
				continue // not valid JSON after all; leave it
			}
			var exists int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM objects WHERE project_id = ? AND object_id = ? AND digest = ?`,
				v.pid, v.oid, digest).Scan(&exists); err != nil {
				return err
			}
			if exists > 0 {
				continue // an earlier version already has the new names
			}
			var next int
			if err := tx.QueryRow(`SELECT COALESCE(MAX(version_index) + 1, 0) FROM objects WHERE project_id = ? AND object_id = ?`,
				v.pid, v.oid).Scan(&next); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO objects(project_id, object_id, digest, version_index, kind, base_object_class, leaf_object_class,
					val, created_at, user_id, size_bytes) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
				v.pid, v.oid, digest, next, v.kind, v.base, v.leaf, canon, nowMicros(), v.user, len(canon)); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE object_aliases SET digest = ? WHERE project_id = ? AND object_id = ? AND digest = ?`,
				digest, v.pid, v.oid, v.digest); err != nil {
				return err
			}
		}
		return nil
	})
}
