import { describe, expect, it } from "vitest";

import {
  buildAuditReport,
  buildEvaluationRequest,
  buildLifecycleEvent,
} from "./event-builder.js";

const UUID_REGEX = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const ISO_REGEX = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/;

describe("buildEvaluationRequest", () => {
  it("produces a valid evaluation request for exec", () => {
    const req = buildEvaluationRequest(
      { toolName: "exec", params: { command: "ls -la" } },
      { agentId: "agent-1", sessionKey: "session-1", toolName: "exec" },
    );

    expect(req.event_id).toMatch(UUID_REGEX);
    expect(req.timestamp).toMatch(ISO_REGEX);
    expect(req.event_type).toBe("tool_call");
    expect(req.tool_name).toBe("exec");
    expect(req.source).toBe("openclaw");
    expect(req.command).toBe("ls -la");
    expect(req.params).toEqual({ command: "ls -la" });
    expect(req.agent_id).toBe("agent-1");
    expect(req.session_id).toBe("session-1");
    expect(req.working_dir).toBeNull();
    expect(req.data).toEqual({});
  });

  it("sets null for missing context fields", () => {
    const req = buildEvaluationRequest(
      { toolName: "read", params: { path: "/etc/hosts" } },
      { toolName: "read" },
    );

    expect(req.agent_id).toBeNull();
    expect(req.session_id).toBeNull();
  });

  it("generates unique event IDs", () => {
    const req1 = buildEvaluationRequest(
      { toolName: "exec", params: {} },
      { toolName: "exec" },
    );
    const req2 = buildEvaluationRequest(
      { toolName: "exec", params: {} },
      { toolName: "exec" },
    );
    expect(req1.event_id).not.toBe(req2.event_id);
  });
});

describe("buildAuditReport", () => {
  it("produces a valid audit report", () => {
    const report = buildAuditReport(
      {
        toolName: "exec",
        params: { command: "ls" },
        result: "file1\nfile2",
        durationMs: 42,
      },
      { agentId: "agent-1", sessionKey: "session-1", toolName: "exec" },
      "corr-123",
    );

    expect(report.event_id).toMatch(UUID_REGEX);
    expect(report.correlation_id).toBe("corr-123");
    expect(report.event_type).toBe("tool_result");
    expect(report.tool_name).toBe("exec");
    expect(report.source).toBe("openclaw");
    expect(report.result_summary).toBe("file1\nfile2");
    expect(report.is_error).toBe(false);
    expect(report.error_message).toBeNull();
    expect(report.duration_ms).toBe(42);
  });

  it("marks error when error string is present", () => {
    const report = buildAuditReport(
      { toolName: "exec", params: {}, error: "command failed" },
      { toolName: "exec" },
      "corr-456",
    );

    expect(report.is_error).toBe(true);
    expect(report.error_message).toBe("command failed");
  });

  it("truncates result_summary to 1000 characters", () => {
    const longResult = "x".repeat(2000);
    const report = buildAuditReport(
      { toolName: "exec", params: {}, result: longResult },
      { toolName: "exec" },
      "corr-789",
    );

    expect(report.result_summary).toHaveLength(1000);
  });

  it("returns null result_summary for undefined result", () => {
    const report = buildAuditReport(
      { toolName: "exec", params: {} },
      { toolName: "exec" },
      "corr-000",
    );

    expect(report.result_summary).toBeNull();
  });

  it("JSON-stringifies non-string results", () => {
    const report = buildAuditReport(
      { toolName: "exec", params: {}, result: { ok: true } },
      { toolName: "exec" },
      "corr-obj",
    );

    expect(report.result_summary).toBe('{"ok":true}');
  });
});

describe("buildLifecycleEvent", () => {
  it("produces a valid lifecycle event", () => {
    const event = buildLifecycleEvent(
      "session_start",
      { agentId: "agent-1", sessionKey: "session-1" },
      { channel: "telegram" },
    );

    expect(event.event_id).toMatch(UUID_REGEX);
    expect(event.timestamp).toMatch(ISO_REGEX);
    expect(event.event_type).toBe("session_start");
    expect(event.source).toBe("openclaw");
    expect(event.agent_id).toBe("agent-1");
    expect(event.session_id).toBe("session-1");
    expect(event.data).toEqual({ channel: "telegram" });
  });
});
