#!/usr/bin/env python3
"""Seed a Weft server with a realistic demo project.

Uses only the standard library and the HTTP API (plus OTLP JSON), so it works
without the SDK installed. Offline mock models are used for evaluations.

    python3 scripts/weft_seed_demo.py --url http://127.0.0.1:6345 --project demo/support-bot
"""
import argparse
import json
import random
import time
import urllib.request
import uuid
from datetime import datetime, timedelta, timezone

QUESTIONS = [
    ("Where is my order #4512?", "Your order #4512 shipped yesterday and arrives Friday."),
    ("How do I reset my password?", "Use 'Forgot password' on the sign-in page; the link is valid for 1 hour."),
    ("Can I change my delivery address?", "Yes, until the order ships. Go to Orders, then Edit address."),
    ("What is your refund policy?", "Refunds are issued within 30 days of delivery for unused items."),
    ("Do you ship to Canada?", "Yes, we ship to Canada; delivery takes 5-8 business days."),
    ("My discount code does not work", "Codes are case sensitive and expire at midnight UTC."),
    ("How do I cancel my subscription?", "Go to Account, then Billing, then Cancel subscription."),
    ("Is the blue jacket in stock in size M?", "The blue jacket is in stock in size M."),
]
DOCS = {
    "order": "Orders ship within 2 days. Tracking is emailed at dispatch.",
    "password": "Password reset links expire after 1 hour.",
    "refund": "Unused items can be refunded within 30 days of delivery.",
    "shipping": "International shipping covers Canada, UK and EU.",
}


class Client:
    def __init__(self, url, project, api_key=None):
        self.url, self.project, self.key = url.rstrip("/"), project, api_key
        self.entity, self.name = project.split("/", 1)

    def post(self, path, body, headers=None):
        h = {"Content-Type": "application/json"}
        if self.key:
            h["Authorization"] = "Bearer " + self.key
        h.update(headers or {})
        req = urllib.request.Request(self.url + path, json.dumps(body).encode(), h)
        with urllib.request.urlopen(req) as r:
            raw = r.read()
        return json.loads(raw) if raw.strip() else {}

    def ref(self, kind, name, digest=None):
        base = f"weft:///{self.project}/{kind}/{name}"
        return f"{base}:{digest}" if digest else base

    def publish(self, object_id, val):
        r = self.post("/obj/create", {"obj": {"project_id": self.project, "object_id": object_id, "val": val}})
        return self.ref("object", object_id, r["digest"])

    def op(self, name, code):
        r = self.post("/obj/create", {"obj": {"project_id": self.project, "object_id": name,
                                              "val": {"_type": "Op", "name": name, "code": code, "language": "python"}}})
        return f"weft:///{self.project}/op/{name}:{r['digest']}"


def iso(t):
    return t.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%fZ")


class Trace:
    """Collects calls for one trace and sends them in a single batch."""

    def __init__(self, client, start, thread_id=None):
        self.c, self.t, self.items = client, start, []
        self.trace_id, self.thread_id = str(uuid.uuid4()), thread_id

    def call(self, op, inputs, output, dur_ms, parent=None, usage=None, exception=None, display=None, offset_ms=0, turn=False):
        cid = self.trace_id if parent is None and not self.items else str(uuid.uuid4())
        st = self.t + timedelta(milliseconds=offset_ms)
        start = {"project_id": self.c.project, "id": cid, "trace_id": self.trace_id, "parent_id": parent, "op_name": op,
                 "started_at": iso(st), "inputs": inputs, "display_name": display, "attributes": {"env": "prod", "app_version": "1.4.2"}}
        if self.thread_id:
            start["thread_id"] = self.thread_id
            if turn:
                start["turn_id"] = cid
        end = {"project_id": self.c.project, "id": cid, "ended_at": iso(st + timedelta(milliseconds=dur_ms)), "output": output,
               "summary": {"usage": usage} if usage else {}, "exception": exception}
        self.items.append({"mode": "start", "req": {"start": start}})
        self.items.append({"mode": "end", "req": {"end": end}})
        return cid

    def send(self):
        self.c.post("/call/upsert_batch", {"batch": self.items})


