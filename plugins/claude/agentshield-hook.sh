#!/usr/bin/env bash
# AgentShield dispatcher hook for Claude Code.
#
# A single entry-point registered against multiple Claude Code hook events.
# It branches on the `hook_event_name` field of the stdin JSON:
#
#   PreToolUse              -> synchronous evaluate; block via exit 2 / decision
#   UserPromptSubmit        -> evaluate the prompt (user_input); notify, never block
#   PostToolUse             -> fire-and-forget audit report (correlated)
#   PostToolUseFailure      -> fire-and-forget audit report (is_error=true)
#   SessionStart            -> lifecycle session_start + non-blocking health log
#   Stop / SessionEnd       -> lifecycle session_end
#   SubagentStop            -> lifecycle session_end (subagent scope)
#
# Pipeline (PreToolUse): skip -> intercept -> breaker -> evaluate -> act.
# Post-tool audit and lifecycle are fire-and-forget. Startup health runs
# (non-blocking) on SessionStart.
#
# Exit codes (PreToolUse):
#   0 = allow (no output, or {"decision":"approve"})
#   2 = block (emits {"decision":"block","reason":"..."} and stderr text)
# All other events always exit 0 (they never block).
#
# Environment (see README "Configuration"):
#   AGENTSHIELD_ENABLED          (default true)
#   AGENTSHIELD_URL              base URL (default http://127.0.0.1:8433)
#   AGENTSHIELD_ENDPOINT         full evaluate URL (alternative to URL)
#   AGENTSHIELD_AUTH_TOKEN       bearer token (required when engine auth is on)
#   AGENTSHIELD_TIMEOUT_MS       5..5000 (default 2000)
#   AGENTSHIELD_TIMEOUT_POLICY   allow|block|log (default allow -- see README)
#   AGENTSHIELD_NOTIFY           all|high|critical|none (default high)
#   AGENTSHIELD_SKIP             space-separated skip list
#   AGENTSHIELD_INTERCEPT        space-separated intercept list (empty = all)
#   AGENTSHIELD_FAILURE_THRESHOLD       consecutive failures to open (default 3)
#   AGENTSHIELD_RECOVERY_INTERVAL_MS    half-open after this (default 30000)
# Note: the evaluation mode (enforce/audit/shadow) is engine-side config; it is
# not a connector setting and is not sent on the request.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=agentshield-lib.sh
. "${SCRIPT_DIR}/agentshield-lib.sh"

# Require jq and curl; without them we fail open silently (allow).
if ! command -v jq >/dev/null 2>&1 || ! command -v curl >/dev/null 2>&1; then
  >&2 echo "[AgentShield] jq and curl are required; allowing tool call"
  exit 0
fi

INPUT="$(cat)"
EVENT="$(printf '%s' "$INPUT" | jq -r '.hook_event_name // empty')"
SESSION_ID="$(printf '%s' "$INPUT" | jq -r '.session_id // empty')"
AGENT_ID="$(printf '%s' "$INPUT" | jq -r '.agent_id // empty')"
WORKING_DIR="$(printf '%s' "$INPUT" | jq -r '.cwd // empty')"

# Globally disabled: do nothing.
if [ "$(as_enabled)" = "false" ]; then
  exit 0
fi

# JSON value or null helper.
as__json_or_null() {
  if [ -z "$1" ]; then printf 'null'; else jq -n --arg v "$1" '$v'; fi
}

# -------------------------------------------------------------------------
# Apply timeout / fail-mode policy for PreToolUse. Echoes nothing; sets exit.
# -------------------------------------------------------------------------
as_apply_pretool_policy() {
  local reason="$1" policy
  policy="$(as_timeout_policy)"
  case "$policy" in
    log|allow)
      # fail-open: allow execution.
      >&2 echo "[AgentShield] ${reason} -- allowing (fail-open: ${policy})"
      exit 0
      ;;
    block|*)
      # Anything not explicitly recognised as fail-open blocks. as_timeout_policy
      # already resolves unknown values to "block"; this arm keeps the safe
      # branch as the default here too, so a future policy value added in one
      # place cannot silently become fail-open in the other.
      jq -n --arg reason "AgentShield unavailable (fail-closed policy): ${reason}" \
        '{decision: "block", reason: $reason}'
      >&2 echo "[AgentShield] ${reason} -- blocking (fail-closed)"
      exit 2
      ;;
  esac
}

