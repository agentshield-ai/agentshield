#!/usr/bin/env bash
# Install AgentShield hooks for Claude Code.
set -euo pipefail

INSTALL_DIR="${HOME}/.agentshield"
HOOK_PATH="${INSTALL_DIR}/agentshield-hook.sh"
LIB_PATH="${INSTALL_DIR}/agentshield-lib.sh"
SETTINGS_FILE="${HOME}/.claude/settings.json"

echo "Installing AgentShield for Claude Code..."

# Try to read the auth token from the local AgentShield config for convenience.
AGENTSHIELD_TOKEN=""
if [ -f "${HOME}/.agentshield/config.yaml" ]; then
  AGENTSHIELD_TOKEN=$(awk '/^[[:space:]]*token:[[:space:]]*/ {print $2}' "${HOME}/.agentshield/config.yaml" | tr -d '"' | head -n1 || true)
fi

# Copy the dispatcher and its shared library (the dispatcher sources the lib
# from its own directory, so both must live together).
mkdir -p "$INSTALL_DIR"
cp "$(dirname "$0")/agentshield-hook.sh" "$HOOK_PATH"
cp "$(dirname "$0")/agentshield-lib.sh" "$LIB_PATH"
chmod +x "$HOOK_PATH"

# Check whether the engine is running (non-blocking, short timeout).
if curl -s --max-time 2 http://127.0.0.1:8433/api/v1/health >/dev/null 2>&1; then
  echo "AgentShield engine detected on port 8433"
else
  echo "AgentShield engine not running on port 8433"
  echo "  Start it with: agentshield serve --config ~/.agentshield/config.yaml"
fi

# Generate the Claude Code settings snippet. A single dispatcher script is
# registered against every relevant hook event; it branches internally on
# hook_event_name.
HOOK_CONFIG=$(cat <<EOF
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "*", "hooks": [ { "type": "command", "command": "${HOOK_PATH}", "timeout": 10 } ] }
    ],
    "PostToolUse": [
      { "matcher": "*", "hooks": [ { "type": "command", "command": "${HOOK_PATH}" } ] }
    ],
    "PostToolUseFailure": [
      { "matcher": "*", "hooks": [ { "type": "command", "command": "${HOOK_PATH}" } ] }
    ],
    "SessionStart": [
      { "matcher": "*", "hooks": [ { "type": "command", "command": "${HOOK_PATH}" } ] }
    ],
    "Stop": [
      { "matcher": "*", "hooks": [ { "type": "command", "command": "${HOOK_PATH}" } ] }
    ],
    "SubagentStop": [
      { "matcher": "*", "hooks": [ { "type": "command", "command": "${HOOK_PATH}" } ] }
    ]
  }
}
EOF
)

if [ -f "$SETTINGS_FILE" ]; then
  echo ""
  echo "Add (merge) this into your ${SETTINGS_FILE}:"
  echo ""
  echo "$HOOK_CONFIG"
  echo ""
  echo "Or run: claude /hooks to configure interactively"
else
  echo ""
  echo "Create ${SETTINGS_FILE} with:"
  echo ""
  echo "$HOOK_CONFIG"
fi

echo ""
echo "AgentShield hook installed to ${HOOK_PATH}"
echo "Shared library installed to ${LIB_PATH}"
echo ""
if [ -n "$AGENTSHIELD_TOKEN" ]; then
  echo "Set this in your shell profile before using Claude Code hooks:"
  echo "  export AGENTSHIELD_AUTH_TOKEN=${AGENTSHIELD_TOKEN}"
else
  echo "Set AGENTSHIELD_AUTH_TOKEN in your shell profile (required when engine auth is enabled)."
  echo "Example:"
  echo "  export AGENTSHIELD_AUTH_TOKEN=your-32-plus-char-token"
fi
echo ""
echo "Fail mode defaults to ALLOW (fail-open) for this local hook."
echo "For fail-closed parity with the OpenClaw/Hermes connectors, set:"
echo "  export AGENTSHIELD_TIMEOUT_POLICY=block"
echo ""
echo "Test it with:"
echo "  echo '{\"hook_event_name\":\"PreToolUse\",\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"ls /tmp\"}}' | ${HOOK_PATH}"
echo "  echo '{\"hook_event_name\":\"PreToolUse\",\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"sudo rm -rf /\"}}' | ${HOOK_PATH}"
