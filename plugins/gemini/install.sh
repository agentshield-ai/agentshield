#!/usr/bin/env bash
# Install the AgentShield connector for the Google Gemini CLI.
#
# Copies the connector package into ~/.agentshield/gemini/, prints the
# settings.json hooks snippet to wire it into Gemini CLI, and health-checks
# the engine.
set -euo pipefail

INSTALL_DIR="${HOME}/.agentshield/gemini"
SETTINGS_FILE="${HOME}/.gemini/settings.json"
SRC_DIR="$(cd "$(dirname "$0")" && pwd)"
PY="${PYTHON:-python3}"

echo "Installing AgentShield for the Gemini CLI..."

# Try to read auth token from the local AgentShield config for convenience.
AGENTSHIELD_TOKEN=""
if [ -f "${HOME}/.agentshield/config.yaml" ]; then
  AGENTSHIELD_TOKEN=$(awk '/^[[:space:]]*token:[[:space:]]*/ {print $2}' "${HOME}/.agentshield/config.yaml" | tr -d '"' | head -n1 || true)
fi

# Copy the connector package.
mkdir -p "$INSTALL_DIR"
cp -r "${SRC_DIR}/agentshield_gemini" "$INSTALL_DIR/"

# Resolve the entry-point command. The connector is pure stdlib, so it runs
# directly with the system Python via the package's module entry-point.
HOOK_CMD="${PY} -m agentshield_gemini.hook"

# Health-check the engine.
ENGINE_URL="${AGENTSHIELD_URL:-http://127.0.0.1:8433}"
if curl -s --max-time 2 "${ENGINE_URL}/api/v1/health" >/dev/null 2>&1; then
  echo "AgentShield engine detected at ${ENGINE_URL}"
else
  echo "WARNING: AgentShield engine not reachable at ${ENGINE_URL}"
  echo "  Start it with: agentshield serve --config ~/.agentshield/config.yaml"
fi

# Print the settings.json snippet. The connector reads its package from
# PYTHONPATH; we point PYTHONPATH at the install dir in the command.
cat <<EOF

Add the following to ${SETTINGS_FILE} (merge with any existing "hooks"):

{
  "hooks": {
    "BeforeTool": [
      { "matcher": ".*", "hooks": [ { "type": "command",
          "command": "PYTHONPATH=${INSTALL_DIR} ${HOOK_CMD}", "name": "agentshield" } ] }
    ],
    "AfterTool": [
      { "matcher": ".*", "hooks": [ { "type": "command",
          "command": "PYTHONPATH=${INSTALL_DIR} ${HOOK_CMD}", "name": "agentshield" } ] }
    ],
    "SessionStart": [
      { "matcher": "", "hooks": [ { "type": "command",
          "command": "PYTHONPATH=${INSTALL_DIR} ${HOOK_CMD}", "name": "agentshield" } ] }
    ],
    "SessionEnd": [
      { "matcher": "", "hooks": [ { "type": "command",
          "command": "PYTHONPATH=${INSTALL_DIR} ${HOOK_CMD}", "name": "agentshield" } ] }
    ]
  }
}

EOF

if [ -n "$AGENTSHIELD_TOKEN" ]; then
  echo "Set this in your shell profile before launching the Gemini CLI:"
  echo "  export AGENTSHIELD_AUTH_TOKEN=${AGENTSHIELD_TOKEN}"
else
  echo "Set AGENTSHIELD_AUTH_TOKEN in your shell profile (required when engine auth is enabled):"
  echo "  export AGENTSHIELD_AUTH_TOKEN=your-32-plus-char-token"
fi

echo ""
echo "Connector installed to ${INSTALL_DIR}"
echo ""
echo "Test it with:"
echo "  echo '{\"hook_event_name\":\"BeforeTool\",\"tool_name\":\"run_shell_command\",\"tool_input\":{\"command\":\"ls /tmp\"}}' | PYTHONPATH=${INSTALL_DIR} ${HOOK_CMD}"
echo "  echo '{\"hook_event_name\":\"BeforeTool\",\"tool_name\":\"run_shell_command\",\"tool_input\":{\"command\":\"sudo rm -rf /\"}}' | PYTHONPATH=${INSTALL_DIR} ${HOOK_CMD}"
