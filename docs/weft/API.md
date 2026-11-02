# Weft trace server API

Weft is an OpenTelemetry-native LLM tracing and evaluation server. It follows
the data model popularised by W&B Weave (calls, ops, objects, tables,
feedback, evaluations) and accepts OTLP spans directly.

Base URL defaults to `http://localhost:6345`. All endpoints are `POST` with a
JSON body unless marked `GET`. Errors return a non-2xx status with
`{"reason": "<message>"}`.

## Conventions

- **project_id** is `"<entity>/<project>"`. A bare `"<project>"` is normalised
  to `"default/<project>"`. Projects are created implicitly on first write.
- **Timestamps** are RFC 3339 strings with up to nanosecond precision, UTC.
- **IDs**: SDK calls use UUIDv7 strings for `id` and `trace_id`. OTLP spans use
  the hex span id (16 chars) and hex trace id (32 chars).
- **Digests** are unpadded base64url SHA-256 of canonical JSON (keys sorted,
  no whitespace). Identical values always produce identical digests.
- **Refs** (`weave:///` is accepted as an alias of `weft:///` everywhere):
  - object: `weft:///<entity>/<project>/object/<name>:<digest>`
  - op: `weft:///<entity>/<project>/op/<name>:<digest>`
  - table: `weft:///<entity>/<project>/table/<digest>`
  - call: `weft:///<entity>/<project>/call/<call_id>`
  - A ref may carry an extra path that walks into the value:
    `/attr/<field>`, `/key/<dict key>`, `/index/<n>`, `/id/<row_digest>`
    (for tables). A string value that is itself a ref is followed when walking.
    Example dataset row: `weft:///e/p/object/my_ds:<d>/attr/rows/id/<row_digest>`.
- **Auth**: when the server is started with API keys, every endpoint except
  `GET /health`, `GET /server_info` and the static UI needs one of
  `Authorization: Bearer <key>`, `Authorization: Basic base64("api:<key>")`,
  or `X-Weft-Api-Key: <key>`. `GET /auth/check` returns 200 or 401.
  Keys may be configured as `name=key`; requests with that key act as user
  `name`, which becomes the default `wb_user_id` of calls, objects and
  feedback. Otherwise the `X-Weft-User` header is used, and feedback without
  a user is stored as `"anonymous"`.

## Server

| Endpoint | Response |
|---|---|
| `GET /health` | `{"status":"ok"}` |
| `GET /server_info` | `{"version": "...", "auth_required": bool, "otel_endpoint": "/otel/v1/traces"}` |
| `GET /auth/check` | `{"ok": true}` or 401 |
| `GET /auth/whoami` | `{"user_id": "alice" \| "anonymous", "auth_required": bool}` |
| `GET /projects` | `{"projects": [{"project_id","entity","project","call_count","trace_count","object_count","last_activity"}]}` |
| `/project/stats` `{project_id}` | `{"call_count","trace_count","object_count","table_count","file_count","feedback_count","storage_size_bytes"}` |

## Calls

### Call schema

```jsonc
{
  "id": "0190...", "project_id": "e/p",
  "op_name": "weft:///e/p/op/my_fn:abc" /* or a plain span name for OTLP */,
  "display_name": null, "trace_id": "...", "parent_id": null,
  "thread_id": null, "turn_id": null,
  "started_at": "2026-01-01T00:00:00.000000Z", "ended_at": null,
  "exception": null,                 // string when the call failed
  "attributes": {},                  // free-form; OTLP spans put raw span data under attributes.otel
  "inputs": {}, "output": null,
  "summary": {
    "usage": {"gpt-4o-mini": {"requests":1,"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}},
    "status_counts": {"success": 3, "error": 0},   // descendants (rolled up)
    "weave": {                                     // computed on read
      "status": "success|error|running|descendant_error",
      "latency_ms": 1234, "trace_name": "my_fn", "display_name": null,
      "costs": {"gpt-4o-mini": {"requests":1,"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,
                "prompt_token_cost":1.5e-7,"completion_token_cost":6e-7,
                "prompt_tokens_total_cost":1.5e-6,"completion_tokens_total_cost":3e-6,
                "prompt_token_cost_unit":"USD","completion_token_cost_unit":"USD",
                "effective_date":"...","provider_id":"openai","pricing_level":"default|project"}}
    }
  },
  "wb_user_id": null, "wb_run_id": null, "deleted_at": null,
  "feedback": [ /* Feedback, only with include_feedback */ ],
  "storage_size_bytes": 123          // only with include_storage_size
}
```

