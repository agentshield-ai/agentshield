package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
)

// legacyEncrypt encrypts like Weft before secret scopes: AES-GCM with the
// SHA-256 of WEFT_SECRET_KEY and AAD "weft-secret".
func legacyEncrypt(t *testing.T, passphrase, plain string) string {
	k := sha256.Sum256([]byte(passphrase))
	block, _ := aes.NewCipher(k[:])
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	_, _ = rand.Read(nonce)
	return "enc:v1:" + base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), []byte("weft-secret")))
}

// E7 + migration: a database written before scopes (unsalted key, enc:v1
// rows, a reference bucket naming a secret) still opens and reads; its
// secrets become server secrets, the bucket's secret becomes bucket
// credentials, and the key derivation stays "direct". A new database gets a
// salted Argon2id key.
func TestSecretsMigrationAndKeyDerivation(t *testing.T) {
	t.Setenv("WEFT_SECRET_KEY", "old-passphrase")
	// The old database is written as plain SQLite below, as before
	// encryption at rest; the encrypted suite's key would refuse it.
	t.Setenv(DBEncryptionKeyEnv, "")
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE secrets (name TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE reference_buckets (id TEXT PRIMARY KEY, entity TEXT NOT NULL, project_id TEXT NOT NULL DEFAULT '', provider TEXT NOT NULL,
			bucket TEXT NOT NULL, prefix TEXT NOT NULL DEFAULT '', region TEXT NOT NULL DEFAULT '', endpoint TEXT NOT NULL DEFAULT '',
			path_style INTEGER, secret_name TEXT NOT NULL DEFAULT '', test_object_key TEXT NOT NULL DEFAULT '', verified_at INTEGER,
			created_at INTEGER NOT NULL, created_by TEXT, UNIQUE (entity, project_id, provider, bucket, prefix))`,
		`INSERT INTO reference_buckets(id, entity, provider, bucket, secret_name, created_at) VALUES('b1', 'acme', 's3', 'bkt', 'S3CREDS', 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for name, v := range map[string]string{"OPENAI_API_KEY": "sk-old", "S3CREDS": `{"access_key_id":"a","secret_access_key":"b"}`} {
		if _, err := db.Exec(`INSERT INTO secrets(name, value, updated_at) VALUES(?, ?, 1)`, name, legacyEncrypt(t, "old-passphrase", v)); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if kdf, _ := s.SecretKeyDerivation(ctx); kdf != kdfDirect {
		t.Fatalf("existing database kdf = %q", kdf)
	}
	if v, ok, err := s.GetSecret(ctx, "OPENAI_API_KEY"); err != nil || !ok || v != "sk-old" {
		t.Fatalf("migrated secret = %q %v %v", v, ok, err)
	}
	rec, err := s.ResolveSecret(ctx, "acme/app", "OPENAI_API_KEY")
	if err != nil || rec == nil || rec.Scope != SecretScopeServer {
		t.Fatalf("resolve = %+v %v", rec, err)
	}
	// Bucket credentials are never resolved for objects, only for buckets.
	if rec, err := s.ResolveSecret(ctx, "acme/app", "S3CREDS"); rec != nil || err != nil {
		t.Fatalf("bucket credentials resolved: %+v %v", rec, err)
	}
	if v, ok, err := s.GetBucketCredential(ctx, "S3CREDS"); err != nil || !ok || !strings.Contains(v, "access_key_id") {
		t.Fatalf("bucket credential = %q %v %v", v, ok, err)
	}
	var raw string
	_ = s.r.QueryRow(`SELECT value FROM secrets WHERE name = 'OPENAI_API_KEY'`).Scan(&raw)
	if !strings.HasPrefix(raw, encPrefixV2) {
		t.Fatalf("enc:v1 row not upgraded: %q", raw[:10])
	}
	_ = s.Close()
	// Re-opening (migration already done) still reads it.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok, err := s.GetSecret(ctx, "OPENAI_API_KEY"); err != nil || !ok || v != "sk-old" {
		t.Fatalf("after reopen = %q %v %v", v, ok, err)
	}
	_ = s.Close()

	// A new database stretches the key with a stored salt.
	fresh := filepath.Join(t.TempDir(), "new.db")
	s, err = Open(fresh)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if kdf, _ := s.SecretKeyDerivation(ctx); kdf != kdfArgon2 {
		t.Fatalf("new database kdf = %q", kdf)
	}
	if err := s.SetSecret(ctx, "K", "v"); err != nil {
		t.Fatal(err)
	}
	// The unsalted SHA-256 key of the passphrase does not decrypt it.
	_ = s.r.QueryRow(`SELECT value FROM secrets WHERE name = 'K'`).Scan(&raw)
	k := sha256.Sum256([]byte("old-passphrase"))
	block, _ := aes.NewCipher(k[:])
	gcm, _ := cipher.NewGCM(block)
	ct, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(raw, encPrefixV2))
	if _, err := gcm.Open(nil, ct[:gcm.NonceSize()], ct[gcm.NonceSize():], aadV2(SecretScopeServer, "", "K", SecretKindSecret)); err == nil {
		t.Fatal("new database secret decrypts with the unsalted key")
	}
	if v, ok, err := s.GetSecret(ctx, "K"); err != nil || !ok || v != "v" {
		t.Fatalf("argon2 secret = %q %v %v", v, ok, err)
	}
	// A row moved to another scope by editing the database does not decrypt.
	if _, err := s.w.Exec(`UPDATE secrets SET scope = 'project', scope_id = 'evil/p' WHERE name = 'K'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveSecret(ctx, "evil/p", "K"); err == nil {
		t.Fatal("a re-scoped row decrypted")
	}
}

// Scope precedence and validation.
func TestSecretScopesResolve(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, w := range []SecretWrite{
		{Name: "K", Value: "server"},
		{Name: "K", Value: "team", Scope: SecretScopeTeam, ScopeID: "acme"},
		{Name: "K", Value: "project", Scope: SecretScopeProject, ScopeID: "acme/app", AllowedHosts: []string{"API.example.com", "*.corp.example:8443"}},
	} {
		if err := s.PutSecret(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	for pid, want := range map[string]string{"acme/app": "project", "acme/other": "team", "beta/x": "server", "": "server"} {
		rec, err := s.ResolveSecret(ctx, pid, "K")
		if err != nil || rec == nil || rec.Value != want {
			t.Fatalf("%q: %+v %v", pid, rec, err)
		}
	}
	rec, _ := s.ResolveSecret(ctx, "acme/app", "K")
	if strings.Join(rec.AllowedHosts, ",") != "*.corp.example:8443,api.example.com" {
		t.Fatalf("hosts = %v", rec.AllowedHosts)
	}
	for _, bad := range []SecretWrite{
		{Name: "K", Value: "v", Scope: "galaxy"},
		{Name: "K", Value: "v", Scope: SecretScopeProject},
		{Name: "K", Value: "v", Kind: SecretKindBucket, Scope: SecretScopeProject, ScopeID: "a/b"},
		{Name: "K", Value: "v", AllowedHosts: []string{"https://x.example/"}},
		{Name: "lower", Value: "v"},
	} {
		if err := s.PutSecret(ctx, bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if !SecretHostAllowed([]string{"*.corp.example"}, "a.corp.example", "443") || SecretHostAllowed([]string{"*.corp.example"}, "corp.example", "443") ||
		!SecretHostAllowed(nil, "any", "1") || SecretHostAllowed([]string{"a.example:8443"}, "a.example", "443") {
		t.Fatal("SecretHostAllowed")
	}
}

// I7: feedback types are limited to [A-Za-z0-9_.:*-]; Weft's own types fit.
func TestFeedbackTypeCharset(t *testing.T) {
	for _, ok := range []string{"weft.reaction", "weft.note", "weft.scorer.my-judge_v2", "weft.score.helpfulness", "weft.annotation.quality",
		"weft.signal", "wandb.reaction.1", "custom:thing*"} {
		if !ValidFeedbackType(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"<!channel>", "weft.note <https://evil|click>", "a b", "x\ny", "a&b"} {
		if ValidFeedbackType(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