def chat_usage(model, p, c):
    return {model: {"requests": 1, "prompt_tokens": p, "completion_tokens": c, "total_tokens": p + c}}


def seed_traces(c, ops, n=40):
    now = datetime.now(timezone.utc)
    threads = [f"conv-{i:03d}" for i in range(6)]
    for i in range(n):
        q, a = QUESTIONS[i % len(QUESTIONS)]
        start = now - timedelta(hours=random.uniform(0, 72))
        thread = threads[i % len(threads)] if i < 24 else None
        tr = Trace(c, start, thread)
        model = random.choice(["gpt-4o-mini", "gpt-4o-mini", "claude-sonnet-4-5", "gpt-4.1"])
        failed = random.random() < 0.08
        total = random.randint(900, 4200)
        root = tr.call(ops["support_agent"], {"question": q, "user_id": f"u{random.randint(100, 999)}"},
                       None if failed else {"answer": a, "sources": ["kb://faq"]}, total,
                       exception="RateLimitError: 429 Too Many Requests\n  at openai._base_client.request" if failed else None, turn=True)
        key = next((k for k in DOCS if k in q.lower()), "order")
        tr.call(ops["retrieve_docs"], {"query": q, "k": 3}, [{"id": f"kb://{key}", "text": DOCS[key], "score": 0.82}],
                random.randint(40, 180), parent=root, offset_ms=10)
        msgs = [{"role": "system", "content": "You are a concise, friendly support agent for an online clothing store."},
                {"role": "user", "content": f"Context:\n{DOCS[key]}\n\nQuestion: {q}"}]
        p, comp = random.randint(180, 900), random.randint(20, 160)
        if "order" in q.lower():
            llm1 = tr.call("openai.chat.completions.create", {"model": model, "messages": msgs, "temperature": 0.2,
                                                               "tools": [{"type": "function", "function": {"name": "lookup_order"}}]},
                           {"id": "chatcmpl-1", "model": model, "choices": [{"index": 0, "finish_reason": "tool_calls", "message": {
                               "role": "assistant", "content": None, "tool_calls": [{"id": "call_1", "type": "function",
                                                                                     "function": {"name": "lookup_order", "arguments": "{\"order_id\": \"4512\"}"}}]}}]},
                           random.randint(300, 900), parent=root, usage=chat_usage(model, p, 18), offset_ms=200)
            tr.call(ops["lookup_order"], {"order_id": "4512"}, {"status": "shipped", "eta": "Friday"}, 60, parent=root, offset_ms=1100)
            msgs = msgs + [{"role": "assistant", "content": None, "tool_calls": [{"id": "call_1", "type": "function",
                                                                                 "function": {"name": "lookup_order", "arguments": "{\"order_id\": \"4512\"}"}}]},
                           {"role": "tool", "tool_call_id": "call_1", "content": "{\"status\": \"shipped\", \"eta\": \"Friday\"}"}]
            _ = llm1
        tr.call("openai.chat.completions.create", {"model": model, "messages": msgs, "temperature": 0.2},
                {"id": "chatcmpl-2", "model": model, "choices": [{"index": 0, "finish_reason": "stop",
                                                                  "message": {"role": "assistant", "content": a}}]},
                random.randint(400, total - 300), parent=root, usage=chat_usage(model, p + 40, comp), offset_ms=1250,
                exception="RateLimitError: 429 Too Many Requests" if failed else None)
        tr.send()
        if not failed and random.random() < 0.4:
            c.post("/feedback/create", {"project_id": c.project, "ref": c.ref("call", root),
                                        "feedback_type": "weft.reaction", "payload": {"emoji": random.choice(["👍", "👍", "👎", "🎉"])}})
        if random.random() < 0.15:
            c.post("/feedback/create", {"project_id": c.project, "ref": c.ref("call", root), "feedback_type": "weft.note",
                                        "payload": {"note": "Answer is correct but could link to the help article."}})


