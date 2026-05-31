# AgentShield MCP Security Gateway

A transparent security proxy for the [Model Context Protocol](https://modelcontextprotocol.io)
(MCP). The gateway sits between an MCP **client** (the agent) and one or more
downstream MCP **servers**, intercepts every `tools/call` request, evaluates it
against AgentShield's [Sigma](https://sigmahq.io/)-based detection engine, and
either **blocks** the call or **forwards** it to the downstream server.

Because virtually every modern coding/agent platform speaks MCP — Claude Code,
OpenAI Codex, Gemini CLI, Cursor, Windsurf — a single MCP gateway gives
**near-universal enforcement coverage**, including closed platforms that expose
no native pre-tool hook API. Where a platform has no synchronous interception
point of its own, point it at this gateway and you get full blocking
enforcement for any MCP tool it calls.

## How it works

```
                          ┌──────────────────────────────────────────┐
                          │            AgentShield MCP Gateway          │
   MCP client    stdio /  │  ┌────────────┐   evaluate   ┌──────────┐  │   stdio /   ┌────────────────┐
  (the agent) ───HTTP────▶│  │ MCP Server │──────────────▶│ Pipeline │──┼──HTTP──────▶│ AgentShield     │
                          │  │ (upstream) │◀──block/allow─│ + breaker│  │             │ engine (Sigma)  │
                          │  └─────┬──────┘               └────┬─────┘  │             └────────────────┘
                          │        │ forward (if allowed)      │ audit/lifecycle (fire-and-forget)
                          │  ┌─────▼──────┐                    │        │
                          │  │ MCP Client │────────────────────┘        │   stdio /   ┌────────────────┐
                          │  │(downstream)│─────────────────────────────┼──HTTP──────▶│ Downstream MCP  │
                          │  └────────────┘                             │             │ server(s)       │
                          └──────────────────────────────────────────┘             └────────────────┘
```

For each `tools/call` the gateway runs the canonical AgentShield connector
pipeline (contract Section 3):

1. **Normalise** the (possibly namespaced) MCP tool name and arguments into a
   canonical name + human-readable command string (see *Tool mapping* below).
2. **Skip** check — tools in `skip` are forwarded without evaluation.
3. **Intercept** filter — when `intercept` is non-empty, only listed tools (by
   original *or* canonical name) are evaluated; everything else is forwarded.
4. **Circuit-breaker** gate — if the breaker is open, the engine is not called
   and the `timeout_policy` is applied.
5. **Build** the `EvaluationRequest` envelope (`source: "mcp-gateway"`).
6. **Evaluate** synchronously via `POST /api/v1/evaluate`, timeout-bounded.
7. **Act** on the verdict:
   - `block` → return a tool result with `isError: true` carrying the reason.
   - `require_approval` → **fail closed** (block); see *Enforcement model*.
   - `log` → forward, log alerts subject to the `notify` threshold.
   - `allow` → forward.
8. **Audit** — after the downstream tool returns, a fire-and-forget
   `AuditReport` is sent to `/api/v1/audit`, correlated to the evaluation.
9. **Lifecycle** — MCP `initialize` maps to `session_start`, transport close
   maps to `session_end`, both sent to `/api/v1/lifecycle`.
10. **Health** — a non-blocking `GET /api/v1/health` runs at startup.

`tools/list` and all other MCP requests pass through transparently; the gateway
mirrors the downstream server's name, version, capabilities and instructions so
the agent sees a faithful proxy.

## Prerequisites

- A running AgentShield engine (see the [main README](../../README.md)).
- Node.js 20+ (the gateway uses the global `fetch` and `AbortSignal.timeout`).
- One or more downstream MCP servers to protect.

## Install

```bash
cd plugins/mcp-gateway
npm install
npm run build
```

## Usage

### stdio (primary)

The simplest deployment drops the gateway in front of an existing stdio MCP
server command. Everything after `--` is the downstream launch command:

```bash
node dist/bin.js -- npx -y @modelcontextprotocol/server-filesystem /data
```

Wire it into any MCP client by replacing the server's `command`/`args` with the
gateway. For example, in a client's `mcp.json` / `settings.json`:

```json
{
  "mcpServers": {
    "filesystem": {
      "command": "node",
      "args": [
        "/abs/path/plugins/mcp-gateway/dist/bin.js",
        "--",
        "npx", "-y", "@modelcontextprotocol/server-filesystem", "/data"
      ],
      "env": {
        "AGENTSHIELD_ENDPOINT": "http://127.0.0.1:8433/api/v1/evaluate",
        "AGENTSHIELD_AUTH_TOKEN": "your-token"
      }
    }
  }
}
```

This pattern works for Claude Code, Codex, Gemini CLI, Cursor and Windsurf —
any client that launches MCP servers as stdio subprocesses.

### Streamable HTTP / SSE

Set `serve.transport` to `http` (or `MCP_SERVE_TRANSPORT=http`) to expose the
gateway over the MCP Streamable HTTP transport. Each agent session gets its own
downstream connection.

```bash
MCP_SERVE_TRANSPORT=http MCP_SERVE_PORT=8434 \
  MCP_DOWNSTREAM_TRANSPORT=http MCP_DOWNSTREAM_URL=http://localhost:7000/mcp \
  node dist/bin.js
```

### Config file

```bash
node dist/bin.js --config ./config.example.json
```

See [`config.example.json`](config.example.json).

## Configuration

Configuration layers, lowest to highest precedence: **defaults < JSON file <
environment**. Invalid values fall back to defaults silently (matching the
sibling connectors). The `auth_token` falls back to `AGENTSHIELD_AUTH_TOKEN`
when unset — secrets are never hard-coded.

| Key | Type | Default | Env override | Validation |
|-----|------|---------|--------------|------------|
| `enabled` | bool | `true` | `AGENTSHIELD_ENABLED` | non-bool → default |
| `endpoint` | string | `http://127.0.0.1:8433/api/v1/evaluate` | `AGENTSHIELD_ENDPOINT` | non-empty, trimmed |
| `auth_token` | string | `""` | `AGENTSHIELD_AUTH_TOKEN` | falls back to env var |
| `timeout_ms` | number | `200` | `AGENTSHIELD_TIMEOUT_MS` | `5`–`5000`, else default |
| `timeout_policy` | enum | `block` | `AGENTSHIELD_TIMEOUT_POLICY` | `allow` \| `block` \| `log` |
| `notify` | enum | `high` | `AGENTSHIELD_NOTIFY` | `all` \| `high` \| `critical` \| `none` |
| `intercept` | string[] | canonical + common MCP names | — | non-string entries dropped |
| `skip` | string[] | `["todo", "memory_search", "session_status"]` | — | non-string entries dropped |
| `circuit_breaker.failure_threshold` | int | `3` | — | `>= 1`, else default |
| `circuit_breaker.recovery_interval_ms` | int | `30000` | — | `>= 1000`, else default |
| `downstream.transport` | enum | `stdio` | `MCP_DOWNSTREAM_TRANSPORT` | `stdio` \| `http` |
| `downstream.command` | string | `null` | `MCP_DOWNSTREAM_COMMAND` | required for stdio |
| `downstream.args` | string[] | `[]` | `MCP_DOWNSTREAM_ARGS` (space-split) | — |
| `downstream.env` | object | `{}` | — | string values only |
| `downstream.url` | string | `null` | `MCP_DOWNSTREAM_URL` | required for http |
| `serve.transport` | enum | `stdio` | `MCP_SERVE_TRANSPORT` | `stdio` \| `http` |
| `serve.host` | string | `127.0.0.1` | `MCP_SERVE_HOST` | — |
| `serve.port` | int | `8434` | `MCP_SERVE_PORT` | `1`–`65535` |
| (log level) | enum | `info` | `AGENTSHIELD_LOG_LEVEL` / `--log-level` | `debug`/`info`/`warn`/`error` |

A downstream stdio command supplied after `--` on the CLI overrides
`downstream.command`/`downstream.args`.

## Evaluation modes

The engine's `effective_mode` governs what verdicts it returns; the gateway
honours them transparently:

- **enforce** — block on critical/high, require approval on medium.
- **audit** — alerts recorded (`log`), nothing blocked.
- **shadow** — silent monitoring.

## Enforcement model

**Blocking (full enforcement).** MCP exposes a synchronous request/response for
`tools/call`, so the gateway evaluates *before* forwarding and can deny the
call. A blocked or approval-required call is returned to the agent as a tool
result with `isError: true` and the human-readable reason, rather than a raw
protocol error the client might silently retry.

- `timeout_policy` defaults to **`block`** (fail-closed): if the engine is
  unreachable, times out, returns a non-2xx, or returns an invalid `action`,
  the call is denied. Set `timeout_policy: "allow"` to fail open.
- **`require_approval` fails closed.** MCP has no standard interactive
  user-approval channel from a proxied server, so the gateway treats
  `require_approval` as a block (contract Section 3.2). The block result is
  flagged `overridable` so a host that *does* offer an override path can surface
  one; when a user overrides, call `POST /api/v1/override` (exposed via the
  client) so the engine can track override escalation.

## Tool mapping

MCP tool names are frequently namespaced by the client (`mcp__server__tool`,
`server.tool`, `server/tool`, `server:tool`). The gateway strips the namespace,
then maps the bare name to an AgentShield canonical name using the same alias
table as the Hermes connector, so the same Sigma rules fire regardless of which
MCP server emitted the call. Unknown tools pass through unchanged.

| Downstream / platform names | Canonical | `command` string |
|---|---|---|
| `terminal`, `execute_command`, `run_command`, `shell`, `bash` | `exec` | the raw command |
| `write_file`, `create_file`, `save_file`, `Write` | `write` | `Write: <path>` |
| `read_file`, `view_file`, `Read`, `cat` | `read` | `Read: <path>` |
| `edit_file`, `patch_file`, `replace_in_file`, `Edit` | `edit` | `Edit: <path>` |
| `web_browse`, `browser`, `navigate`, `browse_url` | `browser` | `<action>: <url>` |
| `send_message`, `slack_send`, `telegram_send`, … | `message` | `Message to <channel>` |
| `delegate`, `spawn_agent`, `create_subagent` | `sessions_spawn` | `Spawn: <agent>` |
| `code_execute`, `python_execute`, `run_python` | `code_execute` | `Execute: <code…>` |
| `web_search`, `search` | `web_search` | `Search: <query>` |
| (others) | passthrough | the original name |

`event_type` is derived independently: `read`→`file_read`, `write`/`create`→
`file_write`, `edit`→`file_edit`, `sessions_spawn`→`session_spawn`, else
`tool_call`.

## Architecture

```
plugins/mcp-gateway/
├── src/
│   ├── bin.ts            # CLI entry: stdio (primary) + streamable HTTP serving
│   ├── index.ts          # public exports + stderr logger factory
│   ├── gateway.ts        # McpGateway: MCP Server (upstream) ↔ Client (downstream) proxy
│   ├── pipeline.ts       # skip→intercept→breaker→evaluate→act→audit→lifecycle
│   ├── client.ts         # AgentShieldClient: evaluate + fire-and-forget audit/lifecycle/feedback/override + health
│   ├── event-builder.ts  # buildEvaluationRequest / buildAuditReport / buildLifecycleEvent
│   ├── normalise.ts      # tool-name + command normalisation (alias table)
│   ├── config.ts         # parseConfig (env + JSON) with validation; loadConfigFile
│   ├── circuit-breaker.ts# three-state circuit breaker
│   └── types.ts          # shared wire + config types
├── config.example.json
├── package.json
├── tsconfig.json
└── vitest.config.ts
```

### Design decisions

- **Single external dependency** — only the official
  `@modelcontextprotocol/sdk`; the engine HTTP client uses the global `fetch`.
- **stdout stays clean** — all diagnostics go to **stderr** so they never
  corrupt the stdio MCP framing.
- **Faithful passthrough** — the gateway advertises the downstream server's own
  identity and capabilities; only `tools/call` is intercepted.
- **In-band block reasons** — denials are returned as `isError` tool results so
  the model can read the reason and adapt, rather than treating it as a
  transport fault.

## Running tests

```bash
cd plugins/mcp-gateway
npm test            # vitest unit tests
npm run typecheck   # tsc --noEmit (strict)
```

## Related

- Main repository: [AgentShield](../../README.md)
- Sibling connectors: [OpenClaw](../openclaw/README.md), [Hermes](../hermes/README.md), [Claude Code](../claude/README.md)
- Detection rules: [`rules/`](../../rules)
- Engine API: [`docs/api.md`](../../docs/api.md)

## Licence

Apache 2.0 — see [LICENSE](../../LICENSE) for details.
