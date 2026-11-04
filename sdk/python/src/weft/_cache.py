"""Client-side cache of immutable server responses.

Content-addressed reads never change, so their responses are kept in memory
(LRU) and on disk (SQLite, default ``~/.cache/weft``) and reused across
processes:

- ``GET /api/v1/objects/{name}/versions/{digest}`` by digest (not ``latest``,
  ``v<N>`` or an alias); the cached value has no tags or aliases, so those
  are always read from the server;
- ``POST /api/v1/tables/query`` and ``/api/v1/tables/stats`` by table digest;
- ``GET /api/v1/files/{digest}`` by file digest;
- ``POST /api/v1/refs/read`` per object, function or table ref with a digest.

Deleting objects through the client invalidates their cached reads.
Configure with ``weft.init(..., settings={"use_server_cache": False,
"server_cache_dir": "...", "server_cache_size_limit": 500_000_000})`` or the
``WEFT_USE_SERVER_CACHE``, ``WEFT_SERVER_CACHE_DIR`` and
``WEFT_SERVER_CACHE_SIZE_LIMIT`` environment variables.
"""

from __future__ import annotations

import json
import logging
import os
import re
import threading
import time
import urllib.parse
from collections import OrderedDict
from typing import Any, Dict, List, Optional

from ._http import HTTPClient
from ._version import __version__

logger = logging.getLogger("weft")

DEFAULT_SIZE_LIMIT = 1_000_000_000
_DIGEST_RE = re.compile(r"[A-Za-z0-9_-]{43}|[0-9a-f]{64}")  # Weft digests (and the 43-character form of earlier versions)
_KEY_VERSION = "v2"
_OBJECT_VERSION_RE = re.compile(r"/api/v1/objects/([^/?]+)/versions/([^/?]+)\?(.*)")
_FILE_RE = re.compile(r"/api/v1/files/([^/?]+)\?(.*)")


def digest_is_cacheable(d: Any) -> bool:
    """True for content digests (hex SHA-256, or the older 43-character form), false for ``latest``, ``v3`` or aliases."""
    return isinstance(d, str) and bool(_DIGEST_RE.fullmatch(d))


def default_cache_dir() -> str:
    base = os.environ.get("XDG_CACHE_HOME") or os.path.join(os.path.expanduser("~"), ".cache")
    return os.path.join(base, "weft")


def _canon(v: Any) -> str:
    return json.dumps(v, sort_keys=True, separators=(",", ":"), ensure_ascii=False, default=repr)


