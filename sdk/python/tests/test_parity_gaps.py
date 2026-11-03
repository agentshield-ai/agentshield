"""Bedrock Agents, Verdict, CallsIter.to_pandas(), registry linking, run
linkage and the retry/queue/timeout/log-level environment variables, against
fake modules and the fake server."""

import contextlib
import json
import logging
import sys
import types

import pytest

import weft
from weft.integrations import patch_bedrock, patch_verdict, unpatch_all


def _mods(monkeypatch, **mods):
    for name, attrs in mods.items():
        m = types.ModuleType(name)
        for k, v in attrs.items():
            setattr(m, k, v)
        monkeypatch.setitem(sys.modules, name, m)


@pytest.fixture(autouse=True)
def _unpatch():
    yield
    unpatch_all()


def kind(c):
    return (c.get("attributes") or {}).get("weft", {}).get("span_kind")


def usage(c):
    return (c.get("summary") or {}).get("usage")


# ---------------------------------------------------------------- Bedrock Agents
MODEL = "anthropic.claude-3-5-sonnet-20240620-v1:0"


def _trace(inner, **outer):
    return {"trace": {"agentId": "AGENT1", "agentAliasId": "ALIAS1", "sessionId": "sess-42", "trace": inner, **outer}}


def _model_io(step, tid, usage_, content, **extra_out):
    prompt = json.dumps({"system": "You are a weather agent.", "messages": [{"role": "user", "content": "Weather in Oslo?"}]})
    return [
        _trace({step: {"modelInvocationInput": {"traceId": tid, "text": prompt, "type": "ORCHESTRATION", "foundationModel": MODEL,
                                                "inferenceConfiguration": {"temperature": 0}}}}),
        _trace({step: {"modelInvocationOutput": {"traceId": tid, "rawResponse": {"content": content},
                                                 "metadata": {"usage": {"inputTokens": usage_[0], "outputTokens": usage_[1]}}, **extra_out}}}),
    ]


def agent_events():
    ev = []
    ev += _model_io("preProcessingTrace", "t-pre", (100, 10), "<thinking>ok</thinking>", parsedResponse={"isValid": True, "rationale": "fine"})
    ev += _model_io("orchestrationTrace", "t-0", (200, 20), "call get_weather")
    ev.append(_trace({"orchestrationTrace": {"rationale": {"traceId": "t-0", "text": "I need the weather."}}}))
    ev.append(_trace({"orchestrationTrace": {"invocationInput": {"traceId": "t-0", "invocationType": "ACTION_GROUP", "actionGroupInvocationInput": {
        "actionGroupName": "weather", "function": "get_weather", "parameters": [{"name": "city", "type": "string", "value": "Oslo"}],
        "executionType": "LAMBDA", "invocationId": "inv-1"}}}}))
    ev.append(_trace({"orchestrationTrace": {"observation": {"traceId": "t-0", "type": "ACTION_GROUP",
                                                             "actionGroupInvocationOutput": {"text": '{"temp": 12}'}}}}))
    ev += _model_io("orchestrationTrace", "t-1", (250, 30), "search the kb")
    ev.append(_trace({"orchestrationTrace": {"invocationInput": {"traceId": "t-1", "invocationType": "KNOWLEDGE_BASE",
                                                                 "knowledgeBaseLookupInput": {"knowledgeBaseId": "KB1", "text": "Oslo climate"}}}}))
    ev.append(_trace({"orchestrationTrace": {"observation": {"traceId": "t-1", "type": "KNOWLEDGE_BASE", "knowledgeBaseLookupOutput": {
        "retrievedReferences": [{"content": {"text": "Oslo is cold."}, "location": {"s3Location": {"uri": "s3://kb/oslo.txt"}}}]}}}}))
    ev.append(_trace({"orchestrationTrace": {"invocationInput": {"traceId": "t-2", "invocationType": "AGENT_COLLABORATOR",
                                                                 "agentCollaboratorInvocationInput": {"agentCollaboratorName": "researcher",
                                                                                                      "input": {"text": "Forecast for Oslo?"}}}}}))
    chain = [{"agentAliasArn": "arn:sup"}, {"agentAliasArn": "arn:researcher"}]
    for e in _model_io("orchestrationTrace", "t-c0", (50, 5), "rain tomorrow"):
        e["trace"].update(collaboratorName="researcher", callerChain=chain)
        ev.append(e)
    ev.append(_trace({"orchestrationTrace": {"observation": {"traceId": "t-2", "type": "AGENT_COLLABORATOR", "agentCollaboratorInvocationOutput": {
        "agentCollaboratorName": "researcher", "output": {"text": "Rain tomorrow."}}}}}))
    ev.append(_trace({"guardrailTrace": {"action": "NONE", "traceId": "t-g", "inputAssessments": [{}]}}))
    ev += _model_io("orchestrationTrace", "t-3", (300, 40), "final")
    ev.append(_trace({"orchestrationTrace": {"observation": {"traceId": "t-3", "type": "FINISH",
                                                             "finalResponse": {"text": "It is 12°C in Oslo; rain tomorrow."}}}}))
    ev.append({"chunk": {"bytes": "It is 12°C in Oslo; ".encode(), "attribution": {"citations": [{"retrievedReferences": [{"location": "s3://kb/oslo.txt"}]}]}}})
    ev.append({"chunk": {"bytes": b"rain tomorrow."}})
    return ev


