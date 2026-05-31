"""Tests for evaluation request, audit report and lifecycle builders."""

from __future__ import annotations

from codex.event_builder import (
    MAX_RESULT_SUMMARY_LENGTH,
    build_audit_report,
    build_evaluation_request,
    build_lifecycle_event,
)


class TestBuildEvaluationRequest:
    def test_basic_bash_envelope(self) -> None:
        req = build_evaluation_request(
            "Bash", {"command": "ls -la"}, session_id="sess-1", working_dir="/repo"
        )
        assert req["source"] == "codex"
        assert req["tool_name"] == "exec"
        assert req["event_type"] == "tool_call"
        assert req["command"] == "ls -la"
        assert req["params"]["command"] == "ls -la"
        assert req["session_id"] == "sess-1"
        assert req["working_dir"] == "/repo"
        assert req["data"] == {}
        assert "event_id" in req and "timestamp" in req

    def test_apply_patch_event_type(self) -> None:
        req = build_evaluation_request("apply_patch", {"path": "/a.py"})
        assert req["tool_name"] == "edit"
        assert req["event_type"] == "file_edit"
        assert req["command"] == "Edit: /a.py"

    def test_command_duplicated_into_params(self) -> None:
        req = build_evaluation_request("read_file", {"path": "/etc/passwd"})
        assert req["params"]["command"] == "Read: /etc/passwd"


class TestBuildAuditReport:
    def test_basic_report(self) -> None:
        rep = build_audit_report(
            "Bash",
            {"command": "ls"},
            "total 0",
            correlation_id="corr-1",
            session_id="sess-1",
        )
        assert rep["event_type"] == "tool_result"
        assert rep["correlation_id"] == "corr-1"
        assert rep["tool_name"] == "exec"
        assert rep["result_summary"] == "total 0"
        assert rep["is_error"] is False
        assert rep["error_message"] is None

    def test_error_string_result(self) -> None:
        rep = build_audit_report(
            "Bash", {}, "Error: command not found", correlation_id="c"
        )
        assert rep["is_error"] is True
        assert rep["error_message"] == "Error: command not found"

    def test_error_dict_result(self) -> None:
        rep = build_audit_report(
            "Bash", {}, {"is_error": True, "error": "boom"}, correlation_id="c"
        )
        assert rep["is_error"] is True
        assert rep["error_message"] == "boom"

    def test_explicit_is_error_overrides(self) -> None:
        rep = build_audit_report(
            "Bash", {}, "fine", correlation_id="c", is_error=True
        )
        assert rep["is_error"] is True

    def test_result_summary_truncated(self) -> None:
        rep = build_audit_report(
            "Bash", {}, "x" * 5000, correlation_id="c"
        )
        assert rep["result_summary"] is not None
        assert len(rep["result_summary"]) == MAX_RESULT_SUMMARY_LENGTH

    def test_none_result_summary(self) -> None:
        rep = build_audit_report("Bash", {}, None, correlation_id="c")
        assert rep["result_summary"] is None
        assert rep["is_error"] is False


class TestBuildLifecycleEvent:
    def test_session_start(self) -> None:
        ev = build_lifecycle_event("session_start", session_id="s", data={"source": "startup"})
        assert ev["event_type"] == "session_start"
        assert ev["source"] == "codex"
        assert ev["session_id"] == "s"
        assert ev["data"] == {"source": "startup"}

    def test_session_end_empty_data(self) -> None:
        ev = build_lifecycle_event("session_end", session_id="s")
        assert ev["event_type"] == "session_end"
        assert ev["data"] == {}
