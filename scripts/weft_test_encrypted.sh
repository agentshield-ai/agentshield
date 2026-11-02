#!/usr/bin/env bash
# Runs the Weft Go test suite with every test database encrypted at rest
# (go-sqlite3 + the adiantum VFS) instead of the default plaintext driver
# (modernc.org/sqlite). store.Open honours WEFT_DB_ENCRYPTION_KEY, so the same
# tests exercise both drivers. Extra arguments are passed to `go test`.
set -euo pipefail
cd "$(dirname "$0")/.."
export WEFT_DB_ENCRYPTION_KEY="${WEFT_DB_ENCRYPTION_KEY:-weft-encrypted-test-suite-passphrase}"
go test "$@" ./internal/weft/... ./cmd/weft/...
# Prove the suite ran encrypted rather than silently on the default driver.
out=$(go test -count=1 -run 'TestOpenHonoursEnvKey' -v ./internal/weft/store/)
if ! grep -q 'suite database encrypted at rest: true' <<<"$out"; then
  echo "$out" >&2
  echo "the suite did not run on the encrypted driver" >&2
  exit 1
fi
echo "weft tests passed with database encryption at rest"
