import { randomUUID } from "node:crypto";

import type { OpenClawPluginApi } from "openclaw/plugin-sdk";

import { CircuitBreaker } from "./src/circuit-breaker.js";
import { AgentShieldClient } from "./src/client.js";
import { parseConfig } from "./src/config.js";
import {
  buildAuditReport,
  buildEvaluationRequest,
  buildLifecycleEvent,
} from "./src/event-builder.js";

import type { EvaluationResponse } from "./src/types.js";

/** Max age (ms) for entries in the pending evaluations correlation map. */
const CORRELATION_TTL_MS = 60_000;

/** Severity emoji mapping */
const SEVERITY_EMOJI: Record<string, string> = {
  critical: "🔴",
  high: "🟠",
  medium: "🟡",
  low: "🔵",
};

/**
 * Send a security alert notification as a system event.
 * The agent will see this and relay it to the user naturally.
 */
const SEVERITY_ORDER: Record<string, number> = {
  low: 0, medium: 1, high: 2, critical: 3,
};
const NOTIFY_THRESHOLD: Record<string, number> = {
  all: 0, high: 2, critical: 3, none: 999,
};

function notifyAlert(
  api: OpenClawPluginApi,
  response: EvaluationResponse,
  toolName: string,
  action: "blocked" | "logged",
  sessionKey: string,
  notifyLevel: string = "high",
): void {
  try {
    const alerts = response.alerts ?? [];
    if (alerts.length === 0) return;

    // Check if any alert meets the notification threshold
    const threshold = NOTIFY_THRESHOLD[notifyLevel] ?? 2;
    const meetsThreshold = alerts.some(
      (a) => (SEVERITY_ORDER[a.severity] ?? 0) >= threshold,
    );
    if (!meetsThreshold) return;

    // Show the highest-severity alert, not merely the first one: alerts[0]
    // may be a low-severity match while a later alert is the one that
    // crossed the notification threshold.
    const topAlert = alerts.reduce((best, a) =>
      (SEVERITY_ORDER[a.severity] ?? 0) > (SEVERITY_ORDER[best.severity] ?? 0)
        ? a
        : best,
    );
    const emoji = SEVERITY_EMOJI[topAlert.severity] ?? "⚪";
    const triage = response.triage_results?.[0];

    const parts: string[] = [
      `🛡️ AgentShield Alert — ${action} (${topAlert.severity})`,
      `${emoji} ${topAlert.rule_name}`,
      `Tool: ${toolName}`,
    ];

    // Include matched command if available
    const command = topAlert.matched_fields?.command;
    if (command && typeof command === "string") {
      const truncated = command.length > 200 ? command.slice(0, 200) + "..." : command;
      parts.push(`Command: ${truncated}`);
    }

    if (triage) {
      parts.push(
        `Triage: ${triage.verdict} (confidence: ${triage.confidence})`,
        `Reasoning: ${triage.reasoning}`,
      );
    }

    if (alerts.length > 1) {
      parts.push(`(+${alerts.length - 1} more alert${alerts.length > 2 ? "s" : ""})`);
    }

    const message = parts.join("\n");

    // Inject as system event — agent sees this and can relay to user
    api.runtime.system.enqueueSystemEvent(message, { sessionKey });
  } catch (err) {
    api.logger.warn(`AgentShield notification failed: ${String(err)}`);
  }
}

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
 * Build a stable correlation bucket key.
 */
function correlationBucketKey(
  sessionKey: string | undefined,
  toolName: string,
): string {
  return `${sessionKey ?? ""}:${toolName}`;
}

/** A pending evaluation awaiting its tool result, keyed by session+tool. */
type PendingEntry = {
  eventId: string;
  timestamp: number;
  /** The session_id sent to the engine, for override reporting. */
  sessionId?: string;
};

/** Drop entries older than the correlation TTL from every bucket. */
function pruneStale(
  pending: Map<string, PendingEntry[]>,
  now: number,
): void {
  for (const [key, entries] of pending) {
    const fresh = entries.filter(
      (entry) => now - entry.timestamp <= CORRELATION_TTL_MS,
    );
    if (fresh.length === 0) {
      pending.delete(key);
    } else {
      pending.set(key, fresh);
    }
  }
}

