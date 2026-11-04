"""SavedView: a saved traces/evaluations view with a fluent query API.

The stored object follows the Weft UI convention (see ``docs/weft/API.md``)::

    {"_class_name": "SavedView", "name": "errors", "view_type": "traces", "label": "Errors",
     "definition": {"filter": {...CallsFilter}, "query": {"$expr": ...}, "sort_by": [...],
                    "cols": ["inputs.question", ...], "search": "...", "columns": [...], "pin": {...},
                    "page_size": 50}}

>>> view = (weft.SavedView(label="Slow errors")
...         .filter_op("answer")
...         .add_filter("summary.weft.status", "equals", "error")
...         .add_filter("summary.weft.latency_ms", ">", 1000)
...         .sort_by("started_at", "desc")
...         .save())
>>> spans = view.spans(limit=20)
>>> views = weft.get_client().get_saved_views()
>>> same = weft.SavedView.load("Slow-errors")
"""

from __future__ import annotations

import datetime
from typing import TYPE_CHECKING, Any, Dict, List, Optional, Union

from pydantic import BaseModel

from ._object import Object

if TYPE_CHECKING:  # pragma: no cover
    from ._client import SpansIter, WeftClient

KNOWN_COLUMNS = [
    "id", "display_name", "op_name", "inputs", "output", "attributes", "summary", "started_at", "ended_at",
    "exception", "func_name", "parent_id", "project_id", "trace_id", "thread_id", "turn_id", "ui_url",
    "user_id", "run_id", "feedback",
]

COLUMN_ALIASES = {"Status": "summary.weft.status"}

# User operator → stored operator (Material-UI style, as the UI stores it).
OPERATOR_MAP = {
    "contains": "(string): contains",
    "does not contain": "(string): notContains",
    "equals": "(string): equals",
    "does not equal": "(string): notEquals",
    "in": "(string): in",
    "=": "(number): =",
    "≠": "(number): !=",
    "!=": "(number): !=",
    "<": "(number): <",
    "≤": "(number): <=",
    "<=": "(number): <=",
    ">": "(number): >",
    "≥": "(number): >=",
    ">=": "(number): >=",
    "is": "(bool): is",
    "after": "(date): after",
    "before": "(date): before",
    "is empty": "(any): isEmpty",
    "is not empty": "(any): isNotEmpty",
}
VALUELESS_OPERATORS = {"(any): isEmpty", "(any): isNotEmpty"}
FIELDS_NO_FLOAT_CONVERT = {"summary.weft.latency_ms", "started_at", "ended_at"}
DEFAULT_PIN = {"left": ["CustomCheckbox", "op_name"], "right": []}
CALLS_FILTER_KEYS = {
    "names", "ids", "session_ids", "input_refs", "output_refs", "parent_ids", "trace_ids", "trace_roots_only",
    "user_ids", "run_ids", "turn_ids",
    # the earlier names of names, ids and session_ids
    "op_names", "call_ids", "thread_ids",
}
# The earlier filter names (views saved before the rename) → Weft's span filter names.
_SPAN_FILTER_NAMES = {"op_names": "names", "call_ids": "ids", "thread_ids": "session_ids"}


def _span_filter(f: Optional[Dict[str, Any]]) -> Dict[str, Any]:
    """A view filter with Weft's span filter names (a current name wins over its earlier one)."""
    out: Dict[str, Any] = {}
    for k, v in (f or {}).items():
        n = _SPAN_FILTER_NAMES.get(k, k)
        if n not in out or n == k:
            out[n] = v
    return out


class Filter(BaseModel):
    """One filter of a view: ``field operator value`` (operator in its stored form)."""

    field: str
    operator: str
    value: Any = None


class QueryTranslationException(Exception):
    """A view query that is not a list of simple filters."""


def to_seconds(value: Any) -> Optional[float]:
    """Seconds since the epoch from a number, numeric string, ISO date or datetime."""
    if value is None or value == "":
        return None
    if isinstance(value, (int, float)) and not isinstance(value, bool):
        return float(value)
    if isinstance(value, datetime.datetime):
        dt = value if value.tzinfo else value.replace(tzinfo=datetime.timezone.utc)
        return dt.timestamp()
    try:
        return float(value)
    except (TypeError, ValueError):
        pass
    try:
        dt = datetime.datetime.fromisoformat(str(value).replace("Z", "+00:00"))
    except ValueError:
        return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=datetime.timezone.utc)
    return dt.timestamp()


