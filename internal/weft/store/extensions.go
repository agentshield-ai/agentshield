package store

import "database/sql"

// Feature modules register extra schema and migrations from their own files
// (in init functions) so they don't need to edit store.go.

var (
	schemaExtensions    []string
	migrationExtensions []func(*sql.DB) error
)

// registerSchema adds CREATE TABLE/INDEX IF NOT EXISTS statements run at Open.
func registerSchema(stmts string) { schemaExtensions = append(schemaExtensions, stmts) }

// registerMigration adds a migration run at Open after all schema statements.
// Migrations must be idempotent.
func registerMigration(fn func(*sql.DB) error) { migrationExtensions = append(migrationExtensions, fn) }

func runExtensions(db *sql.DB) error {
	for _, s := range schemaExtensions {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	for _, m := range migrationExtensions {
		if err := m(db); err != nil {
			return err
		}
	}
	return nil
}

// HasColumn reports whether table has column (for idempotent migrations).
func HasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return false, err
		}
		if n == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// DB exposes the write handle for feature modules in other packages that
// need custom queries. Prefer adding methods to Store.
func (s *Store) DB() *sql.DB { return s.w }

// ReadDB exposes the read handle.
func (s *Store) ReadDB() *sql.DB { return s.r }
