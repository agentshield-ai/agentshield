# Weft for Codex

Traces [OpenAI Codex CLI](https://developers.openai.com/codex/cli) sessions
into [Weft](../../docs/weft/README.md) as sessions:

```
invoke_agent codex           one per turn; session = the Codex session id
├─ chat gpt-5-codex          each model response, with input/cached/output/reasoning tokens
├─ execute_tool shell        each tool call with arguments, output and exit code
├─ execute_tool apply_patch
└─ execute_tool docs__search MCP tools carry mcp.server.name
```

Each Codex session appears under **Sessions**, its agent under **Agents**
(agent `codex`, version = the Codex CLI version) and its spans in **Traces**.

This is separate from the AgentShield security connector in
[`plugins/codex`](../codex); both can be installed.

## How it works

Codex writes each session to a rollout file
(`$CODEX_HOME/sessions/YYYY/MM/DD/rollout-…-<session id>.jsonl`). A **Stop
hook** (fired when a turn finishes) starts a detached collector and exits at
once, with no output and no network on Codex's path. The collector waits half
a second for Codex to finish writing, reads the rollout from a per-session
cursor, rebuilds the turns (timestamps from the rollout, so durations are
real) and posts them to `/api/v1/spans/batch` with a short timeout. Span ids
are derived from the session and rollout positions; a failed send is retried
by the next turn's run without duplicates. Errors go to
`~/.weft-codex/logs/collector.log`.

Where only managed hooks are allowed, use Codex's `notify` program instead:
the same script accepts the `agent-turn-complete` payload and finds the
rollout by session id.

Known gaps: a turn that is aborted or errors is sent
with the next turn (Codex fires no Stop for it), and sub-agents appear as
their spawning tool call.

## Install

Requires Node.js 18+ and a running Weft server.

```bash
node plugins/weft-codex/install.mjs --project my-team/codex --server-url http://localhost:6345
```

This merges a Stop hook into `$CODEX_HOME/hooks.json` (default
`~/.codex/hooks.json`; a `.bak` copy is kept, re-running replaces its own
entry) and saves the settings to `~/.weft-codex/settings.json`. Codex marks
new hooks untrusted: approve it on the next launch. `--dry-run` prints the
result, `--uninstall` removes it.

To configure Codex through `config.toml` instead, print the blocks:

```bash
node plugins/weft-codex/install.mjs --print-toml
```

```toml
[[hooks.Stop]]
[[hooks.Stop.hooks]]
type = "command"
command = 'node "/path/to/plugins/weft-codex/hooks/weft-codex-hook.mjs"'
timeout = 5

# or the notify fallback:
# notify = ["node", "/path/to/plugins/weft-codex/hooks/weft-codex-hook.mjs"]
```

Covered: `codex` (TUI) and `codex exec`. `codex mcp` and `app-server` fire no
hooks; use `notify`, or run the collector by hand:
`node hooks/weft-codex-hook.mjs --flush <rollout.jsonl>`.

## Configuration

| Variable | settings.json key | Default |
|---|---|---|
| `WEFT_PROJECT` | `project` | none; tracing is off without it |
| `WEFT_SERVER_URL` | `server_url` | `http://localhost:6345` |
| `WEFT_API_KEY` | `api_key` | none |
| `WEFT_AGENT_NAME` | `agent_name` | `codex` |
| `WEFT_CAPTURE_CONTENT` | `capture_content` | `true`; `false` sends only structure, names, timing and usage |
| `WEFT_HOOK_TIMEOUT_MS` | `timeout_ms` | `2000` |
| `WEFT_MAX_FIELD_CHARS` | `max_field_chars` | `20000` |
| `WEFT_PLUGIN_DISABLED` | `disabled` | `false` |
| `WEFT_CODEX_STATE_DIR` | | `~/.weft-codex` (cursors, logs, settings) |
| `CODEX_HOME` | | `~/.codex` (where rollouts are found) |
| `WEFT_ROLLOUT_DELAY_MS` | | `500` (collector wait before reading) |
| `WEFT_FLUSH_MODE` | | `background`; `inline` collects inside the hook (tests) |

**Privacy.** With content capture on, prompts, responses, reasoning
summaries, tool arguments and tool output are sent to your Weft server. Use
the server's sensitive-data redaction or `WEFT_CAPTURE_CONTENT=false`.

## Tests

```bash
node --test plugins/weft-codex/test/*.test.mjs
WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 node --test plugins/weft-codex/test/*.test.mjs   # + end to end
```

The tests use a recorded rollout (`test/fixtures/rollout.jsonl`: reasoning,
shell, apply_patch, an MCP tool, duplicated token counts, a failed tool) and
recorded Stop and notify payloads. The end-to-end test runs the real hook
process and checks `/agents/conversations/chat` and `/agents/query`.
