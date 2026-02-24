# AgentShield OpenClaw Plugin

OpenClaw plugin for real-time security evaluation of AI agent tool calls using Sigma rules and optional LLM triage.

## How It Works

The plugin registers `before_tool_call` and `after_tool_call` hooks with OpenClaw. Before each intercepted tool call, the plugin sends a synchronous evaluation request to the AgentShield engine. The engine evaluates the call against loaded Sigma rules (and optionally LLM triage), then returns an `allow`, `block`, or `log` action. After the tool call completes, the plugin sends a fire-and-forget audit report.

### Request Flow

1. OpenClaw fires `before_tool_call`
2. Plugin builds an `EvaluationRequest` (tool name, normalised command, params)
3. `AgentShieldClient` POSTs to `/api/v1/evaluate` with a configurable timeout
4. Engine returns action + alerts + optional triage results
5. If `block`: plugin returns `blockReason` to OpenClaw, tool call is prevented
6. If `log` or `allow`: tool call proceeds, alerts are surfaced via system event (if they meet the notify threshold)
7. After execution, `after_tool_call` sends an audit report to `/api/v1/audit`

Lifecycle events (`session_start`, `session_end`, `agent_start`, `agent_end`) are sent to `/api/v1/lifecycle` as fire-and-forget calls.

## Plugin Architecture

```
plugins/openclaw/
  index.ts                  Main plugin (registers hooks)
  openclaw.plugin.json      Plugin manifest (id, configSchema)
  package.json              NPM metadata, vitest test runner
  src/
    client.ts               HTTP client (evaluate, audit, lifecycle, health, feedback)
    config.ts               Config parsing with defaults and validation
    types.ts                TypeScript type definitions (request/response contracts)
    circuit-breaker.ts      Three-state circuit breaker (closed/open/half-open)
    event-builder.ts        Builds EvaluationRequest, AuditReport, LifecycleEvent
    normalise.ts            Maps OpenClaw tool names to command strings
  skill/
    SKILL.md                AgentShield engine documentation
    manifest.json           Skill manifest
    install.sh              Automated installer (binary + config + service + rules)
    uninstall.sh            Automated uninstaller
```

### Key Components

- **`AgentShieldClient`** (`src/client.ts`): HTTP client. `evaluate()` is async with `AbortSignal.timeout()`. `sendAudit()`, `sendLifecycle()`, and `submitFeedback()` are fire-and-forget. Headers include `Authorization: Bearer <token>` and `X-AgentShield-Version: 1.0.0`.
- **`CircuitBreaker`** (`src/circuit-breaker.ts`): After N consecutive failures the circuit opens and all calls are skipped for the recovery interval. After recovery, a single probe request is allowed (half-open). Success resets to closed.
- **`normaliseToolCall`** (`src/normalise.ts`): Maps tool names to command strings for Sigma rule matching. `exec` passes `params.command` directly. File tools (`write`, `read`, `edit`) use `"Write: <path>"` format. `browser` uses `"<action>: <url>"`. `sessions_spawn` uses `"Spawn: <agentId>"`.
- **`notifyAlert`** (`index.ts`): Sends system events via `api.runtime.system.enqueueSystemEvent()` for alerts meeting the configured severity threshold.

## Installation

### Via Skill Installer

```bash
cd plugins/openclaw/skill
./install.sh
```

The installer downloads the AgentShield binary (or compiles via Go), creates `~/.agentshield/` directory structure, clones sigma-ai rules, generates an auth token, creates `config.yaml`, sets up a systemd (Linux) or launchd (macOS) service, patches OpenClaw config, and runs a health check.

### Manual Setup

1. Build and start the AgentShield engine:
```bash
go build ./cmd/agentshield/
./agentshield serve --config ~/.agentshield/config.yaml
```

2. Clone detection rules:
```bash
git clone https://github.com/agentshield-ai/sigma-ai.git ~/.agentshield/rules
```

3. Configure the OpenClaw plugin entry (see Configuration below).

## Configuration

Plugin config is set in OpenClaw's plugin configuration under the `agentshield` key. All values have defaults; only `auth_token` must be set explicitly.

### Config Keys

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `enabled` | boolean | `true` | Enable/disable the plugin |
| `endpoint` | string | `http://127.0.0.1:8433/api/v1/evaluate` | AgentShield engine evaluate endpoint |
| `auth_token` | string | `""` | Bearer token for engine authentication |
| `timeout_ms` | number | `200` | Evaluation request timeout (valid: 5-5000) |
| `timeout_policy` | string | `"block"` | Action on timeout/failure: `"allow"`, `"block"`, `"log"` |
| `intercept` | string[] | `["exec","write","edit","browser","message","sessions_spawn"]` | Tool names to evaluate |
| `skip` | string[] | `["read","session_status"]` | Tool names to skip (checked before intercept) |
| `notify` | string | `"high"` | Minimum alert severity for user notifications: `"all"`, `"high"`, `"critical"`, `"none"` |
| `circuit_breaker.failure_threshold` | number | `3` | Consecutive failures before circuit opens |
| `circuit_breaker.recovery_interval_ms` | number | `30000` | Recovery wait before half-open probe (min: 1000) |

