"""Map Gemini CLI tool names and parameters to AgentShield canonical forms.

The Gemini CLI ships its own built-in tool vocabulary (``run_shell_command``,
``read_file``, ``write_file``, ``replace``, ``web_fetch``,
``google_web_search`` and friends).  This module normalises both the
*tool name* (so the same Sigma rules match regardless of platform) and a
human-readable *command string* used by the detection engine for field
matching.  The canonical vocabulary and command-string construction
mirror the Hermes and OpenClaw reference connectors exactly.
"""

from __future__ import annotations

from typing import Any, Dict, Optional, Tuple

# Gemini CLI tool name -> AgentShield canonical name.
#
# The values on the right are the canonical AgentShield names shared by
# every connector.  Names not present here pass through unchanged so that
# unknown / MCP-provided tools are never silently dropped.
_TOOL_ALIASES: Dict[str, str] = {
    # Terminal / exec
    "run_shell_command": "exec",
    "shell": "exec",
    "execute_command": "exec",
    "terminal": "exec",
    # File write
    "write_file": "write",
    "create_file": "write",
    "save_file": "write",
    # File read
    "read_file": "read",
    "read_many_files": "read",
    "view_file": "read",
    # File edit
    "replace": "edit",
    "edit_file": "edit",
    "patch_file": "edit",
    # Browser / web retrieval
    "web_fetch": "browser",
    "web_browse": "browser",
    "browser": "browser",
    "navigate": "browser",
    # Messaging
    "send_message": "message",
    "slack_send": "message",
    "discord_send": "message",
    # Agent delegation / sub-agents
    "delegate": "sessions_spawn",
    "spawn_agent": "sessions_spawn",
    "create_subagent": "sessions_spawn",
    # Code execution
    "code_execute": "code_execute",
    "python_execute": "code_execute",
    "run_python": "code_execute",
    # Web search
    "google_web_search": "web_search",
    "web_search": "web_search",
    "search": "web_search",
    # Memory / skills
    "save_memory": "memory",
    "memory_add": "memory",
    "memory_search": "memory",
    # Planning / task tracking
    "write_todos": "planning",
    "task_plan": "planning",
    "todo": "planning",
    "enter_plan_mode": "planning",
    "exit_plan_mode": "planning",
    "tracker_create_task": "planning",
    "tracker_update_task": "planning",
}


def normalise_tool_name(gemini_name: str) -> str:
    """Return the AgentShield canonical name for a Gemini CLI tool.

    Args:
        gemini_name: The platform-specific tool name supplied by Gemini CLI.

    Returns:
        The canonical AgentShield tool name. Unknown tools pass through
        unchanged so they are evaluated rather than dropped.
    """
    return _TOOL_ALIASES.get(gemini_name, gemini_name)


def normalise_tool_call(
    tool_name: str,
    args: Dict[str, Any],
) -> Tuple[str, Optional[str]]:
    """Normalise a Gemini CLI tool call into ``(canonical_name, command)``.

    Args:
        tool_name: The Gemini CLI tool name.
        args: The tool arguments (the ``tool_input`` object).

    Returns:
        A tuple of the canonical tool name and a human-readable command
        string (or ``None``) used by the engine for Sigma field matching.
    """
    canonical = normalise_tool_name(tool_name)
    command = _build_command(canonical, tool_name, args)
    return canonical, command


def event_type_for_tool_call(canonical: str, args: Dict[str, Any]) -> str:
    """Return the semantic event type for a normalised Gemini tool call.

    Args:
        canonical: The canonical tool name.
        args: The tool arguments (reserved for future action-sensitive maps).

    Returns:
        One of ``file_read``, ``file_write``, ``file_edit``,
        ``session_spawn`` or ``tool_call``.
    """
    del args  # Reserved for future action-sensitive mappings.

    if canonical in ("read", "file_read"):
        return "file_read"
    if canonical in ("write", "file_write", "create"):
        return "file_write"
    if canonical in ("edit", "file_edit"):
        return "file_edit"
    if canonical in ("sessions_spawn", "session_spawn"):
        return "session_spawn"
    return "tool_call"


def _build_command(
    canonical: str,
    original: str,
    args: Dict[str, Any],
) -> Optional[str]:
    """Build the human-readable command string for a canonical tool.

    Args:
        canonical: The canonical tool name.
        original: The original Gemini CLI tool name.
        args: The tool arguments.

    Returns:
        A command string suitable for Sigma matching, or ``None`` when the
        tool carries no meaningful command payload.
    """
    if canonical == "exec":
        cmd = args.get("command") or args.get("cmd")
        return str(cmd) if cmd else None

    if canonical in ("write", "read", "edit"):
        return f"{canonical.capitalize()}: {_file_path(args)}"

    if canonical == "browser":
        # Gemini's web_fetch takes a free-text ``prompt`` that frequently
        # embeds the URL; fall back to it when no explicit url is present.
        action = _str(args.get("action", "navigate"))
        url = _str(args.get("url") or args.get("prompt", ""))
        return f"{action}: {url}".strip()

    if canonical == "message":
        channel = _str(args.get("channel") or args.get("chat_id") or args.get("to", ""))
        platform = original.replace("_send", "").replace("send_", "")
        return f"Message to {channel}" if channel else f"Message via {platform}"

    if canonical == "sessions_spawn":
        agent = _str(
            args.get("agent_id")
            or args.get("agent")
            or args.get("task")
            or args.get("agentId", "")
        )
        return f"Spawn: {agent}" if agent else "Spawn: <unknown>"

    if canonical == "code_execute":
        code = _str(args.get("code") or args.get("script", ""))
        if len(code) > 200:
            code = code[:200] + "..."
        return f"Execute: {code}" if code else None

    if canonical == "web_search":
        query = _str(args.get("query") or args.get("q", ""))
        return f"Search: {query}" if query else None

    # Fallback: use the original tool name.
    return original


def _file_path(args: Dict[str, Any]) -> str:
    """Extract a file path from the various Gemini argument styles.

    Args:
        args: The tool arguments.

    Returns:
        The first matching path-like value, or ``"<unknown>"``.
    """
    for key in ("path", "file_path", "filepath", "filename", "file", "absolute_path"):
        val = args.get(key)
        if isinstance(val, str) and val:
            return val
    return "<unknown>"


def _str(value: Any) -> str:
    """Coerce *value* to a string, mapping ``None`` to the empty string."""
    return str(value) if value is not None else ""
