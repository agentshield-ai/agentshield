"""File-like values: ``Content`` plus Image/Audio/Video/PDF/Html/Markdown media types.

Values are uploaded with ``POST /api/v1/files/upload`` when a span or object is serialised:

- ``Content`` → ``{"_type": "Content", "mimetype", "filename", "file_digest"}``
- ``Image``/``Audio``/``Video``/``PDF``/``Html``/``Markdown`` → ``{"_type": "<Type>", "file_digest", "mime_type", "filename"}``

Media that already lives in your own bucket is logged by reference, without
uploading (bring your own bucket): ``weft.Content.from_uri("s3://bucket/key")``
(also ``weft.Image.from_uri(...)``, ``Video``, ``PDF``, ...) serialises as
``{"_type": "<Type>", "uri": "s3://bucket/key", "mimetype"/"mime_type", "filename"}``.
``https://`` URLs and Weft file digests (``weft-file://<digest>``) work too.
``client.resolve_url(uri)`` turns a reference or file digest into a download URL
(``POST /api/v1/files/resolve``: presigned for the server's S3 backend).
"""

from __future__ import annotations

import mimetypes
import os
from typing import TYPE_CHECKING, Any, Dict, Optional, Union

from ._canonical import file_digest

if TYPE_CHECKING:  # pragma: no cover
    from ._client import WeftClient


class Content:
    """Arbitrary file content (documents, audio, archives, ...)."""

    _media_type = "Content"
    _default_mimetype = "application/octet-stream"

    def __init__(self, data: bytes, mimetype: Optional[str] = None, filename: Optional[str] = None):
        if isinstance(data, str):
            data = data.encode("utf-8")
        self._data: Optional[bytes] = bytes(data)
        self.uri: Optional[str] = None
        self._size: Optional[int] = None
        self.filename = filename
        self.mimetype = mimetype or _guess(filename, self._data) or self._default_mimetype

    @classmethod
    def from_uri(
        cls,
        uri: str,
        mimetype: Optional[str] = None,
        filename: Optional[str] = None,
        size: Optional[int] = None,
    ) -> "Content":
        """Reference media that already lives elsewhere (``s3://bucket/key``, ``https://...`` or
        ``weft-file://<digest>``) without uploading it. The bytes are fetched only if read
        (``data``, ``as_text``, ``save``), through ``client.resolve_url`` for non-HTTP URIs."""
        if not isinstance(uri, str) or "://" not in uri:
            raise ValueError(f"expected a URI such as s3://bucket/key, got {uri!r}")
        obj = cls.__new__(cls)
        obj._data = None
        obj.uri = uri
        obj._size = size
        path = uri.split("?", 1)[0].rstrip("/")
        obj.filename = filename or (os.path.basename(path.split("://", 1)[1]) or None)
        obj.mimetype = mimetype or (_guess(obj.filename, b"") if obj.filename else None) or cls._default_mimetype
        return obj

    @property
    def is_reference(self) -> bool:
        """True for ``from_uri`` content (stored by reference, not uploaded)."""
        return self.uri is not None

    @property
    def data(self) -> bytes:
        if self._data is None:
            self._data = self._fetch()
        return self._data

    @data.setter
    def data(self, value: bytes) -> None:
        self._data = bytes(value)

    def _fetch(self) -> bytes:
        import urllib.request

        assert self.uri is not None
        url = self.uri
        if not url.startswith(("http://", "https://")):
            from ._client import require_client

            client = require_client()
            url = client.resolve_url(url)
            if url and url.startswith(client.server_url + "/api/v1/files/"):
                # this server's file: read it with the client's credentials
                return client._http.request_raw("GET", url[len(client.server_url):])
        with urllib.request.urlopen(url, timeout=60) as resp:  # noqa: S310 - user-provided media URL
            return resp.read()

    def resolve_url(self, expires_in: Optional[int] = None) -> str:
        """A download URL for this content (its reference, or its uploaded file digest)."""
        from ._client import require_client

        return require_client().resolve_url(self.uri if self.uri is not None else self.digest, expires_in=expires_in)

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
        if self._data is None and self._size is not None:
            return self._size
        return len(self.data)

    @property
    def digest(self) -> str:
        return file_digest(self.data)

    def as_text(self, encoding: str = "utf-8") -> str:
        return self.data.decode(encoding)

    def save(self, path: Union[str, "os.PathLike[str]"]) -> None:
        with open(os.fspath(path), "wb") as f:
            f.write(self.data)

    def _weft_serialize(self, client: Optional["WeftClient"]) -> Dict[str, Any]:
        if self.uri is not None:
            if self._media_type == "Content":
                out: Dict[str, Any] = {"_type": "Content", "mimetype": self.mimetype, "filename": self.filename, "uri": self.uri}
            else:
                out = {"_type": self._media_type, "uri": self.uri, "mime_type": self.mimetype, "filename": self.filename}
            if self._size is not None:
                out["size"] = self._size
            return out
        file_digest = client._upload_file(self.data, self.mimetype) if client is not None else self.digest
        if self._media_type == "Content":
            return {"_type": "Content", "mimetype": self.mimetype, "filename": self.filename, "file_digest": file_digest}
        return {"_type": self._media_type, "file_digest": file_digest, "mime_type": self.mimetype, "filename": self.filename}

    def __repr__(self) -> str:
        if self.uri is not None:
            return f"{type(self).__name__}(uri={self.uri!r}, mimetype={self.mimetype!r})"
        return f"{type(self).__name__}(mimetype={self.mimetype!r}, filename={self.filename!r}, size={self.size})"


class Image(Content):
    """An image file (``weft.Image.from_path("cat.png")``, ``weft.Image.from_uri("s3://...")``)."""

    _media_type = "Image"
    _default_mimetype = "image/png"


class Audio(Content):
    """An audio file."""

    _media_type = "Audio"
    _default_mimetype = "audio/wav"


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