**Usage roll-up.** Clients report only a call's *own* LLM usage in
`summary.usage` at call end. The server sums usage and `status_counts` from all
descendants, so `summary.usage` on a root call is the total for the trace.
`summary.weave.costs` is present when `include_costs` is true.

### Endpoints

| Endpoint | Body | Response |
|---|---|---|
| `/call/start` | `{"start": {project_id, id?, op_name, display_name?, trace_id?, parent_id?, thread_id?, turn_id?, started_at, attributes?, inputs?, wb_user_id?, wb_run_id?}}` | `{"id","trace_id"}` (generated when absent) |
| `/call/end` | `{"end": {project_id, id, ended_at, exception?, output?, summary?}}` | `{}` |
| `/call/upsert_batch` | `{"batch": [{"mode":"start","req":{"start":{...}}}, {"mode":"end","req":{"end":{...}}}]}` | `{"res": [...]}` |
| `/call/read` | `{project_id, id, include_costs?, include_feedback?, include_storage_size?}` | `{"call": Call\|null}` |
| `/calls/stream_query` | CallsQuery | `application/jsonl`, one Call per line |
| `/calls/query` | CallsQuery | `{"calls": [Call]}` |
| `/calls/query_stats` | `{project_id, filter?, query?, search?}` | `{"count", "total_storage_size_bytes"}` |
| `/calls/delete` | `{project_id, call_ids, wb_user_id?}` | `{}` (soft delete, cascades to descendants) |
| `/call/update` | `{project_id, call_id, display_name}` | `{}` |
| `/calls/stats` | `{project_id, filter?, query?, search?, start?, end?, bucket_seconds?}` | see below |
| `/calls/score` | `{project_id, call_ids, scorer_refs}` (server-runnable scorers) | `{"results": [{"call_id","scorer","output","scorer_call_id","feedback_id","error"?}]}`; each result is also stored as `wandb.runnable.<scorer>` feedback on the call |
| `/calls/op_counts` | `{project_id, trace_roots_only?}` | `{"ops": [{"op_name","op_base_name","count","error_count","last_called"}]}` |

**CallsQuery**

```jsonc
{
  "project_id": "e/p",
  "filter": {
    "op_names": ["weft:///e/p/op/f:*", "f", "openai.chat"],  // ref, ref with :* wildcard, or base name
    "input_refs": [], "output_refs": [], "parent_ids": [], "trace_ids": [],
    "call_ids": [], "trace_roots_only": false, "wb_user_ids": [], "wb_run_ids": [],
    "thread_ids": [], "turn_ids": []
  },
  "query": {"$expr": Operation},
  "search": "free text over op name, display name, inputs, output, exception",
  "sort_by": [{"field": "started_at", "direction": "desc"}],  // default started_at desc
  "limit": 100, "offset": 0,
  "columns": ["id", "inputs.x"],     // optional projection hint
  "expand_columns": ["inputs.example", "inputs.model"],  // resolve refs at these paths
  "include_costs": false, "include_feedback": false, "include_storage_size": false,
  "include_deleted": false
}
```

**Operation language** (Mongo-style, used by calls, feedback and costs):

