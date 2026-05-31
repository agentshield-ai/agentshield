"""Tests for the AgentShield HTTP client."""

from __future__ import annotations

import json
from http.server import BaseHTTPRequestHandler, HTTPServer
from threading import Thread
from typing import Any, Dict, List

import pytest

from codex.client import AgentShieldClient
from codex.config import AgentShieldConfig


class _Handler(BaseHTTPRequestHandler):
    """Minimal HTTP handler that records requests and returns canned responses."""

    canned: List[Dict[str, Any]] = []
    captured: List[Dict[str, Any]] = []
    status_code: int = 200

    def do_POST(self) -> None:  # noqa: N802 - http.server API
        length = int(self.headers.get("Content-Length", 0))
        body = json.loads(self.rfile.read(length)) if length else {}
        hdrs = {k.lower(): v for k, v in self.headers.items()}
        _Handler.captured.append({"path": self.path, "body": body, "headers": hdrs})

        resp = _Handler.canned.pop(0) if _Handler.canned else {
            "action": "allow",
            "event_id": "test-id",
        }
        self.send_response(_Handler.status_code)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(resp).encode())

    def do_GET(self) -> None:  # noqa: N802 - http.server API
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b'{"status": "ok"}')

    def log_message(self, *args: Any) -> None:  # noqa: A002
        pass


@pytest.fixture()
def server():
    _Handler.canned = []
    _Handler.captured = []
    _Handler.status_code = 200

    srv = HTTPServer(("127.0.0.1", 0), _Handler)
    port = srv.server_address[1]
    thread = Thread(target=srv.serve_forever, daemon=True)
    thread.start()
    yield port
    srv.shutdown()


def _make_client(port: int, auth_token: str = "") -> AgentShieldClient:
    config = AgentShieldConfig(
        endpoint=f"http://127.0.0.1:{port}/api/v1/evaluate",
        auth_token=auth_token,
        timeout_ms=2000,
    )
    return AgentShieldClient(config)


class TestEvaluate:
    def test_returns_allow(self, server: int) -> None:
        _Handler.canned = [{"action": "allow", "event_id": "e1"}]
        client = _make_client(server)
        result = client.evaluate({"event_id": "e1", "tool_name": "exec"})
        assert result["action"] == "allow"

    def test_returns_block(self, server: int) -> None:
        _Handler.canned = [{"action": "block", "event_id": "e2", "reason": "danger"}]
        client = _make_client(server)
        result = client.evaluate({"event_id": "e2"})
        assert result["action"] == "block"
        assert result["reason"] == "danger"

    def test_sends_auth_header(self, server: int) -> None:
        _Handler.canned = [{"action": "allow", "event_id": "e3"}]
        client = _make_client(server, auth_token="secret-token")
        client.evaluate({"event_id": "e3"})
        assert _Handler.captured[0]["headers"].get("authorization") == "Bearer secret-token"

    def test_sends_version_header(self, server: int) -> None:
        _Handler.canned = [{"action": "allow", "event_id": "e4"}]
        client = _make_client(server)
        client.evaluate({"event_id": "e4"})
        assert _Handler.captured[0]["headers"].get("x-agentshield-version") == "1.0.0"

    def test_raises_on_invalid_action(self, server: int) -> None:
        _Handler.canned = [{"action": "explode", "event_id": "e5"}]
        client = _make_client(server)
        with pytest.raises(ValueError, match="invalid action"):
            client.evaluate({"event_id": "e5"})

    def test_raises_on_http_error(self, server: int) -> None:
        _Handler.status_code = 500
        _Handler.canned = [{"error": "internal"}]
        client = _make_client(server)
        with pytest.raises(RuntimeError, match="HTTP 500"):
            client.evaluate({"event_id": "e6"})


class TestFireAndForget:
    def test_audit_posts_to_audit_endpoint(self, server: int) -> None:
        client = _make_client(server)
        client.send_audit({"event_id": "a1", "event_type": "tool_result"})
        assert _Handler.captured[0]["path"].endswith("/audit")

    def test_lifecycle_posts_to_lifecycle_endpoint(self, server: int) -> None:
        client = _make_client(server)
        client.send_lifecycle({"event_id": "l1", "event_type": "session_start"})
        assert _Handler.captured[0]["path"].endswith("/lifecycle")

    def test_audit_swallows_errors(self) -> None:
        config = AgentShieldConfig(
            endpoint="http://127.0.0.1:1/api/v1/evaluate", timeout_ms=100
        )
        client = AgentShieldClient(config)
        # Must not raise even though the endpoint is unreachable.
        client.send_audit({"event_id": "x"})


class TestHealthCheck:
    def test_returns_true_when_healthy(self, server: int) -> None:
        assert _make_client(server).health_check() is True

    def test_returns_false_on_unreachable(self) -> None:
        config = AgentShieldConfig(
            endpoint="http://127.0.0.1:1/api/v1/evaluate", timeout_ms=100
        )
        assert AgentShieldClient(config).health_check() is False
