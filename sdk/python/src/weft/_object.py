"""Versioned objects: Object, Dataset (with Table rows), Model and prompts."""

from __future__ import annotations

import logging
import string
from typing import TYPE_CHECKING, Any, ClassVar, Dict, Iterable, Iterator, List, Optional, Tuple

from pydantic import BaseModel, ConfigDict, PrivateAttr, field_validator

from ._op import BoundOp, Op
from ._refs import ObjectRef, OpRef, Ref, TableRef, is_ref_uri, parse_uri

if TYPE_CHECKING:  # pragma: no cover
    from ._client import WeftClient

logger = logging.getLogger("weft")

_REGISTRY: Dict[str, type] = {}


def registered_class(name: str) -> Optional[type]:
    return _REGISTRY.get(name)


class Object(BaseModel):
    """Base class for versioned objects.

    Subclass it (pydantic fields) and publish with ``weft.publish(obj)``.
    Methods decorated with ``@weft.op`` are traced, and published alongside
    the object as op refs.
    """

    model_config = ConfigDict(
        ignored_types=(Op,),
        arbitrary_types_allowed=True,
        protected_namespaces=(),
        extra="forbid",
    )

    name: Optional[str] = None
    description: Optional[str] = None

    _weft_ref: Optional[ObjectRef] = PrivateAttr(default=None)
    _weft_exclude: ClassVar[Tuple[str, ...]] = ()
    _weft_publish_ops: ClassVar[bool] = True

    def __init_subclass__(cls, **kwargs: Any) -> None:
        super().__init_subclass__(**kwargs)
        if cls.__module__ != "weft_loaded":  # classes rebuilt from the server stay unregistered
            _REGISTRY[cls.__name__] = cls

    @property
    def ref(self) -> Optional[ObjectRef]:
        return self._weft_ref

    def publish(self, name: Optional[str] = None) -> ObjectRef:
        from ._client import require_client

        return require_client().publish(self, name=name)

    @classmethod
    def _weft_from_val(cls, val: Dict[str, Any], client: "WeftClient", ref: Optional[ObjectRef]) -> Any:
        data: Dict[str, Any] = {}
        allow_extra = cls.model_config.get("extra") == "allow"
        known = set(cls.model_fields) | {f.alias for f in cls.model_fields.values() if f.alias}
        for k, v in val.items():
            if k in ("_type", "_class_name", "_bases"):
                continue
            if k not in known and not allow_extra:
                continue
            if isinstance(v, str) and is_ref_uri(v):
                try:
                    r = parse_uri(v)
                except ValueError:
                    data[k] = v
                    continue
                if isinstance(r, OpRef):
                    if k in cls.model_fields:
                        data[k] = v
                    continue
                data[k] = resolve_ref_value(r, client)
            elif isinstance(v, list):
                data[k] = [_resolve_list_item(x, client) for x in v]
            else:
                data[k] = v
        obj = cls(**data)
        obj._weft_ref = ref
        return obj


_REGISTRY["Object"] = Object


def _resolve_list_item(x: Any, client: "WeftClient") -> Any:
    if _is_obj_or_table_ref(x):
        return resolve_ref_value(parse_uri(x), client)
    if isinstance(x, str) and is_ref_uri(x):
        try:
            r = parse_uri(x)
        except ValueError:
            return x
        if isinstance(r, OpRef):
            try:
                return client.get(r)
            except Exception as e:
                logger.warning("weft: could not load op %s: %s", x, e)
    return x


def _is_obj_or_table_ref(x: Any) -> bool:
    if not (isinstance(x, str) and is_ref_uri(x)):
        return False
    try:
        r = parse_uri(x)
    except ValueError:
        return False
    return isinstance(r, TableRef) or (isinstance(r, ObjectRef) and not isinstance(r, OpRef))


def resolve_ref_value(r: Ref, client: "WeftClient") -> Any:
    if isinstance(r, TableRef) and not r.extra:
        return Table(ref=r, client=client)
    try:
        return client.get(r)
    except Exception as e:
        logger.warning("weft: could not resolve %s: %s", r.uri(), e)
        return r.uri()


class WeftObject(dict):
    """A loaded object whose class is not known locally: a dict with attribute access."""

    _weft_ref: Optional[ObjectRef] = None

    def __getattr__(self, item: str) -> Any:
        try:
            return self[item]
        except KeyError:
            raise AttributeError(item) from None

    @property
    def ref(self) -> Optional[ObjectRef]:
        return self._weft_ref

    def __repr__(self) -> str:
        return f"WeftObject({dict.__repr__(self)})"


