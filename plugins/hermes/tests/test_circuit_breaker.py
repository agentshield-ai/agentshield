"""Tests for the circuit breaker module."""

from __future__ import annotations

import time
from unittest.mock import patch

from hermes.circuit_breaker import CircuitBreaker


class TestCircuitBreaker:
    def test_starts_in_closed_state(self) -> None:
        cb = CircuitBreaker(failure_threshold=3, recovery_interval_ms=30_000)
        assert cb.get_state() == "closed"
        assert cb.is_open() is False

    def test_transitions_to_open_after_threshold_failures(self) -> None:
        cb = CircuitBreaker(failure_threshold=3, recovery_interval_ms=30_000)

        cb.record_failure()
        assert cb.get_state() == "closed"

        cb.record_failure()
        assert cb.get_state() == "closed"

        cb.record_failure()
        assert cb.get_state() == "open"
        assert cb.is_open() is True

    def test_resets_failure_count_on_success(self) -> None:
        cb = CircuitBreaker(failure_threshold=3, recovery_interval_ms=30_000)

        cb.record_failure()
        cb.record_failure()
        cb.record_success()

        # Counter reset — need 3 more to trip.
        cb.record_failure()
        cb.record_failure()
        assert cb.get_state() == "closed"

        cb.record_failure()
        assert cb.get_state() == "open"

    def test_transitions_open_to_half_open_after_recovery(self) -> None:
        cb = CircuitBreaker(failure_threshold=1, recovery_interval_ms=30_000)

        cb.record_failure()
        assert cb.get_state() == "open"
        assert cb.is_open() is True

        # Advance past recovery interval.
        with patch.object(time, "monotonic", return_value=time.monotonic() + 31):
            assert cb.is_open() is False
            assert cb.get_state() == "half-open"

    def test_half_open_to_closed_on_success(self) -> None:
        cb = CircuitBreaker(failure_threshold=1, recovery_interval_ms=30_000)
        cb.record_failure()

        with patch.object(time, "monotonic", return_value=time.monotonic() + 31):
            assert cb.is_open() is False  # half-open, probe allowed

        cb.record_success()
        assert cb.get_state() == "closed"

    def test_half_open_to_open_on_failure(self) -> None:
        cb = CircuitBreaker(failure_threshold=1, recovery_interval_ms=30_000)
        cb.record_failure()

        with patch.object(time, "monotonic", return_value=time.monotonic() + 31):
            assert cb.is_open() is False  # half-open

        cb.record_failure()
        assert cb.get_state() == "open"
        assert cb.is_open() is True

    def test_remains_open_before_recovery_interval(self) -> None:
        cb = CircuitBreaker(failure_threshold=1, recovery_interval_ms=30_000)
        cb.record_failure()
        assert cb.is_open() is True
        # Not enough time has passed.
        assert cb.is_open() is True
