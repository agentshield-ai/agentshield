"""Minimal stdlib HTTP client for the Weft server API."""

from __future__ import annotations

import ipaddress
import json
import logging
import os
import urllib.error
import urllib.parse
import urllib.request
import uuid
from typing import Any, Dict, Iterator, Optional, Set, Tuple

from ._version import __version__

logger = logging.getLogger("weft")


class _RefuseRedirects(urllib.request.HTTPRedirectHandler):
    """The Weft API never redirects. Following one would resend the API key
    (and, for 307/308, the request body) to whatever host the Location names,
    so every redirect is turned into an error instead."""

    def redirect_request(self, req: Any, fp: Any, code: int, msg: str, headers: Any, newurl: str) -> None:
        raise urllib.error.HTTPError(
            req.full_url, code, f"redirect to {newurl} refused (the Weft API does not redirect)", headers, fp
        )


# Same handlers as urllib's default opener (proxies from the environment, https), minus redirects.
_OPENER = urllib.request.build_opener(_RefuseRedirects)

_warned_plain_http: Set[str] = set()


def is_loopback_host(host: Optional[str]) -> bool:
    if not host:
        return False
    host = host.strip("[]").lower()
    if host == "localhost" or host.endswith(".localhost"):
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


def warn_plain_http_key(base_url: str, api_key: Optional[str]) -> bool:
    """Warn (once per server) when an API key would travel over plain http to a non-loopback host."""
    if not api_key:
        return False
    try:
        u = urllib.parse.urlsplit(base_url)
    except ValueError:
        return False
    if u.scheme != "http" or is_loopback_host(u.hostname):
        return False
    key = f"{u.hostname}:{u.port}"
    if key not in _warned_plain_http:
        _warned_plain_http.add(key)
        logger.warning(
            "weft: sending the API key to %s over plain http; anyone on the network path can read it. Use https.", base_url
        )
    return True


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
        warn_plain_http_key(self.base_url, api_key)

    def headers(self, content_type: Optional[str] = "application/json") -> Dict[str, str]:
        h = {"User-Agent": f"weft-python/{__version__}", "Accept": "application/json"}
        if content_type:
            h["Content-Type"] = content_type
        if self.api_key:
            h["Authorization"] = f"Bearer {self.api_key}"
        return h

    def _open(self, method: str, path: str, data: Optional[bytes], headers: Dict[str, str], timeout: Optional[float]):
        req = urllib.request.Request(self.base_url + path, data=data, method=method, headers=headers)
        debug = debug_http_enabled()
        if debug:
            started = _debug_request(req, data)
        try:
            resp = _OPENER.open(req, timeout=timeout or self.timeout)
            if debug:
                return _debug_response(resp, started)
            return resp
        except urllib.error.HTTPError as e:
            body = b""
            try:
                body = e.read()
            except Exception:
                pass
            if debug:
                _debug_response_parts(e.code, str(e.reason), e.headers, body, started)
            reason = body.decode("utf-8", "replace")
            try:
                reason = json.loads(reason).get("reason", reason)
            except Exception:
                pass
            raise WeftHTTPError(e.code, reason or str(e.reason), path) from None
        except (urllib.error.URLError, OSError, ValueError) as e:
            reason = getattr(e, "reason", e)
            if debug:
                _debug_write(f"{_DIV_RESPONSE}\nError: {reason}\n")
            raise WeftHTTPError(0, f"connection failed: {reason}", path) from None

    def request_raw(self, method: str, path: str, body: Any = None, timeout: Optional[float] = None) -> bytes:
        data = None if body is None else json.dumps(body, default=_json_default, allow_nan=False).encode("utf-8")
        with self._open(method, path, data, self.headers("application/json" if data is not None else None), timeout) as resp:
            return resp.read()

    def request(self, method: str, path: str, body: Any = None, query: Optional[Dict[str, str]] = None, timeout: Optional[float] = None) -> Any:
        if query:
            import urllib.parse

            path = path + "?" + urllib.parse.urlencode(query)
        raw = self.request_raw(method, path, body, timeout)
        return json.loads(raw) if raw.strip() else {}

    def post(self, path: str, body: Any, timeout: Optional[float] = None) -> Any:
        raw = self.request_raw("POST", path, body, timeout)
        return json.loads(raw) if raw.strip() else {}

    def get(self, path: str, timeout: Optional[float] = None, query: Optional[Dict[str, str]] = None) -> Any:
        if query:
            import urllib.parse

            path = path + "?" + urllib.parse.urlencode(query)
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


