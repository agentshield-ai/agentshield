#!/usr/bin/env bash
# AgentShield shared library for the Claude Code connector.
#
# This file is *sourced* by agentshield-hook.sh. It provides:
#   - configuration loading (env vars, with documented defaults),
#   - canonical tool-name normalisation mirroring plugins/hermes/normalise.py
#     and plugins/openclaw/src/normalise.ts,
#   - HTTP helpers (evaluate / audit / lifecycle / health),
#   - a best-effort consecutive-failure short-circuit backed by a temp state
#     file (a pragmatic substitute for the full circuit breaker used by the
#     TypeScript and Python connectors -- see README "Circuit breaker").
#
# It deliberately has no side effects on source: callers invoke the functions.
#
# Constants from the connector contract specification (X-AgentShield-Version):
#   CONTRACT_VERSION       = 1.0.0
#   MAX_RESULT_SUMMARY_LEN = 1000

# shellcheck disable=SC2034  # several constants are consumed by the dispatcher.

CONTRACT_VERSION="1.0.0"
MAX_RESULT_SUMMARY_LEN=1000
AS_SOURCE="claude_code"

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
#
# All configuration is read from the environment. Defaults match the shared
# connector contract (timeout range 5-5000 ms, etc.) except the fail-mode
# default, which is documented as a deliberate divergence for this local-dev
# hook (see as_timeout_policy).

# Engine base URL. The contract derives every endpoint from the evaluate URL,
# but Claude Code historically configures a base URL, so we accept both:
#   - AGENTSHIELD_URL       -> base (e.g. http://127.0.0.1:8433)
#   - AGENTSHIELD_ENDPOINT  -> full evaluate URL (…/api/v1/evaluate)
as_base_url() {
  local ep="${AGENTSHIELD_ENDPOINT:-}"
  if [ -n "$ep" ]; then
    # Strip a trailing /api/v1/evaluate (or /evaluate) to recover the base.
    ep="${ep%/}"
    ep="${ep%/evaluate}"
    ep="${ep%/api/v1}"
    printf '%s' "$ep"
    return
  fi
  printf '%s' "${AGENTSHIELD_URL:-http://127.0.0.1:8433}"
}

as_api_root() {
  printf '%s/api/v1' "$(as_base_url)"
}

# Auth token: explicit env var only (never hardcoded). Empty => no header.
as_auth_token() {
  printf '%s' "${AGENTSHIELD_AUTH_TOKEN:-}"
}

# Timeout in milliseconds, clamped to the contract range [5, 5000].
# curl works in seconds, so the dispatcher converts as needed.
as_timeout_ms() {
  local t="${AGENTSHIELD_TIMEOUT_MS:-2000}"
  case "$t" in
    ''|*[!0-9]*) t=2000 ;;
  esac
  if [ "$t" -lt 5 ]; then t=5; fi
  if [ "$t" -gt 5000 ]; then t=5000; fi
  printf '%s' "$t"
}

# curl --max-time wants a (possibly fractional) seconds value.
as_timeout_secs() {
  local ms
  ms="$(as_timeout_ms)"
  awk -v ms="$ms" 'BEGIN { printf "%.3f", ms / 1000.0 }'
}

# Fail-mode policy applied when the engine is unreachable / errors, or when
# the failure short-circuit is open. One of: allow | block | log.
#
# DIVERGENCE: the shared contract default is "block" (fail-closed). This hook
# defaults to "allow" (fail-open) so that a missing or crashed local engine
# never wedges an interactive Claude Code session. Operators who want
# fail-closed parity with OpenClaw/Hermes set AGENTSHIELD_TIMEOUT_POLICY=block.
#
# The divergence is in the default only. An explicitly set but unrecognised
# value resolves to "block", matching every other connector, because a typo in
# a security control must not silently select the weakest behaviour: an
# operator who wrote AGENTSHIELD_TIMEOUT_POLICY=blok was asking for
# fail-closed, and resolving that to fail-open would disable enforcement with
# no indication. The coercion is announced on stderr so the typo is findable.
as_timeout_policy() {
  if [ -z "${AGENTSHIELD_TIMEOUT_POLICY:-}" ]; then
    printf 'allow'
    return
  fi
  case "$AGENTSHIELD_TIMEOUT_POLICY" in
    allow|block|log) printf '%s' "$AGENTSHIELD_TIMEOUT_POLICY" ;;
    *)
      >&2 echo "[AgentShield] invalid AGENTSHIELD_TIMEOUT_POLICY" \
        "'${AGENTSHIELD_TIMEOUT_POLICY}' -- falling back to 'block' (fail-closed)."
      printf 'block'
      ;;
  esac
}

