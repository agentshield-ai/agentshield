"""Local digests (weft._canonical, DESIGN.md §10.3) equal the server's, byte for byte.

The SDK names a traced function by a ref whose digest it computes before the
server answers, so the two must agree. Offline, the vectors in
``sdk/testdata/digest_vectors.json`` (shared with the TypeScript SDK) are checked; with
``WEFT_TEST_SERVER_URL`` every vector is also saved as an object, a table row
and a file on a live server and the server's digests are compared.
"""

import json
import os
import uuid

import pytest

import weft
from weft._canonical import canonical_json, digest, file_digest, table_digest

URL = os.environ.get("WEFT_TEST_SERVER_URL")
VECTORS = json.load(open(os.path.join(os.path.dirname(__file__), "..", "..", "testdata", "digest_vectors.json"), encoding="utf-8"))


@pytest.mark.parametrize("vec", VECTORS["values"], ids=lambda v: v["name"])
def test_value_vectors(vec):
    assert canonical_json(vec["value"]) == vec["canonical"]
    assert digest(vec["value"]) == vec["digest"]


def test_table_and_file_vectors():
    t = VECTORS["table"]
    assert table_digest(t["row_digests"]) == t["digest"]
    for f in VECTORS["files"]:
        assert file_digest(f["text"].encode("utf-8")) == f["digest"]


@pytest.mark.skipif(not URL, reason="WEFT_TEST_SERVER_URL not set")
def test_digests_match_a_live_server():
    c = weft.init(f"digest-check/{uuid.uuid4().hex[:8]}", server_url=URL, api_key=os.environ.get("WEFT_TEST_API_KEY"),
                  autopatch=False, settings={"print_call_link": False})
    try:
        # plus numbers whose text is Python's own (the TypeScript SDK sends 1 for 1.0)
        values = [v["value"] for v in VECTORS["values"]] + [{"f": 1.0, "g": 1e-07, "h": 1e16, "k": 10**20, "m": -0.0}]
        for i, v in enumerate(values):
            res = c._http.post("/api/v1/objects/save", {"project_id": c.project_id, "name": f"v{i}", "value": v})
            assert res["digest"] == digest(v), (i, v)
        res = c._http.post("/api/v1/tables/create", {"project_id": c.project_id, "rows": values})
        assert res["row_digests"] == [digest(v) for v in values]
        assert res["digest"] == table_digest(res["row_digests"])
        for f in VECTORS["files"]:
            data = f["text"].encode("utf-8")
            up = c._http.post_multipart("/api/v1/files/upload", {"project_id": c.project_id}, {"file": ("f.txt", data, "text/plain")})
            assert up["digest"] == file_digest(data)

        @weft.trace
        def traced(x: float) -> float:
            return x * 1.5

        ref = c.save(traced)
        c.flush()
        obj = c._read_object(c.project_id, ref.name, "latest")
        assert obj["digest"] == ref.digest and obj["kind"] == "function" and obj["ref"] == ref.uri()
        assert c._warned_digest is False
    finally:
        weft.finish()
