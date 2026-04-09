"""Tests for event builder functions."""

from __future__ import annotations

import uuid

from hermes.event_builder import (
    build_audit_report,
    build_evaluation_request,
    build_lifecycle_event,
)


class TestBuildEvaluationRequest:
    def test_basic_exec_request(self) -> None:
        req = build_evaluation_request(
            "terminal",
            {"command": "ls -la"},
            task_id="session-1",
        )
        assert req["event_type"] == "tool_call"
        assert req["tool_name"] == "exec"
        assert req["source"] == "hermes"
        assert req["command"] == "ls -la"
        assert req["session_id"] == "session-1"
        assert req["params"]["command"] == "ls -la"
        uuid.UUID(req["event_id"])

    def test_write_file_request(self) -> None:
        req = build_evaluation_request(
            "write_file",
            {"path": "/tmp/test.txt", "content": "hello"},
        )
        assert req["event_type"] == "file_write"
        assert req["tool_name"] == "write"
        assert req["command"] == "Write: /tmp/test.txt"

    def test_read_file_request(self) -> None:
        req = build_evaluation_request(
            "read_file",
            {"path": "/tmp/test.txt"},
        )
        assert req["event_type"] == "file_read"
        assert req["tool_name"] == "read"

    def test_optional_agent_id(self) -> None:
        req = build_evaluation_request(
            "terminal",
            {"command": "echo hi"},
            agent_id="agent-42",
        )
        assert req["agent_id"] == "agent-42"

    def test_no_task_id(self) -> None:
        req = build_evaluation_request("terminal", {"command": "pwd"})
        assert req["session_id"] is None

    def test_timestamp_is_iso_format(self) -> None:
        req = build_evaluation_request("terminal", {"command": "date"})
        from datetime import datetime

        datetime.fromisoformat(req["timestamp"])

    def test_skips_normalization_when_canonical_provided(self) -> None:
        req = build_evaluation_request(
            "terminal",
            {"command": "ls"},
            canonical_name="exec",
            command="ls",
        )
        assert req["tool_name"] == "exec"
        assert req["command"] == "ls"


class TestBuildAuditReport:
    def test_basic_report(self) -> None:
        report = build_audit_report(
            "terminal",
            {"command": "ls"},
            "file1.txt\nfile2.txt",
            correlation_id="corr-123",
            task_id="session-1",
        )
        assert report["event_type"] == "tool_result"
        assert report["tool_name"] == "exec"
        assert report["source"] == "hermes"
        assert report["correlation_id"] == "corr-123"
        assert report["is_error"] is False
        assert report["result_summary"] == "file1.txt\nfile2.txt"

    def test_error_detected_from_result(self) -> None:
        report = build_audit_report(
            "terminal",
            {"command": "bad-cmd"},
            "Error: command not found",
            correlation_id="corr-456",
        )
        assert report["is_error"] is True
        assert report["error_message"] == "Error: command not found"

    def test_non_error_result(self) -> None:
        report = build_audit_report(
            "terminal",
            {"command": "echo hi"},
            "hi",
            correlation_id="corr-789",
        )
        assert report["is_error"] is False
        assert report["error_message"] is None

    def test_result_summary_truncation(self) -> None:
        long_result = "x" * 2000
        report = build_audit_report(
            "terminal",
            {"command": "cat bigfile"},
            long_result,
            correlation_id="corr-789",
        )
        assert report["result_summary"] is not None
        assert len(report["result_summary"]) == 1000

    def test_none_result(self) -> None:
        report = build_audit_report(
            "terminal",
            {"command": "true"},
            None,
            correlation_id="corr-000",
        )
        assert report["result_summary"] is None

    def test_skips_normalization_when_canonical_provided(self) -> None:
        report = build_audit_report(
            "terminal",
            {"command": "ls"},
            "ok",
            correlation_id="corr-111",
            canonical_name="exec",
        )
        assert report["tool_name"] == "exec"


class TestBuildLifecycleEvent:
    def test_session_start(self) -> None:
        event = build_lifecycle_event("session_start", task_id="sess-1")
        assert event["event_type"] == "session_start"
        assert event["source"] == "hermes"
        assert event["session_id"] == "sess-1"
        uuid.UUID(event["event_id"])

    def test_custom_data(self) -> None:
        event = build_lifecycle_event(
            "session_end",
            task_id="sess-1",
            data={"message_count": 42},
        )
        assert event["data"]["message_count"] == 42

    def test_no_data_defaults_to_empty(self) -> None:
        event = build_lifecycle_event("agent_start")
        assert event["data"] == {}
