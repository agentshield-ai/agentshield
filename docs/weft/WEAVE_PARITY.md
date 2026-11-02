# W&B Weave vs Weft: feature parity audit

**Date:** 2026-11-02. **Weft:** branch `feat/weft-tracing`, commit `6aa068a`, re-checked at `f1d098e`. `f1d098e` landed during the audit. It adds a `turn_count` to `/agents/spans/query` groups, used by the Agents dashboard. Every check below was run again on a build of `f1d098e`, with the same results. **Weave:** the docs and live API as served on the audit date, and the `weave` 0.54.0 Python client.

This is an independent re-audit. Every status in the previous version of this file was treated as an unverified claim and checked again against the code, the tests and a running server. Rows that changed are listed in §19.

**Sources.**
- Weave docs: `https://docs.coreweave.com/products/wandb/weave/` (page index `https://docs.coreweave.com/_llms/en/forge/weights-and-biases.txt`, 140 Weave pages). Each guide page was mapped to a row, or to §18 when it is out of scope.
- Live trace-server spec: `https://trace.wandb.ai/openapi.json`, fetched on the audit date: **129** method/path pairs. The previous audit's copy had 127; the two new pairs are the Bulk Export API.
- The official client's generated server SDK, `weave/vendor/weave_server_sdk` in `weave` 0.54.0: 118 method/path pairs. The client's `integrations/` directory was used to check integration coverage.
- Weft: `internal/weft/**`, `cmd/weft`, `web/weft-ui/src/**` (and the embedded bundle in `internal/weft/webui/static`), `sdk/python/src/weft/**`, `sdk/typescript/src/**`, `plugins/weft-*/**`, `scripts/weft_weave_compat_check.py`, `docs/weft/API.md`.

**Method.**
1. **Status rule.** **Impl** needs code plus a test or a live check in this audit. **Partial** names the gap. **Missing** means no code was found. Docs and commit messages were not used as evidence.
2. **Server.** `go build -o /tmp/weft-final ./cmd/weft`, run on `127.0.0.1:6499` (checked free first) with a fresh database in the scratchpad. It was seeded with `python3 scripts/weft_seed_demo.py --url http://127.0.0.1:6499` (project `demo/support-bot`). A second instance on port 6498 ran with `--api-keys` for access-control checks. Both were stopped at the end.
3. **Embedded UI.** A `vite build` of `web/weft-ui` gave asset names identical to `internal/weft/webui/static`, so the binary serves the current UI source.
4. **Endpoint coverage.** The earlier `endpoint_coverage.py` was re-run. It matches every vendored-SDK and live-spec method/path against the `r.Get/Post/…` registrations in `internal/weft/server/*.go`, including the `routeExtensions` and `publicRouteExtensions` files and the expanded v2 kinds. A new `live_probe.py` also sent every spec pair to the server and counted chi's 404/405 or the UI's HTML fallback as unrouted. Both methods agree.
5. **Response shapes.** The 27 cases in the earlier `shapes.py` and `shapes2.py` were re-run on the seeded project. Five of the scripts' bodies were invalid for this data (missing object, missing `after_ms`/`start`, dummy table digest, wrong rescore field), so they were re-sent with valid bodies (`shapes_all.py`).
6. **Official client.** `scripts/weft_weave_compat_check.py` was run with `weavevenv/bin/python` and `WEFT_URL` set.
7. **Spot checks.** 53 end-to-end checks, logged in §21: 18 HTTP and Weft Python SDK scripts, 1 TypeScript SDK run compiled from `src`, 2 runs of the official `weave` agent SDK, 1 access-control session and 31 Playwright page and flow checks.
8. **Test suites.** All passed: `go test ./internal/weft/...`; Python SDK 205 tests (24 live, `WEFT_TEST_SERVER_URL`); TypeScript SDK 76 tests (live files included, `tsc --noEmit` clean); UI vitest 101 tests; harness plugins (`node --test plugins/weft-*/test/*.test.mjs`) 24 tests, 4 of them live e2e.

Status key: **Impl** = implemented, **Partial** = present with stated gaps, **Missing** = no code found.

## Summary

| Feature group | Impl | Partial | Missing |
|---|---|---|---|
| 1. Tracing | 22 | 3 | 0 |
| 2. Integrations | 15 | 2 | 2 |
| 3. Objects and versioning | 8 | 1 | 0 |
| 4. Datasets | 7 | 0 | 0 |
| 5. Models | 4 | 0 | 0 |
| 6. Prompts | 3 | 0 | 0 |
| 7. Evaluations, scorers, guardrails | 13 | 0 | 0 |
| 8. Leaderboards | 3 | 0 | 0 |
| 9. Feedback, annotations, queues | 7 | 0 | 0 |
| 10. Monitors, signals, automations | 5 | 0 | 0 |
| 11. Agents | 6 | 2 | 0 |
| 12. Playground | 9 | 0 | 0 |
| 13. Costs | 3 | 0 | 0 |
| 14. UI views and export | 13 | 0 | 1 |
| 15. SDKs and client APIs | 8 | 2 | 0 |
| 16. Admin, settings, security | 8 | 0 | 1 |
| **Total (148 rows)** | **134** | **10** | **4** |

**API endpoints:** Weft routes **127 of 129** live OpenAPI method/path pairs; the two missing pairs are the new Bulk Export API. It routes **118 of 118** pairs in the vendored client SDK. 27 of 27 checked response shapes contain every top-level property of the spec schema. **Official client:** `weft_weave_compat_check.py` passed 10 of 10 checks with weave 0.54.0.