def _field_expr(field: str) -> Dict[str, Any]:
    if field in FIELDS_NO_FLOAT_CONVERT:
        return {"$getField": field}
    return {"$convert": {"input": {"$getField": field}, "to": "double"}}


def filter_to_clause(item: Filter) -> Dict[str, Any]:
    """Translate one filter into a calls-query operation."""
    op, f, v = item.operator, item.field, item.value
    get = {"$getField": f}
    if op == "(any): isEmpty":
        return {"$eq": [get, {"$literal": ""}]}
    if op == "(any): isNotEmpty":
        return {"$not": [{"$eq": [get, {"$literal": ""}]}]}
    if op == "(string): contains":
        return {"$contains": {"input": get, "substr": {"$literal": v}}}
    if op == "(string): notContains":
        return {"$not": [{"$contains": {"input": get, "substr": {"$literal": v}}}]}
    if op == "(string): equals":
        return {"$eq": [get, {"$literal": v}]}
    if op == "(string): notEquals":
        return {"$not": [{"$eq": [get, {"$literal": v}]}]}
    if op == "(string): in":
        values = v if isinstance(v, list) else [s.strip() for s in str(v).split(",")]
        return {"$or": [{"$eq": [get, {"$literal": x}]} for x in values]}
    if op.startswith("(number): "):
        num = float(v)
        lhs = _field_expr(f)
        sym = op[len("(number): "):]
        if sym == "=":
            return {"$eq": [lhs, {"$literal": num}]}
        if sym == "!=":
            return {"$not": [{"$eq": [lhs, {"$literal": num}]}]}
        if sym == ">":
            return {"$gt": [lhs, {"$literal": num}]}
        if sym == ">=":
            return {"$gte": [lhs, {"$literal": num}]}
        if sym == "<":
            return {"$not": [{"$gte": [lhs, {"$literal": num}]}]}
        if sym == "<=":
            return {"$not": [{"$gt": [lhs, {"$literal": num}]}]}
    if op == "(bool): is":
        return {"$eq": [get, {"$literal": str(v)}]}
    if op in ("(date): after", "(date): before"):
        secs = to_seconds(v)
        if secs is None:
            raise ValueError(f"not a date or timestamp: {v!r}")
        gt = {"$gt": [get, {"$literal": secs}]}
        return gt if op == "(date): after" else {"$not": [gt]}
    raise ValueError(f"cannot translate filter operator {op!r}")


def filters_to_query(filters: Optional[List[Filter]]) -> Optional[Dict[str, Any]]:
    if not filters:
        return None
    return {"$expr": {"$and": [filter_to_clause(f) for f in filters]}}


def _field_of(operand: Any) -> Optional[str]:
    if isinstance(operand, dict):
        if "$getField" in operand:
            return operand["$getField"]
        conv = operand.get("$convert")
        if isinstance(conv, dict) and isinstance(conv.get("input"), dict):
            return conv["input"].get("$getField")
    return None


def _literal_of(operand: Any) -> Any:
    if isinstance(operand, dict) and "$literal" in operand:
        return operand["$literal"]
    raise QueryTranslationException(f"not a literal: {operand!r}")


def _clause_to_filter(c: Dict[str, Any]) -> Filter:
    if "$eq" in c:
        a, b = c["$eq"]
        field, val = _field_of(a), _literal_of(b)
        if field is None:
            raise QueryTranslationException(str(c))
        if isinstance(a, dict) and "$convert" in a:
            return Filter(field=field, operator="(number): =", value=val)
        if val == "":
            return Filter(field=field, operator="(any): isEmpty")
        if isinstance(val, str) and val in ("True", "False"):
            return Filter(field=field, operator="(bool): is", value=val == "True")
        return Filter(field=field, operator="(string): equals", value=val)
    if "$contains" in c:
        field = _field_of(c["$contains"].get("input"))
        if field is None:
            raise QueryTranslationException(str(c))
        return Filter(field=field, operator="(string): contains", value=_literal_of(c["$contains"].get("substr")))
    for key, num_op in (("$gt", "(number): >"), ("$gte", "(number): >=")):
        if key in c:
            a, b = c[key]
            field, val = _field_of(a), _literal_of(b)
            if field is None:
                raise QueryTranslationException(str(c))
            if key == "$gt" and field in ("started_at", "ended_at"):
                return Filter(field=field, operator="(date): after", value=val)
            return Filter(field=field, operator=num_op, value=val)
    if "$not" in c:
        inner = c["$not"][0]
        f = _clause_to_filter(inner)
        flip = {
            "(any): isEmpty": "(any): isNotEmpty",
            "(string): contains": "(string): notContains",
            "(string): equals": "(string): notEquals",
            "(number): =": "(number): !=",
            "(number): >=": "(number): <",
            "(number): >": "(number): <=",
            "(date): after": "(date): before",
        }
        if f.operator not in flip:
            raise QueryTranslationException(str(c))
        return Filter(field=f.field, operator=flip[f.operator], value=f.value)
    if "$or" in c:
        fs = [_clause_to_filter(x) for x in c["$or"]]
        if fs and all(x.operator == "(string): equals" and x.field == fs[0].field for x in fs):
            return Filter(field=fs[0].field, operator="(string): in", value=[x.value for x in fs])
    raise QueryTranslationException(str(c))


