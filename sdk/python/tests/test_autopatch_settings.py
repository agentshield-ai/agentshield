"""Per-integration autopatch settings, environment switches and patching on import."""

import asyncio
import sys
import textwrap
import types
import uuid

import pytest

import weft
from weft import integrations as wi
from weft.integrations import AutopatchSettings, IntegrationSettings, OpSettings, unpatch_all
from weft.integrations import _settings


@pytest.fixture(autouse=True)
def _reset(monkeypatch):
    for k in ("WEFT_AUTOPATCH", "WEFT_DISABLED_INTEGRATIONS", "WEFT_AUTOPATCH_OPENAI", "WEFT_AUTOPATCH_CREWAI"):
        monkeypatch.delenv(k, raising=False)
    _settings.configure(None)
    yield
    _settings.configure(None)
    unpatch_all()


@pytest.fixture
def fake_openai(monkeypatch):
    class Completions:
        def create(self, **kw):
            return {"id": "x", "model": kw["model"], "choices": [{"index": 0, "message": {"role": "assistant", "content": "secret answer"}}],
                    "usage": {"prompt_tokens": 2, "completion_tokens": 1, "total_tokens": 3}}

    for name in ("openai", "openai.resources", "openai.resources.chat"):
        monkeypatch.setitem(sys.modules, name, types.ModuleType(name))
    m = types.ModuleType("openai.resources.chat.completions")
    m.Completions = Completions
    monkeypatch.setitem(sys.modules, "openai.resources.chat.completions", m)
    return Completions


@pytest.fixture
def fake_crewai(monkeypatch):
    class Agent:
        role = "Writer"

        def execute_task(self, task, context=None):
            return f"did {task}"

    m = types.ModuleType("crewai")
    m.Agent = Agent
    monkeypatch.setitem(sys.modules, "crewai", m)
    return Agent


def test_settings_parsing():
    _settings.configure({"OpenAI": {"enabled": False}, "llama_index": True, "anthropic": OpSettings(name="claude"),
                         "crewai": IntegrationSettings(op_settings=OpSettings(color="red"))})
    assert not _settings.is_enabled("openai") and _settings.is_enabled("llamaindex")
    assert _settings.get("anthropic").op_settings.name == "claude"
    assert _settings.get("crewai").op_settings.color == "red"
    _settings.configure(AutopatchSettings(openai=IntegrationSettings(enabled=False)))
    assert not _settings.is_enabled("openai") and _settings.is_enabled("crewai")
    with pytest.raises(TypeError):
        _settings.configure({"openai": 3})
    with pytest.raises(Exception):
        _settings.configure({"openai": {"op_settings": {"bogus": 1}}})


def test_disable_integration_via_settings(client, fake, fake_openai):
    status = wi.autopatch({"openai": {"enabled": False}})
    assert status["openai"] == "disabled"
    assert not getattr(fake_openai.create, "__weft_patched__", False)
    # enable it, patch, then disable again at runtime: calls go straight through
    _settings.configure(None)
    assert wi.autopatch()["openai"] == "patched"
    fake_openai().create(model="gpt-4o", messages=[])
    _settings.update("openai", {"enabled": False})
    fake_openai().create(model="gpt-4o", messages=[])
    client.flush()
    assert len(fake.calls_by_op("openai.chat.completions.create")) == 1


