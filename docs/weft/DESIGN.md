# Weft design: observability for agents and agent teams

Weft records what AI agents and teams of agents do, why they did it, and how
well and how safely they did it. It stores sessions, agents, the hand-offs
between them, each model's reasoning, every tool call, AgentShield security
verdicts and evaluation scores, and it shows them together.

This document is the contract between the server, the SDKs and the UI. Field
and route names here are binding. Where an implementation has to differ, update
this file in the same change.

## 1. Principles

1. **OpenTelemetry first.** Ingest and naming follow the OpenTelemetry GenAI
   semantic conventions (`gen_ai.*`). Anything Weft adds lives under `weft.*`,
   and AgentShield's attributes live under `agentshield.*`. Other conventions
   (OpenInference, OpenLLMetry, Langfuse, MLflow, Vercel AI and so on) are
   mapped onto the GenAI model at ingest.
2. **Agents are the unit.** The primary objects are sessions, agents and teams,
   not individual function calls. The generic call tree is still there,
   underneath and in the raw Traces explorer.
3. **Reasoning is data.** Model reasoning (thinking blocks, reasoning
   summaries) is stored as steps that can be searched and scored, and each step
   is linked to the tool call or hand-off it led to.
4. **Security is part of the trace.** AgentShield verdicts are attached to the
   exact tool call and reasoning step they concern.
5. **Compatibility is an adapter.** Clients built for other tracing products
   work only through an optional adapter that is off by default (§8). Nothing
   outside the adapter depends on it.

## 2. Data model

All rows are scoped by `project_id` (`entity/project`).

| Object | Source | Key fields |
|---|---|---|
| **Session** | `gen_ai.conversation.id` (falls back to `thread_id`) | `session_id`, `name`, `started_at`, `last_activity`, `duration_ms` (null when unknown), `verdicts_only` (known only from AgentShield verdicts), `agents[]`, `team_id`, `turn_count`, `span_count`, `tool_call_count`, `handoff_count`, `reasoning_step_count`, `detection_count` (by action), `error_count`, `input_tokens`, `output_tokens`, `reasoning_tokens`, `cost_usd`, `status` (`running` / `ok` / `error`; `unknown` for `verdicts_only`), `failed_step` (an `ok` session in which a step failed); status comes from the top-level spans of the session's traces, as the Traces list reads them (API.md, *Run status*); a trace whose finished steps have arrived but whose top-level span has not (OTLP exports a span when it ends) is `running`, until 10 minutes after its latest span, then it has its steps' outcome (`failed_step` or `ok`) |
| **Agent** | `gen_ai.agent.name`, `gen_ai.agent.id`, `gen_ai.agent.version` on `invoke_agent` spans | name, versions, models, tools, sessions, handoffs in and out, detection rate (flagged tool calls / tool calls, API.md), scores |
| **Team** | `weft.team.id` / `weft.team.name` on any span of a session; otherwise derived as the set of agents that took part in a session (`team_id` = `auto:` + sorted agent names joined by `+`) | `team_id`, `name`, `agents[]`, `session_count`, aggregated graph |
| **Turn** | the existing turn detection (`is_turn`) | unchanged |
| **Hand-off** | an edge between two agents (§2.1) | `id`, `session_id`, `trace_id`, `from_span_id`, `to_span_id`, `from_agent`, `to_agent`, `kind` (`delegate` / `handoff` / `return`), `started_at`, `ended_at`, `status`, `input_preview`, `output_preview` |
| **Reasoning step** | extracted from model spans (§2.2) | `id`, `span_id`, `session_id`, `trace_id`, `agent_name`, `index`, `kind` (`thinking` / `summary` / `redacted`), `text`, `tokens`, `started_at`, `next_span_id` (the tool call or hand-off that followed) |
| **Tool call** | `execute_tool` spans | `gen_ai.tool.name`, `gen_ai.tool.call.id`, arguments, result, status |
| **Detection** | an AgentShield verdict (§5) | `id`, `session_id`, `span_id` (the matched tool span, nullable), `tool_call_id`, `tool_name`, `agent_name`, `action` (`allow` / `log` / `require_approval` / `block`), `mode` (`enforce` / `audit` / `shadow`), `severity`, `rules[]` (`id`, `title`, `level`, `tags`), `source` (`engine` / `replay`), `ruleset` (a label or hash), `overridden` (bool), `created_at`; also `event_id`, `trace_id`, `link` (`tool_call_id` / `nearest_time` / `replay` / `session` / `none`), `engine_span_id`, `event_source`, `overridden_at`, `replay_job_id`, `ingested_at` |
| **Score** | feedback rows with a scope (§6) | `scope` (`session` / `agent` / `handoff` / `turn` / `span`), the target ids, `scorer`, `value`, `reason` |

