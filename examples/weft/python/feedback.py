"""Attach feedback (reactions, notes, custom payloads) to spans and query it.

    python examples/weft/python/feedback.py
"""

import weft

client = weft.init("feedback-demo")


@weft.trace
def answer(question: str) -> str:
    return "42"


if __name__ == "__main__":
    result, span = answer.call("What is the meaning of life?")
    span.feedback.add_reaction("👍")
    span.feedback.add_note("Correct, if terse.")
    span.feedback.add("quality", {"stars": 5})
    span.set_display_name("meaning-of-life")

    for fb in span.feedback:
        print(fb["feedback_type"], fb["payload"])

    # Custom model prices are used for cost roll-ups in the UI and API.
    client.add_cost("my-finetune", prompt_token_cost=2e-6, completion_token_cost=8e-6)

    # Calls can be filtered by feedback fields with the query language.
    liked = client.spans(
        query={"$eq": [{"$getField": "feedback.weft.reaction.emoji"}, {"$literal": "👍"}]},
        include_feedback=True,
    )
    print("liked spans:", [c.display_name or c.base_name for c in liked])