def test_op_settings_postprocess_and_display(client, fake, fake_openai, fake_crewai):
    def scrub_inputs(inputs):
        return {**inputs, "messages": "<scrubbed>"}

    def scrub_output(out):
        return {"redacted": True, "model": out.get("model")} if isinstance(out, dict) else "<scrubbed>"

    wi.autopatch({
        "openai": {"op_settings": {"postprocess_inputs": scrub_inputs, "postprocess_output": scrub_output,
                                   "call_display_name": "LLM call", "color": "purple", "attributes": {"team": "search"}}},
        "crewai": {"op_settings": {"name": "my_crew_step", "kind": "chain", "postprocess_output": lambda o: o.upper()}},
    })
    r = fake_openai().create(model="gpt-4o", messages=[{"role": "user", "content": "my password is hunter2"}])
    assert r["choices"][0]["message"]["content"] == "secret answer"  # the caller still gets the real response
    assert fake_crewai().execute_task("plan") == "did plan"
    client.flush()
    [c] = fake.calls_by_op("openai.chat.completions.create")
    assert c["inputs"]["messages"] == "<scrubbed>" and c["inputs"]["model"] == "gpt-4o"
    assert c["output"] == {"redacted": True, "model": "gpt-4o"} and c["display_name"] == "LLM call"
    assert c["summary"]["usage"]["gpt-4o"]["total_tokens"] == 3  # usage still recorded
    assert c["attributes"]["weft"]["color"] == "purple" and c["attributes"]["team"] == "search"
    assert c["attributes"]["weft"]["integration"] == "openai"
    [a] = fake.calls_by_op("my_crew_step")
    assert a["output"] == "DID PLAN" and a["attributes"]["weft"]["span_kind"] == "chain"


def test_settings_apply_to_callback_integrations(client, fake, monkeypatch):
    class BaseCallbackHandler:
        def __init__(self):
            pass

    for name, attrs in {"langchain_core": {}, "langchain_core.callbacks": {"BaseCallbackHandler": BaseCallbackHandler},
                        "langchain_core.tracers": {}, "langchain_core.tracers.context": {"register_configure_hook": lambda *a: None}}.items():
        m = types.ModuleType(name)
        for k, v in attrs.items():
            setattr(m, k, v)
        monkeypatch.setitem(sys.modules, name, m)
    import weft.integrations.langchain as lc

    monkeypatch.setattr(lc, "_TRACER_CLS", None)
    monkeypatch.setattr(lc, "_registered", False)
    lc.patch_langchain()
    _settings.configure({"langchain": {"op_settings": {"postprocess_inputs": lambda d: {"n": len(d)}}}})
    t = lc.WeftTracer()
    rid = uuid.uuid4()
    t.on_chain_start({"name": "Chain"}, {"a": 1, "b": 2}, run_id=rid)
    t.on_chain_end({"x": 1}, run_id=rid)
    monkeypatch.setenv("WEFT_DISABLED_INTEGRATIONS", "langchain")
    rid2 = uuid.uuid4()
    t.on_chain_start({"name": "Chain"}, {"a": 1}, run_id=rid2)
    t.on_chain_end({}, run_id=rid2)
    client.flush()
    [c] = fake.calls_by_op("langchain.Chain.Chain")
    assert c["inputs"] == {"n": 2} and c["attributes"]["weft"]["integration"] == "langchain"


def test_env_switches(monkeypatch, fake_openai, fake_crewai):
    monkeypatch.setenv("WEFT_DISABLED_INTEGRATIONS", "openai, llama-index")
    st = wi.autopatch()
    assert st["openai"] == "disabled" and st["llamaindex"] == "disabled" and st["crewai"] == "patched"
    assert not _settings.is_enabled("llama_index")
    monkeypatch.delenv("WEFT_DISABLED_INTEGRATIONS")
    monkeypatch.setenv("WEFT_AUTOPATCH_CREWAI", "false")
    assert not _settings.is_enabled("crewai") and _settings.is_enabled("openai")
    monkeypatch.setenv("WEFT_AUTOPATCH", "0")
    assert wi.enabled_integrations() == []


def test_init_with_autopatch_settings(fake, fake_openai, monkeypatch):
    monkeypatch.delenv("WEFT_DISABLED", raising=False)
    weft.init("p", server_url=fake.url, autopatch_settings={"openai": {"op_settings": {"call_display_name": "chat"}}})
    try:
        assert getattr(fake_openai.create, "__weft_patched__", False)
        fake_openai().create(model="gpt-4o", messages=[])
        weft.flush()
        assert fake.calls_by_op("openai.chat.completions.create")[0]["display_name"] == "chat"
    finally:
        weft.finish()
    weft.init("p", server_url=fake.url, autopatch=False, autopatch_settings={"openai": False})
    try:
        assert not _settings.is_enabled("openai")
    finally:
        weft.finish()