### 2.1 Hand-offs

A hand-off edge is recorded when any of these holds:

- an `invoke_agent` span's nearest `invoke_agent` ancestor in the same trace
  belongs to a different agent (`kind=delegate`);
- a span carries `weft.handoff.to_agent` (plus optional
  `weft.handoff.from_agent` and `weft.handoff.kind`), or is a framework
  hand-off span: OpenAI Agents SDK `handoff` spans (`from_agent` /
  `to_agent`), LangGraph node transitions between agent nodes, CrewAI
  delegation, Google ADK `transfer_to_agent`;
- an `invoke_agent` span has an OTel span link to a span of another agent
  (`kind=handoff`).

When the delegated agent finishes and control goes back, a `kind=return` edge
is recorded. The edges are indexed into an `agent_handoffs` table from the
existing call hook, so the order in which spans arrive does not matter.

### 2.2 Reasoning steps

Steps are extracted from model (`chat`) spans, both from what was sent and
from what came back:

- **Anthropic:** `thinking` content blocks (`kind=thinking`) and
  `redacted_thinking` blocks (`kind=redacted`, no text).
- **OpenAI Responses:** `reasoning` output items with their `summary` text
  (`kind=summary`). `reasoning_tokens` comes from
  `output_tokens_details.reasoning_tokens`.
- **Gemini:** parts with `thought: true`.
- **GenAI semconv messages** (`gen_ai.input.messages` /
  `gen_ai.output.messages`): parts of type `reasoning` or `thinking`.
- **Weft SDK:** `weft.reasoning(text, kind=...)` inside a span, recorded as a
  span event named `weft.reasoning`.

`next_span_id` points to the first tool call or hand-off that starts after
the step, within the same agent turn.

## 3. Native API (`/api/v1`)

These are JSON over HTTP with the same authentication as the rest of the
server. Every request carries `project_id` (in the query for GET, in the
body for POST). Lists take `limit`, `offset` and `sort_by`, and return
`{items, total_count}`.

