"""Attach feedback (reactions, notes, custom payloads) to calls and query it.

    python examples/weft/python/feedback.py
"""

import weft

client = weft.init("feedback-demo")


@weft.op
def answer(question: str) -> str:
    return "42"


if __name__ == "__main__":
    result, call = answer.call("What is the meaning of life?")
    call.feedback.add_reaction("👍")
    call.feedback.add_note("Correct, if terse.")
    call.feedback.add("quality", {"stars": 5})
    call.set_display_name("meaning-of-life")

    for fb in call.feedback:
        print(fb["feedback_type"], fb["payload"])

    # Custom model prices are used for cost roll-ups in the UI and API.
    client.add_cost("my-finetune", prompt_token_cost=2e-6, completion_token_cost=8e-6)

    # Calls can be filtered by feedback fields with the query language.
    liked = client.get_calls(
        query={"$eq": [{"$getField": "feedback.weft.reaction.emoji"}, {"$literal": "👍"}]},
        include_feedback=True,
    )
    print("liked calls:", [c.display_name or c.op_base_name for c in liked])
