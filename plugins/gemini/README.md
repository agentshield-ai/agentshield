# AgentShield Connector for the Google Gemini CLI

A connector for the [Google Gemini CLI](https://github.com/google-gemini/gemini-cli) that intercepts agent tool calls in real time, evaluates them against AgentShield's [Sigma](https://sigmahq.io/)-based detection engine (a standardised format for describing log-based detection patterns), and **blocks** or logs security-relevant activity before execution.

It plugs into the Gemini CLI's native [hooks system](https://geminicli.com/docs/hooks/), which fires synchronously as part of the agent loop. The `BeforeTool` hook is a genuine, blocking pre-tool enforcement point — equivalent to the `pre_tool_call` hook used by the Hermes connector — so enforcement here is real, not advisory.

## Enforcement model

**Blocking (synchronous).** The Gemini CLI runs each `BeforeTool` hook as a subprocess and waits for it to finish before dispatching the tool. When a hook prints `{"decision": "deny", "reason": "..."}` to stdout and exits `0`, the CLI aborts the tool call and surfaces the reason. This connector therefore enforces `block` and (fail-closed) `require_approval` verdicts by emitting a `deny` decision. No MCP gateway fallback is required for enforcement; the hook itself is authoritative.

Hook events used:

| Gemini event | Purpose | Blocking? |
|---|---|---|
| `BeforeTool` | Synchronous, timeout-bounded security evaluation | **Yes** (`deny`) |
| `AfterTool` | Fire-and-forget post-tool audit report | No |
| `SessionStart` | Fire-and-forget lifecycle event + opportunistic health probe | No |
| `SessionEnd` | Fire-and-forget lifecycle event | No |

## Prerequisites

- A running AgentShield engine (see the [main README](../../README.md) for build and setup instructions).
- [Google Gemini CLI](https://github.com/google-gemini/gemini-cli) with hooks support (the `hooks` block in `settings.json`).
- Python 3.11+ available on `PATH`. The connector uses **only the Python standard library** — no `pip install` step is required.

## Installation

Run the installer, which copies the connector into `~/.agentshield/gemini/`, health-checks the engine, and prints the `settings.json` hooks snippet to paste in:

```bash
./install.sh
```

Then merge the printed `hooks` block into `~/.gemini/settings.json` (project-level `.gemini/settings.json` also works and takes precedence). Set the auth token if the engine requires it:

```bash
export AGENTSHIELD_AUTH_TOKEN="your-32-plus-char-token"
```

Restart the Gemini CLI to pick up the new hooks.

### Verifying it works

A benign command is allowed (empty stdout, exit 0):

```bash
echo '{"hook_event_name":"BeforeTool","tool_name":"run_shell_command","tool_input":{"command":"ls /tmp"}}' \
  | PYTHONPATH=~/.agentshield/gemini python3 -m agentshield_gemini.hook
```

A dangerous command is blocked (a `deny` decision is printed):

```bash
echo '{"hook_event_name":"BeforeTool","tool_name":"run_shell_command","tool_input":{"command":"sudo rm -rf /"}}' \
  | PYTHONPATH=~/.agentshield/gemini python3 -m agentshield_gemini.hook
```

## Configuration

Configuration is read (in order) from `$AGENTSHIELD_GEMINI_CONFIG`, then `~/.agentshield/gemini.json` (either a flat object or an `{"agentshield": {...}}` block), then built-in defaults. The `AGENTSHIELD_AUTH_TOKEN` environment variable is used when no token is set in config. Every key has a default so the connector works out of the box against a local engine.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `enabled` | `bool` | `true` | Enable or disable the connector entirely. |
| `endpoint` | `string` | `"http://127.0.0.1:8433/api/v1/evaluate"` | AgentShield evaluation endpoint URL. Sibling endpoints (`/audit`, `/lifecycle`, `/health`, `/feedback`, `/override`) are derived by stripping the trailing `/evaluate`. |
| `auth_token` | `string` | `""` | Bearer token sent with every request (falls back to the `AGENTSHIELD_AUTH_TOKEN` env var). |
| `timeout_ms` | `int` | `200` | HTTP timeout for evaluation calls (valid range: 5–5000; out-of-range values fall back to the default). |
| `timeout_policy` | `"allow" \| "block" \| "log"` | `"block"` | What to do when the engine is unreachable or the call fails. Default fails **closed**. |
| `notify` | `"all" \| "high" \| "critical" \| "none"` | `"high"` | Minimum alert severity that triggers a user-visible notification (logged to stderr). |
| `intercept` | `list[str]` | see below | Tool names to evaluate before execution (Gemini CLI tool names; canonical names also match). An empty/unset list evaluates everything not skipped. |
| `skip` | `list[str]` | see below | Tool names skipped unconditionally (checked first). |
| `circuit_breaker_failure_threshold` | `int` | `3` | Consecutive failures before the circuit breaker opens (must be ≥ 1). |
| `circuit_breaker_recovery_interval_ms` | `int` | `30000` | Time (ms) before a half-open probe is attempted (must be ≥ 1000). |

The connector also honours these environment variables:

| Variable | Purpose |
|---|---|
| `AGENTSHIELD_AUTH_TOKEN` | Bearer token fallback when `auth_token` is unset. |
| `AGENTSHIELD_GEMINI_CONFIG` | Path to a JSON config file (highest-precedence config source). |
| `AGENTSHIELD_GEMINI_STATE_DIR` | Directory for cross-process state (breaker + correlation). Defaults to `~/.agentshield/gemini-state/`. |
| `AGENTSHIELD_LOG_LEVEL` | Log level for hook stderr output (default `WARNING`). |
| `AGENTSHIELD_URL` | Base engine URL used by `install.sh` for the health check. |

### Default `intercept` list

`run_shell_command`, `write_file`, `replace`, `read_file`, `read_many_files`, `web_fetch`, `google_web_search`, `save_memory`

### Default `skip` list

`write_todos`, `list_directory`, `glob`, `grep_search`, `ask_user`

## How it works

### Data flow

```
Gemini CLI tool call
  -> BeforeTool hook (subprocess, stdin JSON)
     -> normalise tool name + params
     -> skip / intercept filter
     -> circuit-breaker gate
     -> POST /api/v1/evaluate  (synchronous, timeout-bounded)
     -> act on action:
          block            -> {"decision":"deny", reason}   (tool aborted)
          require_approval  -> {"decision":"deny", reason}   (fail-closed)
          log / allow       -> {}                            (tool proceeds)
  -> tool executes
  -> AfterTool hook (subprocess) -> POST /api/v1/audit  (fire-and-forget)

SessionStart / SessionEnd hooks -> POST /api/v1/lifecycle  (fire-and-forget)
SessionStart additionally probes GET /api/v1/health (non-blocking).
```

### Tool-name normalisation

Gemini CLI tool names (e.g. `run_shell_command`, `write_file`, `replace`, `web_fetch`, `google_web_search`) are mapped to AgentShield canonical names (e.g. `exec`, `write`, `edit`, `browser`, `web_search`) so the same Sigma detection rules work across every supported platform. Unknown or MCP-provided tools pass through unchanged rather than being silently dropped. See [`agentshield_gemini/normalise.py`](agentshield_gemini/normalise.py) for the full mapping table.

| Gemini CLI tool | Canonical name |
|---|---|
| `run_shell_command`, `shell`, `execute_command`, `terminal` | `exec` |
| `write_file`, `create_file`, `save_file` | `write` |
| `read_file`, `read_many_files`, `view_file` | `read` |
| `replace`, `edit_file`, `patch_file` | `edit` |
| `web_fetch`, `web_browse`, `browser`, `navigate` | `browser` |
| `google_web_search`, `web_search`, `search` | `web_search` |
| `save_memory`, `memory_add`, `memory_search` | `memory` |
| `write_todos`, `task_plan`, `todo`, `enter_plan_mode`, `exit_plan_mode` | `planning` |
| `delegate`, `spawn_agent`, `create_subagent` | `sessions_spawn` |
| `send_message`, `slack_send`, `discord_send` | `message` |
| `code_execute`, `python_execute`, `run_python` | `code_execute` |

### `BeforeTool` — synchronous evaluation

For every intercepted tool call the connector:

1. Checks the **skip** set; if the tool (or its canonical name) is listed, it is allowed without evaluation.
2. Checks the **intercept** set; if a list is configured and neither the tool nor its canonical name appears in it, the tool is allowed without evaluation.
3. Checks the **circuit breaker**; if open, the engine is not called and the configured `timeout_policy` is applied.
4. Builds an `EvaluationRequest` (canonical tool name, command string, params, session id, working dir) and POSTs it to `/api/v1/evaluate`, validating that the response `action` is one of `allow | block | log | require_approval`.
5. If the engine returns `action: "block"`, the tool call is denied and the alert is logged (subject to the `notify` threshold). The top triage reasoning is appended to the reason when present.
6. If the engine returns `action: "require_approval"`, the connector **fails closed** and denies the tool call with an approval-required reason.
7. If the engine returns `action: "log"`, the tool proceeds but alerts are logged.
8. If `allow` is returned with rule alerts but a triage result gives a high-confidence (`> 0.8`) `allow` verdict, the alerts are treated as overridden and the tool proceeds.
9. On any failure (timeout, non-2xx, invalid `action`), the breaker records a failure and the `timeout_policy` is applied.

### `AfterTool` — fire-and-forget audit

After a tool completes, the connector sends an `AuditReport` to `/api/v1/audit` containing a truncated result summary (1000 chars max), error status (derived from Gemini's `tool_response.error`), and a `correlation_id` linking back to the originating evaluation.

### Lifecycle and health

`SessionStart` / `SessionEnd` emit fire-and-forget `LifecycleEvent`s to `/api/v1/lifecycle`. On `SessionStart` the connector additionally performs a non-blocking `GET /api/v1/health` probe and logs reachability; it never blocks the CLI on the result.

### Evaluation modes

The engine's mode (configured engine-side) shapes the verdicts the connector receives:

- **enforce** — block on critical/high, require approval on medium (denied here, fail-closed), allow others.
- **audit** — log all alerts; never block.
- **shadow** — silent monitoring; no alerts surfaced.

## Design decisions

- **Zero external dependencies** — uses only the Python standard library (`urllib.request`, `json`, `fcntl`).
- **Cross-process state** — because each hook runs as a short-lived subprocess, the circuit breaker and the evaluate→audit correlation map are persisted to small JSON files under the state directory, guarded by POSIX advisory file locks (`fcntl`). The transition semantics match the in-memory breaker used by the Hermes and OpenClaw connectors; the wall clock is used for the recovery interval (as the TypeScript connector does), since a monotonic clock cannot be shared across processes.
- **Synchronous fire-and-forget** — a background worker thread cannot be relied upon to flush before a subprocess exits, so audit/lifecycle calls are issued synchronously with a short (2 s) timeout and their failures are swallowed at debug level.
- **Never crashes the agent loop** — `main()` always exits `0`; a block is conveyed via the JSON `decision` field. Unexpected internal errors apply the configured `timeout_policy` (fail-closed by default).

## Architecture

```
plugins/gemini/
├── README.md
├── install.sh                       # Copies connector, prints settings.json snippet, health-checks engine
├── pyproject.toml                   # ruff / mypy / pytest config (stdlib-only, no runtime deps)
├── agentshield_gemini/
│   ├── __init__.py                  # CONTRACT_VERSION, SOURCE constants
│   ├── __main__.py                  # `python -m agentshield_gemini` entry-point
│   ├── hook.py                      # Hook dispatcher + BeforeTool/AfterTool/Session handlers
│   ├── client.py                    # HTTP client: evaluate, audit, lifecycle, override, feedback, health
│   ├── config.py                    # Config parser with validation and defaults
│   ├── event_builder.py             # Builds EvaluationRequest, AuditReport, LifecycleEvent
│   ├── normalise.py                 # Maps Gemini CLI tool names to AgentShield canonical forms
│   ├── circuit_breaker.py           # Cross-process three-state circuit breaker
│   └── correlation.py               # Cross-process evaluate -> audit correlation FIFO
└── tests/
    ├── conftest.py
    ├── test_circuit_breaker.py
    ├── test_client.py
    ├── test_config.py
    ├── test_correlation.py
    ├── test_event_builder.py
    ├── test_hook.py
    └── test_normalise.py
```

## Running tests

```bash
cd plugins/gemini
python -m pytest tests/ -v
ruff check .
mypy agentshield_gemini
```

## Related

- [AgentShield main repository](../../README.md)
- Sibling connectors: [OpenClaw](../openclaw/), [Hermes](../hermes/), [Claude Code](../claude/)
- [Detection rules](../../rules/)
- [Engine API reference](../../docs/api.md)

## Licence

Apache 2.0 — see [LICENSE](../../LICENSE) for details.
