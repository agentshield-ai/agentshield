# OpenClaw Hook Layer Implementation Plan

**Author**: OpenClaw Architect
**Date**: 2026-02-07
**Task**: #5 -- PHASE 3b
**Depends on**: Integration Contract (Task #3)
**Blocks**: Task #6 (Implementation)

---

## 1. Overview

This plan describes the implementation of an OpenClaw workspace plugin (`agentshield`) that intercepts tool calls via the existing `before_tool_call` and `after_tool_call` plugin hooks, forwards evaluation requests to the AgentShield real-time receiver over HTTP, and returns allow/block decisions to the agent loop.

### Scope

- A new OpenClaw extension at `extensions/agentshield/`
- Registers `before_tool_call` and `after_tool_call` hooks via `api.on()`
- HTTP client sending to AgentShield's `/api/v1/evaluate` and `/api/v1/audit`
- Circuit breaker for fault tolerance
- Event type normalisation per the integration contract (Section 2.4)
- Lifecycle event emission via `session_start`, `session_end`, `before_agent_start`, `agent_end` hooks
- Configuration via OpenClaw's plugin config system (`api.pluginConfig`)
- Comprehensive test suite using vitest

### Out of Scope

- AgentShield server implementation (Task #7)
- Changes to OpenClaw core (no upstream modifications needed)
- The `modify` action (reserved for future contract versions)

---

## 2. Key Findings from Architecture Analysis

### 2.1 Hook Availability

| Hook | Status | Execution Model | Notes |
|------|--------|-----------------|-------|
| `before_tool_call` | **Wired** -- called via `wrapToolWithBeforeToolCallHook` in `pi-tools.ts` | Sequential (modifying) | Returns `{ block?, blockReason?, params? }` |
| `after_tool_call` | **Defined but NOT wired** -- `runAfterToolCall` exists in hook runner but is never invoked | Parallel (fire-and-forget) | Needs wiring in tool execution path |
| `session_start` | Wired | Parallel | Available for lifecycle events |
| `session_end` | Wired | Parallel | Available for lifecycle events |
| `before_agent_start` | Wired | Sequential (modifying) | Available for lifecycle events |
| `agent_end` | Wired | Parallel | Available for lifecycle events |

**Critical finding**: The `after_tool_call` hook runner method exists but is never invoked in the current tool execution pipeline. We have two options:

1. **Option A (preferred)**: Submit a small upstream PR to OpenClaw adding `runAfterToolCall` invocation in the tool wrapper (analogous to the existing `before_tool_call` wiring). This is a 10-15 line change.
2. **Option B (fallback)**: Skip the `after_tool_call` audit path in v1. The `before_tool_call` hook provides the core security value. Audit data can be reconstructed from AgentShield's event store. This has no security impact but reduces observability.

**Recommendation**: Proceed with Option A as a prerequisite. If upstream PR is delayed, fall back to Option B and deliver the core blocking functionality without audit reports.

### 2.2 Hook Context Available

From `PluginHookToolContext`:
- `toolName: string` -- normalised tool name (e.g., `exec`, not `bash`)
- `agentId?: string` -- agent identifier
- `sessionKey?: string` -- session key

From `PluginHookBeforeToolCallEvent`:
- `toolName: string` -- same as context
- `params: Record<string, unknown>` -- full tool parameters

From `PluginHookAfterToolCallEvent`:
- `toolName: string`
- `params: Record<string, unknown>`
- `result?: unknown`
- `error?: string`
- `durationMs?: number`

**Not available**: `toolCallId` (exists in wrapper but not passed to hook context), `workingDir`.

### 2.3 Plugin Registration Pattern

OpenClaw extensions follow this structure:
```
extensions/agentshield/
  index.ts              # Plugin definition with register(api)
  package.json          # Package manifest with openclaw.extensions
  openclaw.plugin.json  # Plugin config schema
  src/
    client.ts           # HTTP client for AgentShield API
    circuit-breaker.ts  # Circuit breaker state machine
    config.ts           # Config parsing and validation
    event-builder.ts    # Build evaluation/audit request payloads
    normalise.ts        # Tool name -> event type mapping
    types.ts            # TypeScript type definitions
```

The plugin imports types from `openclaw/plugin-sdk` and exports a default object with `{ id, name, description, configSchema, register }`.

---

## 3. File-by-File Implementation Plan

### 3.1 `extensions/agentshield/package.json`

```json
{
  "name": "@openclaw/agentshield",
  "version": "0.1.0",
  "description": "AgentShield real-time security evaluation for tool calls",
  "type": "module",
  "devDependencies": {
    "openclaw": "workspace:*"
  },
  "openclaw": {
    "extensions": ["./index.ts"]
  }
}
```

No runtime dependencies -- uses Node's built-in `fetch` API (available in Node 18+).

### 3.2 `extensions/agentshield/openclaw.plugin.json`

```json
{
  "id": "agentshield",
  "configSchema": {
    "type": "object",
    "additionalProperties": false,
    "properties": {
      "enabled": { "type": "boolean", "default": true },
      "endpoint": { "type": "string", "default": "http://127.0.0.1:8432/api/v1/evaluate" },
      "auth_token": { "type": "string" },
      "timeout_ms": { "type": "number", "default": 50, "minimum": 5, "maximum": 5000 },
      "timeout_policy": { "type": "string", "enum": ["allow", "block", "log"], "default": "allow" },
      "intercept": {
        "type": "array",
        "items": { "type": "string" },
        "default": ["exec", "write", "edit", "browser", "message", "sessions_spawn"]
      },
      "skip": {
        "type": "array",
        "items": { "type": "string" },
        "default": ["read", "session_status"]
      },
      "circuit_breaker": {
        "type": "object",
        "properties": {
          "failure_threshold": { "type": "number", "default": 3 },
          "recovery_interval_ms": { "type": "number", "default": 30000 }
        }
      }
    }
  }
}
```

### 3.3 `extensions/agentshield/src/types.ts`

Type definitions for the plugin's internal use:

```typescript
// Plugin configuration (parsed from api.pluginConfig)
export type AgentShieldConfig = {
  enabled: boolean;
  endpoint: string;
  auth_token: string;
  timeout_ms: number;
  timeout_policy: "allow" | "block" | "log";
  intercept: string[];
  skip: string[];
  circuit_breaker: {
    failure_threshold: number;
    recovery_interval_ms: number;
  };
};

// Evaluation request payload (matches contract Section 2.1)
export type EvaluationRequest = {
  event_id: string;
  timestamp: string;
  event_type: string;
  tool_name: string;
  source: "openclaw";
  command: string | null;
  params: Record<string, unknown>;
  agent_id: string | null;
  session_id: string | null;
  working_dir: string | null;
  data: Record<string, unknown>;
};

// Evaluation response payload (matches contract Section 2.2)
export type EvaluationResponse = {
  action: "allow" | "block" | "log";
  event_id: string;
  alerts?: Array<{
    rule_id: string;
    rule_name: string;
    level: "low" | "medium" | "high" | "critical";
    description?: string;
  }>;
  reason?: string;
};

// Audit report payload (matches contract Section 2.3)
export type AuditReport = {
  event_id: string;
  correlation_id: string;
  timestamp: string;
  event_type: "tool_result";
  tool_name: string;
  source: "openclaw";
  result_summary: string | null;
  is_error: boolean;
  error_message: string | null;
  duration_ms: number;
  agent_id: string | null;
  session_id: string | null;
  working_dir: string | null;
  data: Record<string, unknown>;
};

// Lifecycle event payload (matches contract Section 15)
export type LifecycleEvent = {
  event_id: string;
  timestamp: string;
  event_type: string;
  source: "openclaw";
  agent_id: string | null;
  session_id: string | null;
  data: Record<string, unknown>;
};

// Circuit breaker states
export type CircuitBreakerState = "closed" | "open" | "half-open";
```

### 3.4 `extensions/agentshield/src/config.ts`

Parse and validate plugin configuration from `api.pluginConfig`:

```typescript
export function parseConfig(raw: Record<string, unknown> | undefined): AgentShieldConfig
```

- Applies defaults for all optional fields
- Validates `endpoint` is a valid URL
- Validates `timeout_policy` is one of the three allowed values
- Returns a frozen config object

### 3.5 `extensions/agentshield/src/normalise.ts`

Tool name to event type mapping (contract Section 2.4):

```typescript
export function normaliseToolCall(toolName: string, params: Record<string, unknown>): {
  event_type: string;
  command: string | null;
}
```

Mapping table:

| `toolName` | `event_type` | `command` |
|------------|-------------|-----------|
| `exec` | `tool_call` | `params.command` |
| `write` | `file_write` | `"Write: ${params.path \|\| params.filePath}"` |
| `read` | `file_read` | `"Read: ${params.path \|\| params.filePath}"` |
| `edit` | `file_edit` | `"Edit: ${params.path \|\| params.filePath}"` |
| `browser` | `browser_action` | `"${params.action}: ${params.url \|\| ''}"` |
| `message` | `message_send` | `"Message to ${params.channel}"` |
| `sessions_spawn` | `session_spawn` | `"Spawn: ${params.agentId}"` |
| Other | `tool_call` | `toolName` |

Note: Since `before_tool_call` receives params AFTER `normalizeToolParams()` has run (which maps `file_path` -> `path`), the primary key for file paths is `params.path`. We also check `params.filePath` as a fallback for direct API callers.

### 3.6 `extensions/agentshield/src/event-builder.ts`

Build request/audit payloads from hook event and context data:

```typescript
import { randomUUID } from "node:crypto";

export function buildEvaluationRequest(
  event: { toolName: string; params: Record<string, unknown> },
  ctx: { agentId?: string; sessionKey?: string; toolName: string },
): EvaluationRequest

export function buildAuditReport(
  event: { toolName: string; params: Record<string, unknown>; result?: unknown; error?: string; durationMs?: number },
  ctx: { agentId?: string; sessionKey?: string; toolName: string },
  correlationId: string,
): AuditReport

export function buildLifecycleEvent(
  eventType: string,
  ctx: { agentId?: string; sessionKey?: string },
  data: Record<string, unknown>,
): LifecycleEvent
```

Key design decisions:
- `event_id` uses `crypto.randomUUID()` (available in Node 16+)
- `timestamp` uses `new Date().toISOString()`
- `result_summary` is truncated to 1000 characters
- `working_dir` is set to `null` (not available in hook context)
- The evaluation request's `event_id` is stored and passed as `correlation_id` to the audit report

### 3.7 `extensions/agentshield/src/circuit-breaker.ts`

Simple three-state circuit breaker:

```typescript
export class CircuitBreaker {
  constructor(opts: { failureThreshold: number; recoveryIntervalMs: number })

  isOpen(): boolean
  recordSuccess(): void
  recordFailure(): void
  getState(): CircuitBreakerState
}
```

State transitions:
- `closed` -> `open`: After `failureThreshold` consecutive failures
- `open` -> `half-open`: After `recoveryIntervalMs` elapsed since last failure
- `half-open` -> `closed`: On success
- `half-open` -> `open`: On failure

No external dependencies -- pure state machine using `Date.now()`.

### 3.8 `extensions/agentshield/src/client.ts`

HTTP client for communicating with AgentShield:

```typescript
export class AgentShieldClient {
  constructor(config: AgentShieldConfig, logger: PluginLogger)

  async evaluate(request: EvaluationRequest): Promise<EvaluationResponse>
  sendAudit(report: AuditReport): void   // fire-and-forget
  sendLifecycle(event: LifecycleEvent): void   // fire-and-forget
  async healthCheck(): Promise<boolean>
}
```

Implementation details:
- Uses Node's built-in `fetch` with `AbortSignal.timeout(config.timeout_ms)` for the evaluate path
- Sets headers: `Content-Type: application/json`, `X-AgentShield-Version: 1.0.0`, `X-AgentShield-Auth: <token>`
- The `evaluate` method throws on any non-200 response (caught by caller)
- `sendAudit` and `sendLifecycle` use fire-and-forget `fetch().catch(() => {})` with no timeout constraint (they are non-blocking)
- Response validation: checks `action` field is one of `allow`, `block`, `log`

### 3.9 `extensions/agentshield/index.ts`

The main plugin entry point:

```typescript
import type { OpenClawPluginApi } from "openclaw/plugin-sdk";

const plugin = {
  id: "agentshield",
  name: "AgentShield",
  description: "Real-time security evaluation for AI agent tool calls",

  register(api: OpenClawPluginApi) {
    const config = parseConfig(api.pluginConfig);

    if (!config.enabled) {
      api.logger.info("AgentShield plugin disabled");
      return;
    }

    const client = new AgentShieldClient(config, api.logger);
    const circuitBreaker = new CircuitBreaker(config.circuit_breaker);
    const skipSet = new Set(config.skip);
    const interceptSet = config.intercept.length > 0 ? new Set(config.intercept) : null;

    // Store evaluation event_ids for correlation with audit reports
    const pendingEvaluations = new Map<string, string>(); // toolCallKey -> event_id

    // ---- before_tool_call: synchronous evaluation ----
    api.on("before_tool_call", async (event, ctx) => {
      // Skip tools in the skip list
      if (skipSet.has(event.toolName)) {
        return null;
      }

      // If intercept list is configured, only intercept those tools
      if (interceptSet && !interceptSet.has(event.toolName)) {
        return null;
      }

      // Skip if circuit breaker is open
      if (circuitBreaker.isOpen()) {
        api.logger.debug(`AgentShield circuit breaker open, applying ${config.timeout_policy}`);
        return applyTimeoutPolicy(config.timeout_policy);
      }

      const request = buildEvaluationRequest(event, ctx);
      const correlationKey = `${ctx.sessionKey}:${event.toolName}:${Date.now()}`;

      try {
        const response = await client.evaluate(request);
        circuitBreaker.recordSuccess();

        // Store event_id for audit correlation
        pendingEvaluations.set(correlationKey, request.event_id);

        if (response.action === "block") {
          api.logger.warn(
            `AgentShield blocked ${event.toolName}: ${response.reason ?? "no reason"}`,
          );
          return { block: true, blockReason: response.reason ?? "Blocked by AgentShield" };
        }

        if (response.action === "log" && response.alerts?.length) {
          api.logger.info(
            `AgentShield logged ${response.alerts.length} alert(s) for ${event.toolName}`,
          );
        }

        return null; // allow

      } catch (err) {
        circuitBreaker.recordFailure();
        api.logger.warn(`AgentShield evaluation failed: ${String(err)}`);
        return applyTimeoutPolicy(config.timeout_policy);
      }
    }, { priority: -100 }); // Run early, before other plugins modify params

    // ---- after_tool_call: fire-and-forget audit ----
    api.on("after_tool_call", async (event, ctx) => {
      // Find the correlation ID from the pending evaluation
      // Use approximate key matching (same session + tool name within last 60s)
      const correlationId = findCorrelationId(pendingEvaluations, ctx, event);

      if (correlationId) {
        const report = buildAuditReport(event, ctx, correlationId);
        client.sendAudit(report);
      }
    });

    // ---- Lifecycle hooks ----
    api.on("session_start", async (event, ctx) => {
      client.sendLifecycle(buildLifecycleEvent("session_start", ctx, {
        session_id: event.sessionId,
        resumed_from: event.resumedFrom ?? null,
      }));
    });

    api.on("session_end", async (event, ctx) => {
      client.sendLifecycle(buildLifecycleEvent("session_end", ctx, {
        session_id: event.sessionId,
        message_count: event.messageCount,
        duration_ms: event.durationMs ?? null,
      }));
    });

    api.on("before_agent_start", async (event, ctx) => {
      client.sendLifecycle(buildLifecycleEvent("agent_start", ctx, {}));
      return undefined; // Do not modify system prompt
    });

    api.on("agent_end", async (event, ctx) => {
      client.sendLifecycle(buildLifecycleEvent("agent_end", ctx, {
        success: event.success,
        error: event.error ?? null,
        duration_ms: event.durationMs ?? null,
      }));
    });

    // Startup health check
    client.healthCheck().then((ok) => {
      if (ok) {
        api.logger.info("AgentShield reachable at " + config.endpoint);
      } else {
        api.logger.warn("AgentShield not reachable at " + config.endpoint + " (will retry on first tool call)");
      }
    }).catch(() => {
      api.logger.warn("AgentShield health check failed");
    });
  },
};

export default plugin;
```

### 3.10 Helper: `applyTimeoutPolicy`

```typescript
function applyTimeoutPolicy(
  policy: "allow" | "block" | "log",
): { block: true; blockReason: string } | null {
  switch (policy) {
    case "block":
      return { block: true, blockReason: "AgentShield unavailable (fail-closed policy)" };
    case "allow":
    case "log":
      return null;
  }
}
```

### 3.11 Helper: `findCorrelationId`

```typescript
function findCorrelationId(
  pending: Map<string, string>,
  ctx: { sessionKey?: string; toolName: string },
  event: { toolName: string },
): string | null {
  // Find most recent matching entry and remove it
  for (const [key, eventId] of pending) {
    if (key.includes(ctx.sessionKey ?? "") && key.includes(event.toolName)) {
      pending.delete(key);
      return eventId;
    }
  }
  return null;
}
```

Note: This correlation approach is best-effort. If `after_tool_call` is not yet wired (Option B), this code simply never runs.

---

## 4. `after_tool_call` Wiring (Option A -- Upstream PR)

The `after_tool_call` hook type and runner exist but the tool execution pipeline does not invoke it. To wire it, we need a small change in the tool wrapper, analogous to the existing `before_tool_call` pattern.

### 4.1 Proposed Change Location

File: `src/agents/pi-tools.before-tool-call.ts` (or a new sibling file)

### 4.2 Proposed Change

Add an `after_tool_call` invocation in `wrapToolWithBeforeToolCallHook`:

```typescript
// In wrapToolWithBeforeToolCallHook, modify the execute wrapper:
execute: async (toolCallId, params, signal, onUpdate) => {
  const outcome = await runBeforeToolCallHook({
    toolName,
    params,
    toolCallId,
    ctx,
  });
  if (outcome.blocked) {
    throw new Error(outcome.reason);
  }

  const startTime = Date.now();
  let result: unknown;
  let error: string | undefined;

  try {
    result = await execute(toolCallId, outcome.params, signal, onUpdate);
    return result;
  } catch (err) {
    error = String(err);
    throw err;
  } finally {
    // Fire-and-forget after_tool_call hook
    const hookRunner = getGlobalHookRunner();
    if (hookRunner?.hasHooks("after_tool_call")) {
      const durationMs = Date.now() - startTime;
      hookRunner.runAfterToolCall(
        { toolName, params: isPlainObject(outcome.params) ? outcome.params as Record<string, unknown> : {}, result, error, durationMs },
        { toolName, agentId: ctx?.agentId, sessionKey: ctx?.sessionKey },
      ).catch((err) => {
        log.warn(`after_tool_call hook failed: ${String(err)}`);
      });
    }
  }
}
```

This is approximately 15 lines of new code in the existing wrapper function. It:
- Captures start time before execution
- Captures result/error via try/catch/finally
- Invokes `runAfterToolCall` in the `finally` block (always runs)
- Uses `.catch()` to ensure hook errors never propagate to the agent loop

### 4.3 Size and Risk

- **Lines changed**: ~15-20
- **Risk**: Very low -- the `finally` block ensures tool execution is never affected
- **Backward compatible**: Plugins not registering `after_tool_call` see zero overhead (the `hasHooks` guard exits immediately)

---

## 5. Testing Strategy

### 5.1 Unit Tests

All source files in `src/` get corresponding test files:

| Source | Test | Key Scenarios |
|--------|------|---------------|
| `src/config.ts` | `src/config.test.ts` | Default values, validation errors, partial config |
| `src/normalise.ts` | `src/normalise.test.ts` | All 7 tool mappings, unknown tool fallback, empty params |
| `src/event-builder.ts` | `src/event-builder.test.ts` | UUID format, timestamp format, truncation at 1000 chars |
| `src/circuit-breaker.ts` | `src/circuit-breaker.test.ts` | State transitions: closed->open, open->half-open, half-open->closed, half-open->open |
| `src/client.ts` | `src/client.test.ts` | Success, timeout, connection refused, 401, 500, invalid JSON |
| `index.ts` | `index.test.ts` | Full integration: register, hook invocation, block flow, allow flow, circuit breaker, disabled config |

### 5.2 Testing Approach

- Use vitest (OpenClaw's test runner)
- Mock `fetch` using `vi.fn()` for HTTP client tests
- Use the `setup()` pattern from existing plugin tests (see `voice-call.plugin.test.ts`) to create a mock `OpenClawPluginApi`
- Test the `before_tool_call` handler by capturing the registered handler and invoking it directly with mock events

### 5.3 Contract Tests

Shared test fixture from integration contract Section 13.2:

```typescript
// test/contract.test.ts
describe("contract compliance", () => {
  it("sends valid evaluation request for exec tool", () => {
    const request = buildEvaluationRequest(
      { toolName: "exec", params: { command: "ls -la" } },
      { agentId: "agent-1", sessionKey: "session-1", toolName: "exec" },
    );
    // Verify all required fields present
    expect(request.event_id).toMatch(/^[0-9a-f-]{36}$/);
    expect(request.event_type).toBe("tool_call");
    expect(request.tool_name).toBe("exec");
    expect(request.source).toBe("openclaw");
    expect(request.command).toBe("ls -la");
  });

  it("handles block response correctly", async () => {
    // Mock fetch to return block response
    // Invoke before_tool_call handler
    // Verify it returns { block: true, blockReason: ... }
  });

  it("handles timeout with fail-open policy", async () => {
    // Mock fetch to timeout
    // Invoke before_tool_call handler with timeout_policy: "allow"
    // Verify it returns null (allow)
  });
});
```

---

## 6. Configuration and Deployment

### 6.1 Enabling the Plugin

Users add to their OpenClaw `config.yaml`:

```yaml
plugins:
  agentshield:
    enabled: true
    auth_token: "<shared-secret-from-agentshield>"
```

All other fields have sensible defaults per the `openclaw.plugin.json` schema.

### 6.2 Plugin Discovery

OpenClaw discovers workspace plugins via the `extensions/` directory. Adding `extensions/agentshield/` with the proper `package.json` (containing `openclaw.extensions`) is sufficient for automatic discovery and loading.

### 6.3 Zero Overhead When Disabled

When `enabled: false` or the plugin is not configured:
- The `register` function returns early
- No hooks are registered
- No HTTP connections are made
- Zero runtime cost

When enabled but AgentShield is not running:
- The health check logs a warning
- The first tool call attempts evaluation, fails, and records a circuit breaker failure
- After `failure_threshold` failures, the circuit breaker opens
- While open, tool calls proceed immediately with `timeout_policy` (default: allow)
- The circuit breaker probes every `recovery_interval_ms`

---

## 7. Implementation Order

### Phase 1: Core types and utilities (no external deps)
1. `src/types.ts` -- type definitions
2. `src/config.ts` + `src/config.test.ts` -- config parsing
3. `src/normalise.ts` + `src/normalise.test.ts` -- event type mapping
4. `src/event-builder.ts` + `src/event-builder.test.ts` -- request/response builders
5. `src/circuit-breaker.ts` + `src/circuit-breaker.test.ts` -- state machine

### Phase 2: HTTP client
6. `src/client.ts` + `src/client.test.ts` -- HTTP client with mocked fetch

### Phase 3: Plugin registration
7. `index.ts` + `index.test.ts` -- full plugin with hook registration
8. `package.json` + `openclaw.plugin.json` -- plugin manifest

### Phase 4: `after_tool_call` wiring (upstream PR)
9. Modify `pi-tools.before-tool-call.ts` to invoke `runAfterToolCall` in `finally` block
10. Test the wiring with a simple test that registers an `after_tool_call` hook and verifies it is called

### Phase 5: Contract compliance tests
11. Shared test fixtures validating request/response schemas match the contract

---

## 8. Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| `after_tool_call` not wired upstream | Audit reports not sent | Fall back to Option B (skip audit in v1) |
| `fetch` not available in older Node | Plugin fails to load | Node 18+ is required by OpenClaw already |
| Circuit breaker leaks memory (pending evaluations map) | Memory growth | Add TTL-based cleanup of stale entries (60s) |
| Hook priority conflicts with other plugins | AgentShield sees modified params | Use `priority: -100` to run early |
| AgentShield latency exceeds budget | Tool calls delayed | 50ms timeout + circuit breaker ensures bounded delay |

---

## 9. Estimated Effort

| Component | Lines (approx) | Complexity |
|-----------|----------------|------------|
| `src/types.ts` | 80 | Low |
| `src/config.ts` | 60 | Low |
| `src/normalise.ts` | 50 | Low |
| `src/event-builder.ts` | 80 | Low |
| `src/circuit-breaker.ts` | 60 | Low |
| `src/client.ts` | 80 | Medium |
| `index.ts` | 120 | Medium |
| Test files (6) | 400 | Medium |
| `after_tool_call` wiring | 20 | Low |
| Package manifests | 40 | Low |
| **Total** | **~990** | |

---

## 10. Open Questions

1. **Plugin location**: Should the plugin live in `extensions/agentshield/` within the OpenClaw repo, or as a separate npm package? Recommendation: start as a workspace extension for ease of development, extract later if needed.

2. **`after_tool_call` wiring PR timing**: Should we submit the upstream PR before or in parallel with plugin development? Recommendation: in parallel -- the plugin works without it (just without audit reports).

3. **Lifecycle events priority**: The contract defines lifecycle events (Section 15) but they are fire-and-forget. Should we implement them in the initial version? Recommendation: yes, they are low effort and high value for AgentShield's triage context.
