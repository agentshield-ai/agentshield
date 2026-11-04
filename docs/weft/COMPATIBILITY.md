# Compatibility with the W&B Weave client

Weft has its own SDKs, UI and plugins, and its own trace API
([API.md](API.md), under `/api/v1`). For teams that already instrument code with the official
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
| Route shapes | The client's trace, object, table, feedback, file, cost, model-call and evaluation routes (`/call/*`, `/calls/*`, `/obj/*`, `/table/*`, …), with digests in the client's form and its ref kinds; see [Route shapes](#route-shapes). |
| Projects | `POST /service/projects_info` maps external to internal project ids (the identity in Weft). |
| Auth | The `Wandb-Api-Key` header is read as Weft's `X-Weft-Api-Key`. |
| Refs | `weave:///` refs and `weave-file://` URIs in request bodies and query strings are read as `weft:///` and `weft-file://`. Responses to the weave client carry `weave:///` refs, with the kinds `op` and `call` for Weft's `function` and `span`. |
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
| attributes `weave.eval.*` (`weave.eval.predict_and_score_call_id`), `weave.parent_call.*` | `weft.eval.*` (`weft.eval.prediction_span_id`), `weft.parent_span.*` |
| `weave.op_name`, `weave.display_name`, `wandb.display_name` | `weft.span.name`, `weft.display_name` |
| `wandb.thread_id`, `wandb.turn_id`, `wandb.is_turn` / `weave.is_turn`, `wandb.thread_name` | `weft.session_id`, `weft.turn_id`, `weft.is_turn`, `weft.conversation.name` |
| `weave.kind` / `weave.span_kind`, `weave.color`, `weave.agent.name` | `weft.span_kind`, `weft.color`, `weft.agent.name` |
| nested `attributes.weave.{kind, eval, parent_call, op_name, ...}` | `attributes.weft.{span_kind, eval, parent_span, span.name, ...}` (in `eval`, `predict_and_score_call_id` becomes `prediction_span_id`); the client's other keys (`client_version`, `os_name`, ...) stay under `attributes.weave` |
| Agents API span fields `parent_call_id`, `parent_call_trace_id` (the client's names, given back in responses to it) | `linked_parent_span_id`, `linked_parent_trace_id` |

The mapping lives in `internal/weft/compat/legacy`. It maps onto Weft's
current attribute keys. Weft's own earlier key names (`weft.op_name`,
`weft.thread_id`, `weft.parent_call.*`, …) are not part of the adapter:
Weft reads them itself, with or without it (API.md, *Span attributes*).

### Decision: legacy ref input is behind the flag

Weft used to accept `weave:///` refs everywhere. Accepting them is harmless,
but only the weave client produces them, so the adapter now does the
translation and Weft's core only knows `weft:///`. Everything stored is in
Weft's scheme (earlier versions normalised refs on write, so existing data
needs no migration). With the adapter off, a `weave:///` string is an
ordinary string: it is not followed, resolved or validated as a ref.

## Route shapes

Before it had its own API ([DESIGN.md](DESIGN.md) §10), Weft served its
trace and object API in the route shapes of the weave client: `/call/*`,
`/calls/*`, `/obj/*`, `/objs/*`, `/table/*`, `/refs/*`, `/file/*`,
`/files/*`, `/feedback/*`, `/cost/*`, `/threads/*`, `/trace/*`,
`/completions/*`, `/evaluate_model`, `/evaluation_status`,
`/evaluations/*`, `/leaderboard/compute`, `/link_to_registry`,
`/registry/query`, `/dataset_sources/*`, `/embeddings/create`,
`/image/create`, `/project/stats`, `/tags`, `/aliases`,
`/v2/{entity}/{project}/…`, `/v1/entities/{entity}/projects/{project}…`,
`/inference/*`, `/geolocate`, `GET /version` and `GET /server_info`. These
routes now live in the adapter (`internal/weft/compat`, `routes*.go`,
`v2.go`, `extra.go`, `inference.go`) and are served only with the adapter on
(`--weave-compat`).

With the adapter off, every one of them returns 404 (the UI's catch-all
answers unknown `GET` paths with its own not-found page); `TestOffByDefault`
in `internal/weft/compat/compat_test.go` checks every route the adapter adds.