- Logic: `{"$and": [op...]}`, `{"$or": [op...]}`, `{"$not": [op]}`
- Comparison: `{"$eq": [a, b]}`, `{"$gt": [a, b]}`, `{"$gte": [a, b]}`, `{"$lt": [a, b]}`, `{"$lte": [a, b]}`, `{"$in": [a, [b, c]]}`
- Text: `{"$contains": {"input": a, "substr": b, "case_insensitive": true}}`
- Operands: `{"$literal": v}`, `{"$getField": "inputs.question"}`,
  `{"$convert": {"input": a, "to": "double|int|string|bool|exists"}}`, or a nested operation.

Gettable call fields: `id, op_name, display_name, trace_id, parent_id, thread_id, turn_id, started_at, ended_at, exception, wb_user_id, wb_run_id, deleted_at`, any dotted path below `inputs.`, `output.`, `attributes.`, `summary.`, plus the computed `summary.weave.status`, `summary.weave.latency_ms`, `summary.weave.trace_name`, `summary.total_tokens`, and feedback fields `feedback.<feedback_type>.<payload path>` (for example `feedback.wandb.reaction.1.emoji`, `feedback.wandb.runnable.my_judge.output.score`).
Sort fields use the same paths.

**`/calls/stats` response**

```jsonc
{"bucket_seconds": 3600,
 "buckets": [{"start": "...", "count": 10, "error_count": 1,
              "latency_ms_avg": 12, "latency_ms_p50": 10, "latency_ms_p95": 30, "latency_ms_p99": 40,
              "prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150, "cost_usd": 0.001}],
 "totals": {"count", "error_count", "total_tokens", "cost_usd", "latency_ms_p50", "latency_ms_p95"}}
```
`start`/`end` default to the span of matching calls; `bucket_seconds` is chosen to give about 60 buckets when absent.

## Threads

| Endpoint | Body | Response |
|---|---|---|
| `/threads/query` | `{project_id, limit?, offset?, sort_by?, filter?: {after_datetime?, before_datetime?, thread_ids?}}` | `{"threads": [{"thread_id","turn_count","call_count","start_time","last_updated","first_turn_id","last_turn_id","p50_turn_duration_ms","p99_turn_duration_ms","total_tokens","cost_usd"}]}` |

A *turn* is a call with `thread_id` set and either `turn_id == id` or no parent
in the same thread. Sort fields: `last_updated` (default desc), `start_time`, `turn_count`.

## Objects

ObjSchema:

```jsonc
{"project_id","object_id","created_at","deleted_at":null,"digest","version_index":0,"is_latest":true,
 "kind":"object|op","base_object_class":"Dataset|null","leaf_object_class":"Dataset|null",
 "val": {...}, "wb_user_id":null, "aliases": ["latest","prod"], "size_bytes": 123,
 "version_count": 3   // only on latest_only queries
}
```

`base_object_class` is the first of `_class_name`, then `_bases`, that is in
the known list: `Dataset, Model, LLMStructuredCompletionModel, Evaluation,
Scorer, LLMAsAJudgeScorer, Prompt, StringPrompt, MessagesPrompt, Leaderboard,
Monitor, AnnotationSpec, SavedView, Provider, ProviderModel, Op`. Use the more
general class for base (e.g. `StringPrompt` has base `Prompt`, leaf
`StringPrompt`; `LLMAsAJudgeScorer` has base `Scorer`). `kind` is `op` when
`val._type == "Op"`. `builtin_object_class` on create forces the class.

