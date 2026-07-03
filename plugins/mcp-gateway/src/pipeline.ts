import { randomUUID } from "node:crypto";

import { CircuitBreaker } from "./circuit-breaker.js";
import { AgentShieldClient } from "./client.js";
import {
  buildAuditReport,
  buildEvaluationRequest,
  buildLifecycleEvent,
  type ToolContext,
} from "./event-builder.js";
import { normaliseToolCall } from "./normalise.js";
import type { AgentShieldConfig, EvaluationResponse, Logger } from "./types.js";

const SEVERITY_ORDER: Record<string, number> = {
  low: 0,
  medium: 1,
  high: 2,
  critical: 3,
};

const NOTIFY_THRESHOLD: Record<string, number> = {
  all: 0,
  high: 2,
  critical: 3,
  none: 999,
};

const CORRELATION_TTL_MS = 60_000;

/**
 * Window within which a re-issued blocked call is treated as an override.
 * The gateway fails closed, so a blocked tool call never executes; the only
 * override an MCP proxy can observe is the agent re-issuing a call it was just
 * told was blocked. Re-issuing the same (session, tool) within this window is
 * reported as an override so session.override_count reflects the agent
 * ignoring blocks (ai_agent_override_escalation.yml).
 */
const OVERRIDE_REISSUE_WINDOW_MS = 60_000;

/**
 * The outcome of evaluating a tool call.
 *
 * - `allowed: true`  -> forward the call to the downstream MCP server.
 * - `allowed: false` -> block; `reason` is surfaced to the agent.
 * `overridable` flags a require_approval verdict so a host that supports an
 * approval prompt can offer one; the gateway itself fails closed.
 */
export type Decision = {
  allowed: boolean;
  reason: string | null;
  overridable: boolean;
  eventId: string;
};

/**
 * Thread-unsafe-but-single-threaded FIFO correlation tracker.
 *
 * Maps `(session, tool)` to a queue of evaluate `event_id`s so the post-call
 * audit can link back to the pre-call evaluation (contract Section 1.2). Node
 * runs this proxy on a single thread, so no lock is needed; stale entries are
 * purged lazily on pop.
 */
class CorrelationTracker {
  private readonly pending = new Map<string, Array<{ id: string; t: number }>>();

  private static key(sessionId: string | null, tool: string): string {
    return `${sessionId ?? ""}:${tool}`;
  }

  push(sessionId: string | null, tool: string, eventId: string): void {
    const key = CorrelationTracker.key(sessionId, tool);
    const bucket = this.pending.get(key) ?? [];
    bucket.push({ id: eventId, t: Date.now() });
    this.pending.set(key, bucket);
  }

  pop(sessionId: string | null, tool: string): string | null {
    const key = CorrelationTracker.key(sessionId, tool);
    const bucket = this.pending.get(key);
    if (!bucket || bucket.length === 0) {
      return null;
    }
    const now = Date.now();
    while (bucket.length > 0 && now - (bucket[0] as { t: number }).t > CORRELATION_TTL_MS) {
      bucket.shift();
    }
    if (bucket.length === 0) {
      this.pending.delete(key);
      return null;
    }
    const entry = bucket.shift() as { id: string };
    if (bucket.length === 0) {
      this.pending.delete(key);
    }
    return entry.id;
  }
}

/**
 * The security pipeline: skip -> intercept -> circuit-breaker -> evaluate ->
 * act -> audit -> lifecycle (contract Section 3). MCP-transport-agnostic so it
 * can be unit-tested with a stubbed client.
 */
export class GatewayPipeline {
  private readonly config: AgentShieldConfig;
  private readonly client: AgentShieldClient;
  private readonly breaker: CircuitBreaker;
  private readonly logger: Logger;
  private readonly skipSet: Set<string>;
  private readonly interceptSet: Set<string> | null;
  private readonly correlation = new CorrelationTracker();
  /** Last block per (session, tool), for re-issue override detection. */
  private readonly recentBlocks = new Map<string, { eventId: string; t: number }>();

  constructor(config: AgentShieldConfig, client: AgentShieldClient, logger: Logger) {
    this.config = config;
    this.client = client;
    this.logger = logger;
    this.breaker = new CircuitBreaker({
      failureThreshold: config.circuit_breaker.failure_threshold,
      recoveryIntervalMs: config.circuit_breaker.recovery_interval_ms,
    });
    this.skipSet = new Set(config.skip);
    this.interceptSet = config.intercept.length > 0 ? new Set(config.intercept) : null;
  }