### Example OpenClaw Config

```json
{
  "plugins": {
    "entries": {
      "agentshield": {
        "enabled": true,
        "config": {
          "enabled": true,
          "endpoint": "http://127.0.0.1:8433/api/v1/evaluate",
          "auth_token": "your-64-char-token-here",
          "timeout_ms": 200,
          "timeout_policy": "block"
        }
      }
    }
  }
}
```

### Manifest vs Code Defaults

The plugin manifest (`openclaw.plugin.json`) declares `timeout_ms: 50` and `timeout_policy: "allow"` as defaults. The code (`src/config.ts`) uses `timeout_ms: 200` and `timeout_policy: "block"`. The code defaults take precedence at runtime. The skill manifest (`skill/manifest.json`) uses `timeout_ms: 100` and `timeout_policy: "allow"` for its installer preset.

## Evaluation Modes

The evaluation mode is configured on the **engine** side (`evaluation_mode` in `config.yaml`), not in the plugin. The plugin receives the effective mode in the response as `effective_mode`.

- **enforce**: Blocks tool calls that match rules. Production use.
- **audit**: Logs alerts without blocking. Default engine mode.
- **shadow**: Silent monitoring only. No alerts surfaced.

## Tool Call Normalization

The plugin normalises OpenClaw tool names into command strings that Sigma rules can match against:

| Tool Name | Normalized Command |
|-----------|-------------------|
| `exec` | `params.command` (verbatim) |
| `write` | `Write: <path>` |
| `read` | `Read: <path>` |
| `edit` | `Edit: <path>` |
| `browser` | `<action>: <url>` |
| `message` | `Message to <channel>` |
| `sessions_spawn` | `Spawn: <agentId>` |
| (other) | tool name as-is |

## API Endpoints Used

The plugin communicates with these engine endpoints (all derived from the configured `endpoint` by replacing `/evaluate`):

| Endpoint | Method | Behavior |
|----------|--------|----------|
| `/api/v1/evaluate` | POST | Synchronous, timeout-bounded |
| `/api/v1/audit` | POST | Fire-and-forget |
| `/api/v1/lifecycle` | POST | Fire-and-forget |
| `/api/v1/health` | GET | 2-second timeout, returns boolean |
| `/api/v1/feedback` | POST | Fire-and-forget |

### Evaluation Request (sent by plugin)

```json
{
  "event_id": "uuid",
  "timestamp": "2026-02-24T12:00:00.000Z",
  "event_type": "tool_call",
  "tool_name": "exec",
  "source": "openclaw",
  "command": "ls -la",
  "params": { "command": "ls -la" },
  "agent_id": "agent-uuid-or-null",
  "session_id": "session-key-or-null",
  "working_dir": null,
  "data": {}
}
```

### Evaluation Response (returned by engine)

```json
{
  "action": "allow",
  "event_id": "uuid",
  "alerts": [
    {
      "rule_id": "rule-001",
      "rule_name": "Suspicious File Access",
      "severity": "medium",
      "description": "Monitor for suspicious file operations",
      "matched": true,
      "matched_fields": { "command": "cat /etc/passwd" }
    }
  ],
  "reason": "No critical alerts",
  "triage_results": [
    {
      "verdict": "allow",
      "confidence": 0.92,
      "reasoning": "Standard system inspection",
      "suggested_action": "allow",
      "provider": "openai",
      "model": "gpt-4o-mini",
      "processing_time": 450
    }
  ],
  "effective_mode": "enforce",
  "overridable": false,
  "timestamp": "2026-02-24T12:00:00.100Z"
}
```

Actions are lowercase: `"allow"`, `"block"`, `"log"`.

## Triage Override Behavior

When triage results are present, a high-confidence allow (`confidence > 0.8`, `verdict: "allow"`) overrides rule-based alerts. This means even if Sigma rules fire, the plugin will allow the tool call if the LLM triage concludes it is benign with sufficient confidence.

## Development

### Prerequisites

- Node.js 18+
- vitest (`devDependencies`)
- OpenClaw `>=2026.2.0` (peer dependency)

### Running Tests

```bash
npm test
```

Tests use vitest. Test files are colocated with source:
- `index.test.ts` — plugin integration tests
- `src/client.test.ts` — HTTP client tests
- `src/config.test.ts` — config parsing tests
- `src/circuit-breaker.test.ts` — circuit breaker state machine tests
- `src/event-builder.test.ts` — event builder tests
- `src/normalise.test.ts` — tool call normalization tests

There is no build step. The plugin entry point is `index.ts` loaded directly by OpenClaw.

## Limitations

- The plugin does not control the evaluation mode. Mode is set on the engine (`evaluation_mode` in `config.yaml`).
- `working_dir` is always `null` in the current implementation (not passed by the plugin).
- Audit report `result_summary` is truncated to 1000 characters.
- The correlation map for linking `before_tool_call` to `after_tool_call` uses a 60-second TTL. Long-running tool calls may lose correlation.
- There is no `npm run build` step — this is a raw TypeScript plugin, not compiled.
- Chat commands (`/agentshield status`, `/agentshield mode`, etc.) are not implemented in this plugin.

## License

Apache 2.0
