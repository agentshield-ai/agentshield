import { describe, expect, it } from "vitest";

import { normaliseToolCall } from "./normalise.js";

describe("normaliseToolCall", () => {
  it("maps exec with command", () => {
    const result = normaliseToolCall("exec", { command: "ls -la" });
    expect(result.command).toBe("ls -la");
  });

  it("maps exec with missing command to null", () => {
    const result = normaliseToolCall("exec", {});
    expect(result.command).toBeNull();
  });

  it("maps write with path", () => {
    const result = normaliseToolCall("write", { path: "/tmp/foo.txt" });
    expect(result.command).toBe("Write: /tmp/foo.txt");
  });

  it("maps read with filePath fallback", () => {
    const result = normaliseToolCall("read", {
      filePath: "/etc/passwd",
    });
    expect(result.command).toBe("Read: /etc/passwd");
  });

  it("maps edit with path", () => {
    const result = normaliseToolCall("edit", {
      path: "/src/main.ts",
    });
    expect(result.command).toBe("Edit: /src/main.ts");
  });

  it("maps browser with action and url", () => {
    const result = normaliseToolCall("browser", {
      action: "navigate",
      url: "https://example.com",
    });
    expect(result.command).toBe("navigate: https://example.com");
  });

  it("maps browser with missing url", () => {
    const result = normaliseToolCall("browser", { action: "click" });
    expect(result.command).toBe("click:");
  });

  it("maps message with channel", () => {
    const result = normaliseToolCall("message", {
      channel: "telegram",
    });
    expect(result.command).toBe("Message to telegram");
  });

  it("maps sessions_spawn with agentId", () => {
    const result = normaliseToolCall("sessions_spawn", {
      agentId: "agent-42",
    });
    expect(result.command).toBe("Spawn: agent-42");
  });

  it("maps unknown tool with tool name as command", () => {
    const result = normaliseToolCall("custom_tool", { foo: "bar" });
    expect(result.command).toBe("custom_tool");
  });

  it("handles file ops with no path", () => {
    const result = normaliseToolCall("write", {});
    expect(result.command).toBe("Write: <unknown>");
  });
});
