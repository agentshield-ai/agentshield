#!/usr/bin/env python3
"""Drive the official `weave` Python client against a Weft server started with
the compatibility adapter on (--weave-compat, docs/weft/COMPATIBILITY.md) and
assert the results through Weft's own API.

    ./bin/weft --weave-compat &
    pip install weave
    WEFT_URL=http://127.0.0.1:6345 python scripts/weft_weave_compat_check.py

Against a server without the adapter the client cannot log in, and the script
fails at weave.init().

The script points weave at Weft (WANDB_BASE_URL for the W&B GraphQL shim,
WF_TRACE_SERVER_URL for traces), runs ops, publishes and reloads a dataset,
runs an evaluation and adds feedback, then checks what Weft stored through
Weft's own API (/api/v1, docs/weft/API.md).
"""
import asyncio
import json
import os
import sys
import tempfile
import urllib.parse
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


def _headers():
    headers = {"Content-Type": "application/json"}
    if os.environ.get("WEFT_API_KEY"):
        headers["Authorization"] = "Bearer " + os.environ["WEFT_API_KEY"]
    return headers


def weft(path, body=None, raw=False):
    """Weft's own API: POST a JSON body, or GET when body is None."""
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(URL + path, data, _headers())
    with urllib.request.urlopen(req) as r:
        out = r.read()
    return out if raw else json.loads(out)


def q(**params):
    return urllib.parse.urlencode({"project_id": PROJECT, **params})


def check(cond, msg):
    print(("ok   " if cond else "FAIL ") + msg)
    if not cond:
        check.failed = True


check.failed = False


def main():
    try:
        client = weave.init(PROJECT)
    except Exception as e:  # noqa: BLE001
        print(f"FAIL weave.init: {type(e).__name__}: {e}")
        print("     Is the server running with the compatibility adapter (--weave-compat)?")
        sys.exit(1)

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
    # The client's user and run field names (wb_user_ids filter, wb_user_id
    # sort and query field) are translated; Weft's own API says user_id.
    mine = list(client.get_calls(filter={"call_ids": [call.id], "wb_user_ids": [], "wb_run_ids": []},
                                 sort_by=[{"field": "wb_user_id", "direction": "asc"}],
                                 query={"$expr": {"$eq": [{"$getField": "wb_run_id"}, {"$literal": "no-such-run"}]}}))
    check(mine == [], "the client's wb_* filter, sort and query fields are understood")
    check(len(list(client.get_calls(filter={"call_ids": [call.id]}, sort_by=[{"field": "wb_user_id", "direction": "asc"}]))) == 1,
          "the client can sort by wb_user_id")

    root = weft(f"/api/v1/spans/{call.id}?" + q(include_feedback="true"))
    check("user_id" in root and "run_id" in root and "wb_user_id" not in root, "Weft's API names the user and run fields its own way")
    check(root["name"].startswith(f"weft:///{PROJECT}/function/pipeline:") and "op_name" not in root,
          "a weave op call is a span named by its function ref")
    usage = root["summary"]["usage"]["gpt-4o-mini"]
    check(usage["prompt_tokens"] == 20 and usage["requests"] == 2, f"usage rolled up once, not double counted ({usage})")
    check({f["feedback_type"] for f in root["feedback"]} >= {"weft.reaction", "weft.note"}, "feedback stored under Weft's names")
    check(all(f["ref"] == f"weft:///{PROJECT}/span/{call.id}" for f in root["feedback"]), "feedback refs in Weft's scheme")
    client_types = {f.feedback_type for f in client.get_call(call.id).feedback}
    check(client_types >= {"wandb.reaction.1", "wandb.note.1"}, f"the client reads its own feedback names back ({client_types})")
    kids = weft("/api/v1/spans/query", {"project_id": PROJECT, "filter": {"parent_ids": [call.id]}})
    check(len(kids["items"]) == 3 and kids["total_count"] == 3, "child calls linked to the parent")

    op = weft("/api/v1/objects/add?" + q())
    check(op["kind"] == "function", "weave ops classified as functions")
    src = weft(f"/api/v1/files/{op['value']['files']['obj.py']}?" + q(), raw=True).decode()
    check("def add" in src, "op source file found under weave's client-side digest")
    ds_obj = weft("/api/v1/objects/capitals?" + q())
    check(len(ds_obj["digest"]) == 43, f"the client's digest form is kept for its objects ({ds_obj['digest']})")

    evals = weft("/api/v1/spans/query", {"project_id": PROJECT, "filter": {"names": ["Evaluation.evaluate"]}})["items"]
    cmp = weft("/api/v1/evaluations/compare", {"project_id": PROJECT, "ids": [evals[0]["id"]]})
    check(len(cmp["rows"]) == 2 and cmp["rows"][0]["inputs"].get("q") in ("France", "Spain"), "Weft evaluation comparison of a weave evaluation")
    weave.finish()
    if check.failed:
        sys.exit(1)
    print(f"all checks passed for weave {weave.__version__} against {URL}")


if __name__ == "__main__":
    main()
