"""Digests, computed exactly like the server (and W&B Weave).

- bytes/files: URL-safe base64 of SHA-256 with ``-``→``X``, ``_``→``Y``, no padding
- objects and table rows: that digest of ``json.dumps(val, sort_keys=True)``
  (", " / ": " separators, keys by code point, ``\\uXXXX`` escapes with
  surrogate pairs, number text as sent)
- tables: hex SHA-256 of the concatenated row digests

The SDK sends numbers as ``json.dumps`` writes them (float ``repr``), and the
server keeps number text verbatim, so local digests equal server digests.
"""

from __future__ import annotations

import base64
import hashlib
import json
from typing import Any, Iterable, List

# U+007F is escaped like CPython json.dumps (the server matches).
# Kept in sync with internal/weft/refs/refs.go so local digests match.
ESCAPE_DEL = True

_SHORT = {'"': '\\"', "\\": "\\\\", "\n": "\\n", "\r": "\\r", "\t": "\\t", "\b": "\\b", "\f": "\\f"}


def _string(s: str, out: List[str]) -> None:
    out.append('"')
    for ch in s:
        o = ord(ch)
        if ch in _SHORT:
            out.append(_SHORT[ch])
        elif 0x20 <= o <= 0x7E or (o == 0x7F and not ESCAPE_DEL):
            out.append(ch)
        elif 0xD800 <= o <= 0xDFFF:
            # a lone surrogate reaches the server as U+FFFD
            out.append("\\ufffd")
        elif o > 0xFFFF:
            o -= 0x10000
            out.append("\\u%04x\\u%04x" % (0xD800 + (o >> 10), 0xDC00 + (o & 0x3FF)))
        else:
            out.append("\\u%04x" % o)
    out.append('"')


def _encode(v: Any, out: List[str]) -> None:
    if v is None:
        out.append("null")
    elif v is True:
        out.append("true")
    elif v is False:
        out.append("false")
    elif isinstance(v, str):
        _string(v, out)
    elif isinstance(v, (int, float)):
        out.append(json.dumps(v))  # same text the HTTP client sends
    elif isinstance(v, dict):
        out.append("{")
        for i, k in enumerate(sorted(v, key=str)):
            if i:
                out.append(", ")
            _string(str(k), out)
            out.append(": ")
            _encode(v[k], out)
        out.append("}")
    elif isinstance(v, (list, tuple)):
        out.append("[")
        for i, x in enumerate(v):
            if i:
                out.append(", ")
            _encode(x, out)
        out.append("]")
    else:
        raise TypeError(f"value of type {type(v).__name__} is not JSON-safe")


def digest_json(value: Any) -> str:
    """The text that gets hashed: Python ``json.dumps(value, sort_keys=True)``."""
    out: List[str] = []
    _encode(value, out)
    return "".join(out)


# Backwards-compatible name.
canonical_json = digest_json


def b64digest(data: bytes) -> str:
    """Weave-style digest of raw bytes (also file digests)."""
    enc = base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b"=").decode("ascii")
    return enc.replace("-", "X").replace("_", "Y")


def digest(value: Any) -> str:
    """Digest of a JSON-safe value (objects, ops, table rows)."""
    return b64digest(digest_json(value).encode("utf-8"))


def table_digest(row_digests: Iterable[str]) -> str:
    """Digest of a table: hex SHA-256 of its concatenated row digests."""
    h = hashlib.sha256()
    for d in row_digests:
        h.update(d.encode("utf-8"))
    return h.hexdigest()
