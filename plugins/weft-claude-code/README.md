# Weft for Claude Code

Traces [Claude Code](https://docs.claude.com/en/docs/claude-code) sessions
into [Weft](../../docs/weft/README.md) as sessions:

```
invoke_agent claude-code          one per prompt (a turn); session = the Claude Code session
├─ chat claude-sonnet-4-5         each assistant message from the transcript, with token usage
├─ execute_tool Bash              each tool use (PreToolUse → PostToolUse), with input and result
└─ invoke_agent code-reviewer     each Task/Agent sub-agent
   ├─ chat claude-haiku-4-5       the sub-agent's LLM calls (from its transcript)
   └─ execute_tool Read           the sub-agent's tools
```

Each Claude Code session appears under **Sessions** (its story: prompts,
model calls, tool calls, sub-agents and answers), its agents under **Agents**
(`claude-code` and each sub-agent type) and **Teams**, and its spans in
**Traces**. Usage includes cache reads and writes, so cost is shown per turn,
per LLM call and per session.

This is separate from the AgentShield security plugin in
[`plugins/claude`](../claude); the two can be installed side by side.

## Plugin or Claude Code's built-in telemetry

Claude Code can also send its sessions to Weft by itself, with no plugin:

```bash
export CLAUDE_CODE_ENABLE_TELEMETRY=1
export OTEL_LOGS_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf      # or http/json (Weft has no gRPC receiver)
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:6345/otel
export OTEL_EXPORTER_OTLP_HEADERS="project_id=my-team/claude-code,Authorization=Bearer%20<key>"
export OTEL_LOG_USER_PROMPTS=1          # optional: prompt text
export OTEL_LOG_ASSISTANT_RESPONSES=1   # optional: reply text
export OTEL_LOG_TOOL_DETAILS=1          # optional: tool arguments
```

Weft turns those log events into the same spans this plugin sends (a turn per
prompt, model calls with tokens and Claude Code's own cost, tool calls with
their tool-use ids, sub-agents under the Agent call), live, every few
seconds. What only the plugin records: reasoning, the messages each model
call received, and tool results (file contents, command output); the
built-in export sends their sizes only, and prompt and reply text only with
the variables above. Claude Code's metrics, if exported, are accepted and
discarded. Its beta trace export (`CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1`,
`OTEL_TRACES_EXPORTER=otlp`) is mapped too.

Running both is safe: Weft shows each session from one source only, this
plugin first, then Claude Code's traces, then its log events, so nothing is
counted twice. The plugin's spans are recognised by `weft.source =
weft-claude-code` and the Claude Code session id. See
[Claude Code without a plugin](../../docs/weft/guides/setup.md#claude-code-without-a-plugin)
and [API.md](../../docs/weft/API.md#claude-code-log-events).

## How it works

- Every hook (`SessionStart`, `UserPromptSubmit`, `PreToolUse`,
  `PostToolUse`, `PostToolUseFailure`, `Stop`, `SubagentStop`, `SessionEnd`)
  runs `hooks/weft-hook.mjs`, which appends one line to a local session log
  under `~/.weft-claude-code/sessions/` and exits. It prints nothing and
  always exits 0, so it can never block or change what Claude does. There is
  no network call in the hook.
- On `Stop`, `SessionEnd`, or a new prompt after an unfinished turn, the hook
  starts a detached worker (`weft-hook.mjs --flush <session>`). The worker
  reads the session log and the transcript, builds the spans and posts them to
  `/api/v1/spans/batch` with a short timeout (`WEFT_HOOK_TIMEOUT_MS`, 2 s).
- Span ids are derived from the session, prompt and tool-use ids. If the
  server is down, the worker logs the error and the next flush resends the
  same spans; nothing is duplicated.
- Failures are logged to `~/.weft-claude-code/logs/` and otherwise ignored.

Turns interrupted without a `Stop` (Esc, a new prompt) are closed when the
next prompt or `SessionEnd` arrives and marked as interrupted.

## Install

Requires Node.js 18+ and a running Weft server (`./bin/weft`, default
`http://localhost:6345`).

**Option A: settings.json hooks (any Claude Code version).**

```bash
node plugins/weft-claude-code/install.mjs --project my-team/claude-code \
  --server-url http://localhost:6345            # --api-key <key> when auth is on
```

This adds the hooks to `~/.claude/settings.json` (a `.bak` copy is kept;
running it again replaces its own entries) and saves the settings to
`~/.weft-claude-code/settings.json`. `--dry-run` prints the result instead,
`--settings <path>` targets a project's `.claude/settings.json`, and
`--uninstall` removes the hooks.

**Option B: as a Claude Code plugin.** The directory is a plugin
(`.claude-plugin/plugin.json`, `hooks/hooks.json`) and its own marketplace:

```bash
claude --plugin-dir plugins/weft-claude-code        # one session
# or, inside Claude Code:
/plugin marketplace add ./plugins/weft-claude-code
/plugin install weft-claude-code@weft
```

Set the configuration in the environment Claude Code starts in (or in
`~/.weft-claude-code/settings.json`).

## Configuration

| Variable | settings.json key | Default |
|---|---|---|
| `WEFT_PROJECT` | `project` | none; tracing is off without it (`entity/project`) |
| `WEFT_SERVER_URL` | `server_url` | `http://localhost:6345` |
| `WEFT_API_KEY` | `api_key` | none (sent as `Authorization: Bearer`) |
| `WEFT_AGENT_NAME` | `agent_name` | `claude-code` |
| `WEFT_CAPTURE_CONTENT` | `capture_content` | `true`; `false` sends only structure, names, timing and usage |
| `WEFT_HOOK_TIMEOUT_MS` | `timeout_ms` | `2000` (worker's network timeout) |
| `WEFT_MAX_FIELD_CHARS` | `max_field_chars` | `20000` (longer strings, e.g. file contents, are cut) |
| `WEFT_PLUGIN_DISABLED` | `disabled` | `false` |
| `WEFT_CLAUDE_CODE_STATE_DIR` | | `~/.weft-claude-code` |
| `WEFT_DEBUG` | `debug` | `false` (debug lines in the logs) |
| `WEFT_FLUSH_MODE` | | `background`; `inline` flushes inside the hook (tests), `off` never flushes |

**Privacy.** With content capture on, prompts, responses, tool arguments and
tool results (file contents, shell output) are sent to your Weft server. Turn
on the server's sensitive-data redaction for the project
(`/project/sensitive_data_settings/update`, optionally with
`--pii-analyzer-url`) or set `WEFT_CAPTURE_CONTENT=false`.

## Tests

```bash
node --test plugins/weft-claude-code/test/*.test.mjs
WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 node --test plugins/weft-claude-code/test/*.test.mjs   # + end to end
```

The unit tests replay a recorded session (`test/fixtures/session.json`: hook
payloads and transcript lines, including a sub-agent and a failed tool) and
check the calls sent. The end-to-end tests send the session to a live server,
once through the library and once through real hook processes, and check
`/agents/conversations/chat` and `/agents/query`.

`lib/weft.mjs` is vendored from
[`plugins/weft-harness-common`](../weft-harness-common/weft.mjs); after
editing that file run `node plugins/weft-harness-common/sync.mjs`.
