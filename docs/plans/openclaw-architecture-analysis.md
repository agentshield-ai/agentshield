# OpenClaw Architecture Analysis for AgentShield Integration

**Date:** 2026-02-07
**Author:** OpenClaw Architect (automated analysis)
**Purpose:** Identify hook points for runtime interception by AgentShield

---

## 1. Architecture Overview

OpenClaw is a cloud-hosted runtime for AI agents with multi-channel messaging, Docker sandbox isolation, and a plugin system. It is built in TypeScript (ESM) on Node.js 22+, using pnpm workspaces. The primary runtime mode is a **gateway server** that receives messages from messaging channels (WhatsApp, Telegram, Discord, Slack, Signal, iMessage, etc.) and routes them to AI agents backed by LLM providers (Anthropic, OpenAI, Google, Bedrock, etc.).

### End-to-End Flow

1. **Entry**: `openclaw.mjs` -> `src/entry.ts` -> `src/cli/run-main.js` -> Commander program (`src/cli/program.js`)
2. **Gateway startup**: `openclaw gateway run` -> creates HTTP/WS server, loads plugins, initialises hook runner, starts channel connections
3. **Inbound message**: Channel adapter receives message -> routing -> session resolution -> `runEmbeddedPiAgent()` in `src/agents/pi-embedded-runner/run.ts`
4. **Agent loop**: Pi agent runs with tools -> each tool call goes through policy filtering + `before_tool_call` hook -> tool execution -> `after_tool_call` hook -> result back to LLM
5. **Outbound**: Agent reply -> channel adapter -> delivered to user

### Workspace Structure

```
src/
  agents/           # Core agent execution: tools, sandbox, session management
  cli/              # Commander CLI wiring
  commands/         # CLI command implementations
  config/           # Configuration loading and schema
  gateway/          # HTTP/WS gateway server
  hooks/            # Hooks system (bundled + workspace hooks)
  infra/            # Infrastructure: events, env, ports, heartbeat
  plugins/          # Plugin registry, loader, hook runner
  security/         # Security audit module
  channels/         # Channel abstraction layer
  browser/          # Playwright browser integration
  ...
extensions/         # Workspace plugin packages (channel extensions, auth, etc.)
packages/           # Shared workspace packages
skills/             # Skill definitions (YAML/MD)
```

### Key Dependencies

- `@mariozechner/pi-agent-core` / `pi-ai` / `pi-coding-agent`: Core agent runtime (Pi framework)
- `@sinclair/typebox`: Tool schema definitions
- `hono` / `express`: HTTP server
- `ws`: WebSocket
- `commander`: CLI framework
- `zod`: Configuration validation

---

## 2. Container Lifecycle

### Sandbox Docker Architecture

Sandbox containers are Docker containers that isolate agent tool execution. They are **not** the main OpenClaw process container but are spawned alongside it.

**Three Dockerfile variants:**
- `Dockerfile`: Main OpenClaw gateway (Node.js, runs as non-root `node` user)
- `Dockerfile.sandbox`: Lightweight Debian container for shell/tool execution (`sleep infinity`)
- `Dockerfile.sandbox-browser`: Chromium + VNC + noVNC for browser automation

### Container Lifecycle (`src/agents/sandbox/docker.ts`)

1. **Creation**: `ensureSandboxContainer()` -> checks if container exists -> if not, `createSandboxContainer()`:
   - `docker create` with security hardening flags
   - `docker start <name>`
   - Optional `setupCommand` run via `docker exec`

2. **Naming**: `<containerPrefix><slugified-session-key>` (max 63 chars)

3. **Scoping** (`SandboxScope`):
   - `session`: One container per session (most isolated)
   - `agent`: One container per agent (shared across sessions)
   - `shared`: Single container for all agents

4. **Config hash tracking**: Each container stores a `configHash` label. On reuse, if hash mismatches, the container is recreated (unless recently used, in which case a hint is printed).

5. **Pruning** (`src/agents/sandbox/prune.ts`): Idle containers are removed after configurable hours/days via `maybePruneSandboxes()`.

6. **Registry**: `src/agents/sandbox/registry.ts` tracks containers in a local JSON file.

### Default Docker Security Config (`src/agents/sandbox/config.ts`)