# =========================================================================
# PreToolUse: synchronous evaluation
# =========================================================================
handle_pre_tool_use() {
  local tool_name tool_input canonical command event_type event_id
  tool_name="$(printf '%s' "$INPUT" | jq -r '.tool_name // .toolName // "unknown"')"
  tool_input="$(printf '%s' "$INPUT" | jq -c '.tool_input // .input // {}')"

  canonical="$(as_normalise_name "$tool_name")"

  # 1+2. Skip check.
  if as_should_skip "$tool_name" "$canonical"; then
    exit 0
  fi
  # 3. Intercept filter.
  if ! as_should_intercept "$tool_name" "$canonical"; then
    exit 0
  fi

  # 4. Circuit-breaker gate.
  case "$(as_breaker_state)" in
    open)
      as_apply_pretool_policy "circuit breaker open"
      ;;
    *) ;; # closed / half-open: proceed (half-open lets a single probe through).
  esac

  command="$(as_build_command "$canonical" "$tool_name" "$tool_input")"
  event_type="$(as_event_type "$canonical")"
  event_id="$(as_uuid)"

  # 5. Build request (canonical OpenClaw/Hermes envelope, spec 1.1).
  # Note: the evaluation mode (enforce/audit/shadow) is engine-side config and
  # is NOT carried on the request — the EvaluationRequest model has no such
  # field, so there is no per-call override from the connector.
  local request
  request="$(jq -n \
    --arg event_id "$event_id" \
    --arg timestamp "$(as_now_iso)" \
    --arg event_type "$event_type" \
    --arg tool_name "$canonical" \
    --arg source "$AS_SOURCE" \
    --arg command "$command" \
    --argjson params "$tool_input" \
    --argjson agent_id "$(as__json_or_null "$AGENT_ID")" \
    --argjson session_id "$(as__json_or_null "$SESSION_ID")" \
    --argjson working_dir "$(as__json_or_null "$WORKING_DIR")" \
    '{
      event_id: $event_id,
      timestamp: $timestamp,
      event_type: $event_type,
      tool_name: $tool_name,
      source: $source,
      command: (if $command == "" then null else $command end),
      params: ($params + (if $command == "" then {} else {command: $command} end)),
      agent_id: $agent_id,
      session_id: $session_id,
      working_dir: $working_dir,
      data: {}
    }')"

  # 6. Evaluate (synchronous, timeout-bounded).
  local response curl_rc action
  response="$(as_post_sync "$(as_api_root)/evaluate" "$request")"
  curl_rc=$?

  if [ "$curl_rc" -ne 0 ] || [ -z "$response" ]; then
    as_record_failure
    as_apply_pretool_policy "engine unreachable"
  fi

  action="$(printf '%s' "$response" | jq -r '.action // empty' 2>/dev/null)"

  # Validate action ∈ {allow, block, log, require_approval}.
  case "$action" in
    allow|block|log|require_approval) ;;
    *)
      as_record_failure
      as_apply_pretool_policy "invalid engine response"
      ;;
  esac

  as_record_success

  local reason triage_reason
  case "$action" in
    block)
      reason="$(printf '%s' "$response" | jq -r '.reason // empty')"
      [ -z "$reason" ] && reason="Blocked by AgentShield"
      triage_reason="$(printf '%s' "$response" | jq -r '.triage_results[0].reasoning // empty')"
      [ -n "$triage_reason" ] && reason="${reason} (Triage: ${triage_reason})"
      jq -n --arg reason "$reason" '{decision: "block", reason: $reason}'
      >&2 echo "[AgentShield] blocked ${tool_name}: ${reason}"
      exit 2
      ;;
    require_approval)
      # Fail-closed: Claude Code's PreToolUse hook has no interactive approval
      # prompt, so treat require_approval exactly like block (spec 3.2).
      reason="$(printf '%s' "$response" | jq -r '.reason // empty')"
      [ -z "$reason" ] && reason="AgentShield requires explicit approval before this tool call can proceed"
      jq -n --arg reason "$reason" '{decision: "block", reason: $reason}'
      >&2 echo "[AgentShield] approval required for ${tool_name}: ${reason}"
      exit 2
      ;;
    log)
      # Allow, but optionally surface a non-blocking notification.
      as_emit_notification "$response" "$tool_name" "logged"
      as_corr_push "$SESSION_ID" "$canonical" "$event_id"
      exit 0
      ;;
    allow|*)
      as_emit_notification "$response" "$tool_name" "allowed"
      as_corr_push "$SESSION_ID" "$canonical" "$event_id"
      exit 0
      ;;
  esac
}

