"""Tests for Codex tool name and command normalisation."""

from __future__ import annotations

from codex.normalise import (
    event_type_for_tool_call,
    normalise_tool_call,
    normalise_tool_name,
)


class TestNormaliseToolName:
    def test_bash_maps_to_exec(self) -> None:
        assert normalise_tool_name("Bash") == "exec"

    def test_lowercase_shell_maps_to_exec(self) -> None:
        assert normalise_tool_name("shell") == "exec"

    def test_apply_patch_maps_to_edit(self) -> None:
        assert normalise_tool_name("apply_patch") == "edit"

    def test_write_file_maps_to_write(self) -> None:
        assert normalise_tool_name("write_file") == "write"

    def test_read_file_maps_to_read(self) -> None:
        assert normalise_tool_name("read_file") == "read"

    def test_mcp_tool_uses_suffix_alias(self) -> None:
        assert normalise_tool_name("mcp__filesystem__read_file") == "read"

    def test_mcp_tool_uses_suffix_bash(self) -> None:
        assert normalise_tool_name("mcp__shellserver__run_command") == "exec"

    def test_mcp_unknown_suffix_passes_through(self) -> None:
        assert normalise_tool_name("mcp__weather__get_forecast") == "get_forecast"

    def test_unknown_passes_through(self) -> None:
        assert normalise_tool_name("custom_widget") == "custom_widget"


class TestNormaliseToolCall:
    def test_bash_with_command(self) -> None:
        name, cmd = normalise_tool_call("Bash", {"command": "rm -rf /"})
        assert name == "exec"
        assert cmd == "rm -rf /"

    def test_bash_no_command(self) -> None:
        name, cmd = normalise_tool_call("Bash", {})
        assert name == "exec"
        assert cmd is None

    def test_apply_patch_with_path(self) -> None:
        name, cmd = normalise_tool_call("apply_patch", {"path": "/src/main.py"})
        assert name == "edit"
        assert cmd == "Edit: /src/main.py"

    def test_apply_patch_no_path(self) -> None:
        name, cmd = normalise_tool_call("apply_patch", {"command": "*** patch ***"})
        assert name == "edit"
        assert cmd == "Edit: <unknown>"

    def test_write_with_file_path(self) -> None:
        name, cmd = normalise_tool_call("write_file", {"file_path": "/tmp/bar.txt"})
        assert name == "write"
        assert cmd == "Write: /tmp/bar.txt"

    def test_read_with_path(self) -> None:
        name, cmd = normalise_tool_call("read_file", {"path": "/etc/hosts"})
        assert name == "read"
        assert cmd == "Read: /etc/hosts"

    def test_browser_with_url(self) -> None:
        name, cmd = normalise_tool_call(
            "web_browse", {"action": "navigate", "url": "https://example.com"}
        )
        assert name == "browser"
        assert cmd == "navigate: https://example.com"

    def test_message_with_channel(self) -> None:
        name, cmd = normalise_tool_call("slack_send", {"channel": "#general"})
        assert name == "message"
        assert cmd == "Message to #general"

    def test_sessions_spawn_with_agent(self) -> None:
        name, cmd = normalise_tool_call("delegate", {"agent_id": "research-agent"})
        assert name == "sessions_spawn"
        assert cmd == "Spawn: research-agent"

    def test_code_execute_truncates_long_code(self) -> None:
        long_code = "x = 1\n" * 100
        name, cmd = normalise_tool_call("code_execute", {"code": long_code})
        assert name == "code_execute"
        assert cmd is not None
        assert cmd.endswith("...")
        assert len(cmd) <= 220

    def test_web_search(self) -> None:
        name, cmd = normalise_tool_call("web_search", {"query": "python dataclass"})
        assert name == "web_search"
        assert cmd == "Search: python dataclass"

    def test_unknown_tool_uses_name_as_command(self) -> None:
        name, cmd = normalise_tool_call("custom_tool", {"foo": "bar"})
        assert name == "custom_tool"
        assert cmd == "custom_tool"


class TestEventTypeForToolCall:
    def test_read_is_file_read(self) -> None:
        assert event_type_for_tool_call("read", {}) == "file_read"

    def test_write_is_file_write(self) -> None:
        assert event_type_for_tool_call("write", {}) == "file_write"

    def test_edit_is_file_edit(self) -> None:
        assert event_type_for_tool_call("edit", {}) == "file_edit"

    def test_sessions_spawn_is_session_spawn(self) -> None:
        assert event_type_for_tool_call("sessions_spawn", {}) == "session_spawn"

    def test_exec_is_tool_call(self) -> None:
        assert event_type_for_tool_call("exec", {}) == "tool_call"
