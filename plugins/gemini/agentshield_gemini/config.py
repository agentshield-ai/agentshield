"""Configuration parsing and validation for the AgentShield Gemini connector.

Settings are loaded from a JSON config block (the ``agentshield`` section
of the Gemini CLI ``settings.json`` is exported into the hook environment,
or a standalone ``~/.agentshield/gemini.json`` file).  Every key has a
default so the connector works out of the box against a local engine.

The keys, defaults, and validation ranges are identical to the Hermes and
OpenClaw reference connectors per the AgentShield connector contract.
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from typing import Any, Dict, List, Optional

VALID_TIMEOUT_POLICIES = {"allow", "block", "log"}
VALID_NOTIFY_LEVELS = {"all", "high", "critical", "none"}

# Default intercept list expressed in *Gemini CLI* tool names. Canonical
# names also match via the pipeline's intercept check, so a custom canonical
# entry (e.g. "exec") works too.
DEFAULT_INTERCEPT: List[str] = [
    "run_shell_command",
    "write_file",
    "replace",
    "read_file",
    "read_many_files",
    "web_fetch",
    "google_web_search",
    "save_memory",
]

# Low-risk / noisy Gemini tools skipped unconditionally.
DEFAULT_SKIP: List[str] = [
    "write_todos",
    "list_directory",
    "glob",
    "grep_search",
    "ask_user",
]


@dataclass(frozen=True)
class AgentShieldConfig:
    """Validated configuration for the AgentShield Gemini connector."""

    enabled: bool = True
    endpoint: str = "http://127.0.0.1:8433/api/v1/evaluate"
    auth_token: str = ""
    timeout_ms: int = 200
    timeout_policy: str = "block"
    intercept: List[str] = field(default_factory=lambda: list(DEFAULT_INTERCEPT))
    skip: List[str] = field(default_factory=lambda: list(DEFAULT_SKIP))
    notify: str = "high"
    circuit_breaker_failure_threshold: int = 3
    circuit_breaker_recovery_interval_ms: int = 30_000


def parse_config(raw: Optional[Dict[str, Any]] = None) -> AgentShieldConfig:
    """Build a validated :class:`AgentShieldConfig` from raw settings.

    Missing or invalid values fall back to defaults. The
    ``AGENTSHIELD_AUTH_TOKEN`` environment variable is used when no token is
    supplied in settings.

    Args:
        raw: The raw settings mapping, or ``None``.

    Returns:
        A validated, frozen :class:`AgentShieldConfig`.
    """
    if not raw or not isinstance(raw, dict):
        raw = {}

    enabled = raw.get("enabled", True)
    if not isinstance(enabled, bool):
        enabled = True

    endpoint = raw.get("endpoint", "")
    if not isinstance(endpoint, str) or not endpoint.strip():
        endpoint = AgentShieldConfig.endpoint
    else:
        endpoint = endpoint.strip()

    # Auth token: setting > env var > empty.
    auth_token = raw.get("auth_token", "")
    if not isinstance(auth_token, str) or not auth_token:
        auth_token = os.environ.get("AGENTSHIELD_AUTH_TOKEN", "")

    timeout_ms_raw = raw.get("timeout_ms", 200)
    if (
        not isinstance(timeout_ms_raw, (int, float))
        or isinstance(timeout_ms_raw, bool)
        or timeout_ms_raw < 5
        or timeout_ms_raw > 5000
    ):
        timeout_ms_raw = 200
    timeout_ms = int(timeout_ms_raw)

    timeout_policy = raw.get("timeout_policy", "block")
    if not isinstance(timeout_policy, str) or timeout_policy not in VALID_TIMEOUT_POLICIES:
        timeout_policy = "block"

    notify = raw.get("notify", "high")
    if not isinstance(notify, str) or notify not in VALID_NOTIFY_LEVELS:
        notify = "high"

    intercept_raw = raw.get("intercept")
    if not isinstance(intercept_raw, list):
        intercept = list(DEFAULT_INTERCEPT)
    else:
        intercept = [s for s in intercept_raw if isinstance(s, str) and s.strip()]

    skip_raw = raw.get("skip")
    if not isinstance(skip_raw, list):
        skip = list(DEFAULT_SKIP)
    else:
        skip = [s for s in skip_raw if isinstance(s, str) and s.strip()]

    cb_threshold_raw = raw.get("circuit_breaker_failure_threshold", 3)
    if (
        not isinstance(cb_threshold_raw, (int, float))
        or isinstance(cb_threshold_raw, bool)
        or cb_threshold_raw < 1
    ):
        cb_threshold_raw = 3
    cb_threshold = int(cb_threshold_raw)

    cb_recovery_raw = raw.get("circuit_breaker_recovery_interval_ms", 30_000)
    if (
        not isinstance(cb_recovery_raw, (int, float))
        or isinstance(cb_recovery_raw, bool)
        or cb_recovery_raw < 1000
    ):
        cb_recovery_raw = 30_000
    cb_recovery = int(cb_recovery_raw)

    return AgentShieldConfig(
        enabled=enabled,
        endpoint=endpoint,
        auth_token=auth_token,
        timeout_ms=timeout_ms,
        timeout_policy=timeout_policy,
        intercept=intercept,
        skip=skip,
        notify=notify,
        circuit_breaker_failure_threshold=cb_threshold,
        circuit_breaker_recovery_interval_ms=cb_recovery,
    )


def load_config() -> AgentShieldConfig:
    """Load connector config from the standard locations.

    Search order:

    1. ``AGENTSHIELD_GEMINI_CONFIG`` env var pointing at a JSON file.
    2. ``~/.agentshield/gemini.json``.
    3. Built-in defaults.

    Returns:
        A validated :class:`AgentShieldConfig`.
    """
    import json
    from pathlib import Path

    candidates: List[Path] = []
    env_path = os.environ.get("AGENTSHIELD_GEMINI_CONFIG")
    if env_path:
        candidates.append(Path(env_path))
    candidates.append(Path.home() / ".agentshield" / "gemini.json")

    for path in candidates:
        try:
            if path.is_file():
                data = json.loads(path.read_text(encoding="utf-8"))
                if isinstance(data, dict):
                    # Allow either a flat object or an {"agentshield": {...}} block.
                    block = data.get("agentshield") if "agentshield" in data else data
                    if isinstance(block, dict):
                        return parse_config(block)
        except (OSError, ValueError):
            continue

    return parse_config({})