| Endpoint | Body | Response |
|---|---|---|
| `/obj/create` | `{"obj": {project_id, object_id, val, builtin_object_class?, wb_user_id?}}` | `{"digest","object_id","version_index"}` (re-publishing an identical val returns the existing version) |
| `/obj/read` | `{project_id, object_id, digest}` — digest may be a digest, `latest`, `v<N>`, or an alias | `{"obj": ObjSchema}` |
| `/objs/query` | `{project_id, filter?: {base_object_classes?, leaf_object_classes?, object_ids?, is_op?, latest_only?}, limit?, offset?, sort_by?: [{field: "created_at\|object_id", direction}], metadata_only?, include_deleted?}` | `{"objs": [ObjSchema]}` |
| `/obj/delete` | `{project_id, object_id, digests?}` (all versions when absent) | `{"num_deleted"}` |
| `/obj/alias/set` | `{project_id, object_id, digest, alias}` | `{}` (moves the alias if it existed) |
| `/obj/alias/remove` | `{project_id, object_id, alias}` | `{}` |

### Object value conventions

Objects are JSON. Class information lives in `_type`, `_class_name`, `_bases`.
Nested objects, tables and ops are stored as ref strings.

```jsonc
// Op
{"_type":"Op","name":"my_fn","code":"def my_fn(x):\n  ...","language":"python","signature":"(x)"}
// Dataset
{"_type":"Dataset","_class_name":"Dataset","_bases":["Object","BaseModel"],"name":"qa","description":null,
 "rows":"weft:///e/p/table/<digest>"}
// Model (SDK subclass)
{"_type":"MyModel","_class_name":"MyModel","_bases":["Model","Object","BaseModel"],"name":"m","prompt":"...",
 "predict":"weft:///e/p/op/MyModel.predict:<d>"}
// LLMStructuredCompletionModel (server-runnable model)
{"_type":"LLMStructuredCompletionModel","_class_name":"LLMStructuredCompletionModel",
 "_bases":["Model","Object","BaseModel"],"name":"m","llm_model_id":"gpt-4o-mini",
 "default_params":{"messages_template":[{"role":"system","content":"..."},{"role":"user","content":"{question}"}],
                   "temperature":0,"max_tokens":512,"response_format":"text|json_object"}}
// Evaluation
{"_type":"Evaluation","_class_name":"Evaluation","_bases":["Object","BaseModel"],"name":"qa-eval",
 "description":null,"dataset":"weft:///e/p/object/qa:<d>","scorers":["weft:///e/p/object/judge:<d>"],"trials":1}
// Scorers the server can run (evaluate_model and monitors)
{"_class_name":"LLMAsAJudgeScorer","_bases":["Scorer","Object","BaseModel"],"name":"judge",
 "model":"gpt-4o-mini" /* or ref to LLMStructuredCompletionModel */,
 "scoring_prompt":"Is {output} a correct answer to {question}? Reply JSON {\"correct\": bool}",
 "response_format":"json_object"}
{"_class_name":"ExactMatchScorer","_bases":["Scorer","Object","BaseModel"],"name":"exact","target_column":"expected"}
{"_class_name":"ContainsScorer", ... "target_column":"expected","case_insensitive":true}
{"_class_name":"RegexScorer", ... "pattern":"^\\d+$"}
{"_class_name":"JSONValidScorer", ...}
{"_class_name":"LevenshteinScorer", ... "target_column":"expected"}   // returns similarity 0..1
// Prompts
{"_class_name":"StringPrompt","_bases":["Prompt","Object","BaseModel"],"name":"p","content":"Hello {name}"}
{"_class_name":"MessagesPrompt","_bases":["Prompt","Object","BaseModel"],"name":"p","messages":[{"role":"system","content":"..."}]}
// Leaderboard
{"_class_name":"Leaderboard","_bases":["Object","BaseModel"],"name":"lb","description":"",
 "columns":[{"evaluation_object_ref":"weft:///e/p/object/qa-eval:<d>","scorer_name":"exact",
             "summary_metric_path":"true_fraction","should_minimize":false}]}
// Monitor (online evaluation)
{"_class_name":"Monitor","_bases":["Object","BaseModel"],"name":"mon","description":"","active":true,
 "sampling_rate":1.0,"op_names":["openai.chat"],"query":{"$expr":{...}} /* optional */,
 "scorers":["weft:///e/p/object/judge:<d>"]}
// AnnotationSpec (human feedback schema)
{"_class_name":"AnnotationSpec","_bases":["Object","BaseModel"],"name":"quality","description":"",
 "field_schema":{"type":"integer","minimum":1,"maximum":5},   // or {"type":"string","enum":[..]}, {"type":"boolean"}, {"type":"string","maxLength":500}
 "unique_among_creators":true,"op_scope":null}
// SavedView
{"_class_name":"SavedView","_bases":["Object","BaseModel"],"name":"errors","view_type":"traces","label":"Errors",
 "definition":{"filter":{...},"query":{...},"sort_by":[...],"cols":[...]}}
// Custom OpenAI-compatible provider
{"_class_name":"Provider","_bases":["Object","BaseModel"],"name":"local","base_url":"http://localhost:11434/v1",
 "api_key_name":"OLLAMA_KEY","extra_headers":{},"return_type":"openai"}
{"_class_name":"ProviderModel","_bases":["Object","BaseModel"],"name":"llama3","provider":"weft:///e/p/object/local:<d>","max_tokens":4096}
```

