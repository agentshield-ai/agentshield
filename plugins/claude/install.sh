#!/usr/bin/env bash
# Install AgentShield hooks for Claude Code
#
# Recommended: HTTP hook integration (direct engine communication).
# Fallback: shell script hook (requires jq + curl).
set -euo pipefail

AGENTSHIELD_PORT="${AGENTSHIELD_PORT:-8433}"
AGENTSHIELD_URL="http://127.0.0.1:${AGENTSHIELD_PORT}"
INSTALL_DIR="${HOME}/.agentshield"
HOOK_SCRIPT="${INSTALL_DIR}/agentshield-hook.sh"
SETTINGS_FILE="${HOME}/.claude/settings.json"

echo "Installing AgentShield for Claude Code..."
echo ""

# Try to read auth token from local AgentShield config for convenience
AGENTSHIELD_TOKEN=""
if [ -f "${HOME}/.agentshield/config.yaml" ]; then
  AGENTSHIELD_TOKEN=$(awk '/^[[:space:]]*token:[[:space:]]*/ {gsub(/"/, "", $2); print $2}' "${HOME}/.agentshield/config.yaml" | head -n1 || true)
fi

# Copy shell fallback script
mkdir -p "$INSTALL_DIR"
cp "$(dirname "$0")/agentshield-hook.sh" "$HOOK_SCRIPT"
chmod +x "$HOOK_SCRIPT"

# Check if engine is running
if curl -s --max-time 2 "${AGENTSHIELD_URL}/api/v1/health" >/dev/null 2>&1; then
  echo "AgentShield engine detected on port ${AGENTSHIELD_PORT}"
else
  echo "WARNING: AgentShield engine not running on port ${AGENTSHIELD_PORT}"
  echo "  Start it with: agentshield serve --config ~/.agentshield/config.yaml"
  echo ""
fi

# Build the recommended HTTP hook configuration
# This uses Claude Code's native HTTP hook support to POST directly to the
# engine's /api/v1/hooks/claude endpoint — no shell script needed.
read -r -d '' HTTP_HOOK_CONFIG <<'HEREDOC' || true
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "http",
            "url": "AGENTSHIELD_URL_PLACEHOLDER/api/v1/hooks/claude",
            "headers": {
              "Authorization": "Bearer $AGENTSHIELD_AUTH_TOKEN"
            },
            "allowedEnvVars": ["AGENTSHIELD_AUTH_TOKEN"],
            "timeout": 5
          }
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "http",
            "url": "AGENTSHIELD_URL_PLACEHOLDER/api/v1/hooks/claude",
            "headers": {
              "Authorization": "Bearer $AGENTSHIELD_AUTH_TOKEN"
            },
            "allowedEnvVars": ["AGENTSHIELD_AUTH_TOKEN"],
            "timeout": 5,
            "async": true
          }
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "http",
            "url": "AGENTSHIELD_URL_PLACEHOLDER/api/v1/hooks/claude",
            "headers": {
              "Authorization": "Bearer $AGENTSHIELD_AUTH_TOKEN"
            },
            "allowedEnvVars": ["AGENTSHIELD_AUTH_TOKEN"],
            "timeout": 5
          }
        ]
      }
    ]
  }
}
HEREDOC

# Substitute URL placeholder
HTTP_HOOK_CONFIG="${HTTP_HOOK_CONFIG//AGENTSHIELD_URL_PLACEHOLDER/$AGENTSHIELD_URL}"

# Shell fallback configuration (for environments without HTTP hook support)
read -r -d '' SHELL_HOOK_CONFIG <<HEREDOC || true
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "${HOOK_SCRIPT}",
            "timeout": 5
          }
        ]
      }
    ]
  }
}
HEREDOC

echo "=========================================="
echo "  RECOMMENDED: HTTP Hook (no dependencies)"
echo "=========================================="
echo ""
echo "Add this to your ${SETTINGS_FILE}:"
echo ""
echo "$HTTP_HOOK_CONFIG"
echo ""
echo "=========================================="
echo "  FALLBACK: Shell Hook (requires jq+curl)"
echo "=========================================="
echo ""
echo "Add this to your ${SETTINGS_FILE}:"
echo ""
echo "$SHELL_HOOK_CONFIG"
echo ""
echo "=========================================="
echo ""
echo "Shell hook installed to: ${HOOK_SCRIPT}"
echo ""

if [ -n "$AGENTSHIELD_TOKEN" ]; then
  echo "Set this in your shell profile before using Claude Code:"
  echo "  export AGENTSHIELD_AUTH_TOKEN=${AGENTSHIELD_TOKEN}"
else
  echo "Set AGENTSHIELD_AUTH_TOKEN in your shell profile (required when engine auth is enabled):"
  echo "  export AGENTSHIELD_AUTH_TOKEN=your-token-here"
fi
echo ""
echo "Test the HTTP hook:"
echo "  curl -s -X POST ${AGENTSHIELD_URL}/api/v1/hooks/claude \\"
echo "    -H 'Content-Type: application/json' \\"
echo "    -d '{\"session_id\":\"test\",\"hook_event_name\":\"PreToolUse\",\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"ls /tmp\"}}'"
echo ""
echo "Test the shell hook:"
echo "  echo '{\"hook_event_name\":\"PreToolUse\",\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"ls /tmp\"}}' | ${HOOK_SCRIPT}"
