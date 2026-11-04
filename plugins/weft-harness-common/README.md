# Weft harness plugins: shared core

`weft.mjs` is the dependency-free core shared by the Weft agent harness plugins
([Claude Code](../weft-claude-code), [Codex](../weft-codex),
[OpenClaw](../weft-openclaw), [Pi](../weft-pi)): configuration (`WEFT_SERVER_URL`,
`WEFT_API_KEY`, `WEFT_PROJECT`, ...), stable span ids, the span shapes the
agents index understands (`invoke_agent` turns, `chat` LLM spans,
`execute_tool` tools, conversation = the span's `session_id`), truncation and
the sender, which posts finished spans to Weft's `POST /api/v1/spans/batch`
([API.md](../../docs/weft/API.md#spans)) with a timeout.

**Correlation with AgentShield** ([DESIGN.md](../../docs/weft/DESIGN.md) §5.3).
Every plugin records the harness session id as `gen_ai.conversation.id` (and
the span's `session_id`) and the harness tool-use id as `gen_ai.tool.call.id` on tool
spans, exactly the values the AgentShield plugin of the same harness sends as
`session_id` and `tool_call_id`: Claude Code's `session_id` / `tool_use_id`
(sub-agent tools included: they share the session id), the Codex session id
/ `call_id`, and OpenClaw's `ctx.sessionId` (else the session key) /
`toolCallId`, a sub-agent's child session being a session of its own. Each
plugin's `test/correlation.test.mjs` runs the AgentShield plugin on the same
payloads and checks the ids match (Pi has no AgentShield plugin).

Each plugin vendors a copy as `lib/weft.mjs` so it installs on its own. After
editing this file run:

```bash
node plugins/weft-harness-common/sync.mjs
```

A test in `plugins/weft-claude-code/test` fails when a copy is stale.

Run every plugin's tests (CI runs this in `.github/workflows/weft.yml`):

```bash
node --test plugins/weft-claude-code/test/*.test.mjs plugins/weft-codex/test/*.test.mjs plugins/weft-openclaw/test/*.test.mjs plugins/weft-pi/test/*.test.mjs
WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 node --test plugins/weft-*/test/*.test.mjs   # + end to end against a server
```
