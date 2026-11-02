"""Patch integrations whose library is imported after ``weft.init()``.

``autopatch()`` patches a library straight away when it is already imported.
When it is installed but not imported yet, a meta-path hook runs the patcher
right after the library's trigger module finishes importing, so neither
``weft.init()`` nor an unused integration pays the library's import cost.
"""

from __future__ import annotations

import importlib.abc
import importlib.util
import logging
import sys
import threading
from typing import Any, Callable, Dict, List, Optional

logger = logging.getLogger("weft")

_lock = threading.RLock()
_pending: Dict[str, List[Callable[[], Any]]] = {}
_local = threading.local()
_finder: Optional["_PostImportFinder"] = None


def is_imported(module: str) -> bool:
    return sys.modules.get(module) is not None


def is_installed(module: str) -> bool:
    if module in sys.modules:
        return sys.modules[module] is not None
    try:
        return importlib.util.find_spec(module) is not None
    except (ImportError, ValueError, AttributeError):
        return False


def _run(module: str) -> None:
    with _lock:
        fns = _pending.pop(module, [])
    for fn in fns:
        try:
            fn()
        except Exception as e:  # pragma: no cover - never break an import
            logger.debug("weft: deferred patch for %s failed: %s", module, e)


class _PostImportFinder(importlib.abc.MetaPathFinder):
    def find_spec(self, fullname: str, path: Any = None, target: Any = None) -> Any:
        with _lock:
            if fullname not in _pending:
                return None
        busy = getattr(_local, "busy", None)
        if busy is None:
            busy = _local.busy = set()
        if fullname in busy:
            return None
        busy.add(fullname)
        try:
            spec = None
            for finder in sys.meta_path:
                if finder is self or not hasattr(finder, "find_spec"):
                    continue
                try:
                    spec = finder.find_spec(fullname, path, target)
                except Exception:
                    spec = None
                if spec is not None:
                    break
        finally:
            busy.discard(fullname)
        if spec is None or spec.loader is None or not hasattr(spec.loader, "exec_module"):
            return spec
        loader = spec.loader
        orig_exec = loader.exec_module

        def exec_module(module: Any) -> None:
            orig_exec(module)
            _run(fullname)

        try:
            loader.exec_module = exec_module  # type: ignore[method-assign]
        except Exception:  # pragma: no cover - loader without instance attributes
            return spec
        return spec


def when_imported(module: str, fn: Callable[[], Any]) -> None:
    """Run ``fn`` now if ``module`` is imported, else right after it is imported."""
    global _finder
    if is_imported(module):
        fn()
        return
    with _lock:
        _pending.setdefault(module, []).append(fn)
        if _finder is None:
            _finder = _PostImportFinder()
        if _finder not in sys.meta_path:
            sys.meta_path.insert(0, _finder)


def clear() -> None:
    """Forget deferred patches and remove the import hook."""
    global _finder
    with _lock:
        _pending.clear()
        if _finder is not None and _finder in sys.meta_path:
            sys.meta_path.remove(_finder)
        _finder = None


def pending() -> List[str]:
    with _lock:
        return sorted(_pending)