class ServerCache:
    """A memory LRU in front of an on-disk SQLite store with a size limit."""

    def __init__(self, cache_dir: Optional[str] = None, size_limit: int = DEFAULT_SIZE_LIMIT, memory_size: int = 1000):
        self.cache_dir = cache_dir or default_cache_dir()
        self.size_limit = int(size_limit)
        self.memory_size = memory_size
        self._mem: "OrderedDict[str, bytes]" = OrderedDict()
        self._lock = threading.Lock()
        self._db: Any = None
        self._size = 0
        try:
            import sqlite3

            os.makedirs(self.cache_dir, mode=0o700, exist_ok=True)
            self._db = sqlite3.connect(os.path.join(self.cache_dir, "server_cache.sqlite"), timeout=5, check_same_thread=False)
            self._db.execute("CREATE TABLE IF NOT EXISTS cache (key TEXT PRIMARY KEY, value BLOB NOT NULL, size INTEGER NOT NULL, atime REAL NOT NULL)")
            self._db.execute("CREATE INDEX IF NOT EXISTS cache_atime ON cache(atime)")
            self._db.commit()
            self._size = int(self._db.execute("SELECT COALESCE(SUM(size), 0) FROM cache").fetchone()[0])
        except Exception as e:  # unwritable directory, no sqlite3 module, corrupt file
            logger.debug("weft: on-disk server cache unavailable (%s); using memory only", e)
            self._db = None

    # ---- primitives -----------------------------------------------------------
    def get(self, key: str) -> Optional[bytes]:
        with self._lock:
            v = self._mem.get(key)
            if v is not None:
                self._mem.move_to_end(key)
                return v
            if self._db is None:
                return None
            try:
                row = self._db.execute("SELECT value FROM cache WHERE key = ?", (key,)).fetchone()
                if row is None:
                    return None
                self._db.execute("UPDATE cache SET atime = ? WHERE key = ?", (time.time(), key))
                self._db.commit()
            except Exception as e:
                logger.debug("weft: server cache read failed: %s", e)
                return None
            v = bytes(row[0])
            self._remember(key, v)
            return v

    def set(self, key: str, value: bytes) -> None:
        if len(value) > self.size_limit:
            return
        with self._lock:
            self._remember(key, value)
            if self._db is None:
                return
            try:
                old = self._db.execute("SELECT size FROM cache WHERE key = ?", (key,)).fetchone()
                self._db.execute(
                    "INSERT OR REPLACE INTO cache(key, value, size, atime) VALUES (?, ?, ?, ?)", (key, value, len(value), time.time())
                )
                self._size += len(value) - (int(old[0]) if old else 0)
                if self._size > self.size_limit:
                    self._evict()
                self._db.commit()
            except Exception as e:
                logger.debug("weft: server cache write failed: %s", e)

    def _remember(self, key: str, value: bytes) -> None:
        self._mem[key] = value
        self._mem.move_to_end(key)
        while len(self._mem) > self.memory_size:
            self._mem.popitem(last=False)

    def _evict(self) -> None:
        """Drop least recently used entries until the store is at 90% of the limit."""
        target = self.size_limit * 0.9
        for key, size in self._db.execute("SELECT key, size FROM cache ORDER BY atime").fetchall():
            if self._size <= target:
                break
            self._db.execute("DELETE FROM cache WHERE key = ?", (key,))
            self._mem.pop(key, None)
            self._size -= int(size)

    def delete_prefix(self, prefix: str) -> int:
        with self._lock:
            n = 0
            for k in [k for k in self._mem if k.startswith(prefix)]:
                del self._mem[k]
                n += 1
            if self._db is not None:
                try:
                    rows = self._db.execute("SELECT key, size FROM cache WHERE substr(key, 1, ?) = ?", (len(prefix), prefix)).fetchall()
                    for k, size in rows:
                        self._db.execute("DELETE FROM cache WHERE key = ?", (k,))
                        self._size -= int(size)
                    self._db.commit()
                    n = max(n, len(rows))
                except Exception as e:
                    logger.debug("weft: server cache delete failed: %s", e)
            return n

    def clear(self) -> None:
        with self._lock:
            self._mem.clear()
            if self._db is not None:
                try:
                    self._db.execute("DELETE FROM cache")
                    self._db.commit()
                except Exception as e:
                    logger.debug("weft: server cache clear failed: %s", e)
            self._size = 0

    @property
    def disk_size(self) -> int:
        return self._size

    def close(self) -> None:
        with self._lock:
            if self._db is not None:
                try:
                    self._db.close()
                except Exception:  # pragma: no cover
                    pass
                self._db = None


