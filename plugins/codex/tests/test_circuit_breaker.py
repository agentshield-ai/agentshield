"""Tests for the file-backed circuit breaker."""

from __future__ import annotations

import time
from pathlib import Path

import pytest

from codex.circuit_breaker import CircuitBreaker


@pytest.fixture()
def state_path(tmp_path: Path) -> Path:
    return tmp_path / "breaker.json"


class TestCircuitBreaker:
    def test_starts_closed(self, state_path: Path) -> None:
        cb = CircuitBreaker(state_path=state_path)
        assert cb.get_state() == "closed"
        assert cb.is_open() is False

    def test_opens_after_threshold(self, state_path: Path) -> None:
        cb = CircuitBreaker(failure_threshold=3, state_path=state_path)
        cb.record_failure()
        cb.record_failure()
        assert cb.get_state() == "closed"
        cb.record_failure()
        assert cb.get_state() == "open"
        assert cb.is_open() is True

    def test_success_resets(self, state_path: Path) -> None:
        cb = CircuitBreaker(failure_threshold=2, state_path=state_path)
        cb.record_failure()
        cb.record_failure()
        assert cb.get_state() == "open"
        cb.record_success()
        assert cb.get_state() == "closed"

    def test_open_promotes_to_half_open_after_recovery(self, state_path: Path) -> None:
        cb = CircuitBreaker(
            failure_threshold=1, recovery_interval_ms=20, state_path=state_path
        )
        cb.record_failure()
        assert cb.get_state() == "open"
        time.sleep(0.05)
        # is_open lazily promotes to half-open and lets a probe through.
        assert cb.is_open() is False
        assert cb.get_state() == "half-open"

    def test_half_open_failure_reopens(self, state_path: Path) -> None:
        cb = CircuitBreaker(
            failure_threshold=1, recovery_interval_ms=20, state_path=state_path
        )
        cb.record_failure()
        time.sleep(0.05)
        assert cb.is_open() is False  # now half-open
        cb.record_failure()
        assert cb.get_state() == "open"

    def test_state_persists_across_instances(self, state_path: Path) -> None:
        cb1 = CircuitBreaker(failure_threshold=2, state_path=state_path)
        cb1.record_failure()
        cb1.record_failure()
        # A fresh instance (simulating a new Codex hook process) sees the state.
        cb2 = CircuitBreaker(failure_threshold=2, state_path=state_path)
        assert cb2.get_state() == "open"
