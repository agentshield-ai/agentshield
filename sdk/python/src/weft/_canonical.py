"""Weft digests (docs/weft/DESIGN.md §10.3), computed exactly like the server.

- objects and table rows: the lowercase hex SHA-256 of the value's canonical
  JSON: keys sorted by code point, no insignificant whitespace (``,`` and
  ``:``), strings as UTF-8 escaping only ``"``, ``\\`` and U+0000-U+001F
  (``\\b \\f \\n \\r \\t``, otherwise ``\\u00xx``), numbers with the text the
  SDK sends (``json.dumps``), ``true``, ``false``, ``null``;
- tables: the digest of the JSON list of their row digests;
- files: the lowercase hex SHA-256 of their bytes.

The server keeps number text verbatim and hashes the same canonical form
(``internal/weft/refs/digest.go``), so local digests equal server digests;
``tests/test_digest_crosscheck.py`` checks this against a live server.
"""

from __future__ import annotations

import hashlib
import json
from typing import Any, Iterable, List

_SHORT = {'"': '\\"', "\\": "\\\\", "\n": "\\n", "\r": "\\r", "\t": "\\t", "\b": "\\b", "\f": "\\f"}


def _string(s: str, out: List[str]) -> None:
    out.append('"')
    i, n = 0, len(s)
    while i < n:
        ch = s[i]
        o = ord(ch)
        i += 1
        if ch in _SHORT:
            out.append(_SHORT[ch])
        elif o < 0x20:
            out.append("\\u%04x" % o)
        elif 0xD800 <= o <= 0xDFFF:
            # Surrogates reach the server as \\uXXXX escapes: a high-low pair
            # decodes to one character, a lone surrogate to U+FFFD.
            if o <= 0xDBFF and i < n and 0xDC00 <= ord(s[i]) <= 0xDFFF:
                out.append(chr(0x10000 + ((o - 0xD800) << 10) + (ord(s[i]) - 0xDC00)))
                i += 1
            else:
                out.append("�")
        else:
            out.append(ch)
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
                out.append(",")
            _string(str(k), out)
            out.append(":")
            _encode(v[k], out)
        out.append("}")
    elif isinstance(v, (list, tuple)):
        out.append("[")
        for i, x in enumerate(v):
            if i:
                out.append(",")
            _encode(x, out)
        out.append("]")
    else:
        raise TypeError(f"value of type {type(v).__name__} is not JSON-safe")


def canonical_json(value: Any) -> str:
    """Weft's canonical JSON of a value: the text its digest hashes."""
    out: List[str] = []
    _encode(value, out)
    return "".join(out)


# Earlier name.
digest_json = canonical_json


def file_digest(data: bytes) -> str:
    """Digest of file content: the hex SHA-256 of the bytes."""
    return hashlib.sha256(data).hexdigest()


def digest(value: Any) -> str:
    """Digest of a JSON-safe value (objects, functions, table rows)."""
    return hashlib.sha256(canonical_json(value).encode("utf-8")).hexdigest()


def table_digest(row_digests: Iterable[str]) -> str:
    """Digest of a table: the digest of the JSON list of its row digests."""
    return digest(list(row_digests))
