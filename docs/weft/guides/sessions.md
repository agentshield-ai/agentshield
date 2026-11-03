# Sessions

A **session** is one conversation with your agents: from the user's first
request to the final answer, including every hand-off between agents, their
reasoning and their tool calls. The Sessions page is the project's home.

## See what needs your attention

The cards at the top count the sessions in the chosen time range that need a
look. Each card's number is exactly the list you get when you click it.

| Card | What it counts |
|---|---|
| Failed sessions | Sessions that failed themselves: the top-level span of one of their traces (agent span or not) ended with an error. |
| Failed steps | Sessions that ended without failing, but where a tool call, model call, hand-off or delegated agent failed along the way (a sub-agent in a trace of its own counts when it is linked to its caller). The agent may have retried, worked around it, or given a weaker answer. |
| Blocked or needing approval | Sessions where AgentShield blocked a tool call, or asked a person for approval three or more times. |
| Low scores | Sessions with any score below 0.5. |
| Cost spikes | Sessions costing at least three times the typical (median) session. |

Hover the ⓘ next to a card for how it is counted. The server counts every
session in the range, however many there are. With filters set, a card
counts the list its link opens: the card's own condition replaces a filter on
the same thing (with **Security: Needs approval** set, the security card
counts every blocked or approval-heavy session).

## Find a session

The list shows the most recent sessions first, each with the user's first
request, the agents involved, the outcome, how long it took and what it cost.
Badges flag problems:

- **Blocked** or **Needs approval**: click the badge to jump straight to that
  tool call.
- **N failed steps** (or **A step failed**, when the step that failed is not
  an agent span, such as a database call under the server span around the
  agent): click it to jump to the failed step.
- **Go to the failure**: on a failed session, opens the step that failed.
- **Verdicts only — no agent spans recorded**: AgentShield checked tool calls
  in this session, but the agent itself sent no spans to Weft, so its steps,
  duration and cost are unknown (the duration shows as —), and so is its
  outcome: its status shows as **No agent data**, never Succeeded or Failed,
  even when a call was blocked. `status:unknown` lists these sessions.
  Connect the agent (see [Set up](setup.md)) to record them.

A session's status comes from the top-level spans of its traces, as on
Traces, so a status chip keeps the same runs on both pages (see
[Status](filters.md#status)).

Use the filter bar to narrow the list, for example `agent:researcher
status:failed`. Plain text finds sessions by what was said and done in them,
including the agents' reasoning, tool calls, error messages and hand-offs. See
[Filters and saved views](filters.md).

## Read a session

Opening a session shows a header with its figures and these tabs:

- **Timeline:** every step in order. Each agent's reasoning sits right before
  the tool call or hand-off it led to ("because: …"), and AgentShield's
  verdict sits on the tool call it concerns. Indentation shows which agent was
  in control. Click a step to see its details.
- **Graph:** which agent handed work to which, sized by how often. Switch to
  **Table** for the same data as a table.
- **Reasoning:** every reasoning step on its own, with a search box.
- **Security:** AgentShield's verdicts in this session.
- **Scores:** the session's scores, with the scorer and its reason.
- **Raw spans:** the underlying trace data, for developers.

The breadcrumb at the top always shows where you are: Session › Agent › Turn
› Step.

## Get from a problem to its cause

When a session has a problem, a box above the timeline lists it with a
**Show step** button. That scrolls to the step, opens it, and expands the
reasoning that led to it. Tick **Only problems** to hide everything else.

## Keyboard

| Key | Does |
|---|---|
| `j` / `k` | Next / previous step |
| `Enter` | Open or close the current step |
| `/` | Jump to the filter box |
| `?` | Show all shortcuts |

The current step is part of the page address, so you can share a link to it.