  /**
   * Evaluate a single tool call before it reaches the downstream server.
   *
   * Returns a {@link Decision}. The original (possibly namespaced) tool name is
   * passed through; normalisation happens internally so Sigma rules match.
   */
  async evaluateToolCall(
    toolName: string,
    args: Record<string, unknown>,
    ctx: ToolContext,
  ): Promise<Decision> {
    const safeArgs = args && typeof args === "object" ? args : {};
    const { canonical } = normaliseToolCall(toolName, safeArgs);

    // Step 2: skip check (original or canonical name).
    if (this.skipSet.has(toolName) || this.skipSet.has(canonical)) {
      return this.allow(null);
    }

    // Step 3: intercept filter (only listed tools are evaluated).
    if (
      this.interceptSet &&
      !this.interceptSet.has(toolName) &&
      !this.interceptSet.has(canonical)
    ) {
      return this.allow(null);
    }

    // Step 4: circuit-breaker gate.
    if (this.breaker.isOpen()) {
      this.logger.warn(
        `AgentShield circuit breaker open; applying ${this.config.timeout_policy} policy`,
      );
      return this.applyTimeoutPolicy();
    }

    // Step 5: build request.
    const request = buildEvaluationRequest({ toolName, params: safeArgs }, ctx);

    // Step 6: evaluate (awaited, timeout-bounded).
    let response: EvaluationResponse;
    try {
      response = await this.client.evaluate(request);
      this.breaker.recordSuccess();
    } catch (err: unknown) {
      this.breaker.recordFailure();
      this.logger.warn(`AgentShield evaluation failed: ${String(err)}`);
      return this.applyTimeoutPolicy();
    }

    // Step 7: act on the action.
    return this.act(response, toolName, request.event_id, ctx);
  }

  /** Send a fire-and-forget audit report after the downstream tool runs. */
  recordResult(
    toolName: string,
    args: Record<string, unknown>,
    ctx: ToolContext,
    result: unknown,
    opts: { isError: boolean; errorMessage: string | null; durationMs: number },
  ): void {
    const safeArgs = args && typeof args === "object" ? args : {};
    const corrId = this.correlation.pop(ctx.sessionId, toolName) ?? randomUUID();
    const report = buildAuditReport(
      {
        toolName,
        params: safeArgs,
        result,
        isError: opts.isError,
        error: opts.errorMessage ?? undefined,
        durationMs: opts.durationMs,
      },
      ctx,
      corrId,
    );
    this.client.sendAudit(report);
  }

  /** Emit a fire-and-forget lifecycle event (session_start / session_end). */
  recordLifecycle(eventType: "session_start" | "session_end", ctx: ToolContext): void {
    this.client.sendLifecycle(buildLifecycleEvent(eventType, ctx));
  }

  /** Report a user override of a block / require_approval verdict (Section 1.5). */
  reportOverride(sessionId: string, eventId: string): void {
    this.client.sendOverride(sessionId, eventId);
  }

  /**
   * Record that a call was blocked. If the same (session, tool) was already
   * blocked within OVERRIDE_REISSUE_WINDOW_MS, the agent is re-issuing a call
   * it was told was blocked — report that prior block as overridden.
   */
  private noteBlockedCall(
    sessionId: string | null,
    toolName: string,
    eventId: string,
  ): void {
    const now = Date.now();
    // Prune stale markers so the map stays bounded by active (session, tool)s.
    for (const [k, v] of this.recentBlocks) {
      if (now - v.t > OVERRIDE_REISSUE_WINDOW_MS) {
        this.recentBlocks.delete(k);
      }
    }

    const key = `${sessionId ?? ""}:${toolName}`;
    const prior = this.recentBlocks.get(key);
    if (prior && sessionId && now - prior.t <= OVERRIDE_REISSUE_WINDOW_MS) {
      this.reportOverride(sessionId, prior.eventId);
      this.logger.info(
        `AgentShield override reported: re-issued blocked ${toolName} (event ${prior.eventId})`,
      );
    }
    this.recentBlocks.set(key, { eventId, t: now });
  }

