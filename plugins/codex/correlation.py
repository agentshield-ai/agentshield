"""Cross-process correlation tracker linking evaluate -> audit events.

The Codex CLI invokes ``PreToolUse`` and ``PostToolUse`` as two separate
short-lived subprocesses, so the pre-call ``event_id`` must be persisted to disk
for the post-call audit to pick it up.  This module implements a process-safe
FIFO keyed by ``(session_id, tool_name)`` with a TTL, mirroring the in-memory
``_CorrelationTracker`` used by the Hermes connector and the disk-backed tracker
used by the Gemini connector.  Without it every ``AuditReport`` would carry a
fresh, unrelated ``correlation_id`` and could never be linked back to the
evaluation that authorised the tool call.
"""

from __future__ import annotations

import contextlib
import json
import time
from pathlib import Path
from types import TracebackType
from typing import Any, Dict, List, Optional, TextIO, Type

# A pending entry is ``[event_id (str), enqueued_at (float)]``.
_Entry = List[Any]
_Store = Dict[str, List[_Entry]]

try:  # POSIX advisory locking; absent on Windows.
    import fcntl

    _HAVE_FCNTL = True
except ImportError:  # pragma: no cover - exercised only on Windows.
    _HAVE_FCNTL = False

_CORRELATION_TTL_S = 60.0

_DEFAULT_CORRELATION_PATH = Path.home() / ".codex" / ".agentshield-correlation.json"


class CorrelationTracker:
    """Disk-backed FIFO mapping ``(session, tool)`` to pending event ids."""

    def __init__(self, state_path: Optional[Path] = None) -> None:
        """Initialise the tracker.

        Args:
            state_path: Path to the JSON file holding pending event ids.
                Defaults to ``~/.codex/.agentshield-correlation.json``.
        """
        self._state_path = state_path or _DEFAULT_CORRELATION_PATH
        with contextlib.suppress(OSError):
            self._state_path.parent.mkdir(parents=True, exist_ok=True)

    @staticmethod
    def _key(session_id: Optional[str], tool_name: str) -> str:
        """Return the composite bucket key for a session and tool."""
        return f"{session_id or ''}:{tool_name}"

    def push(self, session_id: Optional[str], tool_name: str, event_id: str) -> None:
        """Append *event_id* to the bucket for ``(session_id, tool_name)``.

        Args:
            session_id: The Codex session id.
            tool_name: The canonical tool name.
            event_id: The originating evaluation ``event_id`` to remember.
        """
        key = self._key(session_id, tool_name)
        try:
            with _FileLock(self._state_path) as store:
                data = store.read()
                bucket = data.setdefault(key, [])
                bucket.append([event_id, time.time()])
                store.write(data)
        except OSError:
            # Best-effort: a failed write simply degrades to a fresh
            # correlation id on the matching audit, which is safe.
            pass

    def pop(self, session_id: Optional[str], tool_name: str) -> Optional[str]:
        """Pop and return the oldest fresh event id for a session and tool.

        Args:
            session_id: The Codex session id.
            tool_name: The canonical tool name.

        Returns:
            The oldest non-expired event id, or ``None`` if none remain.
        """
        key = self._key(session_id, tool_name)
        now = time.time()
        try:
            with _FileLock(self._state_path) as store:
                data = store.read()
                bucket: List[_Entry] = data.get(key, [])

                # Purge stale entries in this bucket only.
                while bucket and (now - float(bucket[0][1])) > _CORRELATION_TTL_S:
                    bucket.pop(0)

                if not bucket:
                    data.pop(key, None)
                    store.write(data)
                    return None

                event_id = str(bucket.pop(0)[0])
                if not bucket:
                    data.pop(key, None)
                else:
                    data[key] = bucket
                store.write(data)
                return event_id
        except OSError:
            return None


class _FileLock:
    """Context manager giving exclusive read/modify/write access to a JSON file."""

    def __init__(self, path: Path) -> None:
        self._path = path
        self._handle: Optional[TextIO] = None

    def __enter__(self) -> "_FileLock":
        self._handle = open(self._path, "a+", encoding="utf-8")  # noqa: SIM115
        if _HAVE_FCNTL:
            with contextlib.suppress(OSError):
                fcntl.flock(self._handle.fileno(), fcntl.LOCK_EX)
        return self

    def __exit__(
        self,
        exc_type: Optional[Type[BaseException]],
        exc: Optional[BaseException],
        tb: Optional[TracebackType],
    ) -> None:
        if self._handle is not None:
            self._handle.close()
            self._handle = None

    def read(self) -> _Store:
        """Read and parse the JSON contents (empty dict on any error)."""
        assert self._handle is not None
        self._handle.seek(0)
        try:
            raw = self._handle.read()
            data = json.loads(raw) if raw.strip() else {}
            return data if isinstance(data, dict) else {}
        except ValueError:
            return {}

    def write(self, data: _Store) -> None:
        """Overwrite the file with *data*."""
        assert self._handle is not None
        self._handle.seek(0)
        self._handle.truncate(0)
        self._handle.write(json.dumps(data))
        self._handle.flush()
