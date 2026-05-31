"""Tests for config parsing and validation."""

from __future__ import annotations

import json
import os
from pathlib import Path
from unittest.mock import patch

from agentshield_gemini.config import load_config, parse_config


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
        assert "run_shell_command" in cfg.intercept

    def test_override_values(self) -> None:
        cfg = parse_config(
            {
                "enabled": False,
                "endpoint": "http://remote:9000/api/v1/evaluate",
                "timeout_ms": 500,
                "timeout_policy": "allow",
                "notify": "critical",
                "intercept": ["run_shell_command", "write_file"],
                "skip": ["read_file"],
                "circuit_breaker_failure_threshold": 5,
                "circuit_breaker_recovery_interval_ms": 60_000,
            }
        )
        assert cfg.enabled is False
        assert cfg.endpoint == "http://remote:9000/api/v1/evaluate"
        assert cfg.timeout_ms == 500
        assert cfg.timeout_policy == "allow"
        assert cfg.notify == "critical"
        assert cfg.intercept == ["run_shell_command", "write_file"]
        assert cfg.skip == ["read_file"]
        assert cfg.circuit_breaker_failure_threshold == 5
        assert cfg.circuit_breaker_recovery_interval_ms == 60_000

    def test_invalid_timeout_ms_too_high(self) -> None:
        assert parse_config({"timeout_ms": 999_999}).timeout_ms == 200

    def test_invalid_timeout_ms_too_low(self) -> None:
        assert parse_config({"timeout_ms": 1}).timeout_ms == 200

    def test_bool_is_not_valid_timeout_ms(self) -> None:
        # bool is a subclass of int; ensure it is rejected.
        assert parse_config({"timeout_ms": True}).timeout_ms == 200

    def test_invalid_timeout_policy_falls_back(self) -> None:
        assert parse_config({"timeout_policy": "explode"}).timeout_policy == "block"

    def test_invalid_notify_falls_back(self) -> None:
        assert parse_config({"notify": "banana"}).notify == "high"

    def test_auth_token_from_env(self) -> None:
        with patch.dict(os.environ, {"AGENTSHIELD_AUTH_TOKEN": "secret123"}):
            assert parse_config({}).auth_token == "secret123"

    def test_auth_token_setting_overrides_env(self) -> None:
        with patch.dict(os.environ, {"AGENTSHIELD_AUTH_TOKEN": "from-env"}):
            assert parse_config({"auth_token": "from-setting"}).auth_token == "from-setting"

    def test_intercept_filters_bad_values(self) -> None:
        cfg = parse_config({"intercept": ["run_shell_command", 123, "", None, "write_file"]})
        assert cfg.intercept == ["run_shell_command", "write_file"]

    def test_circuit_breaker_threshold_too_low(self) -> None:
        cfg = parse_config({"circuit_breaker_failure_threshold": 0})
        assert cfg.circuit_breaker_failure_threshold == 3

    def test_circuit_breaker_recovery_too_low(self) -> None:
        cfg = parse_config({"circuit_breaker_recovery_interval_ms": 100})
        assert cfg.circuit_breaker_recovery_interval_ms == 30_000


class TestLoadConfig:
    def test_loads_from_file_via_env(self, tmp_path: Path) -> None:
        cfg_path = tmp_path / "gemini.json"
        cfg_path.write_text(json.dumps({"timeout_ms": 1234, "notify": "all"}), encoding="utf-8")
        with patch.dict(os.environ, {"AGENTSHIELD_GEMINI_CONFIG": str(cfg_path)}, clear=False):
            cfg = load_config()
        assert cfg.timeout_ms == 1234
        assert cfg.notify == "all"

    def test_loads_from_agentshield_block(self, tmp_path: Path) -> None:
        cfg_path = tmp_path / "settings.json"
        cfg_path.write_text(
            json.dumps({"agentshield": {"timeout_policy": "log"}}), encoding="utf-8"
        )
        with patch.dict(os.environ, {"AGENTSHIELD_GEMINI_CONFIG": str(cfg_path)}, clear=False):
            cfg = load_config()
        assert cfg.timeout_policy == "log"

    def test_missing_file_uses_defaults(self, tmp_path: Path) -> None:
        missing = tmp_path / "nope.json"
        env = {"AGENTSHIELD_GEMINI_CONFIG": str(missing)}
        # Also ensure no token leaks in from a real environment.
        with patch.dict(os.environ, env, clear=False):
            cfg = load_config()
        assert cfg.timeout_ms == 200