```typescript
{
  image: "openclaw-sandbox:latest",         // Debian bookworm-slim base
  readOnlyRoot: true,                        // --read-only
  tmpfs: ["/tmp", "/var/tmp", "/run"],       // Writable temp dirs
  network: "none",                           // NO network access by default
  capDrop: ["ALL"],                          // Drop ALL capabilities
  // Plus: --security-opt no-new-privileges
  // Optional: seccomp, apparmor profiles
  // Optional: pidsLimit, memory, cpus, ulimits
}
```

---

## 3. Agent Execution Pipeline

### Message -> Agent Run

The core execution path is:

```
Inbound message (channel adapter)
  -> Session resolution (src/routing/, src/sessions/)
  -> runEmbeddedPiAgent() (src/agents/pi-embedded-runner/run.ts)
    -> resolveModel() - pick LLM provider/model
    -> buildEmbeddedRunPayloads() - construct system prompt, tools, messages
    -> runEmbeddedAttempt() - single LLM API call with tool loop
      -> Pi agent core (pi-agent-core) runs the agentic loop
      -> subscribeEmbeddedPiSession() (src/agents/pi-embedded-subscribe.ts)
        -> Tool execution events: start -> update -> end
```

### Tool Construction (`src/agents/pi-tools.ts` -> `createOpenClawCodingTools()`)

This is the **central function** that builds the tool array for each agent run:

1. **Base coding tools**: `read`, `write`, `edit` (from pi-coding-agent)
2. **Exec/process tools**: `createExecTool()`, `createProcessTool()` - bash execution
3. **OpenClaw tools**: browser, canvas, sessions, memory, web, cron, gateway, agents, message
4. **Channel tools**: Channel-specific tools (e.g., whatsapp_login)
5. **Plugin tools**: Tools registered by plugins via `registerTool()`

### Tool Filtering Pipeline (applied in order)

```
1. Tool profile policy (minimal/coding/messaging/full)
2. Provider-specific profile policy
3. Global tool policy (config.tools.allow/deny)
4. Global provider tool policy
5. Agent-specific tool policy
6. Agent provider tool policy
7. Group/channel tool policy
8. Sandbox tool policy
9. Subagent tool policy
```

Then each tool is wrapped:
- `wrapToolWithBeforeToolCallHook()` - runs plugin hooks
- `wrapToolWithAbortSignal()` - respects abort signals

---

## 4. Tool Execution Path (Critical Integration Surface)

### From "agent wants to run a tool" to "tool result returned"

The Pi agent core emits events that are handled by `src/agents/pi-embedded-subscribe.handlers.tools.ts`:

```
Pi Agent Core decides to call a tool
  |
  v
AgentEvent: tool_execution_start
  -> handleToolExecutionStart()
  -> emitAgentEvent({ stream: "tool", data: { phase: "start", name, args } })
  |
  v
Tool.execute(toolCallId, params, signal, onUpdate) is called
  |
  v  (INTERCEPTION POINT: wrapToolWithBeforeToolCallHook)
  runBeforeToolCallHook() <- src/agents/pi-tools.before-tool-call.ts
    -> getGlobalHookRunner().runBeforeToolCall(event, ctx)
    -> If hook returns { block: true, blockReason: "..." }:
       -> throw Error(reason) -> tool result is error
    -> If hook returns { params: {...} }:
       -> params are merged/replaced before execution
  |
  v
  Actual tool execution (bash, read, write, etc.)
  |
  v
AgentEvent: tool_execution_end
  -> handleToolExecutionEnd()
  -> emitAgentEvent({ stream: "tool", data: { phase: "result", name, result } })
  |
  v
  after_tool_call hook (fire-and-forget, parallel)
  |
  v
Tool result returned to Pi agent -> LLM sees result -> next iteration
```

### Key File: `src/agents/pi-tools.before-tool-call.ts`

```typescript
export async function runBeforeToolCallHook(args: {
  toolName: string;
  params: unknown;
  toolCallId?: string;
  ctx?: { agentId?: string; sessionKey?: string };
}): Promise<{ blocked: true; reason: string } | { blocked: false; params: unknown }> {
  const hookRunner = getGlobalHookRunner();
  if (!hookRunner?.hasHooks("before_tool_call")) {
    return { blocked: false, params: args.params };
  }
  // ... runs hooks, can block or modify params
}
```

### Bash/Exec Tool (`src/agents/bash-tools.exec.ts`)

