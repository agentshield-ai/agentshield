# Agents

The Agents page lists every agent that took part in a session, so you can see
who does the work, how they hand it on, and how safely.

## Read the list

For each agent:

- **Sessions** and **Runs**: how many sessions it took part in, and how many
  times it was started.
- **Hand-offs**: how often other agents handed work to it, and how often it
  handed work on.
- **Reasoning**: how many reasoning steps it recorded.
- **Errors**: how many of its steps failed, including tool calls it recovered
  from.
- **Flagged**: the share of its tool calls that AgentShield did not simply
  allow.
- **Scores**: the average of each scorer that rated it.
- **Cost** and **Last seen**.

Hover a column heading's ⓘ for exactly how the figure is computed.

## Look at one agent

Click an agent to see the tools it calls (and how often each fails), whom it
hands work to and who hands work to it, its models, and its recent sessions.
Click a session to open its timeline at that agent's steps.

## Find agents

Use the filter bar: for example `has:block` lists agents that had a tool call
blocked, and `tool:web_search` agents that called that tool. Filters about a
session as a whole, such as `cost>0.1`, keep the agents that took part in a
matching session. See [Filters and saved views](filters.md).

## Name your agents

Agents are named by the `gen_ai.agent.name` attribute on their spans, which
the SDKs and plugins set for you. Give each agent a stable name, so its
history adds up across sessions.
