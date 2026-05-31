# AgentShield Connector for the OpenAI Codex CLI

A connector for the [OpenAI Codex CLI](https://developers.openai.com/codex/cli)
that intercepts agent tool calls in real time, evaluates them against
AgentShield's [Sigma](https://sigmahq.io/)-based detection engine, and **blocks**,
gates, or logs security-relevant activity before execution.

Unlike a notify-only integration, this connector uses Codex's
[lifecycle hooks](https://developers.openai.com/codex/hooks). The `PreToolUse`
hook can synchronously **deny** a tool call, so AgentShield delivers genuine
blocking enforcement (not audit-only) for the Codex CLI.

## Enforcement model

**Blocking.** Codex fires the `PreToolUse` hook *before* executing `Bash`
commands, `apply_patch` file edits, and MCP tool calls, and a hook process can
return `permissionDecision: "deny"` to stop the call. This connector maps the
engine's `block` and (fail-closed) `require_approval` verdicts onto a `deny`
decision, so dangerous operations never run. It also wires the
`PermissionRequest` hook so that when Codex escalates for approval, AgentShield
can deny outright.

The hook output schema used here (`hookSpecificOutput.hookEventName` /
`permissionDecision` / `permissionDecisionReason` for `PreToolUse`, and
`decision.behavior` for `PermissionRequest`) and the event names are taken from
the official [OpenAI Codex CLI hooks reference](https://developers.openai.com/codex/hooks).

> **Guardrail, not a complete enforcement boundary.** Per the official Codex
> documentation, hook interception is currently *incomplete*: only "simple"
> shell calls are intercepted (the newer `unified_exec` streaming-shell path is
> not fully covered), and non-shell, non-MCP tools such as `WebSearch` are not
> intercepted at all. Because Codex can often achieve equivalent work through a
> tool path the hook does not observe, a determined bypass is possible. For
> full, transport-level enforcement across **every** tool call — and a single
> chokepoint for all MCP traffic across multiple clients — deploy the
> [AgentShield MCP gateway](../mcp-gateway/) in front of the agent's MCP servers
> in addition to (or instead of) this hook.

## Data flow

```
Codex tool call
   │  (PreToolUse hook, stdin JSON)
   ▼
agentshield_hook.py ── normalise ── skip/intercept ── circuit-breaker
   │
   ▼  POST /api/v1/evaluate  (synchronous, timeout-bounded)
AgentShield engine ── Sigma rules ── triage ── action
   │
   ▼  permissionDecision: allow | deny   (stdout JSON)
Codex proceeds or blocks the tool call

After execution:  PostToolUse ──▶ POST /api/v1/audit   (fire-and-forget)
Session lifecycle: SessionStart/Stop ──▶ POST /api/v1/lifecycle
```

## Prerequisites

- A running AgentShield engine (see the [main README](../../README.md) for build
  and setup instructions).
- [OpenAI Codex CLI](https://developers.openai.com/codex/cli) with hooks support
  (the `[hooks]` table in `~/.codex/config.toml`).
- Python 3.11+ on `PATH` (the connector uses only the standard library).

## Installation

```bash
plugins/codex/install.sh
```

This copies the connector to `~/.codex/agentshield/`, prints the
`config.toml` hook snippet to paste into `~/.codex/config.toml`, and
health-checks the engine. Set the auth token for an authenticated engine:

```bash
export AGENTSHIELD_AUTH_TOKEN="your-token"
```

The hook snippet registers a single `agentshield_hook.py` against the
`PreToolUse`, `PermissionRequest`, `PostToolUse`, `SessionStart`, and `Stop`
events; the script dispatches internally on `hook_event_name`.

## Configuration

Settings are read (in precedence order) from environment variables, then a JSON
file at `~/.codex/agentshield.json` (overridable via `AGENTSHIELD_CONFIG`),
falling back to defaults. Every key has a default so the connector works out of
the box when the engine runs locally. Values, defaults, and ranges match the
Hermes and OpenClaw connectors.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `enabled` | `bool` | `true` | Enable or disable the connector entirely. |
| `endpoint` | `string` | `"http://127.0.0.1:8433/api/v1/evaluate"` | AgentShield evaluation endpoint URL. |
| `auth_token` | `string` | `""` | Bearer token sent with every request (falls back to `AGENTSHIELD_AUTH_TOKEN`). |
| `timeout_ms` | `int` | `200` | HTTP timeout for evaluation calls (valid range: 5–5000). |
| `timeout_policy` | `"allow" \| "block" \| "log"` | `"block"` | What to do when the engine is unreachable or times out. |
| `intercept` | `list[str]` | `["Bash", "apply_patch", "exec", "write", ...]` | Tool names to evaluate before execution (raw or canonical names). |
| `skip` | `list[str]` | `["todo", "memory_search", "memory_add"]` | Tool names to skip unconditionally (checked first). |
| `notify` | `"all" \| "high" \| "critical" \| "none"` | `"high"` | Minimum alert severity that surfaces a user-visible message. |
| `circuit_breaker_failure_threshold` | `int` | `3` | Consecutive failures before the circuit breaker opens. |
| `circuit_breaker_recovery_interval_ms` | `int` | `30000` | Milliseconds before a half-open probe is attempted. |

### Environment variables

| Variable | Purpose |
|----------|---------|
| `AGENTSHIELD_AUTH_TOKEN` | Bearer token (used when `auth_token` is empty). |
| `AGENTSHIELD_URL` | Engine base URL; `/api/v1/evaluate` is appended. |
| `AGENTSHIELD_ENDPOINT` | Full evaluate endpoint (overrides `AGENTSHIELD_URL`). |
| `AGENTSHIELD_TIMEOUT_MS` | Override `timeout_ms`. |
| `AGENTSHIELD_TIMEOUT_POLICY` | Override `timeout_policy`. |
| `AGENTSHIELD_NOTIFY` | Override `notify`. |
| `AGENTSHIELD_ENABLED` | Set `false`/`0` to disable. |
| `AGENTSHIELD_CONFIG` | Path to the JSON settings file. |
| `AGENTSHIELD_APPROVAL_MODE` | When truthy, surface `require_approval` to Codex's own prompt instead of denying. |
| `AGENTSHIELD_LOG_LEVEL` | Python logging level (default `WARNING`). |

## How it works

### Tool-name normalisation

Codex tool names — `Bash`, `apply_patch`, and MCP names of the form
`mcp__<server>__<tool>` — are mapped to AgentShield canonical names (`exec`,
`edit`, `read`, …) so the same Sigma rules fire across every platform. MCP tools
are normalised by their trailing tool segment, so an MCP `run_command` maps to
`exec` just like the native `Bash` tool. See [`normalise.py`](normalise.py).

### `PreToolUse` — synchronous, blocking evaluation

For each intercepted tool call the connector:

1. Normalises the tool name and builds a command string.
2. Checks the **skip** set; if listed, the call is allowed without evaluation.
3. Checks the **intercept** set; if not listed, the call is allowed.
4. Checks the **circuit breaker**; if open, applies `timeout_policy`.
5. POSTs an `EvaluationRequest` to `/api/v1/evaluate` (timeout-bounded).
6. Acts on the engine's `action`:
   - `block` → emits `permissionDecision: "deny"` (the call is blocked).
   - `require_approval` → **fails closed** and denies, unless
     `AGENTSHIELD_APPROVAL_MODE` is set, in which case it defers to Codex's own
     approval prompt.
   - `log` → allows, surfacing an alert message if it meets the `notify`
     threshold.
   - `allow` → allows.

On evaluation failure or an open circuit breaker the `timeout_policy` applies;
the default is **fail-closed** (`block`).

### `PermissionRequest` — approval gating

When Codex escalates for approval (shell escalation, network access), the
connector evaluates the call and denies (`decision.behavior: "deny"`) on a
`block`/`require_approval` verdict, otherwise defers to Codex.

### `PostToolUse` — fire-and-forget audit

After a tool runs, the connector sends an `AuditReport` to `/api/v1/audit` with
the result summary (truncated to 1000 chars), an error flag derived from
`tool_response`, and a correlation id. Because each hook is a short-lived
process, the audit POST is sent inline with a short timeout and any failure is
swallowed.

### Lifecycle and health

`SessionStart` emits a `session_start` lifecycle event and runs a non-blocking
`GET /api/v1/health`; `Stop` emits `session_end`.

### Circuit breaker

Codex runs each hook as a fresh process, so the three-state circuit breaker
persists its state to a small JSON file (`~/.codex/.agentshield-breaker.json`).
A run of failures across consecutive hook invocations still trips the breaker
and spares the engine. The state-machine semantics are identical to the
in-memory Hermes/OpenClaw breakers.

## Architecture

```
plugins/codex/
├── agentshield_hook.py   # Per-event entry-point Codex invokes (stdin → decision)
├── __init__.py           # Package metadata
├── circuit_breaker.py    # File-backed three-state circuit breaker
├── client.py             # HTTP client: evaluate, audit, lifecycle, health
├── config.py             # Config loading (env + JSON file) with validation
├── event_builder.py      # Builds EvaluationRequest, AuditReport, LifecycleEvent
├── normalise.py          # Maps Codex/MCP tool names to canonical forms
├── install.sh            # Installs the hook and prints the config.toml snippet
└── tests/
    ├── test_circuit_breaker.py
    ├── test_client.py
    ├── test_config.py
    ├── test_event_builder.py
    ├── test_hook.py
    └── test_normalise.py
```

### Design decisions

- **Blocking, not audit-only.** Codex's `PreToolUse` hook supports a synchronous
  `deny`, so AgentShield prevents dangerous calls rather than merely recording
  them after the fact.
- **Zero external dependencies.** Standard library only (`urllib`, `json`).
- **Fail-closed by default.** When the engine is unreachable the connector blocks
  (`timeout_policy: block`), matching the canonical connector contract. (The
  legacy Claude Code Bash hook deliberately fails open; this connector does not.)
- **Process-local state persisted to disk.** The circuit breaker survives across
  the per-event hook processes via an atomically written state file.

## Evaluation modes

The engine's mode (set engine-side via `AGENTSHIELD_MODE`) governs how verdicts
are produced:

- **enforce** — block on critical/high, require approval on medium, allow others.
- **audit** — log all alerts, never block or require approval.
- **shadow** — silent monitoring, no alerts surfaced.

## Tool mapping

| Codex tool | Canonical | `event_type` |
|------------|-----------|--------------|
| `Bash` | `exec` | `tool_call` |
| `apply_patch` | `edit` | `file_edit` |
| `mcp__<server>__read_file` | `read` | `file_read` |
| `mcp__<server>__write_file` | `write` | `file_write` |
| `mcp__<server>__run_command` | `exec` | `tool_call` |
| `web_browse` / `browser` | `browser` | `tool_call` |
| `send_message` (and `*_send`) | `message` | `tool_call` |
| `delegate` / `spawn_agent` | `sessions_spawn` | `session_spawn` |
| unknown / other MCP tools | (passed through) | `tool_call` |

## Running tests

```bash
cd plugins
python -m pytest codex/tests/ -v
```

## Related

- [AgentShield main repository](../../README.md)
- [Hermes connector](../hermes/README.md)
- [OpenClaw connector](../openclaw/README.md)
- [Claude Code hook](../claude/README.md)
- [Detection rules](../../rules/)
- [Engine API](../../docs/api.md)

## Licence

Apache 2.0 — see [LICENSE](../../LICENSE) for details.