### Decision: the `--legacy-routes` switch is removed

While Weft's own SDKs, UI and plugins moved to `/api/v1`, a temporary server
switch, `--legacy-routes` (`WEFT_LEGACY_ROUTES`, on by default), served these
route shapes without the rest of the adapter. They have all moved, so the
switch was to be turned off by default. Nothing else needed the route shapes
without the adapter (no Weft SDK release used them, and the official weave
client needs the whole adapter anyway), so the switch was removed instead of
being kept as a second way to serve the adapter's routes: `--legacy-routes`
is an unknown flag, and a `WEFT_LEGACY_ROUTES` setting is ignored with a
warning at startup. To serve the older route shapes, turn the adapter on.

On these routes:

- **Digests.** New objects, tables, rows and files get digests in the
  client's form (`compat/legacy/digest.go`): the URL-safe base64 SHA-256 of
  Python's `json.dumps(v, sort_keys=True)` with `-`→`X`, `_`→`Y` and no
  padding (files: of the bytes; tables: hex SHA-256 of the concatenated row
  digests). The weave client computes digests itself and uses them in refs
  before the server answers, so the server must agree with it. Weft's own
  API writes Weft's digests (lowercase hex SHA-256 of canonical JSON, API.md
  *Conventions*); both forms are stored side by side and resolve the same
  way. Saving the same value through both APIs gives two versions.
- **Ref kinds.** Responses name function refs `op` and span refs `call`
  (`weft:///e/p/op/f:<d>`, `weft:///e/p/call/<id>`), as those routes always
  did. Weft reads both spellings everywhere.
- **Implementation.** The core routes call the store and the evaluation
  engine like Weft's own handlers do; routes whose logic lives in a Weft
  handler are forwarded to it with the request and response translated
  (`/calls/stats` and `/calls/metrics` to `/api/v1/spans/stats|metrics`,
  `/files/resolve_url`, `/embeddings/create`, `/image/create`,
  `/link_to_registry`, `/registry/query`, the runtimes and bulk export
  paths). The older `/files/resolve_url` answers this server's files as
  `/file/content?project_id=&digest=` URLs.

### Mapping to Weft's own API