def hexid(n):
    return "".join(random.choice("0123456789abcdef") for _ in range(n))


def seed_otlp(c, n=8):
    """GenAI semantic-convention spans as an OpenTelemetry SDK would export them."""
    now = time.time_ns()
    spans = []
    for i in range(n):
        trace, root, llm = hexid(32), hexid(16), hexid(16)
        t0 = now - random.randint(1, 48) * 3600 * 10**9
        q, a = random.choice(QUESTIONS)

        def attr(k, v):
            if isinstance(v, int):
                return {"key": k, "value": {"intValue": str(v)}}
            return {"key": k, "value": {"stringValue": v}}

        spans.append({"traceId": trace, "spanId": root, "name": "invoke_agent triage", "kind": 1,
                      "startTimeUnixNano": str(t0), "endTimeUnixNano": str(t0 + 2_100_000_000),
                      "attributes": [attr("gen_ai.operation.name", "invoke_agent"), attr("gen_ai.agent.name", "triage"),
                                     attr("input.value", json.dumps({"ticket": q})), attr("output.value", json.dumps({"route": "billing"}))]})
        spans.append({"traceId": trace, "spanId": llm, "parentSpanId": root, "name": "chat claude-haiku-4-5", "kind": 3,
                      "startTimeUnixNano": str(t0 + 100_000_000), "endTimeUnixNano": str(t0 + 1_900_000_000),
                      "attributes": [attr("gen_ai.operation.name", "chat"), attr("gen_ai.system", "anthropic"),
                                     attr("gen_ai.request.model", "claude-haiku-4-5"), attr("gen_ai.response.model", "claude-haiku-4-5"),
                                     attr("gen_ai.input.messages", json.dumps([{"role": "user", "parts": [{"type": "text", "content": q}]}])),
                                     attr("gen_ai.output.messages", json.dumps([{"role": "assistant", "finish_reason": "end_turn",
                                                                                "parts": [{"type": "text", "content": a}]}])),
                                     attr("gen_ai.usage.input_tokens", random.randint(80, 300)),
                                     attr("gen_ai.usage.output_tokens", random.randint(20, 90))]})
    body = {"resourceSpans": [{"resource": {"attributes": [{"key": "service.name", "value": {"stringValue": "triage-service"}}]},
                               "scopeSpans": [{"scope": {"name": "opentelemetry.instrumentation.anthropic"}, "spans": spans}]}]}
    c.post("/otel/v1/traces", body, {"project_id": c.project})


