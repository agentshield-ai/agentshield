# Filters and saved views

Sessions, Agents, Teams, Security and Traces share one filter bar, with the
same filters and the same typing shortcuts on every page.

## Filter

Type in the filter box (press `/` to jump to it). Plain text searches: on
Sessions it finds sessions by their title, agents and team, and by what was
said and done in them, including the agents' messages, their reasoning,
their tool calls, error messages and hand-offs; on Traces it finds traces
with any span containing the text. These shortcuts become filters when you
press Enter:

| Type | Keeps |
|---|---|
| `agent:researcher` | where this agent took part |
| `team:trip-planners` | this team |
| `status:failed` | sessions or traces that failed themselves (see [Status](#status)) |
| `status:failed_step` | sessions or traces that went on after a failed step |
| `status:ok` | sessions or traces where nothing failed |
| `status:running` | sessions or traces still running |
| `status:unknown` | sessions known only from AgentShield verdicts ("No agent data") |
| `has:block` | where AgentShield blocked a tool call |
| `has:approval` | where AgentShield asked for approval |
| `has:flagged` | blocked, or asked for approval 3 or more times |
| `score<0.5` | where a score is below 0.5 (`score<GoalCompletionJudge:0.5` for one scorer) |
| `model:gpt-4o` | where this model was used |
| `tool:Bash` | where this tool was called |
| `cost>0.1` | sessions costing at least $0.10 |
| `time:7d` | the last 7 days (also `1h`, `24h`, `30d`, `all`) |

Or choose **Add filter** and pick from the lists. Each filter shows as a chip;
click it to change it, or its × to remove it. On Traces, **Add filter** also
offers **Advanced: field condition**, a condition on any span field (for
example `inputs.question` contains `Oslo`).

Every filter is applied by the server, so lists, counts and the attention
cards cover every matching session. Where a page has to work from a list of
matching sessions (Agents, Teams and Traces use the most recent 1,000), it
says "Showing the first 1,000 of …" when more matched. Agents and Teams list
at most 500 agents or teams, the most recently active first, and say
"Showing the first 500 of …" when there are more; narrow the time range or
search by name to reach the rest.

## Status

**Status** means the same on Sessions and Traces, so a chip carried from
one page to the other keeps the same runs. Traces reads it off each trace's
top-level span; a session off the top-level spans of its traces.

| Chip | Sessions | Traces |
|---|---|---|
| **Failed** | the session itself failed: a top-level span ended with an error | the top-level span ended with an error |
| **Went on after a failed step** | it ended without failing, but a tool call, model call, hand-off or delegated agent in it failed | it ended without an error, but a span under it failed |
| **Succeeded** | it ended and nothing in it failed | the same |
| **Running** | a top-level span has not ended | the top-level span has not ended |
| **No agent data** | known only from AgentShield verdicts: no agent spans were recorded, so whether it succeeded is unknown | no traces (the session has none) |

A failed sub-agent whose caller carried on is a failed step, not a failed
session. A session with no agent data never matches Failed, Went on after a
failed step, Succeeded or Running, even when AgentShield blocked a call in
it: a blocked call is not a failed session. Use **Security: Has a block** to
find those. Listing spans on Traces (Roots only unticked), the chip applies
to each span: Failed keeps spans that ended with an error, Went on after a
failed step keeps spans with a failed span under them.

On Agents, Teams and Security the status chip keeps what belongs to a
session with that status (Agents reads Failed and Succeeded off the agent's
own calls).

## Filters on pages that do not list sessions

Some filters are about a session as a whole: its team, whether it failed, its
scores, its cost. On Agents, Teams, Security and Traces they keep what belongs
to a matching session: the agents that took part in one, the teams that had
one, the detections and traces inside one. A note under the chips says so
whenever this applies. No filter is ever ignored.

## Time

Every list starts at the last 24 hours. When nothing happened then, it widens
to the last 7 days, 30 days or all time, and tells you. Pick another range
from the clock menu, or **Custom range** for exact dates.

## Saved views

Filters live in the page address, so you can bookmark or share a filtered
list. To keep one for the team, open **Views**, choose **Save current filters
as a view**, and give it a name. Saved views belong to the project. Delete one
with its bin icon; you have a few seconds to undo.
