"""WeftClient: project-scoped client holding the HTTP transport and sender."""

from __future__ import annotations

import atexit
import datetime
import logging
import os
import platform
import sys
import re
import threading
import traceback
from typing import Any, Dict, Iterator, List, Optional, Union

from . import _context
from ._call import Call
from ._canonical import b64digest, digest, table_digest
from ._http import HTTPClient, WeftHTTPError, env_flag
from ._ids import uuid7
from ._object import Object, Table, WeftObject, registered_class, resolve_ref_value, _is_obj_or_table_ref
from ._op import BoundOp, Op
from ._redact import Redactor
from ._refs import CallRef, ObjectRef, OpRef, Ref, TableRef, is_ref_uri, parse_name_version, parse_uri, split_project
from ._sender import Sender
from ._serialize import object_val, to_json
from ._version import __version__

logger = logging.getLogger("weft")

DEFAULT_SERVER_URL = "http://localhost:6345"

_global_client: Optional["WeftClient"] = None
_global_lock = threading.Lock()
_atexit_registered = False
_UNSET: Any = object()
_NAME_RE = re.compile(r"[^A-Za-z0-9_.\-]+")


def now_iso() -> str:
    return datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%fZ")


def sanitize_name(name: str) -> str:
    out = _NAME_RE.sub("-", name.strip()).strip("-")
    return out[:128] or "object"


def format_exception(exc: BaseException) -> str:
    head = f"{type(exc).__name__}: {exc}"
    try:
        tb = "".join(traceback.format_exception(type(exc), exc, exc.__traceback__))
    except Exception:  # pragma: no cover
        tb = ""
    return head + ("\n\n" + tb.rstrip() if tb else "")


def _setting_bool(settings: Dict[str, Any], key: str, env: str, default: bool) -> bool:
    if key in settings and settings[key] is not None:
        return bool(settings[key])
    raw = os.environ.get(env)
    if raw is None or raw.strip() == "":
        return default
    return raw.strip().lower() in ("1", "true", "yes", "on")


def get_client() -> Optional["WeftClient"]:
    return _global_client


def require_client() -> "WeftClient":
    if _global_client is None:
        raise RuntimeError("weft is not initialised; call weft.init('<project>') first")
    return _global_client


def tracing_client() -> Optional["WeftClient"]:
    c = _global_client
    if c is None or c.disabled:
        return None
    return c


class CallsIter:
    """Lazy iterator over ``/calls/stream_query`` results."""

    def __init__(self, client: "WeftClient", body: Dict[str, Any]):
        self._client = client
        self._body = body

    def __iter__(self) -> Iterator[Call]:
        self._client.flush()
        for d in self._client._http.post_jsonl("/calls/stream_query", self._body):
            yield Call.from_server(d, self._client)

    def __len__(self) -> int:
        self._client.flush()
        body = {k: self._body[k] for k in ("project_id", "filter", "query", "search") if k in self._body}
        n = int(self._client._http.post("/calls/query_stats", body).get("count", 0))
        off = int(self._body.get("offset") or 0)
        n = max(0, n - off)
        lim = self._body.get("limit")
        return min(n, lim) if lim is not None else n

    def __getitem__(self, i: int) -> Call:
        return self.to_list()[i]

    def to_list(self) -> List[Call]:
        return list(self)


