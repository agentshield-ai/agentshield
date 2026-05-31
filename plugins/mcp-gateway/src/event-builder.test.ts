import { describe, expect, it } from "vitest";

import {
  buildAuditReport,
  buildEvaluationRequest,
  buildLifecycleEvent,
  summariseResult,
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
