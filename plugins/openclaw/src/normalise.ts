/**
 * Map OpenClaw tool names to AgentShield event semantics and command strings.
 *
 * Tool names arrive already normalised by OpenClaw's `normalizeToolName()`
 * (e.g. "bash" -> "exec"), so we use them as-is.
 *
 * Mapping follows the server-side normalization defaults so the adapter can
 * preserve richer event semantics like file reads/writes.
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

export function eventTypeForToolCall(
  toolName: string,
  _params: Record<string, unknown>,
): string {
  switch (toolName) {
    case "read":
    case "file_read":
      return "file_read";
    case "write":
    case "file_write":
    case "create":
      return "file_write";
    case "edit":
    case "file_edit":
      return "file_edit";
    case "sessions_spawn":
    case "session_spawn":
      return "session_spawn";
    default:
      return "tool_call";
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
