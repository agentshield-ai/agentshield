"""File-like values: ``Content`` plus Video/PDF/Html/Markdown media types.

Values are uploaded with ``/file/create`` when a call or object is serialised:

- ``Content`` → ``{"_type": "Content", "mimetype", "filename", "file_digest"}``
- ``Video``/``PDF``/``Html``/``Markdown`` → ``{"_type": "<Type>", "file_digest", "mime_type", "filename"}``
"""

from __future__ import annotations

import mimetypes
import os
from typing import TYPE_CHECKING, Any, Dict, Optional, Union

from ._canonical import b64digest

if TYPE_CHECKING:  # pragma: no cover
    from ._client import WeftClient


class Content:
    """Arbitrary file content (documents, audio, archives, ...)."""

    _media_type = "Content"
    _default_mimetype = "application/octet-stream"

    def __init__(self, data: bytes, mimetype: Optional[str] = None, filename: Optional[str] = None):
        if isinstance(data, str):
            data = data.encode("utf-8")
        self.data = bytes(data)
        self.filename = filename
        self.mimetype = mimetype or _guess(filename, self.data) or self._default_mimetype

    @classmethod
    def from_path(cls, path: Union[str, "os.PathLike[str]"], mimetype: Optional[str] = None) -> "Content":
        p = os.fspath(path)
        with open(p, "rb") as f:
            data = f.read()
        return cls(data, mimetype=mimetype, filename=os.path.basename(p))

    @classmethod
    def from_bytes(cls, data: bytes, mimetype: Optional[str] = None, filename: Optional[str] = None, extension: Optional[str] = None) -> "Content":
        if filename is None and extension:
            filename = "file." + extension.lstrip(".")
        return cls(data, mimetype=mimetype, filename=filename)

    @classmethod
    def from_text(cls, text: str, mimetype: str = "text/plain", filename: Optional[str] = None, encoding: str = "utf-8") -> "Content":
        return cls(text.encode(encoding), mimetype=mimetype, filename=filename)

    @property
    def size(self) -> int:
        return len(self.data)

    @property
    def digest(self) -> str:
        return b64digest(self.data)

    def as_text(self, encoding: str = "utf-8") -> str:
        return self.data.decode(encoding)

    def save(self, path: Union[str, "os.PathLike[str]"]) -> None:
        with open(os.fspath(path), "wb") as f:
            f.write(self.data)

    def _weft_serialize(self, client: Optional["WeftClient"]) -> Dict[str, Any]:
        file_digest = client._upload_file(self.data, self.mimetype) if client is not None else self.digest
        if self._media_type == "Content":
            return {"_type": "Content", "mimetype": self.mimetype, "filename": self.filename, "file_digest": file_digest}
        return {"_type": self._media_type, "file_digest": file_digest, "mime_type": self.mimetype, "filename": self.filename}

    def __repr__(self) -> str:
        return f"{type(self).__name__}(mimetype={self.mimetype!r}, filename={self.filename!r}, size={self.size})"


class Video(Content):
    _media_type = "Video"
    _default_mimetype = "video/mp4"


class PDF(Content):
    _media_type = "PDF"
    _default_mimetype = "application/pdf"


class Html(Content):
    """HTML rendered by the UI: ``weft.Html("<b>hi</b>")``."""

    _media_type = "Html"
    _default_mimetype = "text/html"

    def __init__(self, data: Union[str, bytes], mimetype: Optional[str] = None, filename: Optional[str] = None):
        super().__init__(data if isinstance(data, bytes) else data.encode("utf-8"), mimetype or "text/html", filename)


class Markdown(Content):
    """Markdown rendered by the UI: ``weft.Markdown("# Title")``."""

    _media_type = "Markdown"
    _default_mimetype = "text/markdown"

    def __init__(self, data: Union[str, bytes], mimetype: Optional[str] = None, filename: Optional[str] = None):
        super().__init__(data if isinstance(data, bytes) else data.encode("utf-8"), mimetype or "text/markdown", filename)


def _guess(filename: Optional[str], data: bytes) -> Optional[str]:
    if filename:
        mt, _ = mimetypes.guess_type(filename)
        if mt:
            return mt
    from ._serialize import sniff_mime

    m = sniff_mime(data)
    return None if m == "application/octet-stream" else m
