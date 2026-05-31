"""Tests for evaluation request, audit report, and lifecycle builders."""

from __future__ import annotations

from agentshield_gemini.event_builder import (
    MAX_RESULT_SUMMARY_LENGTH,
    build_audit_report,
    build_evaluation_request,
    build_lifecycle_event,
)


class TestBuildEvaluationRequest:
    def test_basic_envelope(self) -> None:
        req = build_evaluation_request(
            "run_shell_command",
            {"command": "rm -rf /"},
            session_id="sess-1",
            working_dir="/work",
        )
        assert req["source"] == "gemini"
        assert req["tool_name"] == "exec"
        assert req["event_type"] == "tool_call"
        assert req["command"] == "rm -rf /"
        assert req["params"]["command"] == "rm -rf /"
        assert req["session_id"] == "sess-1"
        assert req["working_dir"] == "/work"
        assert req["data"] == {}
        assert req["event_id"]
        assert req["timestamp"]

    def test_command_duplicated_into_params(self) -> None:
        req = build_evaluation_request("write_file", {"file_path": "/tmp/x"})
        assert req["tool_name"] == "write"
        assert req["event_type"] == "file_write"
        assert req["params"]["command"] == "Write: /tmp/x"

    def test_precomputed_canonical_is_respected(self) -> None:
        req = build_evaluation_request(
            "weird", {}, canonical_name="exec", command="echo hi"
        )
        assert req["tool_name"] == "exec"
        assert req["command"] == "echo hi"


class TestBuildAuditReport:
    def test_basic(self) -> None:
        rep = build_audit_report(
            "read_file",
            {"file_path": "/etc/hosts"},
            "file contents",
            correlation_id="corr-1",
            session_id="sess-1",
        )
        assert rep["source"] == "gemini"
        assert rep["event_type"] == "tool_result"
        assert rep["tool_name"] == "read"
        assert rep["correlation_id"] == "corr-1"
        assert rep["result_summary"] == "file contents"
        assert rep["is_error"] is False
        assert rep["error_message"] is None

    def test_explicit_error(self) -> None:
        rep = build_audit_report(
            "run_shell_command",
            {"command": "false"},
            None,
            correlation_id="corr-2",
            is_error=True,
            error_message="command failed",
        )
        assert rep["is_error"] is True
        assert rep["error_message"] == "command failed"

    def test_result_summary_truncated(self) -> None:
        big = "a" * (MAX_RESULT_SUMMARY_LENGTH + 500)
        rep = build_audit_report(
            "read_file", {"file_path": "/x"}, big, correlation_id="c"
        )
        assert len(rep["result_summary"]) == MAX_RESULT_SUMMARY_LENGTH


class TestBuildLifecycleEvent:
    def test_session_start(self) -> None:
        ev = build_lifecycle_event(
            "session_start", session_id="sess-1", data={"source": "startup"}
        )
        assert ev["event_type"] == "session_start"
        assert ev["source"] == "gemini"
        assert ev["session_id"] == "sess-1"
        assert ev["data"] == {"source": "startup"}
