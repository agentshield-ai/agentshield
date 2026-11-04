"""Deprecated names (documented in CHANGELOG.md): each warns once per call site."""

from __future__ import annotations

import warnings


def deprecated_call(old: str, new: str, stacklevel: int = 3) -> None:
    """Emit a ``DeprecationWarning`` for ``old``, pointing at ``new``."""
    warnings.warn(f"{old} is deprecated; use {new}", DeprecationWarning, stacklevel=stacklevel)
