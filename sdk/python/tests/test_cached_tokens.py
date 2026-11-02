"""Prompt-cache counts reach summary.usage the way the server prices them
(docs/weft/API.md "Cached tokens"): a gross prompt_tokens with
cache_read_input_tokens / cache_creation_input_tokens as parts of it."""

import json

import pytest

from weft.integrations import _settings
from weft.integrations._common import finish_call
from weft.integrations.anthropic import MessageAccumulator, record_anthropic_usage
from weft.integrations.google_genai import GenerateContentAccumulator, record_genai_usage
from weft._usage import record_openai_usage


def _call(client, name="llm.test"):
    return client.create_call(_settings.integration_op("test", name, "llm"), {"model": "m"})


def _usage(fake, call_id):
    return fake.calls[call_id]["summary"]["usage"]


ANTHROPIC_USAGE = {"input_tokens": 1000, "output_tokens": 100, "cache_read_input_tokens": 9000, "cache_creation_input_tokens": 500}


def test_anthropic_message_and_stream(client, fake):
    c1 = _call(client)
    msg = {"id": "msg_1", "model": "claude-sonnet-4-5", "content": [{"type": "text", "text": "hi"}], "usage": ANTHROPIC_USAGE}
    record_anthropic_usage(c1, msg)
    finish_call(client, c1, msg)
    acc = MessageAccumulator()
    for ev in (
        {"type": "message_start", "message": {"id": "msg_2", "model": "claude-sonnet-4-5", "content": [],
                                               "usage": {"input_tokens": 1000, "output_tokens": 1, "cache_read_input_tokens": 9000, "cache_creation_input_tokens": 500}}},
        {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}},
        {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "hi"}},
        {"type": "content_block_stop", "index": 0},
        {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"output_tokens": 100}},
        {"type": "message_stop"},
    ):
        acc.add(ev)
    c2 = _call(client)
    out = acc.result()
    record_anthropic_usage(c2, out)
    finish_call(client, c2, out)
    client.flush()
    for c in (c1, c2):
        u = _usage(fake, c.id)["claude-sonnet-4-5"]
        assert u["prompt_tokens"] == 10500 and u["completion_tokens"] == 100 and u["total_tokens"] == 10600
        assert u["cache_read_input_tokens"] == 9000 and u["cache_creation_input_tokens"] == 500


def test_openai_chat_and_responses_keep_cached_tokens(client, fake):
    c1, c2, c3 = _call(client), _call(client), _call(client)
    record_openai_usage({"model": "gpt-4o", "usage": {"prompt_tokens": 2000, "completion_tokens": 50, "total_tokens": 2050,
                                                      "prompt_tokens_details": {"cached_tokens": 1536, "audio_tokens": 0},
                                                      "completion_tokens_details": {"reasoning_tokens": 10}}}, None, c1)
    record_openai_usage({"model": "gpt-5", "usage": {"input_tokens": 3000, "output_tokens": 70, "total_tokens": 3070,
                                                     "input_tokens_details": {"cached_tokens": 2048},
                                                     "output_tokens_details": {"reasoning_tokens": 40}}}, None, c2)
    record_openai_usage({"model": "router/x", "usage": {"prompt_tokens": 100, "completion_tokens": 5,
                                                        "prompt_tokens_details": {"cached_tokens": 0, "cache_write_tokens": 80}}}, None, c3)
    for c in (c1, c2, c3):
        finish_call(client, c, None)
    client.flush()
    chat = _usage(fake, c1.id)["gpt-4o"]
    assert chat["prompt_tokens"] == 2000 and chat["cache_read_input_tokens"] == 1536
    assert chat["prompt_tokens_details"] == {"cached_tokens": 1536} and chat["reasoning_tokens"] == 10
    resp = _usage(fake, c2.id)["gpt-5"]
    assert resp["prompt_tokens"] == 3000 and resp["cache_read_input_tokens"] == 2048
    assert resp["input_tokens_details"] == {"cached_tokens": 2048} and resp["reasoning_tokens"] == 40
    router = _usage(fake, c3.id)["router/x"]
    assert router["cache_creation_input_tokens"] == 80 and "cache_read_input_tokens" not in router


