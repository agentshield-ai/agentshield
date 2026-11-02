package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

func runDBT(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runDB(args, &out, &errb)
	return code, out.String() + errb.String()
}

func TestDBEncryptDecryptCommands(t *testing.T) {
	t.Setenv(store.DBEncryptionKeyEnv, "")
	t.Setenv("WEFT_DB_ENCRYPTION_KEY_FILE", "")
	t.Setenv("WEFT_SECRET_KEY", "")
	dir := t.TempDir()
	plain := filepath.Join(dir, "weft.db")
	enc := filepath.Join(dir, "weft-enc.db")
	back := filepath.Join(dir, "weft-back.db")
	rekeyed := filepath.Join(dir, "weft-rekeyed.db")
	keyFile := filepath.Join(dir, "db.passphrase")
	newKeyFile := filepath.Join(dir, "db.passphrase.new")
	if err := os.WriteFile(keyFile, []byte("a long passphrase for tests\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newKeyFile, []byte("another long passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	st, err := store.OpenWith(plain, store.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSecret(ctx, "OPENAI_API_KEY", "sk-plain-marker"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO projects VALUES('ent/marker-project', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	// Usage errors.
	if code, _ := runDBT(t); code != 2 {
		t.Fatalf("no command: exit %d", code)
	}
	if code, msg := runDBT(t, "encrypt", "--db", plain, "--out", enc); code != 2 || !strings.Contains(msg, "key is required") {
		t.Fatalf("missing key: exit %d %s", code, msg)
	}
	if code, _ := runDBT(t, "frobnicate", "--db", plain, "--out", enc, "--db-encryption-key-file", keyFile); code != 2 {
		t.Fatalf("unknown command: exit %d", code)
	}

	if code, msg := runDBT(t, "encrypt", "--db", plain, "--out", enc, "--db-encryption-key-file", keyFile); code != 0 {
		t.Fatalf("encrypt: exit %d %s", code, msg)
	}
	raw, _ := os.ReadFile(enc)
	if bytes.HasPrefix(raw, []byte("SQLite format 3")) || bytes.Contains(raw, []byte("marker-project")) {
		t.Fatal("encrypted output is readable")
	}
	// Running the server with the key from the file opens it, secrets included
	// (the command copied <db>.key next to the output).
	st, err = store.OpenWith(enc, store.OpenOptions{EncryptionKey: "a long passphrase for tests"})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok, err := st.GetSecret(ctx, "OPENAI_API_KEY"); err != nil || !ok || v != "sk-plain-marker" {
		t.Fatalf("secret in encrypted copy = %q %v %v", v, ok, err)
	}
	_ = st.Close()

	// Refuses to overwrite; wrong key fails cleanly.
	if code, _ := runDBT(t, "encrypt", "--db", plain, "--out", enc, "--db-encryption-key-file", keyFile); code != 1 {
		t.Fatalf("overwrite: exit %d", code)
	}
	if code, msg := runDBT(t, "decrypt", "--db", enc, "--out", back, "--db-encryption-key", "wrong key wrong key"); code != 1 || !strings.Contains(msg, "wrong database encryption key") {
		t.Fatalf("wrong key: exit %d %s", code, msg)
	}
	if _, err := os.Stat(back); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed decrypt left output behind")
	}

	if code, msg := runDBT(t, "rekey", "--db", enc, "--out", rekeyed, "--db-encryption-key-file", keyFile, "--new-db-encryption-key-file", newKeyFile); code != 0 {
		t.Fatalf("rekey: exit %d %s", code, msg)
	}
	if _, err := store.OpenWith(rekeyed, store.OpenOptions{EncryptionKey: "a long passphrase for tests"}); !errors.Is(err, store.ErrDBWrongKey) {
		t.Fatalf("old key on rekeyed db: %v", err)
	}

	t.Setenv(store.DBEncryptionKeyEnv, "another long passphrase")
	if code, msg := runDBT(t, "decrypt", "--db", rekeyed, "--out", back); code != 0 {
		t.Fatalf("decrypt (key from env): exit %d %s", code, msg)
	}
	st, err = store.OpenWith(back, store.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var n int
	if err := st.DB().QueryRow(`SELECT count(*) FROM projects WHERE project_id = 'ent/marker-project'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("decrypted project rows = %d %v", n, err)
	}
	if v, ok, err := st.GetSecret(ctx, "OPENAI_API_KEY"); err != nil || !ok || v != "sk-plain-marker" {
		t.Fatalf("secret after round trip = %q %v %v", v, ok, err)
	}
}

func TestResolveDBKey(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "k")
	_ = os.WriteFile(f, []byte("  secret value \n"), 0o644)
	if k, err := resolveKey("", f, "a", "b"); err != nil || k != "secret value" {
		t.Fatalf("key file: %q %v", k, err)
	}
	if _, err := resolveKey("x", f, "a", "b"); err == nil {
		t.Fatal("both key and key file accepted")
	}
	empty := filepath.Join(dir, "empty")
	_ = os.WriteFile(empty, []byte("\n"), 0o600)
	if _, err := resolveKey("", empty, "a", "b"); err == nil {
		t.Fatal("empty key file accepted")
	}
	if w := keyWarnings("short", f); len(w) != 2 {
		t.Fatalf("warnings = %v", w)
	}
}
