"""Minimal stdlib HTTP client for the Weft server API."""

from __future__ import annotations

import json
import os
import urllib.error
import urllib.request
import uuid
from typing import Any, Dict, Iterator, Optional, Tuple

from ._version import __version__


class WeftHTTPError(Exception):
    """A non-2xx response from the server, or a transport failure (status 0)."""

    def __init__(self, status: int, reason: str, path: str = ""):
        super().__init__(f"{path}: HTTP {status}: {reason}" if status else f"{path}: {reason}")
        self.status = status
        self.reason = reason
        self.path = path

    @property
    def retryable(self) -> bool:
        return self.status == 0 or self.status == 429 or self.status >= 500


def _json_default(o: Any) -> Any:
    return repr(o)


class HTTPClient:
    def __init__(self, base_url: str, api_key: Optional[str] = None, timeout: float = 30.0, project_id: Optional[str] = None):
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.timeout = timeout
        self.project_id = project_id

    def headers(self, content_type: Optional[str] = "application/json") -> Dict[str, str]:
        h = {"User-Agent": f"weft-python/{__version__}", "Accept": "application/json"}
        if content_type:
            h["Content-Type"] = content_type
        if self.api_key:
            h["Authorization"] = f"Bearer {self.api_key}"
        return h

    def _open(self, method: str, path: str, data: Optional[bytes], headers: Dict[str, str], timeout: Optional[float]):
        req = urllib.request.Request(self.base_url + path, data=data, method=method, headers=headers)
        try:
            return urllib.request.urlopen(req, timeout=timeout or self.timeout)
        except urllib.error.HTTPError as e:
            body = b""
            try:
                body = e.read()
            except Exception:
                pass
            reason = body.decode("utf-8", "replace")
            try:
                reason = json.loads(reason).get("reason", reason)
            except Exception:
                pass
            raise WeftHTTPError(e.code, reason or str(e.reason), path) from None
        except (urllib.error.URLError, OSError, ValueError) as e:
            reason = getattr(e, "reason", e)
            raise WeftHTTPError(0, f"connection failed: {reason}", path) from None

    def request_raw(self, method: str, path: str, body: Any = None, timeout: Optional[float] = None) -> bytes:
        data = None if body is None else json.dumps(body, default=_json_default, allow_nan=False).encode("utf-8")
        with self._open(method, path, data, self.headers("application/json" if data is not None else None), timeout) as resp:
            return resp.read()

    def post(self, path: str, body: Any, timeout: Optional[float] = None) -> Any:
        raw = self.request_raw("POST", path, body, timeout)
        return json.loads(raw) if raw.strip() else {}

    def get(self, path: str, timeout: Optional[float] = None) -> Any:
        raw = self.request_raw("GET", path, None, timeout)
        return json.loads(raw) if raw.strip() else {}

    def post_jsonl(self, path: str, body: Any, timeout: Optional[float] = None) -> Iterator[Any]:
        data = json.dumps(body, default=_json_default, allow_nan=False).encode("utf-8")
        resp = self._open("POST", path, data, self.headers(), timeout)
        try:
            for line in resp:
                line = line.strip()
                if line:
                    yield json.loads(line)
        finally:
            resp.close()

    def post_multipart(self, path: str, fields: Dict[str, str], files: Dict[str, Tuple[str, bytes, str]], timeout: Optional[float] = None) -> Any:
        boundary = "weft-" + uuid.uuid4().hex
        parts = []
        for k, v in fields.items():
            parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="{k}"\r\n\r\n{v}\r\n'.encode("utf-8"))
        for k, (filename, content, mime) in files.items():
            parts.append(
                f'--{boundary}\r\nContent-Disposition: form-data; name="{k}"; filename="{filename}"\r\n'
                f"Content-Type: {mime}\r\n\r\n".encode("utf-8")
            )
            parts.append(content)
            parts.append(b"\r\n")
        parts.append(f"--{boundary}--\r\n".encode("utf-8"))
        data = b"".join(parts)
        with self._open("POST", path, data, self.headers(f"multipart/form-data; boundary={boundary}"), timeout) as resp:
            raw = resp.read()
        return json.loads(raw) if raw.strip() else {}


def env_flag(name: str) -> bool:
    return os.environ.get(name, "").strip().lower() in ("1", "true", "yes", "on")
