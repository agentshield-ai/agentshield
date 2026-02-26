#!/usr/bin/env bash
# AgentShield hook for Claude Code
# Evaluates tool calls against AgentShield engine before execution.
#
# Install: Add to ~/.claude/settings.json under hooks.PreToolUse
# Requires: AgentShield engine running (default: http://127.0.0.1:8432)
#
# Exit codes:
#   0 = allow (no output or {"decision":"approve"})
#   2 = block ({"decision":"block","reason":"..."})
#
# Environment:
#   AGENTSHIELD_URL         - Engine URL (default: http://127.0.0.1:8432)
#   AGENTSHIELD_AUTH_TOKEN  - Engine auth token (required when auth is enabled)

set -euo pipefail

AGENTSHIELD_URL="${AGENTSHIELD_URL:-http://127.0.0.1:8432}"
AGENTSHIELD_AUTH_TOKEN="${AGENTSHIELD_AUTH_TOKEN:-}"
AGENTSHIELD_FAIL_POLICY="${AGENTSHIELD_FAIL_POLICY:-allow}"

# Read JSON input from stdin (Claude Code passes tool call context)
INPUT=$(cat)

# Extract tool name and parameters from Claude Code's PreToolUse schema
TOOL_NAME=$(echo "$INPUT" | jq -r '.tool_name // .toolName // "unknown"')
TOOL_INPUT=$(echo "$INPUT" | jq -c '.tool_input // .input // {}')

# Build command string for rule matching
# Claude Code tools: Bash, Write, Edit, Read, Glob, Grep, etc.
COMMAND=""
case "$TOOL_NAME" in
  Bash|bash)
    COMMAND=$(echo "$TOOL_INPUT" | jq -r '.command // ""')
    ;;
  Write|write|FileWrite)
    COMMAND=$(echo "$TOOL_INPUT" | jq -r '.file_path // ""')
    CONTENT=$(echo "$TOOL_INPUT" | jq -r '.content // ""')
    ;;
  Edit|edit|FileEdit)
    COMMAND=$(echo "$TOOL_INPUT" | jq -r '.file_path // ""')
    CONTENT=$(echo "$TOOL_INPUT" | jq -r '.new_string // .content // ""')
    ;;
  *)
    COMMAND=$(echo "$TOOL_INPUT" | jq -r 'to_entries | map(.value) | join(" ")' 2>/dev/null || echo "")
    ;;
esac

# Build evaluation request
EVENT_ID=$(uuidgen 2>/dev/null || cat /proc/sys/kernel/random/uuid 2>/dev/null || echo "cc-$(date +%s)")

EVAL_REQUEST=$(jq -n \
  --arg event_id "$EVENT_ID" \
  --arg tool "$TOOL_NAME" \
  --arg command "$COMMAND" \
  --arg content "${CONTENT:-}" \
  '{
    event_id: $event_id,
    tool: $tool,
    args: {
      command: $command,
      content: $content
    },
    fields: {
      event_type: "tool_call",
      tool: $tool,
      command: $command,
      content: $content
    }
  }'
)

# Call AgentShield engine
if [ -z "$AGENTSHIELD_AUTH_TOKEN" ]; then
  # Missing auth token — fail open to avoid breaking workflows, but emit warning for operator.
  >&2 echo "[AgentShield hook] AGENTSHIELD_AUTH_TOKEN is not set; request may be rejected by engine auth"
fi

RESPONSE=$(curl -s --max-time 2 \
  -X POST "${AGENTSHIELD_URL}/api/v1/evaluate" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${AGENTSHIELD_AUTH_TOKEN}" \
  -d "$EVAL_REQUEST" 2>/dev/null) || {
  # Engine unreachable — apply configured fail policy
  if [ "$AGENTSHIELD_FAIL_POLICY" = "block" ]; then
    jq -n '{"decision": "block", "reason": "AgentShield unavailable (fail-closed policy)"}'
    exit 2
  fi
  exit 0
}

# Parse response
ACTION=$(echo "$RESPONSE" | jq -r '.action // "allow"')

case "$ACTION" in
  block)
    # Extract alert details for the block reason
    RULE_NAME=$(echo "$RESPONSE" | jq -r '.alerts[0].rule_name // "Unknown rule"')
    SEVERITY=$(echo "$RESPONSE" | jq -r '.alerts[0].severity // "unknown"')
    ALERT_COUNT=$(echo "$RESPONSE" | jq -r '.alerts | length')
    
    REASON="AgentShield blocked: ${RULE_NAME} (${SEVERITY})"
    if [ "$ALERT_COUNT" -gt 1 ]; then
      REASON="${REASON} (+$((ALERT_COUNT - 1)) more alerts)"
    fi
    
    # Output block decision for Claude Code
    jq -n --arg reason "$REASON" '{"decision": "block", "reason": $reason}'
    exit 2
    ;;
  log)
    # Audit mode — allow but log
    exit 0
    ;;
  *)
    # Allow
    exit 0
    ;;
esac
