"""Tests for tool name and command normalisation."""

from __future__ import annotations

from hermes.normalise import normalise_tool_call, normalise_tool_name


class TestNormaliseToolName:
    def test_terminal_maps_to_exec(self) -> None:
        assert normalise_tool_name("terminal") == "exec"

    def test_execute_command_maps_to_exec(self) -> None:
        assert normalise_tool_name("execute_command") == "exec"

    def test_write_file_maps_to_write(self) -> None:
        assert normalise_tool_name("write_file") == "write"

    def test_read_file_maps_to_read(self) -> None:
        assert normalise_tool_name("read_file") == "read"

    def test_edit_file_maps_to_edit(self) -> None:
        assert normalise_tool_name("edit_file") == "edit"

    def test_web_browse_maps_to_browser(self) -> None:
        assert normalise_tool_name("web_browse") == "browser"

    def test_send_message_maps_to_message(self) -> None:
        assert normalise_tool_name("send_message") == "message"

    def test_delegate_maps_to_sessions_spawn(self) -> None:
        assert normalise_tool_name("delegate") == "sessions_spawn"

    def test_code_execute_maps_to_code_execute(self) -> None:
        assert normalise_tool_name("code_execute") == "code_execute"

    def test_unknown_passes_through(self) -> None:
        assert normalise_tool_name("custom_widget") == "custom_widget"


class TestNormaliseToolCall:
    def test_exec_with_command(self) -> None:
        name, cmd = normalise_tool_call("terminal", {"command": "ls -la"})
        assert name == "exec"
        assert cmd == "ls -la"

    def test_exec_with_cmd_alias(self) -> None:
        name, cmd = normalise_tool_call("execute_command", {"cmd": "whoami"})
        assert name == "exec"
        assert cmd == "whoami"

    def test_exec_no_command(self) -> None:
        name, cmd = normalise_tool_call("terminal", {})
        assert name == "exec"
        assert cmd is None

    def test_write_with_path(self) -> None:
        name, cmd = normalise_tool_call("write_file", {"path": "/tmp/foo.txt"})
        assert name == "write"
        assert cmd == "Write: /tmp/foo.txt"

    def test_write_with_file_path(self) -> None:
        name, cmd = normalise_tool_call("create_file", {"file_path": "/tmp/bar.txt"})
        assert name == "write"
        assert cmd == "Write: /tmp/bar.txt"

    def test_read_with_path(self) -> None:
        name, cmd = normalise_tool_call("read_file", {"path": "/etc/hosts"})
        assert name == "read"
        assert cmd == "Read: /etc/hosts"

    def test_edit_with_path(self) -> None:
        name, cmd = normalise_tool_call("edit_file", {"path": "/src/main.py"})
        assert name == "edit"
        assert cmd == "Edit: /src/main.py"

    def test_browser_with_url(self) -> None:
        name, cmd = normalise_tool_call("web_browse", {
            "action": "navigate",
            "url": "https://example.com",
        })
        assert name == "browser"
        assert cmd == "navigate: https://example.com"

    def test_message_with_channel(self) -> None:
        name, cmd = normalise_tool_call("telegram_send", {"channel": "#general"})
        assert name == "message"
        assert cmd == "Message to #general"

    def test_message_without_channel(self) -> None:
        name, cmd = normalise_tool_call("telegram_send", {})
        assert name == "message"
        assert cmd == "Message via telegram"

    def test_sessions_spawn_with_agent(self) -> None:
        name, cmd = normalise_tool_call("delegate", {"agent_id": "research-agent"})
        assert name == "sessions_spawn"
        assert cmd == "Spawn: research-agent"

    def test_code_execute_with_code(self) -> None:
        name, cmd = normalise_tool_call("python_execute", {"code": "print('hello')"})
        assert name == "code_execute"
        assert cmd == "Execute: print('hello')"

    def test_code_execute_truncates_long_code(self) -> None:
        long_code = "x = 1\n" * 100  # > 200 chars
        name, cmd = normalise_tool_call("code_execute", {"code": long_code})
        assert name == "code_execute"
        assert cmd is not None
        assert cmd.endswith("...")
        assert len(cmd) <= 220  # "Execute: " + 200 + "..."

    def test_web_search(self) -> None:
        name, cmd = normalise_tool_call("web_search", {"query": "python dataclass"})
        assert name == "web_search"
        assert cmd == "Search: python dataclass"

    def test_unknown_tool_uses_name_as_command(self) -> None:
        name, cmd = normalise_tool_call("custom_tool", {"foo": "bar"})
        assert name == "custom_tool"
        assert cmd == "custom_tool"

    def test_file_ops_with_no_path(self) -> None:
        name, cmd = normalise_tool_call("write_file", {})
        assert name == "write"
        assert cmd == "Write: <unknown>"
