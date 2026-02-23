#!/usr/bin/env bash
set -euo pipefail

# Sync upstream Sigma-AI rules into AgentShield subtree.
# Usage:
#   scripts/sync_sigma_ai.sh [ref]
# Example:
#   scripts/sync_sigma_ai.sh main
#   scripts/sync_sigma_ai.sh feat/skills-abuse-rules

REF="${1:-main}"
PREFIX="rules/upstream/sigma-ai"
REMOTE="sigma-ai"

if ! git rev-parse --git-dir >/dev/null 2>&1; then
  echo "Run inside a git repo" >&2
  exit 1
fi

if ! git remote get-url "$REMOTE" >/dev/null 2>&1; then
  echo "Remote '$REMOTE' not found. Add it first:" >&2
  echo "  git remote add $REMOTE https://github.com/agentshield-ai/sigma-ai.git" >&2
  exit 1
fi

git fetch "$REMOTE" "$REF"
git subtree pull --prefix="$PREFIX" "$REMOTE" "$REF" --squash

echo "Synced $REMOTE/$REF into $PREFIX"
