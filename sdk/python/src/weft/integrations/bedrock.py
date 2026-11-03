"""AWS Bedrock via boto3: ``converse``, ``converse_stream``, ``invoke_model`` and
``invoke_model_with_response_stream`` on ``bedrock-runtime`` clients, and
Bedrock Agents (``invoke_agent``, ``invoke_inline_agent`` on
``bedrock-agent-runtime``; see ``bedrock_agents.py``).

Patches ``botocore.client.BaseClient._make_api_call`` so every bedrock-runtime
client is traced, whenever it was created.

Converse reasoning (``reasoningContent``: ``reasoningText`` or
``redactedContent``, streamed or not) is recorded as ``thinking`` /
``redacted_thinking`` content blocks of the output, the shape the server
reads reasoning steps from; redacted content is kept as a digest only.
"""

from __future__ import annotations

import importlib
import io
import json

from typing import Any, Dict, Optional

from .._context import pop_call, push_call
from .._usage import add_usage, anthropic_usage, bedrock_usage
from .._reasoning import bedrock_block, bedrock_content
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


def _add(call: Any, model: Any, u: Dict[str, int]) -> None:
    add_usage(call, model, u.get("p", 0), u.get("c", 0),
              cache_read_input_tokens=u.get("cr", 0), cache_creation_input_tokens=u.get("cw", 0))


def _usage_from_body(body: Any) -> Optional[Dict[str, int]]:
    if not isinstance(body, dict):
        return None
    u = body.get("usage") or {}
    if isinstance(u, dict) and (u.get("input_tokens") is not None or u.get("output_tokens") is not None):  # Anthropic Messages bodies: gross prompt + cache counts
        n = anthropic_usage(u) or {}
        return {"p": n.get("prompt_tokens", 0), "c": n.get("completion_tokens", 0),
                "cr": n.get("cache_read_input_tokens", 0), "cw": n.get("cache_creation_input_tokens", 0)}
    if isinstance(u, dict) and u.get("inputTokens") is not None:
        n = bedrock_usage(u) or {}
        return {"p": n.get("prompt_tokens", 0), "c": n.get("completion_tokens", 0),
                "cr": n.get("cache_read_input_tokens", 0), "cw": n.get("cache_creation_input_tokens", 0)}
    m = body.get("amazon-bedrock-invocationMetrics")
    if isinstance(m, dict):
        cr, cw = _int(m.get("cacheReadInputTokenCount")), _int(m.get("cacheWriteInputTokenCount"))
        return {"p": _int(m.get("inputTokenCount")) + cr + cw, "c": _int(m.get("outputTokenCount")), "cr": cr, "cw": cw}
    if isinstance(body.get("message"), dict) and isinstance(body["message"].get("usage"), dict):  # Anthropic message_start
        return _usage_from_body(body["message"])
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
        # Converse reasoning blocks by content block index: text, signature, redacted bytes
        self.reasoning: Dict[int, Dict[str, Any]] = {}
        self.usage: Optional[Dict[str, int]] = None
        self.stop_reason: Optional[str] = None

    def _add(self, ev: Dict[str, Any]) -> None:
        self.events += 1
        if self._converse:
            if "contentBlockDelta" in ev:
                cbd = ev["contentBlockDelta"]
                delta = cbd.get("delta") or {}
                self.text += delta.get("text") or ""
                rc = delta.get("reasoningContent")
                if isinstance(rc, dict):
                    r = self.reasoning.setdefault(int(cbd.get("contentBlockIndex") or 0), {"text": ""})
                    r["text"] += rc.get("text") or ""
                    if rc.get("signature"):
                        r["signature"] = rc["signature"]
                    if rc.get("redactedContent") is not None:
                        r["redacted"] = rc["redactedContent"]
            if "messageStop" in ev:
                self.stop_reason = ev["messageStop"].get("stopReason")
            if "metadata" in ev:
                n = bedrock_usage(ev["metadata"].get("usage") or {}) or {}
                self.usage = {"p": n.get("prompt_tokens", 0), "c": n.get("completion_tokens", 0),
                              "cr": n.get("cache_read_input_tokens", 0), "cw": n.get("cache_creation_input_tokens", 0)}
        else:
            chunk = _decode_body((ev.get("chunk") or {}).get("bytes"))
            if isinstance(chunk, dict):
                d = chunk.get("delta") or {}
                self.text += d.get("text") or chunk.get("completion") or chunk.get("generation") or chunk.get("outputText") or ""
                u = _usage_from_body(chunk)
                if u is not None:
                    # message_start carries the prompt and cache counts, message_delta the output: keep the largest of each
                    self.usage = u if self.usage is None else {k: max(self.usage.get(k, 0), u.get(k, 0)) for k in set(self.usage) | set(u)}

    def _finish(self, exc: Optional[BaseException] = None) -> None:
        if self._done:
            return
        self._done = True
        if self.usage is not None:
            _add(self._call, self._model, self.usage)
        out: Dict[str, Any] = {"text": self.text, "stop_reason": self.stop_reason, "events": self.events}
        if self.reasoning:
            blocks = []
            for i in sorted(self.reasoning):
                r = self.reasoning[i]
                if r.get("redacted") is not None:
                    blocks.append(bedrock_block({"reasoningContent": {"redactedContent": r["redacted"]}}))
                else:
                    blocks.append(bedrock_block({"reasoningContent": {"reasoningText": {"text": r["text"], "signature": r.get("signature")}}}))
            out["content"] = blocks + ([{"type": "text", "text": self.text}] if self.text else [])
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
        msg = (out.get("output") or {}).get("message") if isinstance(out.get("output"), dict) else None
        if isinstance(msg, dict) and isinstance(msg.get("content"), list):
            # reasoningContent blocks as thinking / redacted_thinking blocks, the shape the server reads
            out = {**out, "output": {**out["output"], "message": {**msg, "content": bedrock_content(msg["content"])}}}
        n = bedrock_usage(res.get("usage") or {})
        if n is not None:
            add_usage(call, model, **n)
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
    u = _usage_from_body(parsed)
    if u is None and (p is not None or c is not None):
        # the input-count header excludes cached tokens, which have their own headers
        cr, cw = _int(headers.get("x-amzn-bedrock-cache-read-input-token-count")), _int(headers.get("x-amzn-bedrock-cache-write-input-token-count"))
        u = {"p": _int(p) + cr + cw, "c": _int(c), "cr": cr, "cw": cw}
    if u is not None:
        _add(call, model, u)
    finish_call(client, call, out)
    return res


def patch_bedrock() -> bool:
    try:
        mod = importlib.import_module("botocore.client")
    except ImportError:
        return False
    cls = getattr(mod, "BaseClient", None)
    return patch_attr(cls, "_make_api_call", _wrap) if cls is not None else False


