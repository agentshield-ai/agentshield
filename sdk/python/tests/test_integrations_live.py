"""Framework integrations, autopatch settings, local scorers and NER redaction
against a real Weft server. Skipped unless WEFT_TEST_SERVER_URL is set."""

import asyncio
import os
import sys
import types
import uuid

import pytest

import weft
from weft import local_scorers as ls
from weft.integrations import unpatch_all
from weft.integrations import _settings
from weft.integrations._methods import Method, patch_methods

URL = os.environ.get("WEFT_TEST_SERVER_URL")
pytestmark = pytest.mark.skipif(not URL, reason="WEFT_TEST_SERVER_URL not set")


@pytest.fixture
def live():
    project = f"it/integ-{uuid.uuid4().hex[:8]}"
    c = weft.init(project, server_url=URL, api_key=os.environ.get("WEFT_TEST_API_KEY"), autopatch=False,
                  settings={"print_call_link": False})
    yield c
    weft.flush(10)
    weft.finish()
    unpatch_all()
    _settings.configure(None)


def _calls(c, op):
    return list(c.spans(filter={"names": [op]}, sort_by=[{"field": "started_at", "direction": "asc"}]))


def test_framework_spans_kinds_and_usage(live, monkeypatch):
    @weft.trace(name="openai.chat.completions.create", kind="llm")
    def provider(model):
        weft.add_usage(weft.get_current_span(), model, 120, 30)
        return "ok"

    class LLM:
        model = "gpt-4o-mini"

        def call(self, messages):
            return provider(self.model)

    class Agent:
        role = "Researcher"

        def execute_task(self, task):
            return LLM().call([task])

    class Crew:
        def kickoff(self, inputs=None):
            return Agent().execute_task("research weft")

    m = types.ModuleType("crewai")
    m.Crew, m.Agent, m.LLM = Crew, Agent, LLM
    monkeypatch.setitem(sys.modules, "crewai", m)
    from weft.integrations import patch_crewai

    _settings.configure({"crewai": {"op_settings": {"attributes": {"team": "it"}}}})
    assert patch_crewai()
    assert Crew().kickoff(inputs={"topic": "weft"}) == "ok"
    live.flush()
    [crew] = _calls(live, "crewai.Crew.kickoff")
    assert crew.attributes["weft"]["span_kind"] == "workflow" and crew.attributes["weft"]["integration"] == "crewai"
    assert crew.attributes["team"] == "it"
    [agent] = _calls(live, "crewai.Agent.execute_task")
    assert agent.parent_id == crew.id and agent.display_name == "Agent: Researcher"
    [llm] = _calls(live, "crewai.LLM.call")
    assert llm.parent_id == agent.id and llm.attributes["weft"]["span_kind"] == "llm"
    # usage counted once: the root's roll-up equals the provider call's own usage
    root = live.span(crew.id)
    usage = (root.summary or {}).get("usage") or {}
    assert usage["gpt-4o-mini"]["prompt_tokens"] == 120 and usage["gpt-4o-mini"]["requests"] == 1


def test_claude_agent_sdk_thread_and_usage(live, monkeypatch):
    class ResultMessage:
        def __init__(self):
            self.usage = {"input_tokens": 10, "output_tokens": 5}
            self.result, self.session_id, self.is_error, self.num_turns = "done", "s1", False, 1

    class AssistantMessage:
        def __init__(self, content, model="claude-sonnet-4-5"):
            self.content, self.model, self.parent_tool_use_id = content, model, None

    class TextBlock:
        def __init__(self, text):
            self.text = text

    class ClaudeSDKClient:
        async def query(self, prompt, session_id="default"):
            pass

        async def receive_response(self):
            yield AssistantMessage([TextBlock("hello")])
            yield ResultMessage()

    m = types.ModuleType("claude_agent_sdk")
    m.ClaudeSDKClient = ClaudeSDKClient
    monkeypatch.setitem(sys.modules, "claude_agent_sdk", m)
    monkeypatch.setitem(sys.modules, "claude_agent_sdk._internal", None)
    from weft.integrations import patch_claude_agent_sdk

    assert patch_claude_agent_sdk()

    async def run():
        c = ClaudeSDKClient()
        for p in ("hi", "again"):
            await c.query(p)
            async for _ in c.receive_response():
                pass

    asyncio.run(run())
    live.flush()
    turns = _calls(live, "claude_agent_sdk.turn")
    assert len(turns) == 2 and turns[0].session_id == turns[1].session_id and turns[0].turn_id == turns[0].id
    assert turns[0].summary["usage"]["claude-sonnet-4-5"]["total_tokens"] == 15


def test_local_scorer_object_roundtrip_and_ner_redaction(live, monkeypatch):
    monkeypatch.setattr(ls, "load_sequence_classifier", lambda *a, **k: lambda t: [0, 0, 0, 0, 0])
    tox = ls.ToxicityScorer(total_threshold=4)
    ref = weft.save(tox, name="toxicity")
    back = weft.get(ref)
    assert type(back).__name__ == "ToxicityScorer" and back.total_threshold == 4
    assert back.score(output="fine").passed

    class Analyzer:
        def analyze(self, text, entities=None, language="en"):
            i = text.find("Grace Hopper")
            return [types.SimpleNamespace(entity_type="PERSON", start=i, end=i + 12, score=0.9)] if i >= 0 else []

    live._redact = __import__("weft._redact", fromlist=["Redactor"]).Redactor(pii=True, settings={"redact_pii_analyzer": Analyzer()})

    @weft.trace
    def bio(name):
        return f"{name} wrote the first compiler; mail grace@navy.mil"

    bio("Grace Hopper")
    live.flush()
    [c] = _calls(live, "bio")
    assert c.inputs == {"name": "<PERSON>"} and c.output == "<PERSON> wrote the first compiler; mail <EMAIL_ADDRESS>"
