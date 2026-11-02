# W&B Weave vs Weft: feature parity audit

**Date:** 2026-11-02. **Compared:** W&B Weave as documented and served in late 2026, and Weft at commit `b233e51` (`internal/weft/**`, `web/weft-ui/src/**`, `sdk/python/src/weft/**`, `sdk/typescript/src/**`).

**Sources.**
- Weave docs: `https://docs.coreweave.com/products/wandb/weave/` (page index: `https://docs.coreweave.com/_llms/en/forge/weights-and-biases.txt`).
- The live trace-server API spec: `https://trace.wandb.ai/openapi.json` (127 method/path pairs).
- The official client's generated server SDK, `weave/vendor/weave_server_sdk` from `weave` 0.54.0 (118 method/path pairs in 29 resource groups).

**Method.**
1. Each feature row was checked against the current code. A row is **Impl** only when the code was found and, where practical, a test. **Partial** says what is missing. **Missing** means no code was found. Docs were not taken as evidence.
2. Endpoint coverage was computed by a script. It extracts every path from the vendored SDK and the OpenAPI spec and matches each method and path against the `r.Get/Post/Put/Delete/Patch` registrations in `internal/weft/server/*.go`, including the `routeExtensions` files and the expanded `/v2` kinds.
3. A server built from this tree (`go build ./cmd/weft`, port 6471, scratch DB) was spot-checked:
   - `scripts/weft_weave_compat_check.py` with the official `weave` 0.54.0 client: all 10 checks passed.
   - The official `weave.start_conversation/start_turn/start_llm/start_tool` agent SDK, then `/agents/query`.
   - v2 datasets, aliases and tags, annotation queues, and agents OTLP ingest.
   - 27 response shapes compared with the OpenAPI schemas.
4. Test suites run during the audit:
   - `go test ./internal/weft/...`: pass.
   - Python SDK: 101 unit tests and 15 live-server tests pass.
   - TypeScript SDK: 44 unit tests and 5 live-server tests pass.
   - UI: 60 vitest tests pass.

Status key: **Impl** = implemented, **Partial** = present with stated gaps, **Missing** = no code found.

## Summary

| Feature group | Impl | Partial | Missing |
|---|---|---|---|
| 1. Tracing | 21 | 3 | 0 |
| 2. Integrations | 11 | 2 | 2 |
| 3. Objects and versioning | 8 | 0 | 0 |
| 4. Datasets | 6 | 0 | 1 |
| 5. Models | 4 | 0 | 0 |
| 6. Prompts | 3 | 0 | 0 |
| 7. Evaluations, scorers, guardrails | 10 | 2 | 1 |
| 8. Leaderboards | 2 | 1 | 0 |
| 9. Feedback, annotations, queues | 7 | 0 | 0 |
| 10. Monitors, signals, automations | 2 | 3 | 0 |
| 11. Agents | 4 | 3 | 1 |
| 12. Playground | 8 | 0 | 1 |
| 13. Costs | 3 | 0 | 0 |
| 14. UI views | 9 | 3 | 1 |
| 15. SDKs and client APIs | 6 | 2 | 1 |
| 16. Admin, settings, security | 6 | 1 | 2 |
| **Total (140 rows)** | **110** | **20** | **10** |

API endpoints: **127 of 127** Weave OpenAPI method/path pairs are registered in Weft (was 116 at the audit). That includes **118 of 118** in the vendored client SDK. See §17.

