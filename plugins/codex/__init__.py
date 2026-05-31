"""AgentShield connector for the OpenAI Codex CLI.

Codex CLI exposes a lifecycle hooks system whose ``PreToolUse`` event can
synchronously deny a tool call, so this connector provides genuine *blocking*
enforcement (not audit-only).  The package mirrors the structure of the Hermes
and OpenClaw connectors:

- :mod:`codex.normalise`        -- tool-name + command normalisation
- :mod:`codex.config`           -- config parsing and validation
- :mod:`codex.circuit_breaker`  -- file-backed three-state circuit breaker
- :mod:`codex.client`           -- stdlib HTTP client for the engine API
- :mod:`codex.event_builder`    -- EvaluationRequest / AuditReport / LifecycleEvent
- :mod:`codex.agentshield_hook` -- the per-event hook entry-point Codex invokes
"""

from __future__ import annotations

__all__ = ["__version__"]

__version__ = "1.0.0"