# ---------------------------------------------------------------- WEFT_DEBUG_HTTP
# Print every request and response (time, thread, method, URL, headers with
# Authorization redacted, body; then elapsed time, status, headers and body)
# as plain text to stderr, so program output on stdout is not mixed with it.
_DIV_REQUEST = "-" * 21
_DIV_RESPONSE = "----- Response below -----"
_DEBUG_BODY_LIMIT = 64 * 1024


def debug_http_enabled() -> bool:
    """``WEFT_DEBUG_HTTP`` is set (``1``/``true``); read on every request."""
    return env_flag("WEFT_DEBUG_HTTP")


def _debug_write(text: str) -> None:
    import sys

    try:
        sys.stderr.write(text)
        sys.stderr.flush()
    except Exception:  # pragma: no cover
        pass


def _headers_text(items: Any) -> str:
    out = []
    for k, v in items:
        out.append(f"  {k}: {'<Redacted>' if str(k).lower() in ('authorization', 'cookie', 'x-api-key') else v}")
    return "\n".join(out)


def _body_text(body: Optional[bytes], ctype: str) -> str:
    if not body:
        return "  None"
    if ctype.startswith("multipart/"):
        return f"  <multipart/form-data, {len(body)} bytes>"
    try:
        text = body.decode("utf-8")
    except UnicodeDecodeError:
        return f"  <{len(body)} bytes of binary data>"
    if "json" in ctype or text[:1] in "{[":
        try:
            text = json.dumps(json.loads(text), indent=4, ensure_ascii=False)
        except ValueError:
            pass
    if len(text) > _DEBUG_BODY_LIMIT:
        text = text[:_DEBUG_BODY_LIMIT] + f"\n... ({len(text) - _DEBUG_BODY_LIMIT} more characters)"
    return text


def _debug_request(req: "urllib.request.Request", data: Optional[bytes]) -> float:
    import datetime
    import threading
    import time

    ctype = req.get_header("Content-type") or ""
    _debug_write(
        f"{_DIV_REQUEST}\nTime: {datetime.datetime.now().strftime('%H:%M:%S.%f')}\nThread ID: {threading.get_ident()}\n"
        f"Method: {req.get_method()}\nURL: {req.full_url}\nHeaders:\n{_headers_text(req.header_items())}\n"
        f"Body:\n{_body_text(data, ctype)}\n"
    )
    return time.time()


def _debug_response_parts(status: int, reason: str, headers: Any, body: bytes, started: float) -> None:
    import time

    items = headers.items() if headers is not None else []
    ctype = (headers.get("Content-Type") if headers is not None else "") or ""
    _debug_write(
        f"{_DIV_RESPONSE}\nElapsed Time: {time.time() - started:.2f} seconds\nStatus Code: {status}\nReason: {reason}\n"
        f"Headers:\n{_headers_text(items)}\nBody:\n{_body_text(body, ctype)}\n"
    )


class _BufferedResponse:
    """A read-once response replaced by its buffered body (so it can be printed)."""

    def __init__(self, resp: Any, body: bytes):
        import io

        self._buf = io.BytesIO(body)
        self.status = getattr(resp, "status", 200)
        self.headers = getattr(resp, "headers", None)

    def read(self, n: int = -1) -> bytes:
        return self._buf.read(n)

    def __iter__(self) -> Iterator[bytes]:
        return iter(self._buf)

    def close(self) -> None:
        pass

    def __enter__(self) -> "_BufferedResponse":
        return self

    def __exit__(self, *exc: Any) -> None:
        pass


def _debug_response(resp: Any, started: float) -> _BufferedResponse:
    try:
        body = resp.read()
    finally:
        resp.close()
    _debug_response_parts(getattr(resp, "status", 200), getattr(resp, "reason", ""), getattr(resp, "headers", None), body, started)
    return _BufferedResponse(resp, body)


def env_flag(name: str) -> bool:
    return os.environ.get(name, "").strip().lower() in ("1", "true", "yes", "on")
