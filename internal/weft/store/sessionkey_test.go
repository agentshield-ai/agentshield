package store

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"path/filepath"
	"testing"
)

// The session-signing key is stretched (Argon2id) from the secret key:
// stable for a database, different per database salt, and not the
// unstretched HMAC of the passphrase hash used before.
func TestSessionSigningKeyStretched(t *testing.T) {
	t.Setenv("WEFT_SECRET_KEY", "weak passphrase")
	ctx := context.Background()
	dir := t.TempDir()
	open := func(name string) *Store {
		s, err := Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	a := open("a.db")
	k1, err := a.SessionSigningKey(ctx)
	if err != nil || len(k1) != 32 {
		t.Fatalf("key %x %v", k1, err)
	}
	k2, _ := a.SessionSigningKey(ctx)
	if !bytes.Equal(k1, k2) {
		t.Fatal("key not stable")
	}
	b := open("b.db")
	if kb, _ := b.SessionSigningKey(ctx); bytes.Equal(k1, kb) {
		t.Fatal("two databases (different salts) share a session key")
	}
	base := sha256.Sum256([]byte("weak passphrase"))
	m := hmac.New(sha256.New, base[:])
	m.Write([]byte("weft-session-key-v1"))
	if bytes.Equal(k1, m.Sum(nil)) {
		t.Fatal("session key is the unstretched derivation")
	}
	if kdf, _ := a.SecretKeyDerivation(ctx); kdf != kdfArgon2 {
		t.Fatalf("new database kdf %q", kdf)
	}
}