  /** Non-blocking startup health probe. */
  async startupHealthCheck(): Promise<void> {
    try {
      const ok = await this.client.healthCheck();
      if (ok) {
        this.logger.info(`AgentShield reachable at ${this.config.endpoint}`);
      } else {
        this.logger.warn(
          `AgentShield not reachable at ${this.config.endpoint} (will retry on first tool call)`,
        );
      }
    } catch {
      this.logger.warn("AgentShield health check failed");
    }
  }

  private act(
    response: EvaluationResponse,
    toolName: string,
    eventId: string,
    ctx: ToolContext,
  ): Decision {
    if (response.effective_mode) {
      this.logger.debug(`AgentShield effective mode for ${toolName}: ${response.effective_mode}`);
    }
    for (const triage of response.triage_results ?? []) {
      this.logger.info(
        `AgentShield triage: ${triage.verdict} (${triage.confidence}) - ${triage.reasoning}`,
      );
    }

    const action = response.action;

    if (action === "block") {
      const triageReason = response.triage_results?.[0]?.reasoning;
      let reason = response.reason || "Blocked by AgentShield";
      if (triageReason) {
        reason = `${reason} (Triage: ${triageReason})`;
      }
      this.logger.warn(`AgentShield blocked ${toolName}: ${reason}`);
      this.maybeNotify(response, toolName, "blocked");
      this.noteBlockedCall(ctx.sessionId, toolName, eventId);
      return { allowed: false, reason, overridable: false, eventId };
    }

    if (action === "require_approval") {
      // Fail-closed: MCP has no synchronous user-approval channel, so we block
      // (contract Section 3.2). overridable=true lets a host offer an override.
      const reason =
        response.reason ||
        "AgentShield requires explicit approval before this tool call can proceed";
      this.logger.warn(`AgentShield requires approval for ${toolName}: ${reason}`);
      this.maybeNotify(response, toolName, "requires approval");
      this.noteBlockedCall(ctx.sessionId, toolName, eventId);
      return { allowed: false, reason, overridable: response.overridable ?? true, eventId };
    }

    // High-confidence allow override: alerts present but triage says allow.
    const hasHcAllow = (response.triage_results ?? []).some(
      (t) => t.verdict === "allow" && (t.confidence ?? 0) > 0.8,
    );
    if (response.alerts && response.alerts.length > 0 && hasHcAllow) {
      this.logger.info(
        `AgentShield: ${response.alerts.length} alert(s) for ${toolName}, overridden by high-confidence allow`,
      );
    } else if (action === "log" && response.alerts && response.alerts.length > 0) {
      this.logger.info(
        `AgentShield logged ${response.alerts.length} alert(s) for ${toolName}`,
      );
      this.maybeNotify(response, toolName, "logged");
    }

    // allow / log -> push correlation for audit linkage and permit.
    this.correlation.push(ctx.sessionId, toolName, eventId);
    return { allowed: true, reason: null, overridable: false, eventId };
  }

  private allow(reason: string | null): Decision {
    return { allowed: true, reason, overridable: false, eventId: randomUUID() };
  }

  private applyTimeoutPolicy(): Decision {
    if (this.config.timeout_policy === "block") {
      return {
        allowed: false,
        reason: "AgentShield unavailable (fail-closed policy)",
        overridable: false,
        eventId: randomUUID(),
      };
    }
    // allow / log -> fail open.
    return this.allow(null);
  }

  private maybeNotify(
    response: EvaluationResponse,
    toolName: string,
    action: string,
  ): void {
    const alerts = response.alerts ?? [];
    if (alerts.length === 0) {
      return;
    }
    const threshold = NOTIFY_THRESHOLD[this.config.notify] ?? 2;
    const meets = alerts.some(
      (a) => (SEVERITY_ORDER[a.severity] ?? 0) >= threshold,
    );
    if (!meets) {
      return;
    }
    // Show the highest-severity alert, not merely the first: alerts[0] may be
    // a low-severity match while a later alert crossed the threshold.
    const top = alerts.reduce((best, a) =>
      (SEVERITY_ORDER[a.severity] ?? 0) > (SEVERITY_ORDER[best.severity] ?? 0)
        ? a
        : best,
    );
    if (!top) {
      return;
    }
    const parts = [
      `AgentShield Alert - ${action} (${top.severity})`,
      `Rule: ${top.rule_name}`,
      `Tool: ${toolName}`,
    ];
    if (alerts.length > 1) {
      parts.push(`(+${alerts.length - 1} more alert${alerts.length > 2 ? "s" : ""})`);
    }
    this.logger.warn(parts.join(" | "));
  }
}
