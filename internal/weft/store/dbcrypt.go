package store

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"

	_ "github.com/ncruces/go-sqlite3/driver"       // CGO-free SQLite (Wasm translated to Go), used for encrypted databases
	_ "github.com/ncruces/go-sqlite3/vfs/adiantum" // registers the "adiantum" encrypting VFS
	_ "modernc.org/sqlite"                         // pure-Go SQLite driver, used for plaintext databases (the default)
)

// Database encryption at rest (opt-in).
//
// By default the database is a plaintext SQLite file opened with
// modernc.org/sqlite. When an encryption key is configured the database is
// opened instead with github.com/ncruces/go-sqlite3 through its "adiantum"
// VFS, which encrypts every 4 KiB block of the database, its WAL and any
// temporary files with Adiantum (XChaCha12 + AES-256 + NH/Poly1305). The
// SQLite engine, SQL dialect and file format inside the encryption are the
// same, so all queries run unchanged on both drivers.
//
// The passphrase is stretched once per process with Argon2id using exactly
// the parameters of the VFS's own "textkey" (so any go-sqlite3 based tool can
// open the file with textkey=<passphrase>), and the derived 256-bit key is
// passed to each connection as hexkey, avoiding a 64 MiB Argon2id run per
// pooled connection.
//
// Adiantum is length-preserving disk encryption: it hides content, not which
// blocks changed between two snapshots, and it does not authenticate pages
// (tampering garbles data rather than being detected). See docs/weft/README.md.

// DBEncryptionKeyEnv names the environment variable holding the database
// encryption passphrase. Open honours it; cmd/weft also reads it as the
// default for --db-encryption-key.
const DBEncryptionKeyEnv = "WEFT_DB_ENCRYPTION_KEY"

const (
	plainDriver = "sqlite"  // modernc.org/sqlite
	encDriver   = "sqlite3" // github.com/ncruces/go-sqlite3/driver
	encVFS      = "adiantum"

	// adiantumPepper is the Argon2id salt the adiantum VFS uses for textkey.
	adiantumPepper = "github.com/ncruces/go-sqlite3/vfs/adiantum"
)

var sqliteMagic = []byte("SQLite format 3\x00")

// Errors returned by Open when the key does not match the file. They are
// reported before anything is written to the database.
var (
	ErrDBEncrypted    = errors.New("database is encrypted (or is not an SQLite file): set --db-encryption-key, --db-encryption-key-file or " + DBEncryptionKeyEnv)
	ErrDBNotEncrypted = errors.New("database is a plaintext SQLite file but a database encryption key is set: convert it with `weft db encrypt`, or unset the key")
	ErrDBWrongKey     = errors.New("cannot decrypt database: wrong database encryption key, or the file is not a Weft database")
)

// OpenOptions configure OpenWith.
type OpenOptions struct {
	// EncryptionKey, when non-empty, opens (or creates) the database
	// encrypted at rest with this passphrase. Empty opens a plaintext
	// database with the default driver.
	EncryptionKey string
}

// OpenOptionsFromEnv returns the options Open uses: the encryption key from
// WEFT_DB_ENCRYPTION_KEY.
func OpenOptionsFromEnv() OpenOptions {
	return OpenOptions{EncryptionKey: os.Getenv(DBEncryptionKeyEnv)}
}

var derivedKeys sync.Map // sha256(passphrase) -> []byte

// deriveDBKey stretches a passphrase into the 256-bit Adiantum key, exactly
// as the VFS's textkey parameter does. Results are cached per process.
func deriveDBKey(passphrase string) []byte {
	id := sha256.Sum256([]byte(passphrase))
	if k, ok := derivedKeys.Load(id); ok {
		return k.([]byte)
	}
	k := argon2.IDKey([]byte(passphrase), []byte(adiantumPepper), 3, 64*1024, 4, 32)
	derivedKeys.Store(id, k)
	return k
}

