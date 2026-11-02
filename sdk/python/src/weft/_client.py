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


def _env_raw(env: str) -> Optional[str]:
    raw = os.environ.get(env)
    return raw.strip() if raw is not None and raw.strip() != "" else None


def _setting_num(settings: Dict[str, Any], key: str, env: str, default: Any, cast: Any = float, explicit: Any = None) -> Any:
    """``settings[key]``, else an explicit argument, else ``$env``, else ``default`` (bad env values warn)."""
    if key in settings and settings[key] is not None:
        return cast(settings[key])
    if explicit is not None:
        return cast(explicit)
    raw = _env_raw(env)
    if raw is None:
        return default
    try:
        return cast(raw)
    except (TypeError, ValueError):
        logger.warning("weft: ignoring %s=%r (not a number)", env, raw)
        return default


_LOG_LEVELS = ("DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL")


def _apply_log_level(settings: Dict[str, Any]) -> Optional[str]:
    """Set the ``weft`` logger level from ``settings["log_level"]`` or ``WEFT_LOG_LEVEL``."""
    raw = settings.get("log_level") or _env_raw("WEFT_LOG_LEVEL")
    if not raw:
        return None
    level = str(raw).strip().upper()
    if level == "WARN":
        level = "WARNING"
    if level not in _LOG_LEVELS:
        logger.warning("weft: ignoring log level %r (use one of %s)", raw, ", ".join(_LOG_LEVELS))
        return None
    lg = logging.getLogger("weft")
    lg.setLevel(level)
    if not lg.handlers and not logging.getLogger().handlers:
        h = logging.StreamHandler()
        h.setFormatter(logging.Formatter("%(levelname)s %(name)s: %(message)s"))
        lg.addHandler(h)
    return level


def _wandb_run_context() -> Optional["tuple[str, Optional[int]]"]:
    """(run id, step) of an active ``wandb`` run when wandb is already imported (as Weave does)."""
    wandb = sys.modules.get("wandb")
    run = getattr(wandb, "run", None) if wandb is not None else None
    if run is None:
        return None
    run_id = getattr(run, "id", None)
    if not run_id:
        return None
    try:
        step: Optional[int] = int(run.step)
    except Exception:
        step = None
    return str(run_id), step


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

    def to_records(self) -> List[Dict[str, Any]]:
        """Every matching call as a plain dict (``Call.to_dict()``)."""
        return [c.to_dict() for c in self]

    def to_pandas(self, flatten: bool = False) -> Any:
        """The matching calls as a pandas DataFrame, one row per call.

        Columns are the call fields (``id``, ``op_name``, ``inputs``, ``output``,
        ``summary``, ``status``, ...). With ``flatten=True`` nested dicts become
        dotted columns (``inputs.x``, ``summary.usage...``) via
        ``pandas.json_normalize``. Fetches every page, so use ``limit`` or a
        filter for large projects. Needs pandas (``pip install pandas``).
        """
        try:
            import pandas as pd  # type: ignore
        except ImportError:
            raise ImportError("CallsIter.to_pandas() needs pandas: pip install pandas") from None
        records = self.to_records()
        if flatten:
            return pd.json_normalize(records)
        return pd.DataFrame(records)


