"""Bedrock Agents and Verdict tracing, the prompt registry, run linkage and
spans as a DataFrame.

    pip install -e sdk/python            # pandas is optional (for to_pandas)
    WEFT_SERVER_URL=http://localhost:6345 python examples/weft/python/agents_registry_and_queries.py

Runs offline: when boto3 is not installed (or has no AWS credentials) a
stand-in Bedrock Agents client replays a recorded ``invoke_agent`` stream
through the same patched ``botocore`` entry point, so the example shows what
gets recorded. Set ``BEDROCK_AGENT_ID`` / ``BEDROCK_AGENT_ALIAS_ID`` (and AWS
credentials) to call a real agent instead.
"""

import json
import os
import sys
import types
import uuid

# Retry/queue/timeout settings can come from the environment (WEFT_* variables).
os.environ.setdefault("WEFT_RETRY_MAX_ATTEMPTS", "5")
os.environ.setdefault("WEFT_HTTP_TIMEOUT", "20")

import weft  # noqa: E402

MODEL = "anthropic.claude-3-5-sonnet-20240620-v1:0"


def recorded_stream(session_id):
    """A shortened InvokeAgent response stream (enableTrace=True)."""

    def tr(inner):
        return {"trace": {"agentId": "WEATHER", "agentAliasId": "PROD", "sessionId": session_id, "trace": inner}}

    prompt = json.dumps({"system": "You are a weather agent.", "messages": [{"role": "user", "content": "Weather in Oslo?"}]})
    yield tr({"orchestrationTrace": {"modelInvocationInput": {"traceId": "t-0", "text": prompt, "foundationModel": MODEL}}})
    yield tr({"orchestrationTrace": {"modelInvocationOutput": {"traceId": "t-0", "rawResponse": {"content": "call get_weather"},
                                                               "metadata": {"usage": {"inputTokens": 420, "outputTokens": 25}}}}})
    yield tr({"orchestrationTrace": {"invocationInput": {"traceId": "t-0", "invocationType": "ACTION_GROUP", "actionGroupInvocationInput": {
        "actionGroupName": "weather", "function": "get_weather", "parameters": [{"name": "city", "value": "Oslo"}]}}}})
    yield tr({"orchestrationTrace": {"observation": {"traceId": "t-0", "actionGroupInvocationOutput": {"text": '{"temp_c": 12}'}}}})
    yield tr({"orchestrationTrace": {"modelInvocationInput": {"traceId": "t-1", "text": prompt, "foundationModel": MODEL}}})
    yield tr({"orchestrationTrace": {"modelInvocationOutput": {"traceId": "t-1", "rawResponse": {"content": "It is 12°C."},
                                                               "metadata": {"usage": {"inputTokens": 480, "outputTokens": 12}}}}})
    yield {"chunk": {"bytes": "It is 12°C in Oslo right now.".encode()}}


def bedrock_agent_client():
    if os.environ.get("BEDROCK_AGENT_ID"):
        import boto3

        return boto3.client("bedrock-agent-runtime"), os.environ["BEDROCK_AGENT_ID"], os.environ.get("BEDROCK_AGENT_ALIAS_ID", "TSTALIASID")

    # Stand-in: a botocore-like client; weft patches BaseClient._make_api_call, as for boto3.
    class BaseClient:
        meta = types.SimpleNamespace(service_model=types.SimpleNamespace(service_name="bedrock-agent-runtime"))

        def _make_api_call(self, operation_name, api_params):
            return {"completion": recorded_stream(api_params["sessionId"]), "sessionId": api_params["sessionId"], "contentType": "application/json"}

        def invoke_agent(self, **kw):
            return self._make_api_call("InvokeAgent", kw)

    if "botocore.client" not in sys.modules:
        sys.modules.setdefault("botocore", types.ModuleType("botocore"))
        mod = types.ModuleType("botocore.client")
        mod.BaseClient = BaseClient
        sys.modules["botocore.client"] = mod
    from weft.integrations import patch

    patch("bedrock")
    return BaseClient(), "WEATHER", "PROD"


def main():
    client = weft.init("examples-agents-registry", settings={"print_span_link": False})

    # --- Bedrock Agents: one conversation turn per invoke_agent ------------------
    br, agent_id, alias = bedrock_agent_client()
    session_id = f"session-{uuid.uuid4().hex[:8]}"
    res = br.invoke_agent(agentId=agent_id, agentAliasId=alias, sessionId=session_id, inputText="Weather in Oslo?", enableTrace=True)
    answer = "".join(e["chunk"]["bytes"].decode() for e in res["completion"] if "chunk" in e)
    print("agent:", answer)

    # --- Run linkage: spans carry run_id / run_step -----------------------
    step = {"n": 0}

    @weft.trace
    def train_step(batch: int) -> float:
        step["n"] += 1
        return 1.0 / (batch + 1)

    weft.set_run("demo-run", step=lambda: step["n"])
    for b in range(3):
        train_step(b)
    weft.set_run(None)

    # --- Registry: link a prompt version and a dataset --------------------------
    prompt = weft.StringPrompt("You are a concise weather assistant. Question: {q}")
    weft.save(prompt, name="weather-prompt")
    link = weft.link_prompt_to_registry(prompt, target_path="prompts-registry/weather", aliases=["production"])
    print("registry:", link)
    weft.link_to_registry(weft.Dataset(name="weather-questions", rows=[{"q": "Oslo?"}, {"q": "Lima?"}]),
                          target_path="datasets-registry/weather")
    prod = client.get_from_registry("prompts-registry/weather", "production")
    print("production prompt:", prod.format(q="Oslo?"))

    # --- Spans as records / a DataFrame ------------------------------------------
    client.flush()
    spans = client.spans(filter={"names": ["train_step"]}, sort_by=[{"field": "started_at", "direction": "asc"}])
    for r in spans.to_records():
        print(r["name"].rsplit("/", 1)[-1].split(":")[0], r["output"], r["run_id"], r["run_step"])
    try:
        df = spans.to_pandas(flatten=True)
        print(df[["inputs.batch", "output", "run_step"]])
    except ImportError as e:
        print(e)

    weft.finish()


if __name__ == "__main__":
    main()