| Route | Purpose |
|---|---|
| `GET /api/v1/sessions` | list sessions. Filters: `agent`, `team_id`, `status` (`running` / `ok` / `error` / `failed_step` / `unknown`), `has_detection` (`any` / `block` / `require_approval` / `flagged`), `min_score` (`scorer:value`), `score_below`, `cost_min`, `q` (text search), `from`, `to` |
| `GET /api/v1/sessions/attention` | the Sessions home's attention cards, counted on the server over every session matching the same filters, each with the filter that lists its sessions; a card counts exactly the list its link opens (the request's filters with the card's filter laid over them) |
| `GET /api/v1/sessions/{id}` | the session summary (§2 fields) |
| `GET /api/v1/sessions/{id}/timeline` | ordered items: `turn`, `message`, `reasoning`, `llm`, `tool_call`, `handoff`, `error`, each with `span_id`, `agent_name`, `started_at`, `ended_at` and a type-specific payload. Detections are fetched separately (`/detections?session_id=`) and merged by `span_id` / `tool_call_id` in the UI |
| `GET /api/v1/sessions/{id}/graph` | `{nodes: [{agent, calls, errors, tokens, cost_usd, detections}], edges: [{from, to, kind, count, errors, p50_ms}]}` |
| `GET /api/v1/sessions/{id}/reasoning` | reasoning steps in order |
| `GET /api/v1/agents`, `GET /api/v1/agents/{name}` | as today, plus `handoffs_in`, `handoffs_out`, `reasoning_tokens`, `detection_counts`, `scores` |
| `GET /api/v1/teams`, `GET /api/v1/teams/{id}` | team list; a team's agents, sessions, the aggregated graph and per-hand-off statistics (count, error rate, latency, mean score) |
| `GET /api/v1/handoffs` | query hand-offs: `session_id`, `from_agent`, `to_agent`, `team_id` |
| `GET /api/v1/detections`, `GET /api/v1/detections/stats` | query detections (`session_id`, `agent`, `action`, `rule_id`, `source`, time range, and any sessions filter as `session.<filter>`, resolved on the server); counts by rule, action, agent (with the detection rate; engine verdicts, or replay detections with `source=replay`) and day |
| `POST /api/v1/shield/ingest` | AgentShield pushes verdicts here (§5.2) |
| `POST /api/v1/shield/replay` | `{session_ids[] or filter, rules_dir?, label?}` starts a replay job, and returns `{job_id}` |
| `GET /api/v1/shield/replay/{job_id}` | job status, and a diff against the engine's original verdicts: `newly_blocked`, `no_longer_blocked`, `changed` |
| `GET /api/v1/shield/insights` | approval fatigue (`require_approval` per session and agent over time), override escalation, and cross-agent correlation within a team |
| `GET /api/v1/shield/replay`, `GET /api/v1/shield/status` | list replay jobs; whether replay is configured |
| `GET /api/v1/scores` | scope-aware scores (§6): `scope`, `session_id`, `agent`, `handoff_id`, `scorer` |
| `POST /api/v1/scores/run` | run scorers at a scope over chosen sessions |

The trace, object, table, feedback, evaluation and LLM APIs that exist today
remain Weft's lower-level API. The SDKs use them, and `docs/weft/API.md`
documents them as Weft's own API.

## 4. Ingest additions

- OTLP mapping fills the hand-off and reasoning fields from §2.1 and §2.2 for
  every convention Weft already maps.
- Spans whose instrumentation scope is `agentshield` (or that carry
  `agentshield.verdict.action`) become detections (§5.1). They are also stored
  as ordinary spans.
- Weft SDK attributes: `weft.team.id`, `weft.team.name`, `weft.handoff.*`,
  `weft.parent_call.id` / `weft.parent_call.trace_id` (a linked trace, as used
  by TypeSafe) and `weft.eval.*` (evaluation links). Other names for these
  are accepted only through the compatibility adapter (§8).

## 5. AgentShield integration

### 5.1 Engine to Weft over OTel

AgentShield's engine already exports an `evaluate` span over OTLP. The engine
adds these attributes to it:

- `gen_ai.conversation.id`: the session id.
- `gen_ai.tool.name`, plus `gen_ai.tool.call.id` when the plugin supplies
  `tool_call_id` (a new optional field on `EvaluationRequest`).
- `gen_ai.agent.name`: optional, from a new optional `agent_name` field.
- `agentshield.verdict.action`, `agentshield.verdict.mode`,
  `agentshield.verdict.severity` and `agentshield.rule_ids`.
- One `agentshield.rule.matched` event per rule, with `rule.id`, `rule.title`,
  `rule.level` and `rule.tags`.

Pointing the engine's `telemetry.endpoint` at Weft's `/otel/v1/traces`, with
`project_id` in the headers, is enough to make detections appear.

### 5.2 Engine to Weft by direct push

