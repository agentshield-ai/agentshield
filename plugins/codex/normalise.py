"""Map OpenAI Codex CLI tool names and parameters to AgentShield canonical forms.

Codex CLI fires lifecycle hooks (``PreToolUse``, ``PostToolUse``, ...) for a
small set of built-in tools -- ``Bash`` (shell commands), ``apply_patch``
(file edits), plus any MCP tool, which Codex names ``mcp__<server>__<tool>``.

This module normalises both the *tool name* (so Sigma rules can match
regardless of platform) and a *command string* that provides human-readable
context for the detection engine.  The canonical vocabulary and command-string
rules are identical to the Hermes and OpenClaw connectors so that the same
detection rules fire across every platform.
"""

from __future__ import annotations

from typing import Any, Dict, Optional, Tuple

# Codex CLI tool name -> AgentShield canonical name.
#
# Codex's built-in tools are ``Bash`` and ``apply_patch``; MCP tools arrive as
# ``mcp__<server>__<tool>`` and are normalised by the suffix (see
# :func:`normalise_tool_name`).  The wider alias set mirrors the Hermes table so
# that MCP servers exposing common tool names map onto the same canonical names.
_TOOL_ALIASES: Dict[str, str] = {
    # Terminal / exec -- Codex's native shell tool is "Bash".
    "Bash": "exec",
    "bash": "exec",
    "shell": "exec",
    "terminal": "exec",
    "execute_command": "exec",
    "run_command": "exec",
    "computer": "exec",
    # File edit -- Codex applies file changes via "apply_patch".
    "apply_patch": "edit",
    "edit_file": "edit",
    "patch_file": "edit",
    "replace_in_file": "edit",
    "Edit": "edit",
    "str_replace_editor": "edit",
    # File write
    "write_file": "write",
    "create_file": "write",
    "save_file": "write",
    "Write": "write",
    "create": "write",
    # File read
    "read_file": "read",
    "view_file": "read",
    "Read": "read",
    "cat": "read",
    "file_read": "read",
    # Browser
    "web_browse": "browser",
    "browser": "browser",
    "navigate": "browser",
    "browse_url": "browser",
    "web_browser": "browser",
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
    "Agent": "sessions_spawn",
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

# MCP tool names from Codex look like "mcp__<server>__<tool>".
_MCP_PREFIX = "mcp__"


def normalise_tool_name(codex_name: str) -> str:
    """Return the AgentShield canonical name for a Codex tool.

    MCP tools (named ``mcp__<server>__<tool>``) are reduced to their trailing
    ``<tool>`` segment before alias lookup, so an MCP ``run_command`` tool maps
    to ``exec`` just like the native ``Bash`` tool.  Unknown tools pass through
    unchanged.

    Args:
        codex_name: The raw tool name supplied by Codex in ``tool_name``.

    Returns:
        The canonical AgentShield tool name.
    """
    if codex_name.startswith(_MCP_PREFIX):
        suffix = codex_name.split("__")[-1]
        return _TOOL_ALIASES.get(suffix, suffix)
    return _TOOL_ALIASES.get(codex_name, codex_name)


def normalise_tool_call(
    tool_name: str,
    args: Dict[str, Any],
) -> Tuple[str, Optional[str]]:
    """Normalise a Codex tool call into ``(canonical_name, command_string)``.

    Args:
        tool_name: The raw Codex tool name (``Bash``, ``apply_patch``, MCP name).
        args: The ``tool_input`` object supplied by Codex.

    Returns:
        A tuple of the canonical tool name and a human-readable command string
        used by the engine for Sigma field matching (may be ``None``).
    """
    canonical = normalise_tool_name(tool_name)
    command = _build_command(canonical, tool_name, args)
    return canonical, command


def event_type_for_tool_call(canonical: str, args: Dict[str, Any]) -> str:
    """Return the semantic event type for a normalised Codex tool call.

    Args:
        canonical: The canonical tool name.
        args: The tool input (reserved for future action-sensitive mappings).

    Returns:
        One of ``file_read``, ``file_write``, ``file_edit``, ``session_spawn``
        or ``tool_call``.
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
    """Build a human-readable command string from the canonical name and args.

    Args:
        canonical: The canonical tool name.
        original: The raw Codex tool name (used for messaging platform names).
        args: The tool input object.

    Returns:
        A command string for Sigma matching, or ``None`` when no useful string
        can be derived.
    """
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
    """Extract a file path from Codex/MCP argument styles.

    Codex ``apply_patch`` typically embeds the path in ``command``/``patch``;
    MCP file tools may use ``path``/``file_path``.  We probe the common keys and
    fall back to ``<unknown>``.

    Args:
        args: The tool input object.

    Returns:
        The first non-empty path-like value, or ``<unknown>``.
    """
    for key in ("path", "file_path", "filepath", "filename", "file"):
        val = args.get(key)
        if isinstance(val, str) and val:
            return val
    return "<unknown>"


def _str(value: Any) -> str:
    """Coerce a value to a string, treating ``None`` as empty.

    Args:
        value: Any value.

    Returns:
        ``str(value)`` or an empty string when ``value`` is ``None``.
    """
    return str(value) if value is not None else ""
