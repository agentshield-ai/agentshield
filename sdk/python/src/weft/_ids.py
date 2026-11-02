"""UUIDv7 generation (time-ordered ids for calls and traces)."""

from __future__ import annotations

import os
import threading
import time
import uuid

_lock = threading.Lock()
_last_ms = 0
_seq = 0


def uuid7() -> str:
    """Return a new RFC 9562 UUIDv7 string.

    Ids generated in the same millisecond by this process are monotonically
    increasing (a 12 bit sequence counter lives in ``rand_a``).
    """
    global _last_ms, _seq
    with _lock:
        ms = time.time_ns() // 1_000_000
        if ms <= _last_ms:
            ms = _last_ms
            _seq += 1
            if _seq > 0xFFF:
                ms += 1
                _seq = 0
        else:
            _seq = int.from_bytes(os.urandom(2), "big") & 0x7FF
        _last_ms = ms
        seq = _seq
    rand_b = int.from_bytes(os.urandom(8), "big") & ((1 << 62) - 1)
    value = (ms & ((1 << 48) - 1)) << 80
    value |= 0x7 << 76
    value |= seq << 64
    value |= 0b10 << 62
    value |= rand_b
    return str(uuid.UUID(int=value))