An optional `weft:` section in the engine config (`endpoint`, `project`,
`api_key_env`) makes the engine POST each non-`allow` verdict, and optionally
every verdict, to `/api/v1/shield/ingest`. The push happens in the background
through a bounded queue, and it never delays or changes a verdict. User
overrides that reach `/api/v1/override` are forwarded the same way, marking
the detection as `overridden`.

### 5.3 Correlation

The plugins pass the coding harness's session id as both the AgentShield
`session_id` and the Weft `gen_ai.conversation.id`, and the harness tool-use
id as `tool_call_id` and `gen_ai.tool.call.id`. A detection is attached to the
tool span with the same `(session_id, tool_call_id)`. Failing that, it is
attached to the tool span in the same session with the same tool name whose
start time is nearest within ±5 s. Failing that, it stays attached to the
session only. Detections that arrive before their span are linked when the
span arrives.

### 5.4 Replay and security evaluations

Weft imports AgentShield's rule engine (`internal/evaluate`) as a library and
can run it in-process with `--shield-rules-dir`, or call a running engine with
`--shield-url` / `--shield-token`. Replay re-evaluates every tool call of the
chosen sessions without acting on them, stores the results as detections
with `source=replay`, `mode=shadow` and a `ruleset` label, and reports what
would change. The `action` of a replay detection is the action enforce mode
would take, so it can be compared with the engine's verdicts (an `audit` or
`shadow` original is compared the same way).
Replay results depend only on the recording and the ruleset, never on the
engine's configuration, and are the same in-process and remotely:

- *Mode.* The engine evaluates a replay request as enforce mode would act,
  whatever its `evaluation_mode`, and in-process replay does the same, so
  the verdicts recorded in a replayed session's window (and the fields
  derived from them, such as `session.approval_count` for approval fatigue)
  are those enforce mode would record. (Evaluating replays in shadow mode
  instead would leave every replay session with no approvals, so a replay
  could never show a rule that reads them.) Nothing acts on a replay
  verdict, and enforce is the strictest mode, so honouring it for replay
  requests weakens nothing. Replays bypass the engine's verdict cache, whose
  key does not include the mode.
