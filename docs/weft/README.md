# Weft

Weft is an OpenTelemetry-native observability and evaluation platform for
agents, teams of agents and LLM applications: sessions, agents and the
hand-offs between them, each model's reasoning, nested traces of every LLM and
tool call, AgentShield security verdicts, versioned datasets, models, prompts
and scorers, evaluations with side-by-side comparison, leaderboards, human and
automated feedback, online monitors, cost tracking and an LLM playground.

It ships as one Go binary (`cmd/weft`) that holds the API, the OTLP receiver
and the web UI, backed by a single SQLite file. Python and TypeScript SDKs
live in `sdk/`.

```
 your app ──@weft.trace / wrapOpenAI─▶ /api/v1/spans/batch ┐
 any OTel / OpenInference / OpenLLMetry ──▶ /otel/v1/traces ┤──▶ weft (Go) ──▶ SQLite
 browser ──────────────────────────▶ web UI + JSON API ┘        │
                                                                └─▶ LLM providers (playground, judges, monitors)
```

## Run it

```bash
make weft-ui      # rebuild the React UI into internal/weft/webui/static (Node 20.19+; the built UI is committed),
                  # plus static/THIRD_PARTY_LICENSES.txt for the bundled npm packages (scripts/weft_ui_licenses.mjs)
make weft         # build bin/weft
./bin/weft        # http://127.0.0.1:6345, data in ~/.weft/weft.db
```

