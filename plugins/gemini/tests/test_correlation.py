"""Tests for the disk-backed correlation tracker."""

from __future__ import annotations

from pathlib import Path

from agentshield_gemini.correlation import CorrelationTracker


def _tracker(tmp_path: Path) -> CorrelationTracker:
    return CorrelationTracker(state_path=tmp_path / "correlation.json")


class TestCorrelationTracker:
    def test_push_then_pop(self, tmp_path: Path) -> None:
        t = _tracker(tmp_path)
        t.push("s1", "run_shell_command", "e1")
        assert t.pop("s1", "run_shell_command") == "e1"

    def test_fifo_ordering(self, tmp_path: Path) -> None:
        t = _tracker(tmp_path)
        t.push("s1", "run_shell_command", "e1")
        t.push("s1", "run_shell_command", "e2")
        assert t.pop("s1", "run_shell_command") == "e1"
        assert t.pop("s1", "run_shell_command") == "e2"

    def test_pop_empty_returns_none(self, tmp_path: Path) -> None:
        t = _tracker(tmp_path)
        assert t.pop("s1", "run_shell_command") is None

    def test_distinct_keys_isolated(self, tmp_path: Path) -> None:
        t = _tracker(tmp_path)
        t.push("s1", "run_shell_command", "e1")
        t.push("s2", "run_shell_command", "e2")
        assert t.pop("s2", "run_shell_command") == "e2"
        assert t.pop("s1", "run_shell_command") == "e1"

    def test_persists_across_instances(self, tmp_path: Path) -> None:
        _tracker(tmp_path).push("s1", "read_file", "e9")
        assert _tracker(tmp_path).pop("s1", "read_file") == "e9"