Media values: `{"_type":"Image","file_digest":"<d>","mime_type":"image/png"}`,
`{"_type":"Audio","file_digest":"<d>","mime_type":"audio/wav"}`, or a
`data:<mime>;base64,...` string. The UI renders both forms.

## Tables

| Endpoint | Body | Response |
|---|---|---|
| `/table/create` | `{"table": {project_id, rows: [any]}}` | `{"digest","row_digests"}` |
| `/table/update` | `{project_id, base_digest, updates: [{"append":{"row":...}} \| {"pop":{"index":n}} \| {"insert":{"index":n,"row":...}} \| {"replace":{"index":n,"row":...}}]}` | `{"digest","updated_row_digests"}` |
| `/table/query` | `{project_id, digest, filter?: {row_digests?}, limit?, offset?, sort_by?: [{field: "val.x", direction}]}` | `{"rows": [{"digest","val","original_index"}]}` |
| `/table/query_stats` | `{project_id, digest}` | `{"count"}` |

## Refs and files

| Endpoint | Body | Response |
|---|---|---|
| `/refs/read_batch` | `{"refs": [ref...]}` | `{"vals": [any\|null]}` |
| `/file/create` | multipart form: `project_id`, `file` | `{"digest"}` |
| `/file/content` | `{project_id, digest}` | raw bytes |
| `GET /file/content?project_id=..&digest=..` | | raw bytes with sniffed content type (for `<img src>`) |

## Feedback

Feedback schema:
`{"id","project_id","weave_ref","feedback_type","payload","creator","created_at","wb_user_id","annotation_ref","runnable_ref","call_ref","trigger_ref"}`

Feedback types:
- `wandb.reaction.1` payload `{"emoji": "👍"}`
- `wandb.note.1` payload `{"note": "text"}`
- `wandb.annotation.<spec name>` payload `{"value": ...}`, `annotation_ref` = AnnotationSpec ref
- `wandb.runnable.<scorer name>` payload `{"output": <score>}`, `runnable_ref` = scorer ref, `call_ref` = the scorer's own call, `trigger_ref` = monitor ref when a monitor produced it
- anything else: free-form

`weave_ref` targets the scored thing, usually `weft:///e/p/call/<id>`.

| Endpoint | Body | Response |
|---|---|---|
| `/feedback/create` | `{project_id, weave_ref, feedback_type, payload, creator?, wb_user_id?, annotation_ref?, runnable_ref?, call_ref?, trigger_ref?}` | `{"id","created_at","wb_user_id","payload"}` |
| `/feedback/query` | `{project_id, query?, sort_by?, limit?, offset?}` | `{"result": [Feedback]}` |
| `/feedback/replace` | create body plus `feedback_id` | same as create (new id) |
| `/feedback/purge` | `{project_id, query}` (typically `$eq id`) | `{}` |