## 1. Tracing

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| `@op` on sync/async functions, methods, generators | tracking/create-call, trace-generator-func | Impl | `sdk/python/src/weft/_op.py`, `sdk/typescript/src/op.ts`; `sdk/python/tests/test_tracing.py`, `sdk/typescript/test/tracing.test.ts` |
| Nested calls, trace tree, summary roll-up | tracking/tracing | Impl | `internal/weft/store/calls.go`; `TestRollupInOrder`, `TestRollupOutOfOrder` (`store/store_test.go`) |
| Manual `create_call`/`finish_call` | tracking/create-call | Impl | `_client.py` `create_call`/`finish_call`; TS `client.ts` `finishCall` |
| Get current call | tracking/get-call-object | Impl | `get_current_call` (Py), `getCurrentCall` (`context.ts`) |
| Thread-pool context propagation | tracking/create-call | Impl | `weft.ThreadPoolExecutor`, `weft.Thread` (`_context.py`) |
| Display names (static, callable, post-hoc) | tracking/set-call-display | Impl | `call_display_name` in `_op.py`; `/call/update` |
| Op `kind` and `color` | tracking/ops | Impl | `_op.py` and `op.ts` options; UI `components/traces/OpKind.tsx`, `opStyle.ts`; `tests/test_core_features.py` |
| `postprocess_inputs`/`postprocess_output` | tracking/ops | Impl | `_op.py`, `op.ts` |
| Per-op `tracing_sample_rate` (Py and TS) | tracking/ops | Impl | `_op.py`, `OpOptions.tracingSampleRate` (`op.ts`) |
| Print call link | tracking/ops | Impl | `print_call_link` / `WEFT_PRINT_CALL_LINK` (`_client.py`); `test_global_attributes_print_link_and_capture_code` |
| Code-capture toggle | env-vars | Impl | `capture_code`, `WEFT_CAPTURE_CODE`, op `enable_code_capture` |
| Disable tracing: env, init, scoped context | tracking/trace-disable | Impl | `WEFT_DISABLED`, `init(disabled=)`, `weft.tracing_disabled`, TS `withTracingDisabled` |
| Per-call and global attributes | tools/attributes | Impl | `weft.attributes()`, `settings.global_attributes` (`_client.py`) |
| Threads and turns (SDK and UI) | tracking/threads | Impl | `weft.thread()`, TS `withThread`; `pages/threads/*`; `/threads/query`, `/threads/stream_query`; `TestThreadsMixedTurnIDs` |
| Media: image, audio, video, PDF, HTML, Markdown, `Content` API | core-types/media | Impl | `_content.py`, `content.ts`, `components/media/MediaView.tsx` (sandboxed HTML iframe); `media.test.ts` |
| BYOB media references (own bucket) | tracking/byob-references, agents-byob-references | Partial | server: `--files-backend s3://bucket/prefix` stores file contents in an S3-compatible bucket (SigV4, `objstore/s3.go`), `/files/resolve_url` presigns digests and `s3://` keys (`server/files_media.go`; `TestFilesS3Backend`, `objstore/s3_test.go`). The Weft SDKs do not log references to media already in a user bucket |
| Key redaction | tracking/redact-pii | Impl | `_redact.py`, `redact.ts` |
| PII redaction | tracking/redact-pii | Partial | SDK: 5 regex entities; server: 6 regex entities (`store/redact.go`, `TestSensitiveDataRedaction`). No Presidio/NER entities (PERSON, LOCATION, …) |
| OTLP ingest `/otel/v1/traces` (protobuf, JSON, gzip) | tracking/otel | Impl | `internal/weft/otlp/otlp.go`; `TestOTLPJSONOverHTTP`, `otlp_test.go` |
| OTel conventions | tracking/otel | Partial | GenAI, OpenInference, OpenLLMetry, Vercel `ai.*`, MLflow, Pydantic AI/Logfire, Vertex `gcp.vertex.agent.*` and OpenLit are mapped (`otlp/frameworks.go`, `frameworks_test.go`). Langfuse `langfuse.startTime/endTime` is not mapped |
| OTel thread and turn attributes | tracking/otel | Impl | `wandb.thread_id`, `wandb.is_turn` (`otlp.go`) |
| Python OTel helper | tracking/otel | Impl | `sdk/python/src/weft/otel.py`; live test in `test_integration_server.py` |
| Write-ahead log (durable client buffer) | tracking/write-ahead-log | Impl | `WriteAheadLog` in `_sender.py` (`WEFT_ENABLE_WAL`), `sender.ts` `walPath`; `test_core_features.py`, `features.test.ts` |
| Retry and queue settings | env-vars | Impl | `retry_max_attempts`, `retry_backoff`, `max_queue_size` settings (`_client.py`) |

