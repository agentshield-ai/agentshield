# W&B Weave vs Weft: feature parity audit

**Date:** 2026-11-02 (second audit of the day). **Weft:** branch `feat/weft-tracing`, commit `833a087`. **Weave:** the docs and live API as served on the audit date, and the `weave` 0.54.0 Python client, which is still the latest release on PyPI.

This audit re-checks the previous version of this file (`ce78c6d`: 148 rows, 134 Impl, 10 Partial, 4 Missing) after six WIP commits (`e428f35`..`833a087`). Those commits claim to close the Bulk Export API, customer reference buckets, the agent evals view, five integrations, four OTel mappings, the optional call fields, SDK extras and database encryption at rest. Each claim was checked end to end on a running server. Commit messages, the claims and Weft's own docs were not used as evidence. Weave's feature set was re-derived from its docs and client, which added 6 rows. Rows that changed are listed in §19.

**Sources.**
- Weave docs: `https://weave-docs.wandb.ai`, which now redirects to `https://docs.coreweave.com/products/wandb/weave`. The page index `https://docs.coreweave.com/_llms/en/forge/weights-and-biases.txt` lists **142** Weave pages (140 last time), and all 142 were downloaded as Markdown. Each page was mapped to a row or to §18, including the 20 cookbooks, the tutorials and the quickstarts that the previous audit did not map. The cookbooks and tutorials introduce no features beyond the guides, except the two found in `custom-agents-quickstart` (rows 11.9 and 13.4).
- Live trace-server spec: `https://trace.wandb.ai/openapi.json`: **129** method/path pairs, the same as last time.
- `weave` 0.54.0: the generated server SDK (`weave/vendor/weave_server_sdk`, 118 pairs) and the `integrations/` directory with the autopatch map in `integrations/patch.py`. The client ships one integration that has no docs page (`typesafe`), and it gets a row.
- An API-name sweep: all 75 `weave.<name>` and 28 `client.<method>(` identifiers in the docs were checked against the `weft` Python package and the TS exports. The real gaps are in rows 12.7 and 15.11. The rest are TS-only names with Weft equivalents (`weaveImage` → `Image`, `weaveAudio` → `Audio`), attribute keys, boto3 methods or W&B-only APIs (§18).
- Weft: `internal/weft/**`, `cmd/weft`, `web/weft-ui/src/**`, `internal/weft/webui/static`, `sdk/python/src/weft/**`, `sdk/typescript/src/**`, `plugins/weft-*/**`, `docs/weft/API.md`.

**Method.**
1. **Status rule.** **Impl** needs code plus a test or a live check in this audit. **Partial** names the gap. **Missing** means no code was found. Docs and commit messages are not evidence.
2. **Server.** `go build -o /tmp/weft-audit ./cmd/weft`, run on `127.0.0.1:6510` with a fresh scratch database and seeded with `scripts/weft_seed_demo.py`. A second instance on `:6511` ran with `--db-encryption-key-file` for the encryption checks. Both were stopped at the end.
3. **Embedded UI.** A `vite build` of `web/weft-ui` produced asset names identical to `internal/weft/webui/static`, so the binary serves the current UI source.
4. **Real libraries where cheap.** A separate venv had real `boto3` 1.43.123 and `verdict`. Bedrock Agents ran against a local endpoint that returns a genuine AWS event-stream `InvokeAgent` response. Verdict ran a real pipeline under both Weft and the official `weave` 0.54.0 client, for comparison. Pi ran as the real `@mariozechner/pi-coding-agent` 0.73.1 with its faux provider. Playwright (Chromium) drove the embedded UI.
5. **Endpoint coverage and shapes.** The previous `endpoint_coverage.py` (static route match), `live_probe.py` (every spec pair sent to the server) and `shapes_all.py` (27 response shapes) were re-run. The two Bulk Export response shapes were added by hand.
6. **Official client.** `scripts/weft_weave_compat_check.py` was run with weave 0.54.0.
7. **Unchanged rows.** Rows whose code was not touched since `ce78c6d` keep the evidence from the previous audit. Their spot checks (S1–S18, U1–U31 in the previous version) were not repeated one by one. The full test suites below were re-run and pass.
8. **Test suites (all re-run, all pass):**
   - `go test ./internal/weft/... ./cmd/weft/...`
   - Python SDK: 219 tests, live tests included (`WEFT_TEST_SERVER_URL`)
   - TypeScript SDK: 92 tests; `tsc --noEmit` clean
   - UI: vitest 105 tests; `tsc --noEmit` clean
   - Harness plugins (`node --test plugins/weft-*/test/*.test.mjs`): 32 tests, including the real-Pi test. One run had one failure in `weft-claude-code` "the real hook processes (background flush) deliver a session", under parallel load from all suites. It passed in the next full run and in 5 of 5 isolated runs, so it is treated as a timing flake.
   - The new parity-gap suites, run on their own: Python 14 (3 live), TypeScript 16 (5 live).

Status key: **Impl** = implemented, **Partial** = present with stated gaps, **Missing** = no code found.

## Summary

| Feature group | Impl | Partial | Missing |
|---|---|---|---|
| 1. Tracing | 24 | 1 | 0 |
| 2. Integrations | 17 | 2 | 1 |
| 3. Objects and versioning | 9 | 0 | 0 |
| 4. Datasets | 7 | 0 | 0 |
| 5. Models | 4 | 0 | 0 |
| 6. Prompts | 3 | 0 | 0 |
| 7. Evaluations, scorers, guardrails | 13 | 0 | 0 |
| 8. Leaderboards | 3 | 0 | 0 |
| 9. Feedback, annotations, queues | 7 | 0 | 0 |
| 10. Monitors, signals, automations | 5 | 0 | 0 |
| 11. Agents | 7 | 2 | 0 |
| 12. Playground | 8 | 1 | 0 |
| 13. Costs | 3 | 0 | 1 |
| 14. UI views and export | 14 | 0 | 0 |
| 15. SDKs and client APIs | 9 | 2 | 0 |
| 16. Admin, settings, security | 9 | 1 | 1 |
| **Total (154 rows)** | **142** | **9** | **3** |

Hosted-W&B-only features are **out of scope and not counted** in these totals or listed as gaps. They are listed in §18 with the reason for each: billing, SaaS organisation management, the W&B Models integration, W&B Inference, W&B Registry and the self-managed W&B deployment.

**API endpoints:** Weft routes **129 of 129** live OpenAPI method/path pairs and **118 of 118** vendored-SDK pairs. **29 of 29** checked response shapes contain every required top-level property. **Official client:** `weft_weave_compat_check.py` passed 10 of 10 checks with weave 0.54.0.

