"""Tests for Gemini tool name and command normalisation."""

from __future__ import annotations

from agentshield_gemini.normalise import (
    event_type_for_tool_call,
    normalise_tool_call,
    normalise_tool_name,
)


class TestNormaliseToolName:
    def test_run_shell_command_maps_to_exec(self) -> None:
        assert normalise_tool_name("run_shell_command") == "exec"

    def test_write_file_maps_to_write(self) -> None:
        assert normalise_tool_name("write_file") == "write"

    def test_read_file_maps_to_read(self) -> None:
        assert normalise_tool_name("read_file") == "read"

    def test_read_many_files_maps_to_read(self) -> None:
        assert normalise_tool_name("read_many_files") == "read"

    def test_replace_maps_to_edit(self) -> None:
        assert normalise_tool_name("replace") == "edit"

    def test_web_fetch_maps_to_browser(self) -> None:
        assert normalise_tool_name("web_fetch") == "browser"

    def test_google_web_search_maps_to_web_search(self) -> None:
        assert normalise_tool_name("google_web_search") == "web_search"

    def test_save_memory_maps_to_memory(self) -> None:
        assert normalise_tool_name("save_memory") == "memory"

    def test_write_todos_maps_to_planning(self) -> None:
        assert normalise_tool_name("write_todos") == "planning"

    def test_unknown_passes_through(self) -> None:
        assert normalise_tool_name("some_mcp_tool") == "some_mcp_tool"


class TestNormaliseToolCall:
    def test_exec_with_command(self) -> None:
        name, cmd = normalise_tool_call("run_shell_command", {"command": "ls -la"})
        assert name == "exec"
        assert cmd == "ls -la"

    def test_exec_no_command(self) -> None:
        name, cmd = normalise_tool_call("run_shell_command", {})
        assert name == "exec"
        assert cmd is None

    def test_write_with_file_path(self) -> None:
        name, cmd = normalise_tool_call("write_file", {"file_path": "/tmp/foo.txt"})
        assert name == "write"
        assert cmd == "Write: /tmp/foo.txt"

    def test_write_with_absolute_path(self) -> None:
        name, cmd = normalise_tool_call("write_file", {"absolute_path": "/tmp/abs.txt"})
        assert name == "write"
        assert cmd == "Write: /tmp/abs.txt"

    def test_read_with_file_path(self) -> None:
        name, cmd = normalise_tool_call("read_file", {"file_path": "/etc/hosts"})
        assert name == "read"
        assert cmd == "Read: /etc/hosts"

    def test_edit_with_file_path(self) -> None:
        name, cmd = normalise_tool_call("replace", {"file_path": "/src/main.py"})
        assert name == "edit"
        assert cmd == "Edit: /src/main.py"

    def test_browser_with_url(self) -> None:
        name, cmd = normalise_tool_call(
            "web_fetch", {"action": "navigate", "url": "https://example.com"}
        )
        assert name == "browser"
        assert cmd == "navigate: https://example.com"

    def test_browser_falls_back_to_prompt(self) -> None:
        name, cmd = normalise_tool_call(
            "web_fetch", {"prompt": "fetch https://example.com/page"}
        )
        assert name == "browser"
        assert cmd == "navigate: fetch https://example.com/page"

    def test_web_search(self) -> None:
        name, cmd = normalise_tool_call("google_web_search", {"query": "python dataclass"})
        assert name == "web_search"
        assert cmd == "Search: python dataclass"

    def test_code_execute_truncates_long_code(self) -> None:
        long_code = "x = 1\n" * 100  # > 200 chars
        name, cmd = normalise_tool_call("code_execute", {"code": long_code})
        assert name == "code_execute"
        assert cmd is not None
        assert cmd.endswith("...")
        assert len(cmd) <= 220

    def test_unknown_tool_uses_name_as_command(self) -> None:
        name, cmd = normalise_tool_call("custom_tool", {"foo": "bar"})
        assert name == "custom_tool"
        assert cmd == "custom_tool"

    def test_file_ops_with_no_path(self) -> None:
        name, cmd = normalise_tool_call("write_file", {})
        assert name == "write"
        assert cmd == "Write: <unknown>"


class TestEventType:
    def test_read_event_type(self) -> None:
        assert event_type_for_tool_call("read", {}) == "file_read"

    def test_write_event_type(self) -> None:
        assert event_type_for_tool_call("write", {}) == "file_write"

    def test_edit_event_type(self) -> None:
        assert event_type_for_tool_call("edit", {}) == "file_edit"

    def test_spawn_event_type(self) -> None:
        assert event_type_for_tool_call("sessions_spawn", {}) == "session_spawn"

    def test_default_event_type(self) -> None:
        assert event_type_for_tool_call("exec", {}) == "tool_call"