class WeftClient:
    def __init__(
        self,
        project: str,
        server_url: Optional[str] = None,
        api_key: Optional[str] = None,
        *,
        disabled: bool = False,
        timeout: float = 30.0,
        max_queue: int = 10_000,
        batch_size: int = 100,
        settings: Optional[Dict[str, Any]] = None,
    ):
        self.entity, self.project = split_project(project)
        self.server_url = (server_url or os.environ.get("WEFT_SERVER_URL") or DEFAULT_SERVER_URL).rstrip("/")
        self.api_key = api_key if api_key is not None else os.environ.get("WEFT_API_KEY")
        self.disabled = disabled
        self.settings: Dict[str, Any] = dict(settings or {})
        st = self.settings
        from ._cache import CachingHTTPClient, build_cache

        cache, cache_on = build_cache(st)
        self._http: HTTPClient = CachingHTTPClient(
            self.server_url, self.api_key, timeout=timeout, project_id=self.project_id, cache=cache, enabled=cache_on
        )
        self.capture_code = _setting_bool(st, "capture_code", "WEFT_CAPTURE_CODE", True)
        self.print_call_link = _setting_bool(st, "print_call_link", "WEFT_PRINT_CALL_LINK", True)
        self.global_attributes: Dict[str, Any] = dict(st.get("global_attributes") or {})
        wal_path = None
        if _setting_bool(st, "enable_disk_fallback", "WEFT_ENABLE_WAL", False):
            wal_dir = st.get("wal_dir") or os.environ.get("WEFT_WAL_DIR") or os.path.join(os.path.expanduser("~"), ".weft", "wal")
            key = re.sub(r"[^A-Za-z0-9_.-]+", "_", f"{self.server_url}_{self.project_id}").strip("_")
            wal_path = os.path.join(wal_dir, key + ".jsonl")
        self._sender = Sender(
            self._http,
            max_queue=int(st.get("max_queue_size", max_queue)),
            batch_size=int(st.get("batch_size", batch_size)),
            max_retries=int(st.get("retry_max_attempts", 3)),
            backoff=float(st.get("retry_backoff", 0.25)),
            max_backoff=float(st.get("retry_max_backoff", 4.0)),
            wal_path=wal_path,
        )
        self._uploaded: set = set()
        self._warned_digest = False
        if wal_path and not disabled:
            n = self._sender.replay_wal()
            if n:
                logger.info("weft: replaying %d unsent items from %s", n, wal_path)
        self._redact = Redactor(
            keys=self.settings.get("redact_keys"),
            pii=self.settings.get("redact_pii"),
            enabled=self.settings.get("redact_enabled"),
            settings=self.settings,
        )

    # ------------------------------------------------------------------ basics
    @property
    def project_id(self) -> str:
        return f"{self.entity}/{self.project}"

    def __repr__(self) -> str:
        return f"WeftClient(project={self.project_id!r}, server_url={self.server_url!r})"

    def flush(self, timeout: Optional[float] = None) -> bool:
        """Block until queued calls and objects have been sent."""
        return self._sender.flush(timeout)

    def finish(self, timeout: Optional[float] = 30.0) -> None:
        self._sender.stop(timeout)

    def server_info(self) -> Dict[str, Any]:
        return self._http.get("/server_info")

    # ------------------------------------------------------------------ server-response cache
    def cache_stats(self) -> Dict[str, int]:
        """Hits, misses and skips of the client-side cache of immutable reads."""
        return dict(getattr(self._http, "stats", {"hits": 0, "misses": 0, "skips": 0}))

    def clear_server_cache(self) -> None:
        """Empty the memory and on-disk cache of server responses."""
        cache = getattr(self._http, "cache", None)
        if cache is not None:
            cache.clear()

    # ------------------------------------------------------------------ calls
    def create_call(
        self,
        op: Union[Op, BoundOp, str],
        inputs: Dict[str, Any],
        parent: Any = _UNSET,
        display_name: Optional[str] = None,
        attributes: Optional[Dict[str, Any]] = None,
        **_: Any,
    ) -> Call:
        """Start a call explicitly (``finish_call`` ends it). Used by ``@op`` and loggers."""
        if isinstance(op, str):
            op = _synthetic_op(op)
        elif isinstance(op, BoundOp):
            op = op.op
        if parent is _UNSET:
            parent = _context.get_current_call()
        call_id = uuid7()
        trace_id = parent.trace_id if parent is not None else uuid7()
        thread_id = turn_id = None
        tctx = _context.current_thread_context()
        if tctx is not None:
            thread_id = tctx.thread_id
            if parent is None or parent.id == tctx.entry_parent_id or parent.thread_id != thread_id:
                turn_id = call_id
            else:
                turn_id = parent.turn_id
        elif parent is not None and parent.thread_id:
            thread_id, turn_id = parent.thread_id, parent.turn_id

        raw_inputs = dict(inputs)
        if op.postprocess_inputs is not None:
            try:
                raw_inputs = dict(op.postprocess_inputs(raw_inputs))
            except Exception as e:
                logger.warning("weft: postprocess_inputs failed for %s: %s", op.name, e)
        ser_inputs: Dict[str, Any] = {}
        for k, v in raw_inputs.items():
            ser_inputs[k] = self._serialize_self(v) if k == "self" else to_json(v, self)
        if self._redact.active:
            self_val = ser_inputs.pop("self", _UNSET)
            ser_inputs = self._redact(ser_inputs)
            if self_val is not _UNSET:
                ser_inputs = {"self": self_val, **ser_inputs}

        attrs: Dict[str, Any] = dict(self.global_attributes)
        attrs.update(_context.current_attributes())
        if attributes:
            attrs.update(attributes)
        attrs = to_json(attrs, self)
        weft_attrs = {"client_version": __version__, "source": "python-sdk", "python_version": platform.python_version()}
        if isinstance(attrs.get("weft"), dict):
            weft_attrs.update(attrs["weft"])
        kind, color = getattr(op, "kind", None), getattr(op, "color", None)
        if kind:
            weft_attrs["span_kind"] = kind
        if color:
            weft_attrs["color"] = color
        attrs["weft"] = weft_attrs
        if kind or color:
            weave_attrs = dict(attrs.get("weave") or {}) if isinstance(attrs.get("weave"), dict) else {}
            if kind:
                weave_attrs["kind"] = kind
            if color:
                weave_attrs["color"] = color
            attrs["weave"] = weave_attrs

        call = Call(
            id=call_id,
            project_id=self.project_id,
            op_name=self._op_ref(op).uri(),
            trace_id=trace_id,
            parent_id=parent.id if parent is not None else None,
            display_name=display_name,
            thread_id=thread_id,
            turn_id=turn_id,
            started_at=now_iso(),
            inputs=ser_inputs,
            attributes=attrs,
        )
        call._client = self
        call._raw_inputs = dict(inputs)
        if parent is not None:
            parent._child_count += 1
        if call.display_name is None and op.call_display_name is not None:
            try:
                dn = op.call_display_name
                call.display_name = dn(call) if callable(dn) else str(dn)
            except Exception as e:
                logger.warning("weft: call_display_name failed for %s: %s", op.name, e)
        start = {
            "project_id": call.project_id,
            "id": call.id,
            "op_name": call.op_name,
            "trace_id": call.trace_id,
            "parent_id": call.parent_id,
            "started_at": call.started_at,
            "attributes": call.attributes,
            "inputs": call.inputs,
        }
        for k in ("display_name", "thread_id", "turn_id"):
            if getattr(call, k) is not None:
                start[k] = getattr(call, k)
        self._sender.enqueue("call_start", start)
        if parent is None and self.print_call_link:
            print(f"weft: {call.ui_url}", file=sys.stderr)
        return call

    def finish_call(
        self,
        call: Call,
        output: Any = None,
        exception: Optional[BaseException] = None,
        op: Optional[Union[Op, str]] = None,
    ) -> None:
        if call._ended:
            return
        call._ended = True
        if isinstance(op, Op) and op.postprocess_output is not None and exception is None:
            try:
                output = op.postprocess_output(output)
            except Exception as e:
                logger.warning("weft: postprocess_output failed for %s: %s", op.name, e)
        call.ended_at = now_iso()
        call._raw_output = output
        try:
            call.output = self._redact(to_json(output, self))
        except Exception as e:  # pragma: no cover - to_json is defensive already
            call.output = repr(e)
        end: Dict[str, Any] = {"project_id": call.project_id, "id": call.id, "ended_at": call.ended_at, "output": call.output}
        if exception is not None:
            call.exception = format_exception(exception)
            end["exception"] = call.exception
        if call.summary:
            end["summary"] = to_json(call.summary, self)
        self._sender.enqueue("call_end", end)

    def _serialize_self(self, v: Any) -> Any:
        ref = getattr(v, "_weft_ref", None)
        if isinstance(ref, Ref):
            return ref.uri()
        if isinstance(v, Object):
            return to_json(v, self)
        try:
            return repr(v)
        except Exception:  # pragma: no cover
            return f"<{type(v).__name__}>"

    def get_calls(
        self,
        filter: Optional[Dict[str, Any]] = None,
        query: Optional[Dict[str, Any]] = None,
        sort_by: Optional[List[Dict[str, str]]] = None,
        limit: Optional[int] = None,
        offset: Optional[int] = None,
        *,
        search: Optional[str] = None,
        include_costs: bool = False,
        include_feedback: bool = False,
        columns: Optional[List[str]] = None,
        expand_columns: Optional[List[str]] = None,
    ) -> CallsIter:
        """Query calls. ``filter`` follows the CallsQuery filter (op_names, trace_ids, ...)."""
        body: Dict[str, Any] = {"project_id": self.project_id}
        if filter:
            body["filter"] = dict(filter)
        if query:
            body["query"] = query if "$expr" in query else {"$expr": query}
        if search:
            body["search"] = search
        if sort_by:
            body["sort_by"] = sort_by
        if limit is not None:
            body["limit"] = limit
        if offset is not None:
            body["offset"] = offset
        if include_costs:
            body["include_costs"] = True
        if include_feedback:
            body["include_feedback"] = True
        if columns:
            body["columns"] = columns
        if expand_columns:
            body["expand_columns"] = expand_columns
        return CallsIter(self, body)

    def get_call(self, call_id: str, *, include_costs: bool = False, include_feedback: bool = False) -> Optional[Call]:
        self.flush()
        res = self._http.post(
            "/call/read",
            {"project_id": self.project_id, "id": call_id, "include_costs": include_costs, "include_feedback": include_feedback},
        )
        d = res.get("call")
        return Call.from_server(d, self) if d else None

    def delete_calls(self, call_ids: List[str]) -> None:
        self.flush()
        self._http.post("/calls/delete", {"project_id": self.project_id, "call_ids": list(call_ids)})

    def get_feedback(self, query: Optional[Dict[str, Any]] = None, limit: Optional[int] = None) -> List[Dict[str, Any]]:
        self.flush()
        body: Dict[str, Any] = {"project_id": self.project_id}
        if query:
            body["query"] = query if "$expr" in query else {"$expr": query}
        if limit is not None:
            body["limit"] = limit
        return list(self._http.post("/feedback/query", body).get("result") or [])

    def get_evaluations(self) -> List[Any]:
        """Latest version of every Evaluation object in the project."""
        self.flush()
        res = self._http.post(
            "/objs/query",
            {"project_id": self.project_id, "filter": {"base_object_classes": ["Evaluation"], "latest_only": True}},
        )
        out = []
        for o in res.get("objs") or []:
            ref = ObjectRef(self.entity, self.project, o["object_id"], o["digest"])
            out.append(from_val(o.get("val"), self, ref))
        return out

    def get_saved_views(self, view_type: Optional[str] = None) -> List[Any]:
        """Latest version of every SavedView in the project (optionally one ``view_type``)."""
        from ._saved_view import SavedView

        self.flush()
        res = self._http.post(
            "/objs/query",
            {"project_id": self.project_id, "filter": {"base_object_classes": ["SavedView"], "latest_only": True}},
        )
        out = []
        for o in res.get("objs") or []:
            val = o.get("val") or {}
            if view_type and val.get("view_type") != view_type:
                continue
            ref = ObjectRef(self.entity, self.project, o["object_id"], o["digest"])
            v = from_val(val, self, ref)
            if not isinstance(v, SavedView):
                v = SavedView(view_type=val.get("view_type") or "traces", label=val.get("label"), name=val.get("name") or o["object_id"],
                              definition=val.get("definition") or {})
                v._weft_ref = ref
            out.append(v)
        return out

    def get_saved_view(self, name: str) -> Any:
        """Load a SavedView by name, ``name:version`` or ref."""
        from ._saved_view import SavedView

        return SavedView.load(name)

    def get_evaluation_calls(self, limit: Optional[int] = None) -> CallsIter:
        return self.get_calls(filter={"op_names": ["Evaluation.evaluate"]}, limit=limit)

    def whoami(self) -> Dict[str, Any]:
        """``GET /auth/whoami``: the user the API key acts as."""
        return self._http.get("/auth/whoami")

    # ------------------------------------------------------------------ object management
    def _obj_ref(self, obj_ref: Any) -> ObjectRef:
        if isinstance(obj_ref, ObjectRef):
            r = obj_ref
        elif isinstance(obj_ref, str):
            r = self.ref(obj_ref)  # type: ignore[assignment]
        else:
            r = getattr(obj_ref, "_weft_ref", None) or getattr(obj_ref, "ref", None)
        if not isinstance(r, ObjectRef):
            raise TypeError(f"expected an object ref, got {obj_ref!r}")
        if r.digest in ("latest",) or re.fullmatch(r"v\d+", r.digest or ""):
            res = self._http.post("/obj/read", {"project_id": r.project_id, "object_id": r.name, "digest": r.digest})
            r = type(r)(r.entity, r.project, r.name, res["obj"]["digest"])
        return r

    def delete_object_version(self, obj_ref: Any) -> int:
        """Delete one object version (ref, URI, ``"name:v1"`` or a published object)."""
        r = self._obj_ref(obj_ref)
        return int(self._http.post("/obj/delete", {"project_id": r.project_id, "object_id": r.name, "digests": [r.digest]}).get("num_deleted", 0))

    def delete_object_versions(self, obj: Any, digests: Optional[List[str]] = None) -> int:
        """Delete versions: ``(name, [digests or "v0"/"latest"...])``, or a single ref."""
        if digests is None:
            return self.delete_object_version(obj)
        name = obj if isinstance(obj, str) else self._obj_ref(obj).name
        return int(self._http.post("/obj/delete", {"project_id": self.project_id, "object_id": name, "digests": list(digests)}).get("num_deleted", 0))

    def delete_all_object_versions(self, name: str) -> int:
        """Delete every version of an object; returns the number deleted."""
        return int(self._http.post("/obj/delete", {"project_id": self.project_id, "object_id": name}).get("num_deleted", 0))

    def _with_fallback(self, attempts: List[Any]) -> Any:
        last: Optional[Exception] = None
        for fn in attempts:
            try:
                return fn()
            except WeftHTTPError as e:
                if e.status not in (404, 405, 501):
                    raise
                last = e
        assert last is not None
        raise last

    def set_aliases(self, obj_ref: Any, alias: Union[str, List[str]]) -> None:
        """Point alias(es) (e.g. ``"production"``) at an object version."""
        r = self._obj_ref(obj_ref)
        aliases = [alias] if isinstance(alias, str) else list(alias)
        if not aliases:
            return
        body = {"project_id": r.project_id, "digest": r.digest, "aliases": aliases}
        path = f"/objs/{r.name}/aliases"

        def legacy() -> Any:
            for a in aliases:
                self._http.post("/obj/alias/set", {"project_id": r.project_id, "object_id": r.name, "digest": r.digest, "alias": a})

        self._with_fallback([lambda: self._http.request("PUT", path, body), lambda: self._http.post(path, body), legacy])

    set_alias = set_aliases

    def remove_aliases(self, obj_ref: Any, alias: Union[str, List[str]]) -> None:
        """Remove alias(es) from an object (``obj_ref`` may be a name)."""
        name = obj_ref if isinstance(obj_ref, str) and not is_ref_uri(obj_ref) and ":" not in obj_ref else self._obj_ref(obj_ref).name
        aliases = [alias] if isinstance(alias, str) else list(alias)
        if not aliases:
            return
        body = {"project_id": self.project_id, "aliases": aliases}

        def legacy() -> Any:
            for a in aliases:
                self._http.post("/obj/alias/remove", {"project_id": self.project_id, "object_id": name, "alias": a})

        self._with_fallback([lambda: self._http.post(f"/objs/{name}/aliases/remove", body), legacy])

    remove_alias = remove_aliases

    def get_aliases(self, obj_ref: Any) -> List[str]:
        r = self._obj_ref(obj_ref)
        obj = self._http.post("/obj/read", {"project_id": r.project_id, "object_id": r.name, "digest": r.digest, "include_tags_and_aliases": True})["obj"]
        return list(obj.get("aliases") or [])

    def add_tags(self, obj_ref: Any, tags: List[str]) -> None:
        """Tag an object version."""
        r = self._obj_ref(obj_ref)
        path = f"/objs/{r.name}/versions/{r.digest}/tags"
        body = {"project_id": r.project_id, "tags": list(tags)}
        self._with_fallback([lambda: self._http.request("PUT", path, body), lambda: self._http.post(path, body)])

    def remove_tags(self, obj_ref: Any, tags: List[str]) -> None:
        r = self._obj_ref(obj_ref)
        self._http.post(f"/objs/{r.name}/versions/{r.digest}/tags/remove", {"project_id": r.project_id, "tags": list(tags)})

    def get_tags(self, obj_ref: Any) -> List[str]:
        r = self._obj_ref(obj_ref)
        obj = self._http.post("/obj/read", {"project_id": r.project_id, "object_id": r.name, "digest": r.digest, "include_tags_and_aliases": True})["obj"]
        return list(obj.get("tags") or [])

    def list_tags(self) -> List[str]:
        """All tags used in the project."""
        return list(self._http.request("GET", "/tags", query={"project_id": self.project_id}).get("tags") or [])

    def read_file(self, file_digest: str) -> bytes:
        """Raw bytes of an uploaded file (``Content``/media ``file_digest``)."""
        self.flush()
        return self._http.request_raw("POST", "/file/content", {"project_id": self.project_id, "digest": file_digest})

    # ------------------------------------------------------------------ evaluations (server side)
    def _uri_of(self, obj: Any) -> str:
        if isinstance(obj, str):
            return obj if is_ref_uri(obj) else self.ref(obj).uri()
        if isinstance(obj, Ref):
            return obj.uri()
        return self.publish(obj).uri()

    def evaluate_model(
        self,
        evaluation: Any,
        model: Any,
        *,
        wait: bool = True,
        timeout: float = 600.0,
        poll_interval: float = 0.5,
    ) -> Dict[str, Any]:
        """Run an evaluation server side (``/evaluate_model``).

        ``evaluation`` and ``model`` may be objects (published first), refs or
        names. The model must be an ``LLMStructuredCompletionModel`` and the
        scorers server-runnable. With ``wait`` the status is polled until the
        run finishes; returns ``{"call_id", "status", "summary"}``.
        """
        import time

        body = {"project_id": self.project_id, "evaluation_ref": self._uri_of(evaluation), "model_ref": self._uri_of(model)}
        call_id = self._http.post("/evaluate_model", body)["call_id"]
        result: Dict[str, Any] = {"call_id": call_id, "status": {"code": "running"}, "summary": None}
        if not wait:
            return result
        deadline = time.monotonic() + timeout
        while True:
            st = self._http.post("/evaluation_status", {"project_id": self.project_id, "call_id": call_id}).get("status") or {}
            result["status"] = st
            if st.get("code") in ("complete", "failed"):
                break
            if time.monotonic() > deadline:
                raise TimeoutError(f"evaluation {call_id} still running after {timeout}s")
            time.sleep(poll_interval)
        if st.get("code") == "failed":
            raise RuntimeError(f"server evaluation {call_id} failed: {st.get('error')}")
        call = self.get_call(call_id)
        result["summary"] = call.output if call is not None else None
        return result

    def evaluation_status(self, call_id: str) -> Dict[str, Any]:
        return self._http.post("/evaluation_status", {"project_id": self.project_id, "call_id": call_id}).get("status") or {}

    def compare_evaluations(self, call_ids: List[str]) -> Dict[str, Any]:
        """``/evaluations/compare`` for ``Evaluation.evaluate`` call ids."""
        self.flush()
        return self._http.post("/evaluations/compare", {"project_id": self.project_id, "evaluation_call_ids": list(call_ids)})

    def leaderboard(self, object_id: Any, digest: Optional[str] = None) -> Dict[str, Any]:
        """Compute a leaderboard (``/leaderboard/compute``) by name, ref or Leaderboard object."""
        if not isinstance(object_id, str) or is_ref_uri(object_id):
            r = parse_uri(object_id) if isinstance(object_id, str) else (object_id if isinstance(object_id, Ref) else getattr(object_id, "_weft_ref", None))
            if r is None:
                r = self.publish(object_id)
            object_id, digest = r.name, digest or r.digest
        body: Dict[str, Any] = {"project_id": self.project_id, "object_id": object_id}
        if digest:
            body["digest"] = digest
        self.flush()
        return self._http.post("/leaderboard/compute", body)

    # ------------------------------------------------------------------ costs
    def add_cost(
        self,
        llm_id: str,
        prompt_token_cost: float,
        completion_token_cost: float,
        *,
        effective_date: Optional[Union[str, datetime.datetime]] = None,
        prompt_token_cost_unit: str = "USD",
        completion_token_cost_unit: str = "USD",
        provider_id: Optional[str] = None,
    ) -> List[Any]:
        """Set a project-level per-token price for ``llm_id``."""
        cost: Dict[str, Any] = {
            "prompt_token_cost": prompt_token_cost,
            "completion_token_cost": completion_token_cost,
            "prompt_token_cost_unit": prompt_token_cost_unit,
            "completion_token_cost_unit": completion_token_cost_unit,
        }
        if effective_date is not None:
            cost["effective_date"] = to_json(effective_date)
        if provider_id is not None:
            cost["provider_id"] = provider_id
        res = self._http.post("/cost/create", {"project_id": self.project_id, "costs": {llm_id: cost}})
        return res.get("ids") or []

    def query_costs(self, llm_ids: Optional[List[str]] = None, include_defaults: bool = False) -> List[Dict[str, Any]]:
        body: Dict[str, Any] = {"project_id": self.project_id, "include_defaults": include_defaults}
        if llm_ids:
            body["query"] = {"$expr": {"$in": [{"$getField": "llm_id"}, [{"$literal": x} for x in llm_ids]]}}
        return list(self._http.post("/cost/query", body).get("results") or [])

    def purge_costs(self, ids: List[Any]) -> None:
        """Delete project prices by id (``query_costs`` rows or ``add_cost`` ``[id, llm_id]`` pairs)."""
        flat = [i[0] if isinstance(i, (list, tuple)) else (i.get("id") if isinstance(i, dict) else i) for i in ids]
        self._http.post("/cost/purge", {"project_id": self.project_id, "ids": flat})

    # ------------------------------------------------------------------ objects
    def _op_ref(self, op: Op) -> OpRef:
        key = (self.server_url, self.project_id)
        ref = op._refs.get(key)
        if ref is not None:
            return ref
        val = op.op_val(capture_code=self.capture_code)
        d = digest(val)
        name = sanitize_name(op.name)
        ref = OpRef(self.entity, self.project, name, d)
        op._refs[key] = ref

        def check(res: Any) -> None:
            got = (res or {}).get("digest")
            if got and got != d:
                self._digest_mismatch("op " + name, d, got)

        self._sender.enqueue("post", ("/obj/create", {"obj": {"project_id": self.project_id, "object_id": name, "val": val}}), check)
        return ref

    def _digest_mismatch(self, what: str, local: str, server: str) -> None:
        if not self._warned_digest:
            self._warned_digest = True
            logger.warning("weft: server digest for %s (%s) differs from local digest (%s); refs may not resolve", what, server, local)

    def _create_obj(self, name: str, val: Any, kind: str = "object") -> ObjectRef:
        name = sanitize_name(name)
        body = {"obj": {"project_id": self.project_id, "object_id": name, "val": val}}
        try:
            res = self._http.post("/obj/create", body)
            d = res["digest"]
        except (WeftHTTPError, KeyError) as e:
            logger.warning("weft: could not publish %s (%s); using a local digest", name, e)
            d = digest(val)
        cls = OpRef if kind == "op" else ObjectRef
        return cls(self.entity, self.project, name, d)

    def publish(self, obj: Any, name: Optional[str] = None) -> Any:
        """Publish an object (content-addressed) and return its ref."""
        if isinstance(obj, (Op, BoundOp)):
            op = obj if isinstance(obj, Op) else obj.op
            return self._op_ref(op)
        if isinstance(obj, Table):
            return self._publish_table(obj)
        if isinstance(obj, Object):
            oname = name or obj.name or type(obj).__name__
            val = object_val(obj, self, publish_nested=True)
            ref = self._create_obj(oname, val)
            obj._weft_ref = ref
            return ref
        if isinstance(obj, WeftObject):
            val = to_json(dict(obj), self, publish_nested=True)
            ref = self._create_obj(name or obj.get("name") or obj.get("_class_name") or "object", val)
            obj._weft_ref = ref
            return ref
        val = to_json(obj, self, publish_nested=True)
        return self._create_obj(name or type(obj).__name__, val)

    def _publish_table(self, table: Table) -> TableRef:
        if table.ref is not None and table.ref.project_id == self.project_id and table.row_digests is not None:
            return table.ref
        if table.ref is not None and table.ref.project_id == self.project_id and table._rows is None:
            return table.ref
        rows = [to_json(r, self) for r in table.rows]
        chunk = Table.PAGE
        try:
            res = self._http.post("/table/create", {"table": {"project_id": self.project_id, "rows": rows[:chunk]}})
            tdigest = res["digest"]
            row_digests = list(res.get("row_digests") or [])
            for i in range(chunk, len(rows), chunk):
                updates = [{"append": {"row": r}} for r in rows[i : i + chunk]]
                res = self._http.post("/table/update", {"project_id": self.project_id, "base_digest": tdigest, "updates": updates})
                tdigest = res["digest"]
                row_digests.extend(res.get("updated_row_digests") or [])
        except (WeftHTTPError, KeyError) as e:
            logger.warning("weft: could not publish table (%s); using local digests", e)
            row_digests = [digest(r) for r in rows]
            tdigest = table_digest(row_digests)
        table.ref = TableRef(self.entity, self.project, tdigest)
        table.row_digests = row_digests
        return table.ref

    def _append_table(self, base: TableRef, rows: List[Any]) -> "tuple[TableRef, List[str]]":
        """Append rows to a published table with /table/update; returns (new ref, appended row digests)."""
        tdigest = base.digest
        appended: List[str] = []
        json_rows = [to_json(r, self) for r in rows]
        for i in range(0, len(json_rows), Table.PAGE):
            updates = [{"append": {"row": r}} for r in json_rows[i : i + Table.PAGE]]
            res = self._http.post("/table/update", {"project_id": self.project_id, "base_digest": tdigest, "updates": updates})
            tdigest = res["digest"]
            appended.extend(res.get("updated_row_digests") or [])
        return TableRef(self.entity, self.project, tdigest), appended

    def _upload_file(self, data: bytes, mime: str) -> str:
        d = b64digest(data)
        if d in self._uploaded:
            return d
        self._uploaded.add(d)
        ext = mime.split("/")[-1].split(";")[0] or "bin"

        def check(res: Any) -> None:
            got = (res or {}).get("digest")
            if got and got != d:
                self._digest_mismatch("file", d, got)

        self._sender.enqueue("multipart", ("/file/create", {"project_id": self.project_id}, {"file": (f"file.{ext}", data, mime)}), check)
        return d

    def _read_obj_val(self, r: ObjectRef) -> Any:
        res = self._http.post("/obj/read", {"project_id": r.project_id, "object_id": r.name, "digest": r.digest})
        obj = res.get("obj")
        if obj is None:
            raise LookupError(f"object not found: {r.uri()}")
        return obj.get("val")

    def ref(self, s: Union[str, Ref]) -> Ref:
        if isinstance(s, Ref):
            return s
        if is_ref_uri(s):
            return parse_uri(s)
        name, version = parse_name_version(s)
        return ObjectRef(self.entity, self.project, name, version or "latest")

    def get(self, ref: Union[str, Ref]) -> Any:
        """Load the value a ref points to (objects, datasets, tables, calls, paths)."""
        r = self.ref(ref)
        self.flush()
        if isinstance(r, CallRef) and not r.extra:
            return self.get_call(r.id)
        if isinstance(r, TableRef) and not r.extra:
            return Table(ref=r, client=self)
        if isinstance(r, ObjectRef) and not r.extra:
            res = self._http.post("/obj/read", {"project_id": r.project_id, "object_id": r.name, "digest": r.digest})
            obj = res.get("obj")
            if obj is None:
                raise LookupError(f"object not found: {r.uri()}")
            cls = OpRef if obj.get("kind") == "op" else ObjectRef
            real = cls(r.entity, r.project, r.name, obj.get("digest", r.digest))
            return from_val(obj.get("val"), self, real)
        res = self._http.post("/refs/read_batch", {"refs": [r.uri()]})
        vals = res.get("vals") or [None]
        return from_val(vals[0], self, None)

    # ------------------------------------------------------------------ LLM
    def completions_create(self, inputs: Dict[str, Any], track_llm_call: bool = False) -> Dict[str, Any]:
        """Run a chat completion through the server's ``/completions/create``."""
        res = self._http.post(
            "/completions/create",
            {"project_id": self.project_id, "inputs": inputs, "track_llm_call": track_llm_call},
            timeout=300,
        )
        resp = res.get("response") or {}
        if isinstance(resp, dict) and resp.get("error"):
            raise RuntimeError(f"completion failed: {resp['error']}")
        return resp


