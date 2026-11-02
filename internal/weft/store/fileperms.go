package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// Database encryption at rest: the pure-Go SQLite driver (modernc.org/sqlite)
// has no page-level encryption (no SQLCipher or SEE without CGO), so Weft
// does not encrypt the database file itself. Provider secrets inside it are
// encrypted with AES-256-GCM (secrets.go). Protect the file with an
// encrypted volume or filesystem and restrictive permissions; see
// docs/weft/README.md "Security notes".

// PrepareDBFile creates a new database file readable only by its owner, so
// SQLite's -wal and -shm files (which copy the database's mode) are private
// too. Existing files are left alone.
func PrepareDBFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return f.Close()
}

// DBPermissionWarnings lists database, WAL, shared-memory and key files (and
// the directory) that other local users can read.
func DBPermissionWarnings(path string) []string {
	var out []string
	for _, p := range []string{path, path + "-wal", path + "-shm", SecretKeyFile(path)} {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if fi.Mode().Perm()&0o004 != 0 {
			out = append(out, fmt.Sprintf("%s is world-readable (mode %#o); run: chmod 600 %s", p, fi.Mode().Perm(), p))
		}
	}
	dir := filepath.Dir(path)
	if fi, err := os.Stat(dir); err == nil && fi.Mode().Perm()&0o005 == 0o005 && len(out) > 0 {
		out = append(out, fmt.Sprintf("directory %s is world-accessible (mode %#o); consider chmod 700", dir, fi.Mode().Perm()))
	}
	return out
}