| Older route | Weft's route | Differences |
|---|---|---|
| `POST /call/start` `{"start": {...}}` | `POST /api/v1/spans/start` | flat body; `op_name` → `name`, `thread_id` → `session_id` |
| `POST /call/end` `{"end": {...}}` | `POST /api/v1/spans/end` | flat body; `exception` → `error` |
| `POST /call/upsert_batch` `{"batch": [{"mode", "req": {"start"\|"end"}}]}` | `POST /api/v1/spans/batch` `{items: [{start}\|{end}\|{span}]}` | answer `{"res"}` → `{"items"}` |
| `POST /call/read` `{project_id, id, include_*}` | `GET /api/v1/spans/{id}?project_id=&include_*=` | `{"call": Call\|null}` → the span, or 404 |
| `POST /call/update` `{call_id, display_name}` | `POST /api/v1/spans/update` `{id, display_name}` | |
| `POST /calls/query` (default limit 1000) | `POST /api/v1/spans/query` (default 100, at most 10,000) | `{"calls"}` → `{"items", "total_count"}`; filter `op_names` → `names`, `call_ids` → `ids`, `thread_ids` → `session_ids`; unknown filter keys are refused |
| `POST /calls/stream_query` (`application/jsonl`) | `POST /api/v1/spans/stream` (`application/x-ndjson`) | |
| `POST /calls/query_stats` | `total_count` of `POST /api/v1/spans/query` (`limit: 0`) | `has_more` and `total_storage_size_bytes` only here |
| `POST /calls/delete` `{call_ids}` | `POST /api/v1/spans/delete` `{ids}` | |
| `POST /calls/stats`, `/calls/metrics`, `/calls/export` | `POST /api/v1/spans/stats`, `/metrics`, `/export` | the export writes Weft's field names (`name`, `error`, `session_id`) |
| `POST /calls/op_counts` | `POST /api/v1/spans/names` | `{"ops": [{op_name, op_base_name, count, error_count, last_called}]}` → `{"items": [{name, base_name, count, error_count, last_started_at}]}` |
| `POST /calls/score` `{call_ids}` | `POST /api/v1/spans/score` `{ids}` | `results[].call_id`, `scorer_call_id` → `items[].span_id`, `scorer_span_id` |
| `POST /threads/query` | `POST /api/v1/spans/sessions` | `thread_id` → `session_id`, `call_count` → `span_count`, `start_time` → `started_at`; filter `after_datetime`, `before_datetime`, `thread_ids` → `started_after`, `started_before`, `session_ids` |
| `POST /obj/create` `{"obj": {object_id, val, builtin_object_class}}` | `POST /api/v1/objects/save` `{name, value, class}` | `{digest, object_id, version_index}` → `{name, digest, version, kind, ref}` |
| `POST /obj/read` `{object_id, digest}` | `GET /api/v1/objects/{name}[/versions/{version}]` | `{"obj"}` → the object |
| `POST /objs/query` | `POST /api/v1/objects/query` | filter `object_ids` → `names`, `base_object_classes` and `is_op` → `kinds`, `leaf_object_classes` → `classes`; sort `object_id` → `name`, `version_index` → `version` |
| `POST /obj/delete` | `POST /api/v1/objects/delete` | `object_id` → `name` |
| `POST /obj/alias/set`, `/obj/alias/remove`, `PUT /objs/{id}/aliases`, `/objs/{id}/aliases/remove` | `POST /api/v1/objects/{name}/aliases`, `/aliases/remove` | `digest` → `version` |
| `PUT /objs/{id}/versions/{digest}/tags`, `.../tags/remove` | `POST /api/v1/objects/{name}/versions/{version}/tags`, `.../tags/remove` | |
| `GET /tags`, `GET /aliases` | `POST /api/v1/objects/tags`, `/aliases` | `{"tags"}`, `{"aliases"}` → `{"items", "total_count"}` |
| `POST /table/create` `{"table": {project_id, rows}}`, `/table/create_from_digests` | `POST /api/v1/tables/create` `{project_id, rows \| row_digests}` | |
| `POST /table/update` `{base_digest}` | `POST /api/v1/tables/update` `{digest}` | |
| `POST /table/query` | `POST /api/v1/tables/query`, `/tables/stream` | rows `{digest, val, original_index}` → `{digest, value, index}`; sort `val.x` → `value.x` |
| `POST /table/query_stats`, `/table/query_stats_batch` | `POST /api/v1/tables/stats` | |
| `POST /refs/read_batch` | `POST /api/v1/refs/read` | `{"vals"}` → `{"items"}` |
| `POST /file/create` | `POST /api/v1/files/upload` | adds `size_bytes` |
| `POST /file/content`, `GET /file/content?digest=` | `GET /api/v1/files/{digest}` | |
| `POST /files/resolve_url` | `POST /api/v1/files/resolve` | |
| `POST /feedback/create`, `/feedback/replace` | `POST /api/v1/feedback/create`, `/replace` | `call_ref` → `span_ref` |
| `POST /feedback/batch/create` `{batch}` | `POST /api/v1/feedback/batch` `{items}` | `{"res"}` → `{"items"}` |
| `POST /feedback/query` | `POST /api/v1/feedback/query` | `{"result"}` → `{"items"}` |
| `POST /feedback/purge` `{query}` | `POST /api/v1/feedback/delete` `{ids \| query}` | |
| `POST /cost/create` `{costs}`, `/cost/query`, `/cost/purge` | `POST /api/v1/prices/create` `{prices}`, `/query`, `/delete` | `{"ids": [[id, llm_id]]}` → `{"items": [{id, llm_id}]}`; `{"results"}` → `{"items", "total_count"}` |
| `POST /completions/create`, `/completions/create_stream` | `POST /api/v1/completions`, `/completions/stream` | `track_llm_call` → `track`; `call_id` → `span_id` |
| `POST /embeddings/create`, `/image/create` | `POST /api/v1/embeddings`, `/api/v1/images` | `call_id` → `span_id` |
| `POST /evaluate_model`, `/evaluations/evaluate_model` | `POST /api/v1/evaluations/run` | `{"call_id"}` → `{"id"}` |
| `POST /evaluation_status` `{call_id}` | `GET /api/v1/evaluations/{id}/status` | `{"status": {...}}` → the status |
| `POST /evaluations/compare` `{evaluation_call_ids}` | `POST /api/v1/evaluations/compare` `{ids}` | `call_id` → `id` (runs) and `span_id` (row results) |
| `POST /leaderboard/compute` `{object_id, digest}` | `POST /api/v1/leaderboards/compute` `{name, version}` | `evaluation_call_id` → `evaluation_run_id`; `leaderboard` is an Object |
| `POST /project/stats` | `GET /api/v1/projects/stats` | `call_count` → `span_count` |
| `POST /link_to_registry` `{ref, target: {portfolio_name, entity_name, project_name}}` | `POST /api/v1/registry/link` `{ref, project_id, collection}` | `{"version_index"}` → `{"version"}` |
| `POST /registry/query` | `POST /api/v1/registry/query` | same body and answer |
| `POST /dataset_sources/link`, `/query`, `/source_datasets_query`, `/link_delete` | `POST /api/v1/dataset_sources/link`, `/query`, `/datasets`, `/delete` | `dataset_object_id` → `dataset`, `dataset_digest` (request) → `version`, `link_ids` → `ids`; answers are lists |
| `PUT /v2/{entity}/{project}/runtimes/{name}`, `GET .../runtimes` | `PUT /api/v1/runtimes/{name}` (`project_id` in the body), `GET /api/v1/runtimes?project_id=` | `{"runtimes"}` → `{"items", "total_count"}` |
| `POST /v1/entities/{e}/projects/{p}:export`, `GET .../exports/{id}`, `.../artifacts/{target}` | `POST /api/v1/exports`, `GET /api/v1/exports/{id}`, `.../artifacts/{target}` | `project_id` in the body or query |
| `GET /server_info`, `GET /version` | `GET /api/v1/server` | |

