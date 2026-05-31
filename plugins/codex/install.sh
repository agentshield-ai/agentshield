#!/usr/bin/env bash
# Install the AgentShield connector for the OpenAI Codex CLI.
#
# Copies the connector into ~/.codex/agentshield/, prints the config.toml
# hook snippet to wire it in, and health-checks the engine.
#
# Environment:
#   AGENTSHIELD_URL         - Engine base URL (default: http://127.0.0.1:8433)
#   AGENTSHIELD_AUTH_TOKEN  - Engine bearer token (recommended)

set -euo pipefail

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEST_DIR="${HOME}/.codex/agentshield"
AGENTSHIELD_URL="${AGENTSHIELD_URL:-http://127.0.0.1:8433}"

PYTHON_BIN="$(command -v python3 || true)"
if [ -z "${PYTHON_BIN}" ]; then
  echo "[AgentShield] python3 is required but was not found on PATH." >&2
  exit 1
fi

echo "[AgentShield] Installing Codex connector to ${DEST_DIR}"
mkdir -p "${DEST_DIR}"
cp "${SRC_DIR}"/*.py "${DEST_DIR}/"

HOOK_PATH="${DEST_DIR}/agentshield_hook.py"
chmod +x "${HOOK_PATH}" || true

cat <<EOF

[AgentShield] Add the following to ~/.codex/config.toml to enable blocking
              interception (PreToolUse), approval gating (PermissionRequest),
              post-tool audit (PostToolUse) and lifecycle events:

# --- AgentShield hooks -----------------------------------------------------
[[hooks.PreToolUse]]
matcher = ".*"
[[hooks.PreToolUse.hooks]]
type = "command"
command = '${PYTHON_BIN} "${HOOK_PATH}"'
timeout = 5
statusMessage = "AgentShield: evaluating tool call"

[[hooks.PermissionRequest]]
matcher = ".*"
[[hooks.PermissionRequest.hooks]]
type = "command"
command = '${PYTHON_BIN} "${HOOK_PATH}"'
timeout = 5

[[hooks.PostToolUse]]
matcher = ".*"
[[hooks.PostToolUse.hooks]]
type = "command"
command = '${PYTHON_BIN} "${HOOK_PATH}"'
timeout = 5

[[hooks.SessionStart]]
[[hooks.SessionStart.hooks]]
type = "command"
command = '${PYTHON_BIN} "${HOOK_PATH}"'
timeout = 5

[[hooks.Stop]]
[[hooks.Stop.hooks]]
type = "command"
command = '${PYTHON_BIN} "${HOOK_PATH}"'
timeout = 5
# ---------------------------------------------------------------------------

[AgentShield] Optional connector settings live in ~/.codex/agentshield.json,
              e.g. {"timeout_ms": 200, "timeout_policy": "block"}.
              Set AGENTSHIELD_AUTH_TOKEN in your environment for authenticated
              engines.

EOF

echo "[AgentShield] Health-checking engine at ${AGENTSHIELD_URL} ..."
if curl -fsS --max-time 2 "${AGENTSHIELD_URL}/api/v1/health" >/dev/null 2>&1; then
  echo "[AgentShield] Engine reachable."
else
  echo "[AgentShield] Engine NOT reachable (start it before running Codex)." >&2
fi