class CachingHTTPClient(HTTPClient):
    """``HTTPClient`` that serves immutable reads from a ``ServerCache``."""

    def __init__(self, *args: Any, cache: Optional[ServerCache] = None, enabled: bool = True, **kwargs: Any):
        super().__init__(*args, **kwargs)
        self.cache = cache
        self.enabled = enabled and cache is not None
        self.stats = {"hits": 0, "misses": 0, "skips": 0}

    def _key(self, namespace: str, *parts: Any) -> str:
        return "|".join([namespace, self.base_url, *[str(p) for p in parts], _KEY_VERSION, __version__])

    def _prefix(self, namespace: str, *parts: Any) -> str:
        return "|".join([namespace, self.base_url, *[str(p) for p in parts]]) + "|"

    def _get(self, key: str) -> Optional[bytes]:
        if not self.enabled:
            self.stats["skips"] += 1
            return None
        assert self.cache is not None
        v = self.cache.get(key)
        self.stats["hits" if v is not None else "misses"] += 1
        return v

    def _set(self, key: str, value: bytes) -> None:
        if self.enabled and self.cache is not None:
            self.cache.set(key, value)

    # ---- request hooks ----------------------------------------------------------
    def post(self, path: str, body: Any, timeout: Optional[float] = None) -> Any:
        if not isinstance(body, dict):
            return super().post(path, body, timeout)
        if path in ("/api/v1/tables/query", "/api/v1/tables/stats"):
            return self._table(path, body, timeout)
        if path == "/api/v1/refs/read":
            return self._refs(body, timeout)
        if path == "/api/v1/objects/delete":
            self.invalidate_object(body.get("project_id", ""), body.get("name", ""))
        return super().post(path, body, timeout)

    def get(self, path: str, timeout: Optional[float] = None, query: Optional[Dict[str, str]] = None) -> Any:
        if query:
            path = path + "?" + urllib.parse.urlencode(query)
        m = _OBJECT_VERSION_RE.fullmatch(path)
        if m is None:
            return super().get(path, timeout)
        name, version = urllib.parse.unquote(m.group(1)), urllib.parse.unquote(m.group(2))
        if not digest_is_cacheable(version):
            if self.enabled:
                self.stats["skips"] += 1
            return super().get(path, timeout)
        pid = dict(urllib.parse.parse_qsl(m.group(3))).get("project_id", "")
        key = self._key("object", pid, name, version)
        hit = self._get(key)
        if hit is not None:
            try:
                return json.loads(hit)
            except ValueError:
                pass
        res = super().get(path, timeout)
        if isinstance(res, dict) and not res.get("deleted_at"):
            # Tags and aliases change; the cached copy has none (they are read fresh).
            self._set(key, json.dumps({**res, "tags": [], "aliases": [], "is_latest": None}).encode("utf-8"))
        return res

    def request_raw(self, method: str, path: str, body: Any = None, timeout: Optional[float] = None) -> bytes:
        m = _FILE_RE.fullmatch(path) if method == "GET" else None
        if m is not None and digest_is_cacheable(urllib.parse.unquote(m.group(1))):
            pid = dict(urllib.parse.parse_qsl(m.group(2))).get("project_id", "")
            key = self._key("file", pid, urllib.parse.unquote(m.group(1)))
            hit = self._get(key)
            if hit is not None:
                return hit
            data = super().request_raw(method, path, body, timeout)
            self._set(key, data)
            return data
        return super().request_raw(method, path, body, timeout)

    def _table(self, path: str, body: Dict[str, Any], timeout: Optional[float]) -> Any:
        if not digest_is_cacheable(body.get("digest")):
            if self.enabled:
                self.stats["skips"] += 1
            return super().post(path, body, timeout)
        key = self._key(path.strip("/").replace("/", "_"), body.get("project_id", ""), body["digest"], _canon(body))
        hit = self._get(key)
        if hit is not None:
            try:
                return json.loads(hit)
            except ValueError:
                pass
        res = super().post(path, body, timeout)
        self._set(key, json.dumps(res).encode("utf-8"))
        return res

    def _refs(self, body: Dict[str, Any], timeout: Optional[float]) -> Any:
        from ._refs import ObjectRef, TableRef, parse_uri

        refs: List[Any] = list(body.get("refs") or [])
        vals: List[Any] = [None] * len(refs)
        keys: List[Optional[str]] = [None] * len(refs)
        need: List[int] = []
        for i, uri in enumerate(refs):
            key = None
            try:
                r = parse_uri(uri) if isinstance(uri, str) else None
            except ValueError:
                r = None
            if isinstance(r, (ObjectRef, TableRef)) and digest_is_cacheable(r.digest):
                key = self._key("refs_read", r.project_id, uri)
            keys[i] = key
            hit = self._get(key) if key is not None else None
            if key is None and self.enabled:
                self.stats["skips"] += 1
            if hit is not None:
                try:
                    vals[i] = json.loads(hit)
                    continue
                except ValueError:
                    pass
            need.append(i)
        if need:
            res = super().post("/api/v1/refs/read", {**body, "refs": [refs[i] for i in need]}, timeout)
            got = list((res or {}).get("items") or [])
            for j, i in enumerate(need):
                v = got[j] if j < len(got) else None
                vals[i] = v
                if v is not None and keys[i] is not None:
                    self._set(keys[i], json.dumps(v).encode("utf-8"))  # type: ignore[arg-type]
        return {"items": vals}

    # ---- invalidation -------------------------------------------------------------
    def invalidate_object(self, project_id: str, name: str) -> None:
        """Forget cached reads of every version of an object."""
        if self.cache is None or not name:
            return
        self.cache.delete_prefix(self._prefix("object", project_id, name))
        # ref reads embed the object name in the URI; drop this project's object refs to be safe
        self.cache.delete_prefix(self._prefix("refs_read", project_id))


def build_cache(settings: Dict[str, Any]) -> "tuple[Optional[ServerCache], bool]":
    """Resolve the cache settings: returns ``(cache or None, enabled)``."""

    def env_bool(name: str, default: bool) -> bool:
        raw = os.environ.get(name)
        if raw is None or not raw.strip():
            return default
        return raw.strip().lower() in ("1", "true", "yes", "on")

    if settings.get("use_server_cache") is not None:
        enabled = bool(settings["use_server_cache"])
    else:
        enabled = env_bool("WEFT_USE_SERVER_CACHE", True)
    if not enabled:
        return None, False
    cache_dir = settings.get("server_cache_dir") or os.environ.get("WEFT_SERVER_CACHE_DIR") or None
    limit = settings.get("server_cache_size_limit") or os.environ.get("WEFT_SERVER_CACHE_SIZE_LIMIT") or DEFAULT_SIZE_LIMIT
    try:
        limit = int(limit)
    except (TypeError, ValueError):
        limit = DEFAULT_SIZE_LIMIT
    return ServerCache(cache_dir, limit), True


__all__ = ["ServerCache", "CachingHTTPClient", "digest_is_cacheable", "default_cache_dir", "build_cache"]