The `exec` tool is the most security-sensitive:
- Validates environment variables (blocks `LD_PRELOAD`, `DYLD_INSERT_LIBRARIES`, etc.)
- In sandbox mode: executes via `docker exec -i <container> sh -lc <command>`
- In host mode: spawns shell process with approval system
- Has safe-bins allowlist and approval tracking

---

## 5. Sandbox Isolation Model

### Security Boundaries

| Layer | Default | Configurable |
|-------|---------|-------------|
| Network | `none` (no network) | Can set to `bridge`/custom |
| Capabilities | `ALL` dropped | Can add specific caps |
| Root filesystem | Read-only | `readOnlyRoot: false` |
| Privileges | `no-new-privileges` | Cannot be overridden |
| Seccomp | Default Docker seccomp | Custom profile path |
| AppArmor | Default Docker apparmor | Custom profile path |
| Resource limits | Uncapped | `memory`, `cpus`, `pidsLimit`, `ulimits` |
| Workspace | Mounted at `/workspace` | `none`/`ro`/`rw` |
| DNS | Inherited | Custom DNS servers |

### Workspace Access Modes

- `none`: No host workspace mounted; agent works in sandbox-local `/workspace`
- `ro`: Host workspace mounted read-only; writes go to sandbox overlay
- `rw`: Host workspace mounted read-write (least secure)

### Tool Policy in Sandbox

Sandboxed agents have a separate `SandboxToolPolicy` that can further restrict which tools are available:

```typescript
type SandboxToolPolicy = {
  allow?: string[];  // Allowlist (if set, only these tools)
  deny?: string[];   // Denylist (these tools are blocked)
};
```

### What Happens Without Sandbox

When `sandbox.mode = "off"` (the default), all tool execution runs directly on the host. The exec tool has its own approval/allowlist system but there is no container isolation.

---

## 6. Extension/Plugin System

### Plugin Architecture (`src/plugins/`)

Plugins are npm packages loaded at gateway startup. They register capabilities via the `OpenClawPluginApi`:

```typescript
type OpenClawPluginApi = {
  registerTool(tool, opts?)        // Add agent tools
  registerHook(events, handler)    // Add event hooks
  registerHttpHandler(handler)     // Add HTTP middleware
  registerHttpRoute({ path, handler }) // Add HTTP routes
  registerChannel(registration)     // Add messaging channel
  registerGatewayMethod(method, handler) // Add gateway RPC method
  registerCli(registrar)           // Add CLI commands
  registerService(service)         // Add background services
  registerProvider(provider)       // Add LLM provider
  registerCommand(command)         // Add direct commands
  on(hookName, handler)            // Register typed lifecycle hooks
};
```

### Plugin Loading (`src/plugins/loader.ts`)

1. Discover plugins from: bundled, global install, workspace, config
2. Load each plugin module via `jiti` (TypeScript-compatible dynamic import)
3. Call `register()` / `activate()` on each plugin
4. All registrations go into `PluginRegistry`

### Plugin Registry (`src/plugins/registry.ts`)

Central store for all plugin registrations:
- `tools: PluginToolRegistration[]`
- `hooks: PluginHookRegistration[]`
- `typedHooks: TypedPluginHookRegistration[]` (type-safe hooks)
- `httpHandlers`, `httpRoutes`, `channels`, `providers`, `services`, `commands`, `cliRegistrations`

### Global Hook Runner (`src/plugins/hook-runner-global.ts`)

Singleton initialised at gateway startup:
```typescript
let globalHookRunner: HookRunner | null = null;
initializeGlobalHookRunner(registry); // Called once at startup
getGlobalHookRunner(); // Used throughout the codebase
```

---

## 7. Candidate Hook Points for AgentShield Interception

### 7.1 `before_tool_call` Hook (PRIMARY - Already Exists)

**Location:** `src/agents/pi-tools.before-tool-call.ts`
**When:** Before every tool execution
**Data available:**
- `toolName` (normalised): e.g., "exec", "read", "write", "edit", "browser", "message"
- `params`: Full tool parameters (command text, file paths, URLs, etc.)
- `agentId`, `sessionKey`

**Can block:** Yes - return `{ block: true, blockReason: "..." }`
**Can modify:** Yes - return `{ params: { ... } }` to override parameters