## 1. Tracing

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| `@op` on sync/async functions, methods, generators | tracking/create-call, trace-generator-func | Impl | `_op.py`, `op.ts`; `test_tracing.py`, `tracing.test.ts` |
| Nested calls, trace tree, summary roll-up | tracking/tracing | Impl | `store/calls.go`; `TestRollupInOrder`, `TestRollupOutOfOrder`; spot S1 |
| Manual `create_call`/`finish_call` | tracking/create-call | Impl | `_client.py`, TS `client.ts`; Python tests |
| Get current call | tracking/get-call-object | Impl | `get_current_call`, `getCurrentCall`; tests in both SDKs |
| Thread-pool context propagation | tracking/create-call | Impl | `weft.ThreadPoolExecutor`, `weft.Thread` (`_context.py`); tested |
| Display names (static, callable, post-hoc) | tracking/set-call-display | Impl | `call_display_name`, `/call/update`; spot S1 (`inner-op`) |
| Op `kind` and `color` | tracking/ops | Impl | `_op.py`, `op.ts`, UI `OpKind.tsx`; `test_core_features.py` |
| `postprocess_inputs`/`postprocess_output` | tracking/ops | Impl | `_op.py`, `op.ts`; tests in both SDKs |
| Per-op `tracing_sample_rate` | tracking/ops | Impl | `_op.py`, `OpOptions.tracingSampleRate`; tests in both SDKs |
| Print call link | tracking/ops | Impl | `print_call_link`, `WEFT_PRINT_CALL_LINK`; `test_core_features.py` |
| Code-capture toggle | env-vars | Impl | `capture_code`, `WEFT_CAPTURE_CODE`; compat check "op source file found" |
| Disable tracing: env, init, scoped context | tracking/trace-disable | Impl | `WEFT_DISABLED`, `init(disabled=)`, `weft.tracing_disabled`, `withTracingDisabled`; tested |
| Per-call and global attributes | tools/attributes | Impl | `weft.attributes()`, `global_attributes`; tested |
| Threads and turns (SDK, API, UI) | tracking/threads | Impl | `weft.thread()`, `withThread`, `/threads/query`; `TestThreadsMixedTurnIDs`; spot S1, UI U8 |
| Media: image, audio, video, PDF, HTML, Markdown, `Content` | core-types/media | Impl | `_content.py`, `content.ts`, `MediaView.tsx`; `media.test.ts` |
| BYOB media references | tracking/byob-references | Partial | Both SDKs log `Content.from_uri("s3://…")`/`fromUri` by reference (`test_content_from_uri_is_logged_by_reference`, TS `sdkExtras.test.ts`; spot T1). The UI resolves URIs through `/files/resolve_url` (`mediaUri.test.ts`). **Gap:** the server presigns `s3://` keys only inside Weft's own `--files-backend` bucket under the project prefix (`server/files_media.go`). No registered customer "reference buckets", no `gs://` |
| Key redaction | tracking/redact-pii | Impl | `_redact.py`, `redact.ts`; tested |
| PII redaction (SDK and server) | tracking/redact-pii | Impl | Python: regex plus Presidio/transformers NER (`_redact.py`; `test_local_scorers.py`, live test). Server: regex plus an optional Presidio Analyzer with regex fallback (`store/redact.go`, `store/ner.go`; `TestSensitiveDataNERRedaction`, `TestSensitiveDataNERTimeout`, fake analyzer); spot S3. The TS SDK does regex only |
| OTLP ingest `/otel/v1/traces` (protobuf, JSON, gzip) | tracking/otel | Impl | `otlp/otlp.go`; `TestProtobufGenAISpan`, `TestJSONHexIDsAndOpenInference`; spot S2 (usage and cost derived) |
| OTel conventions | tracking/otel | Partial | GenAI, OpenInference, OpenLLMetry/Traceloop, Vercel `ai.*`, MLflow, Pydantic AI/Logfire and Vertex ADK are mapped in `otlp/frameworks.go` (`frameworks_test.go`, 9 tests). **Gap:** Langfuse attributes are not mapped. OpenLit has no specific code or test (only generic `gen_ai.*`) |
| OTel thread and turn attributes | tracking/otel | Impl | `wandb.thread_id`, `wandb.is_turn` (`otlp.go`); `TestGenAIToolAndTurnAttributes` |
| Python and TS OTel helpers | tracking/otel | Impl | `weft/otel.py` (live test), `otel.ts` |
| Write-ahead log | tracking/write-ahead-log | Impl | `_sender.py` (`WEFT_ENABLE_WAL`), `sender.ts` `walPath`; `test_core_features.py`, `features.test.ts` |
| Retry and queue settings | env-vars | Impl | `retry_max_attempts`, `retry_backoff`, `max_queue_size` settings; tested (no env variables; see §15) |
| Call schema fields | tracking/call-schema-reference | Partial | All required `CallSchema` fields are returned. `storage_size_bytes` comes back with `include_storage_size`. **Gap:** `total_storage_size_bytes`, `wb_username` (`include_usernames`), `wb_run_step`, `wb_run_step_end` and `expire_at` are never returned (checked on `/call/read` and `/calls/stream_query`) |

