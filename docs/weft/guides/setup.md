# Getting started

This guide takes you from an empty project to your first agent session in
Weft. It takes about five minutes.

## Open a project

A **project** holds everything one application records: its sessions, agents,
traces, datasets and evaluations. Its name has two parts, a team and a name,
for example `my-team/support-bot`.

1. Open Weft in your browser (by default at `http://127.0.0.1:6345`).
2. Under **Open or start a project**, type a name such as
   `my-team/support-bot` and choose **Open project**.

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
- **Claude Code without a plugin:** Claude Code's own OpenTelemetry export
  can send its sessions straight to Weft. See
  [Claude Code without a plugin](#claude-code-without-a-plugin) below.
- **OpenCode and Copilot CLI:** their built-in OpenTelemetry exports can
  send sessions straight to Weft too. See
  [OpenCode (built-in telemetry)](#opencode-built-in-telemetry) and
  [Copilot CLI (built-in telemetry)](#copilot-cli-built-in-telemetry) below.
- **Codex and Gemini CLI without a plugin:** their built-in OpenTelemetry
  log exports can send sessions straight to Weft. See
  [Codex (built-in telemetry)](#codex-built-in-telemetry) and
  [Gemini CLI (built-in telemetry)](#gemini-cli-built-in-telemetry) below.

Sessions appear within a few seconds. If nothing shows up, check the project
name: it must match exactly.

## Claude Code without a plugin

Claude Code can report what it does over OpenTelemetry by itself. Point it at
Weft with environment variables and every session shows up, with no plugin
to install. Set these where Claude Code starts (your shell profile, or the
`env` block of `~/.claude/settings.json`):

```bash
export CLAUDE_CODE_ENABLE_TELEMETRY=1
export OTEL_LOGS_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf      # or http/json; Weft has no gRPC receiver
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:6345/otel
export OTEL_EXPORTER_OTLP_HEADERS="project_id=my-team/claude-code,Authorization=Bearer%20<your API key>"

# Optional content (off by default in Claude Code):
export OTEL_LOG_USER_PROMPTS=1          # your prompts
export OTEL_LOG_ASSISTANT_RESPONSES=1   # Claude's replies (defaults to OTEL_LOG_USER_PROMPTS)
export OTEL_LOG_TOOL_DETAILS=1          # tool arguments: commands, file paths, sub-agent prompts
```

- The endpoint is Weft's address followed by `/otel`: the exporter adds
  `/v1/logs` (and `/v1/metrics`, `/v1/traces`) itself. To send only the logs,
  set `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://127.0.0.1:6345/otel/v1/logs`
  instead.
- The project comes from the `project_id` header. Without the header, the
  resource attribute `weft.project` picks it
  (`OTEL_RESOURCE_ATTRIBUTES=weft.project=my-team/claude-code`), else
  `default/default`. An API key limited to some projects must send the
  header. Leave out `Authorization` when your server has no API keys.
- Metrics are not needed. If `OTEL_METRICS_EXPORTER=otlp` is set (for
  example for another backend), Weft accepts them and discards them, so
  Claude Code reports no export errors.
- Events arrive every few seconds (`OTEL_LOGS_EXPORT_INTERVAL`, 5 s by
  default), so a session shows up while it runs.

**What you get.** Each prompt becomes a turn of the `claude-code` agent,
with its model calls (model, tokens including cache reads and writes, time,
and the cost Claude Code reports), its tool calls (with their tool-use ids,
so AgentShield verdicts attach to them), sub-agents under the Agent call that
ran them, and tool calls you rejected. What each switch adds:

| Variable | Without it | With it |
|---|---|---|
| `OTEL_LOG_USER_PROMPTS=1` | the turn shows no prompt (a note in the raw span says it was not exported); the session is named by its id | the prompt is the turn's message and names the session |
| `OTEL_LOG_ASSISTANT_RESPONSES=1` | model calls show the tools they asked for, not their text | each reply's text, the turn's answer and each sub-agent's reply |
| `OTEL_LOG_TOOL_DETAILS=1` | tool name, outcome, duration and sizes | the arguments (Claude Code shortens long values), full error messages and custom sub-agents' real names |

**Plugin or built-in telemetry?** The Claude Code plugin
(`plugins/weft-claude-code` in the Weft repository) reads the session
transcript, so it records more: Claude's reasoning, the messages
each model call received, and tool results (file contents, command output).
The built-in export never sends reasoning or tool results (only their
sizes), but it needs nothing installed and shows a turn while it runs; the
plugin sends a turn when it ends. A turn from the built-in export is shown
as running until its last model call asks for no more tools (or a later
prompt starts, which marks it interrupted). If Claude Code stops in the
middle of a turn, the turn reads as running for ten minutes after its last
event and then shows what its steps did.

You can use both: Weft shows each session from one source only, the plugin
first, then Claude Code's traces, then its logs, so nothing is counted twice.

**Traces (beta).** Claude Code also has a beta trace export
(`CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1` and `OTEL_TRACES_EXPORTER=otlp`, plus
`OTEL_LOG_TOOL_CONTENT=1` for Read and Bash output). Weft maps its spans onto
the same turns, model calls, tool calls and sub-agents, and prefers them to
the logs for a session that sends both. Trace spans carry no cost, so their
cost comes from Weft's prices. The beta's span names come from Claude Code's
documentation; we have not seen it export spans yet.

## OpenCode (built-in telemetry)

OpenCode exports OpenTelemetry traces itself. Turn on the model and tool
spans in `opencode.json` (in the project, or `~/.config/opencode/`):

```json
{
  "$schema": "https://opencode.ai/config.json",
  "experimental": { "openTelemetry": true }
}
```

and point the exporter at Weft where OpenCode starts:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:6345/otel
export OTEL_EXPORTER_OTLP_HEADERS="project_id=my-team/opencode,Authorization=Bearer <your API key>"
```

- OpenCode adds `/v1/traces` (and `/v1/logs`) to the endpoint and sends
  OTLP/HTTP JSON. Weft has no gRPC receiver. Weft accepts the logs and
  ignores them.
- OpenCode passes the header value as written: use a plain space after
  `Bearer`, not `%20`. Leave out `Authorization` when your server has no
  API keys.
- Without `experimental.openTelemetry`, OpenCode sends only its internal
  spans, and Weft records nothing from them.

**What you get.** Each prompt becomes a turn of the `opencode` agent in a
session named by OpenCode's session id. The turn holds its model calls
(model and tokens, including cached input; OpenCode exports no cost, so
Weft prices known models), the prompt and reply of each call, and its tool
calls with arguments, results, failures and their tool-call ids, so
AgentShield verdicts attach to them. A sub-agent run by the `task` tool is
its own OpenCode session; Weft shows it as an `invoke_agent <type>` turn
under the task call, with a hand-off from `opencode`. The model call
OpenCode makes to title a new session is kept for its tokens but not shown
as a reply.

The AI SDK records prompts, replies, tool arguments and tool results, and
OpenCode has no setting to turn that off. Each model call carries
OpenCode's full system prompt and tool list.

OpenCode also traces its own internals: database queries, file reads,
plugin loading, about a thousand spans per prompt. Weft stores only the
turns and the model and tool spans, and drops the rest.

A one-shot `opencode run` can exit before its last spans are sent: in our
runs it lost everything after the first batch. The TUI, or `opencode serve`
with `opencode run --attach`, keeps running and sends everything.

## Copilot CLI (built-in telemetry)

> Based on GitHub's documentation of the Copilot CLI OpenTelemetry export
> (the CLI command reference, "OpenTelemetry monitoring"). It has not been
> tested against a real Copilot CLI run.

```bash
export COPILOT_OTEL_ENABLED=true          # implied when the endpoint is set
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:6345/otel
export OTEL_EXPORTER_OTLP_PROTOCOL=http/json        # the default; http/protobuf works too
export OTEL_EXPORTER_OTLP_HEADERS="project_id=my-team/copilot,Authorization=Bearer%20<your API key>"

# Optional content (off by default): prompts, replies, system prompt,
# tool schemas, tool arguments and results
export OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true
```

- Copilot CLI exports OTLP over HTTP only. It adds `/v1/traces` and
  `/v1/metrics` to the endpoint. Weft accepts the metrics and discards them.
- Weft recognises Copilot by its service name (`github-copilot` by default,
  also `copilot-cli` and `copilot-chat`) or by its instrumentation scope
  (`github.copilot`), so a custom `OTEL_SERVICE_NAME` still works.
- Some vendor guides use VS Code's names for the same switches
  (`COPILOT_OTEL_CAPTURE_CONTENT`, `COPILOT_OTEL_EXPORTER_TYPE=otlp-http`).
  The CLI reference lists the variables above.

**What you get.** Each user message is a turn of the `copilot` agent (a
named custom agent keeps its name) in the session given by
`gen_ai.conversation.id`. The turn holds its `chat` model calls (model and
tokens, including cache reads and writes) and its `execute_tool` calls, with
their tool-call ids, so AgentShield verdicts attach. A sub-agent (`explore`,
`task`) is an `invoke_agent` under the tool call that started it. A failed
call or turn shows its `error.type`. Without content capture, turns show no
prompt and tools show no arguments. The token totals on the turn span
repeat its model calls' totals, so Weft does not count them again.
`github.copilot.cost` and `nano_aiu` are billing multipliers, not money,
and stay in the raw span.

## Codex (built-in telemetry)

Codex CLI reports what it does as OpenTelemetry log events. Add an `[otel]`
section to `~/.codex/config.toml` (or `$CODEX_HOME/config.toml`):

```toml
[otel]
log_user_prompt = true   # your prompts; Codex sends "[REDACTED]" without it
exporter = { otlp-http = { endpoint = "http://127.0.0.1:6345/otel/v1/logs", protocol = "binary", headers = { "project_id" = "my-team/codex", "Authorization" = "Bearer <your API key>" } } }
```

- The endpoint is the full logs address: Codex does not add `/v1/logs`.
  `protocol` is `"binary"` (protobuf) or `"json"`. Weft has no gRPC
  receiver, so `otlp-grpc` does not work.
- The project comes from the `project_id` header, else `default/default`.
  Leave out `Authorization` when your server has no API keys.
- Leave `trace_exporter` unset. Codex's traces describe its internals (file
  reads, config loading, rollout writes: about a thousand spans a turn) and
  carry no session, so they add nothing to the session. A
  `metrics_exporter` pointed at `/otel/v1/metrics` is accepted and
  discarded.
- `codex exec` exits on a failed model request without sending its
  telemetry, so such a run never reaches Weft. The interactive `codex` sends
  it.

**What you get.** Each prompt becomes a turn of the `codex` agent, in the
session named by Codex's session id (the id the weft-codex plugin uses, and
`codex exec resume` keeps). The turn holds a model call for every request
(model, and input, cached, output and reasoning tokens; Codex sends no cost,
so Weft prices known models), including each failed attempt Codex retried;
its tool calls with their call ids (AgentShield verdicts from the Codex hook
attach to them), arguments and output, which Codex always sends; and
approval decisions: a denied call is a step that failed with "Rejected by
the user" (or by the approval policy). A sub-agent started by `spawn_agent`
runs in a Codex session of its own; Weft shows it as `codex-subagent` under
the `spawn_agent` call, in the parent's session.

Codex's log events do not include the model's replies or reasoning, so a
turn has no answer text, and model calls show the tools they asked for.

**Plugin or built-in telemetry?** The Codex plugin (`plugins/weft-codex`)
reads Codex's rollout file, so it records the replies, reasoning summaries
and the messages each model call received. The built-in export needs
nothing installed and shows a turn while it runs (until its last request
asks for no tool, or the next prompt starts, which marks it interrupted).
You can use both: Weft shows each Codex session from one source only, the
plugin first.

## Gemini CLI (built-in telemetry)

Gemini CLI reports what it does as OpenTelemetry log events. Set these where
Gemini CLI starts:

```bash
export GEMINI_TELEMETRY_ENABLED=true
export GEMINI_TELEMETRY_TARGET=local
export GEMINI_TELEMETRY_OTLP_ENDPOINT=http://127.0.0.1:6345/otel
export GEMINI_TELEMETRY_OTLP_PROTOCOL=http        # the default is grpc, which Weft does not receive
export GEMINI_TELEMETRY_OTLP_HEADERS="project_id=my-team/gemini,Authorization=Bearer <your API key>"
export GEMINI_TELEMETRY_LOG_PROMPTS=true          # the default; false leaves out prompts, replies and tool arguments
```

or the same in `~/.gemini/settings.json`:

```json
{
  "telemetry": {
    "enabled": true,
    "target": "local",
    "otlpEndpoint": "http://127.0.0.1:6345/otel",
    "otlpProtocol": "http",
    "otlpHeaders": { "project_id": "my-team/gemini" }
  }
}
```

- With `http`, Gemini CLI adds `/v1/logs`, `/v1/traces` and `/v1/metrics`
  to the endpoint and sends OTLP/HTTP JSON. Weft discards the metrics.
- Gemini CLI also sends trace spans (`llm_call`, `tool_call`,
  `agent_call`, one trace each). Weft builds the session from the log
  events and drops these spans for a session the log events describe.
- `GEMINI_TELEMETRY_OTLP_HEADERS` takes `key=value` pairs separated by
  commas, or a JSON object. Leave out `Authorization` when your server has
  no API keys.

**What you get.** Each prompt becomes a turn of the `gemini-cli` agent in
the session named by Gemini CLI's session id (a resumed session continues
it). The turn holds its model calls (model, and input, cached, output and
thinking tokens; Gemini CLI sends no cost, so Weft prices known models),
side requests such as model routing or loop checks marked with their role,
failed attempts Gemini CLI retried, the reply text, and its tool calls with
their arguments, outcome and Gemini CLI's call id. A tool call you rejected
is a step that failed with "Rejected by the user". A sub-agent
(`generalist`, `codebase_investigator`, ...) is shown under the
`invoke_agent` call that ran it, with the result it handed back.

Gemini CLI sends no tool output (only its size). Its hooks give AgentShield
no call id, and the AgentShield Gemini hook reports tools by AgentShield's
names (`exec` for `run_shell_command`), so its verdicts are listed with the
session but not yet attached to a step. There is no Weft plugin for Gemini
CLI.

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
