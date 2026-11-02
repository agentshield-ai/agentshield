"""AWS Bedrock via boto3: ``converse``, ``converse_stream``, ``invoke_model`` and
``invoke_model_with_response_stream`` on ``bedrock-runtime`` clients, and
Bedrock Agents (``invoke_agent``, ``invoke_inline_agent`` on
``bedrock-agent-runtime``; see ``bedrock_agents.py``).

Patches ``botocore.client.BaseClient._make_api_call`` so every bedrock-runtime
client is traced, whenever it was created.
"""

from __future__ import annotations

import importlib
import io
import json

from typing import Any, Dict, Optional

from .._context import pop_call, push_call
from .._usage import add_usage
from ._common import LLMSpec, _tracing, finish_call, patch_attr, start_call

OPS = {
    "Converse": "bedrock.converse",
    "ConverseStream": "bedrock.converse_stream",
    "InvokeModel": "bedrock.invoke_model",
    "InvokeModelWithResponseStream": "bedrock.invoke_model_with_response_stream",
}


def _int(v: Any) -> int:
    try:
        return int(v or 0)
    except (TypeError, ValueError):
        return 0


def _decode_body(body: Any) -> Any:
    if isinstance(body, (bytes, bytearray)):
        body = body.decode("utf-8", "replace")
    if isinstance(body, str):
        try:
            return json.loads(body)
        except ValueError:
            return body
    return body


def _usage_from_body(body: Any) -> Optional[Dict[str, int]]:
    if not isinstance(body, dict):
        return None
    u = body.get("usage") or {}
    if isinstance(u, dict) and (u.get("input_tokens") is not None or u.get("inputTokens") is not None):
        return {"p": _int(u.get("input_tokens", u.get("inputTokens"))), "c": _int(u.get("output_tokens", u.get("outputTokens")))}
    m = body.get("amazon-bedrock-invocationMetrics")
    if isinstance(m, dict):
        return {"p": _int(m.get("inputTokenCount")), "c": _int(m.get("outputTokenCount"))}
    if body.get("prompt_token_count") is not None or body.get("inputTextTokenCount") is not None:
        p = body.get("prompt_token_count", body.get("inputTextTokenCount"))
        c = body.get("generation_token_count") or sum(_int(r.get("tokenCount")) for r in body.get("results") or [])
        return {"p": _int(p), "c": _int(c)}
    return None


class _Body:
    """A replayable StreamingBody replacement (the original is read once)."""

    def __init__(self, data: bytes):
        self._buf = io.BytesIO(data)
        self._len = len(data)

    def read(self, amt: Optional[int] = None) -> bytes:
        return self._buf.read() if amt is None else self._buf.read(amt)

    def iter_lines(self, *a: Any, **k: Any) -> Any:
        return iter(self._buf.read().splitlines())

    def close(self) -> None:
        pass

    def __iter__(self) -> Any:
        return iter([self._buf.read()])


class _StreamProxy:
    """Wraps a Bedrock event stream, accumulating text and usage; ends the call when done."""

    def __init__(self, stream: Any, client: Any, call: Any, model: str, converse: bool):
        self._stream, self._client, self._call, self._model, self._converse = stream, client, call, model, converse
        self._done = False
        self.text = ""
        self.events = 0
        self.usage: Optional[Dict[str, int]] = None
        self.stop_reason: Optional[str] = None

    def _add(self, ev: Dict[str, Any]) -> None:
        self.events += 1
        if self._converse:
            if "contentBlockDelta" in ev:
                self.text += ((ev["contentBlockDelta"].get("delta") or {}).get("text") or "")
            if "messageStop" in ev:
                self.stop_reason = ev["messageStop"].get("stopReason")
            if "metadata" in ev:
                u = ev["metadata"].get("usage") or {}
                self.usage = {"p": _int(u.get("inputTokens")), "c": _int(u.get("outputTokens"))}
        else:
            chunk = _decode_body((ev.get("chunk") or {}).get("bytes"))
            if isinstance(chunk, dict):
                d = chunk.get("delta") or {}
                self.text += d.get("text") or chunk.get("completion") or chunk.get("generation") or chunk.get("outputText") or ""
                u = _usage_from_body(chunk)
                if u is not None:
                    self.usage = u if self.usage is None else {"p": max(self.usage["p"], u["p"]), "c": max(self.usage["c"], u["c"])}

    def _finish(self, exc: Optional[BaseException] = None) -> None:
        if self._done:
            return
        self._done = True
        if self.usage is not None:
            add_usage(self._call, self._model, self.usage["p"], self.usage["c"])
        out = {"text": self.text, "stop_reason": self.stop_reason, "events": self.events}
        finish_call(self._client, self._call, out, exc)

    def __iter__(self) -> Any:
        try:
            for ev in self._stream:
                try:
                    self._add(ev)
                except Exception:  # pragma: no cover - best effort
                    pass
                yield ev
        except BaseException as e:
            self._finish(e)
            raise
        self._finish()

    def close(self) -> None:
        self._finish()
        fn = getattr(self._stream, "close", None)
        if callable(fn):
            fn()

    def __getattr__(self, item: str) -> Any:
        return getattr(self._stream, item)