class WeftClient:
    def __init__(
        self,
        project: str,
        server_url: Optional[str] = None,
        api_key: Optional[str] = None,
        *,
        disabled: bool = False,
        timeout: Optional[float] = None,
        max_queue: Optional[int] = None,
        batch_size: int = 100,
        settings: Optional[Dict[str, Any]] = None,
    ):
        self.entity, self.project = split_project(project)
        self.server_url = (server_url or os.environ.get("WEFT_SERVER_URL") or DEFAULT_SERVER_URL).rstrip("/")
        self.api_key = api_key if api_key is not None else os.environ.get("WEFT_API_KEY")
        self.disabled = disabled
        self.settings: Dict[str, Any] = dict(settings or {})
        st = self.settings
        self.log_level = _apply_log_level(st)
        from ._cache import CachingHTTPClient, build_cache

        # Weave's settings and env variables, WEFT_-prefixed (see init()).
        self.http_timeout = _setting_num(st, "http_timeout", "WEFT_HTTP_TIMEOUT", 30.0, float, timeout)
        queue_env = "WEFT_MAX_CALLS_QUEUE_SIZE" if _env_raw("WEFT_MAX_CALLS_QUEUE_SIZE") is not None else "WEFT_MAX_QUEUE_SIZE"
        queue = _setting_num(st, "max_queue_size", queue_env, 10_000, int, max_queue)
        self.max_queue_size = queue if queue > 0 else sys.maxsize  # 0 = unbounded, as in Weave
        self.retry_max_attempts = _setting_num(st, "retry_max_attempts", "WEFT_RETRY_MAX_ATTEMPTS", 3, int)
        self.retry_backoff = _setting_num(st, "retry_backoff", "WEFT_RETRY_BACKOFF", 0.25, float)
        self.retry_max_backoff = _setting_num(st, "retry_max_backoff", "WEFT_RETRY_MAX_INTERVAL", 4.0, float)
        cache, cache_on = build_cache(st)
        self._http: HTTPClient = CachingHTTPClient(
            self.server_url, self.api_key, timeout=self.http_timeout, project_id=self.project_id, cache=cache, enabled=cache_on
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
            max_queue=self.max_queue_size,
            batch_size=int(st.get("batch_size", batch_size)),
            max_retries=self.retry_max_attempts,
            backoff=self.retry_backoff,
            max_backoff=self.retry_max_backoff,
            wal_path=wal_path,
            # Weave's WEAVE_DISABLE_WAL_SENDER: with the WAL on, write requests to it and send nothing.
            wal_only=_setting_bool(st, "disable_wal_sender", "WEFT_DISABLE_WAL_SENDER", False),
        )
        self.wal_path = wal_path
        self._uploaded: set = set()
        self._warned_digest = False
        if wal_path and not disabled and not self._sender.wal_only:
            n = self._sender.replay_wal()
            if n:
                logger.info("weft: replaying %d unsent items from %s", n, wal_path)
        # W&B run linkage (wb_run_id / wb_run_step); see set_wb_run().
        self._wb_run: Optional[Any] = None
        if st.get("wb_run_id"):
            self.set_wb_run(st["wb_run_id"], st.get("wb_run_step"))
        self._redact = Redactor(
            keys=self.settings.get("redact_keys"),
            pii=self.settings.get("redact_pii"),
            enabled=self.settings.get("redact_enabled"),
            settings=self.settings,
        )

    # ------------------------------------------------------------------ W&B run linkage
    def set_wb_run(self, run_id: Optional[str], step: Any = None) -> None:
        """Link calls started from now on to a W&B run (``wb_run_id``, ``wb_run_step``).

        ``run_id`` is a run id (sent as ``<entity>/<project>/<run_id>``, as Weave
        does) or a full ``entity/project/run_id``. ``step`` is an int or a
        callable returning the current step; it is read at call start
        (``wb_run_step``) and end (``wb_run_step_end``). ``None`` clears it.
        Without an explicit run, an active ``wandb.run`` is used when ``wandb``
        is imported.
        """
        if not run_id:
            self._wb_run = None
            return
        rid = str(run_id)
        if rid.count("/") < 2:
            rid = f"{self.entity}/{self.project}/{rid}"
        self._wb_run = (rid, step)

    def _wb_run_now(self) -> "Optional[tuple[str, Optional[int]]]":
        if self._wb_run is not None:
            rid, step = self._wb_run
            if callable(step):
                try:
                    step = step()
                except Exception:
                    step = None
            try:
                step = int(step) if step is not None else None
            except (TypeError, ValueError):
                step = None
            return rid, step
        ctx = _wandb_run_context()
        if ctx is None:
            return None
        return f"{self.entity}/{self.project}/{ctx[0]}", ctx[1]

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
        wb = self._wb_run_now()
        if wb is not None:
            call.wb_run_id, call.wb_run_step = wb
            start["wb_run_id"] = call.wb_run_id
            if call.wb_run_step is not None:
                start["wb_run_step"] = call.wb_run_step
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
        if call.wb_run_id is not None:
            wb = self._wb_run_now()
            if wb is not None and wb[1] is not None:
                call.wb_run_step_end = wb[1]
                end["wb_run_step_end"] = wb[1]
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
        include_usernames: bool = False,
        include_storage_size: bool = False,
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
        if include_usernames:
            body["include_usernames"] = True
        if include_storage_size:
            body["include_storage_size"] = True
        return CallsIter(self, body)

    def get_call(
        self,
        call_id: str,
        *,
        include_costs: bool = False,
        include_feedback: bool = False,
        include_usernames: bool = False,
        include_storage_size: bool = False,
    ) -> Optional[Call]:
        self.flush()
        body: Dict[str, Any] = {"project_id": self.project_id, "id": call_id, "include_costs": include_costs, "include_feedback": include_feedback}
        if include_usernames:
            body["include_usernames"] = True
        if include_storage_size:
            body["include_storage_size"] = True
        res = self._http.post("/call/read", body)
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

    # ------------------------------------------------------------------ threads, custom runtimes
    def threads_query(self, req: Any = None, **kwargs: Any) -> Any:
        """Weave's ``client.threads_query(ThreadsQueryReq(...))``: threads (calls grouped by
        ``thread_id``) with turn counts, times and turn-duration percentiles.

        ``req`` is a ``weft.trace_server_interface.ThreadsQueryReq`` or a dict; keyword
        arguments build one (``client.threads_query(limit=20, sort_by=[...])``).
        Returns a ``ThreadsQueryRes`` (``.threads``: ``ThreadSchema`` list)."""
        from .trace_server_interface import ThreadsQueryRes

        return ThreadsQueryRes(threads=list(self.threads_query_stream(req, **kwargs)))

    def threads_query_stream(self, req: Any = None, **kwargs: Any) -> Iterator[Any]:
        """``threads_query`` as an iterator of ``ThreadSchema`` (``/threads/stream_query``)."""
        from .trace_server_interface import ThreadSchema, ThreadsQueryReq, threads_query_body

        if req is None:
            req = ThreadsQueryReq(**kwargs)
        elif isinstance(req, dict):
            req = ThreadsQueryReq(**{**req, **kwargs})
        elif kwargs:
            req = req.model_copy(update=kwargs)
        body = threads_query_body(req, self.project_id, self.entity)
        for t in self._http.post_jsonl("/threads/stream_query", body):
            yield ThreadSchema.model_validate(t)

    def register_custom_runtime(
        self,
        *,
        name: str,
        base_url: str,
        runtime_ids: Any,
        api_key_secret: Optional[str] = None,
        headers: Optional[Dict[str, str]] = None,
    ) -> Any:
        """Register a custom runtime (an OpenAI-compatible endpoint), replacing its
        configuration if it exists (Weave's ``register_custom_runtime``).

        ``runtime_ids`` is the complete list: IDs left out are removed. A string ID
        gets the default ``max_tokens`` (4096); pass ``weft.CustomRuntimeID(id=...,
        max_tokens=...)`` to set it. ``api_key_secret`` names a stored secret sent
        as the bearer token. Each ID is then usable as the model ``<name>/<id>`` in
        the playground, LLM judges and ``LLMStructuredCompletionModel``. Returns a
        ``CustomRuntimeApplyRes``."""
        from urllib.parse import quote

        from ._custom_runtime import DEFAULT_CUSTOM_RUNTIME_MAX_TOKENS, CustomRuntimeID
        from .trace_server_interface import CustomRuntimeApplyRes

        if isinstance(runtime_ids, (str, bytes)) or not hasattr(runtime_ids, "__iter__"):
            raise TypeError("runtime_ids must be a list of IDs")
        ids: List[Dict[str, Any]] = []
        for rid in runtime_ids:
            if isinstance(rid, str):
                ids.append({"id": rid, "max_tokens": DEFAULT_CUSTOM_RUNTIME_MAX_TOKENS})
            elif isinstance(rid, CustomRuntimeID):
                ids.append(rid.model_dump())
            elif isinstance(rid, dict):
                ids.append(CustomRuntimeID(**rid).model_dump())
            else:
                raise TypeError(f"runtime_ids entries must be str or CustomRuntimeID, got {type(rid).__name__}")
        path = f"/v2/{quote(self.entity, safe='')}/{quote(self.project, safe='')}/runtimes/{quote(name, safe='')}"
        body = {
            "project_id": self.project_id,
            "runtime_name": name,
            "base_url": base_url,
            "api_key_secret": api_key_secret,
            "headers": dict(headers or {}),
            "runtime_ids": ids,
        }
        return CustomRuntimeApplyRes.model_validate(self._http.request("PUT", path, body))

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

    def list_aliases(self) -> List[str]:
        """All distinct aliases in the project (Weave: ``client.list_aliases()``)."""
        return list(self._http.request("GET", "/aliases", query={"project_id": self.project_id}).get("aliases") or [])

    def get_tags_and_aliases(self, obj_ref: Any) -> "tuple[List[str], List[str]]":
        """``(tags, aliases)`` of an object version in one request (Weave's name and order)."""
        r = self._obj_ref(obj_ref)
        obj = self._http.post("/obj/read", {"project_id": r.project_id, "object_id": r.name, "digest": r.digest, "include_tags_and_aliases": True})["obj"]
        return list(obj.get("tags") or []), list(obj.get("aliases") or [])

    # ------------------------------------------------------------------ registry
    def _registry_target(self, target_path: str) -> "tuple[str, str, str]":
        """``<registry_project>/<collection>`` or ``<entity>/<registry_project>/<collection>``."""
        parts = [p for p in str(target_path).strip().strip("/").split("/")]
        if len(parts) == 2 and all(parts):
            return self.entity, parts[0], parts[1]
        if len(parts) == 3 and all(parts):
            return parts[0], parts[1], parts[2]
        raise ValueError(
            "target_path must be '<registry_project>/<collection>' (e.g. 'wandb-registry-prompts/support-bot') "
            "or '<entity>/<registry_project>/<collection>'"
        )

    def link_to_registry(self, obj: Any, *, target_path: str, aliases: Optional[List[str]] = None) -> Dict[str, Any]:
        """Link a published object version into a registry collection (``POST /link_to_registry``).

        ``obj`` is a published object (or one to publish now), a ref, a
        ``weft:///`` / ``weave:///`` URI or ``"name:version"``. ``target_path`` is
        ``<registry_project>/<collection>`` (the registry project is in this
        client's entity) or ``<entity>/<registry_project>/<collection>``.
        ``aliases`` move to the new registry version (``latest`` and ``v<N>``
        are implicit). Returns ``{"version_index", "ref", "target_path"}``;
        linking a version already in the collection returns its index.
        """
        entity, reg_project, collection = self._registry_target(target_path)
        if isinstance(obj, str) and not is_ref_uri(obj):
            r: Any = self._obj_ref(obj)
        elif isinstance(obj, (str, Ref)):
            r = self.ref(obj) if isinstance(obj, str) else obj
        else:
            r = getattr(obj, "_weft_ref", None) or getattr(obj, "ref", None)
            if not isinstance(r, Ref):
                r = self.publish(obj)
        if isinstance(r, ObjectRef) and (r.digest == "latest" or re.fullmatch(r"v\d+", r.digest or "")):
            r = self._obj_ref(r)
        if not isinstance(r, (ObjectRef, OpRef)):
            raise TypeError(f"only object versions can be linked to a registry, got {r!r}")
        self.flush()
        body = {
            "ref": r.uri(),
            "target": {"portfolio_name": collection, "entity_name": entity, "project_name": reg_project},
            "aliases": list(aliases or []),
        }
        res = self._http.post("/link_to_registry", body)
        return {"version_index": res.get("version_index"), "ref": r.uri(), "target_path": f"{entity}/{reg_project}/{collection}"}

    def link_prompt_to_registry(self, prompt: Any, *, target_path: str, aliases: Optional[List[str]] = None) -> Dict[str, Any]:
        """Weave's ``link_prompt_to_registry``: link a published prompt version into a registry collection."""
        return self.link_to_registry(prompt, target_path=target_path, aliases=aliases)

    def query_registry(
        self,
        registry_project: str,
        collection: Optional[str] = None,
        *,
        alias: Optional[str] = None,
        version_index: Optional[int] = None,
    ) -> List[Dict[str, Any]]:
        """Collections of a registry project and their versions (``POST /registry/query``), newest first.

        ``registry_project`` is a project name in this entity or
        ``entity/project``; ``alias``/``version_index`` need ``collection``.
        """
        pid = registry_project if "/" in registry_project else f"{self.entity}/{registry_project}"
        body: Dict[str, Any] = {"project_id": pid}
        if collection:
            body["portfolio_name"] = collection
        if alias:
            body["alias"] = alias
        if version_index is not None:
            body["version_index"] = int(version_index)
        return list(self._http.post("/registry/query", body).get("collections") or [])

    def get_from_registry(self, target_path: str, version: Union[str, int] = "latest") -> Any:
        """Load the object a registry version points to (``version``: ``"latest"``, ``"v2"``, ``2`` or an alias)."""
        entity, reg_project, collection = self._registry_target(target_path)
        kw: Dict[str, Any] = {}
        if isinstance(version, int) or (isinstance(version, str) and re.fullmatch(r"v?\d+", version)):
            kw["version_index"] = int(str(version).lstrip("v"))
        elif version and version != "latest":
            kw["alias"] = str(version)
        cols = self.query_registry(f"{entity}/{reg_project}", collection, **kw)
        versions = (cols[0].get("versions") if cols else None) or []
        if not versions:
            raise LookupError(f"no version {version!r} in registry collection {entity}/{reg_project}/{collection}")
        return self.get(versions[0]["ref"])

    def read_file(self, file_digest: str) -> bytes:
        """Raw bytes of an uploaded file (``Content``/media ``file_digest``)."""
        self.flush()
        return self._http.request_raw("POST", "/file/content", {"project_id": self.project_id, "digest": file_digest})

    def resolve_url(self, target: Any, expires_in: Optional[int] = None) -> Any:
        """Download URL(s) via ``/files/resolve_url``.

        ``target`` is a file digest, a ``weft-file://<digest>``/``s3://bucket/key``/``https://`` URI,
        a ``Content`` value, or a list of these (then a list is returned). Files in the server's S3
        backend and ``s3://`` references in its bucket get presigned URLs; other files get this
        server's ``/file/content`` URL; HTTP(S) URLs come back unchanged.
        """
        from ._content import Content

        def one(t: Any) -> str:
            if isinstance(t, Content):
                return t.uri if t.uri is not None else t.digest
            if isinstance(t, dict) and (t.get("uri") or t.get("file_digest")):
                return t.get("uri") or t["file_digest"]
            return str(t)

        items = list(target) if isinstance(target, (list, tuple)) else [target]
        uris = [one(t) for t in items]
        self.flush()  # pending uploads must exist before their digests resolve
        body: Dict[str, Any] = {"project_id": self.project_id, "uris": uris}
        if expires_in is not None:
            body["expires_in"] = int(expires_in)
        urls = list(self._http.post("/files/resolve_url", body).get("download_urls") or [])
        return urls if isinstance(target, (list, tuple)) else (urls[0] if urls else None)

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
    with ``wal_dir`` (env ``WEFT_WAL_DIR``) and ``disable_wal_sender`` (env
    ``WEFT_DISABLE_WAL_SENDER``: write every request to the WAL and send
    nothing; the WAL is replayed by a later run without it), ``max_queue_size`` (default
    10000, 0 = unbounded; env ``WEFT_MAX_CALLS_QUEUE_SIZE``),
    ``retry_max_attempts`` (default 3, env ``WEFT_RETRY_MAX_ATTEMPTS``),
    ``retry_backoff`` (first retry delay in seconds, default 0.25, env
    ``WEFT_RETRY_BACKOFF``), ``retry_max_backoff`` (default 4, env
    ``WEFT_RETRY_MAX_INTERVAL``), ``http_timeout`` (seconds, default 30, env
    ``WEFT_HTTP_TIMEOUT``), ``log_level`` (``DEBUG`` ... ``CRITICAL`` for the
    ``weft`` logger, env ``WEFT_LOG_LEVEL``), ``wb_run_id``/``wb_run_step``
    (see ``set_wb_run``), ``redact_keys``, ``redact_pii``, and the cache of immutable server
    responses: ``use_server_cache`` (default True, env
    ``WEFT_USE_SERVER_CACHE``), ``server_cache_dir`` (default
    ``~/.cache/weft``, env ``WEFT_SERVER_CACHE_DIR``) and
    ``server_cache_size_limit`` (bytes, default 1 GB, env
    ``WEFT_SERVER_CACHE_SIZE_LIMIT``). ``WEFT_DEBUG_HTTP=1`` prints every HTTP
    request and response to stderr (Authorization redacted).
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


def link_to_registry(obj: Any, *, target_path: str, aliases: Optional[List[str]] = None) -> Dict[str, Any]:
    """Link any published object version into a registry collection; see ``WeftClient.link_to_registry``."""
    return require_client().link_to_registry(obj, target_path=target_path, aliases=aliases)


def link_prompt_to_registry(prompt: Any, *, target_path: str, aliases: Optional[List[str]] = None) -> Dict[str, Any]:
    """Weave's ``weave.link_prompt_to_registry``: link a published prompt version into a registry collection."""
    return require_client().link_prompt_to_registry(prompt, target_path=target_path, aliases=aliases)


def set_wb_run(run_id: Optional[str], step: Any = None) -> None:
    """Link calls of the global client to a W&B run; see ``WeftClient.set_wb_run``."""
    require_client().set_wb_run(run_id, step)

