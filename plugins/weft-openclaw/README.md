# Weft for OpenClaw

An [OpenClaw](https://openclaw.dev/) gateway plugin that traces agent runs into
[Weft](../../docs/weft/README.md) as sessions:

```
invoke_agent main               one per agent run (a turn); session = the OpenClaw session id
├─ chat claude-sonnet-4-5       each model call, with prompt/history/response and usage
├─ execute_tool web_fetch       each tool call with params, result, error and duration
└─ invoke_agent researcher      sessions_spawn sub-agents (subagent_spawned → subagent_ended)
   └─ invoke_agent researcher   the child session's runs, with their own model and tool calls
                                (session = the child session id: a session of its own)
```

Each OpenClaw session appears under **Sessions**, its agents under **Agents**
(agent = the OpenClaw agent id, or `agentName`) and its spans in **Traces**.

This is separate from the AgentShield security plugin in
[`plugins/openclaw`](../openclaw); both can be loaded. Each tool span carries
the session id and tool-call id that plugin sends to AgentShield (`ctx.sessionId`,
else the session key, and `toolCallId`), so verdicts attach to the tool call.

## How it works

The plugin registers observation hooks: `session_start`, `llm_input`,
`llm_output`, `model_call_started`, `model_call_ended`, `before_tool_call`,
`after_tool_call`, `agent_end`, `subagent_spawned`, `subagent_ended`,
`session_end` and `gateway_stop`. Handlers only update in-memory state and
return immediately; they never return a decision, so they cannot block or
change a tool call (they are registered at the lowest priority). Finished runs
are queued and sent to `/api/v1/spans/batch` by a background timer
(`flushIntervalMs`) with a short timeout; on failure the batch stays queued
(bounded) and a warning is logged at most every 30 s. `gateway_stop` closes
open runs and sends what is left (bounded wait).

Model calls come from the diagnostic `model_call_*` events; the content
hooks (`llm_input`, `llm_output`, `agent_end`) add messages and token usage.
Those need `hooks.allowConversationAccess: true`; without it spans still have
structure, timing and tool calls, and runs are closed by the next run,
`session_end` or after `runIdleMs` of silence.

## Install

Requires OpenClaw 2026.4.25+ (Node 22).

```bash
openclaw plugins install ./plugins/weft-openclaw     # or copy/link it into the gateway's plugins
```

Then enable it in `~/.openclaw/openclaw.json` (JSON5) and restart the gateway:

```json5
{
  plugins: {
    allow: ["weft"],
    entries: {
      weft: {
        enabled: true,
        config: { project: "my-team/openclaw", serverUrl: "http://localhost:6345" },
        hooks: { allowConversationAccess: true },
      },
    },
  },
}
```

## Configuration

| `config` key | Environment fallback | Default |
|---|---|---|
| `project` (or `entity` + `project`) | `WEFT_PROJECT` | none; the plugin stays off without it |
| `serverUrl` | `WEFT_SERVER_URL` | `http://localhost:6345` |
| `apiKey` | `WEFT_API_KEY` | none |
| `agentName` | `WEFT_AGENT_NAME` | the OpenClaw agent id |
| `agentVersion` | | none |
| `captureContent` | `WEFT_CAPTURE_CONTENT` | `true`; `false` sends only structure, names, timing and usage |
| `flushIntervalMs` | | `1000` |
| `timeoutMs` | `WEFT_HOOK_TIMEOUT_MS` | `2000` |
| `maxFieldChars` | `WEFT_MAX_FIELD_CHARS` | `20000` |
| `runIdleMs` | | `600000` |
| `enabled` | `WEFT_PLUGIN_DISABLED` | `true` |

**Privacy.** With content capture on, prompts, responses, history, tool
arguments and results are sent to your Weft server. Use the server's
sensitive-data redaction or `captureContent: false`.

## Tests

```bash
node --test plugins/weft-openclaw/test/*.test.mjs
WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 node --test plugins/weft-openclaw/test/*.test.mjs   # + end to end
```

The tests replay recorded hook events (`test/fixtures/events.json`: two runs,
three model calls, a sub-agent with its own session, a blocked tool) through
the plugin with a fake API and check the calls sent; the end-to-end test
checks `/agents/conversations/chat` and `/agents/query` on a live server.
