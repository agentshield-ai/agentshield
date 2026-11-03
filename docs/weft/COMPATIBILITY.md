# Compatibility with the W&B Weave client

Weft has its own SDKs, UI and plugins, and its own trace API
([API.md](API.md)). For teams that already instrument code with the official
`weave` Python client from W&B, Weft has an optional **compatibility adapter**
that lets that client send traces, objects, evaluations and feedback to Weft
unchanged. It is **off by default**, it lives in one package
(`internal/weft/compat`), and nothing else in Weft depends on it
([DESIGN.md](DESIGN.md) §1 principle 5, §8). This is the only page that
describes it.

## Turning it on

```bash
./bin/weft --weave-compat
# or
WEFT_WEAVE_COMPAT=true ./bin/weft
```

The flag wins over the environment variable. The startup log line reports
`compat_adapter=true`, and `GET /server_info` then includes
`"weave_compat": true`.

## Pointing the weave client at Weft

```bash
export WANDB_BASE_URL=http://127.0.0.1:6345      # login and project setup (GraphQL)
export WF_TRACE_SERVER_URL=http://127.0.0.1:6345 # traces, objects, feedback
export WANDB_API_KEY=<a Weft API key, or any 40 characters when auth is off>
```

```python
import weave
weave.init("my-team/my-app")   # ops, datasets, models, evaluations and feedback work as usual
```

The calls appear in Weft's UI like any other: Traces, Evaluations, the
Agents view and Sessions (for spans with a conversation id).

## What the adapter adds

With the adapter on, the server additionally:

| Area | Behaviour |
|---|---|
| Login | `POST /graphql` answers the handful of W&B GraphQL operations `weave.init()` issues (server info, viewer and default entity, entity, project, `upsertModel`, which creates the project). Other operations get a GraphQL error. |
| Calls | `POST /v2/{entity}/{project}/calls/complete`, `.../call/start` and `.../call/end`, the client's write paths. |
| Projects | `POST /service/projects_info` maps external to internal project ids (the identity in Weft). |
| Auth | The `Wandb-Api-Key` header is read as Weft's `X-Weft-Api-Key`. |
| Refs | `weave:///` refs and `weave-file://` URIs in request bodies and query strings are read as `weft:///` and `weft-file://`. Responses to the weave client carry `weave:///` refs. |
| Feedback | The client's field and type names are translated on the way in and, for the client, on the way out (table below). |
| Fields | The client's user and run fields (`wb_user_id`, `wb_run_id`, `wb_run_step`, `wb_run_step_end`, `wb_username`, and the calls filter's `wb_user_ids` / `wb_run_ids`) and its query fields are translated on the way in; responses to the client name them its way (table below). |
| Attributes | Legacy attribute keys are renamed to Weft's on ingest, over JSON and OTLP (table below), so evaluation links, parent-call links, span kinds and turns work. An OTLP resource with `wandb.entity` / `wandb.project` (and no `weft.project`) is routed to that project. |
| Ops | Ops the client serialises (`_type: "CustomWeaveType"`, `weave_type.type: "Op"`) are stored as ops. |
| Summaries | The client's call summaries already include their descendants; the adapter marks its requests `X-Weft-Summary: cumulative` so usage and status counts are not counted twice. |
| Links | `GET /{entity}/{project}/weave/...` and `/{entity}/{project}/r/call/{id}` (links the client prints) redirect to the matching Weft page: `calls`, `objects`, `evaluations`, `leaderboards`, `threads`, `agents`, `datasets`, `models`, `prompts`, `scorers`, `ops`, `monitors`, `playground` and `traces` keep their path, `agents/conversations/<id>` goes to the conversation, anything else to Traces. |
| Server info | `GET /server_info` adds `min_required_weave_python_version` (and `weave_compat`). |
| Settings | `WF_SCORING_WORKER_REMOTE_SCORER_ALLOW_INSECURE_HTTP` is read as `--remote-scorer-allow-insecure-http` when neither the flag nor `WEFT_REMOTE_SCORER_ALLOW_INSECURE_HTTP` is set. |

The client is recognised by its user agent (`WeaveTrace/…` or `weave/…`), the
`X-Stainless-Lang` header of its generated server SDK, or
`X-Weft-Ref-Scheme: weave`. Only responses to a recognised client are
rewritten; requests are translated for every caller while the adapter is on.

### Names

| The client sends | Weft stores and serves |
|---|---|
| `weave_ref` (feedback field, and in feedback queries) | `ref` |
| feedback type `wandb.reaction.1` | `weft.reaction` |
| `wandb.note.1` | `weft.note` |
| `wandb.runnable.<scorer>` | `weft.scorer.<scorer>` |
| `wandb.annotation.<name>` | `weft.annotation.<name>` |
| `wandb.agent_monitor` | `weft.signal` |
| `wandb.agent_user_feedback` | `weft.user_feedback` |
| call query fields `summary.weave.*`, `feedback.wandb.<type>.<path>` | `summary.weft.*`, `feedback.<weft type>.<path>` |
| `wb_user_id`, `wb_username` (calls, feedback, objects; query and sort fields) | `user_id`, `username` |
| `wb_run_id`, `wb_run_step`, `wb_run_step_end` | `run_id`, `run_step`, `run_step_end` |
| calls filter `wb_user_ids`, `wb_run_ids` | `user_ids`, `run_ids` |
| attributes `weave.eval.*`, `weave.parent_call.*` | `weft.eval.*`, `weft.parent_call.*` |
| `weave.op_name`, `weave.display_name`, `wandb.display_name` | `weft.op_name`, `weft.display_name` |
| `wandb.thread_id`, `wandb.turn_id`, `wandb.is_turn` / `weave.is_turn`, `wandb.thread_name` | `weft.thread_id`, `weft.turn_id`, `weft.is_turn`, `weft.conversation.name` |
| `weave.kind` / `weave.span_kind`, `weave.color`, `weave.agent.name` | `weft.span_kind`, `weft.color`, `weft.agent.name` |
| nested `attributes.weave.{kind, eval, parent_call, ...}` | `attributes.weft.{span_kind, eval, parent_call, ...}`; the client's other keys (`client_version`, `os_name`, ...) stay under `attributes.weave` |