## 2. Integrations

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| OpenAI (streaming, Responses API), Py and TS | integrations/openai | Impl | `integrations/openai.py`, `openai.ts`; `test_integrations.py`, `integrations.test.ts` |
| Anthropic, Py and TS | integrations/anthropic | Impl | `anthropic.py`, `anthropic.ts`; tests |
| Google GenAI and Vertex, Py and TS | integrations/google | Impl | `google_genai.py`, `vertexai.py`, `google_genai.ts`, `vertexai.ts`; tests |
| Bedrock runtime (Converse, InvokeModel, streams) | integrations/bedrock | Impl | `bedrock.py`, `bedrock.ts`; `test_more_integrations.py`, `frameworks.test.ts` |
| Bedrock Agents (`bedrock-agent-runtime` `invoke_agent`) | integrations/bedrock_agents | Missing | `bedrock.py` handles `bedrock-runtime` only. Weave's `bedrock_sdk.py` traces `invoke_agent` |
| Mistral, Cohere, Groq, Together, Cerebras, HF | integrations/* | Impl | `other_llms.py`, `huggingface.py`; TS `mistral.ts`, `cohere.ts`; fake-module tests |
| LiteLLM | integrations/litellm | Impl | `litellm.py`; tested |
| LangChain / LangGraph | integrations/langchain | Impl | `langchain.py`, TS `langchain.ts`; tests |
| DSPy | integrations/dspy | Impl | `dspy.py`; tested |
| OpenAI Agents SDK, Py and TS | integrations/agents/openai-agents-sdk | Impl | `openai_agents.py`, `openai_agents.ts`; tests |
| Vercel AI SDK (TS) | integrations/vercel_ai_sdk | Impl | `vercel_ai.ts` (middleware, tool wrapping, `wrapAISDK`); `frameworks.test.ts`, `frameworks.integration.test.ts` (live) |
| OpenAI-compatible endpoints (OpenRouter, local, Azure, Neon, NIM) | integrations/openrouter, local_models, azure, neon, nvidia_nim | Impl | `patch_openai_compatible`, `wrapOpenAICompatible`, provider detection in `openai.py`; tested. Azure and Neon are not specifically tested |
| Autopatch control and per-integration op settings | integrations/autopatching | Impl | `integrations/_settings.py`, `_hooks.py`, TS `integrations/settings.ts`; `test_autopatch_settings.py`, `frameworks.test.ts`. TS has no import-time patching |
| Python agent frameworks (LlamaIndex, CrewAI, smolagents, Instructor, Haystack, AutoGen, Agno, MCP, Claude Agent SDK, Google ADK, Pydantic AI, verifiers, GEPA, Not Diamond) | integrations/* | Impl | One module each in `integrations/` (`frameworks.py` for ADK, Pydantic AI, verifiers, GEPA, Not Diamond); `test_framework_integrations.py` uses fake modules. Smoke runs against the real libraries reported by the implementers were not repeated |
| TS frameworks (LangChain.js, LlamaIndex.TS, MCP, Claude Agent SDK, OpenAI Agents JS, Bedrock) | integrations (JS) | Impl | `sdk/typescript/src/integrations/*`; `frameworks.test.ts` (fake modules), `frameworks.integration.test.ts` (live, passed) |
| Koog, verl | integrations/koog, verl | Partial | No Weft code. Koog exports OTel and verl logs through the `weave` client; both routes exist in Weft (OTLP endpoint, official-client compatibility) but neither was tested with these tools |
| Verdict | integrations/verdict | Missing | No Weft code. Weave has `integrations/verdict` |
| OpenAI Realtime audio | integrations/openai-realtime-audio | Impl | `openai_realtime.py`, `RealtimeTracer`; `test_openai_realtime`, `test_realtime_tracer_for_raw_websockets`. Python only |
| Coding-agent harnesses (Claude Code, Codex, OpenClaw, Pi) | integrations/agents/*-harness | Partial | `plugins/weft-claude-code`, `weft-codex`, `weft-openclaw`, shared `weft-harness-common`; 24 node tests pass, including live `/agents/conversations/chat` e2e. **Gap:** no Pi (pi-dev) extension |

## 3. Objects and versioning

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Publish/get, Weave-identical content digests | tracking/objects | Impl | `store/objects.go`, `_canonical.py`, `canonical.ts`; `TestObjectsAndRefs`; compat check (dataset round trip) |
| Refs (`/attr`, `/key`, `/index`, `/id`), `refs/read_batch` | tracking/objects | Impl | `refs/refs.go` and tests; shape check |
| Delete object or version | tracking/objects | Impl | `delete_object_version(s)`, TS equivalents; `TestDeleteCascades` |
| Aliases (set, remove, list, resolve) | tracking/objects | Impl | `server/tags.go`; `TestTagsAndAliases`; spot S4 (alias `prod` resolves to v0) |
| Version tags | core-types/prompts-version | Impl | `tags.go`, `TagEditor.tsx`; `TestTagsAndAliases`; spot S4 |
| Version list and diff | tools/comparison | Impl | `ObjectVersionsPage.tsx`, `ObjectDiffPage.tsx`; UI U27 |
| Load a published op as code | tracking/objects | Impl | `_loader.py`, `loader.ts`; tests |
| v2 REST for ops, datasets, models, scorers, evaluations | OpenAPI | Impl | `server/v2.go`; `TestV2ObjectsCRUD`; spot S5 |
| Registry linking | OpenAPI `/link_to_registry`, `weave.link_prompt_to_registry` | Partial | Server: Weft-local registry collections (`server/registry.go`; `TestLinkToRegistry`). **Gap:** no SDK helper equivalent to `link_prompt_to_registry`; not the W&B Registry |

## 4. Datasets

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Dataset object, row tables, paging | core-types/datasets | Impl | `_object.py`, `store/tables.go`; spot S5; shape check `/table/query_stats` |
| `from_calls`, `from_pandas`/`to_pandas`, `add_rows` | core-types/datasets | Impl | `_object.py`; `test_objects.py`. The pandas paths are tested only for the missing-dependency error |
| `from_hf`/`to_hf`, `select()` (Py and TS `select`) | core-types/datasets | Impl | `_object.py`, `objects.ts`; `test_dataset_from_hf_to_hf_and_select` (fake HF module), `test_dataset_select_after_publish`; spot S5 |
| UI create from CSV/JSON, edit/append/delete rows | core-types/datasets | Impl | `CreateDatasetModal.tsx`, `DatasetRowsView.tsx`; UI U19 |
| Add calls to a dataset with field mapping | core-types/datasets | Impl | `AddToDatasetModal.tsx`; UI U17 |
| Dataset lineage (`/dataset_sources/*`) | OpenAPI | Impl | `store/datasetsources.go`; `TestCompatExtraEndpoints`; shape check |
| `table/create_from_digests`, `table/query_stats_batch` | OpenAPI | Impl | `compat_extra.go`; `TestCompatExtraEndpoints`; shape check |

## 5. Models

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| `Model` with `predict`, versioned attributes | core-types/models | Impl | `_object.py`, `objects.ts`; compat check (evaluation) |
| `LLMStructuredCompletionModel` | tools/playground | Impl | `_object.py`, `evals/evaluate.go`; spot S6 |
| Models list and version pages | core-types/models | Impl | `ObjectsPage kind="models"`; UI U23, U28 |
| v2 `/models` REST | OpenAPI | Impl | `v2.go`; `TestV2ObjectsCRUD` |

## 6. Prompts

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| `StringPrompt`, `MessagesPrompt`, `format()` | core-types/prompts | Impl | `_object.py`, TS `index.ts` exports; tests in both SDKs |
| Prompt versions with aliases and tags | core-types/prompts-version | Impl | `tags.go`; `TestTagsAndAliases` |
| Open a prompt in the playground | tools/playground | Impl | `PlaygroundPage.tsx` (`?prompt=`); `playground.test.ts`; UI U24 lists the seeded prompt |

## 7. Evaluations, scorers, guardrails

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| `Evaluation` (dataset, scorers, trials, preprocess, parallelism) | core-types/evaluations | Impl | `_evaluation.py`, `evaluation.ts`; `test_evaluation.py`; compat check |
| Function and class scorers, `summarize`, column mapping | evaluation/scorers, tools/column-mapping | Impl | `scorers.py` (`column_map`), `scorers.ts` (`columnMap`); `test_evaluation.py` |
| Built-in LLM and rule scorers (server-runnable) | evaluation/builtin_scorers | Impl | `evals/scorers.go`, `scorers_extra.go`; `TestExtraScorers` |
| Local SLM scorers | evaluation/weave_local_scorers | Impl | `local_scorers.py` (`weft[scorers]`); `test_local_scorers.py` with model loading mocked. Client side only |
| `EvaluationLogger` | evaluation/evaluation_logger | Impl | `_evaluation.py`, TS `evaluation.ts`; tests in both SDKs |
| Link existing calls or agent spans to an `EvaluationLogger` run | evaluation/evaluation_logger | Impl | `log_prediction(call=…)` accepts calls, ids, turns, `LogResult`s or conversations and stores `linked_calls`/`genai_span_ref`; spans opened inside a prediction get `weave.eval.*`; TS equivalent; UI `LinkedCalls.tsx` on call and compare pages. Tests: `test_log_prediction_links_existing_calls_and_turns`, live `test_evaluation_logger_links_existing_turns`, `linkedCalls.test.ts` |
| Evaluate on the server from the UI | tools/evaluation_playground | Impl | `/evaluate_model`, `evals/runtimes.go`, `NewEvaluationPage.tsx`; `TestEvaluateModelWithPredictURL`, `TestEvaluateModelBoundToRuntime`; spot S6 (8/8 rows complete), UI U22 |
| Re-score an evaluation | OpenAPI `/evaluations/rescore` | Impl | `evals/rescore.go`; `TestCompatExtraEndpoints`; shape check |
| Evaluation status | OpenAPI | Impl | `/evaluation_status`, `/evaluations/status`; spot S6 |
| Compare evaluations | evaluation/compare_evals | Impl | `/evaluations/compare`, `CompareEvaluationsPage.tsx`; `TestServerSideEvaluationCompareLeaderboard`; spot S8, UI U9 |
| Evaluations table, filters, saved views | evaluation/compare_evals | Impl | `EvaluationsPage.tsx`, `EvalRunsTable.tsx`; `test_saved_view_ui_params_dropped_and_evaluations_view` |
| Eval export REST (v2 runs, predictions, scores, `eval_results/query`) | evaluation/export_eval | Impl | `v2.go`; `TestV2EvaluationFlow` |
| Guardrails (`apply_scorer`), scoring existing calls | evaluation/guardrails | Impl | `_call.py`, TS `applyScorer`, `/calls/score`; `TestScoreCallsOnDemand` |

## 8. Leaderboards

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Leaderboard objects, compute API, page and editor | core-types/leaderboards | Impl | `/leaderboard/compute`, `LeaderboardPage.tsx`; `leaderboard.test.ts`; spot S7, UI U10 |
| Leaderboard SDK | core-types/leaderboards | Impl | `weft.Leaderboard`; Python tests |
| Dynamic leaderboard in eval comparison, save as object | evaluation/dynamic_leaderboards | Impl | `InlineLeaderboard.tsx`; `serverModels.test.ts` |

## 9. Feedback, annotations, queues

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Reactions and notes (UI and SDK) | tracking/feedback | Impl | `FeedbackPanel.tsx`, `CallFeedback`; compat check "feedback stored"; spot S10 |
| Human annotation specs | tracking/feedback | Impl | `AnnotationSpec`; Settings → Annotations (UI U15) |
| Query, replace, purge feedback; filter calls by feedback | tracking/feedback | Impl | `/feedback/*`; `TestFeedbackFieldQuery`; shape check (`total_count`) |
| Feedback stats, aggregate, batch create, payload schema | OpenAPI | Impl | `store/feedback_ext.go`; `TestCompatExtraEndpoints`, `TestFeedbackBatchCreateScorerFields`; shape checks |
| Annotation queues API | tracking/annotation-queues | Impl | `server/queues.go`; `TestAnnotationQueues`; spot S9 (2 added, 1 completed) |
| Add traces to a queue, progress, export to dataset | tracking/annotation-queues | Impl | `AddToQueueModal.tsx`, `ExportQueueModal.tsx`; `queueUtils.test.ts`; UI U12 |
| Focused one-at-a-time reviewer | tracking/annotation-review | Impl | `QueueReviewPage.tsx`; UI U21 |

## 10. Monitors, signals, automations

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Custom monitors (ops, filter, sampling, scorers) | evaluation/custom-monitors | Impl | `evals/monitors.go`, `MonitorEditor.tsx`; `TestMonitorScoresEndedCalls`; UI U25 |
| Preset signals (Weave's 8 turn presets plus 2) | evaluation/monitors, view-agent-signals | Impl | `store/agents_signals.go`, `/agents/signals/presets`; `TestAgentSignalsRunAndQuery`, `signals.test.ts`; spot S12 (10 presets), UI U5 |
| Custom tag/rating signals, automatic scoring of new turns | create-custom-signal | Impl | `TestAgentFeedbackBatchAndSignalFilters`, `TestJudgeNormalisation`, `TestAutoSignalsScoreEndedTurns` |
| Remote scorers (bearer, headers, OAuth client credentials) | evaluation/remote-scorers, remote-scorer-signals | Impl | `RemoteScorer` (`evals/scorers_extra.go`), `evals/oauth.go`; `TestRemoteScorerOAuth`, `TestRemotePredictOAuth` |
| Automations (score threshold, error rate, feedback → webhook/Slack) | evaluation/automations | Impl | `automations/automations.go`; `TestAutomationScoreThresholdAndFeedback`, `TestAutomationErrorRate`; spot S18 (webhook delivered), UI U26 |

## 11. Agents

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Agent tracing SDK (`start_conversation/turn/llm/tool/subagent`, `log_turn`/`log_conversation`) | tracking/trace-agents, trace-sub-agents, trace-agents-batch | Impl | `weft/conversation.py`, `conversation.ts`; `test_conversation.py` (12), `conversation.test.ts` (7), live `test_integration_agents.py` and `agents.integration.test.ts`; spots S11 and T1. The official `weave` conversation SDK also works (W1) |
| Agents OTLP endpoint | tracking/trace-agents-otel | Impl | `POST /agents/otel/v1/traces` (`server/agents.go`); exercised in `server/agents_test.go` |
| Span-kind derivation | trace-agents-attributes | Impl | `store/agents.go`; `TestDeriveSpanKinds`, `TestAgentsFromSDKCalls`, `TestSpanKindsReachIndex` |
| `/agents/*` API | OpenAPI | Impl | `server/agents.go`, `store/agents_*.go`; 7 server tests, `TestSpanGroupsCountSDKTurns`; 6 shape checks |
| Agents list, agent page, conversation page | tracking/view-agent-activity | Impl | `pages/agents/*`; `agents.test.ts`; UI U4, U6, U7 |
| Dashboard, Spans and Signals tabs | tracking/view-agent-activity | Impl | `AgentsDashboard.tsx`, `SpansTable.tsx`, `SignalsView.tsx`; UI U4, U5 |
| Agent evals | agent-evals | Partial | `EvaluationLogger` predictions link to turns and conversations, and agent spans carry `weave.eval.*` (§7). **Gap:** no "View spans" route from an evaluation to an Agents view filtered to that run's spans, and no Evals tab in the Agents view |
| Agents BYOB references | agents-byob-references | Partial | `conversation.py` and `conversation.ts` carry `uri` parts (`test_conversation.py`). **Gap:** same server limit as §1 BYOB |

## 12. Playground

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Built-in providers | tools/playground | Impl | `llm/llm.go`, `/llm/models`; `llm` package tests |
| Custom OpenAI-compatible providers | tools/custom-runtimes | Impl | `Provider` objects; `TestCustomRuntimeApply` |
| Params, tools, JSON mode, streaming | tools/playground | Impl | `PlaygroundPage.tsx`, `/completions/create_stream`; `playground.test.ts` |
| Side-by-side comparison | tools/playground | Impl | `PlaygroundPage.tsx` panes; `playground.test.ts` |
| Load from call or prompt; template variables | tools/playground | Impl | `fillVars`, `?call=`/`?prompt=`; `playground.test.ts` |
| Save as model | tools/playground | Impl | saves an `LLMStructuredCompletionModel`; `playground.test.ts` |
| Custom runtimes REST | tools/custom-runtimes | Impl | `server/runtimes.go`; `TestCustomRuntimeApply` |
| History of past runs | tools/playground | Impl | `HistoryPanel.tsx`; `playground.test.ts`; UI U11 |
| Image generation (`/image/create`, Image mode) | OpenAPI | Impl | `files_media.go`, `llm/images.go`, `ImageMode.tsx`; `TestImageCreateMock`; spot S14, UI U20 |

## 13. Costs

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Automatic costs with roll-up, including OTel spans | tracking/costs | Impl | `store/costs.go`; `TestDefaultPriceLookup`, `TestRootCostsNotDoubled`; spot S2; compat check (no double counting) |
| Custom costs add, query, purge (SDK, API, UI) | tracking/costs | Impl | `/cost/*`, Settings → Costs; tests; spot S15 |
| `/calls/usage`, `/trace/usage` | OpenAPI | Impl | `compat_extra.go`; shape checks; spot S2 |

## 14. UI views and export

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Traces table: filters, query builder, sort, columns, page size, bulk delete | tracking/trace-tree | Impl | `TracesPage.tsx`, `FilterBuilder.tsx`; UI U1 |
| Column pinning and reorder | tools/saved-views | Impl | `columnLayout.ts`; `columnLayout.test.ts` |
| Saved views (traces, evaluations) | tools/saved-views | Impl | `SavedView` objects; UI U18; spot S17 |
| Trace view modes (Tree, Code, Flame, Timeline, Graph) | tracking/trace-tree | Impl | `FlameGraph.tsx`, `flameLayout.ts`; `flameLayout.test.ts`, `traceViews.test.ts`; UI U2, U3 |
| Call tabs (Call, Feedback, Scores, Summary, Attributes, Code, Use, Raw) | tracking/trace-tree | Impl | `CallPage.tsx`; UI U2 |
| Chat view for LLM calls | tracking/view-call | Impl | `ChatView.tsx`; UI U31 (user and assistant turns on a `weft.completions.create` call) |
| Default trace plots | tracking/trace-plots | Impl | `CallCharts.tsx`, `/calls/stats`; shape check |
| Custom charts and dashboards | tracking/trace-plots | Impl | `pages/dashboards/*`, `/calls/metrics`; `TestCallsMetrics*`, `dashboards.test.ts`, `scatter.test.ts`; UI U13 |
| Server-side export (CSV, JSON, JSONL) with code snippets | tracking/querying-calls | Impl | `server/export.go`, `ExportModal.tsx`; `TestCallsExportCSVFlatten`; spot S16, UI U16 |
| Compare calls | tools/comparison | Impl | `CompareCallsPage.tsx`; `deltaFormat.test.ts`; UI U23 |
| Object and version comparison | tools/comparison | Impl | `ObjectDiffPage.tsx`; UI U27 |
| Projects page with stats | platform/weave-projects | Impl | `ProjectsPage.tsx`, `/project/stats`; UI U29 |
| Threads list and chat view | tracking/threads | Impl | `ThreadsPage.tsx`, `ThreadPage.tsx`; UI U8 |
| Bulk project export jobs (calls, objects, feedback) | OpenAPI "Bulk Export" | Missing | `POST /v1/entities/{e}/projects/{p}:export` and `GET …/exports/{job_id}` are not routed (§17). `/calls/export` streams calls only, synchronously |

## 15. SDKs and client APIs

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Python `get_calls`/`get_call`/`delete_calls`, `CallsIter` | tracking/querying-calls | Partial | `_client.py` (`columns`, `expand_columns`, costs, feedback); `test_client.py`; spot S1. **Gap:** `CallsIter` has no `to_pandas()`, which Weave's `PaginatedIterator` has |
| Python call helpers (children, feedback, delete, `apply_scorer`) | tracking/update-call | Impl | `_call.py`; tests |
| Alias, tag and object-delete APIs (Py and TS) | tracking/objects | Impl | `_client.py`, `client.ts`; tests |
| Official `weave` client against Weft | n/a | Impl | `scripts/weft_weave_compat_check.py`: 10/10 with weave 0.54.0; `wandbcompat.go`; `TestGraphQLShim`, `TestCallsCompleteCumulativeSummaries` |
| `get_evaluations`, `evaluate_model`, compare, leaderboard helpers | SDK | Impl | `_client.py`; `test_client.py`, `test_parity.py`, live `test_integration_server.py`; TS `sdkExtras.test.ts` |
| W&B login shim (`/graphql`) | n/a | Impl | `wandbcompat.go`; `TestGraphQLShim`; compat check logs in |
| TypeScript SDK breadth | reference/typescript-sdk | Impl | calls, ops, objects, evals, agent tracing, SavedView, BYOB, WAL, OTel helper, integrations; 76 tests |
| SavedView query/apply API (Py and TS) | tools/saved-views | Impl | `_saved_view.py` (filters, sort, columns, pinning, `save`/`load`, `to_calls_query`, `get_calls`), `savedView.ts`; `test_saved_view_fluent_api_save_load_and_apply`, live `test_saved_view_filters_on_the_server`, `sdkExtras.test.ts`; spot S17 |
| Client-side server-response cache | platform/server-caching | Impl | `_cache.py` (SQLite on disk, LRU, size limit, immutable reads only), `WEFT_USE_SERVER_CACHE`, `WEFT_SERVER_CACHE_DIR`, `WEFT_SERVER_CACHE_SIZE_LIMIT`; 4 tests in `test_sdk_extras.py`, live `test_server_cache_hits_on_live_reads`. Python only, like Weave |
| Environment variables | core-types/env-vars | Partial | `WEFT_*` equivalents exist for disable, capture code, print link, parallelism, WAL, PII redaction, autopatch, server cache. **Gap:** no env variables for retry attempts or interval, max queue size, HTTP timeout or log level (`WEAVE_RETRY_MAX_ATTEMPTS`, `WEAVE_MAX_CALLS_QUEUE_SIZE`, `WEAVE_HTTP_TIMEOUT`, `WEAVE_LOG_LEVEL`) |

## 16. Admin, settings, security

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Projects: implicit create, list, stats, delete | platform/weave-projects | Impl | `server/admin.go`; `TestProjectDelete` |
| API keys: hashed, project-scoped, read-only, admin | platform | Impl | `store/apikeys.go`; `TestAPIKeysScopes`, `TestRBACKeys`; live session L1 |
| Provider secrets encrypted at rest | tools/playground | Impl | AES-256-GCM (`store/secrets.go`); `TestSecretsEncryptedAtRest` |
| Retention (TTL) settings and purge | OpenAPI ttl_settings | Impl | `TestTTLPurge`; shape check |
| Server-side ingest sampling | platform/ingest-sampling | Impl | `TestIngestSampling`; shape check |
| Server-side sensitive-data redaction | OpenAPI sensitive_data_settings | Impl | `store/redact.go`, `store/ner.go`; `TestSensitiveDataRedaction`, `TestSensitiveDataNERRedaction`; spot S3 |
| Users, teams, roles (viewer/member/admin), OIDC SSO | guides/platform | Impl | `server/iam.go`, `store/iam.go`, `server/oidc.go`, Settings → Members, `/admin`; `TestRBACRoles`, `TestSessionsAndCSRF`, `TestOIDCLogin` (in-process fake IdP), `TestOIDCRejections`, `iam.test.ts`; live session L1, UI U14, U30. SSO was not tried against a real IdP |
| Audit log (query, CSV export, retention) | guides/platform | Impl | `store/audit.go`, `server/audit.go`, Settings → Audit log; `TestAuditLog`; live session L1 (8 entries incl. 2 denied), UI U15 |
| Database encryption at rest | guides/platform | Missing | `store/fileperms.go` creates the file `0600` and warns on loose permissions (`TestDBPermissionWarnings`). The pure-Go SQLite driver has no page encryption |

## 17. API endpoint coverage

Method/path pairs, from `endpoint_coverage.py`. The live probe agreed on every pair.

| Source | Pairs | Routed in Weft | Wrong method | Missing |
|---|---|---|---|---|
| Live OpenAPI (`trace.wandb.ai/openapi.json`) | 129 | **127** | 0 | 2 |
| Vendored SDK (weave 0.54.0) | 118 | **118** | 0 | 0 |
| Union | 129 | 127 | 0 | 2 |

- The SDK has no pairs outside the spec. The spec has 11 pairs that the 0.54.0 SDK does not call.
- Weft registers 201 pairs. 74 are not in Weave, among them `/calls/query`, `/calls/metrics`, `/calls/export`, `/evaluations/compare`, `/leaderboard/compute`, `/automations/*`, `/api_keys/*`, `/secrets/*`, `/users/*`, `/teams/*`, `/audit/*`, `/auth/*` and `/graphql`.
- **Missing.** Both pairs are new in the spec since the previous audit (tag "Bulk Export"):
  - `POST /v1/entities/{entity}/projects/{project}:export` starts an export job for `targets` ⊆ {calls, objects, feedback}. Weft returns its JSON 404.
  - `GET …/exports/{job_id}` returns the job status. Weft returns the UI's HTML fallback (200, `text/html`).
- **Live probe.** Every spec pair was sent with dummy ids and `{}`. 127 reached a handler: 25 returned 200, 87 returned 400 (validation) and 16 returned a handler 404 for the dummy ids.
- **Shapes.** All 27 cases contain every top-level property of the spec response. They include `/calls/stats` (Weave request), `/calls/query_stats` (`has_more`), `/feedback/query` (`total_count`), `/obj/read`, `/feedback/aggregate`, `/feedback/payload_schema`, `/table/query_stats`, `/evaluations/rescore`, the six `/agents/*` reads, the settings reads and the usage endpoints. Nested schemas were not compared.

## 18. Known differences (intentional)

- **Ref scheme.** Weft stores `weft:///` refs and accepts `weave:///`. `server/wandbcompat.go` rewrites refs to `weave:///` for the official client, which it detects by an `X-Weft-Ref-Scheme: weave` header, a Stainless header, or a `weave/` or `WeaveTrace/` user agent. It buffers the response to do so, so streamed responses to these clients arrive in one piece.
- **Tenancy.** An entity is a team name, the first part of a project id. There are users, teams and roles (§16), but no W&B organisations or billing. Without `--api-keys`, managed keys or SSO the server is open; it binds to loopback by default. Static keys are server admins.
- **Storage.** Everything is in one SQLite file (`modernc.org/sqlite`, no CGO): one writer, no horizontal scaling, no page encryption. File contents can go to S3 with `--files-backend`. TTL purge, audit retention and error-rate automations run as in-process loops.
- **Agents from calls.** Agents are derived from ordinary calls through a span index, not stored as a separate model. The official `weave` conversation SDK exports through an OTel `BatchSpanProcessor` that `weave.finish()` does not flush. So `/agents/query` right after `finish()` can be empty, while with `get_tracer_provider().force_flush()` before `finish()` the agent is returned at once (W2). The delay is on the client side, not in Weft's index. The Weft SDKs use their own sender, which `weft.flush()` covers.
- **Signals** are stored as feedback (`wandb.agent_monitor`) with Weave's `scorer_*` and `span_*` payload fields.
- **Server-side evaluation** runs `LLMStructuredCompletionModel`s, `predict_url` models and runtime-bound models. It never executes SDK `predict` code.
- **`/calls/stats`.** Weave's request fields select Weave's shape. Other requests get Weft's fixed-metric shape, which the UI uses.
- **W&B and CoreWeave services.** `/inference/*` serves Weft's own model catalog, `/link_to_registry` uses a Weft-local registry, and `/geolocate` answers locally. W&B Inference is not hosted, Weave panels in W&B Workspaces do not apply, and self-managed W&B is replaced by Weft's single binary.
- **SDK naming.** `weft.*` and `WEFT_*`. The official `weave` client is supported as a second client.

## 19. Changes from the previous version of this file

Rows whose status changed after re-verification:

| Row | Claimed | Verified | Reason |
|---|---|---|---|
| Agent tracing SDK | Missing | Impl | `conversation.py`, `conversation.ts`, unit and live tests, spots S11 and T1 |
| `from_hf`/`to_hf`, `select()` | Missing | Impl | `_object.py`, `objects.ts`, tests, spot S5 |
| Link existing calls to `EvaluationLogger` | Partial | Impl | `log_prediction(call=…)`, `LinkedCalls.tsx`, tests |
| Remote scorers | Partial (no OAuth) | Impl | `evals/oauth.go`, `TestRemoteScorerOAuth` |
| SavedView query/apply API | Partial | Impl | `_saved_view.py`, `savedView.ts`, tests, spot S17 |
| Client-side server-response cache | Missing | Impl | `_cache.py`, 4 unit tests and 1 live test |
| Users, teams, roles, SSO | Partial | Impl | `iam.go`, `oidc.go`, tests, live session L1 |
| Audit log | Missing | Impl | `audit.go`, `TestAuditLog`, live session L1 |
| Python `get_calls`/`CallsIter` | Impl | Partial | no `CallsIter.to_pandas()` |
| Python framework integrations row | Impl (incl. Koog, verl) | Impl, with Koog and verl split into a Partial row | no Weft code for Koog or verl, and neither was tested |
| API coverage | 127/127 | 127/129 | live spec gained the two Bulk Export pairs |

Unchanged status, new reason:
- **BYOB:** the SDK side is now done; the gap is on the server.
- **Agent evals:** the links exist, but no eval-filtered Agents view.
- **OTel conventions:** OpenLit had been listed as mapped, but has no specific code or test.

New rows (8):
- Partial: Call schema fields; Koog/verl; Registry linking; Environment variables.
- Missing: Bedrock Agents; Verdict; Bulk project export.
- Impl: TS frameworks, split out of the Python frameworks row.

## 20. Remaining gaps

In order of importance:

1. **Bulk Export API.** Weave added project export jobs (`POST …:export`, `GET …/exports/{job_id}`) for calls, objects and feedback. These are the only two spec pairs Weft lacks.
2. **BYOB on the server.** Resolve customer-registered reference buckets (`s3://`, `gs://`) to presigned URLs. Today only Weft's own files backend is presigned, so references to customer buckets show as plain links. This affects media and agent BYOB.
3. **Agent evals view.** Add an evaluation → Agents view filtered to the run's spans ("View spans"), and an Evals tab in the Agents view. The links are already stored.
4. **Integrations.**
   - Missing: Bedrock Agents (`invoke_agent`), Verdict, a Pi harness extension, OpenAI Realtime in TypeScript.
   - Not verified: Koog and verl, through the OTel and official-client routes.
   - NER redaction inside the TypeScript SDK; the server can apply NER instead.
5. **OTel conventions.** Map Langfuse attributes; add an OpenLit fixture test.
6. **Call schema.** Return `total_storage_size_bytes`, `wb_username`, `wb_run_step`/`wb_run_step_end` and `expire_at`.
7. **SDK extras.** `CallsIter.to_pandas()`; a `link_prompt_to_registry` equivalent; env variables for retry, queue size, HTTP timeout and log level.
8. **Database encryption at rest.** Not possible with the pure-Go driver. Today the guidance is an encrypted volume plus `0600` file permissions.

## 21. Spot-check log

All checks ran against `127.0.0.1:6499` and were repeated on the `f1d098e` build. All passed. Scripts are in the audit scratchpad (`finalscripts/`).

**S1–S18** (`spot.py`, Weft Python SDK and HTTP):
- **Tracing:** S1 nested `@op` with display name and thread. S2 OTLP JSON span → call with usage 100/20 and a cost. S3 `pii-v1` policy → `<EMAIL_ADDRESS>`.
- **Objects and datasets:** S4 alias move, tag, read by alias. S5 `Dataset.select` → 3-row table, `/table/query_stats`, v2 `/datasets`.
- **Evaluations:** S6 `/evaluate_model` → `complete` 8/8. S7 `/leaderboard/compute` (3 columns, 2 models). S8 `/evaluations/compare` (8 metrics, 8 rows).
- **Feedback:** S9 annotation queue items, progress and stats. S10 reaction feedback and `/feedback/query` count.
- **Agents:** S11 `weft.start_conversation` and `log_conversation` → `/agents/query` and chat. S12 10 signal presets.
- **Playground and costs:** S13 tracked `/completions/create` (`mock/echo`). S14 `/image/create` (`mock/image`). S15 `/cost/create` and `/cost/query`.
- **Export, SDK, automations:** S16 `/calls/export` CSV with columns. S17 `SavedView.filter_op().get_calls()`, `save`/`load`, response cache. S18 `feedback_created` automation → a local webhook received the POST.

**T1** (TypeScript SDK built from `src`): `startConversation/Turn/LLM/Tool` → `/agents/query` llm=1 tool=1. `Image.fromUri('s3://…')` was stored by reference.

**W1, W2** (official `weave` 0.54.0): `start_conversation/start_turn/start_llm/start_tool` → agent `planner` with llm=1, tool=1 and 10/4 tokens. Without a flush the immediate query was empty; with `force_flush()` the agent was returned at once.

**L1** (`--api-keys` server):
- An unauthenticated request got 401.
- A viewer key could read (200) but not write (403, "the member role on acme/p is needed").
- After promotion to member, the same write returned 200.
- A request for another team's project got 403.
- `/audit/query` listed all 8 actions, including both denials.

**U1–U31** (Playwright on the embedded UI, no page errors):
- **Traces:** U1 traces table; U2–U3 call page Flame and Code views; U31 Chat view on an LLM call; U8 threads; U16 server export modal; U17 Add to dataset modal; U18 Saved views menu; U23 compare calls and models list.
- **Agents:** U4 Agents dashboard, where conversation rows show turn counts after `f1d098e`; U5 Signals presets; U6 agent page; U7 conversation page.
- **Evaluations:** U9 evaluation comparison; U10 leaderboard; U22 New evaluation page.
- **Playground:** U11 History and Image panels; U20 Image mode.
- **Queues:** U12 queues list; U21 queue review.
- **Objects:** U19 Create dataset modal; U24 prompts; U27 object version diff; U28 model versions.
- **Project and admin:** U13 dashboards; U25 monitors; U26 automations; U14 settings tabs; U15 Audit log tab with Export CSV; U29 projects; U30 `/admin` users and teams.