## 2. Integrations

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| OpenAI (streaming, Responses API), Py and TS | integrations/openai | Impl | `integrations/openai.py`, `typescript/src/integrations/openai.ts`; `test_integrations.py` |
| Anthropic, Py and TS | integrations/anthropic | Impl | `integrations/anthropic.py`, `anthropic.ts` |
| Google GenAI, Py and TS | integrations/google | Impl | `integrations/google_genai.py`, `google_genai.ts` |
| Bedrock | integrations/bedrock | Impl | `integrations/bedrock.py`; `test_more_integrations.py` |
| Mistral, Cohere, Groq, Together | integrations/* | Impl | `integrations/other_llms.py` |
| LiteLLM | integrations/litellm | Impl | `integrations/litellm.py` |
| LangChain | integrations/langchain | Impl | `integrations/langchain.py` |
| DSPy | integrations/dspy | Impl | `integrations/dspy.py` |
| OpenAI Agents SDK | integrations/agents/openai-agents-sdk | Impl | `integrations/openai_agents.py` |
| Vercel AI SDK (TS) | integrations/vercel_ai_sdk | Impl | `integrations/vercel_ai.ts` middleware, plus OTel `ai.*` mapping |
| OpenAI-compatible clients (OpenRouter, local, Azure) | integrations/openrouter, local_models, azure | Impl | `patch_openai_compatible`, TS `wrapOpenAICompatible`. Azure is not tested |
| Autopatch control | integrations/autopatching | Partial | `init(autopatch=False)` and `unpatch_all`; no per-integration settings |
| LlamaIndex, CrewAI, smolagents, Instructor, Haystack, AutoGen, Agno, MCP, Claude Agent SDK, Google ADK, Pydantic AI, Koog, verifiers/verl, NIM, Cerebras, HF | integrations | Partial | no native patches. These work only through their OTel/OpenInference instrumentors, whose spans are mapped |
| OpenAI Realtime audio | integrations/openai-realtime-audio | Missing | no code |
| Agent harness plugins (Claude Code, Codex, OpenClaw, Pi) | integrations/agents/*-harness | Missing | no Weft plugins (the repo's AgentShield plugins do not target Weft) |

## 3. Objects and versioning

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Publish/get, content-addressed versions (Weave-identical digests) | tracking/objects | Impl | `/obj/create` (`store/objects.go`), `_canonical.py`, `canonical.ts`; `TestObjectsAndRefs`; compat script |
| Refs (`/attr`, `/key`, `/index`, `/id`), `refs/read_batch` | tracking/objects | Impl | `internal/weft/refs/refs.go` and its tests |
| Delete object or version (SDK, API, UI) | tracking/objects | Impl | `delete_object_version(s)` (`_client.py`), TS `deleteObjectVersion(s)`; `TestDeleteCascades` |
| Aliases (set, remove, list, resolve) | tracking/objects | Impl | `server/tags.go` (`PUT /objs/{id}/aliases`, `GET /aliases`); `set_aliases`/`get_aliases`; `TestTagsAndAliases` |
| Version tags | core-types/prompts-version | Impl | `PUT /objs/{id}/versions/{digest}/tags`, `GET /tags`; `components/objects/TagEditor.tsx` |
| Version list and diff | tools/comparison | Impl | `pages/objects/ObjectVersionsPage.tsx`, `ObjectDiffPage.tsx`, `JsonDiff.tsx` |
| Load a published op as runnable code | tracking/objects | Impl | `_loader.py`, `loader.ts` |
| v2 REST for ops, datasets, models, scorers, evaluations | OpenAPI | Impl | `server/v2.go`; `TestV2ObjectsCRUD` |

## 4. Datasets

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Dataset object, row tables, paging | core-types/datasets | Impl | `_object.py` `Dataset`; `/table/*` (`store/tables.go`) |
| `from_calls`, `from_pandas`/`to_pandas`, `add_rows` | core-types/datasets | Impl | `_object.py`; `test_objects.py` |
| `from_hf`/`to_hf`, `select()` | core-types/datasets | Missing | not in `_object.py` |
| UI create from CSV/JSON, edit/append/delete rows | core-types/datasets | Impl | `CreateDatasetModal.tsx`, `DatasetRowsView.tsx`, `csv.ts` |
| Add calls to a dataset with field mapping | core-types/datasets | Impl | `components/traces/AddToDatasetModal.tsx` |
| Dataset lineage (`/dataset_sources/*`) | OpenAPI | Impl | `server/compat_extra.go`, `store/datasetsources.go`, `DatasetSources.tsx`; `TestCompatExtraEndpoints` |
| `table/create_from_digests`, `table/query_stats_batch` | OpenAPI | Impl | `compat_extra.go`; `TestCompatExtraEndpoints` |

## 5. Models

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| `Model` with `predict`, versioned attributes | core-types/models | Impl | `_object.py`, `objects.ts` |
| `LLMStructuredCompletionModel` (saved playground models) | tools/playground | Impl | `_object.py`; `evals/evaluate.go` |
| Models list and version pages | core-types/models | Impl | route `models` in `App.tsx`, `ObjectVersionPage.tsx` |
| v2 `/models` REST | OpenAPI | Impl | `server/v2.go`; `TestV2ObjectsCRUD` |

## 6. Prompts

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| `StringPrompt`, `MessagesPrompt`, `format(**params)` | core-types/prompts | Impl | `_object.py`; TS exports in `index.ts` |
| Prompt versions with aliases and tags | core-types/prompts-version | Impl | `tags.go`; `TestTagsAndAliases` uses `objs/prompt/aliases` |
| Open a prompt in the playground | tools/playground | Impl | `PlaygroundPage.tsx` (`?prompt=`, `loadPrompt`) |

## 7. Evaluations, scorers, guardrails

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| `Evaluation` (dataset, scorers, trials, preprocess, parallelism) | core-types/evaluations | Impl | `_evaluation.py`, `evaluation.ts`; `test_evaluation.py` |
| Function and class scorers, `summarize`, `column_map` | evaluation/scorers | Impl | `scorers.py`, `scorers.ts` |
| Built-in LLM and rule scorers | evaluation/builtin_scorers | Impl | server-runnable ExactMatch, Contains, Regex, JSONValid, Levenshtein, LLMAsAJudge (`evals/scorers.go`), HallucinationFree, Summarization, Relevance, ValidXML, JSONSchema, OpenAIModeration, EmbeddingSimilarity, Rule (`evals/scorers_extra.go`); `TestExtraScorers` |
| Local SLM scorers (toxicity, bias, coherence, fluency, context relevance, Presidio) | evaluation/weave_local_scorers | Missing | no code |
| `EvaluationLogger` (`log_prediction`, `log_score`, `log_example`, `log_summary`, dataset link) | evaluation/evaluation_logger | Impl | `_evaluation.py` `EvaluationLogger`, TS `logExample` |
| Link existing calls or agent traces to an EvaluationLogger run | evaluation/evaluation_logger, agent-evals | Partial | the logger always creates new predict calls; no way to attach existing calls |
| Evaluate from the UI on the server | tools/evaluation_playground | Partial | `/evaluate_model` runs `LLMStructuredCompletionModel`, any model with a `predict_url`, and models bound to a custom runtime (`evals/runtimes.go`; `TestEvaluateModelWithPredictURL`, `TestEvaluateModelBoundToRuntime`). The UI picker (`NewEvaluationPage.tsx`) still lists only `LLMStructuredCompletionModel` models |
| Re-score an evaluation | OpenAPI `/evaluations/rescore` | Impl | `evals/rescore.go`; `TestCompatExtraEndpoints` |
| Evaluation status | OpenAPI | Impl | `/evaluation_status`, `/evaluations/status` |
| Compare evaluations (summary, per-example diffs, baseline) | evaluation/compare_evals | Impl | `CompareEvaluationsPage.tsx`, `/evaluations/compare`; `TestServerSideEvaluationCompareLeaderboard` |
| Evaluations table with filters and saved views | evaluation/compare_evals | Impl | `EvaluationsPage.tsx` (`SavedView`), `EvalRunsTable.tsx` |
| Eval export REST (v2 evaluation_runs, predictions, scores, eval_results/query) | evaluation/export_eval | Impl | `server/v2.go`; `TestV2EvaluationFlow` |
| Guardrails (`apply_scorer`) and scoring existing calls | evaluation/guardrails | Impl | `Call.apply_scorer` (`_call.py`), TS `applyScorer`; `/calls/score`, `TestScoreCallsOnDemand` |

## 8. Leaderboards

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Leaderboard objects, compute API, page and editor | core-types/leaderboards | Impl | `LeaderboardEditor.tsx`, `LeaderboardPage.tsx`, `/leaderboard/compute`; `TestServerSideEvaluationCompareLeaderboard` |
| Leaderboard SDK | core-types/leaderboards | Impl | `weft.Leaderboard`, `client.leaderboard()` |
| Dynamic leaderboard inside eval comparison | evaluation/dynamic_leaderboards | Partial | `InlineLeaderboard.tsx`. The configuration lives only in the URL (`?lb=`) and is not saved as an object |

## 9. Feedback, annotations, queues

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Reactions and notes (UI and SDK) | tracking/feedback | Impl | `FeedbackPanel.tsx`; `CallFeedback` (`_call.py`) |
| Human annotation specs (int, number, enum, bool, string) | tracking/feedback | Impl | `AnnotationSpec`; Settings → Annotations |
| Query, replace, purge feedback; filter calls by feedback | tracking/feedback | Impl | `/feedback/*`; `TestFeedbackFieldQuery` |
| Feedback stats, aggregate, batch create, payload schema | OpenAPI | Impl | `compat_extra.go`, `store/feedback_ext.go`; `TestCompatExtraEndpoints` |
| Annotation queues API (create, items, progress, stats) | tracking/annotation-queues | Impl | `server/queues.go`, `store/queues.go`; `TestAnnotationQueues` |
| Add traces to a queue; queue progress; export to dataset | tracking/annotation-queues | Impl | `AddToQueueModal.tsx`, `QueuesPage.tsx`, `ExportQueueModal.tsx`; `queueUtils.test.ts` |
| Focused one-item-at-a-time reviewer | tracking/annotation-review | Impl | `pages/queues/QueueReviewPage.tsx` |

## 10. Monitors, signals, automations

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Custom monitors (ops, filter, sampling, scorers → feedback) | evaluation/custom-monitors | Impl | `evals/monitors.go`, `MonitorEditor.tsx`; `TestMonitorScoresEndedCalls` |
| Preset signals | evaluation/monitors, view-agent-signals | Partial | 12 monitor templates (`components/monitors/signals.ts`, `SignalsGallery.tsx`). They do not match Weave's turn-scoped presets: no jailbreak or NSFW tags, and no user-satisfaction, good-intent or response-quality ratings |
| Custom tag signals | create-custom-signal | Partial | any LLM-judge monitor can be built; no tag/rating schema or normalisation |
| Remote scorers | evaluation/remote-scorers, remote-scorer-signals | Partial | `RemoteScorer` (`evals/scorers_extra.go:464`) with bearer or header secrets; no OAuth |
| Automations (score threshold, error rate, feedback created → webhook/Slack, run log) | evaluation/automations | Impl | `internal/weft/automations/automations.go`, `server/automations.go`, `AutomationsPage.tsx`; `TestAutomationScoreThresholdAndFeedback`, `TestAutomationErrorRate` |

## 11. Agents

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Agent tracing SDK (`start_conversation/turn/llm/tool/subagent`, batch logging) | tracking/trace-agents, trace-sub-agents, trace-agents-batch | Missing | none in either Weft SDK. The official `weave` 0.54 conversation SDK works against Weft (verified: 3 spans → 1 agent, 1 LLM call and 1 tool call in `/agents/query`) |
| Agents OTLP endpoint | tracking/trace-agents-otel | Impl | `POST /agents/otel/v1/traces` (`server/agents.go`) |
| Span-kind derivation (GenAI, OpenInference, OpenLLMetry, SDK `kind`) | trace-agents-attributes | Impl | `store/agents.go`; `TestDeriveSpanKinds`, `TestAgentsFromSDKCalls` |
| `/agents/*` API (query, versions, search, spans query/stats, custom-attr schema, conversations, chat) | OpenAPI | Impl | `server/agents.go`, `store/agents_*.go`; `server/agents_test.go` (7 tests); response shapes match the spec |
| Agents list, agent page, conversation page | tracking/view-agent-activity | Impl | `pages/agents/AgentsPage.tsx`, `AgentPage.tsx`, `ConversationPage.tsx`; `agents.test.ts` |
| Agents view: project Dashboard, global Spans tab, Signals tab | tracking/view-agent-activity | Partial | per-agent charts (`AgentCharts.tsx`) only; no project-wide dashboard, no cross-agent span table, and signals sit under Monitors |
| Agent evals | agent-evals | Partial | evaluations work on calls; no linking of agent conversations to eval runs |
| Agents BYOB references | agents-byob-references | Partial | see §1 |

## 12. Playground

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Built-in providers | tools/playground | Impl | `internal/weft/llm/llm.go`, `/llm/models` |
| Custom OpenAI-compatible providers | tools/custom-runtimes | Impl | `Provider` objects (`server.go` `customProvider`); Settings → Secrets & providers |
| Params, tools, JSON mode, streaming | tools/playground | Impl | `PlaygroundPage.tsx`, `/completions/create_stream` |
| Side-by-side model comparison | tools/playground | Impl | `PlaygroundPage.tsx` panes |
| Load from call or prompt; template variables | tools/playground | Impl | `fillVars`, `?call=`/`?prompt=` |
| Save as model | tools/playground | Impl | saves an `LLMStructuredCompletionModel` |
| Custom runtimes REST (`PUT /v2/{e}/{p}/runtimes/{name}`) | tools/custom-runtimes | Impl | `server/runtimes.go`: a `Provider` object plus one `ProviderModel` per runtime ID; `TestCustomRuntimeApply`; official `client.register_custom_runtime` verified |
| History of past playground runs | tools/playground | Missing | runs are logged as calls but there is no history panel |
| Image generation (`/image/create`) | OpenAPI | Impl | `server/files_media.go`, `llm/images.go` (OpenAI-compatible `/images/generations`, offline `mock/*`); `TestImageCreateMock`, `TestImageCreateOpenAICompatible`. No playground UI for it |

## 13. Costs

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Automatic costs with roll-up (including OTel spans) | tracking/costs | Impl | `store/costs.go`; `TestDefaultPriceLookup`, `TestRootCostsNotDoubled`; checked on the live server |
| Custom costs add, query, purge (SDK, API, UI) | tracking/costs | Impl | `/cost/*`; `add_cost`/`query_costs`/`purge_costs`; Settings → Costs |
| `/calls/usage`, `/trace/usage` | OpenAPI | Impl | `compat_extra.go`; response shape matches the spec |

## 14. UI views

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Traces table: filters, query builder, sort, column hide, page size, bulk delete | tracking/trace-tree | Impl | `TracesPage.tsx` (`PAGE_SIZES`), `FilterBuilder.tsx` |
| Column pinning | tools/saved-views | Missing | no code |
| Saved views (traces and evaluations) | tools/saved-views | Impl | `SavedView` objects in `TracesPage.tsx`, `EvaluationsPage.tsx` |
| Trace view modes | tracking/trace-tree | Partial | tree, timeline and graph (`TraceTree.tsx`, `Timeline.tsx`, `TraceGraph.tsx`); no flame graph or code-composition view |
| Call tabs (Call, Feedback, Scores, Summary, Attributes, Code, Use, Raw) | tracking/trace-tree | Impl | `CallPage.tsx:25`, `OpCodeView.tsx` |
| Chat view for LLM calls | tracking/view-call | Impl | `ChatView.tsx` |
| Default trace plots | tracking/trace-plots | Impl | `CallCharts.tsx`, `/calls/stats` |
| Custom charts and dashboards | tracking/trace-plots | Partial | `pages/dashboards/*`, `/calls/metrics` (aggregations, group-by; `TestCallsMetrics*`). Chart types are line, bar, stacked, area, table and stat; no scatter |
| Server-side export (CSV, JSON, JSONL) with Python/cURL snippets | tracking/querying-calls | Impl | `server/export.go`, `ExportModal.tsx`, `codeSnippets.ts`; `TestCallsExportCSVFlatten`, `TestCallsExportStreamingFormats` |
| Compare calls (baseline, diff vs baseline, diff-only) | tools/comparison | Partial | `CompareCallsPage.tsx`; no numeric diff-format options |
| Object and version comparison | tools/comparison | Impl | `ObjectDiffPage.tsx` (split and unified) |
| Projects page with stats | platform/weave-projects | Impl | `ProjectsPage.tsx`, `/projects`, `/project/stats` |
| Threads list and chat view | tracking/threads | Impl | `ThreadsPage.tsx`, `ThreadPage.tsx` |

## 15. SDKs and client APIs

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Python `get_calls`/`get_call`/`delete_calls`, `CallsIter` | tracking/querying-calls | Impl | `_client.py`; `test_client.py` |
| Python call helpers (children, feedback, delete, `apply_scorer`) | tracking/update-call | Impl | `_call.py` |
| Alias, tag and object-delete APIs (Py and TS) | tracking/objects | Impl | `_client.py` and `client.ts` `setAliases`/`addTags`/`deleteObjectVersion` |
| Official `weave` Python client against Weft | n/a | Impl | `scripts/weft_weave_compat_check.py`: 10/10 checks pass with weave 0.54.0; `wandbcompat.go` (ref rewrite, `/graphql` shim, `/v2/.../calls/complete`); `TestGraphQLShim`, `TestCallsCompleteCumulativeSummaries` |
| `get_evaluations`, `evaluate_model`, compare, leaderboard helpers | SDK | Impl | `_client.py` |
| W&B login shim | n/a | Impl | `/graphql` (`wandbcompat.go`) |
| TypeScript SDK breadth | reference/typescript-sdk | Partial | has calls, ops, objects, evals, platform objects, WAL and four integrations; lacks `deleteCalls`, feedback query, cost query/purge and an OTel helper |
| SavedView SDK query/apply API | tools/saved-views | Partial | `SavedView` is a plain object (`_platform.py:139`) with no query or apply methods |
| Client-side server-response cache | platform/server-caching | Missing | no code |

## 16. Admin, settings, security

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Projects: implicit create, list, stats, delete | platform/weave-projects | Impl | `/project/delete` (`server/admin.go`, `store/projectadmin.go`); `TestProjectDelete` |
| API keys: create, list, revoke, hashed, project-scoped, read-only, admin | platform | Impl | `store/apikeys.go`, `server/admin.go`, Settings → API keys; `TestAPIKeysScopes` |
| Provider secrets encrypted at rest | tools/playground | Impl | AES-256-GCM (`store/secrets.go`); `TestSecretsEncryptedAtRest` |
| Retention (TTL) settings and purge | OpenAPI ttl_settings | Impl | `/project/ttl_settings/*`; `TestTTLPurge` |
| Server-side ingest sampling | platform/ingest-sampling | Impl | `/project/ingest_sampling_settings/*`; `TestIngestSampling` |
| Server-side sensitive-data redaction | OpenAPI sensitive_data_settings | Impl | `/project/sensitive_data_settings*`, `store/redact.go`; `TestSensitiveDataRedaction` |
| Users, teams, roles, SSO | guides/platform | Partial | keys carry a user name, a project scope and an admin flag; no user accounts, teams, roles or SSO |
| Audit log | guides/platform | Missing | no code |
| Database encryption at rest | guides/platform | Missing | only provider secrets are encrypted; the SQLite file is plain |

## 17. API endpoint coverage

The audit script compared Weft's registered routes with every method/path pair in the live OpenAPI spec and the vendored server SDK.

- **OpenAPI (`trace.wandb.ai/openapi.json`): 127 / 127 covered** (116 at the audit). Every OpenAPI tag group is complete.
- **Vendored SDK (weave 0.54.0): 118 / 118 covered** (115 at the audit). The SDK has no pairs that are absent from the OpenAPI spec.
- No covered path is registered under the wrong method.

The 11 paths added after the audit, and how Weft implements them (details in [API.md](API.md#weave-api-parity-additions)):

| Method and path | Weft implementation |
|---|---|
| `PUT /v2/{entity}/{project}/runtimes/{runtime_name}` | a `Provider` object plus `ProviderModel`s, so `<runtime>/<id>` works in the playground, completions and judges; Weft kind `predict` for model predict endpoints |
| `POST /image/create` | image generation through OpenAI-compatible providers, `Provider`s and runtimes; `mock/*` offline |
| `POST /files/resolve_url` | presigned URLs from the S3 files backend, else `/file/content` URLs |
| `GET /version` | JSON version, no API key needed (it used to fall through to the UI's HTML) |
| `GET /geolocate` | deterministic local answer: the IP, `location: null`, `allowed: true` |
| `POST /link_to_registry` | project-level registry collections with version indexes and aliases; read back with `POST /registry/query` |
| `GET /inference/catalog/models`, `/inference/modelsdev/models`, `/inference/router/openrouter/models`, `/inference/analysis/artificialanalysis/models`, `/inference/nvidia/v2/hardware` | Weft's own model catalog (built-in models with list prices, plus the project's `ProviderModel`s) in each spec shape |

**Shape checks.** Path coverage does not prove schema compatibility. All 27 response shapes from the audit now match the spec, and so do the responses of the 11 added paths (checked with valid requests against a fresh server). The three audit mismatches are fixed:
- `/calls/stats` accepts Weave's request (`granularity`, `usage_metrics`, `call_metrics`, `timezone`) and returns `usage_buckets`/`call_buckets`. Requests without those fields keep Weft's shape, which the UI uses.
- `/calls/query_stats` returns `has_more` and honours `limit`.
- `/feedback/query` returns `total_count`.

**Weft-only routes.** Besides the 46 method/path pairs not in Weave listed by the audit (`/calls/query`, `/calls/metrics`, `/calls/export`, `/evaluations/compare`, `/leaderboard/compute`, `/automations/*`, `/api_keys/*`, `/secrets/*`, `/llm/models`, `/embeddings/create`, `/graphql`, `/obj/alias/*`, POST variants of the alias and tag routes, ...), Weft adds `GET /v2/{entity}/{project}/runtimes` and `POST /registry/query`.

## 18. Known differences

These are deliberate design choices, not gaps.

- **Ref scheme.** Weft stores `weft:///` refs and also accepts `weave:///`. For the official client, `weaveCompat` (`server/wandbcompat.go`) rewrites `weft:///` to `weave:///` in JSON responses. It does this when it sees the `X-Weft-Ref-Scheme: weave` header, a Stainless header, or a `weave/` or `WeaveTrace/` user agent. To rewrite, it buffers the whole response, so streamed responses to these clients arrive in one piece.
- **Single tenant.** An "entity" is only the first part of a project ID. There are no users, teams or org settings.
  - Without `--api-keys` or managed keys, the server is open; it binds to loopback by default.
  - Static `--api-keys` keys and admin keys can do everything.
  - Any key that can write to a project can use the stored provider secrets.
  - The `/graphql` endpoint is a minimal W&B login shim.
- **Storage.** Everything lives in one SQLite file (`modernc.org/sqlite`, no CGO). That means one writer at a time, no horizontal scaling, and data size bounded by one disk. Uploaded files live in the same database unless `--files-backend s3://…` sends their contents to an S3-compatible bucket.
  - TTL purge and error-rate automations run as in-process background loops.
- **Asynchronous agent index.** Agents are derived from ordinary calls through the store's span index, not stored as a separate data model.
  - In one run, `/agents/query` issued right after `weave.finish()` returned no agents. A query about two seconds later returned the agent. The cause was not determined.
- **Signals as monitors.** Weave Signals are implemented as monitor templates. They write `wandb.runnable.<scorer>` feedback on calls and are not scored per turn.
- **Server-side evaluation.** The server runs `LLMStructuredCompletionModel`s, models with a `predict_url`, and models bound to a custom runtime (OpenAI-compatible or Weft's `predict` kind). It never executes a model's Python or TypeScript `predict` code; other models must be evaluated client-side.
- **`/calls/stats`.** Weave's request fields select Weave's shape; otherwise the endpoint keeps Weft's shape (fixed metrics), which the UI uses. Grouped custom metrics are served by `/calls/metrics`.
- **Inference, registry, geolocation.** Weft has no CoreWeave inference service or W&B Registry. `/inference/*` serve Weft's own model catalog in the spec's shapes (and need an API key when auth is on), `/link_to_registry` links into Weft registry collections, and `/geolocate` answers locally without a GeoIP database.
- **SDK naming.** The SDKs are `weft.*` with `WEFT_*` environment variables, not `weave.*`. The official `weave` client is supported as a second client.

## 19. Remaining gaps

Ordered by importance.

1. **Agent tracing API in the Weft SDKs.** Add `start_conversation/start_turn/start_llm/start_tool/start_subagent` and batch logging (Python and TS). Today only the official `weave` client or raw OTel can produce agent conversations.
2. **Agents view and Signals parity.**
   - Add a project-wide Dashboard and a cross-agent Spans tab.
   - Add a Signals tab that scores turns, using Weave's preset set: frustration, jailbreak, NSFW and low-quality tags; satisfaction, intent, safety and quality ratings.
   - Normalise tags and ratings, and add OAuth for remote scorers.
3. ~~**`/calls/stats` in Weave's shape**, `has_more` on `/calls/query_stats`, `total_count` on `/feedback/query`.~~ Done.
4. **Server-side evaluation of any model.** ~~Custom runtimes and remote predict URLs~~ done (`PUT /v2/.../runtimes/{name}`, `predict_url`). Remaining: let the New Evaluation page pick such models.
5. **Native integrations.** Add LlamaIndex, CrewAI, smolagents, Instructor, MCP, Claude Agent SDK, native Pydantic AI and Google ADK, Haystack, AutoGen, Agno and OpenAI Realtime. Add agent harness plugins (Claude Code, Codex, OpenClaw). Add per-integration autopatch settings.
6. **Local SLM scorers and NER-based PII redaction.** Weave uses Presidio-style entities; add them in the SDKs and the server.
7. **Media extras.** ~~`/files/resolve_url`, `/image/create`, an S3-compatible files backend~~ done. Remaining: SDK-side BYOB references (logging media that already sits in a user bucket) and an image-generation mode in the playground UI.
8. **Evaluation extras.** Link existing calls or agent traces to EvaluationLogger runs and agent evals. Save dynamic leaderboards as objects.
9. **Dataset helpers.** Add `from_hf`/`to_hf` and `select()`.
10. **UI polish.** Add column pinning, flame-graph and code-composition trace views, scatter charts, numeric diff formats in call comparison, and a playground history panel.
11. **Platform security.** Add user accounts, teams and roles, SSO, an audit log, and optional database encryption at rest.
12. **SDK extras.** Add a client-side server-response cache, a SavedView query/apply API, and broader TypeScript coverage (`deleteCalls`, feedback and cost queries, OTel helper).
13. ~~**Minor endpoints.**~~ Done: JSON `GET /version`, and Weft implementations of `/geolocate`, `/link_to_registry` and `/inference/*`.
