# agentshield-wiki

Persistent, LLM-maintained knowledge base for AI agent threat intelligence, paired with the
[agentshield-ai/agentshield](https://github.com/agentshield-ai/agentshield) detection engine.

This wiki tracks **TTPs** (tactics, techniques & procedures) specific to AI agents, maps them to
Sigma rule coverage in the engine, and records open coverage gaps. It is designed to be read and
appended to by LLM sessions — see [`AGENTS.md`](./AGENTS.md) for the schema and workflows.

## Layout

- `AGENTS.md` — schema, taxonomy, and workflows (the contract the LLM follows)
- `log.md` — append-only journal of ingest / query / lint sessions
- `raw/` — immutable source captures (papers, advisories, incidents, blog snapshots)
- `wiki/` — distilled, cross-referenced knowledge pages

Start at [`wiki/overview.md`](./wiki/overview.md) or the catalog at [`wiki/index.md`](./wiki/index.md).
