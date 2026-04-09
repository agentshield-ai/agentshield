# AgentShield Plugin for OpenClaw

A plugin for OpenClaw that intercepts agent tool calls in real time, evaluates them against AgentShield's [Sigma](https://sigmahq.io/)-based detection engine (a standardised format for describing log-based detection patterns), and blocks or logs security-relevant activity before execution.

## Prerequisites

- A running AgentShield engine (see the [main README](../../README.md) for build and setup instructions)
- [OpenClaw](https://openclaw.dev/) installed and configured

## Installation

### Via OpenClaw skill

Run the bundled installer, which downloads the AgentShield engine binary, clones
the detection rules, creates a default config, and registers a system service:

```bash
# From within an OpenClaw session
/install agentshield
```

The script lives at `skill/install.sh` and supports both macOS (launchd) and
Linux (systemd). It defaults to `$HOME/.agentshield` as the install directory.

### Manual setup

1. Copy the plugin directory into your OpenClaw plugins folder.
2. Start the AgentShield engine so that `http://127.0.0.1:8433/api/v1/evaluate`
   is reachable (see the main repository README for engine setup).
3. Add the plugin entry to your OpenClaw config and set `auth_token` to match
   the engine's configured token.

## Configuration

All keys are set under the `agentshield` plugin config in your OpenClaw
settings. Every key has a default so the plugin works out of the box when the
engine is running locally.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `enabled` | `boolean` | `true` | Enable or disable the plugin entirely. |
| `endpoint` | `string` | `"http://127.0.0.1:8433/api/v1/evaluate"` | AgentShield evaluation endpoint URL. |
| `auth_token` | `string` | `""` | Bearer token sent with every request. |
| `timeout_ms` | `number` | `200` | HTTP timeout for evaluation calls (valid range: 5--5000). |
| `timeout_policy` | `"allow" \| "block" \| "log"` | `"block"` | What to do when the engine is unreachable or times out. |
| `intercept` | `string[]` | `["exec", "write", "edit", "read", "browser", "message", "sessions_spawn"]` | Tool names to evaluate before execution. |
| `skip` | `string[]` | `["session_status"]` | Tool names to skip unconditionally (checked first). |
| `notify` | `"all" \| "high" \| "critical" \| "none"` | `"high"` | Minimum alert severity that triggers a user-visible notification. |
| `circuit_breaker.failure_threshold` | `number` | `3` | Consecutive failures before the circuit breaker opens. |
| `circuit_breaker.recovery_interval_ms` | `number` | `30000` | Time (ms) before a half-open probe is attempted. |

## How it works

### `before_tool_call` -- synchronous evaluation

For every intercepted tool call the plugin:

1. Checks the **skip** set; if the tool is listed, processing is skipped.
2. Checks the **intercept** set; if the tool is not listed, processing is skipped.
3. Checks the **circuit breaker**; if open, applies the configured `timeout_policy`.
4. Builds an `EvaluationRequest` (normalised tool name + params) and POSTs it
   to the engine's `/api/v1/evaluate` endpoint.
5. If the engine returns `action: "block"`, the tool call is prevented and the
   user is notified via a system event (subject to the `notify` threshold).
6. If the engine returns `action: "require_approval"`, the plugin fails closed
   and blocks execution with an approval-required reason.
7. If the engine returns `action: "log"`, the tool call proceeds but alerts are
   surfaced to the user.
8. If triage results are present, they are surfaced as advisory context; they
   do not downgrade rule-based enforcement decisions.

The hook runs at priority `-100` so it executes before other plugins can modify
tool parameters.

### `after_tool_call` -- fire-and-forget audit

After every tool call completes, the plugin sends an `AuditReport` to
`/api/v1/audit` containing the tool result summary, duration, error status, and
a correlation ID linking back to the original evaluation.

### Lifecycle hooks

The plugin also emits fire-and-forget lifecycle events to `/api/v1/lifecycle`:

- `session_start` / `session_end`
- `agent_start` (via `before_agent_start`) / `agent_end`

### Startup health check

On registration the plugin performs a non-blocking `GET /api/v1/health` to
verify engine reachability and logs the result.

## Architecture

```
plugins/openclaw/
├── index.ts                  # Plugin entry: registers hooks, wires components
├── openclaw.plugin.json      # Plugin manifest (id, version, config schema)
├── package.json              # npm package metadata
├── skill/
│   └── install.sh            # One-command installer for engine + rules
└── src/
    ├── circuit-breaker.ts    # Three-state circuit breaker (closed/open/half-open)
    ├── client.ts             # HTTP client: evaluate, audit, lifecycle, health, feedback
    ├── config.ts             # Config parser with validation and defaults
    ├── event-builder.ts      # Builds EvaluationRequest, AuditReport, LifecycleEvent
    ├── normalise.ts          # Maps OpenClaw tool names + params to command strings
    └── types.ts              # TypeScript type definitions for all payloads
```

## Licence

Apache 2.0 -- see [LICENSE](../../LICENSE) for details.
