"""Background batching sender.

Span starts/ends are coalesced into ``POST /api/v1/spans/batch`` requests;
other writes (function versions, file uploads, feedback) are sent
individually, in enqueue order. The queue is bounded; when it is full new items are dropped
with a warning. Network failures are retried with backoff and then dropped,
so user code is never blocked or crashed by an unavailable server.
"""

from __future__ import annotations

import base64
import json
import logging
import os
import threading
import time
from collections import deque
from typing import Any, Callable, Deque, Dict, List, Optional, Tuple

from ._http import HTTPClient, WeftHTTPError

logger = logging.getLogger("weft")

Item = Tuple[str, Any, Optional[Callable[[Any], None]]]

_SPAN_KINDS = ("span_start", "span_end")
# Write-ahead logs of earlier versions name span items "call_start"/"call_end"
# and use the earlier field names.
_LEGACY_KINDS = {"call_start": "span_start", "call_end": "span_end"}
_LEGACY_FIELDS = {"op_name": "name", "thread_id": "session_id", "exception": "error"}


def _legacy_item(kind: str, payload: Any) -> Tuple[str, Any]:
    if kind in _LEGACY_KINDS and isinstance(payload, dict):
        return _LEGACY_KINDS[kind], {_LEGACY_FIELDS.get(k, k): v for k, v in payload.items()}
    return kind, payload


class Sender:
    def __init__(
        self,
        http: HTTPClient,
        max_queue: int = 10_000,
        batch_size: int = 100,
        max_batch_bytes: int = 8 * 1024 * 1024,
        max_retries: int = 3,
        backoff: float = 0.25,
        flush_interval: float = 0.1,
        max_backoff: float = 4.0,
        wal_path: Optional[str] = None,
        wal_only: bool = False,
    ):
        self.http = http
        self.max_queue = max_queue
        self.batch_size = batch_size
        self.max_batch_bytes = max_batch_bytes
        self.max_retries = max_retries
        self.backoff = backoff
        self.flush_interval = flush_interval
        self.max_backoff = max_backoff
        self.wal = WriteAheadLog(wal_path) if wal_path else None
        # WEFT_DISABLE_WAL_SENDER: write everything to the WAL and send nothing.
        self.wal_only = bool(wal_only and self.wal is not None)
        self.wal_written = 0
        self._last_failure_retryable = False
        self._q: Deque[Item] = deque()
        self._cv = threading.Condition()
        self._inflight = 0
        self._stopped = False
        self._last_warn = 0.0
        self.dropped = 0
        self.sent_requests = 0
        self.failed_requests = 0
        self._thread: Optional[threading.Thread] = None

    # -- public -------------------------------------------------------------
    def enqueue(self, kind: str, payload: Any, callback: Optional[Callable[[Any], None]] = None) -> bool:
        if self.wal_only:
            self._spill([(kind, payload, callback)])
            return True
        with self._cv:
            if self._stopped:
                return False
            if len(self._q) >= self.max_queue:
                if self.wal is not None:
                    self._spill([(kind, payload, callback)])
                    return True
                self.dropped += 1
                self._warn(f"weft: send queue full ({self.max_queue}); dropping {kind}")
                return False
            self._q.append((kind, payload, callback))
            self._ensure_thread()
            self._cv.notify_all()
            return True

    def flush(self, timeout: Optional[float] = None) -> bool:
        """Wait until everything enqueued so far has been sent (or dropped)."""
        deadline = None if timeout is None else time.monotonic() + timeout
        with self._cv:
            self._cv.notify_all()
            while self._q or self._inflight:
                remaining = None if deadline is None else deadline - time.monotonic()
                if remaining is not None and remaining <= 0:
                    return False
                self._cv.wait(timeout=0.05 if remaining is None else min(0.05, remaining))
            return True

    def stop(self, timeout: Optional[float] = 10.0) -> None:
        self.flush(timeout)
        with self._cv:
            self._stopped = True
            leftover = list(self._q)
            self._q.clear()
            self._cv.notify_all()
        if leftover and self.wal is not None:
            self._spill(leftover)

    def spill_pending(self) -> int:
        """Move everything still queued to the write-ahead log (used at exit)."""
        if self.wal is None:
            return 0
        with self._cv:
            leftover = list(self._q)
            self._q.clear()
            self._cv.notify_all()
        self._spill(leftover)
        return len(leftover)

    def replay_wal(self) -> int:
        """Re-enqueue items saved by a previous process; returns how many."""
        if self.wal is None or self.wal_only:
            return 0
        items = self.wal.drain()
        for kind, payload in items:
            self.enqueue(kind, payload)
        return len(items)

    def _spill(self, items: List[Item]) -> None:
        if self.wal is None:
            return
        try:
            self.wal.append([(k, p) for k, p, _ in items])
            self.wal_written += len(items)
        except OSError as e:  # pragma: no cover
            self._warn(f"weft: could not write the write-ahead log: {e}")

    @property
    def pending(self) -> int:
        with self._cv:
            return len(self._q) + self._inflight

    # -- internals ----------------------------------------------------------
    def _warn(self, msg: str) -> None:
        now = time.monotonic()
        if now - self._last_warn > 5.0:
            self._last_warn = now
            logger.warning(msg)

    def _ensure_thread(self) -> None:
        if self._thread is None or not self._thread.is_alive():
            self._thread = threading.Thread(target=self._run, name="weft-sender", daemon=True)
            self._thread.start()

    def _take(self) -> List[Item]:
        """Take the next unit of work: a run of span items, or one other item."""
        with self._cv:
            while not self._q and not self._stopped:
                self._cv.wait(timeout=1.0)
            if not self._q:
                return []
            first = self._q[0]
            if first[0] not in _SPAN_KINDS:
                self._q.popleft()
                self._inflight += 1
                return [first]
        # brief pause lets span ends join their starts in the same batch
        if self.flush_interval:
            time.sleep(self.flush_interval)
        with self._cv:
            items: List[Item] = []
            size = 0
            while self._q and len(items) < self.batch_size:
                kind = self._q[0][0]
                if kind not in _SPAN_KINDS:
                    break
                est = _estimate(self._q[0][1])
                if items and size + est > self.max_batch_bytes:
                    break
                items.append(self._q.popleft())
                size += est
            self._inflight += len(items)
            return items

    def _run(self) -> None:
        while True:
            items = self._take()
            if not items:
                with self._cv:
                    if self._stopped:
                        return
                continue
            try:
                self._send(items)
            except Exception as e:  # pragma: no cover - defensive
                logger.debug("weft sender error: %s", e)
            finally:
                with self._cv:
                    self._inflight -= len(items)
                    self._cv.notify_all()

    def _send(self, items: List[Item]) -> None:
        kind = items[0][0]
        if kind in _SPAN_KINDS:
            batch = []
            for k, payload, _ in items:
                batch.append({"start" if k == "span_start" else "end": payload})
            body = {"items": batch}
            res = self._with_retries(lambda: self.http.post("/api/v1/spans/batch", body), "spans/batch")
            if res is None and self._last_failure_retryable:
                self._spill(items)
            for _, _, cb in items:
                if cb is not None and res is not None:
                    _safe(cb, res)
            return
        _, payload, cb = items[0]
        if kind == "post":
            path, body = payload
            fn = lambda: self.http.post(path, body)  # noqa: E731
        elif kind == "multipart":
            path, fields, files = payload
            fn = lambda: self.http.post_multipart(path, fields, files)  # noqa: E731
        else:  # pragma: no cover
            logger.debug("unknown sender item %s", kind)
            return
        res = self._with_retries(fn, kind if kind != "post" else payload[0])
        if res is None and self._last_failure_retryable:
            self._spill(items)
        if cb is not None and res is not None:
            _safe(cb, res)

    def _with_retries(self, fn: Callable[[], Any], what: str) -> Any:
        delay = self.backoff
        for attempt in range(self.max_retries + 1):
            try:
                res = fn()
                self.sent_requests += 1
                return res
            except WeftHTTPError as e:
                self._last_failure_retryable = e.retryable
                if not e.retryable or attempt == self.max_retries:
                    self.failed_requests += 1
                    self._warn(f"weft: failed to send {what}: {e}")
                    return None
            except Exception as e:  # pragma: no cover - defensive
                self._last_failure_retryable = False
                self.failed_requests += 1
                self._warn(f"weft: failed to send {what}: {e}")
                return None
            time.sleep(delay)
            delay = min(delay * 2, self.max_backoff)
        return None