def test_gemini_cached_content(client, fake):
    c1, c2 = _call(client), _call(client)
    out = {"model_version": "gemini-2.5-pro", "usage_metadata": {"prompt_token_count": 5000, "candidates_token_count": 200,
                                                                 "thoughts_token_count": 50, "total_token_count": 5250,
                                                                 "cached_content_token_count": 4096}}
    record_genai_usage(c1, out)
    camel = {"modelVersion": "gemini-2.5-pro", "usageMetadata": {"promptTokenCount": 5000, "candidatesTokenCount": 200,
                                                                 "totalTokenCount": 5200, "cachedContentTokenCount": 4096}}
    record_genai_usage(c2, camel)
    for c in (c1, c2):
        finish_call(client, c, None)
    client.flush()
    u1 = _usage(fake, c1.id)["m"]
    assert u1["prompt_tokens"] == 5000 and u1["completion_tokens"] == 250 and u1["cache_read_input_tokens"] == 4096 and u1["reasoning_tokens"] == 50
    u2 = _usage(fake, c2.id)["m"]
    assert u2["prompt_tokens"] == 5000 and u2["cache_read_input_tokens"] == 4096 and u2["total_tokens"] == 5200
    # the stream accumulator keeps the metadata it saw last
    acc = GenerateContentAccumulator()
    acc.add({"candidates": [{"index": 0, "content": {"parts": [{"text": "a"}]}}]})
    acc.add({"candidates": [{"index": 0, "content": {"parts": [{"text": "b"}]}}], "usage_metadata": out["usage_metadata"]})
    assert acc.result()["usage_metadata"]["cached_content_token_count"] == 4096


def test_bedrock_converse_streams_and_invoke(client, fake):
    from weft.integrations import bedrock as br

    model = "anthropic.claude-3-5-sonnet-20240620-v1:0"
    converse = {"output": {"message": {"role": "assistant", "content": [{"text": "hi"}]}}, "stopReason": "end_turn",
                "usage": {"inputTokens": 1000, "outputTokens": 100, "totalTokens": 10600, "cacheReadInputTokens": 9000, "cacheWriteInputTokens": 500}}
    c1 = _call(client)
    br._handle(client, c1, "Converse", model, dict(converse))
    c2 = _call(client)
    stream = br._StreamProxy(iter([
        {"messageStart": {"role": "assistant"}},
        {"contentBlockDelta": {"delta": {"text": "hi"}}},
        {"messageStop": {"stopReason": "end_turn"}},
        {"metadata": {"usage": converse["usage"]}},
    ]), client, c2, model, converse=True)
    list(stream)
    c3 = _call(client)
    chunks = [
        {"type": "message_start", "message": {"model": "claude", "usage": {"input_tokens": 1000, "output_tokens": 1,
                                                                          "cache_read_input_tokens": 9000, "cache_creation_input_tokens": 500}}},
        {"type": "content_block_delta", "delta": {"type": "text_delta", "text": "hi"}},
        {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"output_tokens": 100}},
        {"type": "message_stop", "amazon-bedrock-invocationMetrics": {"inputTokenCount": 1000, "outputTokenCount": 100,
                                                                     "cacheReadInputTokenCount": 9000, "cacheWriteInputTokenCount": 500}},
    ]
    list(br._StreamProxy(iter([{"chunk": {"bytes": json.dumps(c).encode()}} for c in chunks]), client, c3, model, converse=False))
    c4 = _call(client)
    body = {"id": "msg", "content": [{"type": "text", "text": "hi"}], "usage": ANTHROPIC_USAGE}
    br._handle(client, c4, "InvokeModel", model, {"body": json.dumps(body).encode(), "ResponseMetadata": {"HTTPHeaders": {
        "x-amzn-bedrock-input-token-count": "1000", "x-amzn-bedrock-output-token-count": "100"}}})
    c5 = _call(client)
    br._handle(client, c5, "InvokeModel", "amazon.titan", {"body": b'{"results": []}', "ResponseMetadata": {"HTTPHeaders": {
        "x-amzn-bedrock-input-token-count": "1000", "x-amzn-bedrock-output-token-count": "100",
        "x-amzn-bedrock-cache-read-input-token-count": "9000", "x-amzn-bedrock-cache-write-input-token-count": "500"}}})
    client.flush()
    for c, m in ((c1, model), (c2, model), (c3, model), (c4, model), (c5, "amazon.titan")):
        u = _usage(fake, c.id)[m]
        assert u["prompt_tokens"] == 10500 and u["completion_tokens"] == 100, (c.id, u)
        assert u["cache_read_input_tokens"] == 9000 and u["cache_creation_input_tokens"] == 500
    assert _usage(fake, c1.id)[model]["total_tokens"] == 10600
