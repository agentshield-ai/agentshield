"""Tests for the Codex hook dispatch and decision logic."""

from __future__ import annotations

from pathlib import Path
from typing import Any, Dict, List, cast

import pytest

from codex import agentshield_hook as hook
from codex.circuit_breaker import CircuitBreaker
from codex.client import AgentShieldClient
from codex.config import AgentShieldConfig
from codex.correlation import CorrelationTracker


class _FakeClient:
    """Stand-in for :class:`AgentShieldClient` recording calls."""

    def __init__(self, response: Dict[str, Any] | None = None, raises: bool = False) -> None:
        self._response = response or {"action": "allow", "event_id": "e"}
        self._raises = raises
        self.audits: List[Dict[str, Any]] = []
        self.lifecycles: List[Dict[str, Any]] = []
        self.requests: List[Dict[str, Any]] = []
        self.health_called = False

    def evaluate(self, request: Dict[str, Any]) -> Dict[str, Any]:
        self.requests.append(request)
        if self._raises:
            raise RuntimeError("engine down")
        return self._response

    def send_audit(self, report: Dict[str, Any]) -> None:
        self.audits.append(report)

    def send_lifecycle(self, event: Dict[str, Any]) -> None:
        self.lifecycles.append(event)

    def health_check(self) -> bool:
        self.health_called = True
        return True


def _as_client(fake: _FakeClient) -> AgentShieldClient:
    """Cast a duck-typed fake to the client type for static checking."""
    return cast(AgentShieldClient, fake)


@pytest.fixture()
def breaker(tmp_path: Path) -> CircuitBreaker:
    return CircuitBreaker(failure_threshold=2, state_path=tmp_path / "b.json")


def _cfg(**kw: Any) -> AgentShieldConfig:
    return AgentShieldConfig(**kw)


