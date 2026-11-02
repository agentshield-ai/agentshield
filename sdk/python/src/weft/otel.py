"""Send OpenTelemetry spans to Weft.

Requires the ``otel`` extra: ``pip install 'weft[otel]'``.

>>> import weft.otel
>>> provider = weft.otel.configure(project="my-project")
>>> # any OTel / OpenInference / OpenLLMetry instrumentation now exports to Weft
"""

from __future__ import annotations

import json
import os
from typing import Any, Dict, Optional

from ._refs import split_project


def otlp_endpoint(server_url: Optional[str] = None) -> str:
    from ._client import DEFAULT_SERVER_URL

    url = (server_url or os.environ.get("WEFT_SERVER_URL") or DEFAULT_SERVER_URL).rstrip("/")
    return url + "/otel/v1/traces"


def otlp_headers(project: str, api_key: Optional[str] = None) -> Dict[str, str]:
    entity, proj = split_project(project)
    headers = {"project_id": f"{entity}/{proj}"}
    key = api_key if api_key is not None else os.environ.get("WEFT_API_KEY")
    if key:
        headers["Authorization"] = f"Bearer {key}"
    return headers


def configure(
    project: Optional[str] = None,
    server_url: Optional[str] = None,
    api_key: Optional[str] = None,
    *,
    service_name: str = "weft-app",
    set_global: bool = True,
    batch: bool = True,
    resource_attributes: Optional[Dict[str, Any]] = None,
) -> Any:
    """Create a TracerProvider exporting OTLP/HTTP to ``<server>/otel/v1/traces``.

    Spans carry the project via the ``project_id`` header (and the
    ``weft.project`` resource attribute). Returns the TracerProvider; with
    ``set_global`` it is also installed as the global provider.
    """
    try:
        from opentelemetry import trace
        from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
        from opentelemetry.sdk.resources import Resource
        from opentelemetry.sdk.trace import TracerProvider
        from opentelemetry.sdk.trace.export import BatchSpanProcessor, SimpleSpanProcessor
    except ImportError as e:  # pragma: no cover
        raise ImportError("weft.otel requires: pip install 'weft[otel]'") from e

    project = project or os.environ.get("WEFT_PROJECT")
    if not project:
        raise ValueError("weft.otel.configure requires a project (or WEFT_PROJECT)")
    entity, proj = split_project(project)
    attrs: Dict[str, Any] = {"service.name": service_name, "weft.project": f"{entity}/{proj}"}
    attrs.update(resource_attributes or {})
    provider = TracerProvider(resource=Resource.create(attrs))
    exporter = OTLPSpanExporter(endpoint=otlp_endpoint(server_url), headers=otlp_headers(project, api_key))
    provider.add_span_processor(BatchSpanProcessor(exporter) if batch else SimpleSpanProcessor(exporter))
    if set_global:
        trace.set_tracer_provider(provider)
    return provider


def set_inputs(span: Any, inputs: Any) -> None:
    """Record call inputs on a span (``weft.inputs`` attribute, JSON)."""
    span.set_attribute("weft.inputs", _dumps(inputs))


def set_output(span: Any, output: Any) -> None:
    """Record the call output on a span (``weft.output`` attribute, JSON)."""
    span.set_attribute("weft.output", _dumps(output))


def set_usage(span: Any, model: str, prompt_tokens: int, completion_tokens: int) -> None:
    """Record LLM usage with GenAI semantic convention attributes."""
    span.set_attribute("gen_ai.request.model", model)
    span.set_attribute("gen_ai.usage.input_tokens", int(prompt_tokens))
    span.set_attribute("gen_ai.usage.output_tokens", int(completion_tokens))


def _dumps(v: Any) -> str:
    from ._serialize import to_json

    return json.dumps(to_json(v), ensure_ascii=False)


__all__ = ["configure", "otlp_endpoint", "otlp_headers", "set_inputs", "set_output", "set_usage"]