// uriPath escapes the characters that would end the path part of a file: URI.
func uriPath(p string) string {
	return strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(p)
}

// uriValue escapes a file: URI query parameter value (SQLite decodes %HH).
func uriValue(v string) string {
	return strings.NewReplacer("%", "%25", "&", "%26", "#", "%23", "+", "%2b").Replace(v)
}

const commonPragmas = "_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(off)&_pragma=synchronous(NORMAL)"

// dbDSN returns the driver name and DSN for the write handle; the read handle
// appends "&_pragma=query_only(true)". Both drivers accept the same _pragma
// parameters. For encrypted databases the key comes first, as the VFS needs it
// before SQLite reads the header, and temporary files stay in memory.
func dbDSN(path string, key []byte) (driver, dsn string) {
	if key == nil {
		return plainDriver, "file:" + path + "?" + commonPragmas
	}
	// modeof gives the WAL the database file's permissions, as SQLite's own
	// unix VFS (and so modernc) does; go-sqlite3 otherwise creates it 0666 &^ umask.
	return encDriver, "file:" + uriPath(path) + "?vfs=" + encVFS + "&hexkey=" + hex.EncodeToString(key) +
		"&modeof=" + uriValue(path) + "&" + commonPragmas + "&_pragma=temp_store(memory)"
}

// prepareSharedMemory creates the -shm file 0600 when it is missing: the
// go-sqlite3 VFS does not copy the database's mode to it. It holds only the
// WAL index (frame numbers and checksums), never page contents.
func prepareSharedMemory(path string) (created bool, err error) {
	f, err := os.OpenFile(path+"-shm", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, f.Close()
}

// DetectDBEncryption reports whether the file at path looks encrypted: it
// exists, is non-empty and does not start with the SQLite header. A missing
// or empty file is a new database and reports false.
func DetectDBEncryption(path string) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	hdr := make([]byte, len(sqliteMagic))
	n, err := io.ReadFull(f, hdr)
	if n == 0 {
		return false, nil
	}
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, err
	}
	return !bytes.Equal(hdr[:n], sqliteMagic), nil
}

// checkDBFile refuses to open a plaintext file with a key or an encrypted
// file without one, so a mismatch is a clear error rather than a driver's
// "file is not a database".
func checkDBFile(path string, encrypted bool) error {
	enc, err := DetectDBEncryption(path)
	if err != nil {
		return fmt.Errorf("reading database header: %w", err)
	}
	switch {
	case encrypted && !enc && fileSize(path) > 0:
		return fmt.Errorf("%s: %w", path, ErrDBNotEncrypted)
	case !encrypted && enc:
		return fmt.Errorf("%s: %w", path, ErrDBEncrypted)
	}
	return nil
}

func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func isNotADB(err error) bool {
	return err != nil && strings.Contains(err.Error(), "file is not a database")
}

