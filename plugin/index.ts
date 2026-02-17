import type { OpenClawPluginApi } from "openclaw/plugin-sdk";

import { CircuitBreaker } from "./src/circuit-breaker.js";
import { AgentShieldClient } from "./src/client.js";
import { parseConfig } from "./src/config.js";
import {
  buildAuditReport,
  buildEvaluationRequest,
  buildLifecycleEvent,
} from "./src/event-builder.js";

/** Max age (ms) for entries in the pending evaluations correlation map. */
const CORRELATION_TTL_MS = 60_000;

/** Apply the configured timeout/failure policy. */
function applyTimeoutPolicy(
  policy: "allow" | "block" | "log",
): { block: true; blockReason: string } | null {
  if (policy === "block") {
    return {
      block: true,
      blockReason: "AgentShield unavailable (fail-closed policy)",
    };
  }
  return null; // "allow" and "log" both permit execution
}

/**
 * Find and remove the correlation event_id for a completed tool call.
 *
 * Best-effort: if no match is found the audit report is still sent
 * with a fresh event_id as correlation_id.
 */
function findCorrelationId(
  pending: Map<string, { eventId: string; timestamp: number }>,
  ctx: { sessionKey?: string; toolName: string },
  event: { toolName: string },
): string | null {
  const now = Date.now();

  // Clean stale entries while iterating
  for (const [key, entry] of pending) {
    if (now - entry.timestamp > CORRELATION_TTL_MS) {
      pending.delete(key);
      continue;
    }
    if (
      key.includes(ctx.sessionKey ?? "") &&
      key.includes(event.toolName)
    ) {
      pending.delete(key);
      return entry.eventId;
    }
  }
  return null;
}

const plugin = {
  id: "agentshield",
  name: "AgentShield",
  description: "Real-time security evaluation for AI agent tool calls",

  register(api: OpenClawPluginApi) {
    const config = parseConfig(
      api.pluginConfig as Record<string, unknown> | undefined,
    );

    if (!config.enabled) {
      api.logger.info("AgentShield plugin disabled");
      return;
    }

    const client = new AgentShieldClient(config, api.logger);
    const circuitBreaker = new CircuitBreaker({
      failureThreshold: config.circuit_breaker.failure_threshold,
      recoveryIntervalMs: config.circuit_breaker.recovery_interval_ms,
    });
    const skipSet = new Set(config.skip);
    const interceptSet =
      config.intercept.length > 0 ? new Set(config.intercept) : null;

    // Correlation map: toolCallKey -> { eventId, timestamp }
    const pendingEvaluations = new Map<
      string,
      { eventId: string; timestamp: number }
    >();

    // ---- before_tool_call: synchronous evaluation ----
    api.on(
      "before_tool_call",
      async (event, ctx) => {
        if (skipSet.has(event.toolName)) {
          return undefined;
        }

        if (interceptSet && !interceptSet.has(event.toolName)) {
          return undefined;
        }

        if (circuitBreaker.isOpen()) {
          api.logger.warn(
            `AgentShield circuit breaker open, applying ${config.timeout_policy} policy`,
          );
          return applyTimeoutPolicy(config.timeout_policy) ?? undefined;
        }

        const request = buildEvaluationRequest(event, ctx);
        const correlationKey = `${ctx.sessionKey ?? ""}:${event.toolName}:${Date.now()}`;

        try {
          const response = await client.evaluate(request);
          circuitBreaker.recordSuccess();

          // Store for audit correlation
          pendingEvaluations.set(correlationKey, {
            eventId: request.event_id,
            timestamp: Date.now(),
          });

          if (response.action === "block") {
            api.logger.warn(
              `AgentShield blocked ${event.toolName}: ${response.reason ?? "no reason"}`,
            );
            return {
              block: true,
              blockReason:
                response.reason ?? "Blocked by AgentShield",
            };
          }

          if (response.action === "log" && response.alerts?.length) {
            api.logger.info(
              `AgentShield logged ${response.alerts.length} alert(s) for ${event.toolName}`,
            );
          }

          return undefined; // allow
        } catch (err) {
          circuitBreaker.recordFailure();
          api.logger.warn(
            `AgentShield evaluation failed: ${String(err)}`,
          );
          return applyTimeoutPolicy(config.timeout_policy) ?? undefined;
        }
      },
      { priority: -100 },
    ); // Run early, before other plugins modify params

    // ---- after_tool_call: fire-and-forget audit ----
    api.on("after_tool_call", async (event, ctx) => {
      const correlationId = findCorrelationId(
        pendingEvaluations,
        ctx,
        event,
      );
      const report = buildAuditReport(
        event,
        ctx,
        correlationId ?? event.toolName,
      );
      client.sendAudit(report);
    });

    // ---- Lifecycle hooks ----
    api.on("session_start", async (event, ctx) => {
      client.sendLifecycle(
        buildLifecycleEvent(
          "session_start",
          { agentId: ctx.agentId, sessionKey: ctx.sessionId },
          {
            session_id: event.sessionId,
            resumed_from: event.resumedFrom ?? null,
          },
        ),
      );
    });

    api.on("session_end", async (event, ctx) => {
      client.sendLifecycle(
        buildLifecycleEvent(
          "session_end",
          { agentId: ctx.agentId, sessionKey: ctx.sessionId },
          {
            session_id: event.sessionId,
            message_count: event.messageCount,
            duration_ms: event.durationMs ?? null,
          },
        ),
      );
    });

    api.on("before_agent_start", async (_event, ctx) => {
      client.sendLifecycle(
        buildLifecycleEvent("agent_start", ctx, {}),
      );
      return undefined; // Do not modify system prompt
    });

    api.on("agent_end", async (event, ctx) => {
      client.sendLifecycle(
        buildLifecycleEvent("agent_end", ctx, {
          success: event.success,
          error: event.error ?? null,
          duration_ms: event.durationMs ?? null,
        }),
      );
    });

    // Startup health check (non-blocking)
    client
      .healthCheck()
      .then((ok) => {
        if (ok) {
          api.logger.info(
            `AgentShield reachable at ${config.endpoint}`,
          );
        } else {
          api.logger.warn(
            `AgentShield not reachable at ${config.endpoint} (will retry on first tool call)`,
          );
        }
      })
      .catch(() => {
        api.logger.warn("AgentShield health check failed");
      });
  },
};

export default plugin;