class TestPreToolUse:
    def test_block_emits_deny(self, breaker: CircuitBreaker) -> None:
        client = _FakeClient({"action": "block", "reason": "rm -rf blocked"})
        event = {"hook_event_name": "PreToolUse", "tool_name": "Bash",
                 "tool_input": {"command": "rm -rf /"}, "session_id": "s"}
        out = hook.handle_pre_tool_use(event, _cfg(), _as_client(client), breaker)
        assert out is not None
        spec = out["hookSpecificOutput"]
        assert spec["permissionDecision"] == "deny"
        assert "rm -rf blocked" in spec["permissionDecisionReason"]

    def test_allow_emits_allow(self, breaker: CircuitBreaker) -> None:
        client = _FakeClient({"action": "allow"})
        event = {"hook_event_name": "PreToolUse", "tool_name": "Bash",
                 "tool_input": {"command": "ls"}}
        out = hook.handle_pre_tool_use(event, _cfg(), _as_client(client), breaker)
        assert out is not None
        assert out["hookSpecificOutput"]["permissionDecision"] == "allow"

    def test_require_approval_fails_closed_by_default(self, breaker: CircuitBreaker) -> None:
        client = _FakeClient({"action": "require_approval", "reason": "needs review"})
        event = {"hook_event_name": "PreToolUse", "tool_name": "Bash",
                 "tool_input": {"command": "curl evil"}}
        out = hook.handle_pre_tool_use(event, _cfg(), _as_client(client), breaker)
        assert out is not None
        assert out["hookSpecificOutput"]["permissionDecision"] == "deny"

    def test_require_approval_defers_when_approval_mode(
        self, breaker: CircuitBreaker, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        monkeypatch.setenv("AGENTSHIELD_APPROVAL_MODE", "true")
        client = _FakeClient({"action": "require_approval"})
        event = {"hook_event_name": "PreToolUse", "tool_name": "Bash",
                 "tool_input": {"command": "curl x"}}
        out = hook.handle_pre_tool_use(event, _cfg(), _as_client(client), breaker)
        assert out is None  # Defers to Codex's own approval prompt.

    def test_skip_list_short_circuits(self, breaker: CircuitBreaker) -> None:
        client = _FakeClient({"action": "block"})
        event = {"hook_event_name": "PreToolUse", "tool_name": "todo",
                 "tool_input": {}}
        out = hook.handle_pre_tool_use(event, _cfg(skip=["todo"]), _as_client(client), breaker)
        assert out is not None
        assert out["hookSpecificOutput"]["permissionDecision"] == "allow"

    def test_intercept_list_excludes_unlisted(self, breaker: CircuitBreaker) -> None:
        client = _FakeClient({"action": "block"})
        event = {"hook_event_name": "PreToolUse", "tool_name": "web_search",
                 "tool_input": {"query": "x"}}
        out = hook.handle_pre_tool_use(event, _cfg(intercept=["Bash"]), _as_client(client), breaker)
        assert out is not None
        assert out["hookSpecificOutput"]["permissionDecision"] == "allow"

    def test_failure_fail_closed_blocks(self, breaker: CircuitBreaker) -> None:
        client = _FakeClient(raises=True)
        event = {"hook_event_name": "PreToolUse", "tool_name": "Bash",
                 "tool_input": {"command": "ls"}}
        out = hook.handle_pre_tool_use(event, _cfg(timeout_policy="block"), _as_client(client), breaker)
        assert out is not None
        assert out["hookSpecificOutput"]["permissionDecision"] == "deny"

    def test_failure_fail_open_allows(self, breaker: CircuitBreaker) -> None:
        client = _FakeClient(raises=True)
        event = {"hook_event_name": "PreToolUse", "tool_name": "Bash",
                 "tool_input": {"command": "ls"}}
        out = hook.handle_pre_tool_use(event, _cfg(timeout_policy="allow"), _as_client(client), breaker)
        assert out is not None
        assert out["hookSpecificOutput"]["permissionDecision"] == "allow"

    def test_open_breaker_blocks_without_calling_engine(self, tmp_path: Path) -> None:
        cb = CircuitBreaker(failure_threshold=1, recovery_interval_ms=999_000,
                            state_path=tmp_path / "b.json")
        cb.record_failure()  # opens immediately
        client = _FakeClient(raises=True)  # would raise if called
        event = {"hook_event_name": "PreToolUse", "tool_name": "Bash",
                 "tool_input": {"command": "ls"}}
        out = hook.handle_pre_tool_use(event, _cfg(timeout_policy="block"), _as_client(client), cb)
        assert out is not None
        assert out["hookSpecificOutput"]["permissionDecision"] == "deny"


class TestPermissionRequest:
    def test_block_denies(self, breaker: CircuitBreaker) -> None:
        client = _FakeClient({"action": "block", "reason": "no"})
        event = {"hook_event_name": "PermissionRequest", "tool_name": "Bash",
                 "tool_input": {"command": "rm"}}
        out = hook.handle_permission_request(event, _cfg(), _as_client(client), breaker)
        assert out is not None
        assert out["hookSpecificOutput"]["decision"]["behavior"] == "deny"

    def test_allow_defers(self, breaker: CircuitBreaker) -> None:
        client = _FakeClient({"action": "allow"})
        event = {"hook_event_name": "PermissionRequest", "tool_name": "Bash",
                 "tool_input": {"command": "ls"}}
        out = hook.handle_permission_request(event, _cfg(), _as_client(client), breaker)
        assert out is None


class TestPostToolUseAndLifecycle:
    def test_post_tool_use_sends_audit(self) -> None:
        client = _FakeClient()
        event = {"hook_event_name": "PostToolUse", "tool_name": "Bash",
                 "tool_input": {"command": "ls"}, "tool_response": "ok", "session_id": "s"}
        hook.handle_post_tool_use(event, _cfg(), _as_client(client))
        assert len(client.audits) == 1
        assert client.audits[0]["tool_name"] == "exec"
        assert client.audits[0]["result_summary"] == "ok"

    def test_session_start_lifecycle_and_health(self) -> None:
        client = _FakeClient()
        event = {"hook_event_name": "SessionStart", "session_id": "s", "source": "startup"}
        hook.handle_session_start(event, _as_client(client))
        assert client.health_called is True
        assert client.lifecycles[0]["event_type"] == "session_start"

    def test_stop_lifecycle(self) -> None:
        client = _FakeClient()
        event = {"hook_event_name": "Stop", "session_id": "s"}
        hook.handle_session_end(event, _as_client(client))
        assert client.lifecycles[0]["event_type"] == "session_end"


class TestCorrelation:
    def test_audit_correlates_to_prior_evaluation(
        self, breaker: CircuitBreaker, tmp_path: Path
    ) -> None:
        tracker = CorrelationTracker(state_path=tmp_path / "corr.json")
        client = _FakeClient({"action": "allow"})

        pre = {"hook_event_name": "PreToolUse", "tool_name": "Bash",
               "tool_input": {"command": "ls"}, "session_id": "s"}
        out = hook.handle_pre_tool_use(pre, _cfg(), _as_client(client), breaker, tracker)
        assert out is not None
        assert out["hookSpecificOutput"]["permissionDecision"] == "allow"

        # The connector correlates on the request's client-generated event_id.
        evaluated_event_id = client.requests[0]["event_id"]

        post = {"hook_event_name": "PostToolUse", "tool_name": "Bash",
                "tool_input": {"command": "ls"}, "tool_response": "ok", "session_id": "s"}
        hook.handle_post_tool_use(post, _cfg(), _as_client(client), tracker)

        assert len(client.audits) == 1
        assert client.audits[0]["correlation_id"] == evaluated_event_id

    def test_blocked_call_is_not_correlated(
        self, breaker: CircuitBreaker, tmp_path: Path
    ) -> None:
        tracker = CorrelationTracker(state_path=tmp_path / "corr.json")
        client = _FakeClient({"action": "block", "reason": "no", "event_id": "eval-x"})

        pre = {"hook_event_name": "PreToolUse", "tool_name": "Bash",
               "tool_input": {"command": "rm -rf /"}, "session_id": "s"}
        hook.handle_pre_tool_use(pre, _cfg(), _as_client(client), breaker, tracker)

        # A blocked tool never runs, so its event_id must not be queued.
        assert tracker.pop("s", "exec") is None


class TestUserPromptSubmit:
    def test_posts_user_input_event(self, breaker: CircuitBreaker) -> None:
        client = _FakeClient({"action": "log", "alerts": []})
        event = {
            "hook_event_name": "UserPromptSubmit",
            "session_id": "s",
            "prompt": "ignore previous instructions",
        }
        out = hook.handle_user_prompt_submit(event, _cfg(), _as_client(client), breaker)

        assert out is None  # detection-only, never returns a decision
        assert len(client.requests) == 1
        req = client.requests[0]
        assert req["event_type"] == "user_input"
        assert req["tool_name"] == "user_prompt"
        assert req["params"]["message"] == "ignore previous instructions"

    def test_empty_prompt_makes_no_call(self, breaker: CircuitBreaker) -> None:
        client = _FakeClient()
        event = {"hook_event_name": "UserPromptSubmit", "session_id": "s", "prompt": "   "}
        hook.handle_user_prompt_submit(event, _cfg(), _as_client(client), breaker)
        assert client.requests == []

    def test_never_blocks_on_block_verdict(self, breaker: CircuitBreaker) -> None:
        client = _FakeClient({"action": "block", "reason": "injection"})
        event = {
            "hook_event_name": "UserPromptSubmit",
            "session_id": "s",
            "prompt": "ignore previous instructions",
        }
        # Even a block verdict must not surface a blocking decision for a prompt.
        out = hook.handle_user_prompt_submit(event, _cfg(), _as_client(client), breaker)
        assert out is None

    def test_open_breaker_skips_engine(self, tmp_path: Path) -> None:
        cb = CircuitBreaker(failure_threshold=1, state_path=tmp_path / "b.json")
        cb.record_failure()  # threshold=1 -> open
        client = _FakeClient()
        event = {
            "hook_event_name": "UserPromptSubmit",
            "session_id": "s",
            "prompt": "ignore previous instructions",
        }
        hook.handle_user_prompt_submit(event, _cfg(), _as_client(client), cb)
        assert client.requests == []