# Emit a non-blocking systemMessage if alerts meet the notify threshold.
as_emit_notification() {
  local response="$1" tool_name="$2" verb="$3" notify threshold top_sev sev_rank
  notify="$(as_notify)"
  [ "$notify" = "none" ] && return 0

  local alert_count
  alert_count="$(printf '%s' "$response" | jq -r '(.alerts // []) | length')"
  [ "$alert_count" = "0" ] && return 0

  top_sev="$(printf '%s' "$response" | jq -r '.alerts[0].severity // "low"')"
  case "$top_sev" in
    critical) sev_rank=3 ;;
    high) sev_rank=2 ;;
    medium) sev_rank=1 ;;
    *) sev_rank=0 ;;
  esac
  case "$notify" in
    all) threshold=0 ;;
    high) threshold=2 ;;
    critical) threshold=3 ;;
    *) threshold=2 ;;
  esac
  [ "$sev_rank" -lt "$threshold" ] && return 0

  local rule_name msg
  rule_name="$(printf '%s' "$response" | jq -r '.alerts[0].rule_name // "unknown rule"')"
  msg="AgentShield ${verb} ${tool_name}: ${rule_name} (${top_sev})"
  if [ "$alert_count" -gt 1 ]; then
    msg="${msg} (+$((alert_count - 1)) more)"
  fi
  jq -n --arg m "$msg" '{systemMessage: $m, suppressOutput: true}'
}

# =========================================================================
# PostToolUse / PostToolUseFailure: fire-and-forget audit
# =========================================================================
handle_post_tool_use() {
  local is_error="$1"
  local tool_name tool_input canonical corr_id result_summary error_message report

  tool_name="$(printf '%s' "$INPUT" | jq -r '.tool_name // "unknown"')"
  tool_input="$(printf '%s' "$INPUT" | jq -c '.tool_input // {}')"
  canonical="$(as_normalise_name "$tool_name")"

  corr_id="$(as_corr_pop "$SESSION_ID" "$canonical")"
  [ -z "$corr_id" ] && corr_id="$(as_uuid)"

  # tool_response may be a string or an object; stringify and truncate to 1000.
  result_summary="$(printf '%s' "$INPUT" | jq -r '
    .tool_response
    | if . == null then ""
      elif type == "string" then .
      else (tostring) end' 2>/dev/null)"
  result_summary="${result_summary:0:$MAX_RESULT_SUMMARY_LEN}"

  if [ "$is_error" = "true" ]; then
    error_message="$result_summary"
  else
    # Heuristic: a textual result beginning with "Error" counts as an error.
    case "$result_summary" in
      Error*) is_error="true"; error_message="$result_summary" ;;
      *) error_message="" ;;
    esac
  fi

  report="$(jq -n \
    --arg event_id "$(as_uuid)" \
    --arg correlation_id "$corr_id" \
    --arg timestamp "$(as_now_iso)" \
    --arg tool_name "$canonical" \
    --arg source "$AS_SOURCE" \
    --arg result_summary "$result_summary" \
    --argjson is_error "$is_error" \
    --argjson agent_id "$(as__json_or_null "$AGENT_ID")" \
    --argjson session_id "$(as__json_or_null "$SESSION_ID")" \
    --argjson working_dir "$(as__json_or_null "$WORKING_DIR")" \
    --arg error_message "$error_message" \
    '{
      event_id: $event_id,
      correlation_id: $correlation_id,
      timestamp: $timestamp,
      event_type: "tool_result",
      tool_name: $tool_name,
      source: $source,
      result_summary: (if $result_summary == "" then null else $result_summary end),
      is_error: $is_error,
      error_message: (if $error_message == "" then null else $error_message end),
      duration_ms: 0,
      agent_id: $agent_id,
      session_id: $session_id,
      working_dir: $working_dir,
      data: {}
    }')"

  as_post_async "$(as_api_root)/audit" "$report"
  exit 0
}

