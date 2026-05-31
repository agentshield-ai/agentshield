"""Tests for the disk-backed circuit breaker."""

from __future__ import annotations

import time
from pathlib import Path

from agentshield_gemini.circuit_breaker import CircuitBreaker


def _breaker(tmp_path: Path, **kw: int) -> CircuitBreaker:
    return CircuitBreaker(state_path=tmp_path / "breaker.json", **kw)


class TestCircuitBreaker:
    def test_starts_closed(self, tmp_path: Path) -> None:
        cb = _breaker(tmp_path)
        assert cb.get_state() == "closed"
        assert cb.is_open() is False

    def test_opens_after_threshold(self, tmp_path: Path) -> None:
        cb = _breaker(tmp_path, failure_threshold=3)
        cb.record_failure()
        cb.record_failure()
        assert cb.is_open() is False
        cb.record_failure()
        assert cb.is_open() is True
        assert cb.get_state() == "open"

    def test_success_resets(self, tmp_path: Path) -> None:
        cb = _breaker(tmp_path, failure_threshold=2)
        cb.record_failure()
        cb.record_failure()
        assert cb.is_open() is True
        cb.record_success()
        assert cb.is_open() is False
        assert cb.get_state() == "closed"

    def test_open_promotes_to_half_open_after_recovery(self, tmp_path: Path) -> None:
        cb = _breaker(tmp_path, failure_threshold=1, recovery_interval_ms=1000)
        cb.record_failure()
        assert cb.is_open() is True
        time.sleep(1.1)
        # Lazy promotion lets a single probe through.
        assert cb.is_open() is False
        assert cb.get_state() == "half-open"

    def test_half_open_failure_reopens(self, tmp_path: Path) -> None:
        cb = _breaker(tmp_path, failure_threshold=1, recovery_interval_ms=1000)
        cb.record_failure()
        time.sleep(1.1)
        assert cb.is_open() is False  # now half-open
        cb.record_failure()
        assert cb.is_open() is True

    def test_half_open_success_closes(self, tmp_path: Path) -> None:
        cb = _breaker(tmp_path, failure_threshold=1, recovery_interval_ms=1000)
        cb.record_failure()
        time.sleep(1.1)
        assert cb.is_open() is False  # half-open
        cb.record_success()
        assert cb.get_state() == "closed"

    def test_state_persists_across_instances(self, tmp_path: Path) -> None:
        # Simulates a fresh subprocess reading prior state from disk.
        cb1 = _breaker(tmp_path, failure_threshold=1)
        cb1.record_failure()
        cb2 = _breaker(tmp_path, failure_threshold=1)
        assert cb2.is_open() is True
