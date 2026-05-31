import { describe, expect, it } from "vitest";

import {
  eventTypeForToolCall,
  normaliseToolCall,
  normaliseToolName,
  stripNamespace,
} from "./normalise.js";

describe("stripNamespace", () => {
  it("strips mcp__server__tool prefixes", () => {
    expect(stripNamespace("mcp__filesystem__read_file")).toBe("read_file");
  });

  it("strips dotted namespaces", () => {
    expect(stripNamespace("filesystem.read_file")).toBe("read_file");
  });

  it("strips slash and colon namespaces", () => {
    expect(stripNamespace("git/commit")).toBe("commit");
    expect(stripNamespace("server:exec")).toBe("exec");
  });

  it("leaves bare names unchanged", () => {
    expect(stripNamespace("read_file")).toBe("read_file");
  });
});

describe("normaliseToolName", () => {
  it("maps platform aliases to canonical names", () => {
    expect(normaliseToolName("execute_command")).toBe("exec");
    expect(normaliseToolName("write_file")).toBe("write");
    expect(normaliseToolName("delegate")).toBe("sessions_spawn");
  });

  it("maps namespaced MCP tools after stripping the prefix", () => {
    expect(normaliseToolName("mcp__shell__run_command")).toBe("exec");
    expect(normaliseToolName("fs.read_file")).toBe("read");
  });

  it("passes unknown tools through unchanged", () => {
    expect(normaliseToolName("custom_tool")).toBe("custom_tool");
  });
});

describe("normaliseToolCall", () => {
  it("maps exec with command", () => {
    const result = normaliseToolCall("execute_command", { command: "ls -la" });
    expect(result.canonical).toBe("exec");
    expect(result.command).toBe("ls -la");
  });

  it("maps exec with missing command to null", () => {
    expect(normaliseToolCall("shell", {}).command).toBeNull();
  });

  it("maps write with path", () => {
    expect(normaliseToolCall("write_file", { path: "/tmp/foo.txt" }).command).toBe(
      "Write: /tmp/foo.txt",
    );
  });

  it("maps read with file_path fallback", () => {
    expect(normaliseToolCall("read_file", { file_path: "/etc/passwd" }).command).toBe(
      "Read: /etc/passwd",
    );
  });

  it("maps edit with path", () => {
    expect(normaliseToolCall("edit_file", { path: "/src/main.ts" }).command).toBe(
      "Edit: /src/main.ts",
    );
  });

  it("maps browser with action and url", () => {
    expect(
      normaliseToolCall("browser", { action: "navigate", url: "https://example.com" }).command,
    ).toBe("navigate: https://example.com");
  });

  it("defaults browser action to navigate", () => {
    expect(normaliseToolCall("web_browse", { url: "https://x.test" }).command).toBe(
      "navigate: https://x.test",
    );
  });

  it("maps message with channel", () => {
    expect(normaliseToolCall("send_message", { channel: "telegram" }).command).toBe(
      "Message to telegram",
    );
  });

  it("maps message without channel to platform form", () => {
    expect(normaliseToolCall("slack_send", {}).command).toBe("Message via slack");
  });

  it("maps sessions_spawn with agent_id", () => {
    expect(normaliseToolCall("spawn_agent", { agent_id: "agent-42" }).command).toBe(
      "Spawn: agent-42",
    );
  });

  it("maps sessions_spawn without agent to unknown", () => {
    expect(normaliseToolCall("delegate", {}).command).toBe("Spawn: <unknown>");
  });

  it("truncates code_execute commands to 200 chars", () => {
    const long = "x".repeat(250);
    const result = normaliseToolCall("python_execute", { code: long });
    expect(result.command).toBe(`Execute: ${"x".repeat(200)}...`);
  });

  it("maps web_search query", () => {
    expect(normaliseToolCall("search", { query: "exfil" }).command).toBe("Search: exfil");
  });

  it("falls back to the original tool name for unknown tools", () => {
    expect(normaliseToolCall("custom_tool", { foo: "bar" }).command).toBe("custom_tool");
  });

  it("handles file ops with no path", () => {
    expect(normaliseToolCall("write_file", {}).command).toBe("Write: <unknown>");
  });
});

describe("eventTypeForToolCall", () => {
  it("maps read to file_read", () => {
    expect(eventTypeForToolCall("read")).toBe("file_read");
  });

  it("maps write to file_write", () => {
    expect(eventTypeForToolCall("write")).toBe("file_write");
  });

  it("maps edit to file_edit", () => {
    expect(eventTypeForToolCall("edit")).toBe("file_edit");
  });

  it("maps sessions_spawn to session_spawn", () => {
    expect(eventTypeForToolCall("sessions_spawn")).toBe("session_spawn");
  });

  it("defaults to tool_call", () => {
    expect(eventTypeForToolCall("exec")).toBe("tool_call");
  });
});
