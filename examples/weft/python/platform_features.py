"""Span kinds, durability settings, Content, object management, guardrails and
the extra built-in scorers.

    python examples/weft/python/platform_features.py
"""

import weft
from weft import scorers

client = weft.init(
    "platform-demo",
    settings={
        "global_attributes": {"app": "support-bot", "env": "dev"},
        "enable_disk_fallback": True,  # unsent data survives restarts (~/.weft/wal)
        "print_call_link": True,       # prints <server>/<entity>/<project>/calls/<id> per root call
    },
)


@weft.op(kind="retriever")
def search_docs(query: str) -> list:
    return ["Refunds are processed within 5 days.", "Contact support@example.com for help."]


@weft.op(kind="llm")
def draft_answer(query: str, docs: list) -> str:
    return f"{docs[0]} (answering: {query})"


@weft.op(kind="agent", color="blue")
def support_agent(query: str) -> dict:
    docs = search_docs(query)
    answer = draft_answer(query, docs)
    # files and rich media are uploaded and rendered by the UI
    return {"answer": answer, "transcript": weft.Markdown(f"**Q:** {query}\n\n**A:** {answer}")}


if __name__ == "__main__":
    result, call = support_agent.call("How long do refunds take?")

    # guardrails: score the call and block on the result
    rule = scorers.RuleScorer(
        name="mentions_days",
        rule={"$contains": {"input": {"$getField": "output.answer"}, "substr": {"$literal": "days"}}},
    )
    check = call.apply_scorer_sync(rule)
    print("guardrail passed:", check.result)

    with weft.tracing_disabled():
        support_agent("not recorded")

    # attach a document to a call
    @weft.op
    def attach_policy() -> weft.Content:
        return weft.Content.from_text("Refund policy: 5 business days.", filename="policy.txt")

    attach_policy()

    # datasets, aliases and tags
    ds = weft.Dataset(name="support-questions", rows=[{"query": "How long do refunds take?"}, {"query": "How do I contact support?"}])
    ref = weft.publish(ds)
    client.set_aliases(ref, "production")
    client.add_tags(ref, ["reviewed"])
    print("aliases:", client.get_aliases(ref), "tags:", client.get_tags(ref))

    # imperative evaluation linked to the dataset rows
    ev = weft.EvaluationLogger(model="support-agent-v1", dataset=ds, name="support-eval")
    for row in ds.rows:
        out = support_agent(row["query"])["answer"]
        ev.log_example(row, out, {"non_empty": bool(out), "mentions_days": "days" in out})
    print("summary:", ev.log_summary())

    # more built-in scorers (all serialise for server-side runs too)
    xml = scorers.ValidXMLScorer()
    schema = scorers.JSONSchemaScorer(schema={"type": "object", "required": ["answer"]})
    print("xml:", xml.score(output="<ok/>"), "schema:", schema.score(output={"answer": "x"}))
    judge = scorers.RelevanceScorer(model="mock/json")  # offline mock model on the server
    print("relevance:", judge.score(output=result["answer"], query="How long do refunds take?"))
    weft.flush()
