"""An in-process fake Weft server (stdlib http.server) serving Weft's own
``/api/v1`` routes (docs/weft/API.md) and recording the requests.

Stored records use Weft's names: spans ``name``/``error``/``session_id``,
objects ``name``/``version``/``kind``/``value``, table rows ``value``.
"""

from __future__ import annotations

import json
import re
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Dict, List, Optional
from urllib.parse import parse_qs, unquote, urlsplit

from weft._canonical import digest, file_digest, table_digest

_KINDS = {
    "Dataset": "dataset", "Model": "model", "Evaluation": "evaluation", "Scorer": "scorer", "Prompt": "prompt",
    "SavedView": "saved_view", "Leaderboard": "leaderboard", "Monitor": "monitor", "AnnotationSpec": "annotation_spec",
}


def _kind(val: Any) -> str:
    if not isinstance(val, dict):
        return "object"
    if val.get("_type") == "Op":
        return "function"
    for c in [val.get("_class_name")] + list(val.get("_bases") or []):
        if c in _KINDS:
            return _KINDS[c]
    return "object"


class FakeWeft:
    def __init__(self) -> None:
        self.requests: List[Dict[str, Any]] = []
        self.calls: Dict[str, Dict[str, Any]] = {}  # spans by id
        self.objs: Dict[str, List[Dict[str, Any]]] = {}
        self.tables: Dict[str, List[Dict[str, Any]]] = {}
        self.files: Dict[str, bytes] = {}
        self.feedback: List[Dict[str, Any]] = []
        self.costs: Dict[str, Any] = {}
        self.otel: List[bytes] = []
        self.fail_status: Optional[int] = None
        self.fail_paths: Dict[str, int] = {}  # path -> number of upcoming requests to fail with 503
        self.tags: Dict[tuple, List[str]] = {}
        self.aliases: Dict[tuple, str] = {}  # (name, alias) -> digest
        self.registry: Dict[tuple, List[Dict[str, Any]]] = {}  # (project_id, collection) -> versions
        self.embeddings_enabled = False
        self.runtimes: Dict[str, Any] = {}
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

            def do_PUT(self) -> None:
                n = int(self.headers.get("Content-Length") or 0)
                body = json.loads(self.rfile.read(n) or b"{}")
                with fake.lock:
                    fake.requests.append({"method": "PUT", "path": self.path, "body": body, "headers": dict(self.headers)})
                    try:
                        res = fake.handle_put(self.path, body)
                    except KeyError as e:
                        return self._send(404, {"reason": f"not found: {e}"})
                return self._send(200, res)

            def do_GET(self) -> None:
                with fake.lock:
                    fake.requests.append({"method": "GET", "path": self.path, "body": None, "headers": dict(self.headers)})
                    try:
                        res = fake.handle_get(self.path)
                    except KeyError as e:
                        return self._send(404, {"reason": f"not found: {e}"})
                if isinstance(res, tuple):
                    return self._send(200, res[0], res[1])
                return self._send(200, res)

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
                except ValueError as e:
                    return self._send(400, {"reason": str(e)})
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
        """Spans whose function (base) name is ``name``, by start time."""
        return sorted([c for c in self.calls.values() if self.op_base(c) == name], key=lambda c: c.get("started_at", ""))

    spans_by_name = calls_by_op

    def op_base(self, c: Dict[str, Any]) -> str:
        n = c.get("name", "")
        return n.rsplit("/", 1)[-1].rsplit(":", 1)[0] if n.startswith("weft:///") else n

    base_name = op_base

    def children(self, span_id: str) -> List[Dict[str, Any]]:
        return sorted([c for c in self.calls.values() if c.get("parent_id") == span_id], key=lambda c: c.get("started_at", ""))

    # ------------------------------------------------------------------ objects
    def _version(self, name: str, version: str) -> Dict[str, Any]:
        versions = [v for v in self.objs[name]]
        if not versions:
            raise KeyError(name)
        if version == "latest":
            return versions[-1]
        if re.fullmatch(r"v\d+", version):
            return next(v for v in versions if v["version"] == int(version[1:]))
        if (name, version) in self.aliases:
            version = self.aliases[(name, version)]
        for v in versions:
            if v["digest"] == version:
                return v
        raise KeyError(version)

    def _object(self, project_id: str, v: Dict[str, Any], with_value: bool = True) -> Dict[str, Any]:
        e, p = project_id.split("/", 1)
        kind = v["kind"]
        out = {
            "project_id": project_id, "name": v["name"], "digest": v["digest"], "version": v["version"], "kind": kind,
            "class": v.get("class"), "ref": f"weft:///{e}/{p}/{'function' if kind == 'function' else 'object'}/{v['name']}:{v['digest']}",
            "tags": self.tags.get((v["name"], v["digest"]), []),
            "aliases": sorted(a for (o, a), d in self.aliases.items() if o == v["name"] and d == v["digest"]),
            "created_at": "now", "deleted_at": None,
        }
        if with_value:
            out["value"] = v["value"]
        return out

    # ------------------------------------------------------------------ GET and PUT
    def handle_get(self, path: str) -> Any:
        u = urlsplit(path)
        q = {k: v[0] for k, v in parse_qs(u.query).items()}
        p = u.path
        if p == "/api/v1/server":
            return {"name": "weft", "version": "fake", "api_version": "v1", "auth_required": False, "otel_endpoint": "/otel/v1/traces"}
        if p == "/auth/whoami":
            return {"user_id": "anonymous", "auth_required": False}
        if p == "/health":
            return {"status": "ok"}
        m = re.fullmatch(r"/api/v1/files/([^/]+)", p)
        if m:
            return (self.files[unquote(m.group(1))], "application/octet-stream")
        m = re.fullmatch(r"/api/v1/objects/([^/]+)(?:/versions/([^/]+))?", p)
        if m:
            name = unquote(m.group(1))
            return self._object(q["project_id"], self._version(name, unquote(m.group(2) or "latest")))
        m = re.fullmatch(r"/api/v1/spans/([^/]+)", p)
        if m:
            return self.calls[unquote(m.group(1))]
        m = re.fullmatch(r"/api/v1/evaluations/([^/]+)/status", p)
        if m:
            self.eval_polls = getattr(self, "eval_polls", 0) + 1
            code = "complete" if self.eval_polls >= 2 else "running"
            return {"code": code, "completed_rows": self.eval_polls, "total_rows": 2}
        raise KeyError(path)

    def handle_put(self, path: str, body: Any) -> Any:
        m = re.fullmatch(r"/api/v1/runtimes/([^/]+)", path)
        if m:
            name = unquote(m.group(1))
            ids = [{"id": r["id"], "max_tokens": r.get("max_tokens") or 4096, "playground_id": f"{name}/{r['id']}"} for r in body["runtime_ids"]]
            self.runtimes[name] = {"name": name, "base_url": body["base_url"], "api_key_secret": body.get("api_key_secret"),
                                   "headers": body.get("headers") or {}, "runtime_ids": ids, "kind": body.get("kind") or "openai"}
            return self.runtimes[name]
        raise KeyError(path)

    # ------------------------------------------------------------------ POST
    def _upsert(self, mode: str, req: Dict[str, Any]) -> Dict[str, Any]:
        sid = req["id"]
        c = self.calls.setdefault(sid, {})
        c.update(req)
        return {"id": sid, "trace_id": c.get("trace_id")} if mode == "start" else {"id": sid}

    def handle(self, path: str, body: Any, raw: bytes, ctype: str) -> Any:
        if path == "/api/v1/spans/batch":
            res = []
            for item in body["items"]:
                if len(item) != 1:
                    raise ValueError("expected exactly one of start, end or span")
                (mode, req), = item.items()
                res.append(self._upsert("start" if mode in ("start", "span") else "end", req))
            return {"items": res}
        if path == "/api/v1/spans/start":
            return self._upsert("start", body)
        if path == "/api/v1/spans/end":
            return self._upsert("end", body)
        if path in ("/api/v1/spans/query", "/api/v1/spans/stream"):
            spans = self._query(body)
            if path == "/api/v1/spans/query":
                total = len(self._query({**body, "limit": None, "offset": 0}))
                return {"items": spans if body.get("limit") != 0 else [], "total_count": total}
            return ("".join(json.dumps(c) + "\n" for c in spans).encode(), "application/x-ndjson")
        if path == "/api/v1/spans/update":
            self.calls[body["id"]]["display_name"] = body["display_name"]
            return {}
        if path == "/api/v1/spans/delete":
            for sid in body["ids"]:
                self.calls[sid]["deleted_at"] = "now"
            return {}
        if path == "/api/v1/spans/sessions":
            sessions: Dict[str, Dict[str, Any]] = {}
            for c in self.calls.values():
                sid = c.get("session_id")
                if not sid or c.get("project_id") != body["project_id"]:
                    continue
                t = sessions.setdefault(sid, {"session_id": sid, "turn_count": 0, "span_count": 0, "started_at": c.get("started_at"),
                                              "last_updated": c.get("ended_at") or c.get("started_at")})
                t["span_count"] += 1
                if c.get("turn_id") == c.get("id"):
                    t["turn_count"] += 1
                t["started_at"] = min(t["started_at"], c.get("started_at"))
            out = sorted(sessions.values(), key=lambda t: t["session_id"])
            for sb in reversed(body.get("sort_by") or []):
                out.sort(key=lambda t: t.get(sb["field"]) or 0, reverse=sb.get("direction") == "desc")
            total = len(out)
            off = body.get("offset") or 0
            lim = body.get("limit")
            return {"items": out[off:][: 100 if lim is None else lim], "total_count": total}
        if path == "/api/v1/objects/save":
            name, val = body["name"], body["value"]
            if not re.fullmatch(r"[A-Za-z0-9_.-]{1,128}", name):
                raise ValueError(f"invalid name {name!r}")
            d = digest(val)
            versions = self.objs.setdefault(name, [])
            for v in versions:
                if v["digest"] == d:
                    return {"name": name, "digest": d, "version": v["version"], "kind": v["kind"], "ref": self._object(body["project_id"], v)["ref"]}
            v = {"name": name, "digest": d, "value": val, "version": len(versions), "kind": _kind(val),
                 "class": None if _kind(val) == "function" else (val.get("_class_name") if isinstance(val, dict) else None)}
            versions.append(v)
            return {"name": name, "digest": d, "version": v["version"], "kind": v["kind"], "ref": self._object(body["project_id"], v)["ref"]}
        if path == "/api/v1/objects/query":
            flt = body.get("filter") or {}
            out = []
            for name, versions in self.objs.items():
                if flt.get("names") and name not in flt["names"]:
                    continue
                vs = versions[-1:] if flt.get("latest_only") else versions
                for v in vs:
                    if flt.get("kinds") and v["kind"] not in flt["kinds"]:
                        continue
                    out.append(self._object(body["project_id"], v, not body.get("metadata_only")))
            return {"items": out, "total_count": len(out)}
        if path == "/api/v1/objects/delete":
            versions = self.objs.get(body["name"], [])
            digests = body.get("digests")
            keep = [v for v in versions if digests is not None and v["digest"] not in digests and f"v{v['version']}" not in digests]
            n = len(versions) - len(keep)
            self.objs[body["name"]] = keep
            return {"num_deleted": n}
        if path == "/api/v1/objects/tags":
            tags = sorted({t for ts in self.tags.values() for t in ts})
            return {"items": tags, "total_count": len(tags)}
        if path == "/api/v1/objects/aliases":
            aliases = sorted({a for (_, a) in self.aliases})
            return {"items": aliases, "total_count": len(aliases)}
        m = re.fullmatch(r"/api/v1/objects/([^/]+)/aliases(/remove)?", path)
        if m:
            name = unquote(m.group(1))
            for a in body["aliases"]:
                if m.group(2):
                    self.aliases.pop((name, a), None)
                else:
                    self.aliases[(name, a)] = self._version(name, body["version"])["digest"]
            return {}
        m = re.fullmatch(r"/api/v1/objects/([^/]+)/versions/([^/]+)/tags(/remove)?", path)
        if m:
            name = unquote(m.group(1))
            key = (name, self._version(name, unquote(m.group(2)))["digest"])
            cur = self.tags.setdefault(key, [])
            if m.group(3):
                self.tags[key] = [t for t in cur if t not in body["tags"]]
            else:
                cur.extend(t for t in body["tags"] if t not in cur)
            return {}
        if path == "/api/v1/tables/create":
            rows = body["rows"]
            rds = [digest(r) for r in rows]
            td = table_digest(rds)
            self.tables[td] = [{"digest": rd, "value": r} for rd, r in zip(rds, rows)]
            return {"digest": td, "row_digests": rds}
        if path == "/api/v1/tables/update":
            rows = list(self.tables[body["digest"]])
            new = []
            for u in body["updates"]:
                r = u["append"]["row"]
                rows.append({"digest": digest(r), "value": r})
                new.append(digest(r))
            td = table_digest([r["digest"] for r in rows])
            self.tables[td] = rows
            return {"digest": td, "updated_row_digests": new}
        if path == "/api/v1/tables/query":
            rows = self.tables[body["digest"]]
            off = body.get("offset") or 0
            lim = body.get("limit") or len(rows)
            return {"items": [{**r, "index": i} for i, r in enumerate(rows)][off: off + lim], "total_count": len(rows)}
        if path == "/api/v1/files/upload":
            m = re.search(rb"filename=\"[^\"]*\"\r\nContent-Type: [^\r]*\r\n\r\n(.*)\r\n--weft-", raw, re.S)
            content = m.group(1) if m else b""
            d = file_digest(content)
            self.files[d] = content
            return {"digest": d, "size_bytes": len(content)}
        if path == "/api/v1/files/resolve":
            out = []
            for u in body["uris"]:
                if u.startswith(("http://", "https://")):
                    out.append(u)
                elif u.startswith("s3://"):
                    out.append("https://s3.fake/" + u[5:] + "?X-Amz-Signature=fake")
                else:
                    d = u.split("://", 1)[-1]
                    if d not in self.files:
                        raise KeyError(d)
                    out.append(f"{self.url}/api/v1/files/{d}?project_id={body['project_id']}")
            return {"download_urls": out, "expires_at": None}
        if path == "/api/v1/refs/read":
            return {"items": [self._read_ref(r) for r in body["refs"]]}
        if path == "/api/v1/feedback/create":
            fb = dict(body)
            fb["id"] = f"fb{len(self.feedback)}"
            self.feedback.append(fb)
            return {"id": fb["id"], "created_at": "now", "payload": fb["payload"]}
        if path == "/api/v1/feedback/query":
            q = (body.get("query") or {}).get("$expr")
            out = self.feedback
            if q and "$eq" in q:
                field = q["$eq"][0]["$getField"]
                val = q["$eq"][1]["$literal"]
                out = [f for f in out if f.get(field) == val]
            return {"items": out, "total_count": len(out)}
        if path == "/api/v1/feedback/delete":
            ids = set(body.get("ids") or [])
            self.feedback = [f for f in self.feedback if f["id"] not in ids]
            return {}
        if path == "/api/v1/prices/create":
            self.costs.update(body["prices"])
            return {"items": [{"id": f"c{i}", "llm_id": k} for i, k in enumerate(body["prices"])]}
        if path == "/api/v1/prices/query":
            items = [{"id": f"c{i}", "llm_id": k, **v} for i, (k, v) in enumerate(self.costs.items())]
            return {"items": items, "total_count": len(items)}
        if path == "/api/v1/prices/delete":
            ids = set(body.get("ids") or [])
            self.costs = {k: v for i, (k, v) in enumerate(self.costs.items()) if f"c{i}" not in ids}
            return {}
        if path == "/api/v1/registry/link":
            key = (body["project_id"], body["collection"])
            versions = self.registry.setdefault(key, [])
            v = next((x for x in versions if x["ref"] == body["ref"]), None)
            if v is None:
                v = {"version_index": len(versions), "ref": body["ref"], "aliases": []}
                versions.append(v)
            for a in body.get("aliases") or []:
                for x in versions:
                    if a in x["aliases"]:
                        x["aliases"].remove(a)
                v["aliases"].append(a)
            return {"version": v["version_index"]}
        if path == "/api/v1/registry/query":
            out = []
            for (pid, name), versions in self.registry.items():
                if pid != body["project_id"] or (body.get("portfolio_name") and name != body["portfolio_name"]):
                    continue
                vs = [dict(v, version=f"v{v['version_index']}") for v in reversed(versions)]
                if body.get("alias"):
                    vs = [v for v in vs if body["alias"] in v["aliases"]]
                if body.get("version_index") is not None:
                    vs = [v for v in vs if v["version_index"] == body["version_index"]]
                out.append({"name": name, "versions": vs})
            return {"collections": out}
        if path == "/api/v1/leaderboards/compute":
            return {"leaderboard": {"name": body["name"]}, "columns": [], "rows": [], "_request": body}
        if path == "/api/v1/evaluations/compare":
            return {"evaluations": [{"id": c} for c in body["ids"]], "metrics": [], "rows": []}
        if path == "/api/v1/evaluations/run":
            rid = "srv-eval-1"
            self.calls[rid] = {"id": rid, "name": "Evaluation.evaluate", "output": {"exact": {"true_count": 1, "true_fraction": 1.0}},
                               "project_id": body["project_id"], "trace_id": "t", "inputs": {"self": body["evaluation_ref"], "model": body["model_ref"]}}
            self.eval_polls = 0
            return {"id": rid}
        if path == "/api/v1/completions":
            inputs = body["inputs"]
            last = [m for m in inputs["messages"] if m.get("role") == "user"][-1]["content"]
            rf = (inputs.get("response_format") or {}).get("type")
            content = json.dumps({"correct": True, "echo": last}) if rf == "json_object" else last
            return {"response": {"id": "x", "object": "chat.completion", "model": inputs["model"],
                                 "choices": [{"index": 0, "message": {"role": "assistant", "content": content}, "finish_reason": "stop"}],
                                 "usage": {"prompt_tokens": 7, "completion_tokens": 3, "total_tokens": 10}}, "span_id": None}
        if path == "/api/v1/embeddings":
            if not self.embeddings_enabled:
                raise KeyError(path)
            vecs = [[float(len(t)), 1.0] for t in body["inputs"]["input"]]
            return {"response": {"model": body["inputs"]["model"], "data": [{"embedding": v} for v in vecs], "usage": {"prompt_tokens": 4, "total_tokens": 4}}}
        if path == "/remote/score":
            return {"score": 0.9, "received": body}
        if path == "/otel/v1/traces":
            self.otel.append(raw)
            return {}
        raise KeyError(path)

    def _read_ref(self, uri: str) -> Any:
        rest = uri.split(":///", 1)[1].split("/")
        kind, name = rest[2], rest[3]
        extra = rest[4:]
        if kind in ("object", "function"):
            oname, dg = name.rsplit(":", 1)
            val: Any = self._version(oname, dg)["value"]
        else:
            return None
        for i in range(0, len(extra), 2):
            k, p = extra[i], unquote(extra[i + 1])
            if isinstance(val, str) and "/table/" in val:
                val = self.tables[val.rsplit("/", 1)[1]]
            if k == "attr" or k == "key":
                val = val[p]
            elif k == "index":
                val = val[int(p)]
            elif k == "id":
                if isinstance(val, str):
                    val = self.tables[val.rsplit("/", 1)[1]]
                val = next(r["value"] for r in val if r["digest"] == p)
        return val

    _FILTER_KEYS = {"names", "ids", "trace_ids", "parent_ids", "session_ids", "turn_ids", "user_ids", "run_ids",
                    "input_refs", "output_refs", "trace_roots_only"}

    def _query(self, body: Dict[str, Any]) -> List[Dict[str, Any]]:
        flt = body.get("filter") or {}
        unknown = set(flt) - self._FILTER_KEYS
        if unknown:
            raise ValueError(f"unknown filter fields {sorted(unknown)}")
        out = []
        for c in self.calls.values():
            if c.get("deleted_at") and not body.get("include_deleted"):
                continue
            if flt.get("names"):
                names = flt["names"]
                if c.get("name") not in names and self.op_base(c) not in names:
                    continue
            if flt.get("trace_ids") and c.get("trace_id") not in flt["trace_ids"]:
                continue
            if flt.get("parent_ids") and c.get("parent_id") not in flt["parent_ids"]:
                continue
            if flt.get("ids") and c.get("id") not in flt["ids"]:
                continue
            if flt.get("session_ids") and c.get("session_id") not in flt["session_ids"]:
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
