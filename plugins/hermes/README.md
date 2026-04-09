# AgentShield Plugin for Hermes Agent

A plugin for [Hermes Agent](https://github.com/NousResearch/hermes-agent) that intercepts agent tool calls in real time, evaluates them against AgentShield's [Sigma](https://sigmahq.io/)-based detection engine (a standardised format for describing log-based detection patterns), and blocks or logs security-relevant activity before execution.

## Prerequisites

- A running AgentShield engine (see the [main README](../../README.md) for build and setup instructions)
- [Hermes Agent](https://hermes-agent.nousresearch.com/) v0.5.0+ installed

## Installation

Copy the plugin into your Hermes plugins directory:

```bash
cp -r plugins/hermes ~/.hermes/plugins/agentshield
```

Set the auth token (optional for local dev without auth):

```bash
export AGENTSHIELD_AUTH_TOKEN="your-token"
```

Restart Hermes to load the plugin. The plugin uses only the Python standard library (no `pip install` needed).

## Configuration

All settings are declared in `plugin.yaml` and can be overridden via Hermes
plugin settings. The `AGENTSHIELD_AUTH_TOKEN` environment variable is used when
no token is set in plugin settings. Every key has a default so the plugin works
out of the box when the engine is running locally.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `enabled` | `bool` | `true` | Enable or disable the plugin entirely. |
| `endpoint` | `string` | `"http://127.0.0.1:8433/api/v1/evaluate"` | AgentShield evaluation endpoint URL. |
| `auth_token` | `string` | `""` | Bearer token sent with every request (falls back to `AGENTSHIELD_AUTH_TOKEN` env var). |
| `timeout_ms` | `int` | `200` | HTTP timeout for evaluation calls (valid range: 5--5000). |
| `timeout_policy` | `"allow" \| "block" \| "log"` | `"block"` | What to do when the engine is unreachable or times out. |
| `intercept` | `list[str]` | `["terminal", "execute_command", "write_file", ...]` | Tool names to evaluate before execution (Hermes tool names). |
| `skip` | `list[str]` | `["todo", "memory_search", "memory_add"]` | Tool names to skip unconditionally (checked first). |
| `notify` | `"all" \| "high" \| "critical" \| "none"` | `"high"` | Minimum alert severity that triggers a user-visible notification. |
| `circuit_breaker_failure_threshold` | `int` | `3` | Consecutive failures before the circuit breaker opens. |
| `circuit_breaker_recovery_interval_ms` | `int` | `30000` | Time (ms) before a half-open probe is attempted. |

### Default intercept list

The following Hermes tools are intercepted by default:

`terminal`, `execute_command`, `write_file`, `create_file`, `edit_file`,
`patch_file`, `read_file`, `web_browse`, `browser`, `send_message`,
`delegate`, `spawn_agent`, `code_execute`, `python_execute`

## How it works

### Tool name normalisation

Hermes tool names (e.g. `terminal`, `write_file`, `delegate`) are mapped to
AgentShield canonical names (e.g. `exec`, `write`, `sessions_spawn`) so that
the same Sigma detection rules work across all supported platforms. See
[`normalise.py`](normalise.py) for the full mapping table.

### `pre_tool_call` -- synchronous evaluation

For every intercepted tool call the plugin:

1. Checks the **skip** set; if the tool is listed, processing is skipped.
2. Checks the **intercept** set; if the tool is not listed, processing is skipped.
3. Checks the **circuit breaker**; if open, applies the configured `timeout_policy`.
4. Builds an `EvaluationRequest` (normalised tool name + params) and POSTs it
   to the engine's `/api/v1/evaluate` endpoint.
5. If the engine returns `action: "block"`, the tool call is prevented and the
   alert is logged (subject to the `notify` threshold).
6. If the engine returns `action: "require_approval"`, the plugin fails closed
   and blocks execution with an approval-required reason.
7. If the engine returns `action: "log"`, the tool call proceeds but alerts are
   logged.
8. If triage results are present and a high-confidence `allow` verdict is
   returned, rule-based alerts may be overridden.

### `post_tool_call` -- fire-and-forget audit

After every tool call completes, the plugin sends an `AuditReport` to
`/api/v1/audit` containing the tool result summary, error status, and a
correlation ID linking back to the original evaluation. Audit reports are
sent via a bounded background worker queue to avoid blocking the agent loop.

### Lifecycle hooks

The plugin emits fire-and-forget lifecycle events to `/api/v1/lifecycle`:

- `on_session_start` / `on_session_end`

### Startup health check

On registration the plugin performs a non-blocking `GET /api/v1/health` to
verify engine reachability and logs the result.

## Architecture

```
plugins/hermes/
├── __init__.py           # Plugin entry: register(ctx) wires hooks
├── plugin.yaml           # Hermes plugin manifest (settings, hooks)
├── circuit_breaker.py    # Three-state circuit breaker (closed/open/half-open)
├── client.py             # HTTP client: evaluate, audit, lifecycle, health
├── config.py             # Config parser with validation and defaults
├── event_builder.py      # Builds EvaluationRequest, AuditReport, LifecycleEvent
├── normalise.py          # Maps Hermes tool names to AgentShield canonical forms
└── tests/
    ├── test_circuit_breaker.py
    ├── test_client.py
    ├── test_config.py
    ├── test_event_builder.py
    └── test_normalise.py
```

### Design decisions

- **Zero external dependencies** -- uses only Python stdlib (`urllib.request`,
  `threading`, `json`). No `pip install` step required.
- **Bounded background queue** -- fire-and-forget HTTP calls use a single
  background worker with a bounded queue (256 entries) instead of spawning a
  thread per call. Provides natural backpressure under load.
- **Thread-safe circuit breaker** -- all state mutations are protected by a
  lock, safe for concurrent tool calls.

## Running tests

```bash
cd plugins/hermes
python -m pytest tests/ -v
```

## Licence

Apache 2.0 -- see [LICENSE](../../LICENSE) for details.
