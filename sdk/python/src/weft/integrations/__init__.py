"""LLM client integrations. ``weft.init()`` calls ``autopatch()`` by default.

Patching is idempotent and silently skipped when a library is not installed.
"""

from __future__ import annotations

from ._common import unpatch_all
from .anthropic import patch_anthropic
from .openai import patch_openai, patch_openai_compatible


def autopatch() -> None:
    for fn in (patch_openai, patch_anthropic):
        try:
            fn()
        except Exception as e:  # pragma: no cover - never break init
            import logging

            logging.getLogger("weft").debug("weft: autopatch %s failed: %s", fn.__name__, e)


__all__ = ["autopatch", "patch_openai", "patch_anthropic", "patch_openai_compatible", "unpatch_all"]
