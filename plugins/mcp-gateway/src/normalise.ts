/**
 * Map MCP tool names and arguments to AgentShield canonical forms.
 *
 * MCP servers expose arbitrary tool names. Clients frequently namespace them
 * (e.g. `mcp__filesystem__read_file`, `filesystem.read_file`,
 * `filesystem/read_file`). We first strip any namespace prefix, then apply the
 * canonical alias table (mirroring `plugins/hermes/normalise.py` — the broadest
 * reference, contract Section 2.1) so the same Sigma rules match regardless of
 * which MCP server emitted the call. Unknown tools pass through unchanged.
 */

/** MCP / platform tool name -> AgentShield canonical name (contract Section 2.1). */
const TOOL_ALIASES: Record<string, string> = {
  // Terminal / exec
  terminal: "exec",
  execute_command: "exec",
  run_command: "exec",
  shell: "exec",
  bash: "exec",
  Bash: "exec",
  computer: "exec",
  // File write
  write_file: "write",
  create_file: "write",
  save_file: "write",
  Write: "write",
  create: "write",
  // File read
  read_file: "read",
  view_file: "read",
  Read: "read",
  cat: "read",
  file_read: "read",
  // File edit
  edit_file: "edit",
  patch_file: "edit",
  replace_in_file: "edit",
  Edit: "edit",
  str_replace_editor: "edit",
  // Browser
  web_browse: "browser",
  browser: "browser",
  navigate: "browser",
  browse_url: "browser",
  web_browser: "browser",
  // Messaging
  send_message: "message",
  telegram_send: "message",
  discord_send: "message",
  slack_send: "message",
  whatsapp_send: "message",
  signal_send: "message",
  // Agent delegation
  delegate: "sessions_spawn",
  spawn_agent: "sessions_spawn",
  create_subagent: "sessions_spawn",
  Agent: "sessions_spawn",
  // Code execution
  code_execute: "code_execute",
  python_execute: "code_execute",
  run_python: "code_execute",
  // Web search
  web_search: "web_search",
  search: "web_search",
  // Image generation
  image_generate: "image_generate",
  generate_image: "image_generate",
  // Vision
  vision: "vision",
  analyze_image: "vision",
  // TTS
  text_to_speech: "tts",
  tts: "tts",
  // Memory
  memory_add: "memory",
  memory_search: "memory",
  memory_delete: "memory",
  // Task planning
  task_plan: "planning",
  todo: "planning",
  // Cron
  cron_add: "cron",
  cron_remove: "cron",
  cron_list: "cron",
};

/**
 * Strip a common MCP namespace prefix from a tool name.
 *
 * Handles `mcp__server__tool`, `server.tool`, `server/tool`, and `server:tool`.
 * The bare tool segment is returned for alias lookup; the original is preserved
 * by the caller for the `tool_name` wire field.
 */
export function stripNamespace(toolName: string): string {
  if (toolName.includes("__")) {
    // `mcp__<server>__<tool>` or `<server>__<tool>` -> last segment.
    const parts = toolName.split("__").filter((p) => p.length > 0);
    return parts.length > 0 ? (parts[parts.length - 1] as string) : toolName;
  }
  for (const sep of [".", "/", ":"]) {
    if (toolName.includes(sep)) {
      const parts = toolName.split(sep).filter((p) => p.length > 0);
      return parts.length > 0 ? (parts[parts.length - 1] as string) : toolName;
    }
  }
  return toolName;
}

/** Return the AgentShield canonical name for an MCP tool (unknown -> unchanged). */
export function normaliseToolName(toolName: string): string {
  const bare = stripNamespace(toolName);
  return TOOL_ALIASES[bare] ?? bare;
}

/**
 * Normalise an MCP tool call into its canonical name and a human-readable
 * command string used by the engine for Sigma field matching (Section 2.2).
 */
export function normaliseToolCall(
  toolName: string,
  args: Record<string, unknown>,
): { canonical: string; command: string | null } {
  const canonical = normaliseToolName(toolName);
  const command = buildCommand(canonical, toolName, args);
  return { canonical, command };
}

/** Return the semantic event type for a normalised tool call (Section 2.3). */
export function eventTypeForToolCall(canonical: string): string {
  switch (canonical) {
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

/** Build the per-canonical-name command string (contract Section 2.2). */
function buildCommand(
  canonical: string,
  original: string,
  args: Record<string, unknown>,
): string | null {
  switch (canonical) {
    case "exec": {
      const cmd = args.command ?? args.cmd;
      return typeof cmd === "string" && cmd ? cmd : null;
    }
    case "write":
      return `Write: ${filePath(args)}`;
    case "read":
      return `Read: ${filePath(args)}`;
    case "edit":
      return `Edit: ${filePath(args)}`;
    case "browser": {
      const action = str(args.action) || "navigate";
      const url = str(args.url);
      return `${action}: ${url}`.trim();
    }
    case "message": {
      const channel = str(args.channel) || str(args.chat_id) || str(args.to);
      if (channel) {
        return `Message to ${channel}`;
      }
      const platform = original.replace("_send", "").replace("send_", "");
      return `Message via ${platform}`;
    }
    case "sessions_spawn": {
      const agent =
        str(args.agent_id) || str(args.agent) || str(args.task) || str(args.agentId);
      return agent ? `Spawn: ${agent}` : "Spawn: <unknown>";
    }
    case "code_execute": {
      let code = str(args.code) || str(args.script);
      if (code.length > 200) {
        code = `${code.slice(0, 200)}...`;
      }
      return code ? `Execute: ${code}` : null;
    }
    case "web_search": {
      const query = str(args.query) || str(args.q);
      return query ? `Search: ${query}` : null;
    }
    default:
      return original;
  }
}

/** Extract a file path from the various argument styles MCP servers use. */
function filePath(args: Record<string, unknown>): string {
  for (const key of ["path", "file_path", "filepath", "filename", "file"]) {
    const val = args[key];
    if (typeof val === "string" && val) {
      return val;
    }
  }
  return "<unknown>";
}

function str(value: unknown): string {
  return value === null || value === undefined ? "" : String(value);
}
