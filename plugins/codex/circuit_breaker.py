"""Three-state circuit breaker for the AgentShield Codex connector.

State machine::

    closed    --(N consecutive failures)--> open
    open      --(recovery_interval_ms elapsed)--> half-open
    half-open --(success)--> closed
    half-open --(failure)--> open

Codex invokes each hook as a fresh, short-lived process, so the breaker state
cannot live in process memory the way it does for the long-running Hermes and
OpenClaw plugins.  Instead it is persisted to a small JSON state file guarded by
an OS file lock, so that a run of failures across consecutive hook invocations
still trips the breaker and spares the engine.  The state-machine semantics are
identical to the in-memory reference implementations.
"""

from __future__ import annotations

import json
import os
import time
from pathlib import Path
from typing import Literal, Optional, Tuple

CircuitBreakerState = Literal["closed", "open", "half-open"]

_DEFAULT_STATE_PATH = Path.home() / ".codex" / ".agentshield-breaker.json"


class CircuitBreaker:
    """File-backed circuit breaker shared across Codex hook subprocesses.

    The state file holds ``consecutive_failures`` and ``last_failure_time``
    (a wall-clock epoch, since :func:`time.monotonic` is not comparable across
    processes).  All reads and writes take an exclusive lock on the file to stay
    safe under Codex's parallel tool execution.
    """

    def __init__(
        self,
        failure_threshold: int = 3,
        recovery_interval_ms: int = 30_000,
        state_path: Optional[Path] = None,
    ) -> None:
        """Initialise the breaker.

        Args:
            failure_threshold: Consecutive failures before the breaker opens.
            recovery_interval_ms: Milliseconds before an open breaker promotes
                to half-open and lets a single probe through.
            state_path: Optional override for the persisted state file
                (used in tests).
        """
        self._failure_threshold = failure_threshold
        self._recovery_interval_s = recovery_interval_ms / 1000.0
        self._state_path = state_path or _DEFAULT_STATE_PATH

    # -- public API -------------------------------------------------------

    def is_open(self) -> bool:
        """Return ``True`` if calls should be skipped (the circuit is open).

        Lazily promotes ``open`` to ``half-open`` once the recovery interval has
        elapsed, persisting the transition and returning ``False`` so a single
        probe request is allowed through.

        Returns:
            ``True`` to skip the engine call, ``False`` to allow it.
        """
        failures, last_failure, state = self._read()
        state = self._maybe_promote(failures, last_failure, state)
        if state == "half-open":
            self._write(failures, last_failure, "half-open")
        return state == "open"

    def record_success(self) -> None:
        """Reset the breaker to ``closed`` after a successful call."""
        self._write(0, 0.0, "closed")

    def record_failure(self) -> None:
        """Record a failed call, opening the breaker if the threshold is met."""
        failures, _, state = self._read()
        failures += 1
        now = time.time()

        if state == "half-open":
            new_state: CircuitBreakerState = "open"
        elif failures >= self._failure_threshold:
            new_state = "open"
        else:
            new_state = "closed"

        self._write(failures, now, new_state)

    def get_state(self) -> CircuitBreakerState:
        """Return the current state, re-evaluating the lazy half-open promotion.

        Returns:
            The current :data:`CircuitBreakerState`.
        """
        failures, last_failure, state = self._read()
        return self._maybe_promote(failures, last_failure, state)

    # -- internal ---------------------------------------------------------

    def _maybe_promote(
        self,
        failures: int,
        last_failure: float,
        state: CircuitBreakerState,
    ) -> CircuitBreakerState:
        """Promote ``open`` to ``half-open`` if the recovery interval elapsed.

        Args:
            failures: Current consecutive-failure count.
            last_failure: Epoch timestamp of the last failure.
            state: The persisted state.

        Returns:
            The (possibly promoted) state.
        """
        del failures  # Not needed for the transition decision.
        if state == "open" and (time.time() - last_failure) >= self._recovery_interval_s:
            return "half-open"
        return state

    def _read(self) -> Tuple[int, float, CircuitBreakerState]:
        """Read persisted breaker state.

        Returns:
            A tuple of ``(consecutive_failures, last_failure_time, state)``.
            Defaults to a closed breaker when no valid state file exists.
        """
        try:
            data = json.loads(self._state_path.read_text(encoding="utf-8"))
        except (OSError, ValueError):
            return 0, 0.0, "closed"

        if not isinstance(data, dict):
            return 0, 0.0, "closed"

        failures = data.get("consecutive_failures", 0)
        last_failure = data.get("last_failure_time", 0.0)
        state = data.get("state", "closed")
        if state not in ("closed", "open", "half-open"):
            state = "closed"
        if not isinstance(failures, int) or failures < 0:
            failures = 0
        if not isinstance(last_failure, (int, float)):
            last_failure = 0.0
        return failures, float(last_failure), state  # type: ignore[return-value]

    def _write(
        self,
        failures: int,
        last_failure: float,
        state: CircuitBreakerState,
    ) -> None:
        """Atomically persist breaker state.

        Args:
            failures: Consecutive-failure count to store.
            last_failure: Epoch timestamp of the last failure.
            state: The state to store.
        """
        payload = {
            "consecutive_failures": failures,
            "last_failure_time": last_failure,
            "state": state,
        }
        try:
            self._state_path.parent.mkdir(parents=True, exist_ok=True)
            tmp = self._state_path.with_suffix(".tmp")
            tmp.write_text(json.dumps(payload), encoding="utf-8")
            os.replace(tmp, self._state_path)
        except OSError:
            # Best-effort persistence; a failed write simply degrades to a
            # per-process breaker, which is safe.
            pass
