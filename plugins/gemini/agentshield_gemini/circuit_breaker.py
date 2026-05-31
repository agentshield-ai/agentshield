"""Three-state circuit breaker for the AgentShield Gemini connector.

State machine::

    closed    --(N consecutive failures)----> open
    open      --(recovery_interval_ms elapsed)--> half-open  (lazily, on read)
    half-open --(success)--> closed
    half-open --(failure)--> open

The Gemini CLI invokes each hook as a short-lived subprocess, so breaker
state cannot live in process memory if it is to survive between tool
calls.  This implementation therefore persists state to a small JSON file
guarded by an OS-level advisory file lock (:mod:`fcntl`).  The public
contract -- ``is_open`` / ``record_success`` / ``record_failure`` /
``get_state`` -- and the transition semantics are identical to the
in-memory reference implementations used by the Hermes and OpenClaw
connectors.

A monotonic clock cannot be shared across processes, so the wall clock
(:func:`time.time`) is used for the recovery interval; this is the same
clock the TypeScript connector uses (``Date.now()``).
"""

from __future__ import annotations

import contextlib
import json
import time
from pathlib import Path
from typing import Literal

CircuitBreakerState = Literal["closed", "open", "half-open"]

try:  # POSIX advisory locking; absent on Windows.
    import fcntl

    _HAVE_FCNTL = True
except ImportError:  # pragma: no cover - exercised only on Windows.
    _HAVE_FCNTL = False


class CircuitBreaker:
    """Cross-process circuit breaker backed by a JSON state file."""

    def __init__(
        self,
        state_path: Path,
        failure_threshold: int = 3,
        recovery_interval_ms: int = 30_000,
    ) -> None:
        """Initialise the breaker.

        Args:
            state_path: Path to the JSON file holding persisted state.
            failure_threshold: Consecutive failures before opening.
            recovery_interval_ms: Milliseconds before a half-open probe.
        """
        self._state_path = state_path
        self._failure_threshold = failure_threshold
        self._recovery_interval_s = recovery_interval_ms / 1000.0
        self._state_path.parent.mkdir(parents=True, exist_ok=True)

    # -- public API -------------------------------------------------------

    def is_open(self) -> bool:
        """Return ``True`` if calls should be skipped (circuit is open)."""
        state, _, last_failure = self._read()
        if state == "open":
            if (time.time() - last_failure) >= self._recovery_interval_s:
                # Promote to half-open and let a single probe through.
                self._write("half-open", 0, last_failure)
                return False
            return True
        return False

    def record_success(self) -> None:
        """Reset failure count and close the circuit."""
        self._write("closed", 0, 0.0)

    def record_failure(self) -> None:
        """Record a failure and open the circuit when warranted."""
        state, failures, _ = self._read()
        failures += 1
        now = time.time()

        if state == "half-open":
            self._write("open", failures, now)
            return

        if failures >= self._failure_threshold:
            self._write("open", failures, now)
        else:
            self._write(state, failures, now)

    def get_state(self) -> CircuitBreakerState:
        """Return the current state, applying the lazy open -> half-open promotion."""
        state, failures, last_failure = self._read()
        if state == "open" and (time.time() - last_failure) >= self._recovery_interval_s:
            self._write("half-open", failures, last_failure)
            return "half-open"
        return state

    # -- persistence ------------------------------------------------------

    def _read(self) -> tuple[CircuitBreakerState, int, float]:
        """Load ``(state, consecutive_failures, last_failure_time)`` from disk."""
        try:
            with self._open_locked("r") as handle:
                data = json.loads(handle.read() or "{}")
            state = data.get("state", "closed")
            if state not in ("closed", "open", "half-open"):
                state = "closed"
            failures = int(data.get("consecutive_failures", 0))
            last_failure = float(data.get("last_failure_time", 0.0))
            return state, failures, last_failure
        except (OSError, ValueError, TypeError):
            return "closed", 0, 0.0

    def _write(
        self,
        state: CircuitBreakerState,
        failures: int,
        last_failure: float,
    ) -> None:
        """Persist ``(state, consecutive_failures, last_failure_time)`` to disk."""
        payload = {
            "state": state,
            "consecutive_failures": failures,
            "last_failure_time": last_failure,
        }
        try:
            with self._open_locked("w") as handle:
                handle.write(json.dumps(payload))
        except OSError:
            # State persistence is best-effort; failing to write must never
            # break the agent loop.
            pass

    def _open_locked(self, mode: str):  # type: ignore[no-untyped-def]
        """Open the state file with an advisory lock when available.

        Read mode uses ``"a+"`` so that a missing state file is created
        atomically rather than raising; the read offset is rewound to the
        start before the caller reads.
        """
        open_mode = "a+" if mode == "r" else "w"
        handle = open(self._state_path, open_mode, encoding="utf-8")  # noqa: SIM115
        if _HAVE_FCNTL:
            lock_type = fcntl.LOCK_SH if mode == "r" else fcntl.LOCK_EX
            with contextlib.suppress(OSError):
                fcntl.flock(handle.fileno(), lock_type)
        if mode == "r":
            handle.seek(0)
        return handle


def _ensure_state_file(path: Path) -> None:  # pragma: no cover - convenience only
    """Create an empty state file if one does not yet exist."""
    if not path.exists():
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("{}", encoding="utf-8")