def query_to_filters(query: Optional[Dict[str, Any]]) -> Optional[List[Filter]]:
    """Translate a view query back into filters (raises QueryTranslationException if it is not simple)."""
    if not query:
        return None
    expr = query.get("$expr", query)
    clauses = expr["$and"] if isinstance(expr, dict) and "$and" in expr else [expr]
    return [_clause_to_filter(c) for c in clauses]


def _path_parts(path: Union[str, List[Any]]) -> List[Any]:
    if isinstance(path, list):
        return list(path)
    return [int(p) if p.isdigit() else p for p in str(path).split(".")]


def _path_str(path: Union[str, List[Any]]) -> str:
    return path if isinstance(path, str) else ".".join(str(p) for p in path)


def _check_column(path: str) -> None:
    if _path_parts(path)[0] not in KNOWN_COLUMNS:
        raise ValueError(f"unknown column {path!r}: the first path segment must be one of KNOWN_COLUMNS")


class SavedView(Object):
    """A saved traces/evaluations view: filters, query, sort, columns. Build it fluently, ``save()`` it,
    ``load()`` it, and run it with ``spans()`` (or ``to_spans_query()`` for the raw request)."""

    view_type: str = "traces"
    label: Optional[str] = "SavedView"
    definition: Dict[str, Any] = {}

    def __init__(self, view_type: Optional[str] = None, label: Optional[str] = None, **data: Any):
        if view_type is not None:
            data["view_type"] = view_type
        if label is not None:
            data["label"] = label
        super().__init__(**data)
        self.definition = dict(self.definition or {})

    # ---- helpers ------------------------------------------------------------------
    def _client(self) -> "WeftClient":
        from ._client import require_client

        return require_client()

    def _touch(self) -> None:
        # The UI restores ``ui_params`` in preference to the definition, so they go stale on edits.
        self.definition.pop("ui_params", None)

    @property
    def entity(self) -> Optional[str]:
        if self._weft_ref is not None:
            return self._weft_ref.entity
        from ._client import get_client

        c = get_client()
        return c.entity if c else None

    @property
    def project(self) -> Optional[str]:
        if self._weft_ref is not None:
            return self._weft_ref.project
        from ._client import get_client

        c = get_client()
        return c.project if c else None

    # ---- filters ----------------------------------------------------------------------
    def rename(self, label: str) -> "SavedView":
        self.label = label
        return self

    @property
    def filters(self) -> List[Filter]:
        """The query as filters (empty when there is none)."""
        return query_to_filters(self.definition.get("query")) or []

    def add_filter(self, field: str, operator: str, value: Any = None) -> "SavedView":
        """Add ``field operator value``; operators: contains, does not contain, equals, does not equal,
        in, =, !=, <, <=, >, >=, is, after, before, is empty, is not empty."""
        field = COLUMN_ALIASES.get(field, field)
        _check_column(field)
        op = OPERATOR_MAP.get(operator, operator if operator in OPERATOR_MAP.values() else None)
        if not op:
            raise ValueError(f"unknown filter operator {operator!r}")
        if op in VALUELESS_OPERATORS and value is not None:
            raise ValueError(f"filter operator {operator!r} takes no value")
        if op not in VALUELESS_OPERATORS and value is None:
            raise ValueError(f"filter operator {operator!r} needs a value")
        new = Filter(field=field, operator=op, value=value)
        filter_to_clause(new)  # validate now
        filters = self.filters
        if new not in filters:
            filters.append(new)
            self.definition["query"] = filters_to_query(filters)
            self._touch()
        return self

    def remove_filter(self, index_or_field: Union[int, str]) -> "SavedView":
        filters = self.filters
        if not filters:
            return self
        if isinstance(index_or_field, int):
            filters.pop(index_or_field)
        else:
            filters = [f for f in filters if f.field != index_or_field]
        q = filters_to_query(filters)
        if q:
            self.definition["query"] = q
        else:
            self.definition.pop("query", None)
        self._touch()
        return self

    def remove_filters(self) -> "SavedView":
        self.definition.pop("query", None)
        self._touch()
        return self

    def filter_op(self, op_name: Optional[str]) -> "SavedView":
        """Show only spans of one function (a name, ``name:version`` or a full function ref); None clears it."""
        flt = _span_filter(self.definition.get("filter"))
        if op_name is None:
            flt.pop("names", None)
        else:
            from ._refs import is_ref_uri

            uri = op_name
            if not is_ref_uri(op_name):
                if not self.entity:
                    raise ValueError("Must specify an op ref when no project is known")
                uri = f"weft:///{self.entity}/{self.project}/function/{op_name}" + ("" if ":" in op_name else ":*")
            flt["names"] = [uri]
        if flt:
            self.definition["filter"] = flt
        else:
            self.definition.pop("filter", None)
        self._touch()
        return self

    def set_filter(self, **calls_filter: Any) -> "SavedView":
        """Set span filter fields (``trace_roots_only``, ``session_ids``, ``user_ids``, ...).

        The earlier names (``op_names``, ``call_ids``, ``thread_ids``) are stored under Weft's."""
        unknown = set(calls_filter) - CALLS_FILTER_KEYS
        if unknown:
            raise ValueError(f"unknown span filter fields: {sorted(unknown)}")
        flt = _span_filter(self.definition.get("filter"))
        for key, v in calls_filter.items():
            k = _SPAN_FILTER_NAMES.get(key, key)
            if v is None:
                flt.pop(k, None)
            else:
                flt[k] = v
        self.definition["filter"] = flt
        self._touch()
        return self

    def search(self, text: Optional[str]) -> "SavedView":
        """Free-text search over op name, display name, inputs, output and exception."""
        if text:
            self.definition["search"] = text
        else:
            self.definition.pop("search", None)
        self._touch()
        return self

    # ---- sorting and paging ---------------------------------------------------------------
    def add_sort(self, field: str, direction: str) -> "SavedView":
        if direction not in ("asc", "desc"):
            raise ValueError("direction must be 'asc' or 'desc'")
        self.definition.setdefault("sort_by", [])
        self.definition["sort_by"] = list(self.definition["sort_by"]) + [{"field": field, "direction": direction}]
        self._touch()
        return self

    def sort_by(self, field: str, direction: str) -> "SavedView":
        self.definition["sort_by"] = []
        return self.add_sort(field, direction)

    def page_size(self, page_size: int) -> "SavedView":
        self.definition["page_size"] = int(page_size)
        self._touch()
        return self

    # ---- columns ----------------------------------------------------------------------------
    def _cols(self) -> List[str]:
        return list(self.definition.get("cols") or [])

    def show_column(self, col_name: str) -> "SavedView":
        cols = self._cols()
        if col_name not in cols:
            cols.append(col_name)
        self.definition["cols"] = cols
        self._touch()
        return self

    def hide_column(self, col_name: str) -> "SavedView":
        self.definition["cols"] = [c for c in self._cols() if c != col_name]
        self._touch()
        return self

    def add_column(self, path: Union[str, List[Any]], label: Optional[str] = None) -> "SavedView":
        idx = len(self.definition.get("columns") or [])
        return self.insert_column(idx, path, label)

    def add_columns(self, *columns: str) -> "SavedView":
        for c in columns:
            self.add_column(c)
        return self

    def insert_column(self, idx: int, path: Union[str, List[Any]], label: Optional[str] = None) -> "SavedView":
        if isinstance(path, str) and path in COLUMN_ALIASES:
            label = label if label is not None else path
            path = COLUMN_ALIASES[path]
        p = _path_str(path)
        _check_column(p)
        columns = list(self.definition.get("columns") or [])
        columns.insert(idx, {"path": _path_parts(path), "label": label})
        self.definition["columns"] = columns
        cols = self._cols()
        if p not in cols:
            cols.insert(min(idx, len(cols)), p)
        self.definition["cols"] = cols
        self._touch()
        return self

    def set_columns(self, *columns: str) -> "SavedView":
        self.definition["columns"] = []
        self.definition["cols"] = []
        return self.add_columns(*columns)

    def column_index(self, path: Union[int, str, List[Any]]) -> int:
        if isinstance(path, int):
            if path < 0 or path >= len(self.get_table_columns()):
                raise ValueError(f"no column at index {path}")
            return path
        p = _path_str(COLUMN_ALIASES.get(path, path) if isinstance(path, str) else path)
        for i, col in enumerate(self.get_table_columns()):
            if col["path"] == p:
                return i
        raise ValueError(f"the view has no column {p!r}")

    def rename_column(self, path: Union[int, str, List[Any]], label: str) -> "SavedView":
        i = self.column_index(path)
        columns = list(self.definition.get("columns") or [])
        if i >= len(columns):
            raise ValueError("only explicitly added columns can be renamed")
        columns[i] = {**columns[i], "label": label}
        self.definition["columns"] = columns
        return self

    def remove_column(self, path: Union[int, str, List[Any]]) -> "SavedView":
        columns = list(self.definition.get("columns") or [])
        if isinstance(path, int):
            if 0 <= path < len(columns):
                p = _path_str(columns.pop(path)["path"])
                self.definition["cols"] = [c for c in self._cols() if c != p]
        else:
            p = _path_str(COLUMN_ALIASES.get(path, path) if isinstance(path, str) else path)
            columns = [c for c in columns if _path_str(c.get("path") or []) != p]
            self.definition["cols"] = [c for c in self._cols() if c != p]
        if columns:
            self.definition["columns"] = columns
        else:
            self.definition.pop("columns", None)
        self._touch()
        return self

    def remove_columns(self, *columns: str) -> "SavedView":
        if not columns:
            self.definition.pop("columns", None)
            self.definition.pop("cols", None)
            self._touch()
            return self
        for c in columns:
            self.remove_column(c)
        return self

    def _pin(self) -> Dict[str, List[str]]:
        pin = self.definition.get("pin") or {"left": list(DEFAULT_PIN["left"]), "right": []}
        return {"left": list(pin.get("left") or []), "right": list(pin.get("right") or [])}

    def pin_column_left(self, col_name: str) -> "SavedView":
        pin = self._pin()
        if col_name in pin["right"]:
            pin["right"].remove(col_name)
        if col_name not in pin["left"]:
            pin["left"].append(col_name)
        self.definition["pin"] = pin
        return self

    def pin_column_right(self, col_name: str) -> "SavedView":
        pin = self._pin()
        if col_name in pin["left"]:
            pin["left"].remove(col_name)
        if col_name not in pin["right"]:
            pin["right"].append(col_name)
        self.definition["pin"] = pin
        return self

    def unpin_column(self, col_name: str) -> "SavedView":
        pin = self._pin()
        for side in ("left", "right"):
            if col_name in pin[side]:
                pin[side].remove(col_name)
        self.definition["pin"] = pin
        return self

    def get_table_columns(self) -> List[Dict[str, Any]]:
        """``[{"path", "label"}]`` of the grid: explicit columns, else the defaults plus visible ``cols``."""
        if self.definition.get("columns"):
            return [{"path": _path_str(c.get("path") or []), "label": c.get("label") or _path_str(c.get("path") or [])}
                    for c in self.definition["columns"]]
        paths = ["id", "summary.weft.trace_name", "summary.weft.status", "started_at"]
        for c in self._cols():
            if c not in paths and _path_parts(c)[0] in KNOWN_COLUMNS:
                paths.append(c)
        pin = self._pin()
        for p in pin["right"]:
            if p in paths:
                paths.remove(p)
                paths.append(p)
        for p in reversed(pin["left"]):
            if p in paths:
                paths.remove(p)
                paths.insert(0, p)
        headers = {"id": "ID", "summary.weft.trace_name": "Trace", "summary.weft.status": "Status", "started_at": "Called"}
        return [{"path": p, "label": headers.get(p, p)} for p in paths]

    # ---- persistence -----------------------------------------------------------------------
    def save(self) -> "SavedView":
        """Save the view (named after the label, or ``<view_type>_<timestamp>``)."""
        if not self.name:
            from ._client import sanitize_name

            base = self.label if self.label and self.label != "SavedView" else None
            stamp = datetime.datetime.now().strftime("%Y-%m-%d_%H-%M-%S-%f")
            self.name = sanitize_name(base) if base else f"{self.view_type}_{stamp}"
        Object.save(self, self.name)
        return self

    @classmethod
    def load(cls, ref: Any) -> "SavedView":
        """Load a view by name, ``name:version``, ref or URI."""
        from ._client import require_client

        obj = require_client().get(ref)
        if isinstance(obj, SavedView):
            return obj
        if isinstance(obj, dict) and obj.get("definition") is not None:
            v = cls(view_type=obj.get("view_type", "traces"), label=obj.get("label"), name=obj.get("name"),
                    description=obj.get("description"), definition=obj.get("definition") or {})
            v._weft_ref = getattr(obj, "_weft_ref", None)
            return v
        raise TypeError(f"{ref} is not a SavedView")

    def ui_url(self) -> Optional[str]:
        """Link to the view's results in the UI (once saved)."""
        if self._weft_ref is None:
            return None
        from ._client import get_client

        c = get_client()
        base = c.server_url if c else ""
        page = "evaluations" if self.view_type == "evaluations" else "traces"
        return f"{base}/{self._weft_ref.entity}/{self._weft_ref.project}/{page}?view={self._weft_ref.name}"

    # ---- applying the view -----------------------------------------------------------------
    def to_spans_query(
        self,
        *,
        limit: Optional[int] = None,
        offset: Optional[int] = None,
        include_costs: bool = False,
        include_feedback: bool = False,
        all_columns: bool = False,
    ) -> Dict[str, Any]:
        """The ``POST /api/v1/spans/query`` body this view stands for (``project_id`` included when known).

        Views saved before the rename name filter fields the earlier way
        (``op_names``, ``thread_ids``, ...); they are given Weft's span names here."""
        d = self.definition
        body: Dict[str, Any] = {}
        if self.entity and self.project:
            body["project_id"] = f"{self.entity}/{self.project}"
        flt = {k: v for k, v in _span_filter(d.get("filter")).items() if k in CALLS_FILTER_KEYS and v is not None}
        if self.view_type == "evaluations":
            flt["names"] = ["Evaluation.evaluate"]
        if flt:
            body["filter"] = flt
        if d.get("query"):
            q = d["query"]
            body["query"] = q if "$expr" in q else {"$expr": q}
        if d.get("search"):
            body["search"] = d["search"]
        if d.get("sort_by"):
            body["sort_by"] = list(d["sort_by"])
        if d.get("expand_columns"):
            body["expand_columns"] = list(d["expand_columns"])
        if not all_columns and d.get("columns"):
            body["columns"] = [c["path"] for c in self.get_table_columns()]
        if limit is not None:
            body["limit"] = limit
        if offset is not None:
            body["offset"] = offset
        if include_costs:
            body["include_costs"] = True
        if include_feedback:
            body["include_feedback"] = True
        return body

    def to_calls_query(self, **kwargs: Any) -> Dict[str, Any]:
        """Deprecated: use ``to_spans_query()``."""
        from ._deprecation import deprecated_call

        deprecated_call("SavedView.to_calls_query()", "SavedView.to_spans_query()")
        return self.to_spans_query(**kwargs)

    def get_calls(self, *args: Any, **kwargs: Any) -> "SpansIter":
        """Deprecated: use ``spans()``."""
        from ._deprecation import deprecated_call

        deprecated_call("SavedView.get_calls()", "SavedView.spans()")
        return self.spans(*args, **kwargs)

    def spans(
        self,
        limit: Optional[int] = None,
        offset: Optional[int] = None,
        include_costs: bool = False,
        include_feedback: bool = False,
        all_columns: bool = False,
    ) -> "SpansIter":
        """Spans matching the view's filter, query, search and sort."""
        body = self.to_spans_query(
            limit=limit, offset=offset, include_costs=include_costs, include_feedback=include_feedback, all_columns=all_columns
        )
        client = self._client()
        return client.spans(
            filter=body.get("filter"),
            query=body.get("query"),
            sort_by=body.get("sort_by"),
            limit=limit,
            offset=offset,
            search=body.get("search"),
            include_costs=include_costs,
            include_feedback=include_feedback,
            columns=body.get("columns"),
            expand_columns=body.get("expand_columns"),
        )


__all__ = ["SavedView", "Filter", "QueryTranslationException", "filter_to_clause", "filters_to_query", "query_to_filters", "to_seconds"]
