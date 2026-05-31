"""AgentShield connector for the Google Gemini CLI.

This package wires AgentShield's real-time detection engine into the
Gemini CLI hooks system (``BeforeTool`` / ``AfterTool`` / ``SessionStart``
/ ``SessionEnd``).  The ``BeforeTool`` hook performs a synchronous,
timeout-bounded security evaluation and can block a tool call before it
runs; the remaining hooks are fire-and-forget.

Because Gemini CLI invokes hooks as short-lived subprocesses, the
package exposes a single CLI entry-point (:func:`agentshield_gemini.hook.main`)
that dispatches on the ``hook_event_name`` field of the JSON payload
delivered on stdin.  Cross-process state (the circuit breaker and the
evaluate -> audit correlation map) is persisted under a state directory.
"""

from __future__ import annotations

CONTRACT_VERSION = "1.0.0"
SOURCE = "gemini"

__all__ = ["CONTRACT_VERSION", "SOURCE"]
