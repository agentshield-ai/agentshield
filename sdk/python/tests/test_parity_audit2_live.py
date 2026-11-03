"""Second parity audit follow-ups against a real Weft server: Verdict with the
real ``verdict`` package, Realtime WAV audio, custom runtimes, threads_query
and TypeSafe. Skipped unless WEFT_TEST_SERVER_URL is set, e.g.::

    WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 pytest tests/test_parity_audit2_live.py
"""

import io
import json
import os
import urllib.request
import uuid
import wave

import pytest

import weft
from weft.integrations import RealtimeTracer, patch_typesafe, patch_verdict, unpatch_all
from weft.trace_server_interface import SortBy, ThreadsQueryReq

from test_parity_audit2 import _install_typesafe
from test_realtime_audio import SESSION, b64, pcm16, response_events

URL = os.environ.get("WEFT_TEST_SERVER_URL")
pytestmark = pytest.mark.skipif(not URL, reason="WEFT_TEST_SERVER_URL not set")


@pytest.fixture
def live():
    project = f"it/audit2-{uuid.uuid4().hex[:8]}"
    c = weft.init(project, server_url=URL, api_key=os.environ.get("WEFT_TEST_API_KEY"), autopatch=False,
                  settings={"print_call_link": False})
    yield c
    weft.flush(10)
    weft.finish()
    unpatch_all()


def _get(path):
    headers = {}
    if os.environ.get("WEFT_TEST_API_KEY"):
        headers["Authorization"] = f"Bearer {os.environ['WEFT_TEST_API_KEY']}"
    with urllib.request.urlopen(urllib.request.Request(URL + path, headers=headers), timeout=10) as r:
        return json.loads(r.read() or b"{}")


def test_verdict_real_library_one_trace_live(live):
    pytest.importorskip("verdict")
    from verdict import Pipeline
    from verdict.schema import Schema
    from verdict.transform import MapUnit

    assert patch_verdict()
    p = Pipeline("double") >> MapUnit(lambda x: Schema.of(values=x.values * 2)) >> MapUnit(lambda x: Schema.of(values=x.values + 1))
    p.run(Schema.of(values=3), display=False)
    live.flush()
    calls = live.get_calls().to_list()
    assert len(calls) == 5 and len({c.trace_id for c in calls}) == 1, "one trace, as under Weave"
    [root] = [c for c in calls if c.parent_id is None]
    maps = sorted((c for c in calls if c.display_name == "Map"), key=lambda c: c.started_at)
    assert maps[0].parent_id == root.id and maps[1].parent_id == maps[0].id


def test_realtime_audio_round_trip_live(live):
    mic, out = pcm16(400), pcm16(300, freq=220.0)
    rt = RealtimeTracer()
    rt.on_server_event({"type": "session.created", "event_id": "e0", "session": SESSION})
    rt.on_client_event({"type": "input_audio_buffer.append", "audio": b64(mic)})
    rt.on_server_event({"type": "input_audio_buffer.speech_started", "audio_start_ms": 100, "item_id": "item_u"})
    rt.on_server_event({"type": "input_audio_buffer.speech_stopped", "audio_end_ms": 300, "item_id": "item_u"})
    rt.on_server_event({"type": "input_audio_buffer.committed", "item_id": "item_u"})
    rt.on_server_event({"type": "conversation.item.added", "item": {"id": "item_u", "type": "message", "role": "user",
                                                                    "content": [{"type": "input_audio", "transcript": None}]}})
    for ev in response_events("resp_live", "item_a", out):
        rt.on_server_event(ev)
    rt.close()
    live.flush()
    [resp] = live.get_calls(filter={"op_names": ["openai.realtime.response"]}).to_list()

    def frames(media):
        assert media["_type"] == "Audio"
        with wave.open(io.BytesIO(live.read_file(media["file_digest"]))) as w:
            return w.getframerate(), w.readframes(w.getnframes())

    assert frames(resp.inputs["input"][0]["content"][0]["audio"]) == (24000, mic[100 * 48:300 * 48])
    assert frames(resp.output["output"][0]["content"][0]["audio"]) == (24000, out)


