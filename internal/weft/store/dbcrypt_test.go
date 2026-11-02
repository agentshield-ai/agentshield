package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The whole suite runs on the encrypted driver when WEFT_DB_ENCRYPTION_KEY is
// set (scripts/weft_test_encrypted.sh, `make weft-test-encrypted`); the tests
// below pin one mode explicitly so they mean the same under either run.

const secretMarker = "PLAINTEXT-MARKER-4c1f"

func openMode(t *testing.T, path, key string) *Store {
	t.Helper()
	s, err := OpenWith(path, OpenOptions{EncryptionKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func seedCalls(t *testing.T, s *Store, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		st := start(fmt.Sprintf("c%03d", i), "", "chat")
		st.Inputs = map[string]any{"prompt": secretMarker, "n": i, "messages": []any{map[string]any{"role": "user", "content": "hello " + secretMarker}}}
		if err := s.StartCall(ctx, st); err != nil {
			t.Fatal(err)
		}
		if err := s.EndCall(ctx, end(fmt.Sprintf("c%03d", i), usage("gpt-4o", 10, 5), nil)); err != nil {
			t.Fatal(err)
		}
	}
}

func countCalls(t *testing.T, s *Store) int {
	t.Helper()
	calls, err := s.ListCalls(context.Background(), CallsQuery{ProjectID: proj})
	if err != nil {
		t.Fatal(err)
	}
	return len(calls)
}

func fileDigest(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

// assertUnreadable checks the database and any WAL and shared-memory files
// hold no SQLite header and no plaintext JSON.
func assertUnreadable(t *testing.T, path string) {
	t.Helper()
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		b, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range []string{"SQLite format 3", secretMarker, `"prompt"`, "CREATE TABLE", "gpt-4o"} {
			if bytes.Contains(b, []byte(needle)) {
				t.Errorf("%s contains %q in plaintext", filepath.Base(p), needle)
			}
		}
	}
}

func TestEncryptedDatabaseUnreadableOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "weft.db")
	if err := PrepareDBFile(path); err != nil { // as cmd/weft does: an empty file first
		t.Fatal(err)
	}
	s := openMode(t, path, "correct horse")
	if !s.Encrypted() {
		t.Fatal("Encrypted() = false")
	}
	seedCalls(t, s, 50)
	assertUnreadable(t, path) // while open: database, WAL and shared memory
	for _, sfx := range []string{"", "-wal", "-shm"} {
		fi, err := os.Stat(path + sfx)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %#o, want 0600", filepath.Base(path+sfx), fi.Mode().Perm())
		}
	}
	if got := countCalls(t, s); got != 50 {
		t.Fatalf("calls = %d", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	assertUnreadable(t, path)
	if enc, err := DetectDBEncryption(path); err != nil || !enc {
		t.Fatalf("DetectDBEncryption = %v, %v", enc, err)
	}

	// Reopen with the right key.
	s = openMode(t, path, "correct horse")
	if got := countCalls(t, s); got != 50 {
		t.Fatalf("calls after reopen = %d", got)
	}
	_ = s.Close()

	// The key is the VFS's textkey derivation, so go-sqlite3 tools can open it.
	db, err := sql.Open(encDriver, "file:"+path+"?vfs=adiantum&textkey=correct%20horse")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM calls`).Scan(&n); err != nil || n != 50 {
		t.Fatalf("textkey open: n=%d err=%v", n, err)
	}
}

func TestPlaintextDatabaseIsReadable(t *testing.T) {
	// Control for the test above: the same data in a plaintext database is
	// visible, so the needles are meaningful.
	path := filepath.Join(t.TempDir(), "weft.db")
	s := openMode(t, path, "")
	if s.Encrypted() {
		t.Fatal("Encrypted() = true")
	}
	seedCalls(t, s, 5)
	_ = s.Close()
	b, _ := os.ReadFile(path)
	if !bytes.HasPrefix(b, sqliteMagic) || !bytes.Contains(b, []byte(secretMarker)) {
		t.Fatal("plaintext database should show its header and content")
	}
}

func TestKeyMismatchFailsWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	enc := filepath.Join(dir, "enc.db")
	plain := filepath.Join(dir, "plain.db")
	s := openMode(t, enc, "right")
	seedCalls(t, s, 3)
	_ = s.Close()
	s = openMode(t, plain, "")
	seedCalls(t, s, 3)
	_ = s.Close()

	cases := []struct {
		name, path, key string
		want            error
	}{
		{"wrong key", enc, "wrong", ErrDBWrongKey},
		{"missing key", enc, "", ErrDBEncrypted},
		{"key on plaintext", plain, "right", ErrDBNotEncrypted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := fileDigest(t, c.path)
			s, err := OpenWith(c.path, OpenOptions{EncryptionKey: c.key})
			if err == nil {
				_ = s.Close()
				t.Fatal("open succeeded")
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if fileDigest(t, c.path) != before {
				t.Fatal("database file changed after a failed open")
			}
			for _, sfx := range []string{"-wal", "-journal"} {
				if fi, err := os.Stat(c.path + sfx); err == nil && fi.Size() > 0 {
					t.Fatalf("%s written after a failed open", sfx)
				}
			}
		})
	}
	// The wrong-key check also holds below the header pre-check (e.g. a
	// driver that opens the file directly).
	if _, err := openDB(enc, deriveDBKey("wrong"), false); !errors.Is(err, ErrDBWrongKey) {
		t.Fatalf("openDB wrong key: %v", err)
	}
	// And the right key still works afterwards.
	s = openMode(t, enc, "right")
	if got := countCalls(t, s); got != 3 {
		t.Fatalf("calls = %d", got)
	}
	_ = s.Close()
}

func TestConvertDBRoundTrip(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.db")
	enc := filepath.Join(dir, "enc.db")
	back := filepath.Join(dir, "back.db")

	s := openMode(t, plain, "")
	seedCalls(t, s, 20)
	if err := s.SetSecret(context.Background(), "OPENAI_API_KEY", "sk-test"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	if err := ConvertDB(plain, enc, "", "k1"); err != nil {
		t.Fatal(err)
	}
	assertUnreadable(t, enc)
	if fi, _ := os.Stat(enc); fi.Mode().Perm() != 0o600 {
		t.Errorf("encrypted copy mode = %#o", fi.Mode().Perm())
	}
	// The secret key file is per database path; give the copy the original's.
	copyFile(t, SecretKeyFile(plain), SecretKeyFile(enc))
	s = openMode(t, enc, "k1")
	if got := countCalls(t, s); got != 20 {
		t.Fatalf("encrypted calls = %d", got)
	}
	if v, ok, err := s.GetSecret(context.Background(), "OPENAI_API_KEY"); err != nil || !ok || v != "sk-test" {
		t.Fatalf("secret after encrypt = %q %v %v", v, ok, err)
	}
	seedCalls(t, s, 25) // upserts 20, adds 5
	_ = s.Close()

	if err := ConvertDB(enc, back, "", ""); !errors.Is(err, ErrDBEncrypted) {
		t.Fatalf("decrypt without key: %v", err)
	}
	if err := ConvertDB(enc, back, "nope", ""); !errors.Is(err, ErrDBWrongKey) {
		t.Fatalf("decrypt with wrong key: %v", err)
	}
	if _, err := os.Stat(back); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed conversion left an output file")
	}
	if err := ConvertDB(enc, back, "k1", ""); err != nil {
		t.Fatal(err)
	}
	if enc, _ := DetectDBEncryption(back); enc {
		t.Fatal("decrypted copy is not plaintext")
	}
	copyFile(t, SecretKeyFile(plain), SecretKeyFile(back))
	s = openMode(t, back, "")
	if got := countCalls(t, s); got != 25 {
		t.Fatalf("decrypted calls = %d", got)
	}
	_ = s.Close()

	// Refusals: existing output, plaintext source given a key, missing source.
	if err := ConvertDB(plain, back, "", "k2"); err == nil {
		t.Fatal("overwrote an existing database")
	}
	if err := ConvertDB(plain, filepath.Join(dir, "x.db"), "k1", ""); !errors.Is(err, ErrDBNotEncrypted) {
		t.Fatalf("decrypt of plaintext: %v", err)
	}
	if err := ConvertDB(filepath.Join(dir, "missing.db"), filepath.Join(dir, "y.db"), "", "k"); err == nil {
		t.Fatal("converted a missing database")
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSQLFeaturesBothDrivers pins the SQLite features Weft's queries rely on
// to both drivers, whichever one the rest of the suite runs on.
func TestSQLFeaturesBothDrivers(t *testing.T) {
	for _, key := range []string{"", "k"} {
		name := map[string]string{"": "modernc", "k": "encrypted"}[key]
		t.Run(name, func(t *testing.T) {
			s := openMode(t, filepath.Join(t.TempDir(), "weft.db"), key)
			defer s.Close()
			db := s.DB()
			var jm string
			if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&jm); err != nil || jm != "wal" {
				t.Fatalf("journal_mode = %q %v", jm, err)
			}
			var bt int
			if err := s.ReadDB().QueryRow(`PRAGMA busy_timeout`).Scan(&bt); err != nil || bt != 10000 {
				t.Fatalf("busy_timeout = %d %v", bt, err)
			}
			if _, err := s.ReadDB().Exec(`CREATE TABLE ro_probe(x)`); err == nil {
				t.Fatal("read handle accepted a write")
			}
			if _, err := db.Exec(`CREATE TABLE j(id INTEGER, doc TEXT)`); err != nil {
				t.Fatal(err)
			}
			// ?NNN parameters, bound out of order and reused.
			if _, err := db.Exec(`INSERT INTO j VALUES(?2, json_set(?1, '$.id', ?2))`, `{"a":{"b":[1,2,3]},"s":"x"}`, 7); err != nil {
				t.Fatal(err)
			}
			var b1 int
			var s1 string
			if err := db.QueryRow(`SELECT json_extract(doc, '$.a.b[1]'), json_extract(doc, '$.s') FROM j WHERE id = ?1 AND json_extract(doc, '$.id') = ?1`, 7).Scan(&b1, &s1); err != nil || b1 != 2 || s1 != "x" {
				t.Fatalf("json_extract = %d %q %v", b1, s1, err)
			}
			var sum int
			if err := db.QueryRow(`SELECT sum(e.value) FROM j, json_each(j.doc, '$.a.b') AS e`).Scan(&sum); err != nil || sum != 6 {
				t.Fatalf("json_each sum = %d %v", sum, err)
			}
			var typ any
			if err := db.QueryRow(`SELECT doc FROM j`).Scan(&typ); err != nil {
				t.Fatal(err)
			}
			if _, ok := typ.(string); !ok {
				t.Fatalf("TEXT scanned into any as %T, want string", typ)
			}
			for _, col := range []string{"cumulative", "input_refs"} {
				if ok, err := HasColumn(db, "calls", col); err != nil || !ok {
					t.Fatalf("HasColumn(calls, %s) = %v %v", col, ok, err)
				}
			}
			_, err := db.Exec(`INSERT INTO secrets(name, value, updated_at) VALUES('dup','v',0), ('dup','v',0)`)
			if err == nil || !isUniqueErr(err) {
				t.Fatalf("unique violation not recognised: %v", err)
			}
		})
	}
}

// TestOpenHonoursEnvKey proves which driver the rest of the suite ran on.
func TestOpenHonoursEnvKey(t *testing.T) {
	want := os.Getenv(DBEncryptionKeyEnv) != ""
	s := newTestStore(t)
	if s.Encrypted() != want {
		t.Fatalf("Encrypted() = %v with %s set = %v", s.Encrypted(), DBEncryptionKeyEnv, want)
	}
	t.Logf("suite database encrypted at rest: %v", want)
}

func TestOddPathsEncrypted(t *testing.T) {
	// The encrypted DSN escapes the path and its parameters. (The plaintext
	// DSN is unchanged from before encryption existed and does not.)
	dir := filepath.Join(t.TempDir(), "a dir & 50% #1+x=y")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "we?ft.db")
	if err := PrepareDBFile(path); err != nil {
		t.Fatal(err)
	}
	s := openMode(t, path, "k")
	seedCalls(t, s, 2)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if enc, _ := DetectDBEncryption(path); !enc {
		t.Fatal("database written somewhere else")
	}
	s = openMode(t, path, "k")
	if got := countCalls(t, s); got != 2 {
		t.Fatalf("calls = %d", got)
	}
	_ = s.Close()
	plainDir := filepath.Join(t.TempDir(), "plain dir")
	_ = os.MkdirAll(plainDir, 0o700)
	if err := ConvertDB(path, filepath.Join(plainDir, "weft.db"), "k", ""); err != nil {
		t.Fatal(err)
	}
}
