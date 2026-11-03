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
  /** The calling harness's tool-use id for this call, when the client sends one. */
  toolCallId?: string | null;
  /** The agent making the call (gen_ai.agent.name in Weft), when known. */
  agentName?: string | null;
};

/** `_meta` keys MCP clients use to pass the harness tool-use id. */
const TOOL_CALL_ID_META_KEYS = [
  "claudecode/toolUseId",
  "toolUseId",
  "tool_use_id",
  "toolCallId",
  "tool_call_id",
] as const;

/** The engine's limit on ids, in UTF-8 bytes. */
const MAX_ID_BYTES = 256;

/**
 * A tool-use id as the engine accepts it, or null. The id comes from the
 * MCP client, and the engine rejects (400) an evaluation whose tool_call_id
 * is padded, holds a control character or is over 256 bytes; that would
 * count as an engine failure and, through the circuit breaker, block the
 * call. Surrounding whitespace is trimmed; anything else invalid is dropped,
 * so the call is still evaluated, only without Weft correlation.
 */
export function sanitizeToolCallId(v: unknown): string | null {
  if (typeof v !== "string") return null;
  // Whitespace the engine trims as Go's unicode.IsSpace does (a superset is fine).
  const id = v.replace(/^[\s\u0085]+|[\s\u0085]+$/gu, "");
  if (id.length === 0) return null;
  // Control characters (C0, DEL, C1) and the Unicode line/paragraph separators.
  if (/[\u0000-\u001f\u007f-\u009f\u2028\u2029]/u.test(id)) return null;
  // Lone surrogates are not valid UTF-8.
  if (Buffer.from(id, "utf8").toString("utf8") !== id) return null;
  if (Buffer.byteLength(id, "utf8") > MAX_ID_BYTES) return null;
  return id;
}

/** Extract the harness tool-use id from a tools/call request's `_meta`, if any (sanitizeToolCallId). */
export function toolCallIdFromMeta(meta: unknown): string | null {
  if (!meta || typeof meta !== "object") return null;
  const m = meta as Record<string, unknown>;
  for (const key of TOOL_CALL_ID_META_KEYS) {
    const id = sanitizeToolCallId(m[key]);
    if (id) return id;
  }
  return null;
}

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
    ...(ctx.toolCallId ? { tool_call_id: ctx.toolCallId } : {}),
    ...(ctx.agentName ? { agent_name: ctx.agentName } : {}),
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
