"""A nested agent with tool calls and a conversation session.

Uses OpenAI when OPENAI_API_KEY is set (auto-patched by weft.init), otherwise
the Weft server's offline ``mock/echo`` model through /completions/create.

    python examples/weft/python/agent_with_tools.py
"""

import json
import os

import weft

client = weft.init("agent-demo")


@weft.trace(kind="tool")
def get_weather(city: str) -> dict:
    return {"city": city, "forecast": "sunny", "temp_c": 21}


@weft.trace(kind="tool")
def calculator(expression: str) -> float:
    allowed = set("0123456789+-*/(). ")
    if not set(expression) <= allowed:
        raise ValueError(f"unsupported expression: {expression!r}")
    return float(eval(expression))  # noqa: S307 - restricted character set


TOOLS = {"get_weather": get_weather, "calculator": calculator}


@weft.trace(kind="llm")
def llm(messages: list) -> str:
    if os.environ.get("OPENAI_API_KEY"):
        from openai import OpenAI  # traced automatically as openai.chat.completions.create

        resp = OpenAI().chat.completions.create(model="gpt-4o-mini", messages=messages)
        return resp.choices[0].message.content
    # Offline: the server's mock model echoes the last user message and the
    # usage is reported on this span (the server rolls it up to the root).
    resp = client.completions_create({"model": "mock/echo", "messages": messages})
    u = resp.get("usage") or {}
    weft.add_usage(weft.get_current_span(), resp.get("model", "mock/echo"), u.get("prompt_tokens", 0), u.get("completion_tokens", 0))
    return resp["choices"][0]["message"]["content"]


@weft.trace
def plan(question: str) -> list:
    """A toy planner deciding which tools to call."""
    steps = []
    if "weather" in question.lower():
        steps.append(("get_weather", {"city": "Paris"}))
    if any(ch.isdigit() for ch in question):
        steps.append(("calculator", {"expression": "21 * 2"}))
    return steps


@weft.trace(kind="agent", color="purple", span_display_name=lambda span: f"turn: {span.inputs['question'][:30]}")
def agent_turn(question: str) -> str:
    observations = []
    for tool_name, args in plan(question):
        try:
            observations.append({"tool": tool_name, "result": TOOLS[tool_name](**args)})
        except Exception as e:  # recorded on the tool call, agent continues
            observations.append({"tool": tool_name, "error": str(e)})
    messages = [
        {"role": "system", "content": "Answer using the tool observations."},
        {"role": "user", "content": f"{question}\nObservations: {json.dumps(observations)}"},
    ]
    return llm(messages)


if __name__ == "__main__":
    with weft.session() as s:  # each top-level span in the block is a turn
        print(agent_turn("What's the weather in Paris?"))
        print(agent_turn("And what is 21 * 2?"))
    weft.flush()
    print("session:", s.session_id)
    root = next(iter(client.spans(filter={"names": ["agent_turn"]}, limit=1)))
    print("root usage (rolled up by the server):", root.summary.get("usage"))