# Notify threshold: all | high | critical | none. Controls whether a
# non-blocking systemMessage is surfaced for log/allow alerts.
as_notify() {
  local n="${AGENTSHIELD_NOTIFY:-high}"
  case "$n" in
    all|high|critical|none) printf '%s' "$n" ;;
    *) printf 'high' ;;
  esac
}

# Consecutive-failure threshold before the short-circuit opens.
as_failure_threshold() {
  local t="${AGENTSHIELD_FAILURE_THRESHOLD:-3}"
  case "$t" in
    ''|*[!0-9]*) t=3 ;;
  esac
  if [ "$t" -lt 1 ]; then t=3; fi
  printf '%s' "$t"
}

# Recovery interval (ms) after which the short-circuit half-opens.
as_recovery_interval_ms() {
  local t="${AGENTSHIELD_RECOVERY_INTERVAL_MS:-30000}"
  case "$t" in
    ''|*[!0-9]*) t=30000 ;;
  esac
  if [ "$t" -lt 1000 ]; then t=30000; fi
  printf '%s' "$t"
}

# Space-separated skip list (platform or canonical names). Low-risk / noisy.
as_skip_list() {
  printf '%s' "${AGENTSHIELD_SKIP:-TodoWrite todo memory_search Glob Grep LS NotebookRead}"
}

# Space-separated intercept list. Empty => evaluate everything not skipped.
as_intercept_list() {
  printf '%s' "${AGENTSHIELD_INTERCEPT:-}"
}

# Enabled flag.
as_enabled() {
  local e="${AGENTSHIELD_ENABLED:-true}"
  case "$e" in
    false|0|no|off) printf 'false' ;;
    *) printf 'true' ;;
  esac
}

# Directory for transient connector state (correlation ids, failure counter).
as_state_dir() {
  local d="${AGENTSHIELD_STATE_DIR:-${TMPDIR:-/tmp}/agentshield-claude}"
  mkdir -p "$d" 2>/dev/null || true
  printf '%s' "$d"
}

# ---------------------------------------------------------------------------
# Canonical tool-name normalisation
# ---------------------------------------------------------------------------
#
# Mirrors the alias table in plugins/hermes/normalise.py, extended with the
# Claude Code built-in tool names (Bash, Write, Edit, Read, WebFetch, ...).
# Unknown tools pass through unchanged (returned as their own canonical name).

as_normalise_name() {
  local name="$1"
  case "$name" in
    # exec
    terminal|execute_command|run_command|shell|bash|Bash|computer) printf 'exec' ;;
    # write
    write_file|create_file|save_file|Write|create) printf 'write' ;;
    # read
    read_file|view_file|Read|cat|file_read|NotebookRead) printf 'read' ;;
    # edit
    edit_file|patch_file|replace_in_file|Edit|MultiEdit|NotebookEdit|str_replace_editor) printf 'edit' ;;
    # browser
    web_browse|browser|navigate|browse_url|web_browser|WebFetch) printf 'browser' ;;
    # message
    send_message|telegram_send|discord_send|slack_send|whatsapp_send|signal_send) printf 'message' ;;
    # sessions_spawn
    delegate|spawn_agent|create_subagent|Agent|Task) printf 'sessions_spawn' ;;
    # code_execute
    code_execute|python_execute|run_python) printf 'code_execute' ;;
    # web_search
    web_search|search|WebSearch) printf 'web_search' ;;
    # image_generate
    image_generate|generate_image) printf 'image_generate' ;;
    # vision
    vision|analyze_image) printf 'vision' ;;
    # tts
    text_to_speech|tts) printf 'tts' ;;
    # memory
    memory_add|memory_search|memory_delete) printf 'memory' ;;
    # planning
    task_plan|todo|TodoWrite) printf 'planning' ;;
    # cron
    cron_add|cron_remove|cron_list) printf 'cron' ;;
    # unknown: pass through unchanged
    *) printf '%s' "$name" ;;
  esac
}

# Semantic event_type for a canonical name (connector vocabulary, spec 2.3).
as_event_type() {
  local canonical="$1"
  case "$canonical" in
    read) printf 'file_read' ;;
    write) printf 'file_write' ;;
    edit) printf 'file_edit' ;;
    sessions_spawn) printf 'session_spawn' ;;
    *) printf 'tool_call' ;;
  esac
}