**Claimed fixes found broken or incomplete** (details in §19 and §21):
- **BYOB rendering.** Server presigning works, but a bucket URI logged as a plain string (Weave's documented usage), as an agent tool result or as an LLM `attach_media(uri=…)` part is never rendered or resolved (A3). `docs/weft/API.md` §"Reference buckets" says it is.
- **Verdict.** Every pipeline with chained blocks is split into two traces (A6).

## 1. Tracing

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| `@op` on sync/async functions, methods, generators | tracking/create-call, trace-generator-func | Impl | `_op.py`, `op.ts`; `test_tracing.py`, `tracing.test.ts` |
| Nested calls, trace tree, summary roll-up | tracking/tracing | Impl | `store/calls.go`; `TestRollupInOrder`, `TestRollupOutOfOrder` |
| Manual `create_call`/`finish_call` | tracking/create-call, cookbooks/import_from_csv | Impl | `_client.py`, TS `client.ts`; Python tests |
| Get current call | tracking/get-call-object | Impl | `get_current_call`, `getCurrentCall`; tests in both SDKs (`require_current_call` is row 15.11) |
| Thread-pool context propagation | tracking/create-call | Impl | `weft.ThreadPoolExecutor`, `weft.Thread` (`_context.py`); tested |
| Display names (static, callable, post-hoc) | tracking/set-call-display | Impl | `call_display_name`, `/call/update`; tested |
| Op `kind` and `color` | tracking/ops | Impl | `_op.py`, `op.ts`, UI `OpKind.tsx`; `test_core_features.py` |
| `postprocess_inputs`/`postprocess_output` | tracking/ops | Impl | `_op.py`, `op.ts`; tests in both SDKs |
| Per-op `tracing_sample_rate` | tracking/ops | Impl | `_op.py`, `OpOptions.tracingSampleRate`; tests in both SDKs |
| Print call link | tracking/ops | Impl | `print_call_link`, `WEFT_PRINT_CALL_LINK`; `test_core_features.py` |
| Code-capture toggle | env-vars | Impl | `capture_code`, `WEFT_CAPTURE_CODE`; compat check "op source file found" (A14) |
| Disable tracing: env, init, scoped context | tracking/trace-disable | Impl | `WEFT_DISABLED`, `init(disabled=)`, `weft.tracing_disabled`, `withTracingDisabled`; tested |
| Per-call and global attributes | tools/attributes | Impl | `weft.attributes()`, `global_attributes`; tested |
| Threads and turns (SDK, API, UI) | tracking/threads | Impl | `weft.thread()`, `withThread`, `/threads/query`; `TestThreadsMixedTurnIDs` (SDK query helper: row 15.11) |
| Media: image, audio, video, PDF, HTML, Markdown, `Content` | core-types/media, tracking/view-call | Impl | `_content.py`, `content.ts` (`Image`, `Audio` for Weave's `weaveImage`/`weaveAudio`), `MediaView.tsx`; `media.test.ts` |
| BYOB media references | tracking/byob-references | Partial | **Server works:** registered customer buckets (`s3://` SigV4, `gs://` V4 signing, team or project scope, `prefix`) are presigned by `/files/resolve_url` (`server/refbuckets.go`, `objstore/gcs.go`). Tested by `TestReferenceBucketsS3AndGCS` against signature-checking fakes and by spot A2. The SDKs log `Content.from_uri` by reference, and the UI renders those inline from the presigned URL (A3). **Gap:** Weave's documented usage is a URI as a *plain string* in any field ("any string field whose value is a bucket URI renders"). Weft's UI only detects media objects: `detectMedia` in `components/media/mediaUtils.ts` accepts bare strings only as `data:` URLs. A returned `"s3://bucket/frames/x.png"` shows as text and no resolve request is made (A3) |
| Key redaction | tracking/redact-pii | Impl | `_redact.py`, `redact.ts`; tested |
| PII redaction (SDK and server) | tracking/redact-pii | Impl | Python: regex plus Presidio/transformers NER (`_redact.py`). TypeScript: regex plus a pluggable analyzer and a Presidio Analyzer REST client with regex fallback (`redact.ts`; `parityGaps.test.ts` "NER PII redaction", live `parityGaps.integration.test.ts`). Server: regex plus optional Presidio (`store/redact.go`, `store/ner.go`; `TestSensitiveDataNERRedaction`, `TestSensitiveDataNERTimeout`) |
| OTLP ingest `/otel/v1/traces` (protobuf, JSON, gzip) | tracking/otel | Impl | `otlp/otlp.go`; `TestProtobufGenAISpan`, `TestJSONHexIDsAndOpenInference`; spot A7 |
| OTel conventions | tracking/otel | Impl | GenAI, OpenInference, OpenLLMetry/Traceloop, Vercel `ai.*`, MLflow, Pydantic AI/Logfire, Vertex ADK (`otlp/frameworks.go`), plus Langfuse `langfuse.observation.*`/`langfuse.trace.*`/session/user/tags (`otlp/langfuse.go`). Tests: `frameworks_test.go`, `TestLangfuseAttributes`, `TestOpenLitFixture`, `TestKoogFixture`, `TestVerlMLflowFixture`. Live: a Langfuse span became a call with messages, choices, usage 11/4, thread `lf-sess-1` and user `alice` (A7) |
| OTel thread and turn attributes | tracking/otel | Impl | `wandb.thread_id`, `wandb.is_turn` (`otlp.go`); `TestGenAIToolAndTurnAttributes` |
| Python and TS OTel helpers | tracking/otel | Impl | `weft/otel.py` (live test), `otel.ts` (`otel.*` export; Weave's `flushOTel` is covered by `flush`) |
| Write-ahead log | tracking/write-ahead-log | Impl | `_sender.py` (`WEFT_ENABLE_WAL`), `sender.ts` `walPath`; tests (WAL-sender disable switch: row 15.10) |
| Retry and queue settings | env-vars | Impl | `retry_max_attempts`, `retry_backoff`, `retry_max_backoff`, `max_queue_size`, `http_timeout` settings, also from `WEFT_RETRY_MAX_ATTEMPTS`, `WEFT_RETRY_MAX_INTERVAL`, `WEFT_MAX_CALLS_QUEUE_SIZE` and `WEFT_HTTP_TIMEOUT` in both SDKs. Tests: `test_retry_queue_timeout_and_log_level_env`, `test_env_defaults_unchanged`, TS "reads retry, queue, timeout and log-level settings from the environment"; spot A9 |
| Call schema fields | tracking/call-schema-reference | Impl | Every `CallSchema` field is returned. `wb_run_step`/`wb_run_step_end` are stored from `/call/start`, `/call/end` and `upsert_batch`. `expire_at` is derived from the project TTL. `total_storage_size_bytes` (trace roots, `include_total_storage_size`) and `wb_username` (`include_usernames`) are supported (`store/callfields.go`; `callfields_test.go`). Spot A8: step 7→9; `expire_at` = start + 30 d; total 828 = 314 + 514; `wb_username` `alice` |

## 2. Integrations

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| OpenAI (streaming, Responses API), Py and TS | integrations/openai | Impl | `integrations/openai.py`, `openai.ts`; `test_integrations.py`, `integrations.test.ts` |
| Anthropic, Py and TS | integrations/anthropic | Impl | `anthropic.py`, `anthropic.ts`; tests |
| Google GenAI and Vertex, Py and TS | integrations/google | Impl | `google_genai.py`, `vertexai.py`, `google_genai.ts`, `vertexai.ts`; tests |
| Bedrock runtime (Converse, InvokeModel, streams) | integrations/bedrock | Impl | `bedrock.py`, `bedrock.ts`; `test_more_integrations.py`, `frameworks.test.ts` |
| Bedrock Agents (`bedrock-agent-runtime` `invoke_agent`, `invoke_inline_agent`) | integrations/bedrock_agents | Impl | `integrations/bedrock_agents.py`, TS `bedrock_agents.ts`. Patched through botocore, so every client is traced; the event stream is wrapped and stays iterable. Unit and live tests in both SDKs. Spot A5 with **real boto3**: `invoke_agent` → agent call with the streamed answer, an LLM span with usage 120/30 under the foundation model, and an action-group tool span. The agent appears in `/agents/query` |
| Mistral, Cohere, Groq, Together, Cerebras, HF | integrations/* | Impl | `other_llms.py`, `huggingface.py`; TS `mistral.ts`, `cohere.ts`; fake-module tests |
| LiteLLM | integrations/litellm | Impl | `litellm.py`; tested |
| LangChain / LangGraph (incl. `langchain_nvidia_ai_endpoints`) | integrations/langchain | Impl | `langchain.py` registers a global LangChain configure hook, so any chat model, NVIDIA's included, is traced; TS `langchain.ts`; tests |
| DSPy | integrations/dspy | Impl | `dspy.py`; tested |
| OpenAI Agents SDK, Py and TS | integrations/agents/openai-agents-sdk | Impl | `openai_agents.py`, `openai_agents.ts`; tests |
| Vercel AI SDK (TS) | integrations/vercel_ai_sdk | Impl | `vercel_ai.ts`; `frameworks.test.ts`, `frameworks.integration.test.ts` |
| OpenAI-compatible endpoints (OpenRouter, local, Azure, Neon, NIM) | integrations/openrouter, local_models, azure, neon, nvidia_nim | Impl | `patch_openai_compatible`, `wrapOpenAICompatible`, provider detection; tested (Azure and Neon not specifically) |
| Autopatch control and per-integration op settings | integrations/autopatching | Impl | `integrations/_settings.py`, `_hooks.py`, `WEFT_AUTOPATCH`, `WEFT_DISABLED_INTEGRATIONS`, TS `integrations/settings.ts`; `test_autopatch_settings.py` |
| Python agent frameworks (LlamaIndex, CrewAI, smolagents, Instructor, Haystack, AutoGen, Agno, MCP/FastMCP client and server, Claude Agent SDK, Google ADK, Pydantic AI, verifiers, GEPA, Not Diamond) | integrations/* | Impl | One module each in `integrations/`; `test_framework_integrations.py` (fake modules) |
| TS frameworks (LangChain.js, LlamaIndex.TS, MCP, Claude Agent SDK, OpenAI Agents JS, Bedrock) | integrations (JS) | Impl | `sdk/typescript/src/integrations/*`; `frameworks.test.ts`, `frameworks.integration.test.ts` |
| Koog, verl | integrations/koog, verl | Impl | Koog: OTel mapping of Kotlin role names, node spans and the indexed prompt/completion attributes (`otlp/langfuse.go` `koogInputs`/`koogOutput`; `TestKoogFixture`). verl: MLflow spans (`TestVerlMLflowFixture`) and weave-client calls (`server/verl_test.go` `TestVerlWeaveCalls`). Fixture-based; neither real tool was run (Kotlin/JVM, GPU) |
| Verdict | integrations/verdict | Partial | `integrations/verdict.py` adds a `WeftVerdictTracer` to every `Pipeline`; `test_parity_gaps.py` and a live test pass with a fake `verdict` module. **Bug (A6):** with the real `verdict` library, a pipeline of two chained `MapUnit` blocks is recorded as **two traces**: the second block becomes a root call with no parent. Every run reproduced it (3 of 3). Weave 0.54.0 records the same pipeline as one trace of 5 calls. Cause: `start_call` drops a finished call from `self._calls`, and Verdict starts the next block with `parent_id` set to the finished block, so the parent lookup misses and `create_call` runs without a parent |
| OpenAI Realtime audio | integrations/openai-realtime-audio | Partial | Python `openai_realtime.py` and TS `openai_realtime.ts` (new: `wrapOpenAIRealtime`, `instrumentOpenAIRealtime`, `RealtimeTracer`) trace the `openai` package's realtime connections: session, responses with usage, function calls. Tests: `test_openai_realtime`, TS `parityGaps.test.ts` "OpenAI Realtime" and live. **Gaps:** (1) audio is never recorded, only its byte count, whereas Weave buffers the PCM and logs WAV audio; (2) no automatic tracing of the OpenAI Agents SDK realtime path (Python `RealtimeRunner`; TS `@openai/agents-realtime` `RealtimeSession`/`patchRealtimeSession`, the TS path Weave documents) or of raw `websockets`/`aiohttp` clients, which Weave patches. Those need a manual `RealtimeTracer` |
| TypeSafe (`typesafe_sdk` `system_one` → chat spans) | weave 0.54.0 `integrations/typesafe` (no docs page) | Missing | No Weft code. It could be covered by hand with `start_llm` |
| Coding-agent harnesses (Claude Code, Codex, OpenClaw, Pi) | integrations/agents/*-harness | Impl | `plugins/weft-claude-code`, `weft-codex`, `weft-openclaw`, new `weft-pi` (Pi extension: `invoke_agent` per prompt, `chat` per LLM response with cache usage, `execute_tool` per tool), shared `weft-harness-common`; `WEFT_AGENT_NAME` and `WEFT_CAPTURE_CONTENT` match Weave's plugin settings. 32 node tests, including `real-pi.test.mjs` with real `@mariozechner/pi-coding-agent` 0.73.1 (A13) |

## 3. Objects and versioning

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Publish/get, Weave-identical content digests | tracking/objects | Impl | `store/objects.go`, `_canonical.py`, `canonical.ts`; `TestObjectsAndRefs`; compat check |
| Refs (`/attr`, `/key`, `/index`, `/id`), `refs/read_batch` | tracking/objects | Impl | `refs/refs.go` and tests; shape check |
| Delete object or version | tracking/objects | Impl | `delete_object_version(s)`, TS equivalents; `TestDeleteCascades` |
| Aliases (set, remove, list, resolve) | tracking/objects | Impl | `server/tags.go`; `TestTagsAndAliases` (the SDK lacks `list_aliases`/`get_tags_and_aliases` under those names: row 15.11) |
| Version tags | core-types/prompts-version | Impl | `tags.go`, `TagEditor.tsx`; `TestTagsAndAliases` |
| Version list and diff | tools/comparison | Impl | `ObjectVersionsPage.tsx`, `ObjectDiffPage.tsx` (changes/split/unified) |
| Load a published op as code | tracking/objects | Impl | `_loader.py`, `loader.ts`; tests |
| v2 REST for ops, datasets, models, scorers, evaluations | OpenAPI | Impl | `server/v2.go`; `TestV2ObjectsCRUD` |
| Registry linking | OpenAPI `/link_to_registry`, `weave.link_prompt_to_registry` | Impl | Server: Weft-local registry collections (`server/registry.go`; `TestLinkToRegistry`). SDK: `weft.link_to_registry`, `weft.link_prompt_to_registry`, `get_from_registry` (Py), TS equivalents. Tests: `test_link_prompt_and_any_object_to_registry`, TS "links prompts and any object to a registry", live tests in both; spot A9. The hosted W&B Registry itself is out of scope (§18) |

## 4. Datasets

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Dataset object, row tables, paging | core-types/datasets | Impl | `_object.py`, `store/tables.go`; shape check `/table/query_stats` |
| `from_calls`, `from_pandas`/`to_pandas`, `add_rows` | core-types/datasets | Impl | `_object.py`; `test_objects.py` |
| `from_hf`/`to_hf`, `select()` | core-types/datasets, cookbooks/hf_dataset_evals | Impl | `_object.py`, `objects.ts`; `test_dataset_from_hf_to_hf_and_select` |
| UI create from CSV/JSON, edit/append/delete rows | core-types/datasets | Impl | `CreateDatasetModal.tsx`, `DatasetRowsView.tsx` |
| Add calls to a dataset with field mapping | core-types/datasets | Impl | `AddToDatasetModal.tsx` |
| Dataset lineage (`/dataset_sources/*`) | OpenAPI | Impl | `store/datasetsources.go`; `TestCompatExtraEndpoints`; shape check |
| `table/create_from_digests`, `table/query_stats_batch` | OpenAPI | Impl | `compat_extra.go`; `TestCompatExtraEndpoints`; shape check |

## 5. Models

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| `Model` with `predict`, versioned attributes | core-types/models | Impl | `_object.py`, `objects.ts`; compat check (evaluation) |
| `LLMStructuredCompletionModel` | tools/playground | Impl | `_object.py`, `evals/evaluate.go` |
| Models list and version pages | core-types/models | Impl | `ObjectsPage kind="models"` |
| v2 `/models` REST | OpenAPI | Impl | `v2.go`; `TestV2ObjectsCRUD` |

## 6. Prompts

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| `StringPrompt`, `MessagesPrompt`, `format()` | core-types/prompts | Impl | `_object.py`, TS `index.ts`; tests in both SDKs |
| Prompt versions with aliases and tags | core-types/prompts-version | Impl | `tags.go`; `TestTagsAndAliases` |
| Open a prompt in the playground | tools/playground | Impl | `PlaygroundPage.tsx` (`?prompt=`); `playground.test.ts` |

## 7. Evaluations, scorers, guardrails

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| `Evaluation` (dataset, scorers, trials, preprocess, parallelism) | core-types/evaluations | Impl | `_evaluation.py`, `evaluation.ts`; `test_evaluation.py`; compat check |
| Function and class scorers, `summarize`, column mapping | evaluation/scorers, tools/column-mapping | Impl | `scorers.py`, `scorers.ts`; `test_evaluation.py` |
| Built-in LLM and rule scorers (server-runnable) | evaluation/builtin_scorers | Impl | `evals/scorers.go`, `scorers_extra.go`; `TestExtraScorers` |
| Local SLM scorers | evaluation/weave_local_scorers | Impl | `local_scorers.py`; `test_local_scorers.py` (model loading mocked) |
| `EvaluationLogger` | evaluation/evaluation_logger | Impl | `_evaluation.py`, TS `evaluation.ts`; tests; spot A4 |
| Link existing calls or agent spans to an `EvaluationLogger` run | evaluation/evaluation_logger | Impl | `log_prediction(call=…)`, `weave.eval.*` span attributes, `LinkedCalls.tsx`; tests; spot A4 |
| Evaluate on the server from the UI | tools/evaluation_playground | Impl | `/evaluate_model`, `evals/runtimes.go`, `NewEvaluationPage.tsx`; `TestEvaluateModelWithPredictURL`, `TestEvaluateModelBoundToRuntime` |
| Re-score an evaluation | OpenAPI `/evaluations/rescore` | Impl | `evals/rescore.go`; shape check |
| Evaluation status | OpenAPI | Impl | `/evaluation_status`, `/evaluations/status` |
| Compare evaluations | evaluation/compare_evals | Impl | `/evaluations/compare`, `CompareEvaluationsPage.tsx`; compat check "Weft evaluation comparison of a weave evaluation" |
| Evaluations table, filters, saved views | evaluation/compare_evals | Impl | `EvaluationsPage.tsx`, `EvalRunsTable.tsx` |
| Eval export REST (v2 runs, predictions, scores, `eval_results/query`) | evaluation/export_eval | Impl | `v2.go`; `TestV2EvaluationFlow` |
| Guardrails (`apply_scorer`), scoring existing calls | evaluation/guardrails, cookbooks/scorers_as_guardrails | Impl | `_call.py`, TS `applyScorer`, `/calls/score`; `TestScoreCallsOnDemand` |

## 8. Leaderboards

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Leaderboard objects, compute API, page and editor | core-types/leaderboards | Impl | `/leaderboard/compute`, `LeaderboardPage.tsx`; `leaderboard.test.ts` |
| Leaderboard SDK | core-types/leaderboards | Impl | `weft.Leaderboard`; Python tests |
| Dynamic leaderboard in eval comparison, save as object | evaluation/dynamic_leaderboards | Impl | `InlineLeaderboard.tsx`; `serverModels.test.ts` |

## 9. Feedback, annotations, queues

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Reactions and notes (UI and SDK) | tracking/feedback, cookbooks/feedback_prod | Impl | `FeedbackPanel.tsx`, `CallFeedback`; compat check "feedback stored" |
| Human annotation specs | tracking/feedback | Impl | `AnnotationSpec`; Settings → Annotations |
| Query, replace, purge feedback; filter calls by feedback | tracking/feedback | Impl | `/feedback/*`; `TestFeedbackFieldQuery` |
| Feedback stats, aggregate, batch create, payload schema | OpenAPI | Impl | `store/feedback_ext.go`; `TestCompatExtraEndpoints`; shape checks |
| Annotation queues API | tracking/annotation-queues | Impl | `server/queues.go`; `TestAnnotationQueues` |
| Add traces to a queue, progress, export to dataset | tracking/annotation-queues | Impl | `AddToQueueModal.tsx`, `ExportQueueModal.tsx`; `queueUtils.test.ts` |
| Focused one-at-a-time reviewer | tracking/annotation-review | Impl | `QueueReviewPage.tsx` |

## 10. Monitors, signals, automations

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Custom monitors (ops, filter, sampling, scorers) | evaluation/custom-monitors, cookbooks/online_monitoring | Impl | `evals/monitors.go`, `MonitorEditor.tsx`; `TestMonitorScoresEndedCalls` |
| Preset signals (Weave's 8 turn presets plus 2) | evaluation/monitors, view-agent-signals | Impl | `store/agents_signals.go`; `TestAgentSignalsRunAndQuery` |
| Custom tag/rating signals, automatic scoring of new turns | create-custom-signal | Impl | `TestAgentFeedbackBatchAndSignalFilters`, `TestAutoSignalsScoreEndedTurns` |
| Remote scorers (bearer, headers, OAuth client credentials) | evaluation/remote-scorers, remote-scorer-signals | Impl | `RemoteScorer` (`evals/scorers_extra.go`), `evals/oauth.go`; `TestRemoteScorerOAuth`; spot A12 (the call works; the missing egress controls are row 16.11) |
| Automations (score threshold, error rate, feedback → webhook/Slack) | evaluation/automations | Impl | `automations/automations.go`; `TestAutomationScoreThresholdAndFeedback`, `TestAutomationErrorRate` |

## 11. Agents

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Agent tracing SDK (`start_conversation/turn/llm/tool/subagent`, `record()`, `log_turn`/`log_conversation`) | tracking/trace-agents, trace-sub-agents, trace-agents-batch, custom-agents-quickstart | Impl | `weft/conversation.py` (`LLM.record(usage=, response_id=, response_model=, reasoning=, …)`), `conversation.ts`; `test_conversation.py`, `conversation.test.ts`, live tests; spots A3, A4, A11 |
| Agents OTLP endpoint | tracking/trace-agents-otel | Impl | `POST /agents/otel/v1/traces` (`server/agents.go`); `server/agents_test.go` |
| Span-kind derivation | trace-agents-attributes | Impl | `store/agents.go`; `TestDeriveSpanKinds`, `TestSpanKindsReachIndex` |
| `/agents/*` API | OpenAPI | Impl | `server/agents.go`, `store/agents_*.go`; 10 of 10 Agents spec pairs routed; 6 shape checks |
| Agents list, agent page, conversation page | tracking/view-agent-activity | Impl | `pages/agents/*`; `agents.test.ts`; spot A3 (conversation page) |
| Dashboard, Spans and Signals tabs | tracking/view-agent-activity | Impl | `AgentsDashboard.tsx`, `SpansTable.tsx`, `SignalsView.tsx` |
| Agent evals ("Evals" tab, "View spans" from an evaluation) | agent-evals | Impl | `/agents/evals/query`, `/agents/evals/conversations`, and `evaluation_run_id`/`predict_and_score_call_id` filters on `/agents/spans/*` (`server/agents_evals.go`, `store/agents_evals.go`; `agents_evals_test.go`). UI: an Evals tab in the Agents view (`AgentEvals.tsx`); "View spans"/"View conversations" links on the Evaluations table, the comparison page and object pages; per-turn eval badges on conversations. Spot A4 (Playwright): the Evals tab lists the run (2 predictions, 2 conversations, 6 spans, `passed` 50%). The Evaluations page's Spans link opens `agents?tab=spans&eval=<run>`, which shows 6 spans while the project has 8 |
| Agents BYOB references | agents-byob-references | Partial | `conversation.py`/`conversation.ts` carry `uri` parts (`attach_media(uri=…)`), and the server presigns registered buckets (row 1.16). **Gap:** the Agents conversation view renders neither an LLM `attach_media(uri="s3://…png")` part nor an `s3://` tool result. Both show as text and no `/files/resolve_url` request is made (A3). The serialised part `{"type": "uri", "uri": …}` has no `_type`, so `detectMedia` skips it |
| Deep link to a conversation (`weave.trace.urls.agent_conversation_path`) | custom-agents-quickstart | Partial | The UI route `/{entity}/{project}/conversations/{conversation_id}` exists and `conversation_id` is exposed. **Gap:** no SDK helper builds the URL (Weave's is Python-only) |

## 12. Playground

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Built-in providers | tools/playground | Impl | `llm/llm.go`, `/llm/models`; `llm` tests |
| Custom OpenAI-compatible providers | tools/custom-runtimes | Impl | `Provider` objects; `TestCustomRuntimeApply` |
| Params, tools, JSON mode, streaming | tools/playground | Impl | `PlaygroundPage.tsx`, `/completions/create_stream`; `playground.test.ts` |
| Side-by-side comparison | tools/playground | Impl | `PlaygroundPage.tsx` panes; `playground.test.ts` |
| Load from call or prompt; template variables | tools/playground | Impl | `fillVars`, `?call=`/`?prompt=`; `playground.test.ts` |
| Save as model | tools/playground | Impl | `playground.test.ts` |
| Custom runtimes (REST, UI, SDK registration) | tools/custom-runtimes | Partial | REST `server/runtimes.go` (the spec's Custom Runtimes pair is routed), UI binding; `TestCustomRuntimeApply`, `TestEvaluateModelBoundToRuntime`. **Gap:** no SDK registration. Weave has `client.register_custom_runtime(name, base_url, api_key_secret, headers, runtime_ids=[…, weave.CustomRuntimeID(id, max_tokens)])` and TS `registerCustomRuntime`. Neither Weft SDK has an equivalent, so registration needs the REST API or the UI |
| History of past runs | tools/playground | Impl | `HistoryPanel.tsx`; `playground.test.ts` |
| Image generation (`/image/create`, Image mode) | OpenAPI | Impl | `files_media.go`, `llm/images.go`, `ImageMode.tsx`; `TestImageCreateMock` |

## 13. Costs

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Automatic costs with roll-up, including OTel spans | tracking/costs | Impl | `store/costs.go`; `TestDefaultPriceLookup`, `TestRootCostsNotDoubled`; compat check (no double counting) |
| Custom costs add, query, purge (SDK, API, UI) | tracking/costs, cookbooks/custom_model_cost | Impl | `/cost/*`, Settings → Costs; tests |
| `/calls/usage`, `/trace/usage` | OpenAPI | Impl | `compat_extra.go`; shape checks |
| Cache-aware pricing (cache read and cache write tokens priced at their own rates) | custom-agents-quickstart "Record token usage and cost" | Missing | Weft records `cache_read_input_tokens`/`cache_creation_input_tokens` and shows them in the Agents views (`store/agents_query.go`). Prices, however, have only prompt and completion rates (`store/costs.go` `Price`), so cached input is billed at the full input rate. Spot A11: `claude-sonnet-4-5` with 10,000 input tokens (9,000 cache reads) and 100 output tokens costs **$0.0315** in Weft. Weave's documented method (cache reads at their own rate, subtracted from input) gives **$0.0072**, so Weft overstates by about 4.4×. This hits Anthropic-heavy agents, where cache reads dominate |

## 14. UI views and export

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Traces table: filters, query builder, sort, columns, page size, bulk delete | tracking/trace-tree | Impl | `TracesPage.tsx`, `FilterBuilder.tsx` |
| Column pinning and reorder | tools/saved-views | Impl | `columnLayout.ts`; `columnLayout.test.ts` |
| Saved views (traces, evaluations) | tools/saved-views | Impl | `SavedView` objects |
| Trace view modes (Tree with in-trace search, Code, Flame, Timeline, Graph) | tracking/trace-tree | Impl | `TraceTree.tsx` ("Search calls in trace…"), `FlameGraph.tsx`; `flameLayout.test.ts`, `traceViews.test.ts` |
| Call tabs (Call, Feedback, Scores, Summary, Attributes, Code, Use, Raw) | tracking/trace-tree | Impl | `CallPage.tsx` |
| Chat view for LLM calls | tracking/view-call | Impl | `ChatView.tsx` |
| Default trace plots | tracking/trace-plots | Impl | `CallCharts.tsx`, `/calls/stats`; shape check |
| Custom charts and dashboards | tracking/trace-plots | Impl | `pages/dashboards/*`, `/calls/metrics`; `TestCallsMetrics*` |
| Server-side export (CSV, JSON, JSONL) with code snippets | tracking/querying-calls | Impl | `server/export.go`, `ExportModal.tsx`; `TestCallsExportCSVFlatten` |
| Compare calls (baseline, diff display) | tools/comparison | Impl | `CompareCallsPage.tsx`, `diffUtils.ts`; `deltaFormat.test.ts` |
| Object and version comparison | tools/comparison | Impl | `ObjectDiffPage.tsx` (changes, split, unified) |
| Projects page with stats | platform/weave-projects | Impl | `ProjectsPage.tsx`, `/project/stats` |
| Threads list and chat view | tracking/threads | Impl | `ThreadsPage.tsx`, `ThreadPage.tsx` |
| Bulk project export jobs (calls, objects, feedback) | OpenAPI "Bulk Export" | Impl | `POST /v1/entities/{e}/projects/{p}:export` → 202 `{name, job_id, created_at}`; `GET …/exports/{job_id}` → `{state, artifacts[{target, uri, expected_rows, written_rows}], failure, …}`. Artifacts are gzip JSONL, in the S3 files backend when configured and otherwise downloadable from `…/exports/{job_id}/artifacts/{target}` (`server/bulkexport.go`, `store/bulkexport.go`; `bulkexport_test.go`). Spot A1: calls 304, objects 23 and feedback 40 rows, each equal to the project's counts; Pydantic-style 422s for bad targets, a missing body or extra fields; 404 for an unknown job or another project's job. Both responses match the spec's required properties |

## 15. SDKs and client APIs

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Python `get_calls`/`get_call`/`delete_calls`, `CallsIter` with `to_pandas()` | tracking/querying-calls | Impl | `_client.py` (`CallsIter.to_records()`, `to_pandas(flatten=)`, `include_usernames`, `include_storage_size`); `test_calls_to_records_and_to_pandas`; spot A9 with real pandas 3.1.0 (5×22 frame; flattened `inputs.*`/`attributes.*` columns) |
| Python call helpers (children, feedback, delete, `apply_scorer`) | tracking/update-call | Impl | `_call.py`; tests |
| Alias, tag and object-delete APIs (Py and TS) | tracking/objects | Impl | `_client.py` (`get_aliases`, `get_tags`, `list_tags`), `client.ts`; tests |
| Official `weave` client against Weft | n/a | Impl | `scripts/weft_weave_compat_check.py`: 10/10 with weave 0.54.0 (A14); `wandbcompat.go` |
| `get_evaluations`, `evaluate_model`, compare, leaderboard helpers | SDK | Impl | `_client.py`; `test_client.py`, `test_parity.py`; TS `sdkExtras.test.ts` |
| W&B login shim (`/graphql`) | n/a | Impl | `wandbcompat.go`; `TestGraphQLShim`; compat check logs in |
| TypeScript SDK breadth | reference/typescript-sdk | Impl | calls, ops, objects, evals, agent tracing, SavedView, BYOB, WAL, OTel helper, integrations, NER redaction, registry, run linkage; 92 tests |
| SavedView query/apply API (Py and TS) | tools/saved-views | Impl | `_saved_view.py`, `savedView.ts`; tests |
| Client-side server-response cache | platform/server-caching | Impl | `_cache.py`, `WEFT_USE_SERVER_CACHE`, `WEFT_SERVER_CACHE_DIR`, `WEFT_SERVER_CACHE_SIZE_LIMIT`; tests. Python only, like Weave |
| Environment variables | core-types/env-vars | Partial | `WEFT_*` equivalents now cover disable, capture code, print link, parallelism, WAL, retry attempts and interval, max queue size, HTTP timeout, log level, PII redaction, autopatch (`WEFT_AUTOPATCH`, `WEFT_DISABLED_INTEGRATIONS`), server cache and the harness `WEFT_AGENT_NAME`/`WEFT_CAPTURE_CONTENT`. Spot A9: `WEFT_HTTP_TIMEOUT=7` and `WEFT_LOG_LEVEL=DEBUG` were applied. **Gap:** no equivalent of `WEAVE_DEBUG_HTTP` (request/response logging) or `WEAVE_DISABLE_WAL_SENDER` (write the WAL without sending) |
| Lower-level client methods used in the guides | tracking/threads, core-types/prompts-version, tracking/feedback | Partial | Equivalents exist for alias and tag reads (`get_aliases`, `get_tags`, `list_tags`). **Gap:** no SDK method to query threads (Weave: `client.threads_query(ThreadsQueryReq(…))`; Weft has only REST `/threads/query`). No `require_current_call()`, and no `list_aliases()`/`get_tags_and_aliases()` under Weave's names |

## 16. Admin, settings, security

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Projects: implicit create, list, stats, delete | platform/weave-projects | Impl | `server/admin.go`; `TestProjectDelete` |
| API keys: hashed, project-scoped, read-only, admin | platform | Impl | `store/apikeys.go`; `TestAPIKeysScopes`, `TestRBACKeys` |
| Provider secrets encrypted at rest | tools/playground | Impl | AES-256-GCM (`store/secrets.go`, key in `<db>.key`); `TestSecretsEncryptedAtRest` |
| Retention (TTL) settings and purge | OpenAPI ttl_settings | Impl | `TestTTLPurge`; `expire_at` follows it (A8) |
| Server-side ingest sampling, with dry run | platform/ingest-sampling | Impl | `store/projectadmin.go` (`sample_rate`, `dry_run`); `TestIngestSampling` |
| Server-side sensitive-data redaction | OpenAPI sensitive_data_settings | Impl | `store/redact.go`, `store/ner.go`; tests |
| Users, teams, roles (viewer/member/admin), OIDC SSO | guides/platform | Impl | `server/iam.go`, `store/iam.go`, `server/oidc.go`; `TestRBACRoles`, `TestOIDCLogin`, `TestOIDCRejections` |
| Audit log (query, CSV export, retention) | guides/platform | Impl | `store/audit.go`, `server/audit.go`; `TestAuditLog` |
| Database encryption at rest | guides/platform | Impl | Opt-in: `--db-encryption-key`, `--db-encryption-key-file` or `WEFT_DB_ENCRYPTION_KEY` opens the database through `ncruces/go-sqlite3`'s Adiantum VFS, still without CGO (`store/dbcrypt.go`; `dbcrypt_test.go`, `cmd/weft/db_test.go`, `scripts/weft_test_encrypted.sh`). Spot A10: the log shows `db_encrypted=true`; a canary string is absent from the file; a wrong key or no key refuses to start with a clear error; a restart with the key from the env reads the data back. Adiantum does not authenticate pages (documented) |
| Reference buckets: register per team or project, test connection, list, delete | tracking/byob-references "Register the reference bucket" | Partial | `/reference_buckets/create|test|list|delete` (`server/refbuckets.go`; server-admin only, `TestReferenceBucketsNeedServerAdmin`). Credentials come from stored secrets or `AWS_*`, and are never returned; spot A2. **Gap:** REST only, whereas Weave registers buckets in a team-settings dialog with **Test connection**. Weft has no UI, and it does not check the bucket's CORS as Weave does (stated in `API.md`) |
| Outbound request controls for remote scorers (host allowlist, private, loopback and metadata blocking, `http://` opt-in) | evaluation/remote-scorers (`WF_SCORING_WORKER_REMOTE_SCORER_*`) | Missing | Weave rejects loopback, private, internal and cloud-metadata addresses by default, and lets operators set an allowlist, allowed private CIDRs and an insecure-HTTP switch. Every Weft outbound client is a plain `http.Client{}` with no dial guard (`evals/scorers_extra.go` `remoteHTTP`, `evals/oauth.go`, `automations/automations.go`, `llm/llm.go`). Spot A12: a `RemoteScorer` with `url: http://127.0.0.1:6519/internal-admin`, run through `/calls/score`, made the server POST the call's inputs and output to that loopback address. The response was stored as feedback that project readers can read. Low risk on the default loopback-only, single-user setup; real for shared deployments where members can publish scorers |

## 17. API endpoint coverage

Method/path pairs, from `endpoint_coverage.py`. The live probe agreed on every pair.

| Source | Pairs | Routed in Weft | Wrong method | Missing |
|---|---|---|---|---|
| Live OpenAPI (`trace.wandb.ai/openapi.json`) | 129 | **129** | 0 | 0 |
| Vendored SDK (weave 0.54.0) | 118 | **118** | 0 | 0 |
| Union | 129 | 129 | 0 | 0 |

- Every spec tag is fully routed: Bulk Export 2/2, Agents 10/10, Calls 12/12, Evaluations 7/7, Annotation Queues 9/9 and the rest.
- The SDK has no pairs outside the spec. The spec has 11 pairs that the 0.54.0 SDK does not call.
- Weft registers 211 pairs, 82 of them not in Weave. Among them: `/calls/query`, `/calls/metrics`, `/calls/export`, `/evaluations/compare`, `/leaderboard/compute`, `/agents/evals/*`, `/reference_buckets/*`, the export artifact download, `/automations/*`, `/api_keys/*`, `/secrets/*`, `/users/*`, `/teams/*`, `/audit/*`, `/auth/*` and `/graphql`.
- **Live probe.** Every spec pair was sent with dummy ids and `{}`. All 129 reached a handler: 25 returned 200, 87 returned 400, 2 returned 422 (the Bulk Export validation) and 15 returned a handler 404 for the dummy ids.
- **Shapes.** The 27 earlier cases still contain every top-level property of the spec response. The two Bulk Export responses (`ExportStartedResponse`, `ExportStatusResponse` with `JobArtifact`) also contain every required property: 29 of 29. Nested schemas were not compared.

## 18. Known differences and out of scope

**Intentional differences** (unchanged unless noted):
- **Ref scheme.** Weft stores `weft:///` refs and accepts `weave:///`. `server/wandbcompat.go` rewrites refs to `weave:///` for the official client. It detects that client by an `X-Weft-Ref-Scheme: weave` header, a Stainless header, or a `weave/` or `WeaveTrace/` user agent. It buffers the response to do the rewrite, so streamed responses reach these clients in one piece.
- **Tenancy.** An entity is a team name, the first part of a project id. Without `--api-keys`, managed keys or SSO the server is open; it binds to loopback by default. Static keys are server admins.
- **Storage.** One SQLite file: one writer, no horizontal scaling. The default driver is `modernc.org/sqlite`; with a database key it is `ncruces/go-sqlite3` with Adiantum (new). File contents and bulk-export artifacts can go to S3 with `--files-backend`.
- **Agents from calls.** Agents are derived from ordinary calls through a span index. The official `weave` conversation SDK exports through an OTel `BatchSpanProcessor` that `weave.finish()` does not flush, so a query right after `finish()` can be empty.
- **Signals** are stored as feedback (`wandb.agent_monitor`) with Weave's payload fields.
- **Server-side evaluation** runs `LLMStructuredCompletionModel`s, `predict_url` models and runtime-bound models. It never executes SDK `predict` code.
- **Bulk export** runs one job at a time per server (409 otherwise). Artifacts are served by Weft or presigned from the files backend, not written to a customer-configured export destination.
- **SDK naming.** `weft.*` and `WEFT_*`. The official `weave` client is supported as a second client.

**Out of scope: hosted W&B features, deliberately not counted as rows or gaps.** Each needs W&B's hosted platform or another W&B product, not a tracing and evaluation server:
- **Billing, plans and usage limits.** These are SaaS commercial features with no self-hosted counterpart.
- **SaaS organisation management**: organisations, organisation admins, seats, CoreWeave Forge accounts and Forge API keys (named as prerequisites on several pages), and the cross-product W&B team settings page. Weft's own users, teams and roles are covered in §16.
- **W&B Models integration** (`guides/tools/weave-in-workspaces`, `cookbooks/Models_and_Weave_Integration_Demo`): Weave panels in W&B Workspaces, automatic association with the active `wandb.run` and the run link in the Traces table, and references to W&B Artifacts. These need W&B Models. Weft only stores and returns the linkage fields (`wb_run_id`, `wb_run_step`, `wb_run_step_end`; `set_wb_run` stands in for `set_wandb_run_context`).
- **W&B Registry.** It is part of the W&B platform. `link_prompt_to_registry` targets a Weft-local registry instead (row 3.9).
- **W&B Serverless Inference** (`quickstart-inference`, `integrations/inference`) is a hosted model service. `/inference/*` serves Weft's own model catalog for API compatibility.
- **Self-managed W&B deployment** (`guides/platform/weave-self-managed`: ClickHouse, Keeper, Helm, the `WF_CLICKHOUSE_*` settings) is replaced by Weft's single binary.
- **Platform BYOB (secure storage connector) and the W&B presigned-URL service** back W&B artifact and run storage. They are distinct from Weave's reference-media buckets, which are covered in rows 1.16 and 16.10.
- **Support and conceptual pages** (`details/support-weave`, `concepts/what-is-weave`, `agent-integration-quickstart`, `cookbooks`) describe no features of their own. The features in the cookbooks and tutorials are mapped to rows.

## 19. Changes from the previous version of this file

Rows whose status changed after verification:

| Row | Before | Now | Reason |
|---|---|---|---|
| OTel conventions | Partial | Impl | `otlp/langfuse.go`, four fixture tests, live Langfuse span (A7) |
| Call schema fields | Partial | Impl | `store/callfields.go`, `callfields_test.go`, A8 |
| Bedrock Agents | Missing | Impl | `bedrock_agents.py`/`.ts`; real boto3 (A5) |
| Verdict | Missing | **Partial** | Code and fake-module tests exist, but real `verdict` pipelines split into two traces (A6) |
| Koog, verl | Partial | Impl | Fixture tests for both (`TestKoogFixture`, `TestVerlMLflowFixture`, `TestVerlWeaveCalls`) |
| Coding-agent harnesses | Partial | Impl | `plugins/weft-pi`; real Pi 0.73.1 test (A13) |
| OpenAI Realtime audio | Impl | **Partial** | Re-checked against Weave's page: no audio capture, no Agents-SDK realtime or raw-websocket patching. The new TS integration is real but covers only the `openai` package's connections |
| Registry linking | Partial | Impl | `link_prompt_to_registry`/`link_to_registry` in both SDKs (A9) |
| Agent evals | Partial | Impl | `/agents/evals/*`, Evals tab, "View spans" (A4) |
| Bulk project export | Missing | Impl | `server/bulkexport.go` (A1); API coverage 129/129 |
| Python `get_calls`/`CallsIter` | Partial | Impl | `CallsIter.to_pandas()` (A9) |
| Database encryption at rest | Missing | Impl | `store/dbcrypt.go` (A10) |
| Custom runtimes REST | Impl | **Partial** | Re-scoped to Weave's page: no SDK `register_custom_runtime`/`registerCustomRuntime` |

Unchanged status, new reason:
- **BYOB media references** and **Agents BYOB references** remain Partial. The server side (customer `s3://`/`gs://` buckets) is now done and verified. The gap moved to the UI, which does not render bucket URIs logged as plain strings, agent `uri` parts or tool results (A3). **This contradicts `docs/weft/API.md` §"Reference buckets"**, which says URIs "in any call input, output or agent span" render inline.
- **Environment variables** remain Partial. Retry, queue, timeout and log-level variables now exist. Only `WEAVE_DEBUG_HTTP` and `WEAVE_DISABLE_WAL_SENDER` lack equivalents.

New rows (6), from re-deriving Weave's feature set:
- Missing: TypeSafe integration (2.19); cache-aware pricing (13.4); outbound request controls for remote scorers (16.11).
- Partial: conversation deep link (11.9); lower-level client methods (15.11); reference-bucket registration UI (16.10).

## 20. Remaining gaps

Ordered by user impact. Hosted-W&B-only features are not gaps (§18).

1. **Cost overstatement with prompt caching** (13.4). Cached input tokens are billed at the full input rate. For Anthropic agents, where cache reads dominate, costs can be several times too high (4.4× in A11). Add cache-read and cache-write rates to prices and price those tokens separately, as Weave does.
2. **BYOB rendering** (1.16, 11.8). The server now presigns customer `s3://`/`gs://` buckets, but the UI only renders `Content.from_uri` objects. Detect bucket URI strings with a media extension in any value, and handle `{"type": "uri"}` parts and tool results in the Agents conversation view. Then fix the claim in `API.md`.
3. **Verdict trace splitting** (2.17). Keep finished Verdict calls resolvable by `(trace_id, call_id)` until the pipeline root ends, or pass the parent id explicitly. Add a test that chains two blocks with the real library's parent semantics.
4. **Outbound request controls** (16.11). Remote scorers, `predict_url`, OAuth token URLs, custom provider base URLs and automation webhooks can reach loopback, private and metadata addresses, and some responses are readable. Add a guarded dialer, with an allowlist and allowed CIDRs, and an `http://` opt-in for shared deployments.
5. **OpenAI Realtime** (2.18). Record audio (WAV, as Weave does). Auto-trace the Agents SDK realtime path (`RealtimeRunner`, TS `RealtimeSession`) and raw websocket clients.
6. **SDK surface.** Custom-runtime registration (12.7), a threads query method and `require_current_call` (15.11), and a conversation URL helper (11.9).
7. **Reference-bucket admin UI** (16.10). Add a Settings dialog with Test connection; optionally check CORS.
8. **Smaller items.** A TypeSafe integration (2.19); `WEFT_DEBUG_HTTP` and a WAL-sender disable switch (15.10).

## 21. Spot-check log

All checks ran against `127.0.0.1:6510` (encryption checks on `:6511`) on a build of `833a087`. Scripts are in the audit scratchpad (`audit2/scripts/`). **FAIL** marks a claimed fix that did not hold, and **GAP** marks a newly found gap.

- **A1 Bulk export.** `POST …:export` with all three targets → 202. Status `succeeded`, with 3 artifacts. The downloads are `application/gzip` JSONL with 304/23/40 lines, equal to `/calls/query_stats` 304, `/objs/query` 23 and `/feedback/query` 40. Bad target, missing `targets` and an extra field each → 422 with Pydantic-style `detail`. Unknown job → 404, non-UUID id → 422, another project's job → 404.
- **A2 Reference buckets.** An S3 bucket was registered for team `demo` and a GCS bucket with `prefix: frames/` for project `demo/support-bot`, with secrets from `/secrets/set`. Resolving `s3://cust-media/…` returned a SigV4 URL; `gs://cust-gcs/frames/a.mp4` returned a `GOOG4-RSA-SHA256` URL. `gs://cust-gcs/other/…` (outside the prefix) and an unregistered bucket → 400. Project `demo/other` could use the team's S3 bucket but not the project-scoped GCS bucket.
- **A3 BYOB in the UI (FAIL).** Playwright on the embedded UI. A `Content.from_uri("s3://…png")` output rendered an `<img>` from the presigned URL, after one `/files/resolve_url` request. These rendered only as text, with no resolve request:
  - an op returning the bare string `"s3://cust-media/frames/000123.png"`;
  - an agent tool result with an `s3://` URI;
  - an LLM `attach_media(uri="s3://…png", mime_type="image/png")`.
  The same held on the call pages and on the conversation page with every row expanded.
- **A4 Agent evals.**
  - API: an `EvaluationLogger` with 2 predictions, each running a conversation with an LLM and a tool span. `/agents/evals/query` → 1 run: 2 predictions, 2 conversations, 6 spans, `passed.true_fraction` 0.5.
  - Filter: after an unrelated conversation was added, `/agents/spans/query` returned 8 spans without the filter and 6 with `evaluation_run_id`.
  - UI (Playwright): the Agents Evals tab lists the run. The Evaluations page's Spans link opens `agents?tab=spans&eval=…`, which shows 6 spans, 2 conversations and 2 predictions. No page errors.
- **A5 Bedrock Agents** (real boto3 1.43.123). A local endpoint returned a genuine `application/vnd.amazon.eventstream` `InvokeAgent` response: 4 trace events and a chunk. The caller still received the answer. Weft recorded `bedrock_agent.invoke_agent` → `model_invocation` (usage 120/30 under `anthropic.claude-3-haiku-…`) and `action_group` (output `delivered`). `/agents/query` lists agent `AG1`, version `AL1`.
- **A6 Verdict (FAIL).** With the real `verdict` package, `Pipeline("double") >> MapUnit(...) >> MapUnit(...)` under `weft.init` gave 5 calls in **2 traces** each run: the second `verdict.Map` had `parent_id = null`. It happened on 3 of 3 runs. The same script under `weave.init` (weave 0.54.0, against Weft) gave 5 calls in **1 trace**, with the second `Map` nested under the first. Reproduce with `audit2/scripts/verdict_real.py` (Weft) and `audit2/scripts/verdict_weave.py` (Weave).
- **A7 Langfuse OTLP.** An OTLP JSON span with `langfuse.observation.type=generation`, model, input, output, `usage_details`, `langfuse.session.id`, `langfuse.user.id` and tags became a call with `messages`, `choices`, usage 11/4 under `gpt-4o-mini`, `thread_id=lf-sess-1` and `wb_user_id=alice`.
- **A8 Call fields.** `/call/start` with `wb_run_step: 7` and `/call/end` with `wb_run_step_end: 9` were returned as such. `expire_at` was null until TTL 30 days was set, then `2026-12-02T20:00:00Z`. `total_storage_size_bytes` 828 = root 314 + child 514, on the root only. `wb_username` was `alice` with `include_usernames`.
- **A9 SDK extras.** `get_calls(limit=5).to_pandas()` gave a 5×22 frame (pandas 3.1.0), and `flatten=True` gave dotted columns. `weft.link_prompt_to_registry(p, target_path="demo/registry/prompts-coll")` returned a version and ref. `WEFT_HTTP_TIMEOUT=7` and `WEFT_LOG_LEVEL=DEBUG` were applied (client timeout 7.0; logger level 10).
- **A10 Database encryption.**
  - Start: `--db-encryption-key-file` → `db_encrypted=true`. A call carrying `SUPERSECRETCANARY42` was written, and the database file was created with mode `0600`.
  - Inspection: neither the canary nor `CREATE TABLE` appears in the file.
  - Wrong key: refused with "wrong database encryption key". No key: refused with "database is encrypted … set --db-encryption-key".
  - Reopen with `WEFT_DB_ENCRYPTION_KEY`: the call reads back.
- **A11 Cache pricing (GAP).** `LLM.record(usage=Usage(input_tokens=10000, output_tokens=100, cache_read_input_tokens=9000), response_model="claude-sonnet-4-5")` → call cost `prompt_tokens_total_cost` 0.03 + 0.0015 = $0.0315, and the agent `total_cost_usd` was also 0.0315. Weave's method gives $0.0072.
- **A12 Remote scorer egress (GAP).** A `RemoteScorer` object with `url: http://127.0.0.1:6519/internal-admin` was scored over a call via `/calls/score`. The loopback listener received `{"inputs":{"q":1},"output":"hello"}`, and its reply was stored as feedback.
- **A13 Harness plugins.** 32 node tests with `WEFT_TEST_PI_MODULES` set; `real-pi.test.mjs` ran a real Pi 0.73.1 session into Weft. One timing flake is noted in Method 8.
- **A14 Official client.** `weft_weave_compat_check.py`: 10/10 with weave 0.54.0.
- **A15 Coverage.** `endpoint_coverage.py`: 129/129 spec and 118/118 SDK pairs. `live_probe.py`: 129 routed, 0 unrouted. `shapes_all.py`: 27/27, plus the 2 Bulk Export shapes.
