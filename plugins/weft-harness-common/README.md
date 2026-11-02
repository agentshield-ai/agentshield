# Weft harness plugins: shared core

`weft.mjs` is the dependency-free core shared by the Weft agent harness plugins
([Claude Code](../weft-claude-code), [Codex](../weft-codex),
[OpenClaw](../weft-openclaw)): configuration (`WEFT_SERVER_URL`,
`WEFT_API_KEY`, `WEFT_PROJECT`, ...), stable call ids, the call shapes the
agents index understands (`invoke_agent` turns, `chat` LLM calls,
`execute_tool` tools, conversation = thread id), truncation and the
`/call/upsert_batch` sender with a timeout.

Each plugin vendors a copy as `lib/weft.mjs` so it installs on its own. After
editing this file run:

```bash
node plugins/weft-harness-common/sync.mjs
```

A test in `plugins/weft-claude-code/test` fails when a copy is stale.

Run every plugin's tests (CI runs this in `.github/workflows/weft.yml`):

```bash
node --test plugins/weft-claude-code/test/*.test.mjs plugins/weft-codex/test/*.test.mjs plugins/weft-openclaw/test/*.test.mjs
WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 node --test plugins/weft-*/test/*.test.mjs   # + end to end against a server
```
