"""Per-integration autopatch settings (``autopatch_settings``).

``weft.init(..., autopatch_settings={...})`` accepts a dict, or an
``AutopatchSettings`` object, keyed by integration name::

    weft.init("proj", autopatch_settings={
        "openai": {"trace_settings": {"postprocess_inputs": scrub, "postprocess_output": scrub}},
        "crewai": {"enabled": False},
    })

Each value is an ``IntegrationSettings`` (or a dict of its fields):

* ``enabled`` (default True): patch and trace this integration.
* ``trace_settings``: ``TraceSettings`` applied to every span the integration
  records: ``name`` (span name override), ``span_display_name`` (string or
  ``callable(span) -> str``), ``postprocess_inputs(dict) -> dict``,
  ``postprocess_output(value) -> value``, ``kind``, ``color`` and
  ``attributes`` (merged into each span's attributes).

Environment variables (read at patch time and at call time):

* ``WEFT_AUTOPATCH=false`` disables every integration (like ``init(autopatch=False)``).
* ``WEFT_DISABLED_INTEGRATIONS=openai,crewai`` disables the listed integrations.
* ``WEFT_AUTOPATCH_<NAME>=false`` (e.g. ``WEFT_AUTOPATCH_LLAMAINDEX=0``) disables one.

Settings are re-applied to integrations that are already patched, and a
disabled integration stops recording immediately (its patches stay in place
and call straight through).
"""

from __future__ import annotations

import os
import threading
from typing import Any, Callable, Dict, List, Mapping, Optional, Union

from pydantic import BaseModel, ConfigDict, Field, model_validator

_FALSE = ("0", "false", "no", "off", "disabled")

# Aliases so users can pass the library name as well as the integration name.
ALIASES: Dict[str, str] = {
    "llama_index": "llamaindex",
    "llama-index": "llamaindex",
    "google.genai": "google_genai",
    "google-genai": "google_genai",
    "genai": "google_genai",
    "boto3": "bedrock",
    "bedrock_agents": "bedrock",
    "bedrock-agent-runtime": "bedrock",
    "mistralai": "mistral",
    "langchain_core": "langchain",
    "openai-agents": "openai_agents",
    "agents": "openai_agents",
    "fastmcp": "mcp",
    "claude-agent-sdk": "claude_agent_sdk",
    "autogen_agentchat": "autogen",
    "autogen-agentchat": "autogen",
    "huggingface_hub": "huggingface",
    "hf": "huggingface",
    "vertex": "vertexai",
    "google_vertexai": "vertexai",
    "cerebras-cloud-sdk": "cerebras",
    "nvidia_nim": "nvidia",
    "langchain_nvidia_ai_endpoints": "nvidia",
    "haystack-ai": "haystack",
    "pydantic-ai": "pydantic_ai",
    "google.adk": "google_adk",
    "realtime": "openai_realtime",
    "typesafe_sdk": "typesafe",
    "typesafe-sdk": "typesafe",
}


def canonical(name: str) -> str:
    n = str(name).strip().lower()
    return ALIASES.get(n, n.replace("-", "_"))


class TraceSettings(BaseModel):
    """Options applied to the spans an integration records (mirrors ``@weft.trace``)."""

    model_config = ConfigDict(arbitrary_types_allowed=True, extra="forbid")

    name: Optional[str] = None
    span_display_name: Optional[Union[str, Callable[[Any], str]]] = None
    postprocess_inputs: Optional[Callable[[Dict[str, Any]], Dict[str, Any]]] = None
    postprocess_output: Optional[Callable[[Any], Any]] = None
    kind: Optional[str] = None
    color: Optional[str] = None
    attributes: Optional[Dict[str, Any]] = None

    @model_validator(mode="before")
    @classmethod
    def _earlier_names(cls, data: Any) -> Any:
        if isinstance(data, Mapping) and "call_display_name" in data:
            from .._deprecation import deprecated_call

            deprecated_call("TraceSettings(call_display_name=...)", "TraceSettings(span_display_name=...)", stacklevel=6)
            data = dict(data)
            old = data.pop("call_display_name")
            if data.get("span_display_name") is None:
                data["span_display_name"] = old
        return data

    @property
    def call_display_name(self) -> Optional[Union[str, Callable[[Any], str]]]:
        """Deprecated: ``TraceSettings.span_display_name``."""
        from .._deprecation import deprecated_call

        deprecated_call("TraceSettings.call_display_name", "TraceSettings.span_display_name")
        return self.span_display_name


