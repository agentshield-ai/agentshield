import { describe, expect, it } from "vitest";

import {
  buildAuditReport,
  buildEvaluationRequest,
  buildLifecycleEvent,
  summariseResult,
  toolCallIdFromMeta,
} from "./event-builder.js";

const CTX = { agentId: "agent-1", sessionId: "sess-1" };

describe("buildEvaluationRequest", () => {
  it("builds a canonical envelope with source mcp-gateway", () => {
    const req = buildEvaluationRequest(
      { toolName: "mcp__shell__execute_command", params: { command: "rm -rf /" } },
      CTX,
    );
    expect(req.source).toBe("mcp-gateway");
    expect(req.tool_name).toBe("exec");
    expect(req.event_type).toBe("tool_call");
    expect(req.command).toBe("rm -rf /");
    expect(req.params.command).toBe("rm -rf /");
    expect(req.params._mcp_original_tool).toBe("mcp__shell__execute_command");
    expect(req.agent_id).toBe("agent-1");
    expect(req.session_id).toBe("sess-1");
    expect(req.event_id).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("uses file_write event type for write tools", () => {
    const req = buildEvaluationRequest(
      { toolName: "write_file", params: { path: "/tmp/x" } },
      CTX,
    );
    expect(req.tool_name).toBe("write");
    expect(req.event_type).toBe("file_write");
    expect(req.command).toBe("Write: /tmp/x");
  });

  it("sends tool_call_id and agent_name when known (Weft correlation)", () => {
    const req = buildEvaluationRequest(
      { toolName: "exec", params: { command: "ls" } },
      { ...CTX, toolCallId: "toolu_01", agentName: "coder" },
    );
    expect(req.tool_call_id).toBe("toolu_01");
    expect(req.agent_name).toBe("coder");
    const bare = buildEvaluationRequest({ toolName: "exec", params: {} }, CTX);
    expect("tool_call_id" in bare).toBe(false);
    expect("agent_name" in bare).toBe(false);
  });
});

describe("toolCallIdFromMeta", () => {
  it("reads the harness tool-use id from _meta", () => {
    expect(toolCallIdFromMeta({ "claudecode/toolUseId": "toolu_9" })).toBe("toolu_9");
    expect(toolCallIdFromMeta({ toolCallId: "call_1" })).toBe("call_1");
    expect(toolCallIdFromMeta({ progressToken: 3 })).toBeNull();
    expect(toolCallIdFromMeta(undefined)).toBeNull();
    expect(toolCallIdFromMeta({ toolUseId: "x".repeat(300) })).toBeNull();
  });

  it("trims padding and drops ids the engine would reject (400)", () => {
    // Padding is trimmed: the engine rejects leading or trailing whitespace.
    expect(toolCallIdFromMeta({ toolUseId: "  toolu_1\t" })).toBe("toolu_1");
    expect(toolCallIdFromMeta({ toolUseId: "\u00a0toolu_2\u2003" })).toBe("toolu_2");
    // Control characters inside, separators, lone surrogates: dropped.
    expect(toolCallIdFromMeta({ toolUseId: "tool\tu_3" })).toBeNull();
    expect(toolCallIdFromMeta({ toolUseId: "tool\nu_3" })).toBeNull();
    expect(toolCallIdFromMeta({ toolUseId: "tool\u0085u_3" })).toBeNull();
    expect(toolCallIdFromMeta({ toolUseId: "tool\u2028u_3" })).toBeNull();
    expect(toolCallIdFromMeta({ toolUseId: "tool\ud800u_3" })).toBeNull();
    expect(toolCallIdFromMeta({ toolUseId: "   " })).toBeNull();
    // The engine's limit is 256 bytes, not characters.
    expect(toolCallIdFromMeta({ toolUseId: "é".repeat(128) })).toBe("é".repeat(128));
    expect(toolCallIdFromMeta({ toolUseId: "é".repeat(129) })).toBeNull();
    // An invalid id under one key does not hide a valid one under another.
    expect(toolCallIdFromMeta({ "claudecode/toolUseId": "bad\u0001", toolCallId: "call_7" })).toBe("call_7");
  });
});

describe("buildAuditReport", () => {
  it("builds a tool_result report with correlation id", () => {
    const report = buildAuditReport(
      { toolName: "read_file", params: { path: "/x" }, result: "contents", durationMs: 12 },
      CTX,
      "corr-123",
    );
    expect(report.event_type).toBe("tool_result");
    expect(report.source).toBe("mcp-gateway");
    expect(report.tool_name).toBe("read");
    expect(report.correlation_id).toBe("corr-123");
    expect(report.result_summary).toBe("contents");
    expect(report.is_error).toBe(false);
    expect(report.duration_ms).toBe(12);
  });

  it("flags errors", () => {
    const report = buildAuditReport(
      { toolName: "exec", params: {}, error: "boom", isError: true },
      CTX,
      "c",
    );
    expect(report.is_error).toBe(true);
    expect(report.error_message).toBe("boom");
  });
});

describe("buildLifecycleEvent", () => {
  it("builds session_start", () => {
    const ev = buildLifecycleEvent("session_start", CTX);
    expect(ev.event_type).toBe("session_start");
    expect(ev.source).toBe("mcp-gateway");
    expect(ev.session_id).toBe("sess-1");
  });
});

describe("summariseResult", () => {
  it("returns null for nullish", () => {
    expect(summariseResult(null)).toBeNull();
    expect(summariseResult(undefined)).toBeNull();
  });

  it("truncates to 1000 chars", () => {
    const out = summariseResult("a".repeat(1500));
    expect(out).toHaveLength(1000);
  });

  it("stringifies objects", () => {
    expect(summariseResult({ a: 1 })).toBe('{"a":1}');
  });
});
