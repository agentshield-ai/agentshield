import { randomUUID } from "node:crypto";

import { normaliseToolCall } from "./normalise.js";
import type {
  AuditReport,
  EvaluationRequest,
  LifecycleEvent,
} from "./types.js";

const MAX_RESULT_SUMMARY_LENGTH = 1000;

type ToolEvent = {
  toolName: string;
  params: Record<string, unknown>;
};

type ToolContext = {
  agentId?: string;
  sessionKey?: string;
  toolName: string;
};

type AfterToolEvent = ToolEvent & {
  result?: unknown;
  error?: string;
  durationMs?: number;
};

type LifecycleContext = {
  agentId?: string;
  sessionKey?: string;
};

export function buildEvaluationRequest(
  event: ToolEvent,
  ctx: ToolContext,
): EvaluationRequest {
  const { command } = normaliseToolCall(
    event.toolName,
    event.params,
  );

  return {
    event_id: randomUUID(),
    timestamp: new Date().toISOString(),
    event_type: "tool_call", // Always use "tool_call" as the engine expects this
    tool_name: event.toolName,
    source: "openclaw",
    command,
    params: {
      ...event.params,
      // Ensure command is in the format the engine expects for auto-field-mapping
      ...(command && { command }),
    },
    agent_id: ctx.agentId ?? null,
    session_id: ctx.sessionKey ?? null,
    working_dir: null,
    data: {},
  };
}

export function buildAuditReport(
  event: AfterToolEvent,
  ctx: ToolContext,
  correlationId: string,
): AuditReport {
  return {
    event_id: randomUUID(),
    correlation_id: correlationId,
    timestamp: new Date().toISOString(),
    event_type: "tool_result",
    tool_name: event.toolName,
    source: "openclaw",
    result_summary: summariseResult(event.result),
    is_error: event.error !== undefined,
    error_message: event.error ?? null,
    duration_ms: event.durationMs ?? 0,
    agent_id: ctx.agentId ?? null,
    session_id: ctx.sessionKey ?? null,
    working_dir: null,
    data: {},
  };
}

export function buildLifecycleEvent(
  eventType: string,
  ctx: LifecycleContext,
  data: Record<string, unknown>,
): LifecycleEvent {
  return {
    event_id: randomUUID(),
    timestamp: new Date().toISOString(),
    event_type: eventType,
    source: "openclaw",
    agent_id: ctx.agentId ?? null,
    session_id: ctx.sessionKey ?? null,
    data,
  };
}

function summariseResult(result: unknown): string | null {
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

  if (text.length > MAX_RESULT_SUMMARY_LENGTH) {
    return text.slice(0, MAX_RESULT_SUMMARY_LENGTH);
  }

  return text;
}
