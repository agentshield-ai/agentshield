"""HTTP client for communicating with the AgentShield engine.

Uses only the Python standard library (``urllib.request``) so the
connector has zero external dependencies.

Unlike the long-lived Hermes plugin, each Gemini CLI hook runs in a
short-lived subprocess.  A background worker thread would be terminated
when the process exits before it had a chance to flush, so the
"fire-and-forget" calls here are issued *synchronously* with a short
timeout and their failures are swallowed at debug level.  ``evaluate()``
remains the only call whose result the caller acts upon.
"""

from __future__ import annotations

import json
import logging
import urllib.error
import urllib.request
from datetime import datetime, timezone
from typing import Any, Dict, Optional

from . import CONTRACT_VERSION
from .config import AgentShieldConfig

logger = logging.getLogger("agentshield")

VALID_ACTIONS = {"allow", "block", "log", "require_approval"}

# Bound the time a fire-and-forget POST may add to a hook subprocess.
_FIRE_AND_FORGET_TIMEOUT_S = 2.0
_HEALTH_TIMEOUT_S = 2.0


class AgentShieldClient:
    """HTTP client for the AgentShield real-time receiver."""

    def __init__(self, config: AgentShieldConfig) -> None:
        """Initialise the client and derive sibling endpoints.

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

        self._headers: Dict[str, str] = {
            "Content-Type": "application/json; charset=utf-8",
            "X-AgentShield-Version": CONTRACT_VERSION,
        }
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
            RuntimeError: On a non-2xx response.
            ValueError: When ``action`` is missing or not one of the four
                valid values, so the caller can record a breaker failure.
        """
        body = self._post(self._endpoint, request, timeout=self._timeout_s)

        action = body.get("action")
        if not isinstance(action, str) or action not in VALID_ACTIONS:
            raise ValueError(f"AgentShield returned invalid action: {action!r}")

        return body

    # -- fire-and-forget helpers ------------------------------------------

    def send_audit(self, report: Dict[str, Any]) -> None:
        """Send a post-tool audit report (best-effort)."""
        self._fire_and_forget(self._audit_endpoint, report, "audit")

    def send_lifecycle(self, event: Dict[str, Any]) -> None:
        """Send a session lifecycle event (best-effort)."""
        self._fire_and_forget(self._lifecycle_endpoint, event, "lifecycle")

    def submit_feedback(
        self,
        event_id: str,
        feedback_type: str,
        comment: Optional[str] = None,
    ) -> None:
        """Submit feedback on a prior verdict (best-effort).

        Args:
            event_id: The originating evaluate event id.
            feedback_type: ``true_positive`` | ``false_positive`` | ``improvement``.
            comment: Optional free-text comment.
        """
        payload = {
            "event_id": event_id,
            "feedback_type": feedback_type,
            "comment": comment,
            "timestamp": datetime.now(timezone.utc).isoformat(),
        }
        self._fire_and_forget(self._feedback_endpoint, payload, "feedback")

    def send_override(self, session_id: str, event_id: str) -> None:
        """Report that a user overrode a block / require_approval verdict."""
        payload = {"session_id": session_id, "event_id": event_id}
        self._fire_and_forget(self._override_endpoint, payload, "override")

    def health_check(self) -> bool:
        """Return ``True`` if the engine health endpoint is reachable.

        Returns:
            ``True`` for any 2xx response, ``False`` otherwise (never raises).
        """
        try:
            req = urllib.request.Request(self._health_endpoint, method="GET")
            for key, val in self._headers.items():
                req.add_unredirected_header(key, val)
            with urllib.request.urlopen(req, timeout=_HEALTH_TIMEOUT_S) as resp:
                return bool(200 <= resp.status < 300)
        except Exception:  # noqa: BLE001 - reachability probe must never raise.
            return False

    # -- internal ---------------------------------------------------------

    def _post(
        self,
        url: str,
        payload: Dict[str, Any],
        timeout: float,
    ) -> Dict[str, Any]:
        """POST *payload* as JSON and return the parsed response body.

        Args:
            url: The target URL.
            payload: The JSON-serialisable request body.
            timeout: The socket timeout in seconds.

        Returns:
            The parsed JSON response.

        Raises:
            RuntimeError: On an HTTP error status.
        """
        data = json.dumps(payload).encode("utf-8")
        req = urllib.request.Request(url, data=data, method="POST")
        # add_unredirected_header preserves exact header case
        # (urllib's add_header capitalises header names).
        for key, val in self._headers.items():
            req.add_unredirected_header(key, val)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                if resp.status < 200 or resp.status >= 300:
                    raise urllib.error.HTTPError(
                        url, resp.status, f"HTTP {resp.status}", resp.headers, None
                    )
                parsed: Dict[str, Any] = json.loads(resp.read().decode("utf-8"))
                return parsed
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
        """Issue a best-effort POST, swallowing all errors at debug level.

        Because the hook process is short-lived a background thread cannot
        be relied upon to flush, so the request is made synchronously with
        a short timeout.

        Args:
            url: The target URL.
            payload: The JSON-serialisable request body.
            label: A short label used in debug logging.
        """
        try:
            self._post(url, payload, timeout=_FIRE_AND_FORGET_TIMEOUT_S)
        except Exception as exc:  # noqa: BLE001 - fire-and-forget must not raise.
            logger.debug("AgentShield %s send failed: %s", label, exc)
