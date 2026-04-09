"""Tests for config parsing and validation."""

from __future__ import annotations

import os
from unittest.mock import patch

from hermes.config import AgentShieldConfig, parse_config


class TestParseConfig:
    def test_defaults_when_no_input(self) -> None:
        cfg = parse_config(None)
        assert cfg.enabled is True
        assert cfg.endpoint == "http://127.0.0.1:8433/api/v1/evaluate"
        assert cfg.timeout_ms == 200
        assert cfg.timeout_policy == "block"
        assert cfg.notify == "high"
        assert cfg.circuit_breaker_failure_threshold == 3
        assert cfg.circuit_breaker_recovery_interval_ms == 30_000

    def test_defaults_when_empty_dict(self) -> None:
        cfg = parse_config({})
        assert cfg.enabled is True
        assert "terminal" in cfg.intercept

    def test_override_values(self) -> None:
        cfg = parse_config({
            "enabled": False,
            "endpoint": "http://remote:9000/api/v1/evaluate",
            "timeout_ms": 500,
            "timeout_policy": "allow",
            "notify": "critical",
            "intercept": ["terminal", "write_file"],
            "skip": ["read_file"],
            "circuit_breaker_failure_threshold": 5,
            "circuit_breaker_recovery_interval_ms": 60_000,
        })
        assert cfg.enabled is False
        assert cfg.endpoint == "http://remote:9000/api/v1/evaluate"
        assert cfg.timeout_ms == 500
        assert cfg.timeout_policy == "allow"
        assert cfg.notify == "critical"
        assert cfg.intercept == ["terminal", "write_file"]
        assert cfg.skip == ["read_file"]
        assert cfg.circuit_breaker_failure_threshold == 5
        assert cfg.circuit_breaker_recovery_interval_ms == 60_000

    def test_invalid_timeout_ms_falls_back_to_default(self) -> None:
        cfg = parse_config({"timeout_ms": 999_999})
        assert cfg.timeout_ms == 200

    def test_invalid_timeout_ms_too_low(self) -> None:
        cfg = parse_config({"timeout_ms": 1})
        assert cfg.timeout_ms == 200

    def test_invalid_timeout_policy_falls_back(self) -> None:
        cfg = parse_config({"timeout_policy": "explode"})
        assert cfg.timeout_policy == "block"

    def test_invalid_notify_falls_back(self) -> None:
        cfg = parse_config({"notify": "banana"})
        assert cfg.notify == "high"

    def test_auth_token_from_env(self) -> None:
        with patch.dict(os.environ, {"AGENTSHIELD_AUTH_TOKEN": "secret123"}):
            cfg = parse_config({})
            assert cfg.auth_token == "secret123"

    def test_auth_token_setting_overrides_env(self) -> None:
        with patch.dict(os.environ, {"AGENTSHIELD_AUTH_TOKEN": "from-env"}):
            cfg = parse_config({"auth_token": "from-setting"})
            assert cfg.auth_token == "from-setting"

    def test_intercept_filters_bad_values(self) -> None:
        cfg = parse_config({"intercept": ["terminal", 123, "", None, "write_file"]})
        assert cfg.intercept == ["terminal", "write_file"]

    def test_circuit_breaker_threshold_too_low(self) -> None:
        cfg = parse_config({"circuit_breaker_failure_threshold": 0})
        assert cfg.circuit_breaker_failure_threshold == 3

    def test_circuit_breaker_recovery_too_low(self) -> None:
        cfg = parse_config({"circuit_breaker_recovery_interval_ms": 100})
        assert cfg.circuit_breaker_recovery_interval_ms == 30_000
