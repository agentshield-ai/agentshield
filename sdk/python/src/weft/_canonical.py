"""Canonical JSON and digests.

The server computes digests as the unpadded base64url SHA-256 of canonical
JSON: keys sorted, no whitespace, no HTML escaping, numbers kept in the text
form the client sent (Go ``json.Decoder.UseNumber``). Since the SDK sends
numbers as ``json.dumps`` writes them, the canonical form below matches the
server's byte for byte, so digests computed locally equal the server's.
"""

from __future__ import annotations

import base64
import hashlib
import json
import re
from typing import Any

_SURROGATE = re.compile("[\ud800-\udfff]")


def canonical_json(value: Any) -> str:
    s = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False)
    # Go's encoder always escapes U+2028/U+2029 and replaces invalid UTF-16.
    if "\u2028" in s or "\u2029" in s:
        s = s.replace("\u2028", "\\u2028").replace("\u2029", "\\u2029")
    if _SURROGATE.search(s):
        s = _SURROGATE.sub("\ufffd", s)
    return s


def b64digest(data: bytes) -> str:
    return base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b"=").decode("ascii")


def digest(value: Any) -> str:
    """Digest of a JSON-safe value (matches the server's object digest)."""
    return b64digest(canonical_json(value).encode("utf-8"))
