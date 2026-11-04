package store

import (
	"database/sql"
	"fmt"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// Refs to functions and spans had other kind names before Weft's own API
// (docs/weft/DESIGN.md §10.2): weft:///e/p/op/<name>:<digest> and
// weft:///e/p/call/<id>. Weft now writes "function" and "span" and reads
// both (refs.Parse). Columns that hold refs are rewritten once here, so
// joins and filters on them see one spelling; content-addressed values
// (object values, table rows) and span inputs, outputs and attributes are
// user data and are never rewritten, and filters on the refs inside them
// match both spellings (refs.Variants).

func init() { registerMigration(migrateRefKinds) }

// refColumns are the columns rewritten by the migration.
var refColumns = []struct{ table, key, col string }{
	{"feedback", "id", "ref"},
	{"feedback", "id", "call_ref"},
	{"feedback", "id", "runnable_ref"},
	{"feedback", "id", "annotation_ref"},
	{"feedback", "id", "trigger_ref"},
	{"calls", "rowid", "op_name"},
	{"registry_versions", "rowid", "ref"},
	{"annotation_queue_items", "id", "call_op_name"},
}

// refColumnsAdded were added to refColumns after ref-kinds-1 ran on
// existing databases; ref-kinds-2 rewrites them there (on a new database
// ref-kinds-1 already did, and ref-kinds-2 finds nothing to change).
var refColumnsAdded = refColumns[len(refColumns)-1:]

func migrateRefKinds(db *sql.DB) error {
	if err := runOnce(db, "ref-kinds-1", func(tx *sql.Tx) error { return rewriteRefColumns(tx, refColumns) }); err != nil {
		return err
	}
	return runOnce(db, "ref-kinds-2", func(tx *sql.Tx) error { return rewriteRefColumns(tx, refColumnsAdded) })
}

func rewriteRefColumns(tx *sql.Tx, cols []struct{ table, key, col string }) error {
	for _, c := range cols {
		if ok, err := hasColumnTx(tx, c.table, c.col); err != nil || !ok {
			if err != nil {
				return err
			}
			continue
		}
		if err := rewriteRefColumn(tx, c.table, c.key, c.col); err != nil {
			return fmt.Errorf("%s.%s: %w", c.table, c.col, err)
		}
	}
	return nil
}

func hasColumnTx(tx *sql.Tx, table, column string) (bool, error) {
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&n)
	return n > 0, err
}

func rewriteRefColumn(tx *sql.Tx, table, key, col string) error {
	rows, err := tx.Query(`SELECT ` + key + `, "` + col + `" FROM "` + table + `" WHERE "` + col + `" LIKE 'weft:///%/op/%' OR "` +
		col + `" LIKE 'weft:///%/call/%'`)
	if err != nil {
		return err
	}
	type upd struct {
		key any
		val string
	}
	var updates []upd
	for rows.Next() {
		var k any
		var v string
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			return err
		}
		if n := refs.Normalize(v); n != v {
			updates = append(updates, upd{k, n})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}
	stmt, err := tx.Prepare(`UPDATE "` + table + `" SET "` + col + `" = ? WHERE ` + key + ` = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, u := range updates {
		if _, err := stmt.Exec(u.val, u.key); err != nil {
			return err
		}
	}
	return nil
}