def from_val(val: Any, client: WeftClient, ref: Optional[ObjectRef]) -> Any:
    from ._loader import load_dynamic_object, load_op

    if not isinstance(val, dict):
        return val
    if val.get("_type") == "Op":
        return load_op(val, ref, client)
    cls_name = val.get("_class_name")
    if not cls_name:
        return val
    cls = registered_class(cls_name)
    if cls is not None:
        try:
            return cls._weft_from_val(val, client, ref)  # type: ignore[attr-defined]
        except Exception as e:
            logger.debug("weft: could not rebuild %s: %s", cls_name, e)
    elif any(registered_class(b) is not None for b in (val.get("_bases") or []) if b not in ("Object", "BaseModel")):
        try:
            return load_dynamic_object(val, client, ref)
        except Exception as e:
            logger.warning("weft: could not rebuild %s as a runnable object: %s", cls_name, e)
    out = WeftObject()
    for k, v in val.items():
        out[k] = resolve_ref_value(parse_uri(v), client) if _is_obj_or_table_ref(v) else v
    out._weft_ref = ref
    return out


_SYNTHETIC: Dict[str, Op] = {}


def _synthetic_op(name: str, kind: Optional[str] = None) -> Op:
    key = f"{name}|{kind or ''}"
    op = _SYNTHETIC.get(key)
    if op is None:

        def _fn(*args: Any, **kwargs: Any) -> Any:  # pragma: no cover - never executed
            return None

        op = Op(_fn, name=name, code=f"# {name}: recorded by the weft SDK\n", kind=kind)
        _SYNTHETIC[key] = op
    return op


