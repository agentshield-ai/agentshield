"""Tests for the disk-backed correlation tracker."""

from __future__ import annotations

import time
from pathlib import Path

from codex import correlation
from codex.correlation import CorrelationTracker


def _tracker(tmp_path: Path) -> CorrelationTracker:
    return CorrelationTracker(state_path=tmp_path / "corr.json")


def test_push_then_pop_returns_event_id(tmp_path: Path) -> None:
    tracker = _tracker(tmp_path)
    tracker.push("s1", "exec", "evt-1")
    assert tracker.pop("s1", "exec") == "evt-1"


def test_pop_is_fifo_per_bucket(tmp_path: Path) -> None:
    tracker = _tracker(tmp_path)
    tracker.push("s1", "exec", "evt-1")
    tracker.push("s1", "exec", "evt-2")
    assert tracker.pop("s1", "exec") == "evt-1"
    assert tracker.pop("s1", "exec") == "evt-2"


def test_pop_empty_returns_none(tmp_path: Path) -> None:
    tracker = _tracker(tmp_path)
    assert tracker.pop("s1", "exec") is None


def test_buckets_are_isolated_by_session_and_tool(tmp_path: Path) -> None:
    tracker = _tracker(tmp_path)
    tracker.push("s1", "exec", "evt-a")
    tracker.push("s2", "exec", "evt-b")
    tracker.push("s1", "write", "evt-c")
    assert tracker.pop("s2", "exec") == "evt-b"
    assert tracker.pop("s1", "write") == "evt-c"
    assert tracker.pop("s1", "exec") == "evt-a"


def test_expired_entries_are_purged(tmp_path: Path, monkeypatch) -> None:
    tracker = _tracker(tmp_path)
    tracker.push("s1", "exec", "stale")
    # Advance wall-clock past the TTL.
    real_time = time.time()
    monkeypatch.setattr(
        correlation.time, "time", lambda: real_time + correlation._CORRELATION_TTL_S + 1
    )
    assert tracker.pop("s1", "exec") is None
