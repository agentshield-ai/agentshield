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

- the feedback table's `weave_ref` column becomes `ref`;
- stored feedback types get their Weft names;
- legacy attribute keys of stored calls are renamed (eval links, parent-call
  links, span kinds, turn markers).

Saved views, dashboards, monitors and automations are content-addressed
objects, so their stored text is not rewritten (that would change their
digests). Queries and feedback-type patterns saved in them with the earlier
names keep working: the query compiler reads `summary.weave.*` and
`feedback.wandb.*` paths, and automation triggers read `wandb.*` types and
patterns. Re-saving them from the UI stores Weft's names.

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
  sent. The UI renders them as media (`web/weft-ui/src/components/media/clientTypes.ts`);
  Weft's server-side features treat them as plain objects.
- Feedback and ref rewriting in responses is limited to JSON responses of
  `/feedback/*`, `/call/*`, `/calls/*` and `/agents/feedback/*` (plus
  `weave:///` refs in every JSON response). A user value that happens to
  equal a Weft feedback type name in those responses is shown to the client
  under the legacy name.
- `/completions/*` and `/image/create` return `call_id`; for the client the
  adapter renames it to `weave_call_id`.
- The adapter answers only the GraphQL operations listed above; W&B features
  outside tracing (runs, artifacts, reports, the hosted UI) do not exist in
  Weft.
- Links to pages Weft does not have redirect to Traces.
- With `trace.backend=weave`, verl records rollouts through the official
  client; that works with the adapter on, and the calls can be filtered by
  their attributes (`attributes.step`, `attributes.experiment_name`).
