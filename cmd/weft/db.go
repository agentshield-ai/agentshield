package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// dbKeyFlags configure database encryption at rest (store/dbcrypt.go).
type dbKeyFlags struct {
	key, keyFile *string
}

func registerDBKeyFlags(fs *flag.FlagSet) *dbKeyFlags {
	return &dbKeyFlags{
		key: fs.String("db-encryption-key", os.Getenv(store.DBEncryptionKeyEnv),
			"passphrase that encrypts the database at rest (env "+store.DBEncryptionKeyEnv+"); empty keeps it in plaintext. Prefer the env var or --db-encryption-key-file: flags show in process lists"),
		keyFile: fs.String("db-encryption-key-file", os.Getenv("WEFT_DB_ENCRYPTION_KEY_FILE"),
			"file holding the database encryption passphrase, surrounding whitespace ignored (env WEFT_DB_ENCRYPTION_KEY_FILE)"),
	}
}

// resolve returns the passphrase (empty: no encryption).
func (f *dbKeyFlags) resolve() (string, error) {
	return resolveKey(*f.key, *f.keyFile, "--db-encryption-key", "--db-encryption-key-file")
}

func resolveKey(key, keyFile, keyName, fileName string) (string, error) {
	if key != "" && keyFile != "" {
		return "", fmt.Errorf("set only one of %s and %s (or their env vars)", keyName, fileName)
	}
	if keyFile == "" {
		return key, nil
	}
	b, err := os.ReadFile(keyFile)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", fileName, err)
	}
	k := strings.TrimSpace(string(b))
	if k == "" {
		return "", fmt.Errorf("%s %s is empty", fileName, keyFile)
	}
	return k, nil
}

// keyWarnings flags weak or exposed key material.
func keyWarnings(key, keyFile string) []string {
	var out []string
	if key != "" && len(key) < 16 {
		out = append(out, "the database encryption passphrase is shorter than 16 characters; use a long random value, e.g. openssl rand -base64 32")
	}
	if keyFile != "" {
		if fi, err := os.Stat(keyFile); err == nil && fi.Mode().Perm()&0o077 != 0 {
			out = append(out, fmt.Sprintf("%s is readable by other users (mode %#o); run: chmod 600 %s", keyFile, fi.Mode().Perm(), keyFile))
		}
	}
	return out
}

const dbUsage = `usage: weft db <command> --db PATH --out PATH [key flags]

Converts a stopped server's database between plaintext and encrypted at rest.
The original is left untouched; the copy is verified before the command exits.

commands:
  encrypt   plaintext --db -> encrypted --out (key: --db-encryption-key[-file])
  decrypt   encrypted --db -> plaintext --out (key: --db-encryption-key[-file])
  rekey     encrypted --db -> --out under a new key (--new-db-encryption-key[-file])

flags:
`

// runDB implements `weft db ...` and returns the exit code.
func runDB(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("weft db", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, dbUsage)
		fs.PrintDefaults()
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fs.Usage()
		return 2
	}
	cmd := args[0]
	dbPath := fs.String("db", envOr("WEFT_DB", defaultDB()), "source database (env WEFT_DB)")
	out := fs.String("out", "", "destination database; must not exist")
	keys := registerDBKeyFlags(fs)
	newKey := fs.String("new-db-encryption-key", os.Getenv("WEFT_NEW_DB_ENCRYPTION_KEY"), "rekey: the new passphrase (env WEFT_NEW_DB_ENCRYPTION_KEY)")
	newKeyFile := fs.String("new-db-encryption-key-file", os.Getenv("WEFT_NEW_DB_ENCRYPTION_KEY_FILE"), "rekey: file holding the new passphrase (env WEFT_NEW_DB_ENCRYPTION_KEY_FILE)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() > 0 || *out == "" {
		fmt.Fprintln(stderr, "weft db: --out is required and no positional arguments are accepted")
		return 2
	}
	key, err := keys.resolve()
	if err != nil {
		fmt.Fprintln(stderr, "weft db:", err)
		return 2
	}
	if key == "" {
		fmt.Fprintln(stderr, "weft db: a database encryption key is required (--db-encryption-key-file, --db-encryption-key or "+store.DBEncryptionKeyEnv+")")
		return 2
	}
	var srcKey, dstKey string
	switch cmd {
	case "encrypt":
		dstKey = key
	case "decrypt":
		srcKey = key
	case "rekey":
		nk, err := resolveKey(*newKey, *newKeyFile, "--new-db-encryption-key", "--new-db-encryption-key-file")
		if err != nil || nk == "" {
			fmt.Fprintln(stderr, "weft db rekey: a new key is required (--new-db-encryption-key-file or --new-db-encryption-key)", err)
			return 2
		}
		srcKey, dstKey = key, nk
	default:
		fmt.Fprintf(stderr, "weft db: unknown command %q\n", cmd)
		fs.Usage()
		return 2
	}
	for _, w := range keyWarnings(dstKey, *keys.keyFile) {
		fmt.Fprintln(stderr, "warning:", w)
	}
	if err := store.ConvertDB(*dbPath, *out, srcKey, dstKey); err != nil {
		fmt.Fprintf(stderr, "weft db %s: %v\n", cmd, err)
		return 1
	}
	// Provider secrets are encrypted with <db>.key unless WEFT_SECRET_KEY is
	// set; the copy needs the same key to read them.
	if err := copySecretKeyFile(store.SecretKeyFile(*dbPath), store.SecretKeyFile(*out)); err != nil {
		fmt.Fprintf(stderr, "weft db %s: database copied, but %v\n", cmd, err)
		return 1
	}
	past := map[string]string{"encrypt": "Encrypted", "decrypt": "Decrypted", "rekey": "Re-keyed"}[cmd]
	fmt.Fprintf(stdout, "%s %s -> %s (verified).\n", past, *dbPath, *out)
	fmt.Fprintf(stdout, "Next: stop weft, use %s as the database (e.g. mv %s %s; %s is a copy of the secret key file) and start weft ", *out, *out, *dbPath, store.SecretKeyFile(*out))
	switch cmd {
	case "encrypt":
		fmt.Fprintf(stdout, "with the key set. Then delete the plaintext %s, its -wal/-shm files and any backups of it.\n", *dbPath)
	case "decrypt":
		fmt.Fprintln(stdout, "without a database encryption key.")
	case "rekey":
		fmt.Fprintf(stdout, "with the new key. Then delete %s, which the old key still opens.\n", *dbPath)
	}
	return 0
}

func copySecretKeyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading secret key file: %w", err)
	}
	if _, err := os.Stat(dst); err == nil {
		return nil // keep an existing key file
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("copying secret key file: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return fmt.Errorf("copying secret key file: %w", err)
	}
	return f.Close()
}