class Table:
    """A list of rows stored server side as a table (used by ``Dataset.rows``)."""

    PAGE = 1000

    def __init__(self, rows: Optional[Iterable[Any]] = None, *, ref: Optional[TableRef] = None, client: Optional["WeftClient"] = None):
        self._rows: Optional[List[Any]] = list(rows) if rows is not None else None
        self.row_digests: Optional[List[str]] = None
        self.ref = ref
        self._client = client
        if self._rows is None and ref is None:
            self._rows = []

    @property
    def _weft_ref(self) -> Optional[TableRef]:
        return self.ref

    def _load(self) -> None:
        if self._rows is not None:
            return
        assert self.ref is not None
        client = self._client
        if client is None:
            from ._client import require_client

            client = require_client()
        rows: List[Any] = []
        digests: List[str] = []
        offset = 0
        while True:
            res = client._http.post(
                "/table/query",
                {"project_id": self.ref.project_id, "digest": self.ref.digest, "limit": self.PAGE, "offset": offset},
            )
            page = res.get("rows") or []
            for r in page:
                rows.append(r.get("val"))
                digests.append(r.get("digest"))
            if len(page) < self.PAGE:
                break
            offset += len(page)
        self._rows = rows
        self.row_digests = digests

    @property
    def rows(self) -> List[Any]:
        self._load()
        assert self._rows is not None
        return self._rows

    def append(self, row: Any) -> None:
        self.rows.append(row)
        self.ref = None
        self.row_digests = None

    def __iter__(self) -> Iterator[Any]:
        return iter(self.rows)

    def __len__(self) -> int:
        return len(self.rows)

    def __getitem__(self, i: Any) -> Any:
        return self.rows[i]

    def __eq__(self, other: Any) -> bool:
        if isinstance(other, Table):
            return self.rows == other.rows
        if isinstance(other, list):
            return self.rows == other
        return NotImplemented

    def __repr__(self) -> str:
        if self._rows is None:
            return f"Table(ref={self.ref.uri() if self.ref else None})"
        return f"Table({len(self._rows)} rows)"


class Dataset(Object):
    """A versioned collection of rows (dicts). Rows are stored as a table.

    >>> ds = weft.Dataset(name="qa", rows=[{"question": "2+2?", "expected": "4"}])
    >>> weft.publish(ds)
    """

    rows: Any = None

    @field_validator("rows", mode="before")
    @classmethod
    def _convert_rows(cls, v: Any) -> Any:
        if isinstance(v, Table):
            return v
        if v is None:
            return Table([])
        if isinstance(v, TableRef):
            return Table(ref=v)
        if isinstance(v, str) and is_ref_uri(v):
            r = parse_uri(v)
            if isinstance(r, TableRef):
                return Table(ref=r)
            raise ValueError("rows must be a table ref")
        if hasattr(v, "to_dict") and type(v).__module__.startswith("pandas"):
            return Table(v.to_dict(orient="records"))
        if isinstance(v, (str, bytes, dict)):
            raise ValueError("rows must be a list of dicts")
        return Table(list(v))

    def __iter__(self) -> Iterator[Any]:  # type: ignore[override]
        return iter(self.rows)

    def __len__(self) -> int:
        return len(self.rows)

    def __getitem__(self, i: int) -> Any:
        return self.rows[i]

    @classmethod
    def from_calls(cls, calls: Iterable[Any], name: Optional[str] = None) -> "Dataset":
        """Build a dataset from calls: one row ``{"inputs", "output"}`` per call (``self`` dropped)."""
        rows = []
        for c in calls:
            inputs = {k: v for k, v in (c.inputs or {}).items() if k != "self"}
            rows.append({"inputs": inputs, "output": c.output})
        return cls(name=name, rows=rows)

    @classmethod
    def from_pandas(cls, df: Any, name: Optional[str] = None) -> "Dataset":
        return cls(name=name, rows=df.to_dict(orient="records"))

    def to_pandas(self) -> Any:
        import pandas as pd  # type: ignore

        return pd.DataFrame(list(self.rows))

    def add_rows(self, rows: Iterable[Any]) -> "Dataset":
        """Return a new dataset version with ``rows`` appended.

        When this dataset is published, the rows are appended server side with
        ``/table/update`` and the new version (pointing at the new table
        digest) is published under the same name.
        """
        new_rows = list(rows)
        if self._weft_ref is None:
            return Dataset(name=self.name, description=self.description, rows=list(self.rows) + new_rows)
        from ._client import require_client

        client = require_client()
        table: Table = self.rows
        base = table.ref if table.ref is not None and table.ref.project_id == client.project_id else None
        if base is None:
            base = client._publish_table(table)
        tref, appended = client._append_table(base, new_rows)
        new_table = Table(list(table.rows) + new_rows, ref=tref, client=client)
        new_table.row_digests = list(table.row_digests or []) + appended
        new = Dataset(name=self.name, description=self.description, rows=new_table)
        client.publish(new, name=self._weft_ref.name)
        return new