class IntegrationSettings(BaseModel):
    """Configuration for one integration."""

    model_config = ConfigDict(arbitrary_types_allowed=True, extra="forbid")

    enabled: bool = True
    trace_settings: TraceSettings = Field(default_factory=TraceSettings)

    @model_validator(mode="before")
    @classmethod
    def _earlier_names(cls, data: Any) -> Any:
        if isinstance(data, Mapping) and "op_settings" in data:
            return _earlier_op_settings(dict(data), stacklevel=6)
        return data

    @property
    def op_settings(self) -> TraceSettings:
        """Deprecated: ``IntegrationSettings.trace_settings``."""
        from .._deprecation import deprecated_call

        deprecated_call("IntegrationSettings.op_settings", "IntegrationSettings.trace_settings")
        return self.trace_settings


def _earlier_op_settings(d: Dict[str, Any], stacklevel: int) -> Dict[str, Any]:
    """Read the earlier key as ``trace_settings`` (``trace_settings`` wins), with a warning."""
    from .._deprecation import deprecated_call

    deprecated_call('autopatch settings "op_settings"', '"trace_settings"', stacklevel=stacklevel)
    old = d.pop("op_settings")
    if d.get("trace_settings") is None:
        d["trace_settings"] = old
    return d


class AutopatchSettings(BaseModel):
    """All integrations' settings, keyed by integration name (extra keys allowed)."""

    model_config = ConfigDict(arbitrary_types_allowed=True, extra="allow")

    def as_dict(self) -> Dict[str, Any]:
        out: Dict[str, Any] = {}
        for k in list(type(self).model_fields) + list((self.model_extra or {}).keys()):
            v = getattr(self, k, None)
            if v is not None:
                out[k] = v
        return out


_lock = threading.RLock()
_settings: Dict[str, IntegrationSettings] = {}
_version = 0


def _coerce(value: Any) -> IntegrationSettings:
    if isinstance(value, IntegrationSettings):
        return value
    if isinstance(value, bool):
        return IntegrationSettings(enabled=value)
    if isinstance(value, TraceSettings):
        return IntegrationSettings(trace_settings=value)
    if isinstance(value, Mapping):
        d = dict(value)
        if "op_settings" in d:
            d = _earlier_op_settings(d, stacklevel=5)
        ts = d.get("trace_settings")
        if isinstance(ts, Mapping):
            d["trace_settings"] = TraceSettings(**dict(ts))
        elif ts is None:
            d.pop("trace_settings", None)
        return IntegrationSettings(**d)
    raise TypeError(f"autopatch settings must be a dict, bool or IntegrationSettings, got {type(value).__name__}")


def configure(settings: Any = None) -> None:
    """Replace the per-integration settings (``None`` resets to defaults)."""
    global _version
    parsed: Dict[str, IntegrationSettings] = {}
    if settings is not None:
        if isinstance(settings, AutopatchSettings):
            settings = settings.as_dict()
        elif isinstance(settings, BaseModel):
            settings = {k: v for k, v in settings.model_dump().items() if v is not None}
        if not isinstance(settings, Mapping):
            raise TypeError("autopatch_settings must be a dict or AutopatchSettings")
        for k, v in settings.items():
            if v is None:
                continue
            parsed[canonical(k)] = _coerce(v)
    with _lock:
        _settings.clear()
        _settings.update(parsed)
        _version += 1
    _reapply_ops()


def update(name: str, value: Any) -> None:
    """Set one integration's settings (merging is not attempted)."""
    global _version
    with _lock:
        _settings[canonical(name)] = _coerce(value)
        _version += 1
    _reapply_ops()


def get(name: str) -> IntegrationSettings:
    with _lock:
        s = _settings.get(canonical(name))
    return s if s is not None else IntegrationSettings()


def _env_disabled(name: str) -> bool:
    raw = os.environ.get("WEFT_AUTOPATCH")
    if raw is not None and raw.strip().lower() in _FALSE:
        return True
    lst = os.environ.get("WEFT_DISABLED_INTEGRATIONS") or os.environ.get("WEFT_DISABLE_INTEGRATIONS") or ""
    if lst:
        names = {canonical(x) for x in lst.split(",") if x.strip()}
        if name in names or "all" in names or "*" in names:
            return True
    one = os.environ.get("WEFT_AUTOPATCH_" + name.upper())
    return one is not None and one.strip().lower() in _FALSE