# Build a human-readable command string for Sigma matching (spec 2.2).
# Args: <canonical> <original_name> <tool_input_json>
# Echoes the command string (may be empty).
as_build_command() {
  local canonical="$1" original="$2" input="$3"
  local val path
  case "$canonical" in
    exec)
      val="$(printf '%s' "$input" | jq -r '.command // .cmd // empty')"
      printf '%s' "$val"
      ;;
    write|read|edit)
      path="$(printf '%s' "$input" | jq -r '.path // .file_path // .filepath // .filename // .file // empty')"
      [ -z "$path" ] && path="<unknown>"
      # Capitalise first letter of the canonical name.
      local label
      case "$canonical" in
        write) label="Write" ;;
        read) label="Read" ;;
        edit) label="Edit" ;;
      esac
      printf '%s: %s' "$label" "$path"
      ;;
    browser)
      local action url
      action="$(printf '%s' "$input" | jq -r '.action // "navigate"')"
      url="$(printf '%s' "$input" | jq -r '.url // empty')"
      # Trim trailing space when url is empty.
      if [ -n "$url" ]; then
        printf '%s: %s' "$action" "$url"
      else
        printf '%s:' "$action"
      fi
      ;;
    message)
      local channel platform
      channel="$(printf '%s' "$input" | jq -r '.channel // .chat_id // .to // empty')"
      if [ -n "$channel" ]; then
        printf 'Message to %s' "$channel"
      else
        platform="${original/_send/}"; platform="${platform/send_/}"
        printf 'Message via %s' "$platform"
      fi
      ;;
    sessions_spawn)
      local agent
      agent="$(printf '%s' "$input" | jq -r '.agent_id // .agent // .task // .agentId // empty')"
      if [ -n "$agent" ]; then
        printf 'Spawn: %s' "$agent"
      else
        printf 'Spawn: <unknown>'
      fi
      ;;
    code_execute)
      local code
      code="$(printf '%s' "$input" | jq -r '.code // .script // empty')"
      if [ -n "$code" ]; then
        if [ "${#code}" -gt 200 ]; then
          code="${code:0:200}..."
        fi
        printf 'Execute: %s' "$code"
      fi
      ;;
    web_search)
      local query
      query="$(printf '%s' "$input" | jq -r '.query // .q // empty')"
      [ -n "$query" ] && printf 'Search: %s' "$query"
      ;;
    *)
      # default / unknown: use the original tool name
      printf '%s' "$original"
      ;;
  esac
}

# ---------------------------------------------------------------------------
# Skip / intercept gating
# ---------------------------------------------------------------------------
# Returns 0 (true) if the tool should be skipped entirely.
as_should_skip() {
  local original="$1" canonical="$2" word
  for word in $(as_skip_list); do
    if [ "$word" = "$original" ] || [ "$word" = "$canonical" ]; then
      return 0
    fi
  done
  return 1
}

# Returns 0 (true) if the tool should be evaluated given the intercept list.
# Empty intercept list => evaluate everything.
as_should_intercept() {
  local original="$1" canonical="$2" list word
  list="$(as_intercept_list)"
  [ -z "$list" ] && return 0
  for word in $list; do
    if [ "$word" = "$original" ] || [ "$word" = "$canonical" ]; then
      return 0
    fi
  done
  return 1
}

# ---------------------------------------------------------------------------
# Failure short-circuit (best-effort circuit-breaker substitute)
# ---------------------------------------------------------------------------
#
# A true three-state circuit breaker needs shared, concurrent-safe state. Each
# Claude Code hook invocation is a fresh short-lived process, so we approximate
# it with a temp state file holding "consecutive_failures last_failure_epoch".
#
#   closed    : failures < threshold
#   open      : failures >= threshold AND within recovery interval -> skip engine
#   half-open : failures >= threshold AND recovery interval elapsed -> probe once
#
# This is intentionally simpler than the TS/Python breaker (no atomic CAS, no
# half-open single-probe guarantee under concurrency). The README documents the
# divergence rather than overstating parity.

as__failure_file() {
  printf '%s/failures.state' "$(as_state_dir)"
}