The older call, object and feedback records carry the stored field names:
calls `op_name`, `exception`, `thread_id`; objects `object_id`, `val`,
`version_index`, `base_object_class`, `leaf_object_class`, `kind` (`op` or
`object`); feedback `call_ref`. The older routes take the stored names in
their queries too (`$getField` on `op_name`, `exception`, `thread_id`).

### Routes only the adapter serves

These have no counterpart in Weft's own API.

#### Analytics, stats and usage

| Endpoint | Notes |
|---|---|
| `/feedback/stats` | `{project_id, start, end?, feedback_type?, trigger_ref?, granularity?, timezone?, metrics: [{json_path, value_type: numeric\|boolean\|categorical, aggregations?, percentiles?}]}` → `{start, end, granularity, timezone, buckets: [{timestamp, count, "<agg>__<slug>"}], window_stats: {"<slug>": {"<agg>": v}}}`; slug = json_path with non-alphanumerics → `_`; percentiles are `p<N>`. A `trigger_ref` ending `*` matches by prefix. |
| `/feedback/aggregate` | `{project_id, after_ms, before_ms, feedback_types? (prefixes), scorer_ids?, monitor_ids? (trigger refs), group_by?: ["scorer_id"], rating_min?, rating_max?, tags?, time_bucket_seconds?}` → `{buckets: [{group, time_bucket_start_ms, total_count, scored_count, rating_counts, rating_sums, tag_counts}]}`. Numbers in `payload.output` are ratings (`_rating_` for a bare number, else their path); `true` booleans and short strings are tags. Agent-span groups are always empty. |
| `/feedback/payload_schema` | `{project_id, start, end?, feedback_type?, trigger_ref?, sample_limit?}` → `{paths: [{json_path, value_type}]}` from one payload per trigger ref / feedback type |
| `/table/create_from_digests` | `{project_id, row_digests, expected_digest?}` → `{digest}`; rows must exist |
| `/table/query_stats_batch` | `{project_id, digests, include_storage_size?}` → `{tables: [{digest, count, storage_size_bytes?}]}` |
| `/files/query_stats` | `{project_id}` → `{total_size_bytes}` |
| `/calls/usage` | `{project_id, call_ids, include_costs?}` → `{call_usage: {"<id>": {"<model>": {requests, prompt_tokens, completion_tokens, total_tokens, cache_creation_input_tokens, cache_read_input_tokens, *_total_cost?}}}, unfinished_call_ids}` including descendants |
| `/trace/usage` | `{project_id, filter?, query?, include_costs?, limit?}` → same shape, one entry per matching call |
| `/threads/stream_query` | `/threads/query` body; JSONL of threads |
| `/evaluations/evaluate_model` | alias of `/evaluate_model` |
| `/evaluations/status` | `{project_id, call_id}` → `{"status": {"code": "running", completed_rows, total_rows} \| {"code": "complete", output} \| {"code": "failed", error} \| {"code": "not_found"}}` |
| `/evaluations/rescore` | `{project_id, source_evaluation_run_id, scorer_refs}` → `{call_id, evaluation_run_id}` (same id). Records a new `Evaluation.evaluate` call (`attributes.weft.source_evaluation_run_id`) whose `predict_and_score` children reuse the source run's examples and model outputs and run the given server-runnable scorers. Poll `/evaluations/status`. |
| `/dataset_sources/link` | `{project_id, dataset_object_id, dataset_digest (digest, latest or alias), links: [{row_digest, sources: [{source_id, source_kind: call\|span\|conversation, source_trace_id}], link_metadata?}], include_created_status?}` → `{entries: [{link_id, created?}]}` one per (row, source); relinking returns the same id |
| `/dataset_sources/query` | `{project_id, dataset_object_id, row_digests?, source_kinds?, include_deleted?, limit?, offset?}` → `{links: [{id, row_digest, source_kind, source_id, source_trace_id, source_display_name, source_started_at, link_metadata, added_by, created_at, updated_at, deleted_at, dataset_digest}]}` (call sources resolve display name and start time) |
| `/dataset_sources/source_datasets_query` | `{project_id, sources, include_deleted?}` → `{memberships: [{dataset_object_id, source_id, source_kind, source_trace_id, first_seen_at, row_digests (≤100), row_digests_total_count, row_digests_truncated}]}` |
| `/dataset_sources/link_delete` | `{project_id, link_ids}` → `{entries: [{link_id, deleted}]}` (soft delete) |