def test_custom_runtime_and_threads_live(live):
    res = live.register_custom_runtime(name="audit2-vllm", base_url="http://127.0.0.1:9/v1",
                                       runtime_ids=["llama-3-8b", weft.CustomRuntimeID(id="qwen2-7b", max_tokens=8192)])
    assert {r.playground_id: r.max_tokens for r in res.runtime_ids} == {"audit2-vllm/llama-3-8b": 4096, "audit2-vllm/qwen2-7b": 8192}
    listed = _get(f"/v2/{live.entity}/{live.project}/runtimes")["runtimes"]
    assert any(r["name"] == "audit2-vllm" for r in listed)
    live.register_custom_runtime(name="audit2-vllm", base_url="http://127.0.0.1:9/v1", runtime_ids=["llama-3-8b"])
    [rt] = [r for r in _get(f"/v2/{live.entity}/{live.project}/runtimes")["runtimes"] if r["name"] == "audit2-vllm"]
    assert [r["id"] for r in rt["runtime_ids"]] == ["llama-3-8b"], "omitted IDs are removed"

    @weft.op
    def turn(i):
        return weft.require_current_call().id

    with weft.thread("live-a"):
        turn(1)
        turn(2)
    with weft.thread("live-b"):
        turn(3)
    live.flush()
    res = live.threads_query(ThreadsQueryReq(sort_by=[SortBy(field="turn_count", direction="desc")]))
    assert [(t.thread_id, t.turn_count) for t in res.threads] == [("live-a", 2), ("live-b", 1)]
    assert res.threads[0].last_updated is not None and res.threads[0].p50_turn_duration_ms is not None
    url = weft.urls.agent_conversation_path(live.entity, live.project, "live-a")
    assert url == f"{URL}/{live.entity}/{live.project}/conversations/live-a"


def test_typesafe_live(live, monkeypatch):
    mod = _install_typesafe(monkeypatch)
    assert patch_typesafe()
    mod.TypeSafeClient().system_one({"ticket": "refund"}, ["refund?"])
    live.flush()
    [llm] = live.get_calls(filter={"op_names": ["chat"]}).to_list()
    assert llm.attributes["gen_ai"]["provider"]["name"] == "typesafe" and llm.summary["usage"]["jev-2"]["prompt_tokens"] == 42


def test_typesafe_inside_op_is_a_linked_trace_live(live, monkeypatch):
    mod = _install_typesafe(monkeypatch)
    assert patch_typesafe()

    @weft.op
    def classify(msg):
        return mod.TypeSafeClient().system_one(msg, ["refund?"]).answers

    classify("refund please")
    live.flush()
    [op] = live.get_calls(filter={"op_names": ["classify"]}).to_list()
    [llm] = live.get_calls(filter={"op_names": ["chat"]}).to_list()
    [turn] = live.get_calls(filter={"op_names": ["invoke_agent"]}).to_list()
    assert op.trace_id != llm.trace_id == turn.trace_id and turn.parent_id is None and llm.parent_id == turn.id
    # The server exposes Weave's link as the agent span fields parent_call_id / parent_call_trace_id.
    res = live._http.post("/agents/spans/query", {
        "project_id": live.project_id,
        "query": {"$expr": {"$eq": [{"$getField": "parent_call_id"}, {"$literal": op.id}]}},
    })
    spans = {s["span_id"]: s for s in res["spans"]}
    assert set(spans) == {turn.id, llm.id}
    assert all(s["parent_call_trace_id"] == op.trace_id for s in spans.values())


def test_list_aliases_and_get_tags_and_aliases(live):
    ref = weft.publish({"rows": [1, 2]}, name="alias-check")
    live.set_aliases(ref, ["production", "staging"])
    live.add_tags(ref, ["reviewed"])
    tags, aliases = live.get_tags_and_aliases(ref)
    assert tags == ["reviewed"]
    assert {"production", "staging"} <= set(aliases)
    assert {"production", "staging"} <= set(live.list_aliases())
    # The module-level functions (weave.list_aliases & co.) use the global client.
    assert weft.get_tags_and_aliases(ref) == live.get_tags_and_aliases(ref)
    assert weft.list_aliases() == live.list_aliases() and weft.list_tags() == live.list_tags()
    weft.remove_aliases(ref, "staging")
    assert "staging" not in weft.get_aliases(ref)