# Echoes "open", "half-open", or "closed".
as_breaker_state() {
  local f failures last now threshold recovery_s
  f="$(as__failure_file)"
  if [ ! -f "$f" ]; then
    printf 'closed'; return
  fi
  read -r failures last < "$f" 2>/dev/null || { printf 'closed'; return; }
  case "$failures" in ''|*[!0-9]*) failures=0 ;; esac
  case "$last" in ''|*[!0-9]*) last=0 ;; esac
  threshold="$(as_failure_threshold)"
  if [ "$failures" -lt "$threshold" ]; then
    printf 'closed'; return
  fi
  now="$(date +%s)"
  recovery_s=$(( $(as_recovery_interval_ms) / 1000 ))
  if [ $(( now - last )) -ge "$recovery_s" ]; then
    printf 'half-open'
  else
    printf 'open'
  fi
}

as_record_success() {
  printf '0 0\n' > "$(as__failure_file)" 2>/dev/null || true
}

as_record_failure() {
  local f failures last
  f="$(as__failure_file)"
  failures=0
  if [ -f "$f" ]; then
    read -r failures last < "$f" 2>/dev/null || failures=0
    case "$failures" in ''|*[!0-9]*) failures=0 ;; esac
  fi
  failures=$(( failures + 1 ))
  printf '%s %s\n' "$failures" "$(date +%s)" > "$f" 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# Correlation tracker (PreToolUse event_id -> PostToolUse correlation_id)
# ---------------------------------------------------------------------------
# FIFO per (session, canonical tool). Stored as a newline list in a temp file.

as__corr_file() {
  local session="$1" canonical="$2" safe
  # Sanitise to a filesystem-safe key.
  safe="$(printf '%s_%s' "$session" "$canonical" | tr -c 'A-Za-z0-9_.-' '_')"
  printf '%s/corr_%s' "$(as_state_dir)" "$safe"
}

as_corr_push() {
  local session="$1" canonical="$2" event_id="$3"
  printf '%s\n' "$event_id" >> "$(as__corr_file "$session" "$canonical")" 2>/dev/null || true
}

# Pops (FIFO) the oldest event_id; echoes it (empty if none).
as_corr_pop() {
  local session="$1" canonical="$2" f first rest
  f="$(as__corr_file "$session" "$canonical")"
  [ -f "$f" ] || { printf ''; return; }
  first="$(head -n1 "$f" 2>/dev/null)"
  rest="$(tail -n +2 "$f" 2>/dev/null)"
  if [ -n "$rest" ]; then
    printf '%s\n' "$rest" > "$f" 2>/dev/null || true
  else
    rm -f "$f" 2>/dev/null || true
  fi
  printf '%s' "$first"
}

# ---------------------------------------------------------------------------
# UUID / time helpers
# ---------------------------------------------------------------------------
as_uuid() {
  uuidgen 2>/dev/null \
    || cat /proc/sys/kernel/random/uuid 2>/dev/null \
    || printf 'cc-%s-%s' "$(date +%s)" "$RANDOM"
}

as_now_iso() {
  date -u +%Y-%m-%dT%H:%M:%S.000Z
}

# ---------------------------------------------------------------------------
# HTTP helpers
# ---------------------------------------------------------------------------
# Builds the standard header args into the global array AS_CURL_HEADERS.
as__set_headers() {
  AS_CURL_HEADERS=(-H "Content-Type: application/json; charset=utf-8"
                   -H "X-AgentShield-Version: ${CONTRACT_VERSION}")
  local token
  token="$(as_auth_token)"
  if [ -n "$token" ]; then
    AS_CURL_HEADERS+=(-H "Authorization: Bearer ${token}")
  fi
}

# Synchronous, timeout-bounded POST. Echoes the response body; returns curl's
# exit status. Args: <url> <json-payload>
as_post_sync() {
  local url="$1" body="$2"
  as__set_headers
  curl -s --max-time "$(as_timeout_secs)" \
    -X POST "$url" \
    "${AS_CURL_HEADERS[@]}" \
    -d "$body" 2>/dev/null
}

# Fire-and-forget POST: fully detached, never blocks the hook. Args: <url> <json>
as_post_async() {
  local url="$1" body="$2"
  as__set_headers
  ( curl -s --max-time "$(as_timeout_secs)" \
      -X POST "$url" \
      "${AS_CURL_HEADERS[@]}" \
      -d "$body" >/dev/null 2>&1 ) &
  disown 2>/dev/null || true
}

# Non-blocking health probe. Returns 0 if HTTP reachable (2xx), else 1.
as_health_ok() {
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 \
    "$(as_api_root)/health" 2>/dev/null)" || return 1
  case "$code" in
    2*) return 0 ;;
    *) return 1 ;;
  esac
}
