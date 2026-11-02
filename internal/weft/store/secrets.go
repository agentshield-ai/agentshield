package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Secrets (provider API keys) are encrypted at rest with AES-256-GCM. The
// key comes from WEFT_SECRET_KEY (any string; hashed to 32 bytes) or from a
// random key file created next to the database with 0600 permissions.

const encPrefix = "enc:v1:"

// SecretKeyFile returns the default key file path for a database.
func SecretKeyFile(dbPath string) string { return dbPath + ".key" }

// LoadSecretKey resolves the encryption key: the explicit passphrase when
// given, else the key file (created when missing).
func LoadSecretKey(passphrase, keyFile string) ([]byte, error) {
	if passphrase != "" {
		k := sha256.Sum256([]byte(passphrase))
		return k[:], nil
	}
	if b, err := os.ReadFile(keyFile); err == nil {
		k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(k) != 32 {
			return nil, fmt.Errorf("secret key file %s is invalid", keyFile)
		}
		return k, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading secret key file: %w", err)
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("creating secret key file: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(k) + "\n"); err != nil {
		return nil, err
	}
	return k, nil
}

// SetSecretKey installs the encryption key and re-encrypts any legacy
// plaintext secrets.
func (s *Store) SetSecretKey(ctx context.Context, key []byte) error {
	if len(key) != 32 {
		return errors.New("secret key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	s.aead = gcm
	rows, err := s.w.QueryContext(ctx, `SELECT name, value FROM secrets WHERE value NOT LIKE 'enc:v1:%'`)
	if err != nil {
		return err
	}
	type kv struct{ name, value string }
	var legacy []kv
	for rows.Next() {
		var x kv
		if err := rows.Scan(&x.name, &x.value); err != nil {
			rows.Close()
			return err
		}
		legacy = append(legacy, x)
	}
	rows.Close()
	for _, x := range legacy {
		enc, err := s.encrypt(x.value)
		if err != nil {
			return err
		}
		if _, err := s.w.ExecContext(ctx, `UPDATE secrets SET value = ? WHERE name = ?`, enc, x.name); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) encrypt(plain string) (string, error) {
	if s.aead == nil {
		return "", errors.New("secret storage has no encryption key")
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := s.aead.Seal(nonce, nonce, []byte(plain), []byte("weft-secret"))
	return encPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

func (s *Store) decrypt(stored string) (string, error) {
	if !strings.HasPrefix(stored, encPrefix) {
		return stored, nil // legacy plaintext (re-encrypted when a key is set)
	}
	if s.aead == nil {
		return "", errors.New("secret storage has no encryption key")
	}
	raw, err := base64.StdEncoding.DecodeString(stored[len(encPrefix):])
	if err != nil || len(raw) < s.aead.NonceSize() {
		return "", errors.New("corrupt secret")
	}
	pt, err := s.aead.Open(nil, raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():], []byte("weft-secret"))
	if err != nil {
		return "", errors.New("cannot decrypt secret: wrong WEFT_SECRET_KEY or key file")
	}
	return string(pt), nil
}

// SetSecret stores a provider secret, encrypted.
func (s *Store) SetSecret(ctx context.Context, name, value string) error {
	if !secretNameOK(name) {
		return Invalidf("invalid secret name %q (use A-Z, 0-9, _)", name)
	}
	if len(value) > 16384 {
		return Invalidf("secret too long")
	}
	enc, err := s.encrypt(value)
	if err != nil {
		return err
	}
	_, err = s.w.ExecContext(ctx, `INSERT INTO secrets(name, value, updated_at) VALUES(?,?,?)
		ON CONFLICT(name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, name, enc, nowMicros())
	return err
}

func secretNameOK(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, r := range name {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

// GetSecret returns a stored secret, decrypted.
func (s *Store) GetSecret(ctx context.Context, name string) (string, bool, error) {
	var v string
	err := s.r.QueryRowContext(ctx, `SELECT value FROM secrets WHERE name = ?`, name).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	pt, err := s.decrypt(v)
	if err != nil {
		return "", false, err
	}
	return pt, true, nil
}

// ListSecrets returns stored secret names.
func (s *Store) ListSecrets(ctx context.Context) ([]string, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT name FROM secrets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// DeleteSecret removes a stored secret.
func (s *Store) DeleteSecret(ctx context.Context, name string) error {
	_, err := s.w.ExecContext(ctx, `DELETE FROM secrets WHERE name = ?`, name)
	return err
}