def _safe(cb: Callable[[Any], None], arg: Any) -> None:
    try:
        cb(arg)
    except Exception as e:  # pragma: no cover
        logger.debug("weft sender callback failed: %s", e)


def _estimate(payload: Any) -> int:
    # Cheap size estimate; exact accounting is not needed.
    try:
        return len(repr(payload))
    except Exception:  # pragma: no cover
        return 1024


class WriteAheadLog:
    """Append-only JSONL file of unsent requests, replayed on the next start."""

    def __init__(self, path: str):
        self.path = path
        self._lock = threading.Lock()

    @staticmethod
    def _encode(kind: str, payload: Any) -> Dict[str, Any]:
        if kind == "multipart":
            path, fields, files = payload
            enc = {k: [fn, base64.b64encode(data).decode("ascii"), mime] for k, (fn, data, mime) in files.items()}
            return {"kind": kind, "path": path, "fields": fields, "files": enc}
        if kind == "post":
            return {"kind": kind, "path": payload[0], "body": payload[1]}
        return {"kind": kind, "payload": payload}

    @staticmethod
    def _decode(d: Dict[str, Any]) -> Any:
        kind = d["kind"]
        if kind == "multipart":
            files = {k: (fn, base64.b64decode(data), mime) for k, (fn, data, mime) in d["files"].items()}
            return kind, (d["path"], d["fields"], files)
        if kind == "post":
            return kind, (d["path"], d["body"])
        return _legacy_item(kind, d["payload"])

    def append(self, items: List[Any]) -> None:
        if not items:
            return
        lines = "".join(json.dumps(self._encode(k, p), default=repr) + "\n" for k, p in items)
        with self._lock:
            os.makedirs(os.path.dirname(self.path) or ".", exist_ok=True)
            with open(self.path, "a", encoding="utf-8") as f:
                f.write(lines)

    def drain(self) -> List[Any]:
        """Read and remove all saved items (atomically renamed first)."""
        with self._lock:
            if not os.path.exists(self.path):
                return []
            tmp = f"{self.path}.{os.getpid()}.{int(time.time() * 1000)}.replay"
            try:
                os.replace(self.path, tmp)
            except OSError:
                return []
        out = []
        try:
            with open(tmp, encoding="utf-8") as f:
                for line in f:
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        out.append(self._decode(json.loads(line)))
                    except (ValueError, KeyError):
                        continue
        finally:
            try:
                os.remove(tmp)
            except OSError:
                pass
        return out

    def count(self) -> int:
        try:
            with open(self.path, encoding="utf-8") as f:
                return sum(1 for line in f if line.strip())
        except OSError:
            return 0
