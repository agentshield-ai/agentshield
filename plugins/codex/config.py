"""Configuration parsing and validation for the AgentShield Codex connector.

Codex hooks are short-lived subprocesses, so configuration is loaded once per
hook invocation.  Settings are read (in precedence order) from environment
variables, then a JSON config file (``~/.codex/agentshield.json`` by default,
overridable via ``AGENTSHIELD_CONFIG``), falling back to the documented
defaults.  The values, defaults and validation ranges are identical to the
Hermes and OpenClaw connectors.
"""

from __future__ import annotations

import json
import logging
import os
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Dict, List

logger = logging.getLogger("agentshield")

VALID_TIMEOUT_POLICIES = {"allow", "block", "log"}
VALID_NOTIFY_LEVELS = {"all", "high", "critical", "none"}

# Codex's built-in tools are ``Bash`` and ``apply_patch``; MCP tools arrive as
# ``mcp__<server>__<tool>``.  The default intercept list also includes the
# canonical names so that MCP tools normalising onto them are evaluated.
DEFAULT_INTERCEPT: List[str] = [
    "Bash",
    "apply_patch",
    "exec",
    "write",
    "edit",
    "read",
    "browser",
    "message",
    "sessions_spawn",
    "code_execute",
]

DEFAULT_SKIP: List[str] = [
    "todo",
    "memory_search",
    "memory_add",
]

_DEFAULT_CONFIG_PATH = Path.home() / ".codex" / "agentshield.json"


@dataclass(frozen=True)
class AgentShieldConfig:
    """Validated configuration for the AgentShield Codex connector."""

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


def _load_file_settings() -> Dict[str, Any]:
    """Load raw settings from the JSON config file, if present.

    Returns:
        The parsed JSON object, or an empty dict when the file is missing or
        cannot be parsed.
    """
    path = Path(os.environ.get("AGENTSHIELD_CONFIG", str(_DEFAULT_CONFIG_PATH)))
    if not path.is_file():
        return {}
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError) as exc:
        logger.warning("AgentShield: failed to read config %s: %s", path, exc)
        return {}
    return data if isinstance(data, dict) else {}


def _env_overrides() -> Dict[str, Any]:
    """Collect configuration overrides from environment variables.

    Recognised variables: ``AGENTSHIELD_ENABLED``, ``AGENTSHIELD_ENDPOINT``,
    ``AGENTSHIELD_URL`` (base URL; ``/api/v1/evaluate`` is appended),
    ``AGENTSHIELD_TIMEOUT_MS``, ``AGENTSHIELD_TIMEOUT_POLICY`` and
    ``AGENTSHIELD_NOTIFY``.  ``AGENTSHIELD_AUTH_TOKEN`` is handled in
    :func:`parse_config` as the documented token fallback.

    Returns:
        A dict of overrides (only keys that were actually set).
    """
    overrides: Dict[str, Any] = {}

    enabled = os.environ.get("AGENTSHIELD_ENABLED")
    if enabled is not None:
        overrides["enabled"] = enabled.strip().lower() not in ("0", "false", "no", "")

    endpoint = os.environ.get("AGENTSHIELD_ENDPOINT")
    if endpoint:
        overrides["endpoint"] = endpoint
    else:
        base = os.environ.get("AGENTSHIELD_URL")
        if base:
            overrides["endpoint"] = base.rstrip("/") + "/api/v1/evaluate"

    timeout = os.environ.get("AGENTSHIELD_TIMEOUT_MS")
    if timeout:
        try:
            overrides["timeout_ms"] = int(timeout)
        except ValueError:
            logger.warning("AgentShield: invalid AGENTSHIELD_TIMEOUT_MS=%r", timeout)

    policy = os.environ.get("AGENTSHIELD_TIMEOUT_POLICY")
    if policy:
        overrides["timeout_policy"] = policy.strip()

    notify = os.environ.get("AGENTSHIELD_NOTIFY")
    if notify:
        overrides["notify"] = notify.strip()

    return overrides


def load_config() -> AgentShieldConfig:
    """Load and validate configuration for a hook invocation.

    Merges file settings with environment overrides (environment wins) and
    applies validation via :func:`parse_config`.

    Returns:
        A validated :class:`AgentShieldConfig`.
    """
    raw = _load_file_settings()
    raw.update(_env_overrides())
    return parse_config(raw)


def parse_config(raw: Dict[str, Any] | None = None) -> AgentShieldConfig:
    """Build a validated :class:`AgentShieldConfig` from raw settings.

    Missing or invalid values fall back to defaults.  The
    ``AGENTSHIELD_AUTH_TOKEN`` environment variable supplies the bearer token
    when none is given in settings.

    Args:
        raw: A dict of raw settings, or ``None``.

    Returns:
        A validated :class:`AgentShieldConfig`.
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

    timeout_ms = raw.get("timeout_ms", 200)
    if not isinstance(timeout_ms, (int, float)) or isinstance(timeout_ms, bool):
        timeout_ms = 200
    elif timeout_ms < 5 or timeout_ms > 5000:
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
    if not isinstance(cb_threshold, (int, float)) or isinstance(cb_threshold, bool) or cb_threshold < 1:
        cb_threshold = 3
    cb_threshold = int(cb_threshold)

    cb_recovery = raw.get("circuit_breaker_recovery_interval_ms", 30_000)
    if not isinstance(cb_recovery, (int, float)) or isinstance(cb_recovery, bool) or cb_recovery < 1000:
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