Feedback query fields: `id, weave_ref, feedback_type, created_at, creator, wb_user_id, annotation_ref, runnable_ref, call_ref, trigger_ref, payload.<path>`.

## Costs

Built-in prices cover common OpenAI, Anthropic, Google, Mistral and Meta
models. Project prices override them.

| Endpoint | Body | Response |
|---|---|---|
| `/cost/create` | `{project_id, costs: {"<llm_id>": {prompt_token_cost, completion_token_cost, prompt_token_cost_unit?, completion_token_cost_unit?, effective_date?, provider_id?}}}` (per-token USD) | `{"ids": [[id, llm_id]]}` |
| `/cost/query` | `{project_id, query?, limit?, offset?, include_defaults?}` | `{"results": [{"id","llm_id","prompt_token_cost","completion_token_cost","prompt_token_cost_unit","completion_token_cost_unit","effective_date","provider_id","pricing_level"}]}` |
| `/cost/purge` | `{project_id, ids?: [], query?}` | `{}` |

## LLMs and playground

| Endpoint | Body | Response |
|---|---|---|
| `GET /llm/models` | | `{"models": [{"id","provider","label","available","max_tokens"}]}` (`available` = key configured) |
| `/completions/create` | `{project_id, inputs: {model, messages, temperature?, max_tokens?, top_p?, frequency_penalty?, presence_penalty?, stop?, n?, response_format?, tools?}, track_llm_call?: true}` | `{"response": OpenAIChatCompletion \| {"error": "..."}, "weave_call_id": "..."}` |
| `/completions/create_stream` | same | `application/jsonl` of OpenAI chunk objects, last line `{"_meta":{"weave_call_id": "..."}}` |
| `/secrets/set` | `{name, value}` | `{}` |
| `GET /secrets/list` | | `{"secrets": [{"name","source":"env\|db"}]}` (never values) |
| `/secrets/delete` | `{name}` | `{}` |

Model ids: `gpt-*`, `o1*`, `o3*`, `o4*` → OpenAI; `claude-*` → Anthropic;
`<provider>/<model>` where `<provider>` is `openai`, `anthropic`, or the
`object_id` of a Provider object; `mock/echo` and `mock/json` are offline test
models that echo the last user message. Responses are always OpenAI chat
completion shaped. Tracked completions are recorded as calls with
`op_name = "weft.completions.create"`.

## Evaluations

