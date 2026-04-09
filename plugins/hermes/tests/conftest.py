"""Shared fixtures for AgentShield Hermes plugin tests."""

from __future__ import annotations

import sys
from pathlib import Path

import pytest

# Ensure the plugin package is importable regardless of working directory.
_PLUGIN_ROOT = Path(__file__).resolve().parent.parent
if str(_PLUGIN_ROOT) not in sys.path:
    sys.path.insert(0, str(_PLUGIN_ROOT.parent))
