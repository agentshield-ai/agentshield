/**
 * Map OpenClaw tool names to AgentShield command strings.
 *
 * Tool names arrive already normalised by OpenClaw's `normalizeToolName()`
 * (e.g. "bash" -> "exec"), so we use them as-is.
 *
 * Mapping follows integration contract Section 2.4.
 * Note: event_type is always "tool_call" in the new format.
 */
export function normaliseToolCall(
  toolName: string,
  params: Record<string, unknown>,
): { command: string | null } {
  switch (toolName) {
    case "exec":
      return {
        command: typeof params.command === "string" ? params.command : null,
      };

    case "write":
      return {
        command: `Write: ${filePath(params)}`,
      };

    case "read":
      return {
        command: `Read: ${filePath(params)}`,
      };

    case "edit":
      return {
        command: `Edit: ${filePath(params)}`,
      };

    case "browser":
      return {
        command: `${str(params.action)}: ${str(params.url)}`.trim(),
      };

    case "message":
      return {
        command: `Message to ${str(params.channel)}`,
      };

    case "sessions_spawn":
      return {
        command: `Spawn: ${str(params.agentId)}`,
      };

    default:
      return { command: toolName };
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
