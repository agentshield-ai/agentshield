"""DSPy integration: a ``dspy`` callback recording modules, LM calls and tools.

``weft.init()`` appends ``WeftDSPyCallback`` to ``dspy.settings.callbacks``.
LM usage is recorded by the LiteLLM integration on the nested call.
"""

from __future__ import annotations

import importlib
from typing import Any, Dict, Optional

from ._spans import RunTracker

_registered = False


def _base() -> Any:
    try:
        return getattr(importlib.import_module("dspy.utils.callback"), "BaseCallback", object)
    except ImportError:
        return object


_CLS: Any = None


def _build() -> Any:
    global _CLS
    if _CLS is not None:
        return _CLS

    class WeftDSPyCallback(_base()):  # type: ignore[misc]
        def __init__(self) -> None:
            self.tracker = RunTracker()

        def on_module_start(self, call_id: str, instance: Any, inputs: Dict[str, Any]) -> None:
            self.tracker.start(call_id, f"dspy.{type(instance).__name__}", dict(inputs or {}), kind="chain")

        def on_module_end(self, call_id: str, outputs: Optional[Any], exception: Optional[Exception] = None) -> None:
            self.tracker.end(call_id, outputs, error=exception)

        def on_lm_start(self, call_id: str, instance: Any, inputs: Dict[str, Any]) -> None:
            self.tracker.start(call_id, "dspy.LM", {"model": getattr(instance, "model", None), **dict(inputs or {})}, kind="llm")

        def on_lm_end(self, call_id: str, outputs: Optional[Any], exception: Optional[Exception] = None) -> None:
            self.tracker.end(call_id, outputs, error=exception)

        def on_tool_start(self, call_id: str, instance: Any, inputs: Dict[str, Any]) -> None:
            self.tracker.start(call_id, f"dspy.Tool.{getattr(instance, 'name', 'tool')}", dict(inputs or {}), kind="tool")

        def on_tool_end(self, call_id: str, outputs: Optional[Any], exception: Optional[Exception] = None) -> None:
            self.tracker.end(call_id, outputs, error=exception)


    _CLS = WeftDSPyCallback
    return _CLS


def __getattr__(name: str) -> Any:
    if name == "WeftDSPyCallback":
        return _build()
    raise AttributeError(name)


def patch_dspy() -> bool:
    global _registered
    if _registered:
        return True
    try:
        dspy = importlib.import_module("dspy")
    except ImportError:
        return False
    settings = getattr(dspy, "settings", None)
    if settings is None:
        return False
    callbacks = list(getattr(settings, "callbacks", None) or [])
    if not any(isinstance(c, _build()) for c in callbacks):
        callbacks.append(_build()())
        try:
            settings.configure(callbacks=callbacks)
        except Exception:
            settings.callbacks = callbacks
    _registered = True
    return True
