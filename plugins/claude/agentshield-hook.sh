#!/usr/bin/env bash
# AgentShield hook for Claude Code (shell fallback)
#
# Prefer the HTTP hook integration (see README.md). This shell script is a
# fallback for environments where direct HTTP hooks cannot be used.
#
# Install: Add to ~/.claude/settings.json under hooks.PreToolUse
# Requires: AgentShield engine running (default: http://127.0.0.1:8433)
#
# Exit codes:
#   0 = allow  (stdout may contain hookSpecificOutput JSON)
#   2 = block  (stdout contains hookSpecificOutput with deny decision)
#
# Environment:
#   AGENTSHIELD_URL         - Engine URL (default: http://127.0.0.1:8433)
#   AGENTSHIELD_AUTH_TOKEN  - Engine auth token (required when auth is enabled)

set -euo pipefail

AGENTSHIELD_URL="${AGENTSHIELD_URL:-http://127.0.0.1:8433}"
AGENTSHIELD_AUTH_TOKEN="${AGENTSHIELD_AUTH_TOKEN:-}"

# Read JSON input from stdin (Claude Code passes tool call context)
INPUT=$(cat)

# Extract hook event name — dispatch accordingly
HOOK_EVENT=$(echo "$INPUT" | jq -r '.hook_event_name // "PreToolUse"')

# For non-PreToolUse events, allow immediately
if [ "$HOOK_EVENT" != "PreToolUse" ]; then
  exit 0
fi

# Extract tool name, input, session, and cwd from Claude Code's hook schema
TOOL_NAME=$(echo "$INPUT" | jq -r '.tool_name // "unknown"')
TOOL_INPUT=$(echo "$INPUT" | jq -c '.tool_input // {}')
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // ""')
CWD=$(echo "$INPUT" | jq -r '.cwd // ""')

# Build command string for rule matching
COMMAND=""
CONTENT=""
case "$TOOL_NAME" in
  Bash|bash)
    COMMAND=$(echo "$TOOL_INPUT" | jq -r '.command // ""')
    ;;
  Write|write)
    COMMAND=$(echo "$TOOL_INPUT" | jq -r '.file_path // ""')
    CONTENT=$(echo "$TOOL_INPUT" | jq -r '.content // .file_text // ""')
    ;;
  Edit|edit)
    COMMAND=$(echo "$TOOL_INPUT" | jq -r '.file_path // ""')
    CONTENT=$(echo "$TOOL_INPUT" | jq -r '.new_string // .new_str // ""')
    ;;
  *)
    COMMAND=$(echo "$TOOL_INPUT" | jq -r 'to_entries | map(.value | tostring) | join(" ")' 2>/dev/null || echo "")
    ;;
esac

# Build evaluation request
EVENT_ID=$(uuidgen 2>/dev/null || cat /proc/sys/kernel/random/uuid 2>/dev/null || echo "cc-$(date +%s)")

EVAL_REQUEST=$(jq -n \
  --arg event_id "$EVENT_ID" \
  --arg session_id "$SESSION_ID" \
  --arg tool "$TOOL_NAME" \
  --arg command "$COMMAND" \
  --arg content "$CONTENT" \
  --arg working_dir "$CWD" \
  '{
    event_id: $event_id,
    session_id: $session_id,
    tool: $tool,
    source: "claude-code",
    args: {
      command: $command,
      content: $content
    },
    fields: {
      event_type: "tool_call",
      tool: $tool,
      command: $command,
      content: $content,
      working_dir: $working_dir,
      source: "claude-code"
    }
  }'
)

# Build curl auth header only if token is set
AUTH_HEADER=()
if [ -n "$AGENTSHIELD_AUTH_TOKEN" ]; then
  AUTH_HEADER=(-H "Authorization: Bearer ${AGENTSHIELD_AUTH_TOKEN}")
fi

# Call AgentShield engine
RESPONSE=$(curl -s --max-time 2 \
  -X POST "${AGENTSHIELD_URL}/api/v1/evaluate" \
  -H "Content-Type: application/json" \
  "${AUTH_HEADER[@]}" \
  -d "$EVAL_REQUEST" 2>/dev/null) || {
  # Engine unreachable — fail open (allow)
  exit 0
}

# Parse response action
ACTION=$(echo "$RESPONSE" | jq -r '.action // "allow"')

case "$ACTION" in
  block)
    RULE_NAME=$(echo "$RESPONSE" | jq -r '.alerts[0].rule_name // "Unknown rule"')
    SEVERITY=$(echo "$RESPONSE" | jq -r '.alerts[0].severity // "unknown"')
    ALERT_COUNT=$(echo "$RESPONSE" | jq -r '.alerts | length')
    REASON="AgentShield: ${RULE_NAME} (${SEVERITY})"
    if [ "$ALERT_COUNT" -gt 1 ]; then
      REASON="${REASON} (+$((ALERT_COUNT - 1)) more)"
    fi
    jq -n --arg reason "$REASON" '{
      hookSpecificOutput: {
        hookEventName: "PreToolUse",
        permissionDecision: "deny",
        permissionDecisionReason: $reason
      }
    }'
    exit 2
    ;;
  require_approval)
    RULE_NAME=$(echo "$RESPONSE" | jq -r '.alerts[0].rule_name // "Unknown rule"')
    SEVERITY=$(echo "$RESPONSE" | jq -r '.alerts[0].severity // "unknown"')
    REASON="AgentShield: ${RULE_NAME} (${SEVERITY}) — approval required"
    jq -n --arg reason "$REASON" '{
      hookSpecificOutput: {
        hookEventName: "PreToolUse",
        permissionDecision: "ask",
        permissionDecisionReason: $reason
      }
    }'
    exit 0
    ;;
  *)
    # allow / log — proceed
    exit 0
    ;;
esac
