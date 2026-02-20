#!/usr/bin/env bash
# Install AgentShield hooks for Claude Code
set -euo pipefail

INSTALL_DIR="${HOME}/.agentshield"
HOOK_PATH="${INSTALL_DIR}/agentshield-hook.sh"
SETTINGS_FILE="${HOME}/.claude/settings.json"

echo "Installing AgentShield for Claude Code..."

# Try to read auth token from local AgentShield config for convenience
AGENTSHIELD_TOKEN=""
if [ -f "${HOME}/.agentshield/config.yaml" ]; then
  AGENTSHIELD_TOKEN=$(awk '/^[[:space:]]*token:[[:space:]]*/ {print $2}' "${HOME}/.agentshield/config.yaml" | tr -d '"' | head -n1 || true)
fi

# Copy hook script
mkdir -p "$INSTALL_DIR"
cp "$(dirname "$0")/agentshield-hook.sh" "$HOOK_PATH"
chmod +x "$HOOK_PATH"

# Check if engine is running
if curl -s --max-time 2 http://127.0.0.1:8432/api/v1/health >/dev/null 2>&1; then
  echo "✅ AgentShield engine detected"
else
  echo "⚠️  AgentShield engine not running on port 8432"
  echo "   Start it with: agentshield serve --config ~/.agentshield/config.yaml"
fi

# Generate Claude Code settings snippet
HOOK_CONFIG=$(cat <<EOF
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "${HOOK_PATH}"
          }
        ]
      }
    ]
  }
}
EOF
)

# Check if settings file exists
if [ -f "$SETTINGS_FILE" ]; then
  echo ""
  echo "Add this to your ${SETTINGS_FILE}:"
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
echo "✅ AgentShield hook installed to ${HOOK_PATH}"
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
echo "Test it with:"
echo "  echo '{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"ls /tmp\"}}' | ${HOOK_PATH}"
echo "  echo '{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"sudo rm -rf /\"}}' | ${HOOK_PATH}"