class Model(Object):
    """Base class for models. Subclass and define ``@weft.op def predict(self, ...)``."""

    model_config = ConfigDict(extra="allow")

    def get_infer_method(self) -> Any:
        for attr in ("predict", "infer", "forward", "invoke"):
            fn = getattr(self, attr, None)
            if fn is not None and callable(fn):
                return fn
        raise TypeError(f"{type(self).__name__} has no predict method")


class _SafeFormat(dict):
    def __missing__(self, key: str) -> str:
        return "{" + key + "}"


def safe_format(template: str, values: Dict[str, Any]) -> str:
    """``str.format`` that leaves unknown ``{placeholders}`` untouched."""
    try:
        return string.Formatter().vformat(template, (), _SafeFormat(values))
    except (ValueError, IndexError, KeyError, AttributeError):
        out = template
        for k, v in values.items():
            out = out.replace("{" + k + "}", str(v))
        return out


class Prompt(Object):
    """Base class for prompts."""

    def format(self, **kwargs: Any) -> Any:  # pragma: no cover - abstract
        raise NotImplementedError


class StringPrompt(Prompt):
    """A text template: ``StringPrompt("Hello {name}").format(name="x")``."""

    content: str = ""

    def __init__(self, content: Optional[str] = None, **data: Any):
        if content is not None:
            data["content"] = content
        super().__init__(**data)

    def format(self, **kwargs: Any) -> str:
        return safe_format(self.content, kwargs)


class MessagesPrompt(Prompt):
    """A chat template: a list of ``{"role", "content"}`` messages with ``{placeholders}``."""

    messages: List[Dict[str, Any]] = []

    def __init__(self, messages: Optional[List[Dict[str, Any]]] = None, **data: Any):
        if messages is not None:
            data["messages"] = messages
        super().__init__(**data)

    def format(self, **kwargs: Any) -> List[Dict[str, Any]]:
        out = []
        for m in self.messages:
            m2 = dict(m)
            content = m2.get("content")
            if isinstance(content, str):
                m2["content"] = safe_format(content, kwargs)
            elif isinstance(content, list):
                parts = []
                for p in content:
                    if isinstance(p, dict) and isinstance(p.get("text"), str):
                        p = dict(p)
                        p["text"] = safe_format(p["text"], kwargs)
                    parts.append(p)
                m2["content"] = parts
            out.append(m2)
        return out


def find_infer_method(model: Any) -> Any:
    if isinstance(model, Model):
        return model.get_infer_method()
    if isinstance(model, (Op, BoundOp)) or callable(model):
        return model
    raise TypeError("model must be a weft.Model or a function")


class LLMStructuredCompletionModel(Model):
    """A server-runnable chat model: ``messages_template`` formatted with the row's columns.

    >>> m = weft.LLMStructuredCompletionModel(
    ...     name="qa-model", llm_model_id="gpt-4o-mini",
    ...     default_params={"messages_template": [{"role": "user", "content": "{question}"}],
    ...                     "temperature": 0, "response_format": "text"})

    Locally, ``predict`` calls the Weft server's ``/completions/create``.
    """

    _weft_publish_ops: ClassVar[bool] = False

    llm_model_id: str
    default_params: Dict[str, Any] = {}

    @Op
    def predict(self, **inputs: Any) -> Any:
        import json

        from ._client import require_client
        from ._context import get_current_call
        from ._usage import record_openai_usage

        client = require_client()
        params = dict(self.default_params or {})
        template = params.pop("messages_template", None) or [{"role": "user", "content": "{input}"}]
        response_format = params.pop("response_format", None)
        messages = MessagesPrompt(messages=template).format(**inputs)
        req: Dict[str, Any] = {"model": self.llm_model_id, "messages": messages}
        for k in ("temperature", "max_tokens", "top_p", "frequency_penalty", "presence_penalty", "stop", "n", "tools"):
            if k in params and params[k] is not None:
                req[k] = params[k]
        if isinstance(response_format, dict):
            req["response_format"] = response_format
        elif response_format and response_format != "text":
            req["response_format"] = {"type": response_format}
        resp = client.completions_create(req)
        record_openai_usage(resp, self.llm_model_id, get_current_call())
        content = ((resp.get("choices") or [{}])[0].get("message") or {}).get("content")
        rf = response_format.get("type") if isinstance(response_format, dict) else response_format
        if rf == "json_object" and isinstance(content, str):
            try:
                return json.loads(content)
            except ValueError:
                return content
        return content
