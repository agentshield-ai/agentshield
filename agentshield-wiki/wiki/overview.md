# Overview

`agentshield-wiki` is a persistent knowledge base for **AI agent threat intelligence**. It lives
alongside the [`agentshield`](https://github.com/agentshield-ai/agentshield) detection engine and
serves three audiences:

1. **Rule authors** deciding what to build next — `coverage.md` shows the gaps.
2. **Incident responders** investigating suspicious agent behaviour — TTP pages describe mechanism,
   observables, and known bypasses.
3. **LLM sessions** that maintain this wiki over time — [`AGENTS.md`](../AGENTS.md) is their contract.

## What is an "AI agent TTP"?

A technique where the **attacker** is someone trying to manipulate, subvert, or weaponise an AI
agent (Claude Code, Cursor, Aider, Devin, an MCP client, a custom LangChain agent, …) and the
**victim** is either the agent's user, the agent's host environment, or a third party that the
agent touches via its tools.

Concretely, these attacks tend to look like:

- A user or a piece of fetched content tricks the agent into **running** attacker-chosen actions
  (→ `execution`).
- The agent is steered to **enumerate** the host for secrets, source, or credentials
  (→ `discovery`).
- Data is **exfiltrated** through the agent's own tool calls — `curl`, webhooks, DNS, markdown
  images, or just the agent's own response text (→ `exfiltration`).
- The attacker plants instructions in files the agent will read next session
  (`CLAUDE.md`, `AGENTS.md`, `.cursorrules`, MCP server configs) to establish **persistence**.
- The agent is pushed to perform irreversible **impact** — `rm -rf`, force-push, database drops.

These five categories (`execution`, `discovery`, `exfiltration`, `persistence`, `impact`) are the
entire taxonomy used here. See [`AGENTS.md`](../AGENTS.md#3-attack-taxonomy) for definitions.

## How the wiki is organised

```
wiki/
├── overview.md      ← you are here
├── index.md         ← catalog of every page
├── coverage.md      ← TTP → Sigma rule matrix
├── ttp/             ← one page per technique
├── rules/           ← one page per agentshield Sigma rule
├── vendors/         ← agent platforms, MCP servers, model providers
└── synthesis/       ← cross-cutting analysis
```

- **`raw/` is immutable.** Every claim in `wiki/` is backed by a source in `raw/` (or an external
  URL, captured as a stub). Never edit files under `raw/` after commit.
- **Pages are small.** One TTP per page, one rule per page. Link liberally.
- **Gaps are explicit.** `coverage: none` is a valid and useful state — it's how we pick what
  rule to write next.

## Relationship to `agentshield`

| Question | Source of truth |
|---|---|
| "What does this Sigma rule match?" | `agentshield/rules/rules/ai_agent/` (upstream) |
| "What threat does this rule defend against?" | `wiki/rules/<rule-id>.md` |
| "What TTPs exist and which are covered?" | `wiki/coverage.md` + `wiki/ttp/` |
| "What did the triage LLM conclude about event X?" | `agentshield` SQLite store |

The wiki is **descriptive**, not operational. If you want to change detection behaviour, change
it in `agentshield`; the wiki's job is to explain why.

## Where to start

- Browsing: [`index.md`](./index.md)
- Gaps to fill: [`coverage.md`](./coverage.md)
- Contributing: [`AGENTS.md`](../AGENTS.md) §6 (Workflows)
