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
//   - the feedback table's ref column gets its current name;
//   - feedback types get their current names (weft.reaction, weft.note,
//     weft.scorer.<name>, weft.annotation.<name>, weft.signal,
//     weft.user_feedback);
//   - legacy attribute keys of stored calls (eval links, parent-call links,
//     span kinds, turn markers) are renamed to their weft.* equivalents.
//
// Content-addressed objects (saved views, dashboards, monitors, automations)
// are not rewritten, because that would change their digests; the query
// compiler and the automation runner read the old names in them instead.

const legacyNamesMigration = "legacy-names-1"

func migrateLegacyNames(db *sql.DB) error {
	hasOld, err := HasColumn(db, "feedback", legacy.FeedbackRefColumn)
	if err != nil {
		return err
	}
	if hasOld {
		if _, err := db.Exec(`ALTER TABLE feedback RENAME COLUMN ` + legacy.FeedbackRefColumn + ` TO ref`); err != nil {
			return fmt.Errorf("renaming feedback ref column: %w", err)
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