// openDB opens one handle and verifies it can read the schema, mapping a
// failed decryption to ErrDBWrongKey.
func openDB(path string, key []byte, readOnly bool) (_ *sql.DB, retErr error) {
	if key != nil && !readOnly {
		created, err := prepareSharedMemory(path)
		if err != nil {
			return nil, fmt.Errorf("creating shared-memory file: %w", err)
		}
		if created {
			defer func() {
				if retErr != nil {
					_ = os.Remove(path + "-shm")
				}
			}()
		}
	}
	driver, dsn := dbDSN(path, key)
	if readOnly {
		dsn += "&_pragma=query_only(true)"
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master`).Scan(&n); err != nil {
		_ = db.Close()
		if key != nil && isNotADB(err) {
			return nil, fmt.Errorf("%s: %w", path, ErrDBWrongKey)
		}
		return nil, fmt.Errorf("opening database: %w", err)
	}
	return db, nil
}

// ConvertDB copies the database at src into a new database at dst, changing
// its encryption: srcKey and dstKey are passphrases, empty meaning plaintext.
// dst must not exist (or be empty). The copy is a consistent snapshot made
// with VACUUM INTO, written 0600, synced and checked with quick_check and
// per-table row counts before ConvertDB returns. Stop the server first:
// writes made after the snapshot are not copied.
func ConvertDB(src, dst, srcKey, dstKey string) (err error) {
	if fileSize(src) == 0 {
		return fmt.Errorf("%s: no database to convert", src)
	}
	if err := checkDBFile(src, srcKey != ""); err != nil {
		return err
	}
	if fileSize(dst) > 0 {
		return fmt.Errorf("%s already exists; refusing to overwrite it", dst)
	}
	for _, sfx := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(dst + sfx); err == nil {
			return fmt.Errorf("%s exists; remove it first", dst+sfx)
		}
	}
	// Both sides go through go-sqlite3 so one connection can read one VFS and
	// write the other; its default VFS reads and writes plain SQLite files.
	var sk, dk []byte
	srcDSN := "file:" + uriPath(src) + "?_pragma=busy_timeout(10000)"
	if srcKey != "" {
		sk = deriveDBKey(srcKey)
		srcDSN = "file:" + uriPath(src) + "?vfs=" + encVFS + "&hexkey=" + hex.EncodeToString(sk) + "&_pragma=busy_timeout(10000)"
	}
	// The destination names its VFS: without one it would inherit the
	// connection's, and a plaintext copy would go through the encrypting VFS.
	dstURI := "file:" + uriPath(dst) + "?vfs=os"
	if dstKey != "" {
		dk = deriveDBKey(dstKey)
		dstURI = "file:" + uriPath(dst) + "?vfs=" + encVFS + "&hexkey=" + hex.EncodeToString(dk)
	}
	db, err := sql.Open(encDriver, srcDSN+"&_pragma=temp_store(memory)")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	want, err := tableCounts(db)
	if err != nil {
		if srcKey != "" && isNotADB(err) {
			return fmt.Errorf("%s: %w", src, ErrDBWrongKey)
		}
		return fmt.Errorf("reading %s: %w", src, err)
	}
	if err := PrepareDBFile(dst); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			for _, sfx := range []string{"", "-wal", "-shm", "-journal"} {
				_ = os.Remove(dst + sfx)
			}
		}
	}()
	if _, err := db.Exec(`VACUUM INTO ?`, dstURI); err != nil {
		return fmt.Errorf("copying database: %w", err)
	}
	if err := db.Close(); err != nil {
		return err
	}
	if f, err := os.OpenFile(dst, os.O_RDWR, 0); err == nil {
		err = f.Sync()
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	// Verify through the driver Open will use for dst.
	out, err := openDB(dst, dk, true)
	if err != nil {
		return fmt.Errorf("verifying %s: %w", dst, err)
	}
	defer out.Close()
	var res string
	if err := out.QueryRow(`PRAGMA quick_check`).Scan(&res); err != nil || res != "ok" {
		return fmt.Errorf("verifying %s: quick_check: %q %v", dst, res, err)
	}
	got, err := tableCounts(out)
	if err != nil {
		return fmt.Errorf("verifying %s: %w", dst, err)
	}
	for t, n := range want {
		if got[t] != n {
			return fmt.Errorf("verifying %s: table %s has %d rows, want %d", dst, t, got[t], n)
		}
	}
	if enc, _ := DetectDBEncryption(dst); enc != (dstKey != "") {
		return fmt.Errorf("verifying %s: unexpected file header", dst)
	}
	return nil
}

func tableCounts(db *sql.DB) (map[string]int64, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(names))
	for _, n := range names {
		var c int64
		if err := db.QueryRow(`SELECT count(*) FROM "` + strings.ReplaceAll(n, `"`, `""`) + `"`).Scan(&c); err != nil {
			return nil, err
		}
		out[n] = c
	}
	return out, nil
}
