# Getting started

This guide takes you from an empty project to your first agent session in
Weft. It takes about five minutes.

## Open a project

A **project** holds everything one application records: its sessions, agents,
traces, datasets and evaluations. Its name has two parts, a team and a name,
for example `my-team/support-bot`.

1. Open Weft in your browser (by default at `http://127.0.0.1:6345`).
2. Type a project name under **New project** and choose **Open project**.

The project is created the first time data arrives, so nothing is stored yet.

## See a sample session first

On the empty **Sessions** page, choose **Send a test session**. Weft writes a
realistic two-agent session into your project: a coding agent that hands work
to a reviewer, its reasoning, its tool calls, and a tool call that AgentShield
blocked. Use it to look around before you connect your own agents.

## Send your agents' data

The empty Sessions page shows ready-to-copy snippets with your server address
and project already filled in. Pick the one that matches your stack:

- **Python:** install the SDK in `sdk/python`, then call
  `weft.init("my-team/support-bot")` at start-up. OpenAI and Anthropic calls
  are recorded automatically.
- **TypeScript:** use the `weft-sdk` package: `await weft.init('my-team/support-bot')`.
- **OpenTelemetry:** point any OTLP/HTTP trace exporter at
  `<server>/otel/v1/traces` and send the project in a `project_id` header.
  Spans that follow the GenAI conventions, OpenInference or OpenLLMetry become
  sessions, agents, tool calls and reasoning steps.
- **Coding agents:** the plugins for Claude Code, Codex, OpenClaw and Pi send
  every coding session. Set `WEFT_SERVER_URL`, `WEFT_PROJECT` and, if your
  server needs one, `WEFT_API_KEY`.

Sessions appear within a few seconds. If nothing shows up, check the project
name: it must match exactly.

## Group agents into sessions and teams

Weft builds a **session** from everything that shares a conversation id
(`gen_ai.conversation.id`). The SDKs and plugins set it for you. To name a
**team** of agents that work together, set `weft.team.id` and
`weft.team.name` on a span, or use `weft.team(...)` in the SDKs. Without a
name, Weft groups the agents that worked together automatically.

## Connect AgentShield

If you run the AgentShield engine, it can send its verdicts on every tool call
to Weft, so blocked and flagged tool calls show up inside your sessions. The
[Security guide](security.md#connect-agentshield) shows the two lines of
configuration it needs.

## Next steps

- [Sessions](sessions.md): find what happened and what needs attention.
- [Agents](agents.md) and [Teams](teams.md): see how your agents work together.
- [Security](security.md): what AgentShield blocked, and why.
- [Evaluations and scores](evaluations.md): measure quality.
- [Filters and saved views](filters.md): narrow any list down.
