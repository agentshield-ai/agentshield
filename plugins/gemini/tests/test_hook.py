"""Tests for the Gemini hook dispatcher and BeforeTool decision logic."""

from __future__ import annotations

from pathlib import Path
from typing import Any, Dict, List

import pytest

from agentshield_gemini import hook
from agentshield_gemini.config import parse_config


class _StubClient:
    """Records calls and returns a canned evaluate response."""

    def __init__(self, response: Dict[str, Any] | None = None, raise_on_eval: bool = False) -> None:
        self._response = response or {"action": "allow"}
        self._raise = raise_on_eval
        self.audits: List[Dict[str, Any]] = []
        self.lifecycles: List[Dict[str, Any]] = []
        self.health_called = False

    def evaluate(self, request: Dict[str, Any]) -> Dict[str, Any]:
        if self._raise:
            raise RuntimeError("engine down")
        return self._response

    def send_audit(self, report: Dict[str, Any]) -> None:
        self.audits.append(report)

    def send_lifecycle(self, event: Dict[str, Any]) -> None:
        self.lifecycles.append(event)

    def health_check(self) -> bool:
        self.health_called = True
        return True


@pytest.fixture(autouse=True)
def _isolate_state(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("AGENTSHIELD_GEMINI_STATE_DIR", str(tmp_path / "state"))


def _cfg(**kw: Any) -> Any:
    return parse_config(kw)


class TestBeforeTool:
    def test_allow_returns_empty(self) -> None:
        client = _StubClient({"action": "allow"})
        payload = {
            "hook_event_name": "BeforeTool",
            "tool_name": "run_shell_command",
            "tool_input": {"command": "ls"},
            "session_id": "s1",
        }
        out = hook.handle_before_tool(payload, _cfg(), client)
        assert out == {}

    def test_block_returns_deny(self) -> None:
        client = _StubClient(
            {"action": "block", "reason": "dangerous", "alerts": []}
        )
        payload = {
            "hook_event_name": "BeforeTool",
            "tool_name": "run_shell_command",
            "tool_input": {"command": "rm -rf /"},
            "session_id": "s1",
        }
        out = hook.handle_before_tool(payload, _cfg(), client)
        assert out["decision"] == "deny"
        assert "dangerous" in out["reason"]

    def test_require_approval_fails_closed(self) -> None:
        client = _StubClient({"action": "require_approval", "reason": "needs ok"})
        payload = {
            "hook_event_name": "BeforeTool",
            "tool_name": "write_file",
            "tool_input": {"file_path": "/etc/passwd"},
            "session_id": "s1",
        }
        out = hook.handle_before_tool(payload, _cfg(), client)
        assert out["decision"] == "deny"

    def test_skip_tool_is_not_evaluated(self) -> None:
        client = _StubClient({"action": "block"})
        payload = {
            "hook_event_name": "BeforeTool",
            "tool_name": "write_todos",
            "tool_input": {},
            "session_id": "s1",
        }
        out = hook.handle_before_tool(payload, _cfg(), client)
        assert out == {}

    def test_intercept_filter_skips_unlisted(self) -> None:
        client = _StubClient({"action": "block"})
        payload = {
            "hook_event_name": "BeforeTool",
            "tool_name": "read_file",
            "tool_input": {"file_path": "/x"},
            "session_id": "s1",
        }
        cfg = _cfg(intercept=["run_shell_command"])
        out = hook.handle_before_tool(payload, cfg, client)
        assert out == {}

    def test_engine_failure_fails_closed_by_default(self) -> None:
        client = _StubClient(raise_on_eval=True)
        payload = {
            "hook_event_name": "BeforeTool",
            "tool_name": "run_shell_command",
            "tool_input": {"command": "ls"},
            "session_id": "s1",
        }
        out = hook.handle_before_tool(payload, _cfg(timeout_policy="block"), client)
        assert out["decision"] == "deny"

    def test_engine_failure_fail_open_when_configured(self) -> None:
        client = _StubClient(raise_on_eval=True)
        payload = {
            "hook_event_name": "BeforeTool",
            "tool_name": "run_shell_command",
            "tool_input": {"command": "ls"},
            "session_id": "s1",
        }
        out = hook.handle_before_tool(payload, _cfg(timeout_policy="allow"), client)
        assert out == {}

    def test_log_action_allows(self) -> None:
        client = _StubClient({"action": "log", "alerts": [{"severity": "low"}]})
        payload = {
            "hook_event_name": "BeforeTool",
            "tool_name": "run_shell_command",
            "tool_input": {"command": "ls"},
            "session_id": "s1",
        }
        out = hook.handle_before_tool(payload, _cfg(), client)
        assert out == {}


class TestCorrelationRoundTrip:
    def test_before_then_after_links_correlation(self) -> None:
        client = _StubClient({"action": "allow"})
        before = {
            "hook_event_name": "BeforeTool",
            "tool_name": "run_shell_command",
            "tool_input": {"command": "ls"},
            "session_id": "s1",
        }
        hook.handle_before_tool(before, _cfg(), client)

        after = {
            "hook_event_name": "AfterTool",
            "tool_name": "run_shell_command",
            "tool_input": {"command": "ls"},
            "session_id": "s1",
            "tool_response": {"llmContent": "file1\nfile2"},
        }
        hook.handle_after_tool(after, _cfg(), client)
        assert len(client.audits) == 1
        assert client.audits[0]["result_summary"] == "file1\nfile2"
        # Correlation id should be a real (non-empty) value.
        assert client.audits[0]["correlation_id"]

    def test_after_tool_error_response(self) -> None:
        client = _StubClient()
        after = {
            "hook_event_name": "AfterTool",
            "tool_name": "run_shell_command",
            "tool_input": {"command": "false"},
            "session_id": "s1",
            "tool_response": {"error": {"message": "non-zero exit"}},
        }
        hook.handle_after_tool(after, _cfg(), client)
        assert client.audits[0]["is_error"] is True
        assert client.audits[0]["error_message"] == "non-zero exit"


class TestDispatch:
    def test_dispatch_session_start(self) -> None:
        client = _StubClient()
        out = hook.handle_session_event(
            {"session_id": "s1", "source": "startup"}, _cfg(), client, "session_start"
        )
        assert out == {}
        assert client.lifecycles[0]["event_type"] == "session_start"
        assert client.health_called is True

    def test_dispatch_disabled_config(self, monkeypatch: pytest.MonkeyPatch) -> None:
        out = hook.dispatch(
            {"hook_event_name": "BeforeTool", "tool_name": "run_shell_command"},
            config=_cfg(enabled=False),
        )
        assert out == {}

    def test_dispatch_unknown_event(self) -> None:
        out = hook.dispatch(
            {"hook_event_name": "Notification"}, config=_cfg()
        )
        assert out == {}
