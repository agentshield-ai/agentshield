"""HTTP client for communicating with the AgentShield engine.

Uses only the Python standard library (``urllib.request``) so the
plugin has zero external dependencies beyond Hermes itself.

- ``evaluate()`` is synchronous with a configurable timeout.
- ``send_audit()`` and ``send_lifecycle()`` are fire-and-forget.
"""

from __future__ import annotations

import json
import logging
import threading
import urllib.error
import urllib.request
from typing import Any, Dict, Optional

from .config import AgentShieldConfig

logger = logging.getLogger("agentshield")

CONTRACT_VERSION = "1.0.0"

VALID_ACTIONS = {"allow", "block", "log", "require_approval"}


class AgentShieldClient:
    """HTTP client for the AgentShield real-time receiver."""

    def __init__(self, config: AgentShieldConfig) -> None:
        self._endpoint = config.endpoint
        base_url = config.endpoint.rsplit("/evaluate", 1)[0]
        self._audit_endpoint = f"{base_url}/audit"
        self._lifecycle_endpoint = f"{base_url}/lifecycle"
        self._health_endpoint = f"{base_url}/health"
        self._feedback_endpoint = f"{base_url}/feedback"
        self._override_endpoint = f"{base_url}/override"
        self._timeout_s = config.timeout_ms / 1000.0
        self._auth_token = config.auth_token

    # -- synchronous evaluation -------------------------------------------

    def evaluate(self, request: Dict[str, Any]) -> Dict[str, Any]:
        """POST an evaluation request and return the parsed response.

        Raises on network errors or invalid responses so the caller
        can feed the result to the circuit breaker.
        """
        body = self._post(self._endpoint, request, timeout=self._timeout_s)

        action = body.get("action")
        if not isinstance(action, str) or action not in VALID_ACTIONS:
            raise ValueError(f"AgentShield returned invalid action: {action!r}")

        return body

    # -- fire-and-forget helpers ------------------------------------------

    def send_audit(self, report: Dict[str, Any]) -> None:
        self._fire_and_forget(self._audit_endpoint, report, "audit")

    def send_lifecycle(self, event: Dict[str, Any]) -> None:
        self._fire_and_forget(self._lifecycle_endpoint, event, "lifecycle")

    def submit_feedback(
        self,
        event_id: str,
        feedback_type: str,
        comment: Optional[str] = None,
    ) -> None:
        from datetime import datetime, timezone

        payload = {
            "event_id": event_id,
            "feedback_type": feedback_type,
            "comment": comment,
            "timestamp": datetime.now(timezone.utc).isoformat(),
        }
        self._fire_and_forget(self._feedback_endpoint, payload, "feedback")

    def send_override(self, session_id: str, event_id: str) -> None:
        payload = {"session_id": session_id, "event_id": event_id}
        self._fire_and_forget(self._override_endpoint, payload, "override")

    def health_check(self) -> bool:
        """Return *True* if the engine is reachable."""
        try:
            req = urllib.request.Request(self._health_endpoint, method="GET")
            for key, val in self._build_headers().items():
                req.add_unredirected_header(key, val)
            with urllib.request.urlopen(req, timeout=2) as resp:
                return 200 <= resp.status < 300
        except Exception:
            return False

    # -- internal ---------------------------------------------------------

    def _post(
        self,
        url: str,
        payload: Dict[str, Any],
        timeout: float,
    ) -> Dict[str, Any]:
        data = json.dumps(payload).encode("utf-8")
        req = urllib.request.Request(url, data=data, method="POST")
        # Use add_unredirected_header to preserve exact header case
        # (urllib's default add_header lowercases via .capitalize()).
        for key, val in self._build_headers().items():
            req.add_unredirected_header(key, val)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                if resp.status < 200 or resp.status >= 300:
                    raise urllib.error.HTTPError(
                        url, resp.status, f"HTTP {resp.status}", resp.headers, None
                    )
                return json.loads(resp.read().decode("utf-8"))
        except urllib.error.HTTPError as exc:
            raise RuntimeError(
                f"AgentShield returned HTTP {exc.code}: {exc.reason}"
            ) from exc

    def _fire_and_forget(
        self,
        url: str,
        payload: Dict[str, Any],
        label: str,
    ) -> None:
        """POST in a background thread, swallowing errors."""

        def _send() -> None:
            try:
                self._post(url, payload, timeout=5.0)
            except Exception as exc:
                logger.debug("AgentShield %s send failed: %s", label, exc)

        thread = threading.Thread(target=_send, daemon=True)
        thread.start()

    def _build_headers(self) -> Dict[str, str]:
        headers: Dict[str, str] = {
            "Content-Type": "application/json; charset=utf-8",
            "X-AgentShield-Version": CONTRACT_VERSION,
        }
        if self._auth_token:
            headers["Authorization"] = f"Bearer {self._auth_token}"
        return headers
