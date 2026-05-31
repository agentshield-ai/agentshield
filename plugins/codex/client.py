"""HTTP client for communicating with the AgentShield engine.

Uses only the Python standard library (``urllib.request``) so the connector has
zero external dependencies.

- :meth:`AgentShieldClient.evaluate` is synchronous with a configurable timeout.
- :meth:`AgentShieldClient.send_audit`, :meth:`send_lifecycle`,
  :meth:`submit_feedback` and :meth:`send_override` are "fire-and-forget":
  failures are swallowed at debug level.  Unlike the long-running Hermes/OpenClaw
  connectors, a Codex hook is a short-lived process that exits as soon as it has
  emitted its decision, so a background worker thread would be killed before it
  could flush.  These calls are therefore sent inline with a short timeout and
  any error is suppressed.
"""

from __future__ import annotations

import json
import logging
import urllib.error
import urllib.request
from typing import Any, Dict, Optional

from .config import AgentShieldConfig

logger = logging.getLogger("agentshield")

CONTRACT_VERSION = "1.0.0"

VALID_ACTIONS = {"allow", "block", "log", "require_approval"}

# Short timeout for fire-and-forget calls so a hook never hangs the agent.
_FIRE_AND_FORGET_TIMEOUT_S = 2.0
_HEALTH_TIMEOUT_S = 2.0


class AgentShieldClient:
    """HTTP client for the AgentShield real-time receiver."""

    def __init__(self, config: AgentShieldConfig) -> None:
        """Initialise the client and derive secondary endpoints.

        Args:
            config: The validated connector configuration.
        """
        self._endpoint = config.endpoint
        base_url = config.endpoint.rsplit("/evaluate", 1)[0]
        self._audit_endpoint = f"{base_url}/audit"
        self._lifecycle_endpoint = f"{base_url}/lifecycle"
        self._health_endpoint = f"{base_url}/health"
        self._feedback_endpoint = f"{base_url}/feedback"
        self._override_endpoint = f"{base_url}/override"
        self._timeout_s = config.timeout_ms / 1000.0

        # Headers for the unauthenticated health probe (no bearer token: the
        # /health endpoint requires no auth, so the token is not leaked to it).
        self._unauth_headers: Dict[str, str] = {
            "Content-Type": "application/json; charset=utf-8",
            "X-AgentShield-Version": CONTRACT_VERSION,
        }
        self._headers: Dict[str, str] = dict(self._unauth_headers)
        if config.auth_token:
            self._headers["Authorization"] = f"Bearer {config.auth_token}"

    # -- synchronous evaluation -------------------------------------------

    def evaluate(self, request: Dict[str, Any]) -> Dict[str, Any]:
        """POST an evaluation request and return the parsed response.

        Args:
            request: The ``EvaluationRequest`` envelope.

        Returns:
            The parsed ``EvaluationResponse``.

        Raises:
            ValueError: If the engine returns an action outside the valid set.
            RuntimeError: On HTTP errors.
            urllib.error.URLError: On network errors (so the caller can feed the
                failure to the circuit breaker).
        """
        body = self._post(self._endpoint, request, timeout=self._timeout_s)

        action = body.get("action")
        if not isinstance(action, str) or action not in VALID_ACTIONS:
            raise ValueError(f"AgentShield returned invalid action: {action!r}")

        return body

    # -- fire-and-forget helpers ------------------------------------------

    def send_audit(self, report: Dict[str, Any]) -> None:
        """Send an audit report (fire-and-forget).

        Args:
            report: The ``AuditReport`` payload.
        """
        self._fire_and_forget(self._audit_endpoint, report, "audit")

    def send_lifecycle(self, event: Dict[str, Any]) -> None:
        """Send a lifecycle event (fire-and-forget).

        Args:
            event: The ``LifecycleEvent`` payload.
        """
        self._fire_and_forget(self._lifecycle_endpoint, event, "lifecycle")

    def submit_feedback(
        self,
        event_id: str,
        feedback_type: str,
        comment: Optional[str] = None,
    ) -> None:
        """Submit feedback for a prior evaluation (fire-and-forget).

        Args:
            event_id: The evaluation event id the feedback relates to.
            feedback_type: One of ``true_positive``, ``false_positive`` or
                ``improvement``.
            comment: Optional free-text comment.
        """
        from datetime import datetime, timezone

        payload = {
            "event_id": event_id,
            "feedback_type": feedback_type,
            "comment": comment,
            "timestamp": datetime.now(timezone.utc).isoformat(),
        }
        self._fire_and_forget(self._feedback_endpoint, payload, "feedback")

    def send_override(self, session_id: str, event_id: str) -> None:
        """Report a user override of a block/require_approval (fire-and-forget).

        Args:
            session_id: The Codex session id.
            event_id: The overridden evaluation event id.
        """
        payload = {"session_id": session_id, "event_id": event_id}
        self._fire_and_forget(self._override_endpoint, payload, "override")

    def health_check(self) -> bool:
        """Return ``True`` if the engine is reachable.

        Returns:
            ``True`` when ``GET /api/v1/health`` returns a 2xx status.
        """
        try:
            req = urllib.request.Request(self._health_endpoint, method="GET")
            for key, val in self._unauth_headers.items():
                req.add_unredirected_header(key, val)
            with urllib.request.urlopen(req, timeout=_HEALTH_TIMEOUT_S) as resp:
                return 200 <= resp.status < 300
        except (urllib.error.URLError, OSError, ValueError):
            return False

    # -- internal ---------------------------------------------------------

    def _post(
        self,
        url: str,
        payload: Dict[str, Any],
        timeout: float,
    ) -> Dict[str, Any]:
        """POST JSON and return the parsed response body.

        Args:
            url: The target URL.
            payload: The JSON-serialisable request body.
            timeout: Socket timeout in seconds.

        Returns:
            The parsed JSON response object.

        Raises:
            RuntimeError: On a non-2xx HTTP status.
        """
        data = json.dumps(payload).encode("utf-8")
        req = urllib.request.Request(url, data=data, method="POST")
        # add_unredirected_header preserves exact header case
        # (urllib's add_header lowercases via .capitalize()).
        for key, val in self._headers.items():
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
        """POST inline, suppressing any failure at debug level.

        Args:
            url: The target URL.
            payload: The JSON-serialisable request body.
            label: A human-readable label for debug logging.
        """
        try:
            self._post(url, payload, timeout=_FIRE_AND_FORGET_TIMEOUT_S)
        except (urllib.error.URLError, RuntimeError, OSError, ValueError) as exc:
            logger.debug("AgentShield %s send failed: %s", label, exc)