| Flag | Env | Default |
|---|---|---|
| `--addr` | `WEFT_ADDR` | `127.0.0.1:6345` |
| `--db` | `WEFT_DB` | `~/.weft/weft.db` |
| `--api-keys` | `WEFT_API_KEYS` | empty (auth disabled); comma-separated, each `key` or `user=key` |
| `--cors-origins` | `WEFT_CORS_ORIGINS` | empty |
| `--no-ui` | | serve the UI |
| `--log-level` | `WEFT_LOG_LEVEL` | `info` (`debug` logs every request) |
| `--pii-analyzer-url` | `WEFT_PII_ANALYZER_URL` | empty (regex PII redaction only); a Presidio Analyzer REST URL adds PERSON, LOCATION and ORGANIZATION redaction at ingest for projects with sensitive-data redaction on, see [API.md](API.md#project-settings) |
| `--pii-analyzer-timeout` | `WEFT_PII_ANALYZER_TIMEOUT` | `2s`; on timeout or error spans are stored with regex redaction only |
| `--egress-allow` | `WEFT_EGRESS_ALLOW` | empty; comma-separated internal destinations that remote scorers, webhooks, OAuth token URLs and LLM providers may reach: `host`, `host:port`, `*.domain`, IP or CIDR, e.g. `localhost:11434,10.20.0.0/16`. See [Outbound requests](#outbound-requests) |
| `--egress-allow-private` | `WEFT_EGRESS_ALLOW_PRIVATE` | `false`; `true` allows loopback, private, link-local and CGNAT destinations (cloud metadata addresses stay blocked) |
| `--remote-scorer-allow-insecure-http` | `WEFT_REMOTE_SCORER_ALLOW_INSECURE_HTTP` | `false`; `true` allows `http://` RemoteScorer URLs, which otherwise must be `https://` |
| `--ref-buckets-env-credentials` | `WEFT_REF_BUCKETS_ENV_CREDENTIALS` | `false`; `true` lets S3 reference buckets registered without `secret_name` use the server's `AWS_*` credentials, for AWS endpoints only. Otherwise every bucket needs a stored secret |
| `--files-backend` | `WEFT_FILES_BACKEND` | empty (file contents in SQLite); `s3://bucket/prefix` stores them in an S3-compatible bucket, see [API.md](API.md#files-and-media) for `--s3-endpoint`, `--s3-region` and credentials |
| | `WEFT_SECRET_KEY` | key for encrypting stored provider secrets; default is a random key in `<db>.key` |
| `--db-encryption-key-file` | `WEFT_DB_ENCRYPTION_KEY_FILE` | empty (plaintext database); a file holding a passphrase encrypts the whole database at rest, see [Database encryption at rest](#database-encryption-at-rest) |
| `--db-encryption-key` | `WEFT_DB_ENCRYPTION_KEY` | empty; the passphrase itself (prefer the key file or env var, flags show in process lists) |
| `--oidc-issuer` | `WEFT_OIDC_ISSUER` | empty (no SSO); an OpenID Connect issuer URL turns on "Sign in with SSO" and makes authentication required |
| `--oidc-client-id` | `WEFT_OIDC_CLIENT_ID` | required with `--oidc-issuer` |
| `--oidc-client-secret` | `WEFT_OIDC_CLIENT_SECRET` | empty (public PKCE client); prefer the env var, flags show in process lists |
| `--oidc-redirect-url` | `WEFT_OIDC_REDIRECT_URL` | required: `<public URL>/auth/oidc/callback`, registered with the provider |
| `--oidc-allowed-domains` | `WEFT_OIDC_ALLOWED_DOMAINS` | empty; comma-separated email domains allowed to sign in (verified emails only) |
| `--oidc-group-roles` | `WEFT_OIDC_GROUP_ROLES` | empty; `group=admin` (server admin) or `group=viewer\|member\|admin@team`, comma-separated |
| `--oidc-groups-claim` | `WEFT_OIDC_GROUPS_CLAIM` | `groups` |
| `--oidc-admin-emails` | `WEFT_OIDC_ADMIN_EMAILS` | empty; verified emails made server admins at sign-in |
| `--oidc-scopes` | `WEFT_OIDC_SCOPES` | `openid,email,profile` |
| `--session-ttl` | `WEFT_SESSION_TTL` | `12h` browser session lifetime |
| `--secure-cookies` | `WEFT_SECURE_COOKIES` | auto (Secure on TLS or an https redirect URL); `true` behind a TLS-terminating proxy |
| `--audit-retention-days` | `WEFT_AUDIT_RETENTION_DAYS` | `90`; `0` or `-1` keeps the audit log forever |
| `--shield-rules-dir` | `WEFT_SHIELD_RULES_DIR` | empty; an AgentShield Sigma rules directory (e.g. `rules/rules`) turns on in-process replay of recorded tool calls, see [AgentShield integration](#agentshield-integration) |
| `--shield-rules-roots` | `WEFT_SHIELD_RULES_ROOTS` | empty; comma-separated directories under which a replay request may name its own `rules_dir` |
| `--shield-url` | `WEFT_SHIELD_URL` | empty; replay against a running AgentShield engine instead (operator configuration: not subject to the egress guard, so loopback works) |
| `--shield-token` | `WEFT_SHIELD_TOKEN` | the engine's bearer token for `--shield-url` (prefer the env var) |
| `--shield-timeout` | `WEFT_SHIELD_TIMEOUT` | `10s` per remote evaluation |
| | `WEFT_SESSION_KEY` | session-cookie signing key (32+ chars); default is derived from the secret key |

The optional compatibility adapter has a flag of its own, off by default; see
[COMPATIBILITY.md](COMPATIBILITY.md). Only the adapter serves the route
shapes Weft had before its own `/api/v1` API (`/call/*`, `/obj/*`,
`/table/*`, …); the earlier `--legacy-routes` switch is gone, and a
`WEFT_LEGACY_ROUTES` setting is ignored with a warning.

The guides in `docs/weft` are built into the server: open **/help** in the UI
(each page's "Read the guide" link goes there).

Provider keys for the playground, LLM judges and monitors come from the
environment (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`, …) or
from **Settings → Secrets** in the UI. The `mock/echo` and `mock/json` models
work offline.

### Outbound requests

Weft refuses to send user-configured requests (remote scorers, `predict_url`
models, webhooks, OAuth token URLs, LLM provider base URLs, reference-bucket
tests, including their CORS preflight) to loopback, private, link-local, CGNAT, cloud-metadata and other
internal addresses, checking the address each connection dials so DNS
rebinding and redirects are covered. The caller sees `destination not
allowed`; the server logs the host, address and reason at warn level. Local
model servers and mocks need an explicit allow:

```bash
./bin/weft --egress-allow localhost:11434            # Ollama (the built-in ollama/ models)
./bin/weft --egress-allow 127.0.0.1:8000,10.0.0.0/8  # a local vLLM and an internal network
./bin/weft --egress-allow-private                    # everything internal except cloud metadata
```

`OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL` and `OLLAMA_BASE_URL` set in the
server's environment are allowed automatically (the same names stored as
secrets are not). The PII analyzer, the OIDC issuer and the S3/GCS files
backend are operator configuration and are not restricted. Details:
[API.md](API.md#outbound-request-restrictions).

For UI development: `./bin/weft` in one shell, `cd web/weft-ui && npm run dev`
in another (Vite proxies API paths to `WEFT_SERVER_URL`, default `:6345`).
To test SSO through Vite, register `http://localhost:5173/auth/oidc/callback`
as the redirect URL and start Weft with `--cors-origins http://localhost:5173`
(the proxy rewrites `Host`, so the session's same-origin check needs it).

## Send data

**Python SDK** (`sdk/python`):

```python
import weft

weft.init("my-team/my-app")

@weft.trace
def answer(question: str) -> str:
    return client.chat.completions.create(model="gpt-4o-mini",
        messages=[{"role": "user", "content": question}]).choices[0].message.content
```

OpenAI and Anthropic clients are patched automatically, so their requests appear
as child spans with token usage and cost. Versioned values are saved with
`weft.save(obj, name)` and read back with `weft.get(ref)`; spans are queried
with `client.spans(...)` and `client.span(id)`.

**Any OpenTelemetry exporter.** Point an OTLP/HTTP trace exporter at
`http://<host>:6345/otel/v1/traces` and send the project in a header:

```bash
export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:6345/otel/v1/traces
export OTEL_EXPORTER_OTLP_TRACES_HEADERS="project_id=my-team/my-app,authorization=Bearer <key>"
```

Spans that follow the GenAI semantic conventions, OpenInference or
OpenLLMetry become spans with chat-formatted inputs and outputs, token usage
and costs. The full mapping is in [API.md](API.md#opentelemetry-ingest).

Datasets, models, scorers and evaluations follow the same pattern:

```python
dataset = weft.Dataset(name="capitals", rows=[{"question": "France?", "expected": "Paris"}])

@weft.trace
def exact(expected: str, output: str) -> bool:
    return expected == output

evaluation = weft.Evaluation(dataset=dataset, scorers=[exact, weft.scorers.LevenshteinScorer()])
await evaluation.evaluate(my_model)        # recorded as an evaluation run, comparable in the UI
```

`weft.otel.configure(project=...)` installs an OpenTelemetry tracer provider
that exports to Weft, so OpenInference or OpenLLMetry instrumentation needs no
further setup. See [sdk/python/README.md](../../sdk/python/README.md).

**TypeScript SDK** (`sdk/typescript`, package `weft-sdk`):

```ts
import * as weft from 'weft-sdk'
await weft.init('my-team/my-app')
const answer = weft.trace(async function answer(question: string) { /* ... */ })
const openai = weft.wrapOpenAI(new OpenAI())
```

It also provides `Dataset`, `Evaluation`, `EvaluationLogger`, built-in scorers,
`save`/`get`, `spans`/`span`, `wrapAnthropic`, `attributes` and `withSession`. See
[sdk/typescript/README.md](../../sdk/typescript/README.md).

The TypeScript SDK also traces LangChain.js, LlamaIndex.TS, the Vercel AI SDK
(models, tools, `generateText`/`streamText`), MCP clients and servers, the
Claude Agent SDK, the OpenAI Agents SDK, Mistral, Cohere, Groq, Together,
Cerebras, AWS Bedrock and Vertex AI, with per-integration settings
(`autopatchSettings`, postprocess hooks).

Both SDKs trace OpenAI Realtime sessions with their audio saved as WAV files:
the `openai` package's realtime connections, and the OpenAI Agents SDK
(`RealtimeRunner` in Python, which uses `websockets`; `RealtimeSession` in
TypeScript via `weft.patchRealtimeSession()`, which finds
`@openai/agents-realtime` from the application's directory as well as the
SDK's and patches both its ESM and CommonJS builds). The Python SDK also patches raw
`websockets`, `websocket-client` and `aiohttp` Realtime clients. Lower-level
client methods are there too:
`register_custom_runtime` / `registerCustomRuntime`, `sessions_query` / `getSessions`,
`require_current_span`, the module-level tag and alias functions
(`weft.add_tags`, `weft.list_aliases`, `weft.get_tags_and_aliases`, …),
`weft.urls.agent_conversation_path` (TypeScript:
`agentConversationPath`, `client.conversationUrl`), and the
`WEFT_DEBUG_HTTP` / `WEFT_DISABLE_WAL_SENDER` switches.

**Agent teams.** Both SDKs record what the Sessions, Teams and hand-off
views are built from ([DESIGN.md](DESIGN.md) §2):

- **Sessions:** `gen_ai.conversation.id`, from a conversation's id or the
  session the integrations know (OpenAI Agents `group_id`, LangGraph
  `thread_id`, Claude Agent SDK session id, Google ADK `session_id`, Bedrock
  Agents `sessionId`).
- **Teams:** `weft.team("trip-planners", "Trip planners")` (TypeScript:
  `weft.withTeam({ id, name }, fn)`) or `team=` on a conversation stamps
  `weft.team.id` / `weft.team.name`.
- **Hand-offs:** `weft.handoff(to_agent, input=..., kind="handoff"|"delegate")`
  records `weft.handoff.*`; `weft.start_handoff(...)` (`startHandoff`) covers
  the receiving agent's run. Nested runs of another agent are delegations,
  and the integrations record the frameworks' own hand-offs (OpenAI Agents
  hand-off spans and agents-as-tools, LangGraph agent nodes, CrewAI
  delegation tools, Google ADK `transfer_to_agent`, AutoGen Swarm transfers,
  Claude Agent SDK sub-agents, Pydantic AI agents called from tools).
- **Reasoning:** thinking blocks, reasoning items and summaries, reasoning
  content and thought parts are kept from Anthropic, OpenAI (Responses and
  OpenAI-compatible chat), Gemini, Bedrock Converse and the Claude Agent SDK,
  and `weft.reasoning(text, kind=...)` adds a step. Redacted reasoning is
  stored as redacted, without its payload. `capture_reasoning` /
  `captureReasoning` (`WEFT_CAPTURE_REASONING=false`) turns capture off.

Examples: [`examples/weft/python/agent_team.py`](../../examples/weft/python/agent_team.py)
and [`examples/weft/typescript/agent_team.ts`](../../examples/weft/typescript/agent_team.ts).

**Coding agents.** Agent harness plugins trace Claude Code
([`plugins/weft-claude-code`](../../plugins/weft-claude-code)), Codex
([`plugins/weft-codex`](../../plugins/weft-codex)), OpenClaw
([`plugins/weft-openclaw`](../../plugins/weft-openclaw)) and Pi
([`plugins/weft-pi`](../../plugins/weft-pi)) sessions: a turn per
prompt, LLM calls with token usage, tool calls and sub-agents. They are configured with `WEFT_SERVER_URL`, `WEFT_API_KEY` and
`WEFT_PROJECT`, never block the agent, and send in the background. Each
records the harness's session id as `gen_ai.conversation.id` and the
harness's tool-use id as `gen_ai.tool.call.id`, the same values the
AgentShield plugins send as `session_id` and `tool_call_id`, so AgentShield
verdicts attach to the exact tool call ([DESIGN.md](DESIGN.md) §5.3).

**Claude Code without a plugin.** Claude Code's built-in OpenTelemetry export
works too: set `CLAUDE_CODE_ENABLE_TELEMETRY=1`, `OTEL_LOGS_EXPORTER=otlp`,
`OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf` (or `http/json`),
`OTEL_EXPORTER_OTLP_ENDPOINT=http://<host>:6345/otel` and
`OTEL_EXPORTER_OTLP_HEADERS="project_id=my-team/claude-code"`. Weft turns its
log events (`POST /otel/v1/logs`) into the same sessions the plugin records,
without reasoning or tool results; `OTEL_LOG_USER_PROMPTS`,
`OTEL_LOG_ASSISTANT_RESPONSES` and `OTEL_LOG_TOOL_DETAILS` add prompts,
replies and tool arguments. Its metrics (`/otel/v1/metrics`) are accepted and
discarded. The [setup guide](guides/setup.md#claude-code-without-a-plugin)
has the details, and [API.md](API.md#claude-code-log-events) the mapping.
Codex (`[otel]` `exporter = { otlp-http = … }` in `config.toml`) and Gemini
CLI (`GEMINI_TELEMETRY_OTLP_PROTOCOL=http`) export log events too, which
Weft maps onto sessions the same way ([setup](guides/setup.md#codex-built-in-telemetry),
[mapping](API.md#codex-log-events)).

Both SDKs also support guardrails (`span.apply_scorer` / `span.applyScorer`
score a finished span and attach the result as feedback), loading published
ops back as runnable functions (this executes the stored code), object classes
for annotation specs, monitors, leaderboards and saved views, and redaction of
credential-like keys (`api_key`, `password`, `token`, …) before data leaves
the process.

**Clients of other tracing products.** An optional compatibility adapter,
off by default, lets the client of one other product send traces to Weft.
It is described, with its limits, in [COMPATIBILITY.md](COMPATIBILITY.md).

**Demo data:** `python3 scripts/weft_seed_demo.py` fills `demo/support-bot` with
agent traces, sessions, OTLP spans, datasets, evaluations, a leaderboard, a
monitor and feedback, using offline mock models.

## AgentShield integration

[AgentShield](../../CLAUDE.md) evaluates every tool call an agent makes
against Sigma rules and answers `allow`, `log`, `require_approval` or
`block`. Weft stores those verdicts as **detections** attached to the exact
tool span they concern ([DESIGN.md §5](DESIGN.md#5-agentshield-integration)),
so a session's timeline shows what the agent did, why, and what the security
engine said about it.

**Correlation ids.** Both sides must name the same session and tool call:

| | AgentShield `EvaluationRequest` | Weft span attribute |
|---|---|---|
| session | `session_id` | `gen_ai.conversation.id` |
| tool call | `tool_call_id` (the harness tool-use id, e.g. Claude Code's `tool_use_id`) | `gen_ai.tool.call.id` |
| agent | `agent_name` (optional) | `gen_ai.agent.name` |

The AgentShield plugins send `tool_call_id` and `agent_name` when the harness
provides them (Claude Code: `tool_use_id`, and `agent_type` or
`AGENTSHIELD_AGENT_NAME`; OpenClaw: the tool call id and agent id, with the
OpenClaw session id as `session_id`; MCP gateway: a tool-use id in the
request `_meta`; Codex, Gemini CLI and Hermes: the hook's tool-call id and
`AGENTSHIELD_AGENT_NAME`). A detection is linked to the tool span with the
same session and tool call id; failing that, to the session's span of the same
tool name that started nearest the verdict, within ±5 s; failing that, to the
session only. Spans may arrive before or after their verdicts. Plugins that
canonicalise tool names (the Claude Code hook sends `exec` for `Bash`) rely on
the tool call id, because the time fallback compares tool names. A sub-agent
started by a tool (Claude Code's Task tool) is an agent span carrying the
tool-use id; verdicts on that call link to it by id, never by time.

**By OpenTelemetry.** Point the engine's trace exporter at Weft. The
`evaluate` span carries `gen_ai.conversation.id`, `gen_ai.tool.name`,
`gen_ai.tool.call.id`, `gen_ai.agent.name`, `agentshield.verdict.action`,
`.mode`, `.severity`, `agentshield.rule_ids` and one
`agentshield.rule.matched` event per rule; Weft stores it as an ordinary span
and as a detection.

```yaml
# AgentShield config.yaml
telemetry:
  enabled: true
  endpoint: http://127.0.0.1:6345/otel/v1/traces   # the URL path is honoured
  insecure: true                                     # plain HTTP
  headers: {project_id: my-team/agents}
  api_key_env: WEFT_API_KEY                          # sent as Authorization: Bearer
  disable_metrics: true                              # Weft takes traces only
  max_retry_sec: 300                                 # longest outage ridden out
  queue_size: 20000                                  # spans waiting; when full the newest is dropped and counted
  queue_max_mb: 256                                  # ... or when they reach this size
  export_concurrency: 4                              # export requests in flight
```

Every evaluation is exported, `allow` included, subject to `sample_rate`.
Spans wait in a queue bounded by `queue_size` (default 20000 spans, a
300 s outage at 15 verdicts a second twice over) and `queue_max_mb`
(default 256); ending a span never waits, and when the queue is full the
new span is dropped (drop-newest), counted and logged at warn level.
Batches of at most 256 spans and 8 MiB are exported by up to
`export_concurrency` requests at once (default 4), so a slow Weft does not
make the queue grow. A failed export (connection refused, a 5xx answer) is
retried with backoff for up to `max_retry_sec` (default 300), so an outage
of Weft shorter than that loses no span while the queue holds, and an
export Weft refuses, or answers with 500 because of one span, is split
until only the offending span is dropped; spans given up on, dropped, or
not exported by the shutdown deadline are counted and logged at warn level
(`ended = exported + failed + dropped + undelivered_at_shutdown + pending`
in `otel_export` on the engine's `GET /api/v1/stats`).

**By push.** The `weft:` section makes the engine POST verdicts to
`/api/v1/shield/ingest` from a bounded background queue; pushing never
delays or changes a verdict. When Weft is unreachable or failing, the batch
in flight is kept and retried with capped exponential backoff until Weft has
been failing for `max_retry_sec` (default 300), while new records keep
queueing; a full queue drops the newest record (drop-newest). The queue is
flushed on shutdown within a 10 s deadline. Records and requests are
size-bounded, and a record Weft refuses (400, 413, or a 500 for that record
alone) is isolated from its batch and dropped on its own, so the rest of the
batch is still delivered. To tell a record Weft cannot store from a Weft
that stores nothing (both answer 500), the pusher sends a probe: the last
detection Weft stored, again (Weft keeps one per event id, so nothing
changes), or else the next queued record alone. If Weft stores it, the
failing records are split and a lone one dropped after a few tries; if not,
it is an outage and the request is retried in place. So one poisoned record
costs about a second, not `max_retry_sec` of delivery for everything behind
it. Every record is counted, `accepted = sent +
dropped + failed + rejected + undelivered_at_shutdown + pending`, every one
not delivered is logged at warn level, and the counters of both paths are
on the engine's `GET /api/v1/stats` (`weft_push`, `otel_export`).
Overrides reported to the engine's `/api/v1/override` are forwarded too and
mark the detection `overridden` (the OTel path cannot carry overrides).

```yaml
weft:
  endpoint: http://127.0.0.1:6345   # https required for non-loopback hosts unless insecure: true
  project: my-team/agents
  api_key_env: WEFT_API_KEY         # a key with the member role on the project
  send: non_allow                   # or all
  queue_size: 10000                 # records waiting; when full the newest is dropped and counted
  max_retry_sec: 300                # longest Weft outage ridden out without losing records
```

Both paths can be on at once: a verdict is stored once, keyed by the engine's
event id. Both carry the same rules: the engine selects them once (at most
64, the most severe first, a repeated rule id keeping its most severe level)
and cleans them the same way for both.

**Querying.** `GET /api/v1/detections` (filters: `session_id`, `agent`,
`action`, `rule_id`, `source`, `ruleset`, `span_id`, `tool_call_id`, time
range), `GET /api/v1/detections/stats` (by action, severity, rule, agent with
a detection rate, and day) and `GET /api/v1/shield/insights` (approval
fatigue, override escalation, cross-agent correlation within a team). See
[API.md](API.md#agentshield-detections).

**Replay.** Start Weft with `--shield-rules-dir rules/rules` (in-process,
rules reloaded for every job) or `--shield-url http://127.0.0.1:8433
--shield-token …` (a running engine; its rate limit applies, about 100
evaluations a minute). `POST /api/v1/shield/replay` with `session_ids` (or a
filter) and optionally a `rules_dir` under `--shield-rules-roots` and a
`label` re-evaluates every recorded `execute_tool` span in shadow mode,
rebuilding each request the way `internal/replay` maps tool names and
arguments, stores the results as `source=replay` detections under the label,
and reports `newly_blocked`, `no_longer_blocked` and `changed` against the
engine's original verdicts. Run it before shipping a rule change to see which
recorded agent behaviour it would have blocked. `ToolSafetyScorer` scores a
session or one agent from these verdicts (`blocked`, `approvals_required`,
`max_severity`), optionally running a replay first (`replay: true`).

The replay engine URL is operator configuration, like the PII analyzer and
the OIDC issuer, so it is not subject to the [egress guard](#outbound-requests).

## Feature map

| Area | What Weft does |
|---|---|
| Traces | Spans with inputs, output, error, attributes, summary. Trace tree and timeline views, filters (function, status, free text, any field via the query builder), sorting, saved views, charts (volume, errors, latency percentiles, tokens, cost), export, bulk delete, compare spans. |
| Usage and cost | Token usage rolls up from every descendant to the trace root, including OTLP spans that arrive out of order. Built-in prices for common models, with cache-read and cache-write prices, so prompt-cached tokens are billed at the provider's cache rates ([Cached tokens](API.md#cached-tokens)); per-project price overrides (**Settings → Costs**). |
| Sessions (traces view) | Spans sharing a `session_id` group into sessions with turn latency and token totals. |
| Agents | The Agents view and `/agents/*` API: agents, versions, conversations, turns and LLM/tool spans derived from spans (GenAI, OpenInference, OpenLLMetry, Vercel AI SDK, MLflow, Pydantic AI, Google ADK, or traced SDK functions with a span kind). Per-agent stats over time, tool usage, message search and a chat view of each conversation. |
| Security | AgentShield verdicts as detections linked to the tool spans they concern (by OTel or push), stats by rule, agent and day, approval-fatigue, override-escalation and cross-agent insights, replay of recorded sessions against changed rules with a diff, and `ToolSafetyScorer`. |
| Objects | Content-addressed, versioned objects (datasets, models, prompts, scorers, functions, anything JSON). Aliases, version diffs, refs (`weft:///entity/project/object/name:digest`). |
| Datasets | Row tables with paging and editing; create from CSV/JSON or from selected spans. |
| Evaluations | SDK-run evaluations (`weft.Evaluation`, `EvaluationLogger`) and server-run evaluations of prompt-based models with built-in scorers and LLM judges. Comparison view with metric deltas and per-example diffs. |
| Leaderboards | Columns of evaluation × scorer × metric, computed from the latest run per model. |
| Feedback | Emoji reactions, notes, schema-driven human annotations, scorer results. Feedback is queryable from trace filters. Any server-runnable scorer can be run on a span from its Scores tab (`/api/v1/spans/score`). |
| Monitors | Online evaluation: active monitors score sampled spans of chosen span names as they finish and attach the scores as feedback. |
| Playground | Chat with OpenAI, Anthropic, Gemini, Groq, Mistral, DeepSeek, xAI, OpenRouter, Ollama or a custom OpenAI-compatible provider. Streaming, side-by-side models, `{variable}` placeholders with inputs, load from a call or saved prompt, save as prompt or as an evaluable model. |
| Access control | Users, teams (entities) and viewer/member/admin roles per team or project; API keys belong to users and carry their roles; OIDC single sign-on (PKCE) with auto-provisioning, domain and group-to-role mapping; an audit log of every write, admin action and refused request with query and CSV export. **Settings → Members / Audit log**, **Users & teams** page. |
| Administration | Managed API keys (hashed, per-project scope, read-only, admin), data retention TTL with a background purge, deterministic per-trace ingest sampling, server-side PII redaction at ingest (regex entities and custom patterns, plus PERSON/LOCATION/ORGANIZATION through an optional Presidio analyzer, configurable per project), project delete. Settings tabs for each (`/project/ttl_settings/*`, …). |

## Layout

| Path | Contents |
|---|---|
| `cmd/weft` | server entry point |
| `internal/weft/store` | SQLite schema, call roll-up, query language, objects, tables, feedback, costs |
| `internal/weft/otlp` | OTLP decoding and span → call mapping |
| `internal/weft/llm` | provider routing, Anthropic translation, streaming, mock models |
| `internal/weft/evals` | scorers, server-side evaluation, comparison, leaderboards, monitors |
| `internal/weft/shield` | AgentShield replay of recorded tool calls |
| `internal/weft/server` | HTTP API (Weft's own `/api/v1` trace and object API in `native*.go`), auth, UI serving, built-in guides (`/api/v1/help`) |
| `internal/weft/compat` | optional compatibility adapter, off by default ([COMPATIBILITY.md](COMPATIBILITY.md)), including every older route shape (`routes*.go`, `v2.go`, `extra.go`, `inference.go`); `compat/legacy` holds the old names and the older digest form used by it and by the one-time database migrations |
| `internal/weft/refs` | refs (`weft:///…`), canonical JSON and Weft's digests |
| `web/weft-ui` | React UI |
| `sdk/python`, `sdk/typescript` | client SDKs |
| `examples/weft` | runnable SDK examples |
| `scripts/weft_seed_demo.py` | demo data seeding |
| `docs/weft/API.md` | API contract (Weft's own API; the adapter's route shapes are in COMPATIBILITY.md) |
| `docs/weft/*.md`, `docs/weft/docs.go` | guides, embedded in the server and shown in the UI at `/help` |
| `docs/weft/PROVENANCE.md`, `scripts/weft_provenance_scan.py` | provenance scan and attribution record |

## Testing

```bash
make weft-test                                     # Go, UI type check, Python SDK unit tests
cd web/weft-ui && npm test                         # UI unit tests
WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 \
  python -m pytest sdk/python                      # SDK integration tests against a running server
cd sdk/typescript && WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 npx vitest run
WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 \
  node --test plugins/weft-claude-code/test/*.test.mjs plugins/weft-codex/test/*.test.mjs plugins/weft-openclaw/test/*.test.mjs plugins/weft-pi/test/*.test.mjs   # harness plugins
```

CI runs all of these in `.github/workflows/weft.yml` and fails if the
committed UI build is stale. It also runs the Go suite a second time with
every test database encrypted at rest (`make weft-test-encrypted`, i.e.
`scripts/weft_test_encrypted.sh`, which sets `WEFT_DB_ENCRYPTION_KEY`), so all
SQL is checked on both SQLite drivers.

## Database encryption at rest

By default the database is a plaintext SQLite file. With a database
encryption key Weft opens it through
[`github.com/ncruces/go-sqlite3`](https://github.com/ncruces/go-sqlite3)
(SQLite compiled to Wasm and translated to Go, no CGO) and its `adiantum`
VFS, which encrypts every 4 KiB block of the database, its WAL and temporary
files. The SQL is the same on both drivers.

```bash
openssl rand -base64 32 > ~/.weft/db.passphrase && chmod 600 ~/.weft/db.passphrase
./bin/weft --db-encryption-key-file ~/.weft/db.passphrase
```

The passphrase is stretched with Argon2id (64 MiB, 3 passes; about 0.2 s once
at startup). Startup fails with a clear error, before anything is written,
when the key is wrong, when an encrypted database is opened without a key, or
when a plaintext database is opened with one. **Losing the passphrase loses
the data**; keep it in a secret manager, separately from database backups.

Converting an existing database (stop the server first; the original is left
untouched and the copy is verified with `quick_check` and per-table row
counts before the command succeeds):

```bash
weft db encrypt --db ~/.weft/weft.db --out ~/.weft/weft-enc.db --db-encryption-key-file ~/.weft/db.passphrase
mv ~/.weft/weft-enc.db ~/.weft/weft.db     # weft-enc.db.key is a copy of weft.db.key
weft db decrypt --db ~/.weft/weft.db --out ~/.weft/plain.db --db-encryption-key-file ~/.weft/db.passphrase
weft db rekey   --db ~/.weft/weft.db --out ~/.weft/weft-new.db --db-encryption-key-file old.passphrase --new-db-encryption-key-file new.passphrase
```

After `encrypt`, delete the plaintext original, its `-wal`/`-shm` files and
any backups of it (on SSDs and copy-on-write filesystems deleted blocks may
survive; an encrypted volume is the only full answer to that). The key is
the same Argon2id derivation as the VFS's own `textkey` URI parameter, so
any go-sqlite3 based tool can open the file with
`file:weft.db?vfs=adiantum&textkey=<passphrase>`.

Costs, measured on 10,000 spans (4 vCPUs, same workload on both drivers):

| | plaintext (modernc) | encrypted |
|---|---|---|
| ingest, one transaction per start/end | 9.2 s | 16.1 s (+74%) |
| ingest, batches of 100 spans | 5.4 s | 7.2 s (+33%) |
| 50 span-list pages (100 roots, sorted) | 0.37 s | 0.77 s |
| 1,000 single-span reads | 0.15 s | 0.18 s |
| 10 JSON-filter scans of all spans | 0.62 s | 0.72 s |
| 10 searches / 10 project stats | 0.47 s / 0.31 s | 0.76 s / 0.88 s |
| binary size (stripped) | 22.3 MB | 29.8 MB (the second SQLite is linked into every build) |

Peak memory is higher too (about 150 MB against 30 MB in that benchmark: the
one-off Argon2id run plus a Wasm heap per connection).

## Security notes

- Without `--api-keys`, managed keys or `--oidc-issuer` the server is open to
  anyone who can reach it. It binds to loopback by default and logs a
  warning on other addresses.
- **Access model.** Static `--api-keys` keys and managed admin keys are
  server admins. Users (**Users & teams**) hold roles: `viewer` (read),
  `member` (also write spans, objects, feedback, run evaluations) and
  `admin` (also manage members, keys, project settings and delete the
  project), granted per team (an entity: every `team/*` project) or per
  project; the stronger applies. Server admins (a user flag) are admin
  everywhere and alone manage users, teams and provider secrets. Every
  authenticated request is checked: reads need viewer on each project the
  request names, writes need member, admin endpoints need admin; requests
  that name no project are refused for non-admins.
- **API keys.** A key whose `user_id` is a registered user is bound to it
  and inherits that user's roles, optionally narrowed to projects or
  read-only. Disabling the user disables the key at once. Users create and
  revoke their own keys, never broader than the credential they use. Keys
  for ids that are not users keep the earlier flag-based access.
- **Sessions and SSO.** OIDC uses the authorization-code flow with PKCE,
  state and nonce; ID tokens are verified against the issuer's JWKS
  (RS256/ES256), audience and expiry. Sessions use an HttpOnly,
  SameSite=Lax cookie (Secure on TLS) holding a random id and its HMAC; only
  the id's SHA-256 is stored. Cookie-authenticated non-GET requests must
  send the session's CSRF token (readable `weft_csrf` cookie) in
  `X-Weft-CSRF`, and a foreign `Origin` is refused; API-key requests are
  exempt. Sign-out, disabling a user and **Sign out everywhere** revoke
  sessions server-side. Without `--oidc-allowed-domains` or
  `--oidc-group-roles` any account of the provider can sign in, but it has
  no access until granted a role.
- **Audit log.** Writes, admin actions, sign-ins and refused requests are
  recorded (actor, auth method, key id, method, path, project, object and
  span ids, status, outcome, client IP); bodies and secret values are not.
  Entries survive project deletion and are purged after
  `--audit-retention-days`. Unauthenticated writes are recorded too, so a
  flood of them grows the table until retention removes it; rate-limit at a
  proxy if the server is exposed.
- **Outbound requests.** Remote scorers, webhooks, OAuth token URLs, remote
  models and provider base URLs are user input, so Weft refuses internal
  destinations (loopback, private, link-local, CGNAT, cloud metadata,
  including IPv4-mapped and NAT64 forms, after DNS resolution and on every
  redirect) unless the operator allows them with `--egress-allow` or
  `--egress-allow-private`. Errors say only `destination not allowed`.
  Without this a project member could make the server call internal services
  and read the reply back as feedback. `--egress-allow-private` reopens that
  for every member, so prefer a narrow `--egress-allow` list on shared
  servers. See [Outbound requests](#outbound-requests).
- Secret names in scorers, OAuth settings and custom providers resolve only
  from the secret store, never from the server's environment. The environment
  supplies only the key of a built-in provider at its default or
  operator-set base URL (`OPENAI_API_KEY` for `openai/…`, and so on), and that
  key is not sent when a project stores its own base URL for the provider.
- The guard also refuses the Weft host's own interface addresses (a service
  bound to `0.0.0.0` is reachable on them) and documentation ranges.
- Remote scorer requests carry span inputs and outputs and often credentials,
  so their URLs must be `https://` unless `--remote-scorer-allow-insecure-http`
  is set.
- Any key that can write to a project can use the stored provider secrets
  through the playground and LLM judges. A `Provider` object sends the
  secret named in `api_key_name` to its `base_url`, so only give write
  access to people you would trust with those secrets.
- Provider secrets are encrypted at rest with AES-256-GCM. The key comes
  from `WEFT_SECRET_KEY` or a random key file created next to the database
  (`<db>.key`, mode 0600). Keep the key file off backups that hold the
  database, or set `WEFT_SECRET_KEY` from a secret manager. Plaintext secrets
  from older versions are re-encrypted at startup.
- Uploaded files are served with `Content-Security-Policy: sandbox` and
  `nosniff` so they cannot run script in the UI's origin.
- **The database is not encrypted at rest unless you set a database
  encryption key** (`--db-encryption-key-file`, see
  [Database encryption at rest](#database-encryption-at-rest)); by default
  only provider secrets inside it are encrypted. With the key, the database,
  WAL and temporary files are encrypted with Adiantum (XChaCha12, AES-256,
  NH and Poly1305): a copy of the files shows no SQLite header and no
  plaintext. What it does not do: it is length-preserving disk encryption,
  so someone holding two snapshots (say, two backups) can tell which 4 KiB
  blocks changed; pages are not authenticated, so tampering garbles data
  instead of being reported (sign backups if forgery matters); the `-shm`
  file holds only the WAL index, unencrypted; and the key sits in the
  server's memory while it runs. An encrypted volume or filesystem
  (LUKS/dm-crypt, FileVault, BitLocker, an encrypted cloud disk with KMS)
  remains a good complement, and the only option for plaintext databases.
  New databases are created mode 0600 (`-wal` and `-shm` follow), and the
  server warns at startup when the database, its WAL/SHM files or
  `<db>.key` are world-readable, or when the database key file is readable
  by others.