def seed_assets(c):
    rows = [{"question": q, "expected": a.split(";")[0]} for q, a in QUESTIONS]
    table = c.post("/table/create", {"table": {"project_id": c.project, "rows": rows}})
    ds = c.publish("support-questions", {"_type": "Dataset", "_class_name": "Dataset", "_bases": ["Object", "BaseModel"],
                                         "name": "support-questions", "description": "Common customer questions with reference answers",
                                         "rows": c.ref("table", table["digest"])})
    c.publish("support-system-prompt", {"_class_name": "MessagesPrompt", "_bases": ["Prompt", "Object", "BaseModel"],
                                        "name": "support-system-prompt", "messages": [
                                            {"role": "system", "content": "You are a concise, friendly support agent for an online clothing store."},
                                            {"role": "user", "content": "{question}"}]})
    exact = c.publish("exact-match", {"_class_name": "ExactMatchScorer", "_bases": ["Scorer", "Object", "BaseModel"],
                                      "name": "exact-match", "target_column": "expected"})
    contains = c.publish("mentions-answer", {"_class_name": "ContainsScorer", "_bases": ["Scorer", "Object", "BaseModel"],
                                             "name": "mentions-answer", "target_column": "question", "case_insensitive": True})
    judge = c.publish("helpfulness-judge", {"_class_name": "LLMAsAJudgeScorer", "_bases": ["Scorer", "Object", "BaseModel"],
                                            "name": "helpfulness-judge", "model": "mock/json", "response_format": "json_object",
                                            "scoring_prompt": "Question: {question}\nAnswer: {output}\nRate helpfulness 1-5 as JSON {\"score\": n, \"correct\": bool}."})
    ev = c.publish("support-eval", {"_type": "Evaluation", "_class_name": "Evaluation", "_bases": ["Object", "BaseModel"],
                                    "name": "support-eval", "dataset": ds, "scorers": [exact, contains, judge], "trials": 1})
    models = []
    for mid, tmpl in [("echo-baseline", "{question}"), ("polite-echo", "Thanks for asking! {question}")]:
        models.append(c.publish(mid, {"_class_name": "LLMStructuredCompletionModel", "_bases": ["Model", "Object", "BaseModel"],
                                      "name": mid, "llm_model_id": "mock/echo",
                                      "default_params": {"messages_template": [{"role": "user", "content": tmpl}], "temperature": 0}}))
    call_ids = [c.post("/evaluate_model", {"project_id": c.project, "evaluation_ref": ev, "model_ref": m})["call_id"] for m in models]
    for cid in call_ids:
        for _ in range(200):
            st = c.post("/evaluation_status", {"project_id": c.project, "call_id": cid})["status"]
            if st["code"] != "running":
                break
            time.sleep(0.1)
    c.publish("support-leaderboard", {"_class_name": "Leaderboard", "_bases": ["Object", "BaseModel"], "name": "support-leaderboard",
                                      "description": "Support answer quality", "columns": [
                                          {"evaluation_object_ref": ev, "scorer_name": "helpfulness-judge", "summary_metric_path": "score.mean", "should_minimize": False},
                                          {"evaluation_object_ref": ev, "scorer_name": "mentions-answer", "summary_metric_path": "true_fraction", "should_minimize": False},
                                          {"evaluation_object_ref": ev, "scorer_name": "model_latency", "summary_metric_path": "mean", "should_minimize": True}]})
    c.publish("answer-quality", {"_class_name": "AnnotationSpec", "_bases": ["Object", "BaseModel"], "name": "answer-quality",
                                 "description": "Reviewer rating of the final answer", "unique_among_creators": True, "op_scope": None,
                                 "field_schema": {"type": "integer", "minimum": 1, "maximum": 5}})
    c.publish("resolution", {"_class_name": "AnnotationSpec", "_bases": ["Object", "BaseModel"], "name": "resolution",
                             "description": "Did the answer resolve the ticket?", "unique_among_creators": True, "op_scope": None,
                             "field_schema": {"type": "string", "enum": ["resolved", "partially", "unresolved"]}})
    c.publish("helpfulness-monitor", {"_class_name": "Monitor", "_bases": ["Object", "BaseModel"], "name": "helpfulness-monitor",
                                      "description": "Judge a sample of production answers", "active": True, "sampling_rate": 0.5,
                                      "op_names": ["support_agent"], "scorers": [judge]})
    return call_ids


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--url", default="http://127.0.0.1:6345")
    ap.add_argument("--project", default="demo/support-bot")
    ap.add_argument("--api-key")
    ap.add_argument("--traces", type=int, default=40)
    args = ap.parse_args()
    random.seed(7)
    c = Client(args.url, args.project, args.api_key)
    ops = {name: c.op(name, code) for name, code in [
        ("support_agent", "@weft.op\ndef support_agent(question: str, user_id: str) -> dict:\n    docs = retrieve_docs(question, k=3)\n    return answer(question, docs)\n"),
        ("retrieve_docs", "@weft.op\ndef retrieve_docs(query: str, k: int = 3) -> list[dict]:\n    return index.search(query, k)\n"),
        ("lookup_order", "@weft.op\ndef lookup_order(order_id: str) -> dict:\n    return orders_api.get(order_id)\n"),
    ]}
    # Assets first so the monitor exists before production traffic arrives.
    eval_ids = seed_assets(c)
    seed_traces(c, ops, args.traces)
    seed_otlp(c)
    print(f"Seeded {args.project}: {args.traces} agent traces, OTLP spans, datasets, evaluations {eval_ids}")
    print(f"Open {args.url}/{args.project}/traces")


if __name__ == "__main__":
    main()
