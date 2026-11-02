"""Redaction of sensitive values in call inputs and outputs.

By default the values of dict keys named like credentials (``api_key``,
``authorization``, ``password``, ``token`` ...; case-insensitive, exact key
match) are replaced with ``"REDACTED"`` before anything leaves the process.
Configure with ``weft.init(..., settings={"redact_keys": [...], "redact_pii": True})``
or disable key redaction with ``WEFT_REDACT_KEYS=0``.
"""

from __future__ import annotations

import os
import re
from typing import Any, Iterable, Optional, Set

REDACTED = "REDACTED"

DEFAULT_REDACT_KEYS = (
    "api_key",
    "apikey",
    "authorization",
    "auth",
    "password",
    "secret",
    "token",
    "access_token",
)

# Ordered: more specific patterns first.
_PII_PATTERNS = [
    (re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}"), "<EMAIL_ADDRESS>"),
    (re.compile(r"\b\d{3}-\d{2}-\d{4}\b"), "<US_SSN>"),
    (re.compile(r"\b(?:\d[ -]?){13,16}\b"), "<CREDIT_CARD>"),
    (re.compile(r"(?<![\w])(?:\+?\d{1,3}[ .-]?)?\(?\d{3}\)?[ .-]?\d{3}[ .-]?\d{4}\b"), "<PHONE_NUMBER>"),
    (re.compile(r"\b(?:\d{1,3}\.){3}\d{1,3}\b"), "<IP_ADDRESS>"),
]


def redact_pii_text(s: str) -> str:
    for pat, repl in _PII_PATTERNS:
        s = pat.sub(repl, s)
    return s


class Redactor:
    def __init__(self, keys: Optional[Iterable[str]] = None, pii: bool = False, enabled: Optional[bool] = None):
        if enabled is None:
            enabled = os.environ.get("WEFT_REDACT_KEYS", "1").strip().lower() not in ("0", "false", "no", "off")
        self.keys: Set[str] = {k.lower() for k in (DEFAULT_REDACT_KEYS if keys is None else keys)} if enabled else set()
        self.pii = pii

    @property
    def active(self) -> bool:
        return bool(self.keys) or self.pii

    def __call__(self, value: Any, _depth: int = 0) -> Any:
        """Redact JSON-safe data (dicts/lists/strings); returns a new value."""
        if not self.active or _depth > 64:
            return value
        if isinstance(value, dict):
            out = {}
            for k, v in value.items():
                if isinstance(k, str) and k.lower() in self.keys:
                    out[k] = REDACTED
                else:
                    out[k] = self(v, _depth + 1)
            return out
        if isinstance(value, list):
            return [self(v, _depth + 1) for v in value]
        if isinstance(value, str) and self.pii:
            return redact_pii_text(value)
        return value