def test_patch_on_import(client, fake, tmp_path, monkeypatch):
    """A library imported after autopatch() is patched when its import finishes."""
    pkg = tmp_path / "weftfakelib"
    pkg.mkdir()
    (pkg / "__init__.py").write_text(textwrap.dedent('''
        class Engine:
            def run(self, prompt):
                return prompt[::-1]
    '''))
    monkeypatch.syspath_prepend(str(tmp_path))
    monkeypatch.delitem(sys.modules, "weftfakelib", raising=False)

    from weft.integrations._methods import Method, patch_methods

    calls = []

    def patcher():
        calls.append(1)
        return patch_methods("weftfakelib", "weftfakelib", [Method("Engine.run", "weftfakelib.Engine.run", "agent")])

    monkeypatch.setitem(wi.INTEGRATIONS, "weftfakelib", wi.Integration("weftfakelib", patcher, ("weftfakelib",)))
    assert wi.autopatch()["weftfakelib"] == "deferred" and calls == []
    assert "weftfakelib" in wi._hooks.pending()
    import weftfakelib

    assert calls == [1] and wi.PATCHED.get("weftfakelib")
    assert weftfakelib.Engine().run("abc") == "cba"
    client.flush()
    [c] = fake.calls_by_op("weftfakelib.Engine.run")
    assert c["output"] == "cba" and c["inputs"]["prompt"] == "abc"
    assert "weftfakelib" not in wi._hooks.pending()


def test_methods_wrapper_preserves_async_and_static(client, fake, monkeypatch):
    from weft.integrations._methods import Method, patch_methods

    class Svc:
        async def fetch(self, x):
            return x * 2

        @staticmethod
        def helper(y):
            return y + 1

        def gen(self, n):
            yield from range(n)

    m = types.ModuleType("weftsvc")
    m.Svc = Svc
    monkeypatch.setitem(sys.modules, "weftsvc", m)
    assert patch_methods("weftsvc", "weftsvc", [Method("Svc.fetch", "svc.fetch", "tool"), Method("Svc.helper", "svc.helper"),
                                                 Method("Svc.gen", "svc.gen")])
    import inspect

    assert inspect.iscoroutinefunction(Svc.fetch)
    assert asyncio.run(Svc().fetch(2)) == 4 and Svc.helper(1) == 2 and Svc().helper(2) == 3
    assert list(Svc().gen(3)) == [0, 1, 2]
    client.flush()
    assert fake.calls_by_op("svc.fetch")[0]["output"] == 4
    assert fake.calls_by_op("svc.helper")[0]["inputs"] == {"y": 1}
    assert fake.calls_by_op("svc.gen")[0]["output"] == [0, 1, 2]
    # no client: methods run untraced
    weft.finish()
    assert asyncio.run(Svc().fetch(5)) == 10


def test_subclasses_defined_after_patching_are_traced(client, fake, monkeypatch):
    from weft.integrations._methods import Method, patch_methods

    class Model:
        def generate(self, prompt):
            raise NotImplementedError

    m = types.ModuleType("weftmodels")
    m.Model = Model
    monkeypatch.setitem(sys.modules, "weftmodels", m)
    patch_methods("weftmodels", "weftmodels", [Method("Model.generate", "weftmodels.generate", "llm", subclasses=True)])

    class MyModel(Model):  # e.g. a user's own model class
        def generate(self, prompt):
            return prompt.upper()

    class Child(MyModel):
        def generate(self, prompt):
            return "child:" + super().generate(prompt)

    assert MyModel().generate("hi") == "HI" and Child().generate("x") == "child:X"
    client.flush()
    gens = fake.calls_by_op("weftmodels.generate")
    assert [g["output"] for g in gens] == ["HI", "child:X", "X"]
    assert gens[2]["parent_id"] == gens[1]["id"]  # super() call nests
    unpatch_all()
    assert "__init_subclass__" not in Model.__dict__ and "_weft_subclass_hooks" not in Model.__dict__

    class Later(Model):
        def generate(self, prompt):
            return prompt

    assert not getattr(Later.__dict__["generate"], "__weft_patched__", False)
