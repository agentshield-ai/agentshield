# Filters and saved views

Sessions, Agents, Teams, Security and Traces share one filter bar, with the
same filters and the same typing shortcuts on every page.

## Filter

Type in the filter box (press `/` to jump to it). Plain text searches: on
Sessions it finds sessions by their title, agents and team, and by what was
said and done in them, including the agents' messages, their reasoning and
their tool calls. These shortcuts become filters when you press Enter:

| Type | Keeps |
|---|---|
| `agent:researcher` | where this agent took part |
| `team:trip-planners` | this team |
| `status:failed` | sessions that failed |
| `status:failed_step` | sessions that went on after a failed step |
| `has:block` | where AgentShield blocked a tool call |
| `has:approval` | where AgentShield asked for approval |
| `has:flagged` | blocked, or asked for approval 3 or more times |
| `score<0.5` | where a score is below 0.5 (`score<GoalCompletionJudge:0.5` for one scorer) |
| `model:gpt-4o` | where this model was used |
| `tool:Bash` | where this tool was called |
| `cost>0.1` | sessions costing at least $0.10 |
| `time:7d` | the last 7 days (also `1h`, `24h`, `30d`, `all`) |

Or choose **Add filter** and pick from the lists. Each filter shows as a chip;
click it to change it, or its × to remove it.

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