The SDKs run evaluations client side and record calls with these op names
(the op ref's name part):

- `Evaluation.evaluate` — inputs `{"self": <evaluation ref>, "model": <model ref>}`,
  output is the summary, e.g. `{"exact": {"true_count": 3, "true_fraction": 0.75}, "judge": {"score": {"mean": 4.2}}, "model_latency": {"mean": 0.8}}`.
  Bool scores summarise to `{true_count, true_fraction}`, numbers to `{mean}`, dicts recursively.
- `Evaluation.predict_and_score` (children) — inputs `{"self", "model", "example": <row ref or dict>}`,
  output `{"output": <model output>, "scores": {"<scorer>": <score>}, "model_latency": <seconds>}`.
- model predict and scorer calls are children of `predict_and_score`; `Evaluation.summarize` is a child of `evaluate`.

| Endpoint | Body | Response |
|---|---|---|
| `/evaluate_model` | `{project_id, evaluation_ref, model_ref, wb_user_id?}` | `{"call_id"}` — runs server side in the background; model must be `LLMStructuredCompletionModel`, scorers must be server-runnable |
| `/evaluation_status` | `{project_id, call_id}` | `{"status": {"code": "running\|complete\|failed\|unknown", "completed_rows", "total_rows", "error"?}}` |
| `/evaluations/compare` | `{project_id, evaluation_call_ids}` | see below |
| `/leaderboard/compute` | `{project_id, object_id, digest?}` | see below |

`/evaluations/compare` response:

```jsonc
{"evaluations": [{"call_id","display_name","evaluation_ref","evaluation_name","model_ref","model_name",
                  "started_at","ended_at","status","summary": {...}, "dataset_ref", "scorers": ["exact"],
                  "total_tokens", "cost_usd", "row_count"}],
 "metrics": [{"key": "exact.true_fraction", "scorer": "exact", "path": "true_fraction",
              "kind": "fraction|mean|other", "values": {"<call_id>": 0.75}}],
 "rows": [{"row_key": "<row digest or input hash>", "inputs": {...},
           "results": {"<call_id>": [{"call_id": "<predict_and_score id>", "output": ..., "scores": {...},
                                      "model_latency": 0.5, "total_tokens": 10, "trial": 0}]}}]}
```

`/leaderboard/compute` response:

```jsonc
{"leaderboard": ObjSchema,
 "columns": [{"index": 0, "evaluation_object_ref","evaluation_name","scorer_name","summary_metric_path","should_minimize"}],
 "rows": [{"model_ref","model_name","values": {"0": {"value": 0.75, "evaluation_call_id": "...", "started_at": "..."}}}]}
```
Each cell uses the most recent finished `Evaluation.evaluate` call for that evaluation object name and model.

## Monitors

Active `Monitor` objects (latest version) are evaluated whenever a call ends
or an OTLP span is ingested. Calls whose op base name is in `op_names` (empty
means all root calls), that match `query`, and pass `sampling_rate` are scored
asynchronously by each server-runnable scorer. The result is stored as
`wandb.runnable.<scorer name>` feedback with `trigger_ref` set to the monitor
ref, and the scorer's own call is recorded with `op_name = "<scorer name>.score"`.
Scorer calls and completions are never themselves monitored.

## OpenTelemetry ingest

`POST /otel/v1/traces` accepts OTLP/HTTP `ExportTraceServiceRequest` as
`application/x-protobuf` or `application/json`, gzip allowed. The project comes
from the `project_id` header (also `x-weft-project`), else the resource
attribute `weft.project` / `wandb.entity` + `wandb.project`, else `default/default`.

Span → call mapping:

| Call field | Source (first match wins) |
|---|---|
| `id`, `trace_id`, `parent_id` | hex span id, trace id, parent span id |
| `op_name` | `weft.op_name`, else span name |
| `display_name` | `weft.display_name`, `wandb.display_name` |
| `started_at`, `ended_at` | span start/end |
| `inputs` | `weft.inputs` (JSON), `input.value` (OpenInference), `gen_ai.input.messages`, `llm.input_messages.N.message.*`, `gen_ai.prompt.N.*` (OpenLLMetry), `gen_ai.prompt`, `traceloop.entity.input`, `gen_ai.*.message` span events. Request params (`gen_ai.request.*`, `llm.invocation_parameters`) are added. |
| `output` | `weft.output`, `output.value`, `gen_ai.output.messages`, `llm.output_messages.N.message.*`, `gen_ai.completion.N.*`, `gen_ai.completion`, `traceloop.entity.output`, `gen_ai.choice` events |
| `summary.usage` | model from `gen_ai.response.model` / `gen_ai.request.model` / `llm.model_name`; tokens from `gen_ai.usage.input_tokens\|prompt_tokens`, `gen_ai.usage.output_tokens\|completion_tokens`, `llm.token_count.*` |
| `exception` | span status ERROR message, or the `exception` event (`exception.type: exception.message`) |
| `thread_id`, `turn_id` | `weft.thread_id`, `gen_ai.conversation.id`, `session.id`; `weft.turn_id` |
| `wb_user_id` | `user.id`, `enduser.id` |
| `attributes` | `weft.attributes.*` (prefix stripped), plus `otel: {span_name, kind, status, resource, scope, attributes, events, links}` |

The response is an OTLP `ExportTraceServiceResponse` in the request's encoding.
