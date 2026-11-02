"""An in-process fake Weft server (stdlib http.server) that records requests."""

from __future__ import annotations

import json
import re
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Dict, List, Optional

from weft._canonical import b64digest, digest, table_digest


def _base_class(val: Any) -> Optional[str]:
    if not isinstance(val, dict):
        return None
    known = ["Dataset", "Model", "Evaluation", "Scorer", "Prompt", "LLMStructuredCompletionModel", "Op"]
    for c in [val.get("_class_name")] + list(val.get("_bases") or []):
        if c in known:
            return c
    return None


class FakeWeft:
    def __init__(self) -> None:
        self.requests: List[Dict[str, Any]] = []
        self.calls: Dict[str, Dict[str, Any]] = {}
        self.objs: Dict[str, List[Dict[str, Any]]] = {}
        self.tables: Dict[str, List[Dict[str, Any]]] = {}
        self.files: Dict[str, bytes] = {}
        self.feedback: List[Dict[str, Any]] = []
        self.costs: Dict[str, Any] = {}
        self.otel: List[bytes] = []
        self.fail_status: Optional[int] = None
        self.fail_paths: Dict[str, int] = {}  # path -> number of upcoming requests to fail with 503
        self.lock = threading.Lock()
        fake = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *a: Any) -> None:
                pass

            def _send(self, status: int, body: Any, ctype: str = "application/json") -> None:
                data = body if isinstance(body, bytes) else json.dumps(body).encode()
                self.send_response(status)
                self.send_header("Content-Type", ctype)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def do_GET(self) -> None:
                with fake.lock:
                    fake.requests.append({"method": "GET", "path": self.path, "body": None, "headers": dict(self.headers)})
                if self.path == "/server_info":
                    return self._send(200, {"version": "fake", "auth_required": False, "otel_endpoint": "/otel/v1/traces"})
                if self.path == "/auth/whoami":
                    return self._send(200, {"user_id": "anonymous", "auth_required": False})
                if self.path == "/health":
                    return self._send(200, {"status": "ok"})
                return self._send(404, {"reason": "not found"})

            def do_POST(self) -> None:
                n = int(self.headers.get("Content-Length") or 0)
                raw = self.rfile.read(n)
                ctype = self.headers.get("Content-Type", "")
                body: Any = None
                if ctype.startswith("application/json"):
                    body = json.loads(raw or b"{}")
                with fake.lock:
                    fake.requests.append({"method": "POST", "path": self.path, "body": body, "raw": raw, "headers": dict(self.headers)})
                if fake.fail_status:
                    return self._send(fake.fail_status, {"reason": "injected failure"})
                with fake.lock:
                    left = fake.fail_paths.get(self.path, 0)
                    if left:
                        fake.fail_paths[self.path] = left - 1
                if left:
                    return self._send(503, {"reason": "injected failure"})
                try:
                    with fake.lock:
                        res = fake.handle(self.path, body, raw, ctype)
                except KeyError as e:
                    return self._send(404, {"reason": f"not found: {e}"})
                if isinstance(res, tuple):
                    return self._send(200, res[0], res[1])
                return self._send(200, res)

        self.httpd = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.httpd.server_address[1]}"
        self.thread = threading.Thread(target=self.httpd.serve_forever, kwargs={"poll_interval": 0.05}, daemon=True)
        self.thread.start()

    def close(self) -> None:
        self.httpd.shutdown()
        self.httpd.server_close()

    # ------------------------------------------------------------------ helpers
    def paths(self) -> List[str]:
        return [r["path"] for r in self.requests]

    def calls_by_op(self, name: str) -> List[Dict[str, Any]]:
        out = []
        for c in self.calls.values():
            op = c.get("op_name", "")
            base = op.rsplit("/", 1)[-1].rsplit(":", 1)[0] if op.startswith("weft:///") else op
            if base == name:
                out.append(c)
        return sorted(out, key=lambda c: c.get("started_at", ""))

    def op_base(self, c: Dict[str, Any]) -> str:
        op = c.get("op_name", "")
        return op.rsplit("/", 1)[-1].rsplit(":", 1)[0] if op.startswith("weft:///") else op

    def children(self, call_id: str) -> List[Dict[str, Any]]:
        return sorted([c for c in self.calls.values() if c.get("parent_id") == call_id], key=lambda c: c.get("started_at", ""))

    # ------------------------------------------------------------------ handlers
    def _upsert(self, mode: str, req: Dict[str, Any]) -> Dict[str, Any]:
        cid = req["id"]
        c = self.calls.setdefault(cid, {})
        c.update(req)
        return {"id": cid, "trace_id": c.get("trace_id")} if mode == "start" else {}

    def handle(self, path: str, body: Any, raw: bytes, ctype: str) -> Any:
        if path == "/call/upsert_batch":
            res = []
            for item in body["batch"]:
                mode = item["mode"]
                res.append(self._upsert(mode, item["req"][mode]))
            return {"res": res}
        if path == "/call/start":
            return self._upsert("start", body["start"])
        if path == "/call/end":
            return self._upsert("end", body["end"])
        if path == "/obj/create":
            o = body["obj"]
            d = digest(o["val"])
            versions = self.objs.setdefault(o["object_id"], [])
            for v in versions:
                if v["digest"] == d:
                    return {"digest": d, "object_id": o["object_id"], "version_index": v["version_index"]}
            versions.append({"digest": d, "val": o["val"], "version_index": len(versions), "object_id": o["object_id"],
                             "kind": "op" if o["val"].get("_type") == "Op" else "object", "base_object_class": _base_class(o["val"])})
            return {"digest": d, "object_id": o["object_id"], "version_index": len(versions) - 1}
        if path == "/obj/read":
            versions = self.objs[body["object_id"]]
            dg = body["digest"]
            if dg == "latest":
                v = versions[-1]
            elif re.fullmatch(r"v\d+", dg):
                v = versions[int(dg[1:])]
            else:
                v = next(x for x in versions if x["digest"] == dg)
            return {"obj": {**v, "project_id": body["project_id"]}}
        if path == "/objs/query":
            flt = body.get("filter") or {}
            out = []
            for oid, versions in self.objs.items():
                vs = versions[-1:] if flt.get("latest_only") else versions
                for v in vs:
                    if flt.get("base_object_classes") and v["base_object_class"] not in flt["base_object_classes"]:
                        continue
                    out.append({**v, "project_id": body["project_id"]})
            return {"objs": out}
        if path == "/table/create":
            rows = body["table"]["rows"]
            rds = [digest(r) for r in rows]
            td = table_digest(rds)
            self.tables[td] = [{"digest": rd, "val": r} for rd, r in zip(rds, rows)]
            return {"digest": td, "row_digests": rds}
        if path == "/table/update":
            rows = list(self.tables[body["base_digest"]])
            new = []
            for u in body["updates"]:
                r = u["append"]["row"]
                rows.append({"digest": digest(r), "val": r})
                new.append(digest(r))
            td = table_digest([r["digest"] for r in rows])
            self.tables[td] = rows
            return {"digest": td, "updated_row_digests": new}
        if path == "/table/query":
            rows = self.tables[body["digest"]]
            off = body.get("offset") or 0
            lim = body.get("limit") or len(rows)
            return {"rows": [{**r, "original_index": i} for i, r in enumerate(rows)][off: off + lim]}
        if path == "/file/create":
            m = re.search(rb"filename=\"[^\"]*\"\r\nContent-Type: [^\r]*\r\n\r\n(.*)\r\n--weft-", raw, re.S)
            content = m.group(1) if m else b""
            d = b64digest(content)
            self.files[d] = content
            return {"digest": d}
        if path in ("/calls/stream_query", "/calls/query"):
            calls = self._query(body)
            if path == "/calls/query":
                return {"calls": calls}
            return ("\n".join(json.dumps(c) for c in calls).encode() + b"\n", "application/jsonl")
        if path == "/calls/query_stats":
            return {"count": len(self._query({**body, "limit": None}))}
        if path == "/call/read":
            c = self.calls.get(body["id"])
            return {"call": c}
        if path == "/call/update":
            self.calls[body["call_id"]]["display_name"] = body["display_name"]
            return {}
        if path == "/calls/delete":
            for cid in body["call_ids"]:
                self.calls[cid]["deleted_at"] = "now"
            return {}
        if path == "/feedback/create":
            fb = dict(body)
            fb["id"] = f"fb{len(self.feedback)}"
            self.feedback.append(fb)
            return {"id": fb["id"], "created_at": "now", "payload": fb["payload"]}
        if path == "/feedback/query":
            q = (body.get("query") or {}).get("$expr")
            out = self.feedback
            if q and "$eq" in q:
                field = q["$eq"][0]["$getField"]
                val = q["$eq"][1]["$literal"]
                out = [f for f in out if f.get(field) == val]
            return {"result": out}
        if path == "/feedback/purge":
            fid = body["query"]["$expr"]["$eq"][1]["$literal"]
            self.feedback = [f for f in self.feedback if f["id"] != fid]
            return {}
        if path == "/cost/create":
            self.costs.update(body["costs"])
            return {"ids": [[f"c{i}", k] for i, k in enumerate(body["costs"])]}
        if path == "/cost/query":
            return {"results": [{"id": f"c{i}", "llm_id": k, **v} for i, (k, v) in enumerate(self.costs.items())]}
        if path == "/cost/purge":
            ids = set(body.get("ids") or [])
            self.costs = {k: v for i, (k, v) in enumerate(self.costs.items()) if f"c{i}" not in ids}
            return {}
        if path == "/leaderboard/compute":
            return {"leaderboard": {"object_id": body["object_id"]}, "columns": [], "rows": [], "_request": body}
        if path == "/evaluations/compare":
            return {"evaluations": [{"call_id": c} for c in body["evaluation_call_ids"]], "metrics": [], "rows": []}
        if path == "/evaluate_model":
            cid = "srv-eval-1"
            self.calls[cid] = {"id": cid, "op_name": "Evaluation.evaluate", "output": {"exact": {"true_count": 1, "true_fraction": 1.0}},
                               "project_id": body["project_id"], "trace_id": "t", "inputs": {"self": body["evaluation_ref"], "model": body["model_ref"]}}
            self.eval_polls = 0
            return {"call_id": cid}
        if path == "/evaluation_status":
            self.eval_polls = getattr(self, "eval_polls", 0) + 1
            code = "complete" if self.eval_polls >= 2 else "running"
            return {"status": {"code": code, "completed_rows": self.eval_polls, "total_rows": 2}}
        if path == "/completions/create":
            inputs = body["inputs"]
            last = [m for m in inputs["messages"] if m.get("role") == "user"][-1]["content"]
            rf = (inputs.get("response_format") or {}).get("type")
            content = json.dumps({"correct": True, "echo": last}) if rf == "json_object" else last
            return {"response": {"id": "x", "object": "chat.completion", "model": inputs["model"],
                                 "choices": [{"index": 0, "message": {"role": "assistant", "content": content}, "finish_reason": "stop"}],
                                 "usage": {"prompt_tokens": 7, "completion_tokens": 3, "total_tokens": 10}}}
        if path == "/refs/read_batch":
            return {"vals": [self._read_ref(r) for r in body["refs"]]}
        if path == "/otel/v1/traces":
            self.otel.append(raw)
            return {}
        raise KeyError(path)

    def _read_ref(self, uri: str) -> Any:
        rest = uri.split(":///", 1)[1].split("/")
        kind, name = rest[2], rest[3]
        extra = rest[4:]
        if kind == "object":
            oname, dg = name.rsplit(":", 1)
            val: Any = next(v for v in self.objs[oname] if v["digest"] == dg)["val"]
        else:
            return None
        for i in range(0, len(extra), 2):
            k, p = extra[i], extra[i + 1]
            if isinstance(val, str) and "/table/" in val:
                val = self.tables[val.rsplit("/", 1)[1]]
            if k == "attr" or k == "key":
                val = val[p]
            elif k == "index":
                val = val[int(p)]
            elif k == "id":
                if isinstance(val, str):
                    val = self.tables[val.rsplit("/", 1)[1]]
                val = next(r["val"] for r in val if r["digest"] == p)
        return val

    def _query(self, body: Dict[str, Any]) -> List[Dict[str, Any]]:
        flt = body.get("filter") or {}
        out = []
        for c in self.calls.values():
            if flt.get("op_names"):
                names = flt["op_names"]
                if c.get("op_name") not in names and self.op_base(c) not in names:
                    continue
            if flt.get("trace_ids") and c.get("trace_id") not in flt["trace_ids"]:
                continue
            if flt.get("parent_ids") and c.get("parent_id") not in flt["parent_ids"]:
                continue
            if flt.get("call_ids") and c.get("id") not in flt["call_ids"]:
                continue
            if flt.get("trace_roots_only") and c.get("parent_id"):
                continue
            out.append(c)
        rev = True
        for s in body.get("sort_by") or []:
            rev = s.get("direction") != "asc"
        out.sort(key=lambda c: c.get("started_at", ""), reverse=rev)
        off = body.get("offset") or 0
        lim = body.get("limit")
        return out[off: off + lim if lim else None]