The mapping lives in `internal/weft/compat/legacy`.

### Decision: legacy ref input is behind the flag

Weft used to accept `weave:///` refs everywhere. Accepting them is harmless,
but only the weave client produces them, so the adapter now does the
translation and Weft's core only knows `weft:///`. Everything stored is in
Weft's scheme (earlier versions normalised refs on write, so existing data
needs no migration). With the adapter off, a `weave:///` string is an
ordinary string: it is not followed, resolved or validated as a ref.

## Data written before the rename

Databases from earlier Weft versions used the names in the left column of the
table above. When the server opens such a database it migrates it once,
whether or not the adapter is on:

- columns get Weft's names: the feedback table's `weave_ref` becomes `ref`,
  and `wb_user_id`, `wb_run_id`, `wb_run_step`, `wb_run_step_end` of calls,
  feedback, objects and registry versions become `user_id`, `run_id`,
  `run_step`, `run_step_end`;
- stored feedback types get their Weft names;
- legacy attribute keys of stored calls are renamed (eval links, parent-call
  links, span kinds, turn markers);
- saved queries get Weft's names (below).

### Decision: saved queries are migrated, not translated at run time

Saved views, dashboards, monitors, leaderboards and automations hold call
queries, field names and feedback types. Earlier versions read the old names
in them at run time (the query compiler mapped `summary.weave.*` and
`feedback.wandb.*` paths, and automations mapped `wandb.*` types and
patterns), which put legacy names in Weft's core. Now the migration does it
once instead: these objects are content-addressed, so they are not edited in
place. For each one whose latest version uses an old name (a legacy query
field, `wb_*` field or filter key, or a `wandb.*` feedback type or pattern),
the migration publishes a new version with Weft's names (same class and
author, the publisher's key order kept), and moves the aliases of the old
version to it, so `latest` and every alias resolve to the new version. The
old version is kept and can still be read by its digest. Older versions are
not rewritten; reading one returns what was saved.

After the migration nothing outside the adapter reads the old names: the
query compiler and the automation runner know only Weft's. A saved query
that names an old field directly by digest gets an unknown-field error (or,
for a `feedback.wandb.*` path, matches no feedback). An automation that
filters on a monitor by its full ref (`trigger.monitor_ref` with a digest)
and whose monitor was migrated needs that ref updated, as after any edit of
the monitor. With the adapter on, queries and saved views the weave client
sends with the old names are translated on the way in.

## Limits

- Only the official Python client is tested: `scripts/weft_weave_compat_check.py`
  drives `weave==0.54.0` against a server started with `--weave-compat` (ops,
  nested calls with usage roll-up, reactions and notes, a dataset published
  and read back through refs, an evaluation, `get_call`) and checks what Weft
  stored through Weft's own API. CI runs it in the *Server and SDKs* job; the
  rest of the job runs with the adapter off.
- The computed call summary block is `summary.weft`; responses to the client
  do not repeat it as `summary.weave`.
- Values the client serialises as custom types (`PIL.Image.Image`,
  `wave.Wave_read`, video, its `Content` type, rich `Markdown`) are stored as
  sent. The UI renders them as media (`web/weft-ui/src/compat/legacyMedia.ts`,
  called from `components/media/mediaUtils.ts` only for values tagged with
  that encoding); Weft's server-side features treat them as plain objects.
- That decoder is used whether or not the adapter is on, on purpose. Media
  values are user data: the one-time migration of old databases renames
  columns, feedback types, legacy attribute keys and saved queries, but never
  rewrites call inputs, outputs, object values or table rows (checked by
  `TestLegacyMigrationLeavesMediaValuesAlone` in
  `internal/weft/store/legacy_names_test.go`). So media a legacy client
  stored keeps its encoding after the adapter is turned off or the database
  is migrated, and still has to render. The decoder only reads; it adds no
  routes, names or behaviour to the server.
- Feedback and ref rewriting in responses is limited to JSON responses of
  `/feedback/*`, `/call/*`, `/calls/*` and `/agents/feedback/*` (plus
  `weave:///` refs in every JSON response). A user value that happens to
  equal a Weft feedback type name in those responses is shown to the client
  under the legacy name.
- User and run field names are renamed in responses of `/call/*`,
  `/calls/*`, `/feedback/*`, `/obj/*`, `/objs/*`, `/threads/*` and
  `/evaluations/*` to the client, as object keys outside user data (`inputs`,
  `output`, `attributes`, `summary`, `payload`, `val`, rows). In requests the
  old names are translated as keys and as string values (query and sort
  fields) outside user data, and inside `val` on `/obj/create` (a saved view's
  query).
- `/completions/*` and `/image/create` return `call_id`; for the client the
  adapter renames it to `weave_call_id`.
- The adapter answers only the GraphQL operations listed above; W&B features
  outside tracing (runs, artifacts, reports, the hosted UI) do not exist in
  Weft.
- Links to pages Weft does not have redirect to Traces.
- With `trace.backend=weave`, verl records rollouts through the official
  client; that works with the adapter on, and the calls can be filtered by
  their attributes (`attributes.step`, `attributes.experiment_name`).
