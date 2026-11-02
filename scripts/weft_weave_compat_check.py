#!/usr/bin/env python3
"""Drive the official `weave` Python client against a Weft server and assert
the results through Weft's own API.

    pip install weave
    WEFT_URL=http://127.0.0.1:6345 python scripts/weft_weave_compat_check.py

The script points weave at Weft (WANDB_BASE_URL for the W&B GraphQL shim,
WF_TRACE_SERVER_URL for traces), runs ops, publishes and reloads a dataset,
runs an evaluation and adds feedback, then checks what Weft stored.
"""
import asyncio
import json
import os
import sys
import tempfile
import urllib.request
import uuid

URL = os.environ.get("WEFT_URL", "http://127.0.0.1:6345").rstrip("/")
os.environ.setdefault("WANDB_BASE_URL", URL)
os.environ.setdefault("WF_TRACE_SERVER_URL", URL)
os.environ.setdefault("WANDB_API_KEY", os.environ.get("WEFT_API_KEY") or "x" * 40)
# Keep weave's response cache isolated so a reset server is re-populated.
os.environ.setdefault("WEAVE_SERVER_CACHE_DIR", tempfile.mkdtemp(prefix="weave-cache-"))

import weave  # noqa: E402
from weave import Dataset, Evaluation, Model  # noqa: E402

PROJECT = f"compat/check-{uuid.uuid4().hex[:8]}"


def weft(path, body):
    headers = {"Content-Type": "application/json"}
    if os.environ.get("WEFT_API_KEY"):
        headers["Authorization"] = "Bearer " + os.environ["WEFT_API_KEY"]
    req = urllib.request.Request(URL + path, json.dumps(body).encode(), headers)
    with urllib.request.urlopen(req) as r:
        return json.loads(r.read())


def check(cond, msg):
    print(("ok   " if cond else "FAIL ") + msg)
    if not cond:
        check.failed = True


check.failed = False


def main():
    client = weave.init(PROJECT)

    @weave.op
    def fake_llm(prompt: str) -> dict:
        return {"model": "gpt-4o-mini", "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
                "choices": [{"message": {"role": "assistant", "content": prompt.upper()}}]}

    @weave.op
    def add(a: int, b: int) -> int:
        return a + b

    @weave.op
    def pipeline(x: int) -> int:
        fake_llm(f"compute {x}")
        fake_llm("again")
        return add(x, 1) * 2

    out, call = pipeline.call(3)
    call.feedback.add_reaction("👍")
    call.feedback.add_note("looks right")

    ds = Dataset(name="capitals", rows=[{"q": "France", "expected": "Paris"}, {"q": "Spain", "expected": "Madrid"}])
    ref = weave.publish(ds)
    loaded = ref.get()

    class Lookup(Model):
        @weave.op
        def predict(self, q: str) -> str:
            return {"France": "Paris", "Spain": "Barcelona"}[q]

    @weave.op
    def exact(expected: str, output: str) -> bool:
        return expected == output

    summary = asyncio.run(Evaluation(dataset=ds, scorers=[exact]).evaluate(Lookup()))
    client.flush()

    check(out == 8, "op result")
    check([r["q"] for r in loaded.rows] == ["France", "Spain"], "dataset round trip through weave refs")
    check(summary["exact"]["true_fraction"] == 0.5, "evaluation summary")
    got = client.get_call(call.id)
    check(got.output == 8, "client.get_call reads the call back")

    root = weft("/call/read", {"project_id": PROJECT, "id": call.id, "include_feedback": True})["call"]
    usage = root["summary"]["usage"]["gpt-4o-mini"]
    check(usage["prompt_tokens"] == 20 and usage["requests"] == 2, f"usage rolled up once, not double counted ({usage})")
    check({f["feedback_type"] for f in root["feedback"]} >= {"wandb.reaction.1", "wandb.note.1"}, "feedback stored")
    kids = weft("/calls/query", {"project_id": PROJECT, "filter": {"parent_ids": [call.id]}})["calls"]
    check(len(kids) == 3, "child calls linked to the parent")

    op = weft("/obj/read", {"project_id": PROJECT, "object_id": "add", "digest": "latest"})["obj"]
    check(op["kind"] == "op", "weave ops classified as ops")
    src = urllib.request.urlopen(urllib.request.Request(
        URL + "/file/content", json.dumps({"project_id": PROJECT, "digest": op["val"]["files"]["obj.py"]}).encode(),
        {"Content-Type": "application/json"})).read().decode()
    check("def add" in src, "op source file found under weave's client-side digest")

    evals = weft("/calls/query", {"project_id": PROJECT, "filter": {"op_names": ["Evaluation.evaluate"]}})["calls"]
    cmp = weft("/evaluations/compare", {"project_id": PROJECT, "evaluation_call_ids": [evals[0]["id"]]})
    check(len(cmp["rows"]) == 2 and cmp["rows"][0]["inputs"].get("q") in ("France", "Spain"), "Weft evaluation comparison of a weave evaluation")
    weave.finish()
    if check.failed:
        sys.exit(1)
    print(f"all checks passed for weave {weave.__version__} against {URL}")


if __name__ == "__main__":
    main()
