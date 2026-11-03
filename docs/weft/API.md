# Weft trace server API

Weft is an OpenTelemetry-native LLM tracing and evaluation server. This page
documents its trace API, which Weft's own SDKs, UI and plugins use: calls,
ops, objects, tables, feedback and evaluations, plus the native `/api/v1`
API (sessions, agents, teams, detections). It also accepts OTLP spans
directly. Clients of another tracing product can use the optional
compatibility adapter described in [COMPATIBILITY.md](COMPATIBILITY.md); it is
off by default and nothing on this page depends on it.

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
- **Refs**:
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
  or `X-Weft-Api-Key: <key>`, or (browsers) an SSO session cookie with
  `X-Weft-CSRF` on non-GET requests. `GET /auth/check` returns 200 or 401.
  Access is role-checked; see [Users, teams and roles](#users-teams-and-roles).
  Keys may be configured as `name=key`; requests with that key act as user
  `name`, which becomes the default `wb_user_id` of calls, objects and
  feedback. Otherwise the `X-Weft-User` header is used, and feedback without
  a user is stored as `"anonymous"`.

## Server

| Endpoint | Response |
|---|---|
| `GET /health` | `{"status":"ok"}` |
| `GET /server_info` | `{"version": "...", "auth_required": bool, "otel_endpoint": "/otel/v1/traces", "trace_server_version": "..."}` (with the compatibility adapter on, also the fields it adds; see COMPATIBILITY.md) |
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
    "weft": {                                      // computed on read
      "status": "success|error|running|descendant_error",
      "latency_ms": 1234, "trace_name": "my_fn", "display_name": null,
      "costs": {"gpt-4o-mini": {"requests":1,"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,
                "cache_read_input_tokens":0,"cache_creation_input_tokens":0,
                "prompt_token_cost":1.5e-7,"completion_token_cost":6e-7,
                "cache_read_input_token_cost":7.5e-8,"cache_creation_input_token_cost":1.5e-7,
                "prompt_tokens_total_cost":1.5e-6,"completion_tokens_total_cost":3e-6,
                "cache_read_input_tokens_total_cost":0,"cache_creation_input_tokens_total_cost":0,
                "prompt_token_cost_unit":"USD","completion_token_cost_unit":"USD",
                "effective_date":"...","provider_id":"openai","pricing_level":"default|project"}}
    }
  },
  "wb_user_id": null, "wb_run_id": null, "deleted_at": null,
  "feedback": [ /* Feedback, only with include_feedback */ ],
  "storage_size_bytes": 123,         // only with include_storage_size
  "total_storage_size_bytes": 456,   // trace roots, only with include_total_storage_size
  "wb_username": "alice",            // only with include_usernames
  "wb_run_step": 5, "wb_run_step_end": 9,
  "expire_at": null                  // started_at + project retention_days, null without a TTL
}
```

The optional fields are described in [Optional call fields](#optional-call-fields).

**Usage roll-up.** Clients report only a call's *own* LLM usage in
`summary.usage` at call end. The server sums usage and `status_counts` from all
descendants, so `summary.usage` on a root call is the total for the trace.
A client whose summaries already include their descendants sends
`X-Weft-Summary: cumulative` with `/call/end` and `/call/upsert_batch`; those
summaries are stored as sent instead of being rolled up again.
`summary.weft` is computed on read (status, latency, trace and display name).
`summary.weft.costs` is present when `include_costs` is true; see
[Cached tokens](#cached-tokens) for how cache reads and writes are priced.

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
| `/calls/score` | `{project_id, call_ids, scorer_refs}` (server-runnable scorers) | `{"results": [{"call_id","scorer","output","scorer_call_id","feedback_id","error"?}]}`; each result is also stored as `weft.scorer.<scorer>` feedback on the call |
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

Gettable call fields: `id, op_name, display_name, trace_id, parent_id, thread_id, turn_id, started_at, ended_at, exception, wb_user_id, wb_run_id, deleted_at`, any dotted path below `inputs.`, `output.`, `attributes.`, `summary.`, plus the computed `summary.weft.status`, `summary.weft.latency_ms`, `summary.weft.trace_name`, `summary.total_tokens`, and feedback fields `feedback.<feedback_type>.<payload path>` (for example `feedback.weft.reaction.emoji`, `feedback.weft.scorer.my_judge.output.score`).
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
Monitor, AnnotationSpec, SavedView, Provider, ProviderModel, Op, Dashboard`. Use the more
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
| `/files/resolve_url` | `{project_id, uris, expires_in?, per_uri_errors?}` | `{"download_urls", "expires_at"}` (`errors` too with `per_uri_errors`); see [Files and media](#files-and-media) |

## Feedback

Feedback schema:
`{"id","project_id","ref","feedback_type","payload","creator","created_at","wb_user_id","annotation_ref","runnable_ref","call_ref","trigger_ref"}`

Feedback types Weft gives meaning to (the `weft.` prefix is reserved for them):
- `weft.reaction` payload `{"emoji": "👍"}`
- `weft.note` payload `{"note": "text"}`
- `weft.annotation.<spec name>` payload `{"value": ...}`, `annotation_ref` = AnnotationSpec ref
- `weft.scorer.<scorer name>`: a scorer's result on one call, payload `{"output": <score>}`, `runnable_ref` = scorer ref, `call_ref` = the scorer's own call, `trigger_ref` = monitor ref when a monitor produced it
- `weft.score.<scorer>`: a scoped score (session, agent, hand-off, turn, span; see [Scores](#scores))
- `weft.signal`: an agent signal result on a turn (scorer fields in the payload, see [Agents](#agents))
- `weft.user_feedback`: feedback on an agent turn given through `/agents/feedback/batch/create` without a type of its own
- anything else: free-form

`ref` targets the scored thing, usually `weft:///e/p/call/<id>`.

Databases written by earlier versions are migrated when the server opens
them: the `ref` column and the types above get these names, and queries saved
in views, dashboards, monitors and automations with the earlier names keep
working.

| Endpoint | Body | Response |
|---|---|---|
| `/feedback/create` | `{project_id, ref, feedback_type, payload, creator?, wb_user_id?, annotation_ref?, runnable_ref?, call_ref?, trigger_ref?}` | `{"id","created_at","wb_user_id","payload"}` |
| `/feedback/query` | `{project_id, query?, sort_by?, limit?, offset?}` | `{"result": [Feedback]}` |
| `/feedback/replace` | create body plus `feedback_id` | same as create (new id) |
| `/feedback/purge` | `{project_id, query}` (typically `$eq id`) | `{}` |

Feedback query fields: `id, ref, feedback_type, created_at, creator, wb_user_id, annotation_ref, runnable_ref, call_ref, trigger_ref, payload.<path>`.

## Costs

Built-in prices cover common OpenAI, Anthropic, Google, Mistral and DeepSeek
models, including the providers' published cache-read and cache-write prices
(`store/costs.go` `DefaultPrices` lists the sources). Project prices override
them.

| Endpoint | Body | Response |
|---|---|---|
| `/cost/create` | `{project_id, costs: {"<llm_id>": {prompt_token_cost, completion_token_cost, cache_read_input_token_cost?, cache_creation_input_token_cost?, prompt_token_cost_unit?, completion_token_cost_unit?, effective_date?, provider_id?}}}` (per-token USD) | `{"ids": [[id, llm_id]]}` |
| `/cost/query` | `{project_id, query?, limit?, offset?, include_defaults?}` | `{"results": [{"id","llm_id","prompt_token_cost","completion_token_cost","cache_read_input_token_cost","cache_creation_input_token_cost","prompt_token_cost_unit","completion_token_cost_unit","effective_date","provider_id","pricing_level"}]}` |
| `/cost/purge` | `{project_id, ids?: [], query?}` | `{}` |

`query` fields: `id, llm_id, provider_id, prompt_token_cost,
completion_token_cost, cache_read_input_token_cost,
cache_creation_input_token_cost, prompt_token_cost_unit,
completion_token_cost_unit, effective_date, pricing_level, pricing_level_id`
(the project id).

### Cached tokens

In `summary.usage`,
`prompt_tokens` (or `input_tokens`) is the **gross** prompt count, and
`cache_read_input_tokens` and `cache_creation_input_tokens` are the parts of
it read from and written to a provider cache. A model's cost is:

```
(prompt − cache_read − cache_creation) × prompt_token_cost
  + cache_read × cache_read_input_token_cost
  + cache_creation × cache_creation_input_token_cost
  + completion × completion_token_cost
```

Example (the audit's): `claude-sonnet-4-5`, 10,000 input tokens of which
9,000 are cache reads, 100 output → 1,000 × $3/M + 9,000 × $0.30/M + 100 ×
$15/M = **$0.0072** (it was $0.0315 before cache pricing).

The server normalises each call's own usage entry before summing, so these
provider shapes all price correctly:

| Source | Usage shape | Normalised to |
|---|---|---|
| OpenAI Chat Completions | `prompt_tokens` (includes cached), `prompt_tokens_details.cached_tokens` | `cache_read_input_tokens` = `cached_tokens` |
| OpenAI Responses | `input_tokens`, `input_tokens_details.cached_tokens` | same |
| OpenAI-compatible routers | `prompt_tokens_details.cache_write_tokens` | `cache_creation_input_tokens` |
| Anthropic (native) | `input_tokens` **excludes** `cache_read_input_tokens` and `cache_creation_input_tokens` | Weft's SDK integrations and harness plugins send the gross count. When an entry's cache counts exceed its prompt count, the server adds them to the prompt; otherwise the entry is taken as gross |
| Gemini / Vertex | `prompt_token_count` (includes cached), `candidates_token_count`, `total_token_count`, `cached_content_token_count`, `thoughts_token_count` (snake or camel case) | `prompt_tokens`, `completion_tokens`, `total_tokens`, `cache_read_input_tokens`, `reasoning_tokens` |
| Bedrock Converse | `inputTokens` (excludes cache), `cacheReadInputTokens`, `cacheWriteInputTokens` | gross `prompt_tokens` plus the cache fields |
| OTel GenAI semconv | `gen_ai.usage.input_tokens` (includes cache), `gen_ai.usage.cache_read.input_tokens`, `gen_ai.usage.cache_creation.input_tokens` | mapped by `/otel/v1/traces`; OpenInference `llm.token_count.prompt_details.cache_read/cache_write`, OpenLLMetry `gen_ai.usage.cache_read_input_tokens`, Vercel `ai.usage.cachedInputTokens` too |
| Langfuse `usage_details` | disjoint buckets: `input` excludes `input_cached_tokens` / `input_cache_read` / `input_cache_creation` | gross `prompt_tokens` plus the cache fields |

Existing canonical fields always win over derived ones. Weft's own LLM calls
(playground, judges, `evaluate_model`) record Anthropic usage in this form.

**Cost fields.** `cache_read_input_token_cost`,
`cache_creation_input_token_cost` (prices), `cache_read_input_tokens`,
`cache_creation_input_tokens` (counts) and
`cache_read_input_tokens_total_cost`, `cache_creation_input_tokens_total_cost`
(amounts, as in `/calls/usage`).
`prompt_tokens_total_cost` is the cost of **all** prompt tokens (uncached +
cache reads + cache writes); the two cache totals are the parts of it spent on
the cache. A call's cost is therefore always `prompt_tokens_total_cost +
completion_tokens_total_cost`; do not add the cache totals again.

**Missing cache prices.** An omitted (or `null`) cache price is stored as `null` and those
tokens are billed at `prompt_token_cost`, so a project price added without
cache rates never makes cached tokens free; send `0` explicitly for free
cache reads. Built-in prices for models whose provider does not price cache
reads or writes separately (OpenAI cache writes, Gemini cache writes, Mistral,
DeepSeek) are `null` and behave the same way. There is a single cache-write price, so
Anthropic's 1-hour cache writes (2× input) are priced at the built-in
5-minute rate (1.25×); set a project `cache_creation_input_token_cost` if you
mostly use 1-hour caching.

The same rates are used everywhere costs appear: `summary.weft.costs`,
`/calls/usage` and `/trace/usage`, `/calls/stats` (`cost_usd`, and the metric buckets'
`input_cost` / `output_cost` / `total_cost`, where `input_cost` includes the
cache costs), `/calls/metrics` (`cost`), `/threads/query`, `/calls/export`,
evaluation summaries and the Agents views (`input_cost_usd` includes the cache
costs, plus `cache_read_cost_usd`, `cache_creation_cost_usd` on spans). Roll-ups sum token counts per model and price
the sum once, so nested calls are never double counted.

## LLMs and playground

| Endpoint | Body | Response |
|---|---|---|
| `GET /llm/models` | | `{"models": [{"id","provider","label","available","max_tokens"}]}` (`available` = key configured) |
| `/completions/create` | `{project_id, inputs: {model, messages, temperature?, max_tokens?, top_p?, frequency_penalty?, presence_penalty?, stop?, n?, response_format?, tools?}, track_llm_call?: true}` | `{"response": OpenAIChatCompletion \| {"error": "..."}, "call_id": "..."}` |
| `/completions/create_stream` | same | `application/jsonl` of OpenAI chunk objects, last line `{"_meta":{"call_id": "..."}}` |
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
| `/evaluate_model` | `{project_id, evaluation_ref, model_ref, wb_user_id?}` | `{"call_id"}` — runs server side in the background; the model must be an `LLMStructuredCompletionModel`, have a `predict_url`, or be bound to a runtime (see [Server-side evaluation of any model](#server-side-evaluation-of-any-model)); scorers must be server-runnable |
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
`weft.scorer.<scorer name>` feedback with `trigger_ref` set to the monitor
ref, and the scorer's own call is recorded with `op_name = "<scorer name>.score"`.
Scorer calls and completions are never themselves monitored.

## OpenTelemetry ingest

`POST /otel/v1/traces` accepts OTLP/HTTP `ExportTraceServiceRequest` as
`application/x-protobuf` or `application/json`, gzip allowed. The project comes
from the `project_id` header (also `x-weft-project`), else the resource
attribute `weft.project`, else `default/default`.

Span → call mapping:

| Call field | Source (first match wins) |
|---|---|
| `id`, `trace_id`, `parent_id` | hex span id, trace id, parent span id |
| `op_name` | `weft.op_name`, else span name |
| `display_name` | `weft.display_name`; Traceloop `traceloop.entity.name` (or `traceloop.workflow.name` on workflow spans) |
| `started_at`, `ended_at` | span start/end |
| `inputs` | `weft.inputs` (JSON), OpenInference LLM spans' `llm.input_messages.*`, `input.value` (OpenInference; serialised LangChain/LangGraph messages become chat messages), framework layouts (see below), `gen_ai.input.messages`, `llm.input_messages.N.message.*`, `gen_ai.prompt.N.*` (OpenLLMetry), `gen_ai.prompt`, `traceloop.entity.input`, `gen_ai.*.message` span events. Request params (`gen_ai.request.*`, `llm.invocation_parameters`) are added. |
| `output` | `weft.output`, OpenInference LLM spans' `llm.output_messages.*`, `output.value`, framework layouts (see below), `gen_ai.output.messages`, `llm.output_messages.N.message.*`, `gen_ai.completion.N.*`, `gen_ai.completion`, `traceloop.entity.output`, `gen_ai.choice` events |
| `summary.usage` | model from `gen_ai.response.model` / `gen_ai.request.model` / `llm.model_name` / `ai.response.model` / `ai.model.id`; tokens from `gen_ai.usage.input_tokens\|prompt_tokens`, `gen_ai.usage.output_tokens\|completion_tokens`, `llm.token_count.*`, `ai.usage.promptTokens\|completionTokens`, `mlflow.chat.tokenUsage`, ADK `usage_metadata`; also `reasoning_tokens` (`gen_ai.usage.reasoning.output_tokens`), `cache_read_input_tokens` and `cache_creation_input_tokens` (`gen_ai.usage.cache_read\|cache_write.input_tokens`) |
| `exception` | span status ERROR message, or the `exception` event (`exception.type: exception.message`) |
| `thread_id`, `turn_id` | `weft.thread_id`, `gen_ai.conversation.id`, `session.id`, `gcp.vertex.agent.session_id`, `ai.telemetry.metadata.sessionId\|threadId\|conversationId`; `weft.turn_id`, or the span's own id when `weft.is_turn` is true |
| `wb_user_id` | `user.id`, `enduser.id`, `ai.telemetry.metadata.userId` |
| `attributes` | `weft.attributes.*` (prefix stripped), plus `otel: {span_name, kind, status, resource, scope, attributes, events, links}` |

The response is an OTLP `ExportTraceServiceResponse` in the request's encoding.
`POST /agents/otel/v1/traces` is the same endpoint.

Framework layouts (checked after `input.value` / `output.value`):

| Framework | Inputs | Output |
|---|---|---|
| MLflow | `mlflow.spanInputs` (JSON-encoded) | `mlflow.spanOutputs` |
| GenAI tool spans, Pydantic AI, ADK, Vercel tools | `gen_ai.tool.call.arguments`, `tool_arguments`, `gcp.vertex.agent.tool_call_args`, `ai.toolCall.args` (objects are spread into inputs, other values become `input`) | `gen_ai.tool.call.result`, `tool_response`, `gcp.vertex.agent.tool_response`, `ai.toolCall.result` |
| Pydantic AI / Logfire agent runs | `pydantic_ai.all_messages` (GenAI parts) or `all_messages_events` (v1), minus the final answer | `final_result`, else the trailing assistant messages |
| Vercel AI SDK | `ai.prompt.messages` (text, image, file, `tool-call` and `tool-result` parts become chat messages with `tool_calls` / `role: tool`), else `ai.prompt` (`{system, prompt\|messages}`); `ai.value(s)` for embeddings | `ai.response.text` + `ai.response.toolCalls` (+ `ai.response.finishReason`) as a chat completion, `ai.response.object`, `ai.embedding` |
| Google ADK / Vertex | `gcp.vertex.agent.llm_request` (`contents` with `function_call` / `function_response` / `inline_data` parts, `config.system_instruction`) | `gcp.vertex.agent.llm_response` |

GenAI message parts: `text` parts are joined; `tool_call` parts become OpenAI
`tool_calls`; a message of only `tool_call_response` parts becomes `role: tool`;
`reasoning` becomes `{"type":"thinking"}`; `blob` / `uri` image parts become
`{"type":"image_url","image_url":{"url": data URI or URI}}`, and other media
`{"type":"media","mime_type","modality","data": data URI or URI}`.

## Annotation queues

Queues collect calls for human review (Review queues in the UI). A queue's
`scorer_refs` are the `AnnotationSpec` refs reviewers fill in. Progress is
tracked per annotator (the API key's user, else `X-Weft-User`, else
`anonymous`).

AnnotationQueueSchema: `{"id","project_id","name","description","scorer_refs":[ref],"created_at","created_by","updated_at","deleted_at":null}`

Item: `{"id","project_id","queue_id","call_id","call_op_name","call_trace_id","call_started_at","call_ended_at","display_fields":[path],
"added_by","annotation_state":"unstarted|in_progress|completed|skipped","annotator_user_id","created_at","created_by","updated_at","deleted_at","position_in_queue"?}`
(`annotation_state` is the requesting annotator's state.)

| Endpoint | Body | Response |
|---|---|---|
| `POST /annotation_queues` | `{project_id, name, scorer_refs, description?}` | `{"id"}` |
| `POST /annotation_queues/query` | `{project_id, name?, limit?, offset?, sort_by?: [{field: created_at\|updated_at\|name, direction}]}` (`name`: case-insensitive substring) | NDJSON stream of AnnotationQueueSchema |
| `POST /annotation_queues/stats` | `{project_id, queue_ids}` | `{"stats": [{"queue_id","total_items","completed_items"}]}` — completed means completed or skipped by at least one annotator |
| `GET /annotation_queues/{queue_id}?project_id=` | | `{"queue"}` |
| `PUT /annotation_queues/{queue_id}` | `{project_id, name?, description?, scorer_refs?}` | `{"queue"}` |
| `DELETE /annotation_queues/{queue_id}?project_id=` | | `{"queue"}` (soft delete) |
| `POST /annotation_queues/{queue_id}/items` | `{project_id, call_ids, display_fields}` (`display_fields`: call paths shown to reviewers, e.g. `inputs.question`) | `{"added_count","duplicates"}` |
| `POST /annotation_queues/{queue_id}/items/query` | `{project_id, filter?: {id?, added_by?, annotation_states?, call_id?, call_op_name?, call_trace_id?}, sort_by?: [{field: created_at\|updated_at\|call_started_at\|call_ended_at\|call_op_name\|annotation_state, direction}], limit?, offset?, include_position?, annotator_user_id?}` (`annotator_user_id` is a Weft extension to view another reviewer's states) | `{"items": [Item]}` in queue order |
| `POST /annotation_queues/{queue_id}/items/{item_id}/progress` | `{project_id, queue_id, annotation_state}` | `{"item"}` |
| `DELETE /annotation_queues/{queue_id}/items/{item_id}?project_id=` | | `{}`; removes the item (soft delete; its feedback stays, and the call can be added again). 404 when it is already gone. The UI delays this call to offer undo. |

Feedback given while reviewing carries the queue: `/feedback/create`,
`/feedback/replace` and `/feedback/batch/create` accept `queue_id`, Feedback
rows return it, and `queue_id` is a feedback query field. The UI's "Export to
dataset" builds a Dataset with one row per annotated call: its inputs, `output`,
one column per spec (latest value given in the queue), `annotators` and `call_id`.

## Automations

An `Automation` object (base class `Automation`) sends a JSON webhook POST or
a Slack incoming-webhook message when its trigger fires. Store it with
`/obj/create`; the latest version of each enabled automation is active (changes
apply within 5 seconds).

```jsonc
{"_type":"Automation","_class_name":"Automation","_bases":["Object","BaseModel"],"name":"low-quality","description":"",
 "enabled": true,                       // default true
 "trigger": {"type":"score_threshold",  // a scorer result crosses a threshold
             "scorer":"judge",          // scorer name or ref (feedback weft.scorer.<name>), or a full feedback type such as weft.annotation.quality
             "path":"score",            // dotted path inside the score (payload.output, or payload.value for annotations); "" = the score itself; true/false count as 1/0
             "operator":"lt",           // gt gte lt lte eq ne (or > >= < <= == !=)
             "threshold": 3,
             "monitor_ref": null},      // optional: only scores written by this monitor
 // or {"type":"error_rate","op_names":["agent"],"window_minutes":60,"threshold":0.1,"min_calls":5}
 //    share of finished calls with an exception (op_names empty = trace roots; internal scorer/judge calls excluded); checked every minute
 // or {"type":"feedback_created","feedback_types":["weft.reaction","weft.note"]}   // exact types or prefixes ending in *; empty = any
 "action": {"type":"slack",             // or "webhook"
            "url":"https://hooks.slack.com/services/…",   // or "url_secret": name of a secret holding the URL
            "headers": {},              // webhook only
            "message": "{automation}: {summary}"},        // optional template; placeholders are {automation} {project_id} {summary} {test} and the event's keys
 "cooldown_seconds": 300}              // minimum gap between fires; default 300, or the window for error_rate
```

Score and feedback triggers are evaluated when feedback is created (by
monitors, `/calls/score`, the UI or the SDKs); error-rate triggers run in a
background loop. Slack receives `{"text": message}`. Webhooks receive:

```jsonc
{"automation": {"name","object_id","ref"}, "project_id", "trigger": {...}, "message", "fired_at", "test": false,
 "event": {"type":"score_threshold","feedback_id","feedback_type","ref","call_id","value","path","operator","threshold","monitor_ref"}
       // error_rate: {"type","total_calls","error_calls","error_rate","threshold","window_minutes","op_names"}
       // feedback_created: {"type","feedback_id","feedback_type","ref","call_id","payload","creator","wb_user_id","queue_id"}
}
```

AutomationRun: `{"id","project_id","automation_id","automation_ref","trigger_type","action_type","status":"success|error","test","event","response_status","response_body","error","duration_ms","created_at"}`

| Endpoint | Body | Response |
|---|---|---|
| `/automations/runs/query` | `{project_id, automation_id?, limit?, offset?}` | `{"runs": [AutomationRun]}` newest first |
| `/automations/test` | `{project_id, automation_id?, digest?, val?, event?}` — fire a saved automation or an unsaved `val` with a sample (or the given) event; ignores `enabled` and cooldowns | `{"run"}` |
| `/automations/check` | `{project_id}` | `{"runs"}` — evaluate error-rate triggers now |
| `/automations/validate` | `{val}` | `{"valid": true}` or 400 with the reason |

The server delivers to any http(s) URL an automation names, like `Provider`
objects: give API keys only to people you trust with that.

## More server-runnable scorers

These classes run in `/evaluate_model`, monitors and `/calls/score`, and
serialise exactly like the SDK classes of the same name. `{inputs}` in prompts
is all input columns as JSON; `{output}` is the output text.

```jsonc
// LLM judge presets: the stored scoring_prompt (the SDKs serialise it; these defaults apply when absent) runs like LLMAsAJudgeScorer in JSON mode
{"_class_name":"HallucinationFreeScorer", "_bases":["Scorer","Object","BaseModel"], "name":"h", "model":"gpt-4o-mini", "scoring_prompt":"…{inputs}…{output}…"}
    // → {"hallucination_free": bool, "reasoning"}
{"_class_name":"SummarizationScorer", … "model", "scoring_prompt"}   // → {"summarization_score": 0..1, "is_faithful": bool, "reasoning"}
{"_class_name":"RelevanceScorer", … "model", "scoring_prompt"}       // → {"is_relevant": bool, "relevance_score": 0..1, "reasoning"}
    // When the judge omits these keys (e.g. the offline mock/json model) they are derived from its "correct"/"score" fields.
{"_class_name":"ValidXMLScorer", …}                                  // → bool: one well-formed root element
{"_class_name":"JSONSchemaScorer", … "schema": {...}}                // → bool. Supports type, enum, const, properties, required,
    // additionalProperties, items, min/maxItems, min/maxLength, pattern, minimum/maximum, exclusiveMinimum/Maximum, allOf/anyOf/oneOf, not
{"_class_name":"OpenAIModerationScorer", …, "model"?: "omni-moderation-latest"}  // → {"flagged": bool, "categories": {name: true}}; OPENAI_API_KEY secret;
    // "model": "mock/moderation" flags a few words offline (Weft extension)
{"_class_name":"EmbeddingSimilarityScorer", … "target_column":"expected", "model":"text-embedding-3-small"}
    // → cosine similarity (number) of output and row[target_column] embeddings; any OpenAI-compatible provider, or mock/* offline
{"_class_name":"RemoteScorer", … "url":"https://…", "headers_secret":"MY_SCORER_HEADERS", "timeout": 30}
    // POSTs {"output","inputs"} and returns the JSON response (or its text). headers_secret names a secret holding a JSON
    // object of headers or a bare token (sent as Authorization: Bearer). endpoint_url and auth_config.bearer_secret_name are accepted as aliases;
    // auth_config with token_url uses OAuth client credentials (see "OAuth client credentials" below).
    // The url must be https unless the server runs with --remote-scorer-allow-insecure-http
    // (default off); an http url fails before any request is sent.
{"_class_name":"RuleScorer", … "rule": {"$contains": {"input": {"$getField":"output"}, "substr": {"$literal":"sorry"}}}, "threshold": null}
    // rule: a query-language operation ($literal $getField $and $or $not $eq $gt $gte $lt $lte $in $contains $convert; optionally
    // wrapped in $expr) evaluated on {output, inputs, <each input column>}; the value is the score; with threshold a number scores value >= threshold.
{"_class_name":"RuleScorer", … "rule":"latency_gt", "threshold": 10}
    // Named rules (used by the Signals gallery) → {"rule","flagged": bool,"value","reason"?}:
    // exception (call raised), latency_gt (seconds > threshold, default 10), empty_output, tool_error (a child call failed or a
    // tool-result message reports an error), invalid_json, pii_detected (email, ssn, credit_card (Luhn), phone, ip_address; value lists the kinds)
```

`exception`, `latency_gt` and `tool_error` read the scored call, so they need a
call (monitors, `/calls/score`); in evaluations they report `flagged: false`.

### Embeddings

| Endpoint | Body | Response |
|---|---|---|
| `/embeddings/create` | `{project_id, inputs: {model, input: string \| [string]}}` | `{"response": {"object":"list","data":[{"object":"embedding","index","embedding":[...]}],"model","usage"}}` |

Models route like completions (`openai/…`, `text-embedding-…`, custom
OpenAI-compatible `Provider` objects). `mock/*` models return deterministic
64-dimension bag-of-words vectors offline. Anthropic models have no embeddings
and return 400.

### Signals (UI)

Monitors → Signals creates a scorer and an active `Monitor` named
`signal-<id>` for common checks: hallucination, relevance, task completion,
refusal and user frustration (LLM judges), safety (moderation), PII, errors,
tool errors, slow calls, empty output and invalid JSON (`RuleScorer` named
rules). Pair one with an Automation (`score_threshold` on the signal's scorer)
to get alerts.

## Custom charts, dashboards and export

### `/calls/metrics`

Aggregates any call metric over time, optionally split by a group key. Used
by dashboards and the "Add chart to dashboard" builder.

```jsonc
// request
{"project_id": "e/p",
 "filter": CallsFilter, "query": {"$expr": ...}, "search": "...",   // as in CallsQuery
 "metrics": [{"name": "p95", "field": "latency_ms", "agg": "p95"},
             {"name": "calls", "agg": "count"},
             {"name": "score", "field": "feedback.weft.scorer.judge.output.score", "agg": "avg"}],
 "group_by": "model",            // optional
 "bucket_seconds": 3600,         // optional; ~60 buckets when absent
 "start": "...", "end": "...",   // optional; default to the span of matching calls
 "max_groups": 10}               // optional (default 10, max 50); the rest merge into one "other" group
// response
{"bucket_seconds": 3600, "start": "...", "end": "...",
 "buckets": ["2026-01-01T00:00:00Z", ...],
 "metrics": [{"name","field","agg"}],          // normalised (default names: "agg(field)" or "agg")
 "group_by": "model",
 "groups": [{"group": "gpt-4o" | null, "other"?: true, "count": 12,
             "series": {"p95": [12.5, null, ...]},   // one value per bucket
             "totals": {"p95": 30.1}}],
 "totals": {"p95": 28.0}, "count": 40, "truncated": false}
```

- **agg**: `count`, `sum`, `avg`, `min`, `max`, `p50`, `p90`, `p95`, `p99`,
  `error_rate`. `count` without a field counts calls; with a field it counts
  calls where the field is numeric. `error_rate` is the share of calls with an
  exception. Empty buckets are `0` for `count` and `sum` and `null` otherwise.
- **field**: `latency_ms`, `total_tokens`, `prompt_tokens`,
  `completion_tokens`, `cost` (USD, from the price book), or any numeric path
  accepted by the query language (`inputs.*`, `output.*`, `attributes.*`,
  `summary.*`, `feedback.<type>.<path>`). Booleans count as 0/1, numeric
  strings are parsed, anything else is skipped. Token and cost fields use each
  call's own usage, or the trace total when `filter.trace_roots_only` is set.
- **group_by**: `op_name`, `model` (the model with the most tokens in the
  call's usage; calls without usage group under `null`), `status`, `thread`,
  `user`, or any field path (`attributes.env`, `inputs.lang`, …).
- At most 20 metrics per request and 200,000 calls are read (`truncated`).

### Dashboard objects

Dashboards are versioned objects with class `Dashboard`; every save is a new
version.

```jsonc
{"_class_name": "Dashboard", "_bases": ["Object", "BaseModel"], "name": "Production health", "description": "",
 "time_range": "7d",            // 15m 1h 6h 24h 7d 30d 90d all, or "custom:<start ISO>/<end ISO>"
 "refresh_seconds": 0,
 "filter": {"trace_roots_only": true}, "query": null,   // applied to every panel
 "panels": [{"id": "p1", "title": "Latency by model",
             "chart": "line",    // line bar stacked area table stat
             "metrics": [{"agg": "p95", "field": "latency_ms"}],
             "group_by": "model",
             "filter": {...}, "query": {...}, "search": "...",   // combined with the dashboard's
             "time_range": "24h",                                // overrides the dashboard range
             "bucket_seconds": null, "width": 1}]}               // width in grid columns (1-3)
```

Panel filter keys override the dashboard's; list keys present on both are
intersected; queries are combined with `$and`.

### `/calls/export`

Streams every call matching a CallsQuery. Pages are read 500 at a time, so
memory stays flat for large exports; calls that start after the request are
excluded so paging stays stable.

```jsonc
{...CallsQuery,                  // filter, query, search, sort_by, limit, offset, include_feedback
 "format": "csv" | "jsonl" | "json",   // default jsonl
 "columns": ["id", "inputs.question", "output.answer"],   // optional projection
 "flatten": true}                // default true for csv, false otherwise
```

- `jsonl` / `json` without `columns` or `flatten` emit full Call objects
  (with costs) like `/calls/stream_query`; `json` is one array.
- CSV starts with `id, trace_id, parent_id, op_name, display_name, status,
  started_at, ended_at, latency_ms, prompt_tokens, completion_tokens,
  total_tokens, cost_usd, exception, thread_id, turn_id, wb_user_id,
  wb_run_id`. With `flatten` the server reads the result twice: once to collect
  every dotted `inputs.*`, `output.*` and `attributes.*` leaf (up to 1000
  columns; lists are JSON), then to write the rows. Without `flatten`, `inputs`,
  `output` and `attributes` are JSON columns.
- `columns` takes the computed names above or any dotted path into the call
  (keys containing dots are matched too); structured values are JSON.
- CSV cells that start with `=`, `+`, `-`, `@`, tab or CR and are not numbers
  get a leading `'` so spreadsheets do not run them as formulas.
- Responses carry `Content-Disposition: attachment; filename="<project>-calls-<time>.<ext>"`.
  Errors in the request return 400 before any data is written.

### Media values

The UI renders these input/output values inline (images, players, a PDF
viewer, a sandboxed iframe for HTML, rendered Markdown):

- `{"_type": "Image" | "Audio" | "Video" | "PDF" | "Html" | "Markdown" | "File", "file_digest", "mime_type", "filename"?}`
- `{"_type": "Content", "mimetype", "filename", "file_digest"}` or with
  `"data": "<base64>"` instead of `file_digest` (`mimetype` and `mime_type` are
  both accepted everywhere)
- `{"_type": "Markdown", "markup": "..."}` inline
- `data:<mime>;base64,...` strings
- custom media types stored through the compatibility adapter (see
  [COMPATIBILITY.md](COMPATIBILITY.md#limits))

HTML is shown with `sandbox=""` (no scripts, forms or same-origin access).

### Saved views for evaluations

The evaluations table stores its views as `SavedView` objects with
`"view_type": "evaluations"` and `definition: {"filter": {"evaluation", "model", "status"}, "search", "ui_params"}`.
The evaluation comparison page keeps its inline leaderboard (metrics, weights,
direction) in the URL (`?lb=`), so it can be shared or saved with the page link.

## Administration

### API keys

Besides the static `--api-keys`, admins can create managed keys. They are
stored as SHA-256 hashes (compared in constant time); the 40-character hex
secret is returned once, at creation. Authentication is required as soon as
static keys are configured **or** at least one managed key is active, so on an
open server the first key must be an admin key (creating it turns auth on).

A managed key has a `user_id` (the default `wb_user_id` of what it writes), a
scope (all projects, or a list of `entity/project`), and an access level:
read-write, `read_only`, or `admin` (all projects; manages keys, secrets and
the project settings below). Static keys count as admin.

- **Project scope.** The project is taken from the `project_id` query
  parameter, the `/v2/{entity}/{project}/` path, the `project_id` (or
  `X-Weft-Project`) header on `/otel/v1/traces` (required for scoped keys),
  `project_id` in the JSON body (top level, inside `start`/`end`/`obj`/`table`
  and in `batch` items), `project_ids`, and top-level ref values (`refs`,
  `*_ref`, `evaluation`, `model` …). Any project outside the scope, or a request
  naming none (except `/auth/*`, `/projects`, `/llm/models`,
  `/secrets/list`), gets 403. `GET /projects` lists only the key's projects.
- **Read-only.** `GET` requests and `POST` endpoints whose last path segment is
  a read (`query`, `read`, `stream_query`, `stats`, `query_stats`, `read_batch`,
  `content`, `compare`, `compute`, `status`, `usage`, `aggregate`,
  `payload_schema`, `export`, …) are allowed; other writes get 403.
- **Admin only:** `/secrets/set|delete`, `/project/delete`,
  `/project/*/update`, `/project/ttl_settings/purge`. Keys bound to a user
  (see [Users, teams and roles](#users-teams-and-roles)) take their access
  from the user's roles instead of these flags, and can manage their own keys.

| Endpoint | Body | Response |
|---|---|---|
| `/api_keys/create` | `{name, user_id?, projects?: [], read_only?, admin?}` | `{"key": ApiKey, "secret": "<40 hex>"}` |
| `GET /api_keys/list?include_revoked=true` (or POST `{include_revoked}`) | | `{"keys": [ApiKey], "static_key_count"}` |
| `/api_keys/revoke` | `{id}` | `{"key": ApiKey}`; the last admin key cannot be revoked while other managed keys are active and no static key exists |
| `GET /auth/principal` | | `{"user_id","admin","read_only","projects": [..]\|null,"key_id","key_name","auth_required"}` |

ApiKey: `{"id","name","user_id","prefix","all_projects","projects","read_only","admin","created_by","created_at","last_used_at","revoked_at"}`
(`last_used_at` is updated at most once a minute).

### Project settings

| Endpoint | Body | Response |
|---|---|---|
| `/project/ttl_settings/read` | `{project_id}` | `{"retention_days": int\|null}` (null = keep forever) |
| `/project/ttl_settings/update` | `{project_id, retention_days: null \| 1..36500}` | same |
| `/project/ttl_settings/purge` | `{project_id}` | `{"purged": {"calls","feedback"}, "retention_days"}` — apply now |
| `/project/ingest_sampling_settings/read` | `{project_id}` | `{"sample_rate": 0..1, "dry_run": bool}` (default 1, false) |
| `/project/ingest_sampling_settings/update` | `{project_id, sample_rate, dry_run?}` | same |
| `GET /project/sensitive_data_settings?project_id=` (or POST `.../read`) | | `{"policy": "off"\|"pii-v1", "entities": [], "custom_patterns": [{name, pattern}], "available_entities": [{name, placeholder, description, kind}], "ner": {enabled, score_threshold, language}, "ner_available", "ner_active", "ner_entities"}` |
| `/project/sensitive_data_settings/update` | `{project_id, policy, entities?, custom_patterns?, ner?: {enabled?, score_threshold?, language?}}` | same as read |
| `/project/sensitive_data_settings/preview` | `{policy, entities?, custom_patterns?, ner?, text?, value?}` | `{"text","value","ner_applied"}` redacted with the given, unsaved settings (NER included when the server has an analyzer) |
| `/project/delete` | `{project_id, confirm?}` (`confirm` must equal the project id when given) | `{"project_id", "deleted": {"<table>": rows}}` |

- **Retention.** A background job (hourly; `WEFT_RETENTION_INTERVAL`, a Go
  duration, overrides) hard-deletes calls that started before
  `now - retention_days`, and feedback on calls created before it. Objects,
  tables and files are versioned assets and are kept.
- **Ingest sampling** is applied to every ingest path (`/call/start|end`,
  `/call/upsert_batch`, OTLP). A trace is kept when
  `SHA-256(trace_id)` (first 8 bytes, as a fraction of 2^64) is below
  `sample_rate`, so a root and all of its children share the decision on any
  server. Ends for dropped calls are dropped too (dropped ids are remembered
  for 24 h). With `dry_run` nothing is dropped; would-be drops get
  `attributes.weft_ingest_sampling = {"would_drop": true, "sample_rate"}`.
- **Sensitive data.** With policy `pii-v1`, strings in call inputs,
  attributes (including raw OTLP attributes), output and exception are
  redacted before storage. Built-in entities (an empty `entities` list means
  all): `API_KEY` (OpenAI/Anthropic `sk-…`, Stripe, AWS `AKIA…`, GitHub, Slack,
  Google `AIza…`, JWTs, `Bearer …`), `EMAIL_ADDRESS`, `IBAN_CODE` (mod-97
  checked), `CREDIT_CARD` (Luhn checked), `US_SSN`, `PHONE_NUMBER`,
  `IP_ADDRESS`. Matches become `<ENTITY>`; custom RE2 patterns become
  `<NAME>` (upper-cased). Ref strings (`weft:///`) and map keys are
  never changed. Existing data is not rewritten.
- **NER entities (Presidio).** Start the server with
  `--pii-analyzer-url http://<presidio-analyzer>:5002` (env
  `WEFT_PII_ANALYZER_URL`; Presidio Analyzer's REST `POST /analyze`) and
  `pii-v1` projects also redact named entities: `PERSON`, `LOCATION` and
  `ORGANIZATION` by default, `NRP` (nationalities, religious or political
  groups) when listed in `entities`. They are listed in `available_entities`
  with `"kind": "ner"` (regex detectors have `"kind": "regex"`). An empty
  `entities` list means every regex detector plus the three default NER
  entities; a non-empty list selects among both kinds. Per project,
  `ner: {"enabled": false}` turns NER off (regex rules stay),
  `score_threshold` (0–1, default 0.5) drops low-confidence entities, and
  `language` (default `en`) is passed to the analyzer. `ner_active` in the
  read response says whether NER applies now (policy on, analyzer configured,
  enabled, at least one NER entity selected).
  - NER runs at ingest before the write transaction, on every ingest path
    (`/call/start|end`, `/call/upsert_batch`, OTLP). The strings of a request (inputs, attributes, output, exception;
    refs, numbers and short or lowercase-only Latin strings are skipped) are
    joined into chunks of up to 50,000 characters, one analyzer request per
    chunk, and each entity becomes `<ENTITY>`. The regex rules then run as
    before.
  - The analyzer has a short time budget per ingest request
    (`--pii-analyzer-timeout`, env `WEFT_PII_ANALYZER_TIMEOUT`, default
    `2s`). On a timeout, error or bad response the call is stored with regex
    redaction only, a warning is logged (at most once a minute), and the
    analyzer is skipped for 30 s so a down analyzer does not slow ingest.
    Names can therefore be stored unredacted while the analyzer is
    unavailable.
  - Run the analyzer with e.g. `docker run -p 5002:3000
    mcr.microsoft.com/presidio-analyzer` and keep it on a private network:
    every redacted string is sent to it.
- **Project delete** removes the project's rows from every table with a
  `project_id` column, including settings, links and tags.

### Tags and aliases

| Endpoint | Body | Response |
|---|---|---|
| `GET /tags?project_id=` | | `{"tags": [..]}` distinct tags on live versions |
| `GET /aliases?project_id=` | | `{"aliases": [..]}` distinct aliases (`latest` is virtual and not listed) |
| `PUT /objs/{object_id}/aliases` (POST also accepted) | `{project_id, digest, aliases}` | `{}`; each alias moves to `digest` |
| `/objs/{object_id}/aliases/remove` | `{project_id, aliases}` | `{}` |
| `PUT /objs/{object_id}/versions/{digest}/tags` (POST also accepted) | `{project_id, tags}` | `{}`; `digest` may be `latest`, `v<N>` or an alias |
| `/objs/{object_id}/versions/{digest}/tags/remove` | `{project_id, tags}` | `{}` |

ObjSchema carries `tags` (always included, so `include_tags_and_aliases` is
honoured). Tags match `[A-Za-z0-9_.:/ -]{1,128}`.

### More trace endpoints

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

### v2 REST (`/v2/{entity}/{project}/…`)

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
| `GET /predictions/{id}`, `GET /predictions?evaluation_run_id` | `{prediction_id, model, inputs, output, evaluation_run_id, wb_user_id}` (list is JSONL) |
| `POST /predictions/{id}/finish` | ends the `predict_and_score` call with `{output, scores: {<scorer>: value}, model_latency}` from its score children → `{success}` |
| `POST /scores` `{prediction_id, scorer, value, evaluation_run_id?}` | a finished `<scorer>.score` child of the prediction (inputs `self` = scorer, `output`, the prediction inputs) plus `weft.scorer.<scorer>` feedback on the prediction → `{score_id}`; `scorer` may be a ref or a plain name |
| `GET /scores/{id}`, `GET /scores?evaluation_run_id` | `{score_id, scorer, value, evaluation_run_id, wb_user_id}` |
| `DELETE /predictions?prediction_ids=`, `DELETE /scores?score_ids=` | soft delete → `{num_deleted}` |
| `POST /eval_results/query` | `{evaluation_call_ids \| evaluation_run_ids, include_rows?, include_summary?, include_raw_data_rows?, include_predict_and_score_children?, require_intersection?, sort_by?: [{field: "scores.<name>" \| "inputs.<path>" \| "outputs.<path>", direction, evaluation_call_id?}], limit?, offset?}` → `{rows: [{row_digest, raw_data_row, evaluations: [{evaluation_call_id, trials: [{predict_and_score_call_id, predict_call_id, scorer_call_ids, model_output, model_latency_seconds, scores, total_tokens}]}]}], summary: {row_count, evaluations: [{evaluation_call_id, display_name, evaluation_ref, model_ref, started_at, trial_count, predict_total_tokens, scorer_stats: [{scorer_key, path, value_type: binary\|continuous\|text, trial_count, pass_true_count, pass_known_count, pass_rate, pass_signal_coverage, numeric_count, numeric_mean}]}]}, total_rows, warnings}`. `include_*` default to true. `filters` are not supported and produce a warning; costs are not computed. |

## Agents

The Agents API (`/agents/*`). Agents, versions,
conversations, turns and spans are derived from calls: every call (SDK call
or OTLP span) gets a row in a span index written in the same transaction.
Fields marked *Weft* are extensions.

**Span kind** (`operation_name`), first match wins: `gen_ai.operation.name`;
`weft.span_kind` (flat OTLP attribute, or `attributes.weft.span_kind` set by
the Weft SDKs), `openinference.span.kind`, `traceloop.span.kind`, `mlflow.spanType`;
Vercel `ai.operationId` / `operation.name` (`*.doGenerate|doStream` → `chat`,
`ai.toolCall` → `execute_tool`, `ai.embed*` → `embeddings`, `ai.generateText`
etc. → `chain`); ADK `gcp.vertex.agent.llm_request` → `chat`; `llm.request.type`;
span name prefixes (`invoke_agent`, `agent run`, `chat `, `call_llm`,
`execute_tool`, `running tool`); SDK LLM ops (`openai.chat.completions.create`,
`anthropic.Messages.create`, …), or an SDK call with chat `messages` and usage.
Kind vocabularies map onto `invoke_agent` (agent, AGENT, subagent, turn),
`chat` (llm, LLM, CHAT_MODEL), `execute_tool` (tool, TOOL, function),
`embeddings`, `retrieval` (retriever, search), `chain` (CHAIN, workflow, task).

| Field | Source |
|---|---|
| agent name | `gen_ai.agent.name`, `weft.agent.name`, `agent.name` (OpenInference), `agent_name` (Pydantic AI), `traceloop.entity.name` on Traceloop agent spans; for `invoke_agent` spans without one, the span name after `invoke_agent `, else the display name or op name |
| agent id / version / description | `gen_ai.agent.id\|version\|description` (`weft.agent.*` also accepted) |
| conversation | the call's `thread_id` (OTLP: `gen_ai.conversation.id`, `session.id`, …), `gen_ai.conversation.name` |
| turn | `turn_id == id`, or `weft.is_turn` |
| tool | `gen_ai.tool.name`, `tool.name`, `ai.toolCall.name`; else the `execute_tool` span name suffix, display name or op name. `gen_ai.tool.call.id`, `ai.toolCall.id` |
| model, provider | `gen_ai.request.model`, `ai.model.id`, `llm.model_name`, `model_name`, SDK `inputs.model`, else the single model in usage; `gen_ai.provider.name`, `gen_ai.system`, `ai.model.provider` (`openai.chat` → `openai`), `llm.provider`, SDK op prefix |

Agent and conversation identity are inherited from the nearest ancestor that
sets them, also when children arrive first. Spans with no agent, conversation
or kind are left out of agent queries. An **invocation** is an `invoke_agent`
span that names its agent, or the top-most span of an agent's run when the
agent never emits `invoke_agent`. Usage and cost are each span's own (not
rolled up), so sums never double count. Deleted calls drop out. Custom
attributes are span attributes outside the reserved conventions (`gen_ai.*`,
`weft.*`, `llm.*`, `input.*`, `output.*`, `ai.*`,
`mlflow.*`, `traceloop.*`, `openinference.*`, `gcp.vertex.agent.*`, …) plus
the call's own attributes; nested values flatten to dotted keys and the
OpenInference `metadata` JSON flattens under `metadata.`.

| Endpoint | Body | Response |
|---|---|---|
| `/agents/query` | `{project_id, filters?: {agent_name? (case-insensitive substring)}, include_costs?, limit? (100), offset?, sort_by?}`; *Weft*: `started_after?`, `started_before?` | `{"agents": [Agent], "total_count"}` |
| `/agents/agent-versions/query` | `{project_id, agent_name, include_costs?, limit?, offset?, sort_by?}` | `{"versions": [Agent + agent_version], "total_count"}`; spans without a version are the version `""` |
| `/agents/spans/query` | `{project_id, query?, started_after?, started_before?, group_by?, measures?, group_filters?, group_distributions?, custom_attr_columns?, signal_filters?, insight_filters?, include_costs?, include_details?, sort_by?, limit? (100), offset?}` | `{"spans": [Span], "groups": [Group], "total_count"}`; `groups` when `group_by` is set |
| `/agents/spans/stats` | `{project_id, start, end?, granularity? (seconds; ~60 buckets when absent), timezone?, query?, group_by?, group_limit? (50), group_filters?, signal_filters?, insight_filters?, metrics?, bucket_by?}` | `{"bucket_type","start","end","granularity","timezone","columns","rows"}` |
| `/agents/spans/custom-attrs/schema` | `{project_id, query?, started_after?, started_before?, limit? (200), offset?}` | `{"attributes": [{"key","source","value_type","span_count"}], "has_more", "limit", "offset"}` |
| `/agents/search` | `{project_id, query?, roles?, agent_name?, conversation_id?, trace_id?, request_model?, provider_name?, started_after?, started_before?, truncate_content? (true), limit? (20), offset?}` | `{"results": [{"conversation_id","conversation_name","agent_name","last_activity","matched_messages": [{"content_digest","content_preview","role","span_id","trace_id","started_at"}]}], "total_conversations"}` |
| `/agents/conversations/spans` | `{project_id, conversation_ids, started_after?, started_before?}` | `{"conversations": [{"conversation_id","spans": [{"span_id","trace_id","operation_name","status","duration_ms"; Weft: "span_name","started_at"}], "spans_feedback": [...]}]}` |
| `/agents/conversations/chat` | `{project_id, conversation_id, include_feedback?, include_model_tool_calls?, limit? (50), offset?}` | `{"conversation_id","turns": [TraceChat],"total_turns","has_more","limit","offset","total_cost_usd","feedback"}`; *Weft*: `conversation_name, agent_names, started_at, last_activity, total_input_tokens, total_output_tokens, span_count, error_count` |
| `/agents/traces/chat` | `{project_id, trace_id, include_feedback?, include_model_tool_calls?}` | TraceChat (404 for an unknown trace) |
| `/agents/otel/v1/traces` | OTLP | same as `/otel/v1/traces` |
| *Weft*: `/agents/signals/presets` (GET or POST) | — | `{"presets": [{"id","name","kind": "tag"\|"rating","description"}]}` |
| *Weft*: `/agents/signals/run` | `{project_id, model, signals: [{preset} \| {id, name?, kind, description}] (1–12), agent_name?, conversation_id?, trace_ids?, started_after?, started_before?, limit? (20 most recent turns, max 200), rescore?}` | `{"turns","scored","skipped","failed","results": [{"span_id","trace_id","signal","kind","score"?,"matched"?,"confidence"?,"reason"?,"feedback_id"?,"error"?}]}` |
| *Weft*: `/agents/signals/query` | `{project_id, agent_name?, conversation_id?, trace_ids?, signal?, kind?, tag?, min_score?, max_score?, matched_only? (true), started_after?, started_before?, limit? (100), offset?}` | `{"results": [SignalResult], "summary": [{"signal","signal_name","kind","count","matched","avg","tag_counts"}], "total_count"}` |
| *Weft*: `/agents/feedback/batch/create` (same as `/feedback/batch/create`) | `{batch: [{project_id, ref, feedback_type, payload, scorer_ratings?, scorer_rating_confidences?, scorer_rating_reasons?, scorer_tags?, scorer_tag_confidences?, scorer_tag_reasons?, scorer_trace_id?, span_*?, ...}]}` | `{"res": [{"id","created_at","wb_user_id","payload"}]}` |

**Signals.** A signal scores one *turn* (a conversation turn, or an agent
invocation at the top of its trace outside conversations) with an LLM judge
(`model`; `mock/json` works offline). Tag signals ask for `{matched,
confidence, reason}`, rating signals for `{score, confidence, reason}`;
1–5, 1–10 and 0–100 scales are rescaled to 0–1. Each result is feedback on
the turn's root call with `feedback_type` `weft.signal` and payload
`{signal, signal_name, signal_kind, model, scorer_call_id, output, reason,
scorer_ratings | scorer_tags, scorer_*_confidences, scorer_*_reasons,
span_trace_id, span_conversation_id, span_agent_name, span_agent_version,
span_status_code, scorer_trace_id, signal_version}`. `signal_version` hashes the
kind and description; turns already scored by the same signal version are
skipped unless `rescore`, so editing a signal's description scores turns
again. **Automatic signals:** the project's `agent-signals` object
(`{model, auto? (true), signals: [{id, preset?, name, kind, description,
enabled, sample_rate? (0–1, default 1)}]}`, edited in the Signals tab) scores
every agent turn as its root call ends, through the same worker as monitors:
enabled signals only, each at its sample rate, never internal calls, once
per turn and signal version (creator `weft-signals-auto`). It needs `model`. The judge's own calls are internal (`attributes.weft.internal`)
and are not agent spans. `/agents/feedback/batch/create` normalises:
tags are lowercased, trimmed and de-duplicated (at most 10; a tag over
36 characters rejects the item), reasons lose control and markup characters
and are cut to 256 characters, confidences are clamped to 0–1; missing
`span_*` fields are filled from the span index. Chat views
(`feedback[]` in `/agents/conversations/chat`, `/agents/traces/chat`) lift the
`scorer_*` fields back to the top level, and `spans_feedback` ratings carry
`confidence` and `reason`. **SignalResult**: `{feedback_id, feedback_type,
kind, signal, signal_id, signal_name, score, tags, confidence, reason,
creator, created_at, span_id, trace_id, conversation_id, conversation_name,
agent_name, agent_version, turn_started_at, turn_status, last_message,
scorer_call_id}`; ratings and tags from annotations, `weft.scorer.*`
scorers and reactions on turns are listed too. Results are newest turn first.

**`signal_filters`** `{ratings?: [{scorer_key, op: gte|gt|lte|lt|eq, value}],
tags?: [...]}` keeps spans whose turn (trace) has signal results where every
rating condition holds and, when tags are given, at least one tag matches
(case-insensitive). **`insight_filters`** `[{field, values, exclude?}]`
(fields `intent_category, intent_sentiment, failure_category,
failure_severity, intent_topic_id, failure_topic_id`; values ORed, filters
ANDed) are per conversation (per trace outside conversations). Weft has no
Insights pipeline: values come from an `insights` map in turn feedback
payloads, and `intent_sentiment` is also derived from `sentiment` /
`user_satisfaction` ratings (<0.2 frustrated, <0.4 dissatisfied, <0.6
neutral, <0.8 satisfied, else delighted) and the `user_frustration` tag.
Unknown fields, ops or sentiment/severity values are a 400.

**Agent** fields: `agent_name, project_id, invocation_count, span_count,
error_count` (failed spans), `first_seen, last_seen, total_duration_ms`
(sum of invocation durations), `total_input_tokens, total_output_tokens,
total_cost_usd` (null unless `include_costs`). *Weft*: `version_count,
versions, conversation_count, invocation_error_count, latency_ms_p50,
latency_ms_p95` (of invocations), `llm_call_count, tool_call_count, models`.
Sort fields: those plus `conversation_count, version_count, latency_ms_p50,
latency_ms_p95`; default `last_seen desc`.

**Span fields** usable in `query` (`$getField`), `group_by`, measures,
metrics and `sort_by` (field source `field`, `column` or `derived`):
`span_id, trace_id, parent_span_id, operation_name, span_name, display_name,
agent_name, agent_id, agent_version, agent_description, conversation_id,
conversation_name, tool_name, tool_call_id, request_model, response_model,
provider_name, status_code` (`OK|ERROR|UNSET`), `status_message, error_type,
started_at, ended_at, duration_ms, input_tokens, output_tokens, total_tokens,
reasoning_tokens, cache_read_input_tokens, cache_creation_input_tokens,
input_cost_usd, output_cost_usd, total_cost_usd, cache_read_cost_usd,
cache_creation_cost_usd, wb_user_id, wb_run_id, parent_call_id,
parent_call_trace_id` (the op a span was emitted from when it started its own
trace inside one: the `weft.parent_call.id` / `weft.parent_call.trace_id`
attributes the Weft SDKs stamp, from SDK or OTLP spans; so
"which agent spans did this call produce" is
`{"$eq": [{"$getField": "parent_call_id"}, {"$literal": "<call id>"}]}`), the
booleans `is_turn, is_error, is_invocation, is_agent, is_llm, is_tool`, and
custom attributes as `custom_attrs_string|int|float|bool.<key>` (or source
`custom_attrs_*` with the bare key). Dotted aliases such as `agent.name`,
`gen_ai.agent.name`, `conversation.id`, `tool.name`, `request.model` work too.
The query language is the calls one (`$and $or $not $eq $gt $gte $lt $lte $in
$contains $convert`); timestamps compare chronologically. Unknown fields in
`group_by` and `sort_by` are a 400.

**Span** (`spans[]`). `input_messages`,
`output_messages` (`{role, content, finish_reason}`; content is text, or a
JSON parts array for structured messages), `system_instructions`,
`finish_reasons`, `reasoning_content`, `tool_call_arguments`,
`tool_call_result`, `tool_definitions` and request parameters are filled only
with `include_details`; costs only with `include_costs` (`input_cost_usd`
covers all input tokens; `cache_read_cost_usd` and `cache_creation_cost_usd`
are the parts of it spent on the cache). `custom_attrs_*` hold
all custom attributes, or only `custom_attr_columns` keys when given.
*Weft*: `duration_ms, is_turn, display_name`.

**Group** (`groups[]`): `group_keys` (alias → value), `span_count,
invocation_count, error_count, conversation_count, conversation_names,
agent_names, agent_versions, provider_names, request_models, first_seen,
last_seen, total_input_tokens, total_output_tokens, total_reasoning_tokens,
total_cache_read_input_tokens, total_cache_creation_input_tokens,
total_duration_ms` (sum over the group's top-level spans, whose parent is not
in the group), `total_cost_usd, total_input_cost_usd, total_output_cost_usd,
first_message` / `last_message` (`{role: user_message|assistant_message,
text}`, previews up to 200 characters), `metrics` (measure alias → value) and
`distributions` (per `group_distributions`: `values` top-N for strings,
`bins` for numbers). *Weft*: `latency_ms_p50, latency_ms_p95` (top-level
spans), `trace_count`. Measures: `{alias, aggregation: sum|avg|min|max|count|
count_distinct|count_true|count_false, value?, filter?}`; `group_filters`
keep groups whose measure is within `[min, max]`. Group sort fields: group key
aliases, measure aliases and the bundle fields; default `last_seen desc`.

**Stats rows.** Time buckets (`bucket_by` absent or `{type: "time"}`): each
row has `timestamp` (bucket start, RFC 3339 in `timezone`), the group-by
aliases, `count` (matching spans) and, per metric, `<alias>_<aggregation>`
and `<alias>_p<percentile>` (for example `latency_p95`). Without `group_by`,
empty buckets are included. Numeric buckets (`{type: "number", alias, bins
(24, max 200), min?, max?, value | (group_by + measure)}`) bucket a span
value, or a per-group measure, into equal-width bins with `<alias>_min`,
`<alias>_max` and `bucket_index`. `columns` describes every column with its
`role` (`time|bucket|group|metric`).

**TraceChat** (`/agents/traces/chat` and each conversation turn):
`messages` is the trajectory in tree order
(children by start time):

- `user_message`: the last user message of the turn root's inputs, else of its
  first LLM call, else the root's readable inputs (`question`, `query`,
  `input`, … or JSON).
- `assistant_message` for each LLM span with text, reasoning or requested
  tools (`text` may be empty; *Weft* `requested_tools` names them), with
  model, duration, own tokens and cost; plus the final answer of the turn or a
  sub-agent when no LLM span already said it.
- `tool_call` for each tool span (`tool_name, tool_arguments, tool_result,
  duration_ms, status`). With `include_model_tool_calls`, tool calls a model
  requested that have no execution span (matched by tool call id) are added
  without status, duration or result.
- `agent_start` for nested `invoke_agent` spans (sub-agents), with model,
  system instructions and tool definitions.

Every message has `span_id, started_at, status_code, status_message,
error_type, agent_name, agent_version` and, with `include_feedback`, the
span's feedback; *Weft*: `operation_name, span_name, media` (image and audio
parts). Totals cover the turn's spans; `total_duration_ms` is the root's
wall-clock duration. *Weft*: `root_span_id, span_count, llm_call_count,
tool_call_count`.

**Turns of a conversation**: spans flagged as turns if there are any, else the
conversation's root spans (each usually the root of its own trace), in start
order. `offset` skips the most recent turns and each page is chronological.
Feedback markers in `/agents/conversations/spans` come from feedback on turn
roots: scorer feedback (`weft.scorer.*`, numeric or boolean output, or the
numeric fields of an output object) becomes `weft.signal` ratings;
numeric annotations become `weft.user_feedback` ratings, and
reactions and string annotations become its tags.

Not implemented: `signal_filters` and `insight_filters` are accepted and
ignored; `agent_handoff` and `context_compacted` messages are never produced;
`content_refs`, `artifact_refs` and `object_refs` are always empty. Queries
aggregate up to 200,000 spans in memory.

## More endpoints

Custom runtimes, server-side evaluation of any model, files and media, image
generation, alternative response shapes, the registry, the model catalog and
geolocation.

### Custom runtimes

`PUT /v2/{entity}/{project}/runtimes/{runtime_name}` (the SDKs'
`client.register_custom_runtime`) creates or replaces a runtime:

```jsonc
// body
{"base_url": "https://vllm.internal/v1",       // OpenAI-compatible base URL
 "api_key_secret": "VLLM_KEY",                  // name of a secret (Settings → Secrets; never read from the server environment); sent as a bearer token
 "headers": {"X-Team": "ml"},                    // literal headers
 "runtime_ids": [{"id": "llama-3-8b", "max_tokens": 8192}, {"id": "qwen2-7b"}],  // complete list; max_tokens defaults to 4096
 "kind": "openai"}                               // Weft: "openai" (default) or "predict"
// response
{"name", "base_url", "api_key_secret", "headers",
 "runtime_ids": [{"id", "max_tokens", "playground_id": "<runtime>/<id>"}],
 "kind", "ref"}                                  // kind and ref are Weft additions
```

The name must match the runtime name pattern (no whitespace; `:` only between
segments) and must not be a built-in provider prefix (`openai`, `anthropic`,
`mock`, ...). Weft stores the runtime as a `Provider` object (object id = the
name with characters outside `[A-Za-z0-9_.-]` replaced by `-`) and one
`ProviderModel` per runtime ID; IDs omitted on a later PUT are deleted. So
`<runtime>/<id>` works everywhere a model id does: the playground,
`/completions/create`, `/image/create`, LLM judges, `LLMStructuredCompletionModel`
and `GET /llm/models?project_id=`. Updates apply immediately. *Weft*: `GET /v2/{entity}/{project}/runtimes` → `{"runtimes": [...]}`.

A `"kind": "predict"` runtime is an HTTP endpoint that serves a model's
predict (below); it has no `ProviderModel` objects.

### Server-side evaluation of any model

`/evaluate_model` runs these models on the server, recording one
`<ModelClass>.predict` call per row (inputs `self` = model ref plus the row's
columns) under each `Evaluation.predict_and_score`:

| Model value | How it is predicted |
|---|---|
| `LLMStructuredCompletionModel` | chat completion of `llm_model_id` (built-in, Provider or runtime model id) |
| any class with `predict_url` | `POST predict_url` (see protocol). Optional: `predict_headers` (literal), `predict_api_key_secret` or `auth_config.bearer_secret_name` (secret sent as `Authorization: Bearer`), `auth_config` with `token_url` (OAuth client credentials, see [below](#oauth-client-credentials-remotescorer-predict_url)), `predict_timeout` (seconds, default 120) |
| any class with `runtime` = a `predict` runtime (name or ref) | `POST` to the runtime's `base_url` with its headers and `api_key_secret`; `runtime_id` (or the runtime's first ID) is sent too |
| any class with `runtime` = an OpenAI-compatible runtime | chat completion of `<runtime>/<runtime_id>` (default: the first ID). Messages come from `default_params.messages_template` if set, else `system_prompt` and `prompt_template` (`{column}` placeholders), else the row's `input`/`question`/`prompt`/`query`/`text` column; `temperature`, `max_tokens`, `top_p`, `response_format` attributes are passed on |

Other models are rejected with 400, as before. A Python `weft.Model`
subclass works as long as its published attributes include `predict_url` or
`runtime`; its own `predict` op is not executed by the server.

Predict protocol:

```jsonc
// request: POST <url>, Content-Type: application/json
{"inputs": {"question": "paris", "expected": "PARIS"},   // the dataset row
 "model_ref": "weft:///acme/app/object/my-model:…", "project_id": "acme/app",
 "runtime_id": "upper-v2"}                                // predict runtimes only
// response: any JSON. {"output": X, ...} predicts X; any other JSON value or
// text body is the prediction. An OpenAI-style "usage" (with "model") is
// recorded as the predict call's token usage. Non-2xx fails the row.
```

### Files and media

`POST /files/resolve_url` `{project_id, uris, expires_in?}` →
`{"download_urls": [...], "expires_at": "<RFC 3339>" | null}`, one URL per uri:

- a file digest from `/file/create` (also `weft-file://<digest>`): a presigned URL when the file is in the S3 backend,
  otherwise this server's `GET /file/content?project_id=..&digest=..` URL
  (built from the request host, honouring `X-Forwarded-Proto/Host`);
- `s3://<backend bucket>/<key>` where the key is under this project's prefix:
  a presigned URL; other buckets, projects and `..` are refused;
- `http(s)://…`: returned unchanged.

- `s3://` / `gs://` URIs in a registered reference bucket (see
  [Reference buckets](#reference-buckets-bring-your-own-bucket)): a presigned
  (S3) or V4 signed (GCS) URL.

`expires_at` is the earliest expiry of the presigned URLs (default lifetime 1 h;
*Weft*: `expires_in` seconds, 1 s to 7 days). One unresolvable
uri (unknown digest, unregistered bucket, other scheme) fails the request with
a 400 naming it.

*Weft*: `per_uri_errors: true` reports failures per URI instead, so one
request can resolve a mixed batch: the response keeps its shape and adds
`errors`, aligned with `uris`, where each entry is `null` or
`{"code", "message"}`; a failed URI's `download_urls` entry is `""`. Codes:
`bucket_not_registered` (no registration of the project or its team covers
the URI, including outside a registration's `prefix`), `no_files_backend`,
`not_project_file`, `file_not_found`, `invalid_uri` and `bucket_error` (the
bucket's credentials secret is missing or signing failed). Errors about the
request itself (project access, `expires_in`, more than 1000 uris) are still
returned as the request's status. The SDKs do not send it; the UI does.

**Bring your own bucket.** `--files-backend s3://bucket/prefix` (env
`WEFT_FILES_BACKEND`) writes new file contents to an S3-compatible store
instead of SQLite; the `files` table keeps the project, digest, size and the
object key (`<prefix>/<project_id>/<digest>`). Reads (`/file/content`, the UI)
go through the server, and `/files/resolve_url` returns presigned GET URLs.
Files stored before the backend was enabled stay in SQLite and remain
readable. SQLite stays the default.

| Flag | Env | Default |
|---|---|---|
| `--files-backend` | `WEFT_FILES_BACKEND` | empty (SQLite) |
| `--s3-endpoint` | `WEFT_S3_ENDPOINT`, `AWS_ENDPOINT_URL_S3`, `AWS_ENDPOINT_URL` | AWS (`https://s3.<region>.amazonaws.com`) |
| `--s3-region` | `WEFT_S3_REGION`, `AWS_REGION`, `AWS_DEFAULT_REGION` | `us-east-1` |
| `--s3-path-style` | `WEFT_S3_PATH_STYLE` | `true` with a custom endpoint (MinIO, R2, Ceph), else virtual-hosted |
| | `WEFT_S3_ACCESS_KEY_ID` / `AWS_ACCESS_KEY_ID`, `WEFT_S3_SECRET_ACCESS_KEY` / `AWS_SECRET_ACCESS_KEY`, `WEFT_S3_SESSION_TOKEN` / `AWS_SESSION_TOKEN` | required with `--files-backend` |

Requests are signed with AWS Signature Version 4 (stdlib only,
`internal/weft/objstore`); only static credentials are supported (no instance
profiles or SSO).

`POST /image/create` `{project_id, inputs: {model, prompt, n?}, track_llm_call?}`
→ `{"response": {...} | {"error": "..."}, "call_id": "..." | null}`.
The model is routed like chat models; OpenAI-compatible providers, `Provider`
objects and runtimes call `<base_url>/images/generations` (Weft also passes
`size`, `quality`, `style`, `response_format`, `background`, `output_format`
from `inputs`). `mock/<anything>` returns deterministic 64×64 PNGs
(`data[].b64_json`) offline; Anthropic models are an error. Tracked requests
are `weft.images.generate` calls with the response as output and token usage
(plus `images`) in the summary.

### Alternative response shapes

- **`/calls/stats`**: a request with any of `granularity`, `usage_metrics`,
  `call_metrics` or `timezone` gets metric buckets; otherwise the time series
  (`bucket_seconds` → `buckets`/`totals`, used by the UI) is unchanged.
  Metric buckets: `{project_id, start, end?, granularity?, usage_metrics?: [{metric, aggregations?, percentiles?}], call_metrics?: [...], filter?, timezone?}`
  → `{start, end, granularity, timezone, usage_buckets, call_buckets}`.
  Usage metrics (`input_tokens`, `output_tokens`, `total_tokens`,
  `cache_read_input_tokens`, `cache_creation_input_tokens`, `input_cost`,
  `output_cost`, `total_cost`) are grouped by bucket and model; call metrics
  (`latency_ms`, `call_count`, `error_count`) by bucket. Columns are
  `<agg>_<metric>` (`sum`, `avg`, `min`, `max`, `count`, `count_true`,
  `count_false`; default `sum`) and `p<pct>_<metric>`, plus `timestamp`,
  `count` and `model`. `sum`/`count`/`avg` are 0 in empty buckets,
  `min`/`max`/percentiles null; every bucket × model pair is listed. Granularity
  defaults to 5 min up to 2 h, 1 h up to 12 h, 6 h up to 3 d, 12 h up
  to 14 d, else 1 d, and is widened to keep at most 10,000 buckets. Buckets
  align to the interval in `timezone`. Token usage is each call's own usage
  (the whole trace's with `filter.trace_roots_only`); costs use the project's
  price book, and unpriced models have null costs. At most 200,000 calls are
  read.
- **`/calls/query_stats`** accepts `limit`: `count` stops at the limit and
  `has_more` says whether more calls match (always present).
- **`/feedback/query`** returns `total_count`: feedback matching `query`,
  ignoring `limit` and `offset`.
- **`GET /version`** → `{"version", "server": "weft", "trace_server_version"}`,
  no API key needed.

### Registry, model catalog and geolocation

**Registry.** `POST /link_to_registry` `{ref, target: {portfolio_name, entity_name, project_name}, aliases?}`
→ `{"version_index"}` links an object version (any project) into the
collection `portfolio_name` of the registry project
`<entity_name>/<project_name>`. Versions are numbered from 0 per collection;
linking a ref that is already in the collection returns its index. Aliases
move to the version they are last given to; `latest` (newest version) and
`v<N>` are implicit and cannot be assigned. With auth, the key needs write access to
both the ref's project and the registry project.
*Weft*: `POST /registry/query` `{project_id (registry project), portfolio_name?, alias?, version_index?}`
→ `{"collections": [{"name", "versions": [{"version_index", "version", "ref", "source_project_id", "object_id", "digest", "base_object_class", "aliases", "created_at", "wb_user_id"}]}]}`
(newest first); `alias`/`version_index` need `portfolio_name` and give 404 when
nothing matches.

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

## Users, teams and roles

Weft has users, teams and three roles:

| Role | Can |
|---|---|
| `viewer` | read: every `GET` and read-only `POST` (`…/query`, `…/read`, `…/stats`, …) |
| `member` | also write: calls, objects, tables, files, feedback, costs, completions, evaluations, queues, … |
| `admin` | also `/project/members/*` writes, `/project/*_settings/update`, `/project/ttl_settings/purge`, `/project/delete`, and the project's audit log |

- A **team** is an entity: a role in team `acme` applies to every project
  `acme/*`, including projects created later. A role can also be granted on
  one project. The effective role is the stronger of the two.
- A **server admin** (`users.admin`) is admin of every project and alone may
  manage users and teams (`/users/create|update|revoke_sessions`,
  `/teams/create|delete`) and provider secrets (`/secrets/set|delete`).
  Static `--api-keys` keys and managed admin keys are server admins.
- **API keys belong to users.** A key created with a `user_id` that names a
  user is bound to it (`"user_bound": true`) and carries that user's roles,
  narrowed by the key's `projects` and `read_only`. Such keys cannot have
  the `admin` flag; make the user an admin instead. A disabled user's keys
  get 401. Keys whose `user_id` is not a user keep the flag-based access
  above.
- **Self-service keys.** Users who are not server admins may call
  `/api_keys/create` for themselves only (no `admin`; a project-scoped key
  can create keys only for a subset of its projects; a read-only credential
  only read-only keys), and `/api_keys/list|revoke` see only their own keys
  (revoking another user's key is a 404).
- **Enforcement.** Every authenticated request is checked against every
  project it names (same discovery as for scoped keys: `project_id` in the
  query, body, wrappers, batch items and refs, the `/v2/{entity}/{project}/`
  path, the OTLP `project_id` header). A non-admin request that names no
  project gets 403, except `/auth/*`, `/projects`, `/llm/models`,
  `/secrets/list`. `GET /projects` lists only projects where the
  caller has a role. Refusals are 403 with a `reason` such as
  `"the member role on acme/app is needed; you have viewer"`.

| Endpoint | Body | Response | Who |
|---|---|---|---|
| `GET /auth/me` | | `{"user": User\|null, "user_id", "auth_method": "none"\|"static_key"\|"api_key"\|"session", "server_admin", "read_only", "projects": [..]\|null, "key_id", "roles": {"entities": {team: role}, "projects": {pid: role}}, "sso_enabled", "auth_required"}` | any |
| `/users/create` | `{id, name?, email?, admin?}` | `{"user": User}` | server admin |
| `GET /users/list?include_disabled=true` (or POST) | | `{"users": [User]}` | server admin, or any team/project admin |
| `/users/update` | `{id, name?, email?, admin?, disabled?}` | `{"user": User}`; disabling revokes sessions; you cannot demote or disable yourself | server admin |
| `/users/revoke_sessions` | `{id}` | `{"revoked": n}` | server admin |
| `/teams/create` | `{name, display_name?}` | `{"team": Team}` | server admin |
| `GET /teams/list` (or POST) | | `{"teams": [Team]}`: all for server admins, else the caller's teams | any |
| `/teams/delete` | `{name}` | `{}`; memberships go, projects stay | server admin |
| `/teams/members/set` | `{team, user_id, role}` | `{"team": Team}` | server admin or team admin |
| `/teams/members/remove` | `{team, user_id}` | `{"team": Team}` | server admin or team admin |
| `/project/members/list` | `{project_id}` | `{"project_id","entity","members": [{user_id, name, email, role, project_role, team_role, server_admin, disabled}], "can_manage"}` | viewer |
| `/project/members/set` | `{project_id, user_id, role}` | `{"project_id","user_id","role"}` | project admin |
| `/project/members/remove` | `{project_id, user_id}` | `{}` | project admin |

User: `{"id","name","email","admin","sso","created_at","disabled_at","last_login_at"}`.
User ids match `[A-Za-z0-9_.@-]{1,64}` and become the default `wb_user_id`.
Team: `{"name","display_name","created_at","members": [{"user_id","role","source": "manual"|"sso"}]}`.

### Single sign-on (OIDC) and sessions

With `--oidc-issuer`, `--oidc-client-id` and `--oidc-redirect-url` (see
[README](README.md#run-it)) the UI offers **Sign in with SSO** and
authentication is required.

| Endpoint (no auth) | Notes |
|---|---|
| `GET /auth/sso` | `{"enabled", "login_url", "issuer"}` |
| `GET /auth/oidc/login?return_to=/path` | 302 to the provider's authorization endpoint (from `.well-known/openid-configuration`) with `state`, `nonce` and an S256 `code_challenge`; sets a 10-minute sealed `weft_oidc` cookie (path `/auth/oidc/`) |
| `GET /auth/oidc/callback` | checks `state`, exchanges the code with the PKCE verifier, verifies the ID token (issuer, audience = client id, expiry, RS256/ES256 signature from the JWKS, nonce), applies the allowed domains and group mapping, provisions the user and redirects to `return_to` (same-site paths only) with the session cookies set; on failure redirects to `/?sso_error=<reason>` |
| `POST /auth/logout` | revokes the session (needs `X-Weft-CSRF`) and clears the cookies |

- **Provisioning.** A user is found by issuer and subject; otherwise an
  existing user with the same email is linked when the email is verified (or
  in an allowed domain); otherwise one is created from `preferred_username`
  or the email's local part. Disabled users cannot sign in.
- **Who may sign in.** With `--oidc-allowed-domains`, only verified emails
  of those domains. Otherwise, with `--oidc-group-roles`, only members of a
  mapped group. With neither, anyone at the provider (with no access until
  granted a role).
- **Groups.** `--oidc-group-roles "platform=admin,ml=member@acme"` reads
  the `--oidc-groups-claim` claim. `group=role@team` memberships are
  re-synced at every sign-in (source `sso`; manual memberships are kept).
  When a `group=admin` mapping exists, the server-admin flag of SSO users
  follows it. `--oidc-admin-emails` grants server admin to those verified
  emails.
- **Cookies.** `weft_session` (HttpOnly, SameSite=Lax, Secure on TLS or an
  https redirect URL, `--session-ttl`): a random id plus its HMAC-SHA256
  under the session key; the database keeps only SHA-256 of the id.
  `weft_csrf` (readable by script): the session's CSRF token.
- **CSRF.** A cookie-authenticated request other than `GET`/`HEAD`/`OPTIONS`
  must send `X-Weft-CSRF: <weft_csrf>` (403 otherwise), and an `Origin`
  other than the server's own or a `--cors-origins` entry is refused.
  Requests carrying an API key do not use the cookie and are exempt.
- Sessions end at sign-out, expiry, `/users/revoke_sessions`, or when the
  user is disabled.

### Audit log

Recorded: every request other than a read (any outcome, including 401 and
403), every refused (403) request, admin actions with a `detail` text, and
SSO sign-ins, sign-in failures and sign-outs. Request bodies, headers and
secret values are never recorded. Entries are written in the background and
purged after `--audit-retention-days` (default 90). Project delete keeps
them.

| Endpoint | Body | Response |
|---|---|---|
| `/audit/query` | `{project_id?, actor?, method?, path_prefix?, outcome?, since?, until?, limit? (≤10000, default 100), offset?}` | `{"entries": [AuditEntry], "total_count", "retention_days"}`, newest first |
| `/audit/export` | same filter (limit default 10000) | `text/csv` with the AuditEntry columns; cells starting with `= + - @` are prefixed with `'` |

Without `project_id` only server admins may query; with it, admins of that
project. AuditEntry:
`{"id","ts","actor","auth_method": "static_key"|"api_key"|"session"|"sso"|"none"|"unauthenticated","key_id","method","path","project_id","object_ids","call_ids","status","outcome": "success"|"denied"|"unauthenticated"|"failed","ip","detail"}`.
`object_ids` and `call_ids` (up to 50 each) come from `object_id`, `obj`,
`call_id(s)`, `start`/`end`, batch items and object/call refs in the body.

### OAuth client credentials (RemoteScorer, predict_url)

`RemoteScorer` and models evaluated through `predict_url` or a `predict`
runtime accept an OAuth 2.0 client-credentials `auth_config`:

```jsonc
"auth_config": {"token_url": "https://idp.example.com/oauth/token", "client_id": "weft",
                "client_secret_secret": "SCORER_CLIENT_SECRET",   // name of a stored secret (Settings → Secrets)
                "scopes": ["score"],                              // list or space-separated string
                "audience": "https://scorer.example.com"}         // optional, sent as a token parameter
```

The token is fetched with the stored secret, cached per token URL, client,
secret, scopes and audience, refreshed 30 seconds before `expires_in`, and
sent as `Authorization: Bearer`. It overrides `bearer_secret_name` and
`headers_secret`. Token errors report the HTTP status and OAuth error code
only. The token URL is checked against the [outbound request
restrictions](#outbound-request-restrictions) separately from the scorer URL.

### Outbound request restrictions

Requests Weft makes to URLs that users or projects supply go through one
egress guard (`internal/weft/egress`): `RemoteScorer` URLs, `predict_url`
models and `predict` runtimes, OAuth token URLs, automation webhooks, LLM
provider base URLs (built-in providers, custom `Provider` objects, custom
runtimes, `OPENAI_BASE_URL` / `OLLAMA_BASE_URL` stored as secrets), embedding,
moderation and image endpoints, and reference-bucket tests.

The guard checks the address every connection actually dials (a
`net.Dialer` control hook), so a hostname that resolves or re-resolves (DNS
rebinding) to an internal address is refused, and so is every redirect hop.
Refused by default:

| Range | Examples |
|---|---|
| Loopback | `127.0.0.0/8`, `::1`, `localhost` |
| Private | `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, IPv6 ULA `fc00::/7`, site-local `fec0::/10` |
| Link-local | `169.254.0.0/16`, `fe80::/10` |
| Cloud metadata | `169.254.169.254`, `169.254.170.2`, `169.254.170.23`, `fd00:ec2::254`, `fd00:ec2::23`, `100.100.100.200` |
| CGNAT | `100.64.0.0/10` |
| Unspecified, multicast, reserved | `0.0.0.0/8`, `::`, `224.0.0.0/4`, `ff00::/8`, `240.0.0.0/4` (incl. broadcast), `192.0.0.0/24`, `198.18.0.0/15`, `100::/64` |
| Embedded IPv4 forms of the above | IPv4-mapped `::ffff:127.0.0.1`, IPv4-compatible `::127.0.0.1`, NAT64 `64:ff9b::/96`, 6to4 `2002::/16` |

Weft applies this to every user-supplied destination. A `RemoteScorer` (and a
`predict_url` model) does not follow redirects: a 3xx response fails the
call. Webhooks and provider calls follow redirects, each hop checked.
`RemoteScorer` URLs must also use `https://` unless the operator starts the
server with `--remote-scorer-allow-insecure-http`
(`WEFT_REMOTE_SCORER_ALLOW_INSECURE_HTTP=true`).

A refused request fails with `destination not allowed` (for example a scorer
result `{"error": "remote scorer: destination not allowed"}`); the resolved
address and the reason are only in the server log, at warn level. Operators
open destinations with `--egress-allow` / `WEFT_EGRESS_ALLOW` (hosts,
`host:port`, `*.domain`, IPs and CIDRs, comma-separated) or
`--egress-allow-private` / `WEFT_EGRESS_ALLOW_PRIVATE=true` (loopback,
private, link-local and CGNAT; cloud metadata, unspecified and multicast stay
refused unless listed in `--egress-allow`). Provider base URLs set in the
server's own environment (`OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL`,
`OLLAMA_BASE_URL`) are operator configuration and are allowed automatically.
Every address a name resolves to is checked, and the request is refused if
any of them is internal, with or without a proxy; the connection then goes
to the addresses that were checked, so a second DNS answer cannot differ.

With an HTTP(S) proxy in the environment (`HTTPS_PROXY`), the destination is
resolved and checked before the request is handed to the proxy. Behind a
proxy:

- a name this host cannot resolve cannot be checked, so it is refused
  (`reason=unresolved` in the log) unless it is listed in `--egress-allow`;
- only the connection to the proxy chosen for that request skips the checks.
  A request addressed to the proxy's own host and port directly (for
  example because `NO_PROXY` excludes loopback) is checked like any other;
- the proxy resolves the name again itself, so a DNS answer that changes
  between the two lookups is not caught here. Configure the proxy to refuse
  internal destinations too if that matters.

Not guarded, because the operator sets them on the command line and they are
often local by design: the PII analyzer (`--pii-analyzer-url`), the OIDC
issuer and its discovery/JWKS/token endpoints (`--oidc-issuer`), and the
S3/GCS files backend (`--s3-endpoint`, `--files-backend`).

### Database encryption at rest

Opt-in. Without a key the SQLite database is a plaintext file (driver
`modernc.org/sqlite`) and only provider secrets inside it are AES-256-GCM
encrypted. With `--db-encryption-key-file` / `WEFT_DB_ENCRYPTION_KEY_FILE`
(or `--db-encryption-key` / `WEFT_DB_ENCRYPTION_KEY`) the server opens the
database through `github.com/ncruces/go-sqlite3` and its `adiantum` VFS: the
database, WAL and temporary files are encrypted in 4 KiB blocks with a key
derived from the passphrase by Argon2id. The API is unchanged. Startup fails
before writing anything when the key is wrong, missing for an encrypted
file, or set for a plaintext one. `weft db encrypt|decrypt|rekey --db PATH
--out PATH` converts a stopped server's database into a new, verified copy.
Adiantum is unauthenticated, deterministic disk encryption: it hides content,
not which blocks changed between snapshots, and does not detect tampering;
see [README](README.md#database-encryption-at-rest) for costs and caveats.
New database files are created mode 0600, and startup logs a warning for
each of the database, `-wal`, `-shm` and `<db>.key` files that is
world-readable.

### Agent evals (Weft)

Evaluation runs and the agent spans and conversations they involve, for the
Agents view's **Evals** tab and the **View spans** / **View conversations**
actions on evaluations. A span belongs to evaluation run R and prediction P
(an `Evaluation.predict_and_score` call under R) when:

- it carries `weft.eval.run_id = R` and `weft.eval.predict_and_score_call_id = P`
  (flat, nested under `attributes.weft.eval`, or as OTel span attributes);
  spans the SDKs record inside `EvaluationLogger.log_prediction()` get them.
  A span with only `predict_and_score_call_id` is linked to P's parent run;
- P links it through `attributes.weft.linked_calls` or
  `attributes.weft.genai_span_ref` (span or call id, `weft:///…/call/<id>`
  ref, or `{call_id|span_id, trace_id}`), or links its conversation through
  `attributes.weft.linked_conversation_ids` (v2 `POST /predictions` with
  `genai_span_ref` and `evaluation_run_id` too);
- or it descends from such a span (OTel attributes do not propagate, and
  LLM integration calls inside a turn are not tagged).

Links are indexed as calls are written (existing databases are backfilled
once at startup). Links recorded by deleted calls, or pointing at deleted
runs, are ignored.

**Filters.** `/agents/spans/query`, `/agents/spans/stats` and
`/agents/spans/custom-attrs/schema` accept `evaluation_run_id?` and
`predict_and_score_call_id?` (both may be given). With `group_by:
[{key: "conversation_id"}]` the spans query lists a run's conversations.

| Endpoint | Body | Response |
|---|---|---|
| `/agents/evals/query` | `{project_id, agent_name?, conversation_id?, trace_ids?, evaluation_run_ids?, include_costs?, include_predictions?, limit? (50), offset?}` | `{"evaluations": [AgentEvalRun], "total_count"}`, runs with agent spans, newest first |
| `/agents/evals/conversations` | `{project_id, evaluation_run_id? \| predict_and_score_call_id? (one required), agent_name?, include_costs?, sort_by? (last_seen desc), limit? (50), offset?}` | `{"conversations": [Group + conversation_id, predictions: [AgentEvalPrediction]], "total_count", "span_count", "other_span_count"}`; `other_span_count` counts the run's spans outside any conversation |

**AgentEvalRun**: `call_id, display_name, evaluation_ref, evaluation_name,
model_ref, model_name, started_at, ended_at, status
(running|success|error|unknown), summary` (the run's output),
`prediction_count` (predictions with agent spans), `span_count, error_count,
turn_count, trace_count, conversation_count, conversation_ids` (up to 50),
`agent_names, first_seen, last_seen, total_input_tokens, total_output_tokens,
total_cost_usd` (with `include_costs`), and `predictions` with
`include_predictions`, `conversation_id` or `trace_ids`. `agent_name`,
`conversation_id` and `trace_ids` keep runs with a matching span, and the
totals and predictions then cover matching spans only.

**AgentEvalPrediction**: `predict_and_score_call_id, status, example,
output, scores, model_latency` (from the call's inputs and output),
`kind, row_digest, example_id, trial_index` (from `attributes.weft.eval`),
`conversation_ids, trace_ids, span_count`.

**UI.** Evaluations list (Agents column) and compare page (run cards, and
each prediction in the row detail) link to the Agents Spans tab filtered to
the run or prediction (`?tab=spans&eval=<run>&pred=<prediction>&range=all`,
`&group=conversation_id` for conversations). The Agents view and each agent
have an **Evals** tab, and a conversation page shows the evaluations and
predictions it belongs to, with scores, and marks the turns of each
prediction.

## Bulk export

The bulk export API exports a whole project as files.

`POST /v1/entities/{entity}/projects/{project}:export` `{"targets": ["calls", "objects", "feedback"]}`
(1 to 3 distinct targets; no other fields) → **202**
`{"name": "entities/{entity}/projects/{project}/exports/{job_id}", "job_id": "<uuid>", "created_at": "<RFC 3339>"}`.

`GET /v1/entities/{entity}/projects/{project}/exports/{job_id}` →

```jsonc
{"name": "...", "job_id": "...", "created_at": "...", "updated_at": "...",
 "state": "in_progress" | "succeeded" | "failed",
 "artifacts": [                                   // empty until succeeded: partial output is never returned
   {"target": "calls", "uri": "s3://bucket/prefix/e/p/exports/<job_id>/calls.jsonl.gz",
    "expected_rows": 1203, "written_rows": 1203,
    "size_bytes": 48211, "format": "jsonl.gz"}],  // size_bytes and format are Weft extensions
 "failure": null | {"code": "export_failed" | "interrupted", "message": "..."},
 "targets": ["calls"]}                            // Weft extension
```

- The job runs in the background. Each target is one gzip-compressed JSON
  Lines file with one row per line: calls as `/calls/stream_query` returns
  them (with `summary.weft.costs` and `storage_size_bytes`), object versions
  as `/objs/query` returns them (with `val`), feedback as `/feedback/query`
  returns it. Rows created after the job started are left out, so
  `expected_rows` (counted at the start) and `written_rows` differ only if rows
  are deleted while the job runs. The spec names no file format; JSON Lines
  was chosen because it streams and needs no library (no Parquet).
- **Where artifacts go.** With an S3 `--files-backend`, artifacts are uploaded
  (streamed from a temporary file) to `<prefix>/<project_id>/exports/<job_id>/<target>.jsonl.gz`
  and `uri` is that `s3://` URI, which `/files/resolve_url` presigns.
  Without one, they are stored in SQLite (1 MiB chunks) and `uri` is this
  server's download URL.
- *Weft*: `GET /v1/entities/{entity}/projects/{project}/exports/{job_id}/artifacts/{target}`
  downloads an artifact (`application/gzip`); for S3 artifacts it redirects to
  a presigned URL.
- One job runs at a time per server: starting another while one is running is
  a **409** `{"detail": {"code": "export_in_progress", "message": ...}}`. A job
  interrupted by a restart is marked `failed` with code `interrupted` when the
  server starts again.
- **Access.** Starting a job and reading its status or artifacts need the
  admin role on the project (team admin, project admin or server admin), or
  member on a personal project whose entity is the caller's user id; 403 is
  `{"detail": "<reason>"}`. Errors: 400/404/409/503
  `{"detail": {"code", "message"}}` and 422 `{"detail": [{"loc", "msg", "type"}]}`
  (missing or empty `targets`, unknown targets, extra fields, a job id that is
  not a UUID).

## Reference buckets (bring your own bucket)

Media that already lives in a customer bucket can be logged by URI
(`s3://bucket/key`, `gs://bucket/key`) and rendered without importing the
bytes. A bucket must be
registered first, for a team (entity: every project of the team may use it)
or for one project, in **Settings → Buckets** or with the endpoints below.
`/files/resolve_url` then presigns any URI in a registered bucket that the
request's project may use. URIs in buckets that are not registered (or
outside a registration's `prefix`) are a 400, and one such URI fails the
whole `uris` batch unless `per_uri_errors` is set (see
[Files and media](#files-and-media)).

How the UI renders bucket URIs:

- **Where.** A string value that is exactly one object URI, in any call
  input, output, attribute, object or dataset value (any field name), an
  agent tool result, or a message part: the GenAI `{"type": "uri", "uri",
  "mime_type"?, "modality"?}` part that `attach_media(uri=…)` writes, Gemini
  `fileData.fileUri`, an `image_url` with an `s3://`/`gs://` URL, or a
  message whose whole text is the URI. `Content.from_uri` values render as
  before. Strings that only mention a URI (`"see s3://b/x.png"`), prefixes
  ending in `/`, and `https://` strings stay text: only bucket URIs render
  as media.
- **Kind** comes from the file extension (or the part's `mime_type` /
  `modality`): images, video, audio and PDFs render inline; other files show
  the URI with a **Get download link** button that presigns on demand.
- **Resolution** is lazy: an inline URI is resolved when it scrolls near the
  viewport, URIs of one bucket are batched per request (with
  `per_uri_errors`, so 40 URIs of an unregistered bucket cost one request, not
  one per URI), and each presigned URL is cached until shortly before
  `expires_at` (then resolved again, also after a load error). Refusals are
  remembered for a minute.
- **Fallback.** When a URI cannot be resolved the URI shows as text with a
  short reason (for example "not rendered: bucket not registered for this
  project"); the page never waits on it.
- In the Agents conversation view, a tool result that is media shows under
  the tool row without expanding it, and media attached to the first LLM
  call's user message (`attach_media`) shows with the turn's user message
  when that message has none of its own.

| Endpoint | Body | Access |
|---|---|---|
| `POST /reference_buckets/create` | `{entity \| project_id, provider: "s3"\|"gcs", bucket, prefix?, region?, endpoint?, path_style?, secret_name?, test_object_key, skip_test?}` → `{"bucket": ReferenceBucket}` | server admin |
| `POST /reference_buckets/test` | same body → `{"ok": bool, "error": string\|null, "read": Check\|null, "cors": Check\|null}` (nothing saved) | server admin |
| `GET\|POST /reference_buckets/list` | `?project_id=` (the project's and its team's buckets) or `?entity=`; none lists all | viewer on the project; server admin otherwise |
| `POST /reference_buckets/delete` | `{id}` | server admin |

ReferenceBucket: `{"id", "scope": "entity"|"project", "entity", "project_id", "provider", "bucket", "prefix", "region", "endpoint", "path_style", "secret_name", "test_object_key", "verified_at", "created_at", "created_by"}`.
Credentials are never returned.

- `bucket` is the name only (`s3://` / `gs://` and a trailing `/` are
  stripped). `prefix`, when set, limits which keys may be presigned.
- **Test connection.** The test fetches the test object through a presigned
  URL and checks the bucket's CORS rules, so one readable object shows that
  both the credentials and CORS are in order. Unless `skip_test` is true,
  `create` runs it first and refuses the registration (400 with the test's
  `error`) if either part fails:
  - `read`: one byte of `test_object_key` (an existing object) through a
    presigned URL, as a browser would fetch it (bad credentials, missing
    object, unreachable endpoint fail it);
  - `cors`: for the Weft origin, a preflight (`OPTIONS` on the presigned URL
    with `Origin` and `Access-Control-Request-Method: GET`, which S3 and GCS
    answer from the bucket's CORS configuration without credentials) must
    allow the origin and `GET`, and the object's `GET` response must carry an
    `Access-Control-Allow-Origin` admitting it. The origin is the request's
    `Origin` header (the UI's own origin when the UI calls), otherwise this
    server's externally visible `scheme://host` (honouring
    `X-Forwarded-Proto/Host`); Weft has no public-URL setting.

  `Check` is `{"ok": bool, "error": string|null, "origin"?}` (`origin` on
  `cors`). `read` is null when the test could not start (missing secret,
  bad `test_object_key`), `cors` when the bucket could not be reached; `error`
  joins the failures. Configure the bucket's CORS rules with this
  Weft's origin in `AllowedOrigins` / `origin` (`GET`, `HEAD`). Images, audio
  and video load without CORS; PDFs are fetched by the page and need it.
  The Buckets tab shows registrations to anyone who can view the project; its
  Register, Test and Remove controls appear for server admins only, and a
  test's read and CORS results show per row and in the Register dialog.
- **S3** (`provider: "s3"`, also `"aws"`): presigned with SigV4
  (`internal/weft/objstore`). `secret_name` names a stored secret
  (Settings → Secrets, `/secrets/set`) holding
  `{"access_key_id": "...", "secret_access_key": "...", "session_token": "..."}`
  (AWS-style key names are accepted too); without `secret_name` the server's
  `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` / `AWS_SESSION_TOKEN` are used.
  `region` (default `AWS_REGION`, then `us-east-1`), `endpoint` and
  `path_style` work as for `--files-backend` (MinIO, R2, Ceph).
- **GCS** (`provider: "gcs"`, also `"gs"`): V4 signed URLs
  (`GOOG4-RSA-SHA256`, RSA-SHA256 with the standard library). `secret_name`
  is required and names a stored secret holding a service-account key file
  (JSON with `client_email` and `private_key`); the service account needs
  `roles/storage.objectViewer` on the bucket. `endpoint` defaults to
  `https://storage.googleapis.com`.
- When several registrations match, a project registration wins over the
  team's, then the longest `prefix`. URIs in the S3 `--files-backend` bucket
  keep the files-backend rule (only keys under the project's prefix).
- Presigned URLs last 1 hour by default (`expires_in`, 1 s to 7 days);
  `expires_at` is the earliest expiry in the batch.

## Optional call fields

Calls have optional fields that Weft accepts, stores and
returns (`/call/read`, `/calls/query`, `/calls/stream_query`):

| Field | Written by | Returned |
|---|---|---|
| `wb_run_step` | `/call/start` (`start.wb_run_step`), `/call/upsert_batch` | always (null when not sent) |
| `wb_run_step_end` | `/call/end` (`end.wb_run_step_end`), `/call/upsert_batch` | always (null when not sent) |
| `expire_at` | derived: `started_at` + the project's `retention_days` (`/project/ttl_settings/update`) | always; null when the project keeps calls forever. It moves with the TTL, matching the purge, which deletes calls by `started_at` |
| `total_storage_size_bytes` | derived: sum of `storage_size_bytes` over the trace | on trace roots, with `include_total_storage_size: true` (`/call/read`, `/calls/query`, `/calls/stream_query`) |
| `wb_username` | derived from `wb_user_id` | with `include_usernames: true`: the Weft user id (handle) when `wb_user_id` names a user, the `wb_user_id` itself when it is a valid handle without a user row (static `user=key` names), otherwise omitted |

`/calls/query_stats` already returns `total_storage_size_bytes` for the
matching calls. The run steps are stored in two new `calls` columns, added by
a migration on start.

## OpenTelemetry: Langfuse, OpenLit, Koog and verl

These layouts are mapped in `internal/weft/otlp/langfuse.go` (tests in
`langfuse_test.go`), on top of the GenAI, OpenInference, OpenLLMetry, Vercel
AI SDK, MLflow, Pydantic AI and ADK mappings in [OpenTelemetry ingest](#opentelemetry-ingest).

**Langfuse** (Langfuse SDK v3+ and exporters that write Langfuse attributes,
for example Koog's Langfuse exporter):

| Attribute | Weft |
|---|---|
| `langfuse.observation.input` | inputs (a message list becomes `inputs.messages`, an object is spread, anything else is `inputs.input`); wins over `input.value` |
| `langfuse.observation.output` | output; for `generation` observations a message, message list or string becomes a chat completion (`choices`) |
| `langfuse.observation.model.name` | `inputs.model` and the usage/cost model |
| `langfuse.observation.model.parameters` | merged into inputs (`temperature`, `max_tokens`, …) |
| `langfuse.observation.usage_details` | `summary.usage`: `input`/`output`/`total` (or OpenAI names) to prompt/completion/total tokens, `input_cached_tokens` to `cache_read_input_tokens`, `output_reasoning_tokens` to `reasoning_tokens` |
| `langfuse.observation.type` | `attributes["weft.span_kind"]` (`generation` = llm, agent, tool, chain, retriever, evaluator, embedding, guardrail), used by the Agents index unless `gen_ai.operation.name` is set |
| `langfuse.observation.level` = `ERROR` | `exception` (= `status_message`) when the span status is not already an error |
| `langfuse.observation.{level,status_message,prompt.name,prompt.version,completion_start_time,cost_details,metadata.*}`, `langfuse.trace.{tags,metadata.*}` | `attributes.langfuse` |
| `langfuse.trace.name` | `display_name` of a root span |
| `langfuse.trace.input` / `langfuse.trace.output` | inputs / output when the span has nothing more specific |
| `langfuse.session.id`, `session.id` | `thread_id` |
| `langfuse.user.id`, `user.id` | `wb_user_id` |

**OpenLit** emits the GenAI conventions (`gen_ai.input.messages` /
`gen_ai.output.messages` with parts, `gen_ai.usage.*`, request parameters),
which were already mapped. The OpenLit fixture test found and fixed: the
system prompt appeared twice (OpenLit puts it both in
`gen_ai.system_instructions` and in the input messages; it is now prepended
only when the messages do not start with one), `gen_ai.session.id` /
`gen_ai.user.id` are now `thread_id` / `wb_user_id`, and tool spans'
`gen_ai.tool.input` / `gen_ai.tool.output` are now inputs / output.
`gen_ai.usage.cost` and `gen_ai.client.token.usage` stay in
`attributes.otel`; Weft computes costs from its price book.

**Koog** (JetBrains' Kotlin agent framework, `agents-features-opentelemetry`)
emits GenAI spans (`invoke_agent <agent>`, `chat <model>`,
`execute_tool <tool>`, `node <id>`), mapped generically, with fixes the Koog
fixture found: message roles are Kotlin enum names (`User`, `Assistant`,
`System`) and are lower-cased; the empty `tool` entry Koog writes before every
user message is dropped; `koog.node.input/output` and
`koog.subgraph.input/output` become inputs / output; and with Koog's
indexed-attribute exporter, `gen_ai.completion.N.tool_calls.M.function` (`{"name","arguments"}`
JSON) becomes an OpenAI tool call. `gen_ai.conversation.id` (the run id) is
the thread.

**verl** has no OTel layout of its own. With
`trace.backend=mlflow` and MLflow's OTLP exporter pointed at Weft, its spans
use the MLflow mapping (`mlflow.spanInputs` / `mlflow.spanOutputs`; test
`TestVerlMLflowFixture`).

## Native API (`/api/v1`)

Weft's own API for agents and agent teams ([DESIGN.md](DESIGN.md) §2, §3):
sessions, agents, teams, the hand-offs between agents, each model's
reasoning, and scores at session, agent and hand-off scope. AgentShield
detections and the `/api/v1/shield/*` routes are documented separately.

**Conventions.** JSON over HTTP with the usual authentication. GET requests
take `project_id` in the query, POST requests in the body; the caller needs
the viewer role on the project for GET and the member role for POST
(a read-only key cannot run scorers). Lists take `limit`, `offset` and
`sort_by` (`field`, `-field` or `field:desc`) and return
`{"items": [...], "total_count": n}`, where `total_count` counts all matches
before paging. Timestamps are RFC 3339 UTC; durations are milliseconds.
Path segments (session ids, agent names, team ids) are URL-escaped, and so
are query values: an auto team id's `+` must be sent as `%2B`. Errors are
`400` (bad parameter), `404` (unknown session, agent or team), `401`/`403`
(authentication, role).

### Where the data comes from

Everything is derived at ingest from calls (SDK calls and OTLP spans), so
the order spans arrive in does not matter: a child that arrives before its
parent is linked when the parent arrives. Databases written before this API
existed are indexed once, the first time the server starts.

- **Session**: `gen_ai.conversation.id` (or the call's `thread_id`). Spans
  without one belong to no session. AgentShield verdict spans
  (instrumentation scope `agentshield`, or `agentshield.verdict.action`) count
  as detections, not as agent activity: they are left out of span, agent,
  tool and graph figures, and a session seen only through verdicts is listed
  with `span_count: 0`. Weft's own judge and monitor calls are ignored.
- **Agent**: `gen_ai.agent.name` on `invoke_agent` spans (see
  [Agents](#agents) for the other conventions).
- **Team**: the first `weft.team.id` (and `weft.team.name`) on any span of
  the session; otherwise `auto:` + the session's agent names, sorted and
  joined by `+` (name: joined by ` + `).
- **Hand-offs** (`kind`, and `source` naming the rule that found them):

| Rule | `source` | `kind` |
|---|---|---|
| an agent invocation whose nearest enclosing invocation belongs to another agent | `nested` | `delegate` |
| a span with `weft.handoff.to_agent` (optional `weft.handoff.from_agent`, `weft.handoff.kind`) | `attribute` | given, default `handoff` |
| OpenAI Agents SDK hand-off spans: `from_agent` / `to_agent` attributes, span names `handoff to <agent>` or `Handoff: <a> → <b>`; agent spans with OpenInference `graph.node.parent_id` | `openai_agents` | `handoff` |
| LangGraph: consecutive agent nodes (`langgraph_node` / `langgraph_step`, flat or in OpenInference `metadata`) of one graph run; a node is an agent node when it ran a model | `langgraph` | `handoff` |
| CrewAI tools `Delegate work to coworker` / `Ask question to coworker` (`coworker` argument) | `crewai` | `delegate` |
| Google ADK `transfer_to_agent` tool (`agent_name` argument) | `adk` | `handoff` |
| an agent invocation with an OTel span link to a span of another agent (any trace) | `link` | `handoff` |
| a `delegate` hand-off whose delegate has finished: control goes back | `return` | `return` |

  An explicit or framework hand-off resolves `to_span_id` to the target
  agent's invocation (a descendant of the hand-off span first, else the first
  one that starts after it) and replaces the `nested` edge for that span.
  Hand-off ids are stable across re-indexing.
- **Reasoning steps** come from model spans' inputs and outputs and from
  `weft.reasoning` span events: Anthropic `thinking` (`kind: thinking`) and
  `redacted_thinking` (`kind: redacted`, no text) blocks; OpenAI Responses
  `reasoning` items (each summary part is a `kind: summary` step; raw
  `reasoning_text` is `thinking`; an item with only `encrypted_content` is
  `redacted`); Gemini parts with `thought: true`; GenAI semconv parts of type
  `reasoning` or `thinking`; `reasoning_content` on chat messages; OTLP
  ingest also keeps OpenInference `message_content.type: reasoning` parts and
  the Vercel AI SDK's `ai.response.reasoning`. A `weft.reasoning` event takes
  `weft.reasoning.text`, `weft.reasoning.kind` and `weft.reasoning.tokens`
  attributes; SDK calls can carry the same as the call attribute
  `weft.reasoning: [{text, kind, tokens}]`. A step's `tokens` is the span's
  reasoning token count (`output_tokens_details.reasoning_tokens`,
  `gen_ai.usage.reasoning.output_tokens`, Gemini `thoughts_token_count`) on
  its first output step. Steps found in a model's input that repeat an
  earlier step of the session (history sent back to the model) are
  duplicates and hidden unless asked for.
- **`next_span_id`** of a step is the first tool call or hand-off that
  starts after it in the same agent run: for a model's output, the tool call
  whose `gen_ai.tool.call.id` matches one the model requested, else the first
  one before the agent's next model call; for an event, the first one after
  the event. Input steps have none.

### Sessions

`GET /api/v1/sessions` lists sessions, most recent activity first.

| Filter | Meaning |
|---|---|
| `agent` | an agent took part |
| `team_id` | the session's team |
| `status` | `running` (a root span has not ended), `error` (a root span or agent invocation failed), `ok` |
| `has_detection` | `any`, or an action (`block`, `require_approval`, …) with at least one detection |
| `min_score`, `max_score` | `scorer:value` against the mean of the session's session-scope scores by that scorer |
| `model`, `tool` | a model or tool was used |
| `q` | text in the session id, name, title, agents or team |
| `from`, `to` | sessions with activity in `[from, to)` |

`sort_by`: `last_activity` (default, desc), `started_at`, `duration_ms`,
`span_count`, `turn_count`, `tool_call_count`, `handoff_count`,
`reasoning_step_count`, `error_count`, `detection_count`, `input_tokens`,
`output_tokens`, `reasoning_tokens`, `cost_usd`, `status`, `name`, `session_id`.

`GET /api/v1/sessions/{id}` returns one summary, plus `trace_ids`.

```json
{
  "session_id": "trip-42", "name": "", "title": "Find three facts about Oslo and summarise them",
  "started_at": "2026-11-03T09:16:02.572650Z", "last_activity": "2026-11-03T09:16:06.572650Z", "duration_ms": 4000,
  "agents": ["orchestrator", "researcher"], "team_id": "trip-planners", "team_name": "Trip planners",
  "turn_count": 1, "trace_count": 1, "span_count": 8, "llm_call_count": 4, "tool_call_count": 2,
  "handoff_count": 1, "reasoning_step_count": 3, "detection_count": {}, "error_count": 0,
  "input_tokens": 830, "output_tokens": 310, "reasoning_tokens": 50, "cost_usd": 0.006525, "status": "ok",
  "models": ["claude-sonnet-4", "gpt-4o"], "tools": ["delegate_research", "web_search"],
  "scores": {"GoalCompletionJudge": {"mean": 0.25, "count": 1}}
}
```

`title` is the first user request; `agents` are in order of first
appearance; `turn_count` counts turn spans, else traces; `handoff_count`
excludes `return` edges; `detection_count` counts AgentShield detections by
action; `cost_usd` is `null` when no model price is known; `scores` are the
session-scope scores (mean and count per scorer).

`GET /api/v1/sessions/{id}/timeline` returns the session's steps in time
order. Filters: `types` (comma-separated), `agent`; `limit` defaults to 1000.
Each item has `type`, `span_id`, `trace_id`, `agent_name`, `started_at`,
`ended_at` and a `payload`:

| `type` | `payload` |
|---|---|
| `turn` | `name`, `status`, `duration_ms`, `input_preview`, `output_preview` (a turn is a turn span, else a trace's root span) |
| `message` | `role` (`user` at the turn's start, `assistant` at its end), `text` |
| `llm` | `model`, `provider`, `input_tokens`, `output_tokens`, `reasoning_tokens`, `cost_usd`, `duration_ms`, `status`, `output_preview`, `tool_calls` (`[{id, name, arguments}]`) |
| `reasoning` | `step_id`, `index`, `kind`, `text`, `tokens`, `next_span_id`, `source` |
| `tool_call` | `tool_name`, `tool_call_id`, `arguments` (main argument or JSON), `result`, `status`, `duration_ms` |
| `handoff` | `handoff_id`, `from_agent`, `to_agent`, `kind`, `source`, `status`, `from_span_id`, `to_span_id`, `duration_ms`, `input_preview`, `output_preview` (`span_id` is the hand-off's `source_span_id`) |
| `error` | `message` (first line), `error_type`, `operation`, `name` |

```json
{"session_id": "trip-42", "total_count": 14, "items": [
  {"type": "llm", "span_id": "00000000000000a2", "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736", "agent_name": "orchestrator",
   "started_at": "2026-11-03T09:16:02.622650Z", "ended_at": "2026-11-03T09:16:03.422650Z",
   "payload": {"model": "claude-sonnet-4", "provider": "anthropic", "input_tokens": 200, "output_tokens": 120, "reasoning_tokens": 0,
     "cost_usd": 0.0024, "duration_ms": 800, "status": "OK", "output_preview": "", "name": "chat claude-sonnet-4",
     "tool_calls": [{"id": "toolu_1", "name": "delegate_research", "arguments": "{\"task\":\"Find three facts about Oslo\"}"}]}},
  {"type": "reasoning", "span_id": "00000000000000a2", "agent_name": "orchestrator", "started_at": "2026-11-03T09:16:02.622650Z", "ended_at": null,
   "payload": {"step_id": "00000000000000a2#0", "index": 0, "kind": "thinking", "text": "The user wants facts; the researcher agent should gather them.",
     "tokens": null, "next_span_id": "00000000000000a3", "source": "output"}},
  {"type": "tool_call", "span_id": "00000000000000a3", "agent_name": "orchestrator", "started_at": "2026-11-03T09:16:03.472650Z",
   "ended_at": "2026-11-03T09:16:04.972650Z", "payload": {"tool_name": "delegate_research", "tool_call_id": "toolu_1",
     "arguments": "Find three facts about Oslo", "result": "", "status": "OK", "duration_ms": 1500}},
  {"type": "handoff", "span_id": "00000000000000a4", "agent_name": "orchestrator", "started_at": "2026-11-03T09:16:03.572650Z",
   "ended_at": "2026-11-03T09:16:04.872650Z", "payload": {"handoff_id": "459997d98657e7e6c51106885dd22d25", "from_agent": "orchestrator",
     "to_agent": "researcher", "kind": "delegate", "source": "nested", "status": "ok", "from_span_id": "00000000000000a1",
     "to_span_id": "00000000000000a4", "duration_ms": 1300, "input_preview": "Find three facts about Oslo",
     "output_preview": "1. Capital of Norway 2. On the Oslofjord 3. Nobel Peace Prize"}}
]}
```

(`trace_id` trimmed from some items.) Detections are not part of the
timeline: fetch them with `/api/v1/detections?session_id=` and merge by
`span_id` / `tool_call_id`.

`GET /api/v1/sessions/{id}/graph` returns the delegation graph. Nodes:
`agent`, `calls` (invocations), `spans`, `errors`, `tool_calls`, `tokens`
(input + output), `cost_usd`, `detections` (by action). Edges, one per
`from`, `to` and `kind`: `count`, `errors`, `error_rate`, `p50_ms`, `p95_ms`
(hand-off durations; `null` for `return`), `mean_score` and `scores`
(hand-off-scope scores).

```json
{"nodes": [
   {"agent": "orchestrator", "calls": 1, "spans": 4, "errors": 0, "tokens": 810, "cost_usd": 0.00495, "tool_calls": 1, "detections": {}},
   {"agent": "researcher", "calls": 1, "spans": 4, "errors": 0, "tokens": 330, "cost_usd": 0.001575, "tool_calls": 1, "detections": {}}],
 "edges": [
   {"from": "orchestrator", "to": "researcher", "kind": "delegate", "count": 1, "errors": 0, "error_rate": 0, "p50_ms": 1300, "p95_ms": 1300,
    "mean_score": 0.75, "scores": {"HandoffQualityJudge": {"mean": 0.75, "count": 1}}},
   {"from": "researcher", "to": "orchestrator", "kind": "return", "count": 1, "errors": 0, "error_rate": 0, "p50_ms": null, "p95_ms": null, "mean_score": null}]}
```

`GET /api/v1/sessions/{id}/reasoning` returns reasoning steps in order.
Filters: `agent`, `kind` (`thinking`, `summary`, `redacted`),
`include_duplicates=true`. `index` is the step's position among the
session's non-duplicate steps (`-1` for duplicates); `span_index` its
position within its span.

```json
{"session_id": "trip-42", "total_count": 3, "items": [
  {"id": "00000000000000a2#0", "span_id": "00000000000000a2", "session_id": "trip-42", "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
   "agent_name": "orchestrator", "index": 0, "span_index": 0, "kind": "thinking",
   "text": "The user wants facts; the researcher agent should gather them.", "tokens": null,
   "started_at": "2026-11-03T09:16:02.622650Z", "next_span_id": "00000000000000a3", "source": "output"}
]}
```

### Agents

`GET /api/v1/agents` lists agents with the [Agents](#agents) statistics
(`agent_name`, `invocation_count`, `span_count`, `error_count`,
`total_input_tokens`, `total_output_tokens`, `total_cost_usd`, latencies,
`models`, …) plus `reasoning_tokens`, `reasoning_step_count`,
`handoffs_in`, `handoffs_out` (excluding returns), `session_count`, `tools`,
`teams`, `detection_counts` (by action), `detection_rate` (detections per
tool call) and `scores` (agent-scope scores, mean and count per scorer).
Agents that only appear as hand-off endpoints (for example LangGraph nodes)
are listed too. Filters: `q` (name contains), `team_id`, `from`, `to`.
`sort_by`: `last_seen` (default, desc), any agent statistic, or
`reasoning_tokens`, `handoffs_in`, `handoffs_out`, `session_count`,
`detection_count`.

`GET /api/v1/agents/{name}` adds `tool_counts` (`[{tool, count, errors}]`),
`handoffs_from` and `handoffs_to` (`[{agent, kind, count}]`) and
`recent_sessions` (`[{session_id, team_id, last_activity}]`, newest 50).

```json
{"agent_name": "researcher", "invocation_count": 1, "span_count": 4, "error_count": 0, "total_input_tokens": 230, "total_output_tokens": 100,
 "total_cost_usd": 0.001575, "latency_ms_p50": 1300, "llm_call_count": 2, "tool_call_count": 1, "models": ["gpt-4o"],
 "reasoning_tokens": 50, "reasoning_step_count": 1, "handoffs_in": 1, "handoffs_out": 0, "session_count": 1, "tools": ["web_search"],
 "teams": ["trip-planners"], "detection_counts": {}, "detection_rate": null,
 "scores": {"ReasoningConsistencyJudge": {"mean": 1, "count": 1}},
 "tool_counts": [{"tool": "web_search", "count": 1, "errors": 0}],
 "handoffs_from": [{"agent": "orchestrator", "kind": "delegate", "count": 1}],
 "handoffs_to": [{"agent": "orchestrator", "kind": "return", "count": 1}],
 "recent_sessions": [{"session_id": "trip-42", "team_id": "trip-planners", "last_activity": "2026-11-03T09:16:04.872650Z"}]}
```

### Teams

`GET /api/v1/teams` lists teams: `team_id`, `name`, `auto` (a derived
team), `agents`, `session_count`, `error_session_count`, `handoff_count`,
`span_count`, `input_tokens`, `output_tokens`, `cost_usd`, `first_seen`,
`last_activity`, `detection_count`. Filters: `q`, `agent`, `from`, `to`.
`sort_by`: `last_activity` (default, desc), `first_seen`, `session_count`,
`handoff_count`, `error_session_count`, `cost_usd`, `name`, `team_id`.

`GET /api/v1/teams/{id}` adds `agent_stats` (per agent: `calls`, `errors`,
`tool_calls`, `tokens`, `cost_usd`, `sessions`, `handoffs_in`,
`handoffs_out`, `detections`), `graph` (the session graph aggregated over the
team's sessions; nodes also carry `sessions`), `handoff_stats` (the graph's
non-return edges, most frequent first: `count`, `error_rate`, `p50_ms`,
`p95_ms`, `mean_score`) and `recent_sessions` (newest 50 session summaries).

```json
{"team_id": "trip-planners", "name": "Trip planners", "auto": false, "agents": ["orchestrator", "researcher"], "session_count": 1,
 "error_session_count": 0, "handoff_count": 1, "cost_usd": 0.006525, "last_activity": "2026-11-03T09:16:06.572650Z",
 "agent_stats": [{"agent": "orchestrator", "calls": 1, "errors": 0, "tool_calls": 1, "tokens": 810, "cost_usd": 0.00495, "sessions": 1,
   "handoffs_in": 0, "handoffs_out": 1, "detections": {}}],
 "handoff_stats": [{"from": "orchestrator", "to": "researcher", "kind": "delegate", "count": 1, "errors": 0, "error_rate": 0,
   "p50_ms": 1300, "p95_ms": 1300, "mean_score": 0.75, "scores": {"HandoffQualityJudge": {"mean": 0.75, "count": 1}}}],
 "graph": {"nodes": ["…"], "edges": ["…"]},
 "recent_sessions": ["… session summaries …"]}
```

### Hand-offs

`GET /api/v1/handoffs` queries hand-offs, oldest first. Filters:
`session_id`, `from_agent`, `to_agent`, `agent` (either end), `team_id`,
`kind`, `from`, `to`. `sort_by`: `started_at` (default), `duration_ms`,
`from_agent`, `to_agent`, `kind`, `status`, `session_id`.

```json
{"total_count": 2, "items": [
  {"id": "459997d98657e7e6c51106885dd22d25", "session_id": "trip-42", "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
   "source_span_id": "00000000000000a4", "from_span_id": "00000000000000a1", "to_span_id": "00000000000000a4",
   "from_agent": "orchestrator", "to_agent": "researcher", "kind": "delegate", "source": "nested",
   "started_at": "2026-11-03T09:16:03.572650Z", "ended_at": "2026-11-03T09:16:04.872650Z", "duration_ms": 1300, "status": "ok",
   "input_preview": "Find three facts about Oslo", "output_preview": "1. Capital of Norway 2. On the Oslofjord 3. Nobel Peace Prize",
   "team_id": "trip-planners"}
]}
```

`from_span_id` is the delegating side (the caller's invocation, or the
hand-off span), `to_span_id` the receiving invocation (`null` until it is
known), `source_span_id` the span the edge was found on. `status` is
`running`, `ok` or `error` from the receiving invocation (else the hand-off
span). Previews are the first 500 characters of the receiving agent's input
and output.

### Scores

Scope scores are feedback rows whose payload carries `scope` (`session`,
`agent`, `handoff`, `turn`, `span`), the target ids (`session_id`, `agent`,
`handoff_id`, `turn_id`, `span_id`), `scorer`, `value` (a number; booleans
are 1 or 0), `reason`, and optionally `output` and `model`. The server's
scorers write them with `feedback_type` `weft.score.<scorer>`, attached
(`ref`) to the target's first span; SDKs can write their own with
`/feedback/create` and any feedback type, as long as the payload has a valid
`scope`.

`GET /api/v1/scores` lists them, newest first. Filters: `scope`,
`session_id`, `agent`, `handoff_id`, `scorer`. With `session_id`, call-level
scorer results (`weft.scorer.*` feedback on the session's spans) are
included as `span` scores.

```json
{"total_count": 4, "items": [
  {"id": "01a1a0b1-ae46-78b2-b0ee-9baf12ebad2e", "scope": "handoff", "session_id": "trip-42", "handoff_id": "459997d98657e7e6c51106885dd22d25",
   "scorer": "HandoffQualityJudge", "value": 0.75, "reason": "…", "model": "mock/json",
   "output": {"appropriate": true, "result_used": true, "score": 0.75, "reasoning": "…"},
   "ref": "weft:///demo/agents/call/00000000000000a4", "feedback_type": "weft.score.HandoffQualityJudge",
   "creator": null, "created_at": "2026-11-03T09:16:07.478592Z"}
]}
```

`POST /api/v1/scores/run` runs scorers at a scope and stores the scores.
It runs synchronously: at most 10 scorers and 100 targets per request.

```json
{"project_id": "demo/agents", "scope": "handoff", "scorers": ["HandoffQualityJudge"],
 "handoff_ids": ["459997d98657e7e6c51106885dd22d25"], "model": "mock/json"}
```

| Field | Meaning |
|---|---|
| `scope` | `session`, `agent` or `handoff` |
| `scorers` | scorer classes by name (the judges below, `ToolSafetyScorer`, or any server-runnable class that needs no configuration), or scorer refs |
| `session_ids` | the sessions to score (required, except for `handoff` with `handoff_ids`) |
| `agent` | `agent` scope: only this agent (default: every agent of each session) |
| `handoff_ids` | `handoff` scope: these hand-offs (default: every non-return hand-off of the sessions) |
| `model` | judge model for the built-in judges (default `gpt-4o-mini`; `mock/json` works offline) |

The response is `{items, total_count}` with one result per target and
scorer: `scope`, `session_id`, `agent`, `handoff_id`, `scorer`, `value`,
`reason`, `output`, `feedback_id`, `scorer_call_id` (the judge's own call,
marked internal), or `error`.

`HandoffQualityJudge` runs at the `handoff` scope only, `GoalCompletionJudge`
at `session`, `ReasoningConsistencyJudge` and `ToolSafetyScorer` at
`session` and `agent`; other classes run at any scope. Each scorer also gets
`session_id`, `agent` and `handoff_id` in its example row. A result's `value`
is the output's `value`, `score`, `safe`, `passed`, `pass` or `correct`
field (booleans as 1 or 0), else its first numeric or boolean field.

What each scope's scorer sees (`{input}` / `{output}` in judge prompts):

| Scope | `{input}` | `{output}` |
|---|---|---|
| `session` | the user's requests | the whole timeline as a transcript, ending with the final answer |
| `agent` | the task handed to the agent (or the user's request) | the agent's reasoning, model replies, tool calls and hand-offs |
| `handoff` | the user's request, what the delegating agent did before the hand-off, and the hand-off input | what the delegate returned and what the delegating agent did next |

Built-in judges (JSON mode; `score` is 0..1, and a generic
`{score: 1-5, correct}` reply is mapped onto these fields):

| Scorer | Scopes | Output |
|---|---|---|
| `HandoffQualityJudge` | `handoff` | `appropriate`, `result_used`, `score`, `reasoning` |
| `ReasoningConsistencyJudge` | `agent`, `session` | `consistent`, `score`, `reasoning` |
| `GoalCompletionJudge` | `session` | `goal_achieved`, `score`, `reasoning` |

The three are also server-runnable scorer classes (`_class_name`), with the
optional fields `model`, `model_id` and `scoring_prompt`, so they can be
stored as scorer objects and run on calls or by monitors.

### Test session

`POST /api/v1/demo/session` with `{"project_id": "…"}` (member role) writes a
sample session into that project, for the UI's "Send a test session" button:
a `lead` agent delegates to a `coder` agent (team `demo-team`), both reason
before acting, the coder's piped install script draws an AgentShield `block`
(linked to its tool call) and it recovers with a safer command. The spans go
through the normal OTLP ingest and the verdict through the detection store.
Each call writes a new session and returns
`{project_id, session_id, trace_id, spans, detections}`.

### Guides

`GET /api/v1/help` → `{"items": [{"name": "README.md", "title"}, ...]}` lists
the guides in `docs/weft` that are built into the server; `GET
/api/v1/help/{name}` → `{"name", "title", "markdown"}` returns one (404 for
other names). The UI shows them at `/help/<name>`, so they match the running
version and work offline.

## AgentShield detections

AgentShield verdicts stored as detections (DESIGN.md §2, §5). Every request
names `project_id` (query for GET, body for POST); GET needs the viewer role,
POST the member role. Times are RFC 3339 (or unix seconds in `from` / `to`).

**Detection object**

| Field | Meaning |
|---|---|
| `id` | Weft's id |
| `event_id` | the engine's evaluation event id (unique per `source`); `replay:<label>:<span_id>` for replays |
| `session_id` | AgentShield `session_id` = `gen_ai.conversation.id` |
| `span_id`, `trace_id` | the tool span the verdict concerns, or null |
| `link` | how `span_id` was found: `tool_call_id`, `nearest_time` (same session and tool name, start within ±5 s), `replay`, or `session` / `none` when unlinked |
| `engine_span_id` | the engine's `evaluate` span when the verdict came over OTLP |
| `tool_call_id`, `tool_name`, `agent_name` | from the verdict; `agent_name` is filled from the linked span when the engine had none |
| `action` | `allow`, `log`, `require_approval`, `block`. For `source=replay`, the action enforce mode would take |
| `mode` | `enforce`, `audit`, `shadow` (replays are always `shadow`) |
| `severity` | highest rule level, or `none` |
| `rules[]` | `{id, title, level, tags[]}` |
| `source` | `engine` or `replay` |
| `ruleset` | replay label (default the rules hash, `rules:<12 hex>`, or `remote:<host>`) |
| `event_source` | the platform that sent the tool call (`claude-code`, `openclaw`, …) |
| `overridden`, `overridden_at` | the user overrode the verdict (forwarded from the engine's `/api/v1/override`) |
| `replay_job_id` | the replay job that produced it |
| `created_at` | verdict time (replays: the tool span's start); `ingested_at` |

### `GET /api/v1/detections`

Filters: `session_id`, `agent`, `action` (comma-separated or repeated),
`rule_id`, `source`, `ruleset`, `span_id`, `tool_call_id`, `tool_name`,
`overridden` and `linked` (`true`/`false`), `from`, `to` (on `created_at`).
`limit` (default 100, max 1000), `offset`, `sort_by` = `created_at` (newest
first, default), `-created_at`, `severity` or `action`. Returns
`{items: [detection], total_count}`. Each item also has `session_title`
(the session's name, else its first user request; absent when it has
neither), so lists can label sessions by more than their id. The session
timeline merges these by `span_id` (or `tool_call_id`).

### `GET /api/v1/detections/stats`

Same filters. Returns `{total, by_action, by_severity, by_source, overridden,
linked, by_rule: [{rule_id, title, level, count, by_action}], by_agent:
[{agent, count, by_action, tool_calls, detection_rate}], by_day: [{day, count,
by_action, by_severity}]}`. `detection_rate` is the share of the agent's
recorded tool calls that drew a non-`allow` engine verdict (null without
spans). Pass `source=engine` to leave replays out of the counts.

### `POST /api/v1/shield/ingest`

The engine's `weft:` push (`internal/weftpush`). Body:

```json
{"project_id": "team/app",
 "detections": [{"event_id": "e-1", "session_id": "s-1", "tool_call_id": "toolu_01", "tool_name": "Bash",
   "agent_name": "coder", "action": "block", "mode": "enforce", "severity": "critical",
   "rules": [{"id": "c65a9620-…", "title": "Remote Code Execution via Piped Script Download", "level": "critical", "tags": ["attack.execution"]}],
   "source": "engine", "event_source": "claude-code", "timestamp": "2026-11-03T09:21:20Z"}],
 "overrides": [{"event_id": "e-1", "session_id": "s-1", "timestamp": "2026-11-03T09:21:25Z"}]}
```

`detection` (one object) is accepted in place of `detections`; `id` is an
alias of `event_id`, and rules may use `rule_id` / `rule_name` / `severity`.
At most 1000 detections and 1000 overrides per request. Validation: `event_id`
required; `action`, `mode`, `severity` and rule levels from the lists above;
`source` must be `engine`; ids and names at most 256 bytes without control
characters. Re-sending an `event_id` updates the same detection (idempotent);
`overridden` never reverts. An override without `event_id` marks the session's
latest `block` / `require_approval`; one whose detection has not arrived yet
is kept and applied when it does. Detections whose `event_id` starts with
`weft-replay-` (remote replay traffic) are ignored. Response:

```json
{"accepted": 1, "created": 1, "duplicates": 0, "ignored": 0,
 "overrides_applied": 1, "overrides_pending": 0,
 "ids": [{"id": "01a1a0b7-26ce-765f-8d58-c15ed46fd607", "event_id": "e-1", "created": true}]}
```

Over OTLP, spans of instrumentation scope `agentshield` (or with
`agentshield.verdict.action`) become detections from the attributes listed in
[README](README.md#agentshield-integration) and their
`agentshield.rule.matched` events; engines that predate them (`verdict.action`,
`rule.matched` events with `rule.name` / `rule.severity`) are understood too.
The event id is the span's `event.id` attribute, so a verdict sent both ways
is stored once.

### `POST /api/v1/shield/replay`

```json
{"project_id": "team/app", "session_ids": ["s-1"], "label": "candidate",
 "rules_dir": "/srv/rulesets/candidate", "event_source": "claude-code",
 "filter": {"agent": "coder", "from": "2026-11-01", "to": "2026-11-03", "limit": 5000}}
```

`session_ids` (at most 1000) or a `filter` (agent and/or time range on the
tool spans' start; `limit` tool calls, default and maximum 10000) is
required. `rules_dir` must lie under `--shield-rules-dir` or a
`--shield-rules-roots` directory (in-process only). `event_source` is the
`source` rules see when the engine recorded none for a call (default `weft`).
Returns `{job_id, status: "queued"}`; 400 when replay is not configured.

Each recorded `execute_tool` span (tool name, `gen_ai.tool.call.arguments` or
the call's inputs) is turned into the request a shipped plugin would send
(`internal/replay` field mapping) and evaluated in shadow mode with a fresh
behavioural session per recorded session. Calls with a matched rule are
stored as `source=replay` detections under the label (a re-run with the same
label replaces them).

### `GET /api/v1/shield/replay/{job_id}`

```json
{"job_id": "01a1a0b7-…", "status": "done", "label": "candidate", "ruleset": "rules:87735dfe7d4c",
 "engine": "in-process", "rules_loaded": 2, "session_ids": ["s-1"],
 "created_at": "…", "started_at": "…", "finished_at": "…", "error": null, "request": {…},
 "summary": {"tool_calls": 3, "evaluated": 3, "errors": 0, "detections": 2, "blocked": 2,
   "approvals_required": 0, "with_engine_verdict": 0, "newly_blocked": 2, "no_longer_blocked": 0,
   "changed": 0, "unchanged": 1},
 "newly_blocked": [{"span_id": "1e8f98db0bc28171", "trace_id": "…", "session_id": "s-1",
   "tool_call_id": "toolu_r2", "tool_name": "Bash", "agent_name": "coder", "started_at": "…",
   "before": null,
   "after": {"action": "block", "severity": "critical", "rule_ids": ["c65a9620-…"], "mode": "shadow"}}],
 "no_longer_blocked": [], "changed": [], "truncated": false}
```

`status` is `queued`, `running`, `done` or `failed` (with `error`). `before`
is the engine's verdict linked to the same span (null: none recorded, treated
as `allow`); an `audit` or `shadow` verdict is compared by the action enforce
mode would have taken for its rules. `newly_blocked`: `after` blocks and
`before` did not; `no_longer_blocked`: the reverse; `changed`: a different
action or rule set otherwise. Each list holds at most 500 items
(`truncated`). `GET /api/v1/shield/replay` lists jobs (`{items, total_count}`,
results omitted).

### `GET /api/v1/shield/insights`

Query: `from`, `to`, `bucket` (`hour` / `day`), `fatigue_threshold` (default
5), `escalation_threshold` (default 3), `window` (default `10m`), `limit`
(default 50). Over engine detections other than `allow`:

- `approval_fatigue`: `{threshold, series: [{bucket, require_approval,
  overridden}], by_session: [{session_id, session_title?, agents, require_approval, approved,
  first_at, last_at, per_hour, fatigued}], by_agent: [{agent,
  require_approval, approved, sessions, approval_rate}]}`; `approved` counts
  overridden approvals.
- `override_escalation`: `{threshold, series: [{bucket, overrides,
  overridden_blocks}], by_session: [{session_id, session_title?, agents, overrides,
  overridden_blocks, overridden_approvals, rule_ids, first_at, last_at,
  escalating}]}`; escalating at the threshold or two overridden blocks.
- `cross_agent`: per team (`weft.team.id` on a span of the session, else
  `auto:` + the sorted agent names joined by `+`; teams of one agent are left
  out): `{team_id, agents, sessions, detections, agents_with_detections,
  shared_rules: [{rule_id, title, level, agents, count}], incidents: [{start,
  end, agents, rule_ids, session_ids, detections, max_action}], correlated}`.
  A shared rule fired for two or more agents; an incident is a run of
  detections no more than `window` apart involving two or more agents.

### `GET /api/v1/shield/status`

`{replay_enabled, replay_engine: "in-process" | "remote" | "", correlation_window_ms}`.

### `ToolSafetyScorer`

A server-runnable scorer class (`_class_name: "ToolSafetyScorer"`). Fields:
`scope` (`session` / `agent`), `session_id`, `agent`, `source` (`engine` /
`replay`), `ruleset`, `replay` (run a replay first), `label`. The session and
agent also come from the example row (`session_id` / `conversation_id`,
`agent` / `agent_name`) or, on `/calls/score` and monitors, from the scored
call's session. Result: `{scope, session_id, agent?, source, ruleset?,
tool_calls, detections, blocked, approvals_required, logged, overridden,
max_severity, max_severity_score (0..1), safe}`.
