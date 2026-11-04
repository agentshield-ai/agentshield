"""A two-agent team: an orchestrator delegates research to a researcher agent,
then hands off to a writer; both record their reasoning.

    WEFT_SERVER_URL=http://localhost:6345 python examples/weft/python/agent_team.py

Then open the project in the UI: Sessions -> "trip-oslo-…" shows the timeline
(reasoning tied to the tool call it led to), the Graph tab shows
orchestrator -> researcher (delegate, and the return) and
orchestrator -> writer (hand-off); Teams -> "Trip planners" aggregates them.
The same data is in the native API:

    curl "$WEFT_SERVER_URL/api/v1/sessions/<session>/graph?project_id=<project>"
    curl "$WEFT_SERVER_URL/api/v1/sessions/<session>/reasoning?project_id=<project>"
    curl "$WEFT_SERVER_URL/api/v1/teams?project_id=<project>"

(the script prints the session id and project).
"""

import uuid

import weft
from weft.conversation import ToolCallPart

client = weft.init("agent-team-demo", settings={"print_call_link": False})
SESSION = f"trip-oslo-{uuid.uuid4().hex[:6]}"


@weft.trace(kind="tool")
def web_search(query: str) -> list:
    return ["Capital of Norway", "On the Oslofjord", "Home of the Nobel Peace Prize"]


with weft.start_conversation(agent_name="orchestrator", conversation_id=SESSION,
                             team=weft.Team("trip-planners", "Trip planners")) as conv:
    with conv.start_turn(user_message="Find three facts about Oslo and summarise them") as turn:
        # The orchestrator's model call: its thinking is a reasoning step,
        # linked by the server to the tool call it asked for (toolu_1).
        with turn.start_llm(model="claude-sonnet-4", provider_name="anthropic") as llm:
            llm.input_messages = [weft.Message.user("Find three facts about Oslo and summarise them")]
            llm.think("The user wants facts; the researcher agent should gather them.")
            llm.output_messages = [weft.Message.assistant("", tool_calls=[
                ToolCallPart(id="toolu_1", name="delegate_research", arguments={"task": "Find three facts about Oslo"})])]
            llm.usage = weft.Usage(input_tokens=200, output_tokens=120, reasoning_tokens=40)

        with turn.start_tool(name="delegate_research", arguments={"task": "Find three facts about Oslo"}, tool_call_id="toolu_1") as tool:
            # A delegation: the researcher's turn runs inside the hand-off, so it
            # nests in the same trace and the server records the delegate edge
            # (and the return when the researcher finishes).
            with weft.start_handoff("researcher", input="Find three facts about Oslo", kind="delegate") as h:
                with conv.start_turn(agent_name="researcher", user_message="Find three facts about Oslo") as rt:
                    weft.reasoning("Search the web first, then pick three facts.")
                    facts = web_search("Oslo facts")
                    rt.output_messages = [weft.Message.assistant("; ".join(facts))]
                h.output = facts
            tool.result = facts

        # A summary step, then an instant hand-off to the writer.
        weft.reasoning("Facts are in; the writer agent should summarise.", kind="summary")
        turn.handoff("writer", input="Summarise the three facts")
        with turn.start_subagent(name="writer", model="gpt-4o") as writer:
            with writer.start_llm(model="gpt-4o", provider_name="openai") as w:
                w.output("Oslo, Norway's capital on the Oslofjord, is home of the Nobel Peace Prize.")
        turn.output_messages = [weft.Message.assistant("Oslo, Norway's capital on the Oslofjord, is home of the Nobel Peace Prize.")]

weft.flush()
print(f"session {SESSION} in project {client.project_id}")
