import { randomUUID } from "node:crypto";

import { eventTypeForToolCall, normaliseToolCall } from "./normalise.js";
import type { AuditReport, EvaluationRequest, LifecycleEvent } from "./types.js";

const MAX_RESULT_SUMMARY_LENGTH = 1000;

const SOURCE = "mcp-gateway" as const;

export type ToolCallEvent = {
  toolName: string;
  params: Record<string, unknown>;
};

export type ToolContext = {
  agentId: string | null;
  sessionId: string | null;
};

export type ToolResultEvent = ToolCallEvent & {
  result?: unknown;
  error?: string;
  isError?: boolean;
  durationMs?: number;
};

/** Build an EvaluationRequest envelope (contract Section 1.1). */
export function buildEvaluationRequest(
  event: ToolCallEvent,
  ctx: ToolContext,
): EvaluationRequest {
  const { canonical, command } = normaliseToolCall(event.toolName, event.params);
  const eventType = eventTypeForToolCall(canonical);

  return {
    event_id: randomUUID(),
    timestamp: new Date().toISOString(),
    event_type: eventType,
    // Send the canonical name so the engine maps it to action.name and the same
    // Sigma rules match across connectors. The original name is preserved in
    // params for downstream debugging.
    tool_name: canonical,
    source: SOURCE,
    command,
    params: {
      ...event.params,
      ...(event.toolName !== canonical ? { _mcp_original_tool: event.toolName } : {}),
      // Duplicate command into params so the engine auto-maps it to
      // process.command_line (Section 1.1, Section 2.2).
      ...(command ? { command } : {}),
    },
    agent_id: ctx.agentId,
    session_id: ctx.sessionId,
    working_dir: null,
    data: {},
  };
}

/** Build an AuditReport (contract Section 1.2). */
export function buildAuditReport(
  event: ToolResultEvent,
  ctx: ToolContext,
  correlationId: string,
): AuditReport {
  const { canonical } = normaliseToolCall(event.toolName, event.params);
  const isError = event.isError ?? event.error !== undefined;

  return {
    event_id: randomUUID(),
    correlation_id: correlationId,
    timestamp: new Date().toISOString(),
    event_type: "tool_result",
    tool_name: canonical,
    source: SOURCE,
    result_summary: summariseResult(event.result),
    is_error: isError,
    error_message: event.error ?? null,
    duration_ms: event.durationMs ?? 0,
    agent_id: ctx.agentId,
    session_id: ctx.sessionId,
    working_dir: null,
    data: {},
  };
}

/** Build a LifecycleEvent (contract Section 1.3). */
export function buildLifecycleEvent(
  eventType: "session_start" | "session_end",
  ctx: ToolContext,
  data: Record<string, unknown> = {},
): LifecycleEvent {
  return {
    event_id: randomUUID(),
    timestamp: new Date().toISOString(),
    event_type: eventType,
    source: SOURCE,
    agent_id: ctx.agentId,
    session_id: ctx.sessionId,
    data,
  };
}

/** Summarise a tool result to at most MAX_RESULT_SUMMARY_LENGTH chars. */
export function summariseResult(result: unknown): string | null {
  if (result === undefined || result === null) {
    return null;
  }

  let text: string;
  if (typeof result === "string") {
    text = result;
  } else {
    try {
      text = JSON.stringify(result);
    } catch {
      text = String(result);
    }
  }

  return text.length > MAX_RESULT_SUMMARY_LENGTH
    ? text.slice(0, MAX_RESULT_SUMMARY_LENGTH)
    : text;
}
