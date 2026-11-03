"""A local fake of the Anthropic Messages, OpenAI Chat Completions and OpenAI
Responses APIs (JSON and server-sent events), so the real client libraries
can be traced in tests. Responses are scripted per request by a handler."""

from __future__ import annotations

import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Callable, Dict, List, Optional

Handler = Callable[[str, Dict[str, Any]], Any]


class FakeModels:
    """``handler(path, body)`` returns a JSON-able response, or a list of SSE
    events (dicts, sent as ``data:`` lines; Anthropic events also get ``event:``)."""

    def __init__(self, handler: Handler) -> None:
        self.handler = handler
        self.requests: List[Dict[str, Any]] = []
        fake = self

        class H(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *a: Any) -> None:
                pass

            def do_POST(self) -> None:
                n = int(self.headers.get("Content-Length") or 0)
                body = json.loads(self.rfile.read(n) or b"{}")
                path = self.path.split("?", 1)[0]
                fake.requests.append({"path": path, "body": body})
                res = fake.handler(path, body)
                if isinstance(res, list):
                    chunks = []
                    for ev in res:
                        if path.endswith("/messages") and isinstance(ev, dict) and ev.get("type"):
                            chunks.append(f"event: {ev['type']}\n")
                        chunks.append(f"data: {json.dumps(ev)}\n\n")
                    if path.endswith("/chat/completions"):
                        chunks.append("data: [DONE]\n\n")
                    data = "".join(chunks).encode()
                    ctype = "text/event-stream"
                else:
                    data = json.dumps(res).encode()
                    ctype = "application/json"
                self.send_response(200)
                self.send_header("Content-Type", ctype)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), H)
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self) -> None:
        self.server.shutdown()
        self.server.server_close()


# ---------------------------------------------------------------- response builders


def anthropic_message(content: List[Dict[str, Any]], model: str = "claude-test", stop: str = "end_turn",
                      usage: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
    return {"id": "msg_1", "type": "message", "role": "assistant", "model": model, "content": content,
            "stop_reason": stop, "stop_sequence": None, "usage": usage or {"input_tokens": 30, "output_tokens": 20}}


def anthropic_events(content: List[Dict[str, Any]], model: str = "claude-test") -> List[Dict[str, Any]]:
    """The SSE events of a streamed Messages response with ``content``."""
    evs: List[Dict[str, Any]] = [{"type": "message_start", "message": {**anthropic_message([], model), "usage": {"input_tokens": 30, "output_tokens": 1}}}]
    for i, b in enumerate(content):
        if b["type"] == "thinking":
            evs.append({"type": "content_block_start", "index": i, "content_block": {"type": "thinking", "thinking": "", "signature": ""}})
            half = len(b["thinking"]) // 2
            for part in (b["thinking"][:half], b["thinking"][half:]):
                evs.append({"type": "content_block_delta", "index": i, "delta": {"type": "thinking_delta", "thinking": part}})
            evs.append({"type": "content_block_delta", "index": i, "delta": {"type": "signature_delta", "signature": b.get("signature", "sig")}})
        elif b["type"] == "redacted_thinking":
            evs.append({"type": "content_block_start", "index": i, "content_block": b})
        elif b["type"] == "text":
            evs.append({"type": "content_block_start", "index": i, "content_block": {"type": "text", "text": ""}})
            evs.append({"type": "content_block_delta", "index": i, "delta": {"type": "text_delta", "text": b["text"]}})
        elif b["type"] == "tool_use":
            evs.append({"type": "content_block_start", "index": i, "content_block": {**b, "input": {}}})
            evs.append({"type": "content_block_delta", "index": i, "delta": {"type": "input_json_delta", "partial_json": json.dumps(b["input"])}})
        evs.append({"type": "content_block_stop", "index": i})
    evs.append({"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence": None}, "usage": {"output_tokens": 25}})
    evs.append({"type": "message_stop"})
    return evs


def chat_completion(content: str, reasoning: Optional[str] = None, model: str = "deepseek-reasoner",
                    tool_calls: Optional[List[Dict[str, Any]]] = None) -> Dict[str, Any]:
    msg: Dict[str, Any] = {"role": "assistant", "content": content}
    if reasoning is not None:
        msg["reasoning_content"] = reasoning
    if tool_calls:
        msg["tool_calls"] = tool_calls
    return {"id": "chatcmpl-1", "object": "chat.completion", "created": 1, "model": model,
            "choices": [{"index": 0, "message": msg, "finish_reason": "tool_calls" if tool_calls else "stop"}],
            "usage": {"prompt_tokens": 12, "completion_tokens": 30, "total_tokens": 42, "completion_tokens_details": {"reasoning_tokens": 18}}}


def chat_chunks(content: str, reasoning: str, model: str = "deepseek-reasoner") -> List[Dict[str, Any]]:
    base = {"id": "chatcmpl-1", "object": "chat.completion.chunk", "created": 1, "model": model}
    out = [{**base, "choices": [{"index": 0, "delta": {"role": "assistant", "content": None, "reasoning_content": ""}, "finish_reason": None}]}]
    for part in (reasoning[: len(reasoning) // 2], reasoning[len(reasoning) // 2:]):
        out.append({**base, "choices": [{"index": 0, "delta": {"reasoning_content": part}, "finish_reason": None}]})
    out.append({**base, "choices": [{"index": 0, "delta": {"content": content}, "finish_reason": None}]})
    out.append({**base, "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]})
    out.append({**base, "choices": [], "usage": {"prompt_tokens": 12, "completion_tokens": 30, "total_tokens": 42,
                                                    "completion_tokens_details": {"reasoning_tokens": 18}}})
    return out


_RESP_N = [0]


def responses_response(output: List[Dict[str, Any]], model: str = "o4-mini", reasoning_tokens: int = 64) -> Dict[str, Any]:
    _RESP_N[0] += 1
    return {
        "id": f"resp_{_RESP_N[0]}", "object": "response", "created_at": 1.0, "status": "completed", "model": model,
        "output": output, "parallel_tool_calls": True, "tool_choice": "auto", "tools": [], "error": None,
        "incomplete_details": None, "instructions": None, "metadata": {}, "temperature": 1.0, "top_p": 1.0,
        "usage": {"input_tokens": 100, "output_tokens": 80, "total_tokens": 180,
                  "input_tokens_details": {"cached_tokens": 0}, "output_tokens_details": {"reasoning_tokens": reasoning_tokens}},
    }


def reasoning_item(summary: str, n: int = 1) -> Dict[str, Any]:
    return {"type": "reasoning", "id": f"rs_{n}", "summary": [{"type": "summary_text", "text": summary}], "encrypted_content": "gAAAA" + "x" * 40}


def function_call(name: str, args: Dict[str, Any], call_id: str) -> Dict[str, Any]:
    return {"type": "function_call", "id": f"fc_{call_id}", "call_id": call_id, "name": name, "arguments": json.dumps(args), "status": "completed"}


def message_item(text: str, n: int = 1) -> Dict[str, Any]:
    return {"type": "message", "id": f"msg_{n}", "role": "assistant", "status": "completed",
            "content": [{"type": "output_text", "text": text, "annotations": []}]}
