import { describe, expect, it } from "vitest";

import { normaliseToolCall } from "./normalise.js";

describe("normaliseToolCall", () => {
  it("maps exec to tool_call with command", () => {
    const result = normaliseToolCall("exec", { command: "ls -la" });
    expect(result.event_type).toBe("tool_call");
    expect(result.command).toBe("ls -la");
  });

  it("maps exec with missing command to null", () => {
    const result = normaliseToolCall("exec", {});
    expect(result.event_type).toBe("tool_call");
    expect(result.command).toBeNull();
  });

  it("maps write to file_write with path", () => {
    const result = normaliseToolCall("write", { path: "/tmp/foo.txt" });
    expect(result.event_type).toBe("file_write");
    expect(result.command).toBe("Write: /tmp/foo.txt");
  });

  it("maps read to file_read with filePath fallback", () => {
    const result = normaliseToolCall("read", {
      filePath: "/etc/passwd",
    });
    expect(result.event_type).toBe("file_read");
    expect(result.command).toBe("Read: /etc/passwd");
  });

  it("maps edit to file_edit", () => {
    const result = normaliseToolCall("edit", {
      path: "/src/main.ts",
    });
    expect(result.event_type).toBe("file_edit");
    expect(result.command).toBe("Edit: /src/main.ts");
  });

  it("maps browser to browser_action", () => {
    const result = normaliseToolCall("browser", {
      action: "navigate",
      url: "https://example.com",
    });
    expect(result.event_type).toBe("browser_action");
    expect(result.command).toBe("navigate: https://example.com");
  });

  it("maps browser with missing url", () => {
    const result = normaliseToolCall("browser", { action: "click" });
    expect(result.event_type).toBe("browser_action");
    expect(result.command).toBe("click:");
  });

  it("maps message to message_send", () => {
    const result = normaliseToolCall("message", {
      channel: "telegram",
    });
    expect(result.event_type).toBe("message_send");
    expect(result.command).toBe("Message to telegram");
  });

  it("maps sessions_spawn to session_spawn", () => {
    const result = normaliseToolCall("sessions_spawn", {
      agentId: "agent-42",
    });
    expect(result.event_type).toBe("session_spawn");
    expect(result.command).toBe("Spawn: agent-42");
  });

  it("maps unknown tool to tool_call with tool name as command", () => {
    const result = normaliseToolCall("custom_tool", { foo: "bar" });
    expect(result.event_type).toBe("tool_call");
    expect(result.command).toBe("custom_tool");
  });

  it("handles file ops with no path", () => {
    const result = normaliseToolCall("write", {});
    expect(result.command).toBe("Write: <unknown>");
  });
});
