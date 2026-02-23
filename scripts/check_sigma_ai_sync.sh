#!/usr/bin/env bash
set -euo pipefail

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

REPO_URL="https://github.com/agentshield-ai/sigma-ai.git"
REF="${1:-main}"
LOCAL_RULES="rules"

if [ ! -d "$LOCAL_RULES" ]; then
  echo "Missing local rules directory at $LOCAL_RULES" >&2
  exit 1
fi

git clone --depth 1 --branch "$REF" "$REPO_URL" "$TMP_DIR/sigma-ai" >/dev/null 2>&1

# Compare upstream rules against local production rules.
# Local rules are a superset (may contain additional rules not yet upstream).
# This check ensures every upstream rule has a corresponding local copy.
MISSING=0
while IFS= read -r -d '' upstream_rule; do
  rel_path="${upstream_rule#"$TMP_DIR/sigma-ai/"}"
  if [ ! -f "$LOCAL_RULES/$rel_path" ]; then
    echo "MISSING: $rel_path (exists upstream but not locally)" >&2
    MISSING=$((MISSING + 1))
  fi
done < <(find "$TMP_DIR/sigma-ai/rules" -name '*.yml' -print0)

if [ "$MISSING" -gt 0 ]; then
  echo "$MISSING upstream rule(s) missing locally" >&2
  exit 1
fi

echo "All upstream sigma-ai rules present in local rules directory"
