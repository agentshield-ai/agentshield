/**
 * Map OpenClaw tool names to AgentShield event types and command strings.
 *
 * Tool names arrive already normalised by OpenClaw's `normalizeToolName()`
 * (e.g. "bash" -> "exec"), so we use them as-is.
 *
 * Mapping follows integration contract Section 2.4.
 */
export function normaliseToolCall(
  toolName: string,
  params: Record<string, unknown>,
): { event_type: string; command: string | null } {
  switch (toolName) {
    case "exec":
      return {
        event_type: "tool_call",
        command: typeof params.command === "string" ? params.command : null,
      };

    case "write":
      return {
        event_type: "file_write",
        command: `Write: ${filePath(params)}`,
      };

    case "read":
      return {
        event_type: "file_read",
        command: `Read: ${filePath(params)}`,
      };

    case "edit":
      return {
        event_type: "file_edit",
        command: `Edit: ${filePath(params)}`,
      };

    case "browser":
      return {
        event_type: "browser_action",
        command: `${str(params.action)}: ${str(params.url)}`.trim(),
      };

    case "message":
      return {
        event_type: "message_send",
        command: `Message to ${str(params.channel)}`,
      };

    case "sessions_spawn":
      return {
        event_type: "session_spawn",
        command: `Spawn: ${str(params.agentId)}`,
      };

    default:
      return { event_type: "tool_call", command: toolName };
  }
}

function filePath(params: Record<string, unknown>): string {
  // params.path is the primary key (after normalizeToolParams maps file_path -> path).
  // params.filePath is a fallback for direct API callers.
  const p = params.path ?? params.filePath;
  return typeof p === "string" ? p : "<unknown>";
}

function str(value: unknown): string {
  return typeof value === "string" ? value : "";
}
