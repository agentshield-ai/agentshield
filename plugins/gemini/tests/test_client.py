"""Tests for the AgentShield HTTP client."""

from __future__ import annotations

import json
from typing import Any, Dict
from unittest.mock import MagicMock, patch

import pytest

from agentshield_gemini.client import AgentShieldClient
from agentshield_gemini.config import parse_config


def _client(**kw: Any) -> AgentShieldClient:
    return AgentShieldClient(parse_config(kw))


class _FakeResponse:
    def __init__(self, status: int, body: Dict[str, Any]) -> None:
        self.status = status
        self._body = json.dumps(body).encode("utf-8")
        self.headers: Dict[str, str] = {}

    def read(self) -> bytes:
        return self._body

    def __enter__(self) -> "_FakeResponse":
        return self

    def __exit__(self, *_exc: object) -> None:
        return None


class TestEndpointDerivation:
    def test_sibling_endpoints_derived(self) -> None:
        c = _client(endpoint="http://host:8433/api/v1/evaluate")
        assert c._audit_endpoint.endswith("/api/v1/audit")
        assert c._lifecycle_endpoint.endswith("/api/v1/lifecycle")
        assert c._health_endpoint.endswith("/api/v1/health")
        assert c._override_endpoint.endswith("/api/v1/override")
        assert c._feedback_endpoint.endswith("/api/v1/feedback")

    def test_auth_header_set_when_token_present(self) -> None:
        c = _client(auth_token="x" * 40)
        assert c._headers["Authorization"] == "Bearer " + "x" * 40

    def test_no_auth_header_when_empty(self) -> None:
        with patch.dict("os.environ", {}, clear=True):
            c = _client(auth_token="")
        assert "Authorization" not in c._headers

    def test_contract_version_header(self) -> None:
        c = _client()
        assert c._headers["X-AgentShield-Version"] == "1.0.0"


class TestEvaluate:
    def test_valid_action_returned(self) -> None:
        c = _client()
        with patch("urllib.request.urlopen", return_value=_FakeResponse(200, {"action": "block"})):
            resp = c.evaluate({"event_id": "e1"})
        assert resp["action"] == "block"

    def test_invalid_action_raises(self) -> None:
        c = _client()
        resp = _FakeResponse(200, {"action": "nonsense"})
        with patch("urllib.request.urlopen", return_value=resp), pytest.raises(ValueError):
            c.evaluate({"event_id": "e1"})

    def test_missing_action_raises(self) -> None:
        c = _client()
        resp = _FakeResponse(200, {})
        with patch("urllib.request.urlopen", return_value=resp), pytest.raises(ValueError):
            c.evaluate({"event_id": "e1"})


class TestHealthCheck:
    def test_health_ok(self) -> None:
        c = _client()
        with patch("urllib.request.urlopen", return_value=_FakeResponse(200, {"status": "ok"})):
            assert c.health_check() is True

    def test_health_unreachable(self) -> None:
        c = _client()
        with patch("urllib.request.urlopen", side_effect=OSError("refused")):
            assert c.health_check() is False


class TestFireAndForget:
    def test_audit_swallows_errors(self) -> None:
        c = _client()
        with patch("urllib.request.urlopen", side_effect=OSError("refused")):
            # Must not raise.
            c.send_audit({"event_id": "e1"})

    def test_audit_posts_when_reachable(self) -> None:
        c = _client()
        mock = MagicMock(return_value=_FakeResponse(200, {"ok": True}))
        with patch("urllib.request.urlopen", mock):
            c.send_audit({"event_id": "e1"})
        assert mock.called
