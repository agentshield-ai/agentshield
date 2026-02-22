#!/usr/bin/env bash
set -euo pipefail

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

REPO_URL="https://github.com/agentshield-ai/sigma-ai.git"
REF="${1:-main}"
LOCAL_PREFIX="rules/upstream/sigma-ai"

if [ ! -d "$LOCAL_PREFIX/rules" ]; then
  echo "Missing local subtree at $LOCAL_PREFIX" >&2
  exit 1
fi

git clone --depth 1 --branch "$REF" "$REPO_URL" "$TMP_DIR/sigma-ai" >/dev/null 2>&1

# Compare canonical upstream content vs vendored subtree content.
# We compare repo root files commonly needed by consumers and rules tree.
diff -qr \
  "$LOCAL_PREFIX/rules" "$TMP_DIR/sigma-ai/rules"

echo "Sigma-AI subtree is in sync with $REF"
