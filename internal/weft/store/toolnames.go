package store

// Tool names in AgentShield verdicts and in harness spans.
//
// AgentShield's connectors report a tool by its canonical name (exec, read,
// write, edit, browser, web_search, sessions_spawn, ...), while a harness's
// own telemetry names it as the harness does (Gemini CLI's
// run_shell_command, Codex's exec_command, OpenCode's bash). A verdict
// without a tool-call id links to a tool span by session, time and tool name
// (nearestToolSpanTx, linkToolSpanDepth); there the two names are equal when
// they map to the same canonical name in the verdict's harness.
//
// One table per harness: the harness's tool name → AgentShield's canonical
// name. The Gemini, Codex and Claude Code tables mirror the connectors'
// tables (plugins/gemini/agentshield_gemini/normalise.py,
// plugins/codex/normalise.py, plugins/claude/agentshield-lib.sh
// as_normalise_name; toolnames_test.go checks they stay in sync) and add the
// names the harness's own telemetry uses for the same tools. OpenCode and
// Copilot CLI have no AgentShield connector here; their tables map their
// built-in tools onto the same vocabulary. A name in no table is its own
// canonical name, so a verdict that names the tool as the harness does still
// matches. Names compare ASCII-case-insensitively (as SQLite's NOCASE).

import "strings"

const (
	harnessGemini     = "gemini"
	harnessCodex      = "codex"
	harnessClaudeCode = "claude-code"
	harnessOpenCode   = "opencode"
	harnessCopilot    = "copilot"
)

// harnessToolAliases maps, per harness, a tool name to its canonical name.
var harnessToolAliases = map[string]map[string]string{
	// plugins/gemini/agentshield_gemini/normalise.py _TOOL_ALIASES. Gemini
	// CLI's glob, grep / search_file_content, list_directory and sub-agent
	// tools are not mapped by the connector, so verdicts carry those names.
	harnessGemini: {
		"run_shell_command": "exec", "shell": "exec", "execute_command": "exec", "terminal": "exec",
		"write_file": "write", "create_file": "write", "save_file": "write",
		"read_file": "read", "read_many_files": "read", "view_file": "read",
		"replace": "edit", "edit_file": "edit", "patch_file": "edit",
		"web_fetch": "browser", "web_browse": "browser", "browser": "browser", "navigate": "browser",
		"send_message": "message", "slack_send": "message", "discord_send": "message",
		"delegate": "sessions_spawn", "spawn_agent": "sessions_spawn", "create_subagent": "sessions_spawn",
		"code_execute": "code_execute", "python_execute": "code_execute", "run_python": "code_execute",
		"google_web_search": "web_search", "web_search": "web_search", "search": "web_search",
		"save_memory": "memory", "memory_add": "memory", "memory_search": "memory",
		"write_todos": "planning", "task_plan": "planning", "todo": "planning", "enter_plan_mode": "planning",
		"exit_plan_mode": "planning", "tracker_create_task": "planning", "tracker_update_task": "planning",
	},
	// plugins/codex/normalise.py _TOOL_ALIASES (Codex's hook names its shell
	// tool "Bash"), plus the names in Codex's own telemetry: exec_command
	// (unified exec), shell_command, local_shell.
	harnessCodex: {
		"Bash": "exec", "bash": "exec", "shell": "exec", "terminal": "exec", "execute_command": "exec",
		"run_command": "exec", "computer": "exec",
		"exec_command": "exec", "shell_command": "exec", "local_shell": "exec",
		"apply_patch": "edit", "edit_file": "edit", "patch_file": "edit", "replace_in_file": "edit", "Edit": "edit",
		"str_replace_editor": "edit",
		"write_file":         "write", "create_file": "write", "save_file": "write", "Write": "write", "create": "write",
		"read_file": "read", "view_file": "read", "Read": "read", "cat": "read", "file_read": "read",
		"web_browse": "browser", "browser": "browser", "navigate": "browser", "browse_url": "browser", "web_browser": "browser",
		"send_message": "message", "telegram_send": "message", "discord_send": "message", "slack_send": "message",
		"whatsapp_send": "message", "signal_send": "message",
		"delegate": "sessions_spawn", "spawn_agent": "sessions_spawn", "create_subagent": "sessions_spawn", "Agent": "sessions_spawn",
		"code_execute": "code_execute", "python_execute": "code_execute", "run_python": "code_execute",
		"web_search": "web_search", "search": "web_search",
		"image_generate": "image_generate", "generate_image": "image_generate",
		"vision": "vision", "analyze_image": "vision",
		"text_to_speech": "tts", "tts": "tts",
		"memory_add": "memory", "memory_search": "memory", "memory_delete": "memory",
		"task_plan": "planning", "todo": "planning",
		"cron_add": "cron", "cron_remove": "cron", "cron_list": "cron",
	},
	// plugins/claude/agentshield-lib.sh as_normalise_name.
	harnessClaudeCode: {
		"terminal": "exec", "execute_command": "exec", "run_command": "exec", "shell": "exec", "bash": "exec", "Bash": "exec",
		"computer":   "exec",
		"write_file": "write", "create_file": "write", "save_file": "write", "Write": "write", "create": "write",
		"read_file": "read", "view_file": "read", "Read": "read", "cat": "read", "file_read": "read", "NotebookRead": "read",
		"edit_file": "edit", "patch_file": "edit", "replace_in_file": "edit", "Edit": "edit", "MultiEdit": "edit",
		"NotebookEdit": "edit", "str_replace_editor": "edit",
		"web_browse": "browser", "browser": "browser", "navigate": "browser", "browse_url": "browser", "web_browser": "browser",
		"WebFetch":     "browser",
		"send_message": "message", "telegram_send": "message", "discord_send": "message", "slack_send": "message",
		"whatsapp_send": "message", "signal_send": "message",
		"delegate": "sessions_spawn", "spawn_agent": "sessions_spawn", "create_subagent": "sessions_spawn", "Agent": "sessions_spawn",
		"Task":         "sessions_spawn",
		"code_execute": "code_execute", "python_execute": "code_execute", "run_python": "code_execute",
		"web_search": "web_search", "search": "web_search", "WebSearch": "web_search",
		"image_generate": "image_generate", "generate_image": "image_generate",
		"vision": "vision", "analyze_image": "vision",
		"text_to_speech": "tts", "tts": "tts",
		"memory_add": "memory", "memory_search": "memory", "memory_delete": "memory",
		"task_plan": "planning", "todo": "planning", "TodoWrite": "planning",
		"cron_add": "cron", "cron_remove": "cron", "cron_list": "cron",
	},
	// OpenCode's built-in tools (ai.toolCall.name). read, write, edit, glob,
	// grep and list are already AgentShield's names or unmapped.
	harnessOpenCode: {
		"bash": "exec", "multiedit": "edit", "patch": "edit", "apply_patch": "edit",
		"webfetch": "browser", "websearch": "web_search", "task": "sessions_spawn", "todowrite": "planning",
	},
	// Copilot CLI's built-in tools (gen_ai.tool.name). read_bash /
	// write_bash talk to a running shell and are not mapped (a verdict on
	// the command belongs to its bash call).
	harnessCopilot: {
		"bash": "exec", "powershell": "exec", "view": "read", "create": "write", "str_replace_editor": "edit",
		"web_fetch": "browser", "task": "sessions_spawn",
	},
}