**This is the single most important hook for AgentShield.** It fires before:
- File writes (`write` tool, `edit` tool)
- Shell commands (`exec` tool - `params.command` contains the shell command)
- Network calls (indirectly via `exec` tool or `browser` tool)
- Browser automation (`browser` tool)
- Message sends (`message` tool)
- Session spawns (`sessions_spawn` tool)

### 7.2 `after_tool_call` Hook (Already Exists)

**Location:** `src/plugins/hooks.ts` -> `runAfterToolCall()`
**When:** After every tool execution completes
**Data available:**
- `toolName`, `params`, `result`, `error`, `durationMs`
- `agentId`, `sessionKey`

**Can block:** No (fire-and-forget)
**Use for:** Post-execution audit logging, telemetry

### 7.3 Agent Event System (Already Exists)

**Location:** `src/infra/agent-events.ts`
**When:** Throughout agent lifecycle
**Streams:**
- `lifecycle`: Agent start/end, compaction
- `tool`: Tool start/update/result (all phases)
- `assistant`: LLM text generation
- `error`: Error events

**Subscription:** `onAgentEvent(listener)` registers a global listener

**Data available per tool event:**
```typescript
{
  runId: string;
  seq: number;        // Monotonically increasing per run
  stream: "tool";
  ts: number;         // Timestamp
  sessionKey?: string;
  data: {
    phase: "start" | "update" | "result";
    name: string;     // Tool name
    toolCallId: string;
    args?: Record<string, unknown>;    // Only on "start"
    result?: unknown;                  // Only on "result"
    isError?: boolean;                 // Only on "result"
    meta?: string;                     // Human-readable summary
  }
}
```

### 7.4 Message Hooks (Already Exist)

- `message_received`: Inbound message before agent processing
- `message_sending`: Outbound message before delivery (can modify/cancel)
- `message_sent`: After message delivered (fire-and-forget)

### 7.5 Session Hooks (Already Exist)

- `session_start`: New session created
- `session_end`: Session completed
- `before_agent_start`: Before LLM call (can inject system prompt)
- `agent_end`: After agent run completes

### 7.6 Gateway Hooks (Already Exist)

- `gateway_start`: Gateway server started
- `gateway_stop`: Gateway server stopping

### 7.7 Exec Tool Specific: Approval System

**Location:** `src/infra/exec-approvals.ts`
**What:** The exec tool has a separate approval/allowlist system that can require user confirmation before running commands
**Integration potential:** AgentShield could act as an approval backend

### 7.8 Tool Display / Tool Summaries

**Location:** `src/agents/tool-display.ts`
**What:** Maps tool names + args to human-readable summaries
**Use for:** Better AgentShield UI descriptions

---

## 8. Existing Event/Logging System

### Structured Logging (`src/logging/`)

- Uses `tslog` for structured file logging
- Subsystem loggers: `createSubsystemLogger("agents/tools")`
- Log levels: `error`, `warn`, `info`, `debug`
- File output: `~/.openclaw/logs/` (default)
- Console capture: All `console.log/error` calls are intercepted and routed to structured logs

### Agent Events (`src/infra/agent-events.ts`)

In-process event bus with listener registration:
```typescript
emitAgentEvent({ runId, stream, data, sessionKey? })
onAgentEvent((evt: AgentEventPayload) => void)
```

Events are emitted at every critical point in the agent lifecycle.

### Diagnostic Events (`src/infra/diagnostic-events.ts`)

Separate diagnostic event system for operational telemetry (heartbeats, queue states, usage, etc.).

### Gateway WebSocket Logging (`src/gateway/ws-log.ts`)

Real-time streaming of agent events over WebSocket to connected clients (web UI, native apps).

---

## 9. Proposed OpenClaw-side Design for AgentShield Integration

### 9.1 Middleware/Hook Layer (Use Existing `before_tool_call`)

The `before_tool_call` plugin hook is the **ideal** interception point. An AgentShield plugin would:

1. Register as an OpenClaw plugin
2. Register a `before_tool_call` hook with high priority
3. On each tool call:
   a. Extract tool name, params, agent context
   b. Send event to AgentShield for analysis
   c. Await allow/block decision
   d. Return `{ block: true, blockReason }` or `{ blocked: false, params }`

### 9.2 Transport Options

