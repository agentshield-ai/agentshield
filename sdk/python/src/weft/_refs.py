"""Ref URIs: ``weft:///<entity>/<project>/<kind>/<name>:<digest>[/attr/x/...]``."""

from __future__ import annotations

import urllib.parse
from dataclasses import dataclass, field
from typing import Any, Optional, Tuple

SCHEMES = ("weft:///",)
_EXTRA_KINDS = ("attr", "key", "index", "id")


def is_ref_uri(s: Any) -> bool:
    return isinstance(s, str) and s.startswith(SCHEMES)


def _quote(s: str) -> str:
    return urllib.parse.quote(s, safe="-._~")


def _extra_path(extra: Tuple[str, ...]) -> str:
    return "".join("/" + _quote(p) for p in extra)


@dataclass(frozen=True)
class Ref:
    entity: str
    project: str

    @property
    def project_id(self) -> str:
        return f"{self.entity}/{self.project}"

    def uri(self) -> str:  # pragma: no cover - overridden
        raise NotImplementedError

    def __str__(self) -> str:
        return self.uri()

    def get(self) -> Any:
        from ._client import require_client

        return require_client().get(self)


@dataclass(frozen=True)
class ObjectRef(Ref):
    name: str
    digest: str
    extra: Tuple[str, ...] = field(default_factory=tuple)

    _kind = "object"

    def uri(self) -> str:
        return f"weft:///{self.entity}/{self.project}/{self._kind}/{self.name}:{self.digest}" + _extra_path(self.extra)

    def with_extra(self, *parts: str) -> "ObjectRef":
        return type(self)(self.entity, self.project, self.name, self.digest, tuple(self.extra) + tuple(str(p) for p in parts))

    def attr(self, name: str) -> "ObjectRef":
        return self.with_extra("attr", name)

    def key(self, k: str) -> "ObjectRef":
        return self.with_extra("key", k)

    def index(self, i: int) -> "ObjectRef":
        return self.with_extra("index", str(i))

    def row(self, row_digest: str) -> "ObjectRef":
        return self.with_extra("id", row_digest)

    def __repr__(self) -> str:
        return f"{type(self).__name__}({self.uri()!r})"


@dataclass(frozen=True)
class OpRef(ObjectRef):
    _kind = "op"


@dataclass(frozen=True)
class TableRef(Ref):
    digest: str
    extra: Tuple[str, ...] = field(default_factory=tuple)

    def uri(self) -> str:
        return f"weft:///{self.entity}/{self.project}/table/{self.digest}" + _extra_path(self.extra)

    def __repr__(self) -> str:
        return f"TableRef({self.uri()!r})"


@dataclass(frozen=True)
class CallRef(Ref):
    id: str
    extra: Tuple[str, ...] = field(default_factory=tuple)

    def uri(self) -> str:
        return f"weft:///{self.entity}/{self.project}/call/{self.id}" + _extra_path(self.extra)

    def __repr__(self) -> str:
        return f"CallRef({self.uri()!r})"


def parse_uri(uri: str) -> Ref:
    for scheme in SCHEMES:
        if uri.startswith(scheme):
            rest = uri[len(scheme):]
            break
    else:
        raise ValueError(f"not a weft ref: {uri!r}")
    parts = rest.split("/")
    if len(parts) < 4:
        raise ValueError(f"invalid ref: {uri!r}")
    entity, project, kind, name_part = parts[0], parts[1], parts[2], parts[3]
    extra = tuple(urllib.parse.unquote(p) for p in parts[4:])
    if extra and (len(extra) % 2 != 0 or any(extra[i] not in _EXTRA_KINDS for i in range(0, len(extra), 2))):
        raise ValueError(f"invalid ref path: {uri!r}")
    if kind in ("object", "op"):
        name, sep, digest = name_part.rpartition(":")
        if not sep:
            name, digest = name_part, "latest"
        cls = OpRef if kind == "op" else ObjectRef
        return cls(entity, project, urllib.parse.unquote(name), digest, extra)
    if kind == "table":
        return TableRef(entity, project, name_part, extra)
    if kind == "call":
        return CallRef(entity, project, name_part, extra)
    raise ValueError(f"unknown ref kind {kind!r} in {uri!r}")


def split_project(project: str) -> Tuple[str, str]:
    """Normalise ``"p"`` to ``("default", "p")`` and ``"e/p"`` to ``("e", "p")``."""
    project = project.strip().strip("/")
    if not project:
        raise ValueError("project name must not be empty")
    if "/" in project:
        entity, _, proj = project.partition("/")
        if not entity or not proj or "/" in proj:
            raise ValueError(f"invalid project {project!r}; use 'project' or 'entity/project'")
        return entity, proj
    return "default", project


def parse_name_version(s: str) -> Tuple[str, Optional[str]]:
    name, sep, version = s.rpartition(":")
    if not sep:
        return s, None
    return name, version or None