def _agent_call(orig: Any, self: Any, client: Any, operation_name: str, api_params: Dict[str, Any]) -> Any:
    from .bedrock_agents import handle_agent_response, start_agent_call

    try:
        call, proc = start_agent_call(client, operation_name, api_params)
    except Exception as e:  # pragma: no cover - never break the caller
        import logging

        logging.getLogger("weft").debug("weft: could not start a bedrock agent call: %s", e)
        return orig(self, operation_name, api_params)
    token = push_call(call)
    try:
        res = orig(self, operation_name, api_params)
    except BaseException as e:
        pop_call(token)
        proc.finish(e)
        raise
    pop_call(token)
    try:
        return handle_agent_response(proc, res)
    except Exception:  # pragma: no cover
        proc.finish(None)
        return res


def _wrap(orig: Any) -> Any:
    def _make_api_call(self: Any, operation_name: str, api_params: Dict[str, Any]) -> Any:
        meta = getattr(self, "meta", None)
        service = getattr(getattr(meta, "service_model", None), "service_name", "")
        if service == "bedrock-agent-runtime" and operation_name in ("InvokeAgent", "InvokeInlineAgent"):
            agent_client = _tracing("bedrock")
            if agent_client is None:
                return orig(self, operation_name, api_params)
            return _agent_call(orig, self, agent_client, operation_name, api_params)
        client = _tracing("bedrock") if service == "bedrock-runtime" and operation_name in OPS else None
        if client is None:
            return orig(self, operation_name, api_params)
        inputs = dict(api_params)
        if "body" in inputs:
            inputs["body"] = _decode_body(inputs["body"])
        model = str(api_params.get("modelId", ""))
        call = start_call(client, LLMSpec(OPS[operation_name], integration="bedrock"), inputs)
        token = push_call(call)
        try:
            res = orig(self, operation_name, api_params)
        except BaseException as e:
            pop_call(token)
            finish_call(client, call, None, e)
            raise
        pop_call(token)
        try:
            return _handle(client, call, operation_name, model, res)
        except Exception as e:  # pragma: no cover - never break the caller
            finish_call(client, call, None)
            import logging

            logging.getLogger("weft").debug("weft: bedrock response handling failed: %s", e)
            return res

    return _make_api_call


def _handle(client: Any, call: Any, op: str, model: str, res: Dict[str, Any]) -> Any:
    out = {k: v for k, v in res.items() if k != "ResponseMetadata"}
    if op == "Converse":
        u = res.get("usage") or {}
        add_usage(call, model, _int(u.get("inputTokens")), _int(u.get("outputTokens")), u.get("totalTokens"))
        finish_call(client, call, out)
        return res
    if op == "ConverseStream":
        res["stream"] = _StreamProxy(res["stream"], client, call, model, converse=True)
        return res
    if op == "InvokeModelWithResponseStream":
        res["body"] = _StreamProxy(res["body"], client, call, model, converse=False)
        return res
    # InvokeModel: read the body once, keep a replayable copy for the caller
    body = res.get("body")
    data = body.read() if hasattr(body, "read") else (body or b"")
    res["body"] = _Body(data if isinstance(data, bytes) else str(data).encode())
    parsed = _decode_body(data)
    out["body"] = parsed
    headers = ((res.get("ResponseMetadata") or {}).get("HTTPHeaders") or {})
    p = headers.get("x-amzn-bedrock-input-token-count")
    c = headers.get("x-amzn-bedrock-output-token-count")
    if p is not None or c is not None:
        add_usage(call, model, _int(p), _int(c))
    else:
        u = _usage_from_body(parsed)
        if u is not None:
            add_usage(call, model, u["p"], u["c"])
    finish_call(client, call, out)
    return res


def patch_bedrock() -> bool:
    try:
        mod = importlib.import_module("botocore.client")
    except ImportError:
        return False
    cls = getattr(mod, "BaseClient", None)
    return patch_attr(cls, "_make_api_call", _wrap) if cls is not None else False