# =========================================================================
# Lifecycle: session_start / session_end
# =========================================================================
handle_lifecycle() {
  local lifecycle_type="$1" event
  event="$(jq -n \
    --arg event_id "$(as_uuid)" \
    --arg timestamp "$(as_now_iso)" \
    --arg event_type "$lifecycle_type" \
    --arg source "$AS_SOURCE" \
    --argjson agent_id "$(as__json_or_null "$AGENT_ID")" \
    --argjson session_id "$(as__json_or_null "$SESSION_ID")" \
    '{
      event_id: $event_id,
      timestamp: $timestamp,
      event_type: $event_type,
      source: $source,
      agent_id: $agent_id,
      session_id: $session_id,
      data: {}
    }')"
  as_post_async "$(as_api_root)/lifecycle" "$event"

  # On session start, run a non-blocking startup health probe.
  if [ "$lifecycle_type" = "session_start" ]; then
    ( if as_health_ok; then
        >&2 echo "[AgentShield] engine reachable at $(as_base_url)"
      else
        >&2 echo "[AgentShield] engine not reachable at $(as_base_url) (will retry on first tool call)"
      fi ) &
    disown 2>/dev/null || true
  fi
  exit 0
}

# =========================================================================
# UserPromptSubmit: evaluate the user's prompt for direct prompt injection and
# semantic manipulation (authority hijacking, urgency pressure). Detection-only —
# it NEVER blocks the prompt (always exit 0); on alerts meeting the notify
# threshold it emits a non-blocking systemMessage. The prompt text is carried in
# params.message, which the engine copies into the `message` field the
# user_input rules match on.
# =========================================================================
handle_user_prompt_submit() {
  local prompt event_id request response
  prompt="$(printf '%s' "$INPUT" | jq -r '.prompt // .user_prompt // empty')"
  [ -z "$prompt" ] && exit 0

  # If the breaker is open, skip (fail open) so a degraded engine never delays
  # prompt submission.
  case "$(as_breaker_state)" in
    open) exit 0 ;;
    *) ;;
  esac

  event_id="$(as_uuid)"
  request="$(jq -n \
    --arg event_id "$event_id" \
    --arg timestamp "$(as_now_iso)" \
    --arg source "$AS_SOURCE" \
    --arg message "$prompt" \
    --argjson agent_id "$(as__json_or_null "$AGENT_ID")" \
    --argjson session_id "$(as__json_or_null "$SESSION_ID")" \
    --argjson working_dir "$(as__json_or_null "$WORKING_DIR")" \
    '{
      event_id: $event_id,
      timestamp: $timestamp,
      event_type: "user_input",
      tool_name: "user_prompt",
      source: $source,
      params: {message: $message},
      agent_id: $agent_id,
      session_id: $session_id,
      working_dir: $working_dir,
      data: {}
    }')"

  response="$(as_post_sync "$(as_api_root)/evaluate" "$request")"
  if [ $? -ne 0 ] || [ -z "$response" ]; then
    as_record_failure
    exit 0  # fail open: never disrupt prompt submission
  fi
  as_record_success

  # Detection-only: surface a non-blocking notification, never block the prompt.
  as_emit_notification "$response" "user prompt" "flagged"
  exit 0
}

# =========================================================================
# Dispatch
# =========================================================================
case "$EVENT" in
  PreToolUse)          handle_pre_tool_use ;;
  UserPromptSubmit)    handle_user_prompt_submit ;;
  PostToolUse)         handle_post_tool_use "false" ;;
  PostToolUseFailure)  handle_post_tool_use "true" ;;
  SessionStart)        handle_lifecycle "session_start" ;;
  Stop|SessionEnd)     handle_lifecycle "session_end" ;;
  SubagentStop)        handle_lifecycle "session_end" ;;
  *)
    # Unknown / unhandled event: do nothing, allow.
    exit 0
    ;;
esac