// harnessBySource maps a verdict's event_source (the connector's source
// field) to its harness, after lower-casing and replacing '_' with '-'.
var harnessBySource = map[string]string{
	"gemini": harnessGemini, "gemini-cli": harnessGemini,
	"codex": harnessCodex, "codex-cli": harnessCodex,
	"claude-code": harnessClaudeCode, "claude": harnessClaudeCode, "claudecode": harnessClaudeCode,
	"opencode": harnessOpenCode,
	"copilot":  harnessCopilot, "copilot-cli": harnessCopilot, "github-copilot": harnessCopilot,
}

// toolCanon holds the tables with ASCII-lower-cased keys and values.
var toolCanon = func() map[string]map[string]string {
	out := make(map[string]map[string]string, len(harnessToolAliases))
	for h, t := range harnessToolAliases {
		m := make(map[string]string, len(t))
		for k, v := range t {
			m[asciiLower(k)] = asciiLower(v)
		}
		out[h] = m
	}
	return out
}()

// asciiLower lower-cases ASCII letters only, as SQLite's NOCASE compares.
func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
}

// harnessOf names the harness of a verdict's event_source ("" when unknown).
func harnessOf(eventSource string) string {
	return harnessBySource[strings.ReplaceAll(asciiLower(strings.TrimSpace(eventSource)), "_", "-")]
}

// canonicalTool is a tool's canonical name in a harness (lower-cased).
func canonicalTool(harness, name string) string {
	n := asciiLower(name)
	if c, ok := toolCanon[harness][n]; ok {
		return c
	}
	return n
}

// ToolNamesMatch reports whether a verdict's tool name and a span's tool
// name name the same tool: equal ignoring ASCII case, or mapped to the same
// canonical name by the table of the verdict's harness (its event_source).
// For a verdict whose harness is unknown, any harness's table may match.
func ToolNamesMatch(eventSource, verdictTool, spanTool string) bool {
	if verdictTool == "" || spanTool == "" {
		return false
	}
	if asciiLower(verdictTool) == asciiLower(spanTool) {
		return true
	}
	if h := harnessOf(eventSource); h != "" {
		return canonicalTool(h, verdictTool) == canonicalTool(h, spanTool)
	}
	for h := range toolCanon {
		if canonicalTool(h, verdictTool) == canonicalTool(h, spanTool) {
			return true
		}
	}
	return false
}
