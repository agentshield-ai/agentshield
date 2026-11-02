"""Background batching sender.

Call starts/ends are coalesced into ``/call/upsert_batch`` requests; other
writes (op publishing, file uploads, feedback) are sent individually, in
enqueue order. The queue is bounded; when it is full new items are dropped
with a warning. Network failures are retried with backoff and then dropped,
so user code is never blocked or crashed by an unavailable server.
"""

from __future__ import annotations

import logging
import threading
import time
from collections import deque
from typing import Any, Callable, Deque, List, Optional, Tuple

from ._http import HTTPClient, WeftHTTPError

logger = logging.getLogger("weft")

Item = Tuple[str, Any, Optional[Callable[[Any], None]]]


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
    ):
        self.http = http
        self.max_queue = max_queue
        self.batch_size = batch_size
        self.max_batch_bytes = max_batch_bytes
        self.max_retries = max_retries
        self.backoff = backoff
        self.flush_interval = flush_interval
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
        with self._cv:
            if self._stopped:
                return False
            if len(self._q) >= self.max_queue:
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
            self._cv.notify_all()

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
        """Take the next unit of work: a run of call items, or one other item."""
        with self._cv:
            while not self._q and not self._stopped:
                self._cv.wait(timeout=1.0)
            if not self._q:
                return []
            first = self._q[0]
            if first[0] not in ("call_start", "call_end"):
                self._q.popleft()
                self._inflight += 1
                return [first]
        # brief pause lets call ends join their starts in the same batch
        if self.flush_interval:
            time.sleep(self.flush_interval)
        with self._cv:
            items: List[Item] = []
            size = 0
            while self._q and len(items) < self.batch_size:
                kind = self._q[0][0]
                if kind not in ("call_start", "call_end"):
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
        if kind in ("call_start", "call_end"):
            batch = []
            for k, payload, _ in items:
                mode = "start" if k == "call_start" else "end"
                batch.append({"mode": mode, "req": {mode: payload}})
            res = self._with_retries(lambda: self.http.post("/call/upsert_batch", {"batch": batch}), "call/upsert_batch")
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
                if not e.retryable or attempt == self.max_retries:
                    self.failed_requests += 1
                    self._warn(f"weft: failed to send {what}: {e}")
                    return None
            except Exception as e:  # pragma: no cover - defensive
                self.failed_requests += 1
                self._warn(f"weft: failed to send {what}: {e}")
                return None
            time.sleep(delay)
            delay = min(delay * 2, 4.0)
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
