"""Agent conversation tracing (weft.start_conversation), an agent eval
that links existing traces, a saved view, and media logged by reference.

    WEFT_SERVER_URL=http://localhost:6345 python examples/weft/python/agent_conversation.py

Open the project in the UI: Agents → support-bot shows the conversation.
"""

import time

import weft
from weft.conversation import ToolCallPart

client = weft.init("agent-conversation-demo")


@weft.trace(kind="tool")
def lookup_order(order_id: int) -> dict:
    return {"order_id": order_id, "status": "shipped", "eta": "Friday"}


def fake_llm(question: str) -> str:
    return f"Order {question.split()[-1].strip('?')} has shipped and arrives Friday."


# --- live tracing: one conversation, two turns --------------------------------
turns = []
with weft.start_conversation(agent_name="support-bot", conversation_name="Order questions") as conv:
    for question in ("Where is order 7?", "And order 9?"):
        order_id = int(question.split()[-1].strip("?"))
        with conv.start_turn(user_message=question, system_instructions=["You are a support agent."]) as turn:
            with turn.start_llm(model="gpt-4o-mini", provider_name="openai") as llm:
                llm.input_messages = [weft.Message.user(question)]
                llm.output_messages = [weft.Message.assistant("", tool_calls=[
                    ToolCallPart(id=f"call-{order_id}", name="lookup_order", arguments={"order_id": order_id})])]
                llm.usage = weft.Usage(input_tokens=42, output_tokens=12)
            with turn.start_tool(name="lookup_order", arguments={"order_id": order_id}, tool_call_id=f"call-{order_id}") as tool:
                tool.result = lookup_order(order_id)
            with turn.start_subagent(name="writer", model="gpt-4o") as writer:
                with writer.start_llm() as w:
                    w.output(fake_llm(question))
                    w.usage = weft.Usage(input_tokens=30, output_tokens=15)
            turn.output_messages = [weft.Message.assistant(fake_llm(question))]
        turns.append(turn)

# --- batch logging: replay a finished conversation ---------------------------
t0 = weft.conversation._now()
weft.log_conversation(
    agent_name="support-bot",
    conversation_name="Replayed from logs",
    turns=[weft.Turn(messages=[weft.Message.user("Cancel order 3")],
                     output_messages=[weft.Message.assistant("Order 3 is cancelled.")],
                     spans=[weft.Tool(name="cancel_order", arguments={"order_id": 3}, result="ok", started_at=t0, ended_at=t0)])],
)

# --- agent eval: score the turns we already traced ----------------------------
ev = weft.EvaluationLogger(model="support-bot", dataset="support-questions", name="support eval")
for turn in turns:
    pred = ev.log_prediction({"question": turn.messages[0].content}, output=turn.output_messages[0].content, span=turn)
    pred.log_score("mentions_eta", "Friday" in pred.output)
    pred.finish()
print("eval summary:", ev.log_summary())

# --- a saved view, applied as a query ----------------------------------------
view = (weft.SavedView(label="Support tool calls")
        .filter_op("lookup_order")
        .add_filter("output.status", "equals", "shipped")
        .sort_by("started_at", "desc")
        .save())
client.flush()
time.sleep(0.2)
print("view", view.name, "matches", len(list(view.spans())), "spans")

# --- media that lives in your bucket, logged by reference ---------------------
@weft.trace
def caption(image) -> str:
    return "a parcel on a doorstep"

caption(weft.Image.from_uri("https://example.com/delivery-photo.jpg"))
weft.finish()