#### v2 REST (`/v2/{entity}/{project}/…`)

Objects (`ops`, `datasets`, `models`, `scorers`, `evaluations`):
`POST /{kind}` create → `{digest, object_id, version_index}`;
`GET /{kind}?limit&offset` → JSONL of latest versions;
`GET /{kind}/{object_id}` (latest) and `GET /{kind}/{object_id}/versions/{digest}` → one item;
`DELETE /{kind}/{object_id}?digests=` → `{num_deleted}`.

| Kind | Create body | Stored as | Item fields (plus `object_id, digest, version_index, created_at`) |
|---|---|---|---|
| ops | `{name?, source_code}` (name defaults to the first `def`) | `{"_type":"Op","name","code","language":"python"}` | `code` |
| datasets | `{name, description?, rows: [{}]}` | `Dataset` with a new rows table | `name, description, rows` (table ref) |
| models | `{name, source_code, description?, attributes?}` | `Model` object (`source_code`, `attributes`) | `name, description, source_code, attributes` |
| scorers | `{name, op_source_code, description?}` | op `<name>.score` + `Scorer` object with `score` = op ref | `name, description, score_op` |
| evaluations | `{name, dataset, scorers?, trials?, description?, evaluation_name?, eval_attributes?}` | `Evaluation` (`eval_attributes` → `metadata`) | `name, description, dataset, scorers, trials, evaluation_name, evaluate_op, predict_and_score_op, summarize_op` (ops null) |

Evaluation runs, predictions and scores are calls, so they show up in
`/evaluations/compare`, leaderboards and the UI:

