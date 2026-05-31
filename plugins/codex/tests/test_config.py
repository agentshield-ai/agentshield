"""Tests for Codex connector configuration parsing and validation."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from codex.config import DEFAULT_INTERCEPT, DEFAULT_SKIP, load_config, parse_config


class TestParseConfigDefaults:
    def test_empty_uses_defaults(self) -> None:
        cfg = parse_config({})
        assert cfg.enabled is True
        assert cfg.endpoint == "http://127.0.0.1:8433/api/v1/evaluate"
        assert cfg.timeout_ms == 200
        assert cfg.timeout_policy == "block"
        assert cfg.notify == "high"
        assert cfg.intercept == DEFAULT_INTERCEPT
        assert cfg.skip == DEFAULT_SKIP

    def test_none_uses_defaults(self) -> None:
        cfg = parse_config(None)
        assert cfg.timeout_policy == "block"


class TestParseConfigValidation:
    def test_timeout_below_min_falls_back(self) -> None:
        assert parse_config({"timeout_ms": 1}).timeout_ms == 200

    def test_timeout_above_max_falls_back(self) -> None:
        assert parse_config({"timeout_ms": 99999}).timeout_ms == 200

    def test_timeout_in_range_kept(self) -> None:
        assert parse_config({"timeout_ms": 500}).timeout_ms == 500

    def test_timeout_bool_rejected(self) -> None:
        assert parse_config({"timeout_ms": True}).timeout_ms == 200

    def test_invalid_timeout_policy_falls_back(self) -> None:
        assert parse_config({"timeout_policy": "explode"}).timeout_policy == "block"

    def test_valid_timeout_policy_allow(self) -> None:
        assert parse_config({"timeout_policy": "allow"}).timeout_policy == "allow"

    def test_invalid_notify_falls_back(self) -> None:
        assert parse_config({"notify": "loud"}).notify == "high"

    def test_intercept_filters_non_strings(self) -> None:
        cfg = parse_config({"intercept": ["Bash", "", 5, "exec"]})
        assert cfg.intercept == ["Bash", "exec"]

    def test_intercept_non_list_falls_back(self) -> None:
        assert parse_config({"intercept": "Bash"}).intercept == DEFAULT_INTERCEPT

    def test_cb_threshold_below_one_falls_back(self) -> None:
        assert parse_config({"circuit_breaker_failure_threshold": 0}).circuit_breaker_failure_threshold == 3

    def test_cb_recovery_below_min_falls_back(self) -> None:
        cfg = parse_config({"circuit_breaker_recovery_interval_ms": 10})
        assert cfg.circuit_breaker_recovery_interval_ms == 30_000


class TestAuthTokenFallback:
    def test_uses_env_when_empty(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv("AGENTSHIELD_AUTH_TOKEN", "env-token-1234567890")
        assert parse_config({"auth_token": ""}).auth_token == "env-token-1234567890"

    def test_setting_overrides_env(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv("AGENTSHIELD_AUTH_TOKEN", "env-token")
        assert parse_config({"auth_token": "explicit"}).auth_token == "explicit"


class TestLoadConfig:
    def test_loads_from_file(self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
        cfg_path = tmp_path / "agentshield.json"
        cfg_path.write_text(json.dumps({"timeout_ms": 333, "timeout_policy": "log"}))
        monkeypatch.setenv("AGENTSHIELD_CONFIG", str(cfg_path))
        monkeypatch.delenv("AGENTSHIELD_TIMEOUT_MS", raising=False)
        monkeypatch.delenv("AGENTSHIELD_TIMEOUT_POLICY", raising=False)
        cfg = load_config()
        assert cfg.timeout_ms == 333
        assert cfg.timeout_policy == "log"

    def test_env_overrides_file(self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
        cfg_path = tmp_path / "agentshield.json"
        cfg_path.write_text(json.dumps({"timeout_ms": 333}))
        monkeypatch.setenv("AGENTSHIELD_CONFIG", str(cfg_path))
        monkeypatch.setenv("AGENTSHIELD_TIMEOUT_MS", "450")
        assert load_config().timeout_ms == 450

    def test_url_env_builds_endpoint(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv("AGENTSHIELD_CONFIG", "/nonexistent/agentshield.json")
        monkeypatch.delenv("AGENTSHIELD_ENDPOINT", raising=False)
        monkeypatch.setenv("AGENTSHIELD_URL", "http://example.test:9000")
        assert load_config().endpoint == "http://example.test:9000/api/v1/evaluate"