/** Dequeue the oldest pending entry for this session+tool bucket, or null. */
function takePending(
  pending: Map<string, PendingEntry[]>,
  ctx: { sessionKey?: string },
  event: { toolName: string },
): PendingEntry | null {
  pruneStale(pending, Date.now());

  const key = correlationBucketKey(ctx.sessionKey, event.toolName);
  const queue = pending.get(key);
  if (!queue || queue.length === 0) {
    return null;
  }

  const next = queue.shift();
  if (queue.length === 0) {
    pending.delete(key);
  } else {
    pending.set(key, queue);
  }
  return next ?? null;
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

    // Correlation map: session/tool bucket -> FIFO queue of allowed
    // evaluations awaiting their tool result, used to correlate audit
    // reports back to the originating evaluation.
    const pendingEvaluations = new Map<string, PendingEntry[]>();

    // Blocked/approval-required calls, tracked so that if the host executes
    // one anyway (an after_tool_call arrives for it), we can report the user
    // override to the engine — this is what increments session.override_count
    // for ai_agent_override_escalation.yml.
    const blockedEvaluations = new Map<string, PendingEntry[]>();

    /** Record a blocked/approval-required call for later override detection. */
    const recordBlocked = (
      request: { event_id: string; session_id: string | null },
      sessionKey: string | undefined,
      toolName: string,
    ): void => {
      const key = correlationBucketKey(sessionKey, toolName);
      const queue = blockedEvaluations.get(key) ?? [];
      queue.push({
        eventId: request.event_id,
        sessionId: request.session_id ?? undefined,
        timestamp: Date.now(),
      });
      blockedEvaluations.set(key, queue);
    };

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
        const correlationKey = correlationBucketKey(ctx.sessionKey, event.toolName);

        try {
          const response = await client.evaluate(request);
          circuitBreaker.recordSuccess();

          // Log effective mode for debugging
          if (response.effective_mode) {
            api.logger.debug?.(
              `AgentShield effective mode for ${event.toolName}: ${response.effective_mode}`,
            );
          }

          // Log triage results if present
          if (response.triage_results?.length) {
            for (const triage of response.triage_results) {
              api.logger.info(
                `AgentShield triage: ${triage.verdict} (${triage.confidence}) - ${triage.reasoning}`,
              );
            }
          }

          if (response.action === "block") {
            // Include triage reasoning in block reason if available
            const triageReason = response.triage_results?.[0]?.reasoning;
            const blockReason = triageReason
              ? `${response.reason ?? "Blocked by AgentShield"} (Triage: ${triageReason})`
              : response.reason ?? "Blocked by AgentShield";

            api.logger.warn(
              `AgentShield blocked ${event.toolName}: ${blockReason}`,
            );

            // Notify user via system event for blocks
            notifyAlert(api, response, event.toolName, "blocked", ctx.sessionKey ?? "", config.notify);

            recordBlocked(request, ctx.sessionKey, event.toolName);
            return {
              block: true,
              blockReason,
            };
          }

          if (response.action === "require_approval") {
            const approvalReason =
              response.reason ??
              "AgentShield requires explicit approval before this tool call can proceed";
            api.logger.warn(
              `AgentShield requires approval for ${event.toolName}: ${approvalReason}`,
            );
            notifyAlert(api, response, event.toolName, "blocked", ctx.sessionKey ?? "", config.notify);
            recordBlocked(request, ctx.sessionKey, event.toolName);
            return {
              block: true,
              blockReason: approvalReason,
            };
          }

          if (response.action === "log" && response.alerts?.length) {
            api.logger.info(
              `AgentShield logged ${response.alerts.length} alert(s) for ${event.toolName}`,
            );
            // Notify user based on configured severity threshold
            notifyAlert(api, response, event.toolName, "logged", ctx.sessionKey ?? "", config.notify);
          }

          // Queue correlation only for calls that are allowed to execute.
          const pending = pendingEvaluations.get(correlationKey) ?? [];
          pending.push({
            eventId: request.event_id,
            timestamp: Date.now(),
          });
          pendingEvaluations.set(correlationKey, pending);

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

    // ---- after_tool_call: override detection + fire-and-forget audit ----
    api.on("after_tool_call", async (event, ctx) => {
      // A tool result for a call we blocked or required approval on means the
      // host executed it anyway — i.e. the user overrode the verdict. Report
      // it so session.override_count reflects reality. Check this before the
      // allowed-call correlation, since blocked calls are tracked separately.
      const overridden = takePending(blockedEvaluations, ctx, event);
      if (overridden) {
        if (overridden.sessionId) {
          client.sendOverride(overridden.sessionId, overridden.eventId);
          api.logger.info(
            `AgentShield override reported for ${event.toolName} (event ${overridden.eventId})`,
          );
        }
        // Correlate the audit report to the original evaluation.
        client.sendAudit(buildAuditReport(event, ctx, overridden.eventId));
        return;
      }

      const allowed = takePending(pendingEvaluations, ctx, event);
      const report = buildAuditReport(
        event,
        ctx,
        allowed?.eventId ?? randomUUID(),
      );
      client.sendAudit(report);
    });

    // ---- Lifecycle hooks ----
    api.on("session_start", async (event, ctx) => {
      client.sendLifecycle(
        buildLifecycleEvent(
          "session_start",
          // Use ctx.sessionKey (not ctx.sessionId, which does not exist) so
          // the top-level session_id matches the tool-call path and the
          // engine can correlate lifecycle events with the session's tool
          // calls. Fall back to the event's session id if the context has
          // none yet at session start.
          { agentId: ctx.agentId, sessionKey: ctx.sessionKey ?? event.sessionId },
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
          { agentId: ctx.agentId, sessionKey: ctx.sessionKey ?? event.sessionId },
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
