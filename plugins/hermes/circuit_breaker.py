"""Three-state circuit breaker for the AgentShield HTTP client.

State machine::

    closed  --(N consecutive failures)--> open
    open    --(recovery_interval_ms elapsed)--> half-open
    half-open --(success)--> closed
    half-open --(failure)--> open
"""

from __future__ import annotations

import time
from typing import Literal

CircuitBreakerState = Literal["closed", "open", "half-open"]


class CircuitBreaker:
    """Protects the agent loop from a slow or unavailable engine."""

    def __init__(
        self,
        failure_threshold: int = 3,
        recovery_interval_ms: int = 30_000,
    ) -> None:
        self._state: CircuitBreakerState = "closed"
        self._consecutive_failures: int = 0
        self._last_failure_time: float = 0.0
        self._failure_threshold = failure_threshold
        self._recovery_interval_s = recovery_interval_ms / 1000.0

    # -- public API --------------------------------------------------------

    def is_open(self) -> bool:
        """Return *True* if calls should be skipped (circuit is open)."""
        if self._state == "closed":
            return False

        if self._state == "open":
            elapsed = time.monotonic() - self._last_failure_time
            if elapsed >= self._recovery_interval_s:
                self._state = "half-open"
                return False  # allow a probe request
            return True

        # half-open: allow the probe request through
        return False

    def record_success(self) -> None:
        self._consecutive_failures = 0
        self._state = "closed"

    def record_failure(self) -> None:
        self._consecutive_failures += 1
        self._last_failure_time = time.monotonic()

        if self._state == "half-open":
            self._state = "open"
            return

        if self._consecutive_failures >= self._failure_threshold:
            self._state = "open"

    def get_state(self) -> CircuitBreakerState:
        # Re-evaluate open -> half-open transition on read.
        if self._state == "open":
            elapsed = time.monotonic() - self._last_failure_time
            if elapsed >= self._recovery_interval_s:
                self._state = "half-open"
        return self._state
