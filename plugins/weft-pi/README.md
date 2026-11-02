# Weft for Pi

A [Pi](https://github.com/badlogic/pi-mono/tree/main/packages/coding-agent)
coding-agent extension that traces Pi sessions into
[Weft](../../docs/weft/README.md) as agent conversations, like W&B Weave's Pi
harness integration (`createOtelExtension`):

```
invoke_agent pi            one per prompt (agent_start .. agent_end): a turn; conversation = the Pi session id
├─ chat claude-sonnet-4-5  each LLM response, with the context sent, the response, tool calls and usage
├─ execute_tool bash       each tool execution, with arguments, result, error and duration
└─ execute_tool edit
```

Sessions appear under **Agents** (agent `pi`), in **Threads** and in
**Traces**. Usage includes cache reads and writes; Pi's own cost estimate is
kept as `attributes.pi.cost_usd`.

## How it works

Pi loads extensions as TypeScript or JavaScript modules whose default export
receives the `ExtensionAPI` (`pi.on(event, handler)`). This one subscribes to
`session_start`, `model_select`, `before_agent_start`, `agent_start`,
`turn_start`, `context`, `message_end`, `tool_execution_start`,
`tool_execution_end`, `agent_end` and `session_shutdown`. Handlers only update
in-memory state and return nothing, so the extension never blocks a tool call
(`tool_call`/`tool_result` are not used), never rewrites a message and never
changes the system prompt.

Finished turns are sent to `/call/upsert_batch` at `agent_end` and by a
background timer (`flushIntervalMs`), with a short timeout. Call ids are
derived from the session and turn, so a resend after a failure upserts the
same calls. `session_shutdown` closes an unfinished turn (marked as such) and
waits briefly for the last send. Warnings go to `~/.weft-pi/logs/weft-pi.log`
(Pi's TUI owns the terminal). Type `/weft` in Pi to see the conversation's
Weft link and what was sent.

This is based on Pi's extension documentation (`docs/extensions.md` and
`docs/session-format.md` in `@mariozechner/pi-coding-agent` 0.73.1: event
names and payloads, `AgentMessage` shapes, `ctx.sessionManager`) and on
Weave's Pi harness page (span layout: `invoke_agent` per prompt, `chat` per LLM
call, `execute_tool` per tool). It was checked against a real Pi agent session
(the SDK's `createAgentSession` with Pi's faux LLM provider; see
`test/real-pi.test.mjs`), which showed that Pi emits `context` before
`turn_start` from the second turn on.

## Install

Requires Node.js 18+, Pi, and a running Weft server (`./bin/weft`, default
`http://localhost:6345`).

**As a Pi package** (all projects):

```bash
pi install ./plugins/weft-pi            # or -l for this project only (.pi/settings.json)
export WEFT_PROJECT=my-team/pi WEFT_SERVER_URL=http://localhost:6345
pi
```

Or copy/link the folder to `~/.pi/agent/extensions/weft-pi/` (Pi loads
`index.js`), or try it once with `pi -e ./plugins/weft-pi/index.js`.

**With the Pi SDK** (as Weave's `createOtelExtension`):

```ts
import { createAgentSession, DefaultResourceLoader, SessionManager } from '@mariozechner/pi-coding-agent';
import { createWeftExtension } from 'weft-pi';   // or a path to plugins/weft-pi/index.js

const loader = new DefaultResourceLoader({ cwd, agentDir, extensionFactories: [createWeftExtension({ project: 'my-team/pi' })] });
await loader.reload();
const { session } = await createAgentSession({ cwd, agentDir, resourceLoader: loader, sessionManager: SessionManager.inMemory() });
await session.bindExtensions({});
await session.prompt('Fix the failing test');
```

## Configuration

`createWeftExtension(options)` options, else the environment, else
`~/.weft-pi/settings.json` (`server_url`, `api_key`, `project`, ...):

| Option | Environment | Default |
|---|---|---|
| `project` (`entity/project`) | `WEFT_PROJECT` | none; the extension stays off without it |
| `serverUrl` | `WEFT_SERVER_URL` | `http://localhost:6345` |
| `apiKey` | `WEFT_API_KEY` | none |
| `agentName` | `WEFT_AGENT_NAME` | `pi` |
| `captureContent` | `WEFT_CAPTURE_CONTENT` | `true`; `false` sends only structure, names, timing and usage |
| `timeoutMs` | `WEFT_HOOK_TIMEOUT_MS` | `2000` |
| `maxFieldChars` | `WEFT_MAX_FIELD_CHARS` | `20000` |
| `flushIntervalMs` | `WEFT_FLUSH_INTERVAL_MS` | `1000` |
| `maxContextMessages` | | `40` (context messages kept per LLM call) |
| `disabled` | `WEFT_PLUGIN_DISABLED` | `false` |
| `stateDir` | `WEFT_PI_STATE_DIR` | `~/.weft-pi` |

**Privacy.** With content capture on, prompts, the system prompt, responses,
file contents, shell commands and output are sent to your Weft server. Images
are replaced by a placeholder. Use the server's sensitive-data redaction or
`captureContent: false`.

**Sub-agents.** Pi has no built-in sub-agents; the `subagent` example
extension runs separate `pi` processes, which (with the extension installed
globally) are traced as their own conversations, and the `subagent` tool call
shows in the parent turn.

## Tests

```bash
node --test plugins/weft-pi/test/*.test.mjs
WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 node --test plugins/weft-pi/test/*.test.mjs   # + end to end
# + a real Pi session (needs npm install @mariozechner/pi-coding-agent somewhere):
WEFT_TEST_SERVER_URL=... WEFT_TEST_PI_MODULES=/path/to/node_modules node --test plugins/weft-pi/test/*.test.mjs
```

The unit tests replay a recorded session (two prompts: a fix with `read`,
`bash` and `edit` over three LLM calls, then a failed LLM call) through the
extension with a fake API; the end-to-end test checks
`/agents/conversations/chat` and `/agents/query` on a live server.
