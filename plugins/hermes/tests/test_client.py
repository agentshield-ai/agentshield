"""Tests for the AgentShield HTTP client."""

from __future__ import annotations

import json
from http.server import BaseHTTPRequestHandler, HTTPServer
from threading import Thread
from typing import Any, Dict, List

import pytest

from hermes.client import AgentShieldClient
from hermes.config import AgentShieldConfig


class _Handler(BaseHTTPRequestHandler):
    """Minimal HTTP handler that records requests and returns canned responses."""

    responses: List[Dict[str, Any]] = []
    requests: List[Dict[str, Any]] = []
    status_code: int = 200

    def do_POST(self) -> None:
        length = int(self.headers.get("Content-Length", 0))
        body = json.loads(self.rfile.read(length)) if length else {}
        # Build a case-insensitive header dict for test assertions.
        hdrs = {k.lower(): v for k, v in self.headers.items()}
        _Handler.requests.append({"path": self.path, "body": body, "headers": hdrs})

        if _Handler.responses:
            resp = _Handler.responses.pop(0)
        else:
            resp = {"action": "allow", "event_id": "test-id"}

        self.send_response(_Handler.status_code)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(resp).encode())

    def do_GET(self) -> None:
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b'{"status": "ok"}')

    def log_message(self, *args: Any) -> None:
        pass  # Suppress request logs during tests


@pytest.fixture()
def server():
    """Start a local HTTP server for testing."""
    _Handler.responses = []
    _Handler.requests = []
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
        _Handler.responses = [{"action": "allow", "event_id": "e1"}]
        client = _make_client(server)

        result = client.evaluate({"event_id": "e1", "tool_name": "exec"})
        assert result["action"] == "allow"

    def test_returns_block(self, server: int) -> None:
        _Handler.responses = [{"action": "block", "event_id": "e2", "reason": "dangerous"}]
        client = _make_client(server)

        result = client.evaluate({"event_id": "e2", "tool_name": "exec"})
        assert result["action"] == "block"
        assert result["reason"] == "dangerous"

    def test_sends_auth_header(self, server: int) -> None:
        _Handler.responses = [{"action": "allow", "event_id": "e3"}]
        client = _make_client(server, auth_token="secret-token")

        client.evaluate({"event_id": "e3", "tool_name": "exec"})

        assert len(_Handler.requests) == 1
        assert _Handler.requests[0]["headers"].get("authorization") == "Bearer secret-token"

    def test_sends_version_header(self, server: int) -> None:
        _Handler.responses = [{"action": "allow", "event_id": "e4"}]
        client = _make_client(server)

        client.evaluate({"event_id": "e4"})

        assert _Handler.requests[0]["headers"].get("x-agentshield-version") == "1.0.0"

    def test_raises_on_invalid_action(self, server: int) -> None:
        _Handler.responses = [{"action": "explode", "event_id": "e5"}]
        client = _make_client(server)

        with pytest.raises(ValueError, match="invalid action"):
            client.evaluate({"event_id": "e5"})

    def test_raises_on_http_error(self, server: int) -> None:
        _Handler.status_code = 500
        _Handler.responses = [{"error": "internal"}]
        client = _make_client(server)

        with pytest.raises(RuntimeError, match="HTTP 500"):
            client.evaluate({"event_id": "e6"})


class TestHealthCheck:
    def test_returns_true_when_healthy(self, server: int) -> None:
        client = _make_client(server)
        assert client.health_check() is True

    def test_returns_false_on_unreachable(self) -> None:
        config = AgentShieldConfig(
            endpoint="http://127.0.0.1:1/api/v1/evaluate",
            timeout_ms=100,
        )
        client = AgentShieldClient(config)
        assert client.health_check() is False