| Transport | Latency | Reliability | Complexity |
|-----------|---------|-------------|------------|
| HTTP webhook | ~5-50ms local | Good | Low |
| Unix domain socket | ~1-5ms | Excellent | Medium |
| WebSocket | ~1-5ms (persistent) | Good | Medium |
| In-process (shared lib) | <1ms | Excellent | High |

**Recommended: HTTP webhook (local)** for initial implementation, with Unix socket as a performance upgrade path.

Rationale:
- OpenClaw and AgentShield run on the same machine in the target use case
- HTTP is universally understood, easy to debug, supports timeouts natively
- No shared memory or IPC complexity
- AgentShield already has HTTP infrastructure (it serves a web UI)

### 9.3 Pause/Resume Mechanism

The `before_tool_call` hook already supports this natively:

```typescript
// In the AgentShield plugin's before_tool_call handler:
async function beforeToolCall(event, ctx) {
  const decision = await fetch("http://localhost:AGENTSHIELD_PORT/api/v1/intercept", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      toolName: event.toolName,
      params: event.params,
      agentId: ctx.agentId,
      sessionKey: ctx.sessionKey,
      timestamp: Date.now(),
    }),
    signal: AbortSignal.timeout(TIMEOUT_MS),
  });

  if (!decision.ok) {
    // AgentShield unavailable -> apply timeout policy
    return null; // Allow by default
  }

  const result = await decision.json();
  if (result.action === "block") {
    return { block: true, blockReason: result.reason };
  }
  if (result.action === "modify") {
    return { params: result.modifiedParams };
  }
  return null; // Allow
}
```

The Pi agent loop is **already async** and waits for each tool's `execute()` to resolve. The hook runs inside the tool wrapper, so pausing is simply an async `await` on the HTTP call.

### 9.4 Timeout Handling

```
AGENTSHIELD_INTERCEPT_TIMEOUT_MS = 5000  (default)
```

Strategy:
- Use `AbortSignal.timeout(TIMEOUT_MS)` on the HTTP request
- On timeout: **auto-allow** (fail-open) to avoid blocking agent execution
- Log the timeout as a warning via AgentShield's own logging
- Configurable via OpenClaw config: `plugins.agentshield.timeoutMs`
- Configurable default policy: `plugins.agentshield.timeoutPolicy: "allow" | "block"`

### 9.5 Configuration

The plugin would be configured in OpenClaw's `config.yaml`:

```yaml
plugins:
  agentshield:
    enabled: true
    endpoint: "http://localhost:8741/api/v1/intercept"
    timeoutMs: 5000
    timeoutPolicy: allow
    # Which tool categories to intercept (default: all)
    intercept:
      - exec        # Shell commands
      - write       # File writes
      - edit        # File edits
      - browser     # Browser automation
      - message     # Outbound messages
      - "*"         # Or intercept everything
    # Tools to skip interception for (performance)
    skip:
      - read        # File reads are low-risk
      - session_status
```

### 9.6 Event Emission (for Audit Trail)

In addition to the blocking `before_tool_call` hook, the plugin should also register an `after_tool_call` hook to report outcomes:

```typescript
api.on("after_tool_call", async (event, ctx) => {
  // Fire-and-forget POST to AgentShield
  fetch(auditEndpoint, {
    method: "POST",
    body: JSON.stringify({
      type: "tool_result",
      toolName: event.toolName,
      params: event.params,
      result: event.result,
      error: event.error,
      durationMs: event.durationMs,
      agentId: ctx.agentId,
      sessionKey: ctx.sessionKey,
    }),
  }).catch(() => {}); // Best-effort
});
```

And lifecycle hooks for session context:

```typescript
api.on("session_start", async (event, ctx) => { /* notify AgentShield */ });
api.on("session_end", async (event, ctx) => { /* notify AgentShield */ });
api.on("before_agent_start", async (event, ctx) => { /* notify AgentShield */ });
api.on("agent_end", async (event, ctx) => { /* notify AgentShield */ });
```

---

## 10. Constraints (What Must NOT Change)

### Sandbox Isolation Must Be Preserved

- The sandbox Docker configuration must remain intact
- No new network ports should be exposed inside sandbox containers
- The `no-new-privileges` flag must not be removed
- AgentShield interception happens in the **OpenClaw host process**, not inside the sandbox container

### Tool Policy Chain Must Be Respected