| Endpoint | Behaviour |
|---|---|
| `POST /evaluation_runs` `{evaluation, model, source_evaluation_run_id?}` | starts an `Evaluation.evaluate` call (inputs `self`, `model`; `attributes.weft.v2 = "evaluation_run"`) → `{evaluation_run_id}` (the call id) |
| `GET /evaluation_runs?evaluation_run_ids&evaluations&models&limit&offset` | JSONL of `{evaluation_run_id, evaluation, model, status: running\|finished\|failed, started_at, finished_at, summary, source_evaluation_run_id}`; lists every `Evaluation.evaluate` call, including SDK and server runs |
| `GET /evaluation_runs/{id}` | one run |
| `POST /evaluation_runs/{id}/finish` `{summary?}` | ends the call; without a summary, one is computed from the finished predictions like the SDKs do → `{success}` |
| `DELETE /evaluation_runs?evaluation_run_ids=` | soft-deletes the runs and their descendants → `{num_deleted}` |
| `POST /predictions` `{model, inputs, output, evaluation_run_id?, genai_span_ref?}` | in a run: an `Evaluation.predict_and_score` child of the run (inputs `self`, `model`, `example` = inputs) with a finished `<model>.predict` child holding the output; outside a run: one finished `<model>.predict` call → `{prediction_id}` |
| `GET /predictions/{id}`, `GET /predictions?evaluation_run_id` | `{prediction_id, model, inputs, output, evaluation_run_id, user_id}` (list is JSONL) |
| `POST /predictions/{id}/finish` | ends the `predict_and_score` call with `{output, scores: {<scorer>: value}, model_latency}` from its score children → `{success}` |
| `POST /scores` `{prediction_id, scorer, value, evaluation_run_id?}` | a finished `<scorer>.score` child of the prediction (inputs `self` = scorer, `output`, the prediction inputs) plus `weft.scorer.<scorer>` feedback on the prediction → `{score_id}`; `scorer` may be a ref or a plain name |
| `GET /scores/{id}`, `GET /scores?evaluation_run_id` | `{score_id, scorer, value, evaluation_run_id, user_id}` |
| `DELETE /predictions?prediction_ids=`, `DELETE /scores?score_ids=` | soft delete → `{num_deleted}` |
| `POST /eval_results/query` | `{evaluation_call_ids \| evaluation_run_ids, include_rows?, include_summary?, include_raw_data_rows?, include_predict_and_score_children?, require_intersection?, sort_by?: [{field: "scores.<name>" \| "inputs.<path>" \| "outputs.<path>", direction, evaluation_call_id?}], limit?, offset?}` → `{rows: [{row_digest, raw_data_row, evaluations: [{evaluation_call_id, trials: [{predict_and_score_call_id, predict_call_id, scorer_call_ids, model_output, model_latency_seconds, scores, total_tokens}]}]}], summary: {row_count, evaluations: [{evaluation_call_id, display_name, evaluation_ref, model_ref, started_at, trial_count, predict_total_tokens, scorer_stats: [{scorer_key, path, value_type: binary\|continuous\|text, trial_count, pass_true_count, pass_known_count, pass_rate, pass_signal_coverage, numeric_count, numeric_mean}]}]}, total_rows, warnings}`. `include_*` default to true. `filters` are not supported and produce a warning; costs are not computed. |

#### Other shapes

- **`/calls/query_stats`** accepts `limit`: `count` stops at the limit and
  `has_more` says whether more calls match (always present).
- **`/feedback/query`** returns `total_count`: feedback matching `query`,
  ignoring `limit` and `offset`.
- **`GET /version`** → `{"version", "server": "weft", "trace_server_version"}`,
  no API key needed.

**Inference catalog.** Weft's own model catalog: the built-in models of
`/llm/models` with Weft's list prices, plus the project's `ProviderModel`
objects (custom providers and runtimes) when `?project_id=` is given. These
need an API key when auth is on.

| Endpoint | Shape |
|---|---|
| `GET /inference/catalog/models` | `{"models": [LLMModelDetails]}`: `id`/`idPlayground` are Weft model ids; prices in cents per billion tokens; `status` is `unavailable` when no provider key is configured; fields Weft cannot know are empty, 0, `false` or `"bf16"` (quantization) |
| `GET /inference/modelsdev/models` | models.dev `api.json`: `{"<provider>": {id, name, npm, env, doc, api, models: {"<model id>": {..., cost: {input, output, cache_read, cache_write} USD per million tokens, limit}}}}` |
| `GET /inference/router/openrouter/models`, `GET /inference/analysis/artificialanalysis/models` | `{"data": [{id, name, context_length, max_output_length, pricing: {prompt, completion, input_cache_read} USD per token as strings, supported_features, ...}]}` |
| `GET /inference/nvidia/v2/hardware?model=` | `{"hardware": [{"id": "weft-<provider>", "type": "serverless", "pricing": {cents_per_million_input_tokens, cents_per_million_output_tokens}, "specs"}]}` for catalog models whose name matches; `model` is required |

