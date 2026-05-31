"""Shared fixtures for AgentShield Gemini connector tests."""

from __future__ import annotations

import sys
from pathlib import Path

# Ensure the connector package is importable regardless of working directory.
_PLUGIN_ROOT = Path(__file__).resolve().parent.parent
if str(_PLUGIN_ROOT) not in sys.path:
    sys.path.insert(0, str(_PLUGIN_ROOT))
