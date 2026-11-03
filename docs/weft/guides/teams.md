# Teams

A **team** is a set of agents that work together in sessions, for example an
orchestrator that hands research to one agent and writing to another. The
Teams pages show how work flows between them and where it breaks.

## Find the hand-off that fails most

1. Open **Teams** and click a team.
2. The **Most failing hand-off** box at the top names the route (for example
   orchestrator → researcher) and how often it failed.
3. The **Hand-offs** table lists every route, the most failing first, with its
   count, error rate, typical time taken and average score.

Then open one of the team's failed sessions under **Recent sessions** and
choose **Go to the failure** to see the failed hand-off and the reasoning
behind it.

## Read a team page

- **How work flows:** a graph of the agents, with an arrow for each hand-off,
  thicker when it happens more often and red when it fails.
- **Agents:** each agent's runs, errors, tool calls, hand-offs and cost
  within this team.
- **Recent sessions** of the team.

## Name a team

Put `weft.team.id` and `weft.team.name` on a span of the session (the SDKs
have `weft.team(...)` for this). Teams without a name are grouped
automatically from the agents that took part, and are marked "grouped
automatically".

## Find teams

The filter bar works as on Sessions. A filter about sessions, such as
`tool:Bash` or `status:failed`, keeps the teams with at least one matching
session. See [Filters and saved views](filters.md).