def is_enabled(name: Optional[str]) -> bool:
    if not name:
        return True
    n = canonical(name)
    if _env_disabled(n):
        return False
    return get(n).enabled


# ------------------------------------------------------------------ ops
# Every op an integration records goes through ``integration_op`` so the
# settings (name, display name, postprocess hooks, kind, color) apply.

_OPS: Dict[str, List[Any]] = {}


class _OpDefaults:
    __slots__ = ("name", "kind", "color", "display", "inputs", "output")

    def __init__(self, name: str, kind: Optional[str], color: Optional[str], display: Any, inputs: Any, output: Any):
        self.name, self.kind, self.color, self.display, self.inputs, self.output = name, kind, color, display, inputs, output


def _compose(first: Optional[Callable[[Any], Any]], second: Optional[Callable[[Any], Any]]) -> Optional[Callable[[Any], Any]]:
    if first is None:
        return second
    if second is None:
        return first
    return lambda v: second(first(v))


def _apply(op: Any, integration: str) -> None:
    d: _OpDefaults = op._weft_integration_defaults
    s = get(integration).trace_settings
    new_name = s.name or d.name
    if new_name != op.name:
        op._refs.clear()
    op.name = new_name
    op.kind = s.kind or d.kind
    op.color = s.color or d.color
    op.span_display_name = s.span_display_name if s.span_display_name is not None else d.display
    op.postprocess_inputs = _compose(d.inputs, s.postprocess_inputs)
    op.postprocess_output = _compose(d.output, s.postprocess_output)


def _reapply_ops() -> None:
    with _lock:
        items = [(k, list(v)) for k, v in _OPS.items()]
    for integration, ops in items:
        for op in ops:
            _apply(op, integration)


_SYNTH: Dict[Any, Any] = {}


def integration_op(
    integration: str,
    name: str,
    kind: Optional[str] = None,
    fn: Optional[Callable[..., Any]] = None,
    *,
    color: Optional[str] = None,
    display: Any = None,
    postprocess_inputs: Optional[Callable[[Dict[str, Any]], Dict[str, Any]]] = None,
    postprocess_output: Optional[Callable[[Any], Any]] = None,
    accumulator: Optional[Callable[[Any, Any], Any]] = None,
) -> Any:
    """Return an ``Op`` for an integration, with that integration's settings applied.

    Without ``fn`` the op is a cached marker op (used with ``create_span``);
    with ``fn`` it wraps and runs ``fn``.
    """
    from .._op import Op

    integration = canonical(integration)
    key = None
    if fn is None:
        key = (integration, name, kind, display)
        with _lock:
            cached = _SYNTH.get(key)
        if cached is not None:
            return cached

        def _marker(*args: Any, **kwargs: Any) -> Any:  # pragma: no cover - never executed
            return None

        fn = _marker
    op = Op(
        fn,
        name=name,
        kind=kind,
        color=color,
        accumulator=accumulator,
        code=f"# {name}: recorded by the weft {integration} integration\n",
        enable_code_capture=False,
    )
    op._weft_integration = integration
    op._weft_integration_defaults = _OpDefaults(name, kind, color, display, postprocess_inputs, postprocess_output)
    _apply(op, integration)
    with _lock:
        _OPS.setdefault(integration, []).append(op)
        if key is not None:
            _SYNTH[key] = op
    return op


def integration_attributes(integration: str) -> Dict[str, Any]:
    """Attributes stamped on every span of an integration (plus user ``trace_settings.attributes``)."""
    n = canonical(integration)
    attrs: Dict[str, Any] = {"weft": {"integration": n}}
    extra = get(n).trace_settings.attributes
    if extra:
        for k, v in extra.items():
            if k == "weft" and isinstance(v, dict):
                attrs["weft"].update(v)
            else:
                attrs[k] = v
    return attrs


def __getattr__(name: str) -> Any:
    if name == "OpSettings":  # earlier name (CHANGELOG.md)
        from .._deprecation import deprecated_call

        deprecated_call("weft.integrations.OpSettings", "weft.integrations.TraceSettings")
        return TraceSettings
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")
