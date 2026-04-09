"""Map Hermes tool names and parameters to AgentShield canonical forms.

Hermes ships 40+ built-in tools whose names vary from those used by
OpenClaw.  This module normalises both the *tool name* (so Sigma rules
can match regardless of platform) and a *command string* that provides
human-readable context for the detection engine.
"""

from __future__ import annotations

from typing import Any, Dict, Optional, Tuple

# Hermes tool name -> AgentShield canonical name
_TOOL_ALIASES: Dict[str, str] = {
    # Terminal / exec
    "terminal": "exec",
    "execute_command": "exec",
    "run_command": "exec",
    "shell": "exec",
    # File write
    "write_file": "write",
    "create_file": "write",
    "save_file": "write",
    # File read
    "read_file": "read",
    "view_file": "read",
    # File edit
    "edit_file": "edit",
    "patch_file": "edit",
    "replace_in_file": "edit",
    # Browser
    "web_browse": "browser",
    "browser": "browser",
    "navigate": "browser",
    "browse_url": "browser",
    # Messaging
    "send_message": "message",
    "telegram_send": "message",
    "discord_send": "message",
    "slack_send": "message",
    "whatsapp_send": "message",
    "signal_send": "message",
    # Agent delegation
    "delegate": "sessions_spawn",
    "spawn_agent": "sessions_spawn",
    "create_subagent": "sessions_spawn",
    # Code execution
    "code_execute": "code_execute",
    "python_execute": "code_execute",
    "run_python": "code_execute",
    # Web search
    "web_search": "web_search",
    "search": "web_search",
    # Image generation
    "image_generate": "image_generate",
    "generate_image": "image_generate",
    # Vision
    "vision": "vision",
    "analyze_image": "vision",
    # TTS
    "text_to_speech": "tts",
    "tts": "tts",
    # Memory
    "memory_add": "memory",
    "memory_search": "memory",
    "memory_delete": "memory",
    # Task planning
    "task_plan": "planning",
    "todo": "planning",
    # Cron
    "cron_add": "cron",
    "cron_remove": "cron",
    "cron_list": "cron",
}


def normalise_tool_name(hermes_name: str) -> str:
    """Return the AgentShield canonical name for a Hermes tool.

    Unknown tools pass through unchanged.
    """
    return _TOOL_ALIASES.get(hermes_name, hermes_name)


def normalise_tool_call(
    tool_name: str,
    args: Dict[str, Any],
) -> Tuple[str, Optional[str]]:
    """Normalise a Hermes tool call into (canonical_name, command_string).

    Returns the canonical tool name and a human-readable command string
    that the AgentShield engine uses for Sigma rule field matching.
    """
    canonical = normalise_tool_name(tool_name)

    command = _build_command(canonical, tool_name, args)
    return canonical, command


def event_type_for_tool_call(canonical: str, args: Dict[str, Any]) -> str:
    """Return the semantic event type for a normalised Hermes tool call."""
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
    """Build a command string from the canonical name and args."""
    if canonical == "exec":
        cmd = args.get("command") or args.get("cmd")
        return str(cmd) if cmd else None

    if canonical in ("write", "read", "edit"):
        return f"{canonical.capitalize()}: {_file_path(args)}"

    if canonical == "browser":
        action = _str(args.get("action", "navigate"))
        url = _str(args.get("url", ""))
        return f"{action}: {url}".strip()

    if canonical == "message":
        # Hermes messaging tools embed the platform in the tool name
        channel = _str(args.get("channel") or args.get("chat_id") or args.get("to", ""))
        platform = original.replace("_send", "").replace("send_", "")
        return f"Message to {channel}" if channel else f"Message via {platform}"

    if canonical == "sessions_spawn":
        agent = _str(args.get("agent_id") or args.get("agent") or args.get("task", ""))
        return f"Spawn: {agent}" if agent else "Spawn: <unknown>"

    if canonical == "code_execute":
        code = _str(args.get("code") or args.get("script", ""))
        if len(code) > 200:
            code = code[:200] + "..."
        return f"Execute: {code}" if code else None

    if canonical == "web_search":
        query = _str(args.get("query") or args.get("q", ""))
        return f"Search: {query}" if query else None

    # Fallback: use original tool name
    return original


def _file_path(args: Dict[str, Any]) -> str:
    """Extract file path from various Hermes arg styles."""
    for key in ("path", "file_path", "filepath", "filename", "file"):
        val = args.get(key)
        if isinstance(val, str) and val:
            return val
    return "<unknown>"


def _str(value: Any) -> str:
    return str(value) if value is not None else ""