class _AgentRuntime:
    """botocore-like client for bedrock-agent-runtime."""

    def __init__(self, events, service="bedrock-agent-runtime"):
        self.meta = types.SimpleNamespace(service_model=types.SimpleNamespace(service_name=service))
        self.events = events

    def _make_api_call(self, operation_name, api_params):
        if operation_name in ("InvokeAgent", "InvokeInlineAgent"):
            evs = self.events

            def gen():
                for e in evs:
                    if isinstance(e, BaseException):
                        raise e
                    yield e

            return {"completion": gen(), "contentType": "application/json", "sessionId": api_params.get("sessionId"),
                    "ResponseMetadata": {"RequestId": "r"}}
        return {"other": True}


def _install_botocore(monkeypatch, cls):
    _mods(monkeypatch, botocore={}, **{"botocore.client": {"BaseClient": cls}})


def test_bedrock_invoke_agent_trace_events_become_spans(client, fake, monkeypatch):
    _install_botocore(monkeypatch, _AgentRuntime)
    assert patch_bedrock()
    rt = _AgentRuntime(agent_events())
    res = rt._make_api_call("InvokeAgent", {"agentId": "AGENT1", "agentAliasId": "ALIAS1", "sessionId": "sess-42",
                                            "inputText": "Weather in Oslo?", "enableTrace": True})
    text = "".join(e["chunk"]["bytes"].decode() for e in res["completion"] if "chunk" in e)
    assert text == "It is 12°C in Oslo; rain tomorrow."
    client.flush()

    [root] = fake.calls_by_op("bedrock_agent.invoke_agent")
    assert kind(root) == "agent" and root["parent_id"] is None
    assert root["thread_id"] == "sess-42" and root["turn_id"] == root["id"], "a Bedrock session is a conversation"
    a = root["attributes"]
    assert a["gen_ai.agent.name"] == "AGENT1" and a["gen_ai.agent.version"] == "ALIAS1" and a["gen_ai.conversation.id"] == "sess-42"
    assert root["display_name"] == "invoke_agent AGENT1" and root["inputs"]["inputText"] == "Weather in Oslo?"
    out = root["output"]
    assert out["completion"] == text and out["sessionId"] == "sess-42" and out["foundation_model"] == MODEL
    assert out["citations"][0]["retrievedReferences"][0]["location"] == "s3://kb/oslo.txt"
    assert out["usage"] == {"input_tokens": 900, "output_tokens": 105} and out["rationales"] == ["I need the weather."]
    assert not root.get("exception")

    llms = fake.calls_by_op("bedrock_agent.model_invocation")
    assert len(llms) == 5
    assert all(kind(c) == "llm" and c["thread_id"] == "sess-42" and c["turn_id"] == root["id"] for c in llms)
    pre = llms[0]
    assert pre["parent_id"] == root["id"] and pre["inputs"]["stage"] == "pre_processing"
    assert pre["inputs"]["system"] == "You are a weather agent." and pre["inputs"]["messages"][0]["content"] == "Weather in Oslo?"
    assert pre["output"]["parsed_response"] == {"isValid": True, "rationale": "fine"}
    assert usage(pre)[MODEL] == {"requests": 1, "prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110}
    assert pre["attributes"]["gen_ai.request.model"] == MODEL and pre["display_name"] == f"chat {MODEL}"

    [tool] = fake.calls_by_op("bedrock_agent.action_group")
    assert kind(tool) == "tool" and tool["parent_id"] == root["id"]
    assert tool["inputs"] == {"action_group": "weather", "function": "get_weather", "parameters": {"city": "Oslo"}, "execution_type": "LAMBDA"}
    assert tool["output"] == {"temp": 12} and tool["attributes"]["gen_ai.tool.name"] == "weather.get_weather"
    assert tool["attributes"]["gen_ai.tool.call.id"] == "inv-1" and tool["display_name"] == "execute_tool weather.get_weather"

    [kb] = fake.calls_by_op("bedrock_agent.knowledge_base")
    assert kind(kb) == "retriever" and kb["inputs"]["query"] == "Oslo climate" and kb["output"]["count"] == 1

    [collab] = fake.calls_by_op("bedrock_agent.collaborator")
    assert kind(collab) == "agent" and collab["parent_id"] == root["id"] and collab["attributes"]["gen_ai.agent.name"] == "researcher"
    assert collab["inputs"]["input"] == "Forecast for Oslo?" and collab["output"] == "Rain tomorrow."
    inner = [c for c in llms if c["parent_id"] == collab["id"]]
    assert len(inner) == 1 and usage(inner[0])[MODEL]["prompt_tokens"] == 50, "the collaborator's own trace nests under it"

    [g] = fake.calls_by_op("bedrock_agent.guardrail")
    assert kind(g) == "guardrail" and g["output"]["action"] == "NONE"
    assert all(c.get("ended_at") for c in fake.calls.values())


def test_bedrock_agent_failure_return_control_and_untouched_services(client, fake, monkeypatch):
    _install_botocore(monkeypatch, _AgentRuntime)
    patch_bedrock()
    events = _model_io("orchestrationTrace", "t-0", (10, 1), "x")[:1] + [
        _trace({"failureTrace": {"traceId": "t-0", "failureReason": "Lambda timed out"}}),
    ]
    list(_AgentRuntime(events)._make_api_call("InvokeAgent", {"agentId": "A", "agentAliasId": "B", "sessionId": "s1", "inputText": "hi"})["completion"])
    rc = [{"returnControl": {"invocationId": "rc-1", "invocationInputs": [{"functionInvocationInput": {
        "actionGroup": "billing", "function": "refund", "parameters": [{"name": "order", "value": "42"}]}}]}}]
    res = _AgentRuntime(rc)._make_api_call("InvokeInlineAgent", {"sessionId": "s2", "foundationModel": MODEL, "instruction": "help", "inputText": "refund 42"})
    stream = res["completion"]
    next(iter(stream))
    stream.close()  # closing early still ends the call
    with pytest.raises(RuntimeError, match="stream broke"):
        list(_AgentRuntime([RuntimeError("stream broke")])._make_api_call("InvokeAgent", {"agentId": "A", "agentAliasId": "B", "sessionId": "s3", "inputText": "x"})["completion"])
    assert _AgentRuntime([], service="bedrock-agent")._make_api_call("InvokeAgent", {}) is not None
    client.flush()
    roots = {c["inputs"].get("sessionId"): c for c in fake.calls.values() if fake.op_base(c).startswith("bedrock_agent.invoke")}
    assert len(roots) == 3
    assert roots["s1"]["exception"].startswith("RuntimeError: Lambda timed out")
    [llm] = fake.calls_by_op("bedrock_agent.model_invocation")
    assert llm["exception"].startswith("RuntimeError: Lambda timed out")
    inline = roots["s2"]
    assert fake.op_base(inline) == "bedrock_agent.invoke_inline_agent" and inline["attributes"]["gen_ai.request.model"] == MODEL
    assert inline["output"]["return_control"][0]["invocations"][0] == {"tool": "billing.refund", "action_group": "billing", "parameters": {"order": "42"}, "function": "refund"}
    [rcall] = fake.calls_by_op("bedrock_agent.return_control")
    assert rcall["parent_id"] == inline["id"] and rcall["output"]["status"] == "returned_to_application"
    assert roots["s3"]["exception"].startswith("RuntimeError: stream broke")


def test_bedrock_agent_respects_autopatch_settings(client, fake, monkeypatch):
    from weft.integrations import _settings

    _install_botocore(monkeypatch, _AgentRuntime)
    patch_bedrock()
    _settings.configure({"bedrock": {"enabled": False}})
    try:
        res = _AgentRuntime(agent_events())._make_api_call("InvokeAgent", {"agentId": "A", "agentAliasId": "B", "sessionId": "s", "inputText": "x"})
        assert len(list(res["completion"])) == len(agent_events())
    finally:
        _settings.configure(None)
    client.flush()
    assert not fake.calls


# ---------------------------------------------------------------- Verdict
_TRACING_SRC = '''
import contextvars, uuid
from contextlib import contextmanager
from dataclasses import dataclass, field
from typing import Any, Optional

@dataclass
class TraceContext:
    trace_id: str
    call_id: str
    parent_id: Optional[str]

current_trace_context = contextvars.ContextVar("current_trace_context", default=None)

@dataclass
class Call:
    name: str
    inputs: dict
    trace_id: str = field(default_factory=lambda: str(uuid.uuid4()))
    call_id: str = field(default_factory=lambda: str(uuid.uuid4()))
    parent_id: Optional[str] = None
    outputs: Any = None
    exception: Any = None
    def set_outputs(self, outputs):
        self.outputs = outputs

class Tracer:
    def start_call(self, name, inputs, trace_id=None, parent_id=None):
        raise NotImplementedError

class NoOpTracer(Tracer):
    @contextmanager
    def start_call(self, name, inputs, trace_id=None, parent_id=None):
        token = current_trace_context.set(TraceContext(trace_id, str(uuid.uuid4()), parent_id))
        try:
            yield None
        finally:
            current_trace_context.reset(token)

class TracingManager(Tracer):
    def __init__(self, tracers):
        self.tracers = tracers
    @contextmanager
    def start_call(self, name, inputs, trace_id=None, parent_id=None):
        parent_ctx = current_trace_context.get()
        if parent_id is None and parent_ctx is not None:
            parent_id = parent_ctx.call_id
        if trace_id is None and parent_ctx is not None:
            trace_id = parent_ctx.trace_id
        contexts = [t.start_call(name, inputs, trace_id, parent_id) for t in self.tracers]
        calls = []
        token = None
        try:
            for ctx in contexts:
                calls.append(ctx.__enter__())
            call_id = calls[0].call_id if calls and calls[0] is not None else str(uuid.uuid4())
            token = current_trace_context.set(TraceContext(trace_id, call_id, parent_id))
            yield calls[0] if calls else None
        except Exception as e:
            for ctx in contexts:
                ctx.__exit__(type(e), e, e.__traceback__)
            raise
        else:
            for ctx in contexts:
                ctx.__exit__(None, None, None)
        finally:
            if token is not None:
                current_trace_context.reset(token)

def ensure_tracing_manager(tracer):
    if tracer is None:
        return TracingManager([NoOpTracer()])
    if isinstance(tracer, TracingManager):
        return tracer
    if isinstance(tracer, list):
        return TracingManager(tracer)
    return TracingManager([tracer])

@dataclass
class ExecutionContext:
    tracer: Any = field(default_factory=NoOpTracer)
    trace_id: str = field(default_factory=lambda: str(uuid.uuid4()))
    call_id: str = field(default_factory=lambda: str(uuid.uuid4()))
    parent_id: Optional[str] = None
    @contextmanager
    def trace_call(self, name, inputs):
        with self.tracer.start_call(name=name, inputs=inputs, trace_id=self.trace_id, parent_id=self.parent_id) as call:
            yield call
'''

_PIPELINE_SRC = '''
from concurrent.futures import ThreadPoolExecutor
from verdict.util.tracing import ExecutionContext, Tracer, ensure_tracing_manager

class JudgeUnit:
    def __init__(self, judge):
        self.judge = judge
    def execute(self, x, ctx):
        with ctx.trace_call("JudgeUnit", {"input": {"text": x}, "unit": self}) as call:
            score = self.judge(x)
            if call is not None:
                call.set_outputs({"score": score})
            return score

class Pipeline:
    def __init__(self, name="Pipeline", tracer=None):
        self.name = name
        self.default_tracer = ensure_tracing_manager(tracer)
        self.units = []
    def __rshift__(self, unit):
        self.units.append(unit)
        return self
    def run(self, x, tracers=None):
        if tracers is not None:
            self.default_tracer = ensure_tracing_manager(tracers)
        ctx = ExecutionContext(tracer=self.default_tracer, parent_id=None)
        with ctx.trace_call(self.name, {"input_data": {"text": x}, "max_workers": 2}) as call:
            child = ExecutionContext(tracer=ctx.tracer, trace_id=ctx.trace_id, parent_id=call.call_id if call is not None else None)
            with ThreadPoolExecutor(2) as pool:  # units run in worker threads, as in Verdict
                scores = list(pool.map(lambda u: u.execute(x, child), self.units))
            out = {"scores": scores}
            if call is not None:
                call.set_outputs(out)
            return out


class ChainPipeline(Pipeline):
    """``a >> b`` as Verdict's GraphExecutor runs it: when a block finishes,
    the next block starts in a new ExecutionContext whose parent_id is the
    *finished* block's call id (``_on_task_complete``)."""
    def run(self, x, tracers=None):
        ctx = ExecutionContext(tracer=self.default_tracer, parent_id=None)
        with ctx.trace_call(self.name, {"input_data": {"text": x}}) as call:
            parent = call.call_id if call is not None else None
            for u in self.units:
                child = ExecutionContext(tracer=ctx.tracer, trace_id=ctx.trace_id, parent_id=parent)
                ids = []
                def run_unit(u=u, child=child):
                    with child.trace_call("MapUnit", {"input": x, "unit": u}) as c:
                        ids.append(c.call_id if c is not None else None)
                        return u.judge(x)
                with ThreadPoolExecutor(1) as pool:  # each block in a worker thread, after the previous one ended
                    x = pool.submit(run_unit).result()
                parent = ids[0]
            if call is not None:
                call.set_outputs(x)
            return x
'''


def _install_verdict(monkeypatch):
    mods = {}
    for name in ("verdict", "verdict.util", "verdict.core"):
        mods[name] = types.ModuleType(name)
    tracing = types.ModuleType("verdict.util.tracing")
    pipeline = types.ModuleType("verdict.core.pipeline")
    for name, m in {**mods, "verdict.util.tracing": tracing, "verdict.core.pipeline": pipeline}.items():
        monkeypatch.setitem(sys.modules, name, m)
    exec(_TRACING_SRC, tracing.__dict__)
    exec(_PIPELINE_SRC, pipeline.__dict__)
    return tracing, pipeline


def test_verdict_pipeline_and_units_are_traced(client, fake, monkeypatch):
    tracing, vp = _install_verdict(monkeypatch)
    assert patch_verdict()

    @weft.op(name="openai.chat.completions.create", kind="llm")
    def judge_llm(text):
        return 4 if "good" in text else 1

    @weft.op
    def evaluate(text):
        p = vp.Pipeline("SafetyJudge") >> vp.JudgeUnit(lambda x: judge_llm(x)) >> vp.JudgeUnit(lambda x: 5)
        return p.run(text)

    assert evaluate("a good answer") == {"scores": [4, 5]}
    client.flush()
    [outer] = fake.calls_by_op("evaluate")
    [pipe] = fake.calls_by_op("verdict.SafetyJudge")
    assert pipe["parent_id"] == outer["id"] and kind(pipe) == "workflow"
    assert pipe["inputs"]["input_data"] == {"text": "a good answer"} and pipe["output"] == {"scores": [4, 5]}
    assert pipe["attributes"]["weft"]["integration"] == "verdict" and pipe["display_name"] == "SafetyJudge"
    units = fake.calls_by_op("verdict.JudgeUnit")
    assert len(units) == 2 and all(u["parent_id"] == pipe["id"] and kind(u) == "scorer" for u in units)
    assert sorted(u["output"]["score"] for u in units) == [4, 5]
    assert units[0]["inputs"]["unit"]["_type"] == "JudgeUnit", "units are summarised, not dumped"
    [llm] = fake.calls_by_op("openai.chat.completions.create")
    assert llm["parent_id"] in {u["id"] for u in units}, "LLM calls in a unit's worker thread nest under the unit"

    # an explicit tracer is kept, Weft's is added first; run(tracers=...) keeps Weft's too
    class Recorder(tracing.Tracer):
        names = []

        @contextlib.contextmanager
        def start_call(self, name, inputs, trace_id=None, parent_id=None):
            self.names.append(name)
            yield None

    p2 = vp.Pipeline("P2", tracer=Recorder())
    assert type(p2.default_tracer.tracers[0]).__name__ == "WeftVerdictTracer"
    p2 >> vp.JudgeUnit(lambda x: 2)
    p2.run("x", tracers=[Recorder()])
    client.flush()
    assert Recorder.names == ["P2", "JudgeUnit"] and fake.calls_by_op("verdict.P2")


def _tree(calls):
    """{display_name: [(id, parent_id, trace_id)]}"""
    out = {}
    for c in calls:
        out.setdefault(c["display_name"], []).append((c["id"], c.get("parent_id"), c["trace_id"]))
    return out


def test_verdict_chained_blocks_stay_in_one_trace(client, fake, monkeypatch):
    """Regression (parity audit A6): the second block's parent is the finished first block."""
    _, vp = _install_verdict(monkeypatch)
    assert patch_verdict()
    p = vp.ChainPipeline("double") >> vp.JudgeUnit(lambda x: x * 2) >> vp.JudgeUnit(lambda x: x + 1)
    assert p.run(3) == 7
    p.run(10)  # a second run is a second, separate trace
    client.flush()
    roots = fake.calls_by_op("verdict.double")
    assert len(roots) == 2 and all(r.get("parent_id") is None for r in roots)
    for root in roots:
        trace = [c for c in fake.calls.values() if c["trace_id"] == root["trace_id"]]
        first, second = sorted((c for c in trace if c["display_name"] == "MapUnit"), key=lambda c: c["started_at"])
        assert first["parent_id"] == root["id"] and second["parent_id"] == first["id"]
        assert len(trace) == 3
    from weft.integrations import verdict as wv
    assert wv.get_tracer()._calls == {} and wv.get_tracer()._owners == {}, "finished traces are released"


def test_verdict_real_library_chain(client, fake):
    """The real ``verdict`` package: one trace per pipeline run."""
    pytest.importorskip("verdict")
    from verdict import Pipeline
    from verdict.schema import Schema
    from verdict.transform import MapUnit

    assert patch_verdict()
    p = Pipeline("double") >> MapUnit(lambda x: Schema.of(values=x.values * 2)) >> MapUnit(lambda x: Schema.of(values=x.values + 1))
    res, _ = p.run(Schema.of(values=3), display=False)
    assert sorted(res.values()) == [6, 7]
    client.flush()
    calls = list(fake.calls.values())
    assert len(calls) == 5 and len({c["trace_id"] for c in calls}) == 1, _tree(calls)
    [root] = [c for c in calls if c.get("parent_id") is None]
    assert root["op_name"].endswith("verdict.double") or root["display_name"] == "double"
    maps = sorted((c for c in calls if c["display_name"] == "Map"), key=lambda c: c["started_at"])
    assert len(maps) == 2 and maps[0]["parent_id"] == root["id"] and maps[1]["parent_id"] == maps[0]["id"]
    ids = {c["id"] for c in calls}
    assert all(c["parent_id"] in ids for c in calls if c is not root)
    from weft.integrations import verdict as wv
    assert wv.get_tracer()._calls == {}


def test_verdict_unit_errors_and_disabled(client, fake, monkeypatch):
    from weft.integrations import _settings

    _, vp = _install_verdict(monkeypatch)
    patch_verdict()

    def boom(x):
        raise ValueError("judge failed")

    with pytest.raises(ValueError):
        (vp.Pipeline("Bad") >> vp.JudgeUnit(boom)).run("x")
    client.flush()
    [unit] = fake.calls_by_op("verdict.JudgeUnit")
    assert unit["exception"].startswith("ValueError: judge failed")
    assert fake.calls_by_op("verdict.Bad")[0]["exception"]
    _settings.configure({"verdict": {"enabled": False}})
    try:
        (vp.Pipeline("Off") >> vp.JudgeUnit(lambda x: 1)).run("x")
    finally:
        _settings.configure(None)
    client.flush()
    assert not fake.calls_by_op("verdict.Off")


# ---------------------------------------------------------------- CallsIter.to_pandas
def test_calls_to_records_and_to_pandas(client, fake, monkeypatch):
    @weft.op
    def f(x: int) -> int:
        return x + 1

    f(1)
    f(2)
    recs = client.get_calls(filter={"op_names": ["f"]}).to_records()
    assert sorted(r["output"] for r in recs) == [2, 3]
    assert set(recs[0]) >= {"id", "op_name", "inputs", "output", "status", "trace_id", "run_step"}
    assert recs[0]["status"] == "success"

    monkeypatch.setitem(sys.modules, "pandas", None)
    with pytest.raises(ImportError, match="pip install pandas"):
        client.get_calls().to_pandas()

    seen = {}

    def DataFrame(records):
        seen["df"] = records
        return ("df", len(records))

    def json_normalize(records):
        seen["flat"] = records
        return ("flat", len(records))

    _mods(monkeypatch, pandas={"DataFrame": DataFrame, "json_normalize": json_normalize})
    assert client.get_calls(filter={"op_names": ["f"]}).to_pandas() == ("df", 2)
    assert client.get_calls(filter={"op_names": ["f"]}, limit=1).to_pandas(flatten=True) == ("flat", 1)
    assert seen["df"][0]["inputs"] in ({"x": 1}, {"x": 2})


def test_get_calls_passes_usernames_and_storage_size(client, fake):
    list(client.get_calls(include_usernames=True, include_storage_size=True))
    [q] = [r for r in fake.requests if r["path"] == "/calls/stream_query"]
    assert q["body"]["include_usernames"] is True and q["body"]["include_storage_size"] is True


# ---------------------------------------------------------------- registry
def test_link_prompt_and_any_object_to_registry(client, fake):
    prompt = weft.StringPrompt("Answer {q} briefly.")
    weft.publish(prompt, name="support-prompt")
    res = weft.link_prompt_to_registry(prompt, target_path="prompts/support", aliases=["production"])
    assert res["version_index"] == 0 and res["target_path"] == "test-entity/prompts/support"
    req = [r for r in fake.requests if r["path"] == "/link_to_registry"][-1]["body"]
    assert req["ref"] == prompt._weft_ref.uri() and req["aliases"] == ["production"]
    assert req["target"] == {"portfolio_name": "support", "entity_name": "test-entity", "project_name": "prompts"}

    ds = weft.Dataset(name="golden", rows=[{"q": "1+1", "a": "2"}])
    assert weft.link_to_registry(ds, target_path="other-team/datasets-registry/golden-sets")["version_index"] == 0  # published on the fly
    assert weft.link_to_registry("support-prompt:v0", target_path="prompts/support")["version_index"] == 0, "same version, same index"
    prompt2 = weft.StringPrompt("Answer {q} in one word.")
    weft.publish(prompt2, name="support-prompt")
    assert weft.link_to_registry(prompt2._weft_ref.uri(), target_path="prompts/support", aliases=["production"])["version_index"] == 1
    [col] = client.query_registry("prompts", "support")
    assert [v["version_index"] for v in col["versions"]] == [1, 0]
    [prod] = client.query_registry("prompts", "support", alias="production")[0]["versions"]
    assert prod["version_index"] == 1
    assert client.get_from_registry("prompts/support", "production").format(q="x") == "Answer x in one word."
    assert client.get_from_registry("prompts/support", "v0").format(q="x") == "Answer x briefly."
    with pytest.raises(ValueError, match="target_path"):
        weft.link_to_registry(prompt, target_path="no-collection")
    with pytest.raises(TypeError):
        client.link_to_registry(weft.CallRef("e", "p", "c1"), target_path="r/c")


# ---------------------------------------------------------------- run linkage
def test_run_id_and_steps_are_sent(client, fake, monkeypatch):
    step = {"n": 3}

    @weft.op
    def train_step():
        step["n"] += 1
        return step["n"]

    weft.set_run("run-abc", step=lambda: step["n"])
    train_step()
    weft.set_run(None)
    train_step()
    weft.set_run("acme/train/run-2", step=7)
    train_step()
    client.flush()
    c1, c2, c3 = fake.calls_by_op("train_step")
    assert c1["run_id"] == "test-entity/test-proj/run-abc" and c1["run_step"] == 3 and c1["run_step_end"] == 4
    assert "run_id" not in c2
    assert c3["run_id"] == "acme/train/run-2" and c3["run_step"] == 7
    assert weft.Call.from_server({"id": "x", "run_step": 2, "username": "ann"}, None).to_dict()["username"] == "ann"


# ---------------------------------------------------------------- environment variables
def test_retry_queue_timeout_and_log_level_env(fake, monkeypatch):
    monkeypatch.setenv("WEFT_RETRY_MAX_ATTEMPTS", "7")
    monkeypatch.setenv("WEFT_RETRY_BACKOFF", "0.5")
    monkeypatch.setenv("WEFT_RETRY_MAX_INTERVAL", "9")
    monkeypatch.setenv("WEFT_MAX_CALLS_QUEUE_SIZE", "123")
    monkeypatch.setenv("WEFT_HTTP_TIMEOUT", "4.5")
    monkeypatch.setenv("WEFT_LOG_LEVEL", "debug")
    lg = logging.getLogger("weft")
    old_level, old_handlers = lg.level, list(lg.handlers)
    try:
        c = weft.WeftClient("e/p", server_url=fake.url)
        s = c._sender
        assert (s.max_retries, s.backoff, s.max_backoff, s.max_queue) == (7, 0.5, 9.0, 123)
        assert c._http.timeout == 4.5 and c.http_timeout == 4.5
        assert c.log_level == "DEBUG" and lg.level == logging.DEBUG
        # explicit settings and arguments win over the environment
        c2 = weft.WeftClient("e/p", server_url=fake.url, timeout=2, settings={"retry_max_attempts": 1, "max_queue_size": 0, "log_level": "error"})
        assert c2._sender.max_retries == 1 and c2._sender.max_queue == sys.maxsize and c2._http.timeout == 2.0
        assert lg.level == logging.ERROR
        monkeypatch.setenv("WEFT_HTTP_TIMEOUT", "soon")
        monkeypatch.delenv("WEFT_MAX_CALLS_QUEUE_SIZE")
        monkeypatch.setenv("WEFT_MAX_QUEUE_SIZE", "55")
        monkeypatch.setenv("WEFT_LOG_LEVEL", "LOUD")
        c3 = weft.WeftClient("e/p", server_url=fake.url)
        assert c3._http.timeout == 30.0 and c3._sender.max_queue == 55 and c3.log_level is None
        for x in (c, c2, c3):
            x.finish(1)
    finally:
        lg.setLevel(old_level)
        lg.handlers[:] = old_handlers


def test_env_defaults_unchanged(fake, monkeypatch):
    for k in ("WEFT_RETRY_MAX_ATTEMPTS", "WEFT_RETRY_BACKOFF", "WEFT_RETRY_MAX_INTERVAL", "WEFT_MAX_CALLS_QUEUE_SIZE",
              "WEFT_MAX_QUEUE_SIZE", "WEFT_HTTP_TIMEOUT", "WEFT_LOG_LEVEL"):
        monkeypatch.delenv(k, raising=False)
    c = weft.WeftClient("e/p", server_url=fake.url)
    s = c._sender
    assert (s.max_retries, s.backoff, s.max_backoff, s.max_queue, c._http.timeout) == (3, 0.25, 4.0, 10_000, 30.0)
    c.finish(1)