- *Window.* A replay session keeps the last 50 tool calls
  (`session.ReplayMaxEvents`, the engine's default `max_events`) and is
  kept for 6 hours idle (`session.ReplayTTL`, longer than any replay job),
  on every engine whatever its `session` settings, and in process.
- *Ids.* The replay session id is `weft-replay:<job>:<key>`, where `<key>`
  is the recorded session id, or `sha256:` + its hex SHA-256 when the full
  id would be over the engine's 256-byte limit, would hold control
  characters or padding, or the recorded id itself starts `sha256:`; the
  job result maps hashed keys back (`replay_sessions`), and detections and
  diffs always carry the recorded id. Tool call ids and agent names the
  engine would reject are left out of the request (no rule reads them).
  In-process replay applies the engine's input validation
  (`models.ValidateEvaluationRequest`) before evaluating, so both paths
  evaluate, and reject, the same calls.
- *Storage.* Detections are stored per session; a session whose detections
  cannot be stored counts them as errors without failing the job.
 `ToolSafetyScorer` (§6) turns this into evaluation
scores (`blocked`, `approvals_required`, `max_severity`) at session and agent
scope, so rule changes can be evaluated against recorded agent behaviour.
`--shield-url` is operator configuration and is not subject to the egress
guard. Remote replay marks its requests with `replay: true` (an optional
field on `EvaluationRequest`), a `weft-replay-` event id and a
`weft-replay:<job>:` session id. The engine still evaluates them, but tags
the `evaluate` span `agentshield.replay`, neither stores nor pushes the
verdict, and keeps the session out of live sessions' cross-session fields.
A replayed session's own cross-session fields cover only the other sessions
of its job (those sharing the `weft-replay:<job>:` prefix), in-process as
well as remotely, so a replay's verdicts depend only on the recording and the
ruleset, not on live traffic.
The namespace is reserved for replay: the engine honours the flag only on
session ids in the `weft-replay:` namespace, and rejects (400, logged) a
request with the flag on any other session, and a request without it whose
session id is in that namespace or whose event id starts `weft-replay-`
(overrides in the namespace too), so none of them can hide live traffic.
Over OTLP, Weft treats an evaluate span as a replay only when it carries
`agentshield.replay = true` or, for engines that predate that attribute, a
`weft-replay:` session id; it never turns one into an engine detection, a
session or agent activity, and the Traces list hides those engine traces
unless asked (`summary.weft.replay`). Only a replay span of the engine's
instrumentation scope (`agentshield`) marks a trace, and only a trace that
holds nothing but such spans and, as its root, the engine's HTTP span: any
OTLP writer can send a span claiming to be a replay with any trace id, and
it must not hide a trace with agent activity or a live verdict (the
claiming span stays visible in it). Engines never push replays, so the push
(`/api/v1/shield/ingest`) rejects replay-namespaced ids with 400. Replay never
creates sessions.

## 6. Scoped evaluation

Scores are feedback rows whose payload carries `scope` and the target ids
(`session_id`, `agent`, `handoff_id`, `turn_id`, `span_id`). The rules for
each scope:

- **Session scorers** see the whole timeline.
- **Agent scorers** see one agent's turns, reasoning and tools within a
  session.
- **Hand-off scorers** see the delegating context, the hand-off input, the
  delegate's output and what the caller did next.

The built-in scorers are the existing ones, plus:

- `HandoffQualityJudge`: was the delegation appropriate, and was the result
  used?
- `ReasoningConsistencyJudge`: did the actions follow from the stated
  reasoning?
- `ToolSafetyScorer`: the AgentShield replay, as a scorer.
- `GoalCompletionJudge`: did the session achieve the user's goal?

Monitors can run any of these on new sessions automatically: a scope scorer
in a monitor runs at its scope (session, agent or hand-off) once the session
is complete (no new spans for `session_idle_seconds`, default 60, and no root
span still running), honours `sampling_rate`, and scores each target once
(API.md, *Monitors*).

## 7. UI

The navigation:

- **Observe:** Sessions (the home page), Agents, Teams, Security, Traces (the
  raw span explorer), Dashboards, Monitors.
- **Evaluate:** Evaluations, Scores, Leaderboards, Review queues, Playground.
- **Assets:** Datasets, Models, Prompts, Scorers, Functions (ops), All
  objects.
- **Project:** Automations, Settings.

The main pages:

- **Session page.** A header with the summary figures, then these tabs:
  - **Timeline:** turns, reasoning, tool calls, hand-offs and detections
    interleaved, with each reasoning step visually tied to the action it led
    to;
  - **Graph:** the delegation graph, an SVG with agents as nodes and hand-offs
    as edges, sized by count and coloured by errors and detections;
  - **Reasoning;**
  - **Security;**
  - **Scores;**
  - **Raw spans.**
- **Team page.** The aggregated graph across sessions, a per-agent table, a
  per-hand-off table (count, error rate, p50 latency, mean score), and
  recent sessions.
- **Security page.** Detections over time by action and severity, top rules,
  a per-agent detection rate, approval fatigue and override escalation
  panels, replay jobs with their diffs, and a "rules changed" call to action.

All wording is Weft's own. The UI never names another product, except on the
compatibility settings panel.

### 7.1 Usability requirements

The owner finds the tool this started from hard to learn, so ease of use is a
requirement, not polish. Each rule below is checked in Phase 4 by a usability
review and by task-based browser tests.

1. **Start from questions, not data types.** The Sessions home answers "what
   happened recently, and what needs my attention?". It shows:
   - attention cards (failed sessions, blocked or approval-heavy sessions,
     low scores, cost spikes), each opening a filtered list;
   - a plain list of recent sessions, each with a one-line summary: the first
     user request, the agents involved, the outcome, duration and cost.
2. **Plain language.** Labels use words a new user knows: "Session", "Agent",
   "Hand-off", "Reasoning", "Tool call", "Blocked", "Needs approval", "Score".
   - Internal terms (op, ref, digest, object, call, span) appear only in the
     raw views and in developer panels, and each has a tooltip.
   - Every ID can be copied, and is never the only label shown.
3. **One click from symptom to cause.**
   - From any error, block or low score, one click opens the session timeline
     scrolled to the step responsible, with the reasoning that led to it
     expanded.
   - Breadcrumbs always show Session › Agent › Turn › Step.
4. **Progressive disclosure.** Each step shows a readable summary first
   (message text, tool name with its main argument, result preview). Raw JSON,
   attributes and token details sit behind "Details". Long content is
   collapsed, with a "show all" control.
5. **One filter model everywhere.**
   - A single filter bar uses the same chips and syntax on Sessions, Agents,
     Teams, Security and Traces: agent, team, status, time, has block, score
     below, model, tool, plus free text.
   - Filters live in the URL and can be saved as a named view.
6. **Good defaults.** The time range defaults to the last 24 hours, widening
   automatically when that range is empty. Sort is newest first. Columns are
   chosen per page, and nothing requires configuration before data shows.
7. **Guided empty states.**
   - Every empty page says what would appear there and gives the shortest way
     to produce it: a copyable snippet for Python, TypeScript, OTel and each
     coding-agent plugin, using the current server URL and project.
   - A "Send a test session" button writes a sample session into the current
     project (`POST /api/v1/demo/session`).
8. **Explain the numbers.** Every metric has a tooltip saying how it is
   computed (for example, how cost treats cached tokens). Scores show their
   scorer and its reason inline.
9. **Fast to scan.**
   - Status uses colour and an icon with a text label, never colour alone.
   - Agents get a consistent colour across pages.
   - Timelines show time gaps, durations and nesting at a glance.
10. **Keyboard and accessibility.**
    - `j`/`k` move between steps, `/` focuses the filter, and `?` lists the
      shortcuts.
    - Contrast meets WCAG 2.1 AA, and every control has an accessible name.
    - Layouts work at 390 px width.
11. **Undo over confirm.** Deleting views, scores or queue items can be
    undone for a few seconds, and only irreversible deletes ask for
    confirmation.
12. **Help in place.** Each page has a short "What is this page?" panel that
    can be dismissed, and a link to the relevant guide in `docs/weft/`. The
    guides are embedded in the server and shown in the UI at `/help/<guide>`,
    so they match the running version and work offline.

## 8. Compatibility adapter

`--weave-compat` (`WEFT_WEAVE_COMPAT=true`) is off by default. It lives in one
package (`internal/weft/compat`, with the old names in
`internal/weft/compat/legacy`), and it turns on everything that exists only
for clients of another tracing product:

- that product's login endpoints and its client's call routes;
- its ref scheme and field and feedback-type names, translated on the way in
  and, for its client (detected by user agent), on the way out;
- its attribute names, mapped onto `weft.*` on ingest;
- redirects for the UI links its client prints;
- its environment-variable names for the settings Weft shares.

With the adapter off, those routes return 404, other ref schemes are plain
strings, and Weft's own SDKs, UI and plugins still work fully. Databases
written with the old names are migrated once when the server opens them,
whether or not the adapter is on; saved queries (content-addressed objects)
get a new version with Weft's names rather than being edited in place, so no
code outside the adapter and that migration reads the old names. The adapter is documented in one page,
[COMPATIBILITY.md](COMPATIBILITY.md), and CI runs the official-client check
with the flag on and everything else with it off.

## 9. Provenance

- `NOTICE` credits any code ported from Apache-2.0 projects.
- Prompts, preset texts and documentation are Weft's own wording. A scan for
  text copied verbatim from other products' code or docs is kept up to date in
  `docs/weft/PROVENANCE.md`.
- Feature-parity comparisons with other products are not kept in this
  repository.