**Geolocation.** `GET /geolocate?ip=` (no API key needed) →
`{"ip": <ip or the caller's address>, "location": null, "allowed": true}`.
Weft has no GeoIP database and no regional restrictions; an invalid `ip` is
a 400.

## Data written before the rename

Databases from earlier Weft versions used the names in the left column of the
[Names](#names) table above. When the server opens such a database it migrates it once,
whether or not the adapter is on:

- columns get Weft's names: the feedback table's `weave_ref` becomes `ref`,
  and `wb_user_id`, `wb_run_id`, `wb_run_step`, `wb_run_step_end` of calls,
  feedback, objects and registry versions become `user_id`, `run_id`,
  `run_step`, `run_step_end`;
- stored feedback types get their Weft names;
- legacy attribute keys of stored calls are renamed (eval links, parent-call
  links, span kinds, turn markers);
- saved queries get Weft's names (below);
- Weft's own field names that said op, call or thread are renamed
  (`weft-names-1`; [DESIGN.md](DESIGN.md) §10.5, *Other APIs*): monitors'
  `op_names` and the per-call scope `call`, automations' `op_names` and
  `min_calls`, saved views' and dashboards' filter keys `op_names`,
  `call_ids`, `thread_ids` and group keys `op_name` / `thread`,
  leaderboards' `evaluation_call_ids`, annotation specs' `op_scope` (new
  versions, as for saved queries below); automation runs' events and
  `weft.signal` feedback payloads (in place); dataset lineage links of kind
  `call` (kind `span`);
- refs held in columns get Weft's kind names, `function` for `op` and
  `span` for `call`: feedback `ref`, `call_ref`, `runnable_ref`,
  `annotation_ref` and `trigger_ref`, span names (`op_name`) and registry
  entries (`store/refkinds.go`, migration `ref-kinds-1`). Values (object
  values, table rows, span inputs and outputs) are never rewritten; Weft
  reads both kind names, and filters on refs inside values match both.

Stored digests are never recomputed: values saved before Weft's own digest
form keep their digests, which resolve as before.

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

### Objects and fields the client shares with Weft

Some of Weft's own field names were also the client's, and some still are:

- **Saved objects.** The client publishes `Monitor` (`op_names`),
  `AnnotationSpec` (`op_scope`) and `SavedView` (filter `op_names`, …)
  objects with its names. The adapter stores object values as sent (they
  are content-addressed, and the client computes their digests), so Weft's
  readers of those classes (the monitor runner, the UI, the SDKs) accept
  the earlier field name as well as Weft's (`names`).
- **Dataset lineage.** The client's source kinds `call` and `span` are both
  Weft spans (`span`); the adapter maps them on the way in, and names Weft's
  `span` kind `call` in answers to the client.
- **Metric buckets.** `/calls/stats` takes the client's `call_metrics`
  (metric `call_count`) and answers `call_buckets` (`<agg>_call_count`
  columns); Weft's `/api/v1/spans/stats` says `span_metrics`, `span_count`
  and `span_buckets`.
- **Feedback.** `/feedback/batch/create` items name the scorer's own span
  `call_ref` (Weft: `span_ref`).
- **Streams.** The adapter's JSON Lines streams (`/calls/stream_query`,
  `/threads/stream_query`, …) answer `200` with no lines when nothing
  matches, as Weft's NDJSON streams do.

## Limits

- Only the official Python client is tested: `scripts/weft_weave_compat_check.py`
  drives `weave==0.54.0` against a server started with `--weave-compat` (ops,
  nested calls with usage roll-up, reactions and notes, a dataset published
  and read back through refs, an evaluation, `get_call`) and checks what Weft
  stored through Weft's own API (`/api/v1`): spans named by function refs,
  feedback on span refs, the client's digest form kept for its objects. CI runs it in the *Server and SDKs* job; the
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