- AgentShield's hook runs **after** tool policy filtering (tools already filtered out won't reach the hook)
- The hook must not override tool policy decisions (if a tool is denied by policy, it won't appear in the tool list at all)
- AgentShield can only block tools that are already policy-allowed

### Plugin Sandboxing

- The AgentShield plugin runs in the OpenClaw process with full access
- It must not introduce vulnerabilities (e.g., no eval of external input)
- HTTP calls to AgentShield should be localhost-only by default

### Performance Budget

- The `before_tool_call` hook is in the hot path of every tool execution
- Each tool call adds one round-trip to AgentShield
- Total added latency per tool call should be < 50ms (ideally < 10ms)
- LLM API calls typically take 1-30 seconds, so 10-50ms overhead is negligible
- But: agents may execute 10-50+ tools per session, so cumulative latency matters

### Backward Compatibility

- The AgentShield plugin should be opt-in (disabled by default)
- When disabled, zero overhead (the hook check is already O(1): `hasHooks("before_tool_call")`)
- No changes to existing OpenClaw API contracts or CLI behaviour

---

## Appendix A: Key Source Files Reference

| File | Purpose |
|------|---------|
| `src/agents/pi-tools.ts` | Tool array construction and filtering |
| `src/agents/pi-tools.before-tool-call.ts` | `before_tool_call` hook wrapper |
| `src/agents/pi-tools.policy.ts` | Tool policy resolution |
| `src/agents/tool-policy.ts` | Tool groups, profiles, allowlists |
| `src/agents/bash-tools.exec.ts` | Exec (bash) tool implementation |
| `src/agents/sandbox/docker.ts` | Sandbox container lifecycle |
| `src/agents/sandbox/config.ts` | Sandbox configuration resolution |
| `src/agents/sandbox/types.ts` | Sandbox type definitions |
| `src/agents/pi-embedded-runner/run.ts` | Main agent execution entry point |
| `src/agents/pi-embedded-subscribe.handlers.tools.ts` | Tool event handling |
| `src/plugins/types.ts` | Plugin API and hook type definitions |
| `src/plugins/hooks.ts` | Hook runner implementation |
| `src/plugins/hook-runner-global.ts` | Global hook runner singleton |
| `src/plugins/registry.ts` | Plugin registration store |
| `src/plugins/loader.ts` | Plugin discovery and loading |
| `src/infra/agent-events.ts` | Agent event bus |
| `src/security/audit.ts` | Security audit module |
| `src/gateway/hooks-mapping.ts` | Webhook hook mapping |
| `src/logging.ts` | Logging infrastructure |

## Appendix B: Plugin Hook Lifecycle Summary

```
                    GATEWAY STARTUP
                         |
                    gateway_start
                         |
          +--------------+--------------+
          |                             |
    Inbound Message              Cron/Heartbeat
          |                             |
    message_received                    |
          |                             |
    session_start (if new)              |
          |                             |
    before_agent_start                  |
          |                             |
    +-----+-----+                      |
    | Agent Loop |                      |
    |            |                      |
    | before_tool_call  <-- INTERCEPT   |
    |   (can block/modify)              |
    |            |                      |
    | Tool execution                    |
    |            |                      |
    | after_tool_call                   |
    |   (audit/log)                     |
    |            |                      |
    | tool_result_persist               |
    |   (can modify result)             |
    |            |                      |
    +-----+-----+                      |
          |                             |
    agent_end                           |
          |                             |
    message_sending  <-- CAN CANCEL     |
          |                             |
    message_sent                        |
          |                             |
    session_end (if complete)           |
          |                             |
          +--------------+--------------+
                         |
                    gateway_stop
```

## Appendix C: Exec Tool Security Model

The exec tool (`src/agents/bash-tools.exec.ts`) has its own layered security:

1. **Environment variable blocklist**: Prevents injection via `LD_PRELOAD`, `DYLD_INSERT_LIBRARIES`, `NODE_OPTIONS`, `BASH_ENV`, etc.
2. **PATH modification blocked** on host (prevents binary hijacking)
3. **Safe-bins allowlist**: Configurable list of pre-approved binaries
4. **Approval system**: Commands can require explicit user approval
5. **Sandbox execution**: In sandbox mode, commands run inside Docker container via `docker exec`
6. **Timeout**: Commands have configurable timeout
7. **Background process management**: Background processes are tracked and can be killed

AgentShield's `before_tool_call` hook fires **before** all of this, giving it first-mover advantage on blocking.
