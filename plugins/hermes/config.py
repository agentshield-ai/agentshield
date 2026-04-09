"""Configuration parsing and validation for the AgentShield Hermes plugin."""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from typing import Any, Dict, List


VALID_TIMEOUT_POLICIES = {"allow", "block", "log"}
VALID_NOTIFY_LEVELS = {"all", "high", "critical", "none"}

DEFAULT_INTERCEPT: List[str] = [
    "terminal",
    "execute_command",
    "write_file",
    "create_file",
    "edit_file",
    "patch_file",
    "read_file",
    "web_browse",
    "browser",
    "send_message",
    "delegate",
    "spawn_agent",
    "code_execute",
    "python_execute",
]

DEFAULT_SKIP: List[str] = [
    "todo",
    "memory_search",
    "memory_add",
]


@dataclass(frozen=True)
class AgentShieldConfig:
    """Validated configuration for the AgentShield plugin."""

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


def parse_config(raw: Dict[str, Any] | None = None) -> AgentShieldConfig:
    """Build a validated :class:`AgentShieldConfig` from raw settings.

    Accepts the dict that Hermes passes via ``ctx.get_setting()`` calls
    or a merged dict built during plugin registration.  Missing or
    invalid values fall back to defaults.  The ``AGENTSHIELD_AUTH_TOKEN``
    environment variable is used when no token is supplied in settings.
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

    # Auth token: setting > env var > empty
    auth_token = raw.get("auth_token", "")
    if not isinstance(auth_token, str) or not auth_token:
        auth_token = os.environ.get("AGENTSHIELD_AUTH_TOKEN", "")

    timeout_ms = raw.get("timeout_ms", 200)
    if not isinstance(timeout_ms, (int, float)) or timeout_ms < 5 or timeout_ms > 5000:
        timeout_ms = 200
    timeout_ms = int(timeout_ms)

    timeout_policy = raw.get("timeout_policy", "block")
    if not isinstance(timeout_policy, str) or timeout_policy not in VALID_TIMEOUT_POLICIES:
        timeout_policy = "block"

    notify = raw.get("notify", "high")
    if not isinstance(notify, str) or notify not in VALID_NOTIFY_LEVELS:
        notify = "high"

    intercept = raw.get("intercept", None)
    if not isinstance(intercept, list):
        intercept = list(DEFAULT_INTERCEPT)
    else:
        intercept = [s for s in intercept if isinstance(s, str) and s.strip()]

    skip = raw.get("skip", None)
    if not isinstance(skip, list):
        skip = list(DEFAULT_SKIP)
    else:
        skip = [s for s in skip if isinstance(s, str) and s.strip()]

    cb_threshold = raw.get("circuit_breaker_failure_threshold", 3)
    if not isinstance(cb_threshold, (int, float)) or cb_threshold < 1:
        cb_threshold = 3
    cb_threshold = int(cb_threshold)

    cb_recovery = raw.get("circuit_breaker_recovery_interval_ms", 30_000)
    if not isinstance(cb_recovery, (int, float)) or cb_recovery < 1000:
        cb_recovery = 30_000
    cb_recovery = int(cb_recovery)

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