# ---------------------------------------------------------------------- init


def _atexit_flush() -> None:
    c = _global_client
    if c is not None:
        c._sender.max_retries = min(c._sender.max_retries, 1)
        c.flush(timeout=float(os.environ.get("WEFT_FLUSH_TIMEOUT", "10")))
        c._sender.spill_pending()


def init(
    project: Optional[str] = None,
    server_url: Optional[str] = None,
    api_key: Optional[str] = None,
    autopatch: bool = True,
    **kwargs: Any,
) -> WeftClient:
    """Initialise tracing for ``project`` (``"project"`` or ``"entity/project"``).

    Environment: ``WEFT_SERVER_URL`` (default http://localhost:6345),
    ``WEFT_API_KEY``, ``WEFT_PROJECT``, ``WEFT_DISABLED``.

    ``autopatch_settings`` (dict keyed by integration, e.g. ``{"openai":
    {"enabled": False}}``): per-integration ``enabled`` and ``op_settings``;
    see ``weft.integrations``. PII redaction settings (``redact_pii``,
    ``redact_pii_fields``, ``redact_pii_exclude_fields``, ``redact_pii_engine``)
    are described in ``weft._redact``.

    ``settings`` (dict): ``global_attributes``, ``print_call_link`` (default
    True, env ``WEFT_PRINT_CALL_LINK``), ``capture_code`` (default True, env
    ``WEFT_CAPTURE_CODE``), ``enable_disk_fallback`` (env ``WEFT_ENABLE_WAL``)
    with ``wal_dir`` (env ``WEFT_WAL_DIR``), ``max_queue_size``,
    ``retry_max_attempts``, ``retry_backoff``, ``retry_max_backoff``,
    ``redact_keys``, ``redact_pii``, and the cache of immutable server
    responses: ``use_server_cache`` (default True, env
    ``WEFT_USE_SERVER_CACHE``), ``server_cache_dir`` (default
    ``~/.cache/weft``, env ``WEFT_SERVER_CACHE_DIR``) and
    ``server_cache_size_limit`` (bytes, default 1 GB, env
    ``WEFT_SERVER_CACHE_SIZE_LIMIT``).
    """
    global _global_client, _atexit_registered
    project = project or os.environ.get("WEFT_PROJECT")
    if not project:
        raise ValueError("weft.init requires a project name (or WEFT_PROJECT)")
    disabled = env_flag("WEFT_DISABLED") or bool(kwargs.pop("disabled", False))
    # Per-integration settings (Weave's autopatch_settings), see integrations/_settings.py.
    autopatch_settings = kwargs.pop("autopatch_settings", None)
    client = WeftClient(project, server_url=server_url, api_key=api_key, disabled=disabled, **kwargs)
    with _global_lock:
        old = _global_client
        _global_client = client
        if not _atexit_registered:
            atexit.register(_atexit_flush)
            _atexit_registered = True
    if old is not None and old is not client:
        old.flush(timeout=5)
    if autopatch_settings is not None:
        from .integrations import configure as _configure_integrations

        _configure_integrations(autopatch_settings)
    if autopatch and not disabled:
        from .integrations import autopatch as _autopatch

        _autopatch()
    if not disabled:
        logger.info("weft: logging to %s (project %s)", client.server_url, client.project_id)
    return client


def finish() -> None:
    """Flush pending data and detach the global client."""
    global _global_client
    with _global_lock:
        c = _global_client
        _global_client = None
    if c is not None:
        c.finish()


def flush(timeout: Optional[float] = None) -> bool:
    c = _global_client
    return c.flush(timeout) if c is not None else True


def publish(obj: Any, name: Optional[str] = None) -> Any:
    return require_client().publish(obj, name=name)


def ref(s: Union[str, Ref]) -> Ref:
    """Build a ref from ``"name"``, ``"name:v3"``, ``"name:latest"``, ``"name:<digest>"`` or a URI."""
    return require_client().ref(s)


def get(s: Union[str, Ref]) -> Any:
    return require_client().get(s)

