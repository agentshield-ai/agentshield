# W&B Weave vs Weft: feature parity audit

**Date:** 2026-11-02 (third audit of the day). **Weft:** branch `feat/weft-tracing`, commit `4ef1de7`. **Weave:** the docs and live API as served on the audit date, and the `weave` 0.54.0 Python client, which is still the latest release on PyPI.

This audit re-checks the previous version of this file (`7962c9e`, checked at `833a087`: 154 rows, 142 Impl, 9 Partial, 3 Missing) after four WIP commits and `4ef1de7` (`0355d2d`..`4ef1de7`). Those commits claim to close every Partial and Missing row and the whole remaining-gaps list: BYOB rendering of plain `s3://`/`gs://` strings and agent `uri` parts with a Settings → Buckets tab, Verdict nesting, Realtime WAV audio with Agents SDK and raw-websocket tracing, TypeSafe, cache-aware pricing, an outbound-request guard, SDK custom-runtime registration, `threads_query`, `require_current_call`, `list_aliases`, `get_tags_and_aliases`, a conversation link helper, `WEFT_DEBUG_HTTP` and `WEFT_DISABLE_WAL_SENDER`. Each claim was checked end to end on a running server, with the real third-party library where one could be installed. Commit messages, the claims and Weft's own docs were not used as evidence. The fixes were also probed for new problems: the egress guard against public hosts with mixed DNS, IPv6-only hosts and the container's HTTPS proxy; cache pricing in parent roll-ups; and the bucket URL resolver on strings that are not media. Rows that changed are listed in §19.

**Sources.**
- Weave's feature set (142 doc pages, the live spec's 129 method/path pairs, the `weave` 0.54.0 client) is the one derived earlier the same day for the second audit. It was not re-derived, so the row set (154) is unchanged. The pages for the changed rows were re-read from that download: byob-references, agents-byob-references, verdict, openai-realtime-audio, custom-runtimes, threads, prompts-version, feedback, objects, env-vars, remote-scorers and custom-agents-quickstart. `weave` 0.54.0's `integrations/typesafe`, `integrations/anthropic` and `trace/urls.py` were read for the matching rows.
- An API-name sweep of the docs' `weave.<name>` and `client.<method>(` identifiers was done in the second audit. Every name it flagged now has a Weft equivalent. `weave.list_aliases()` and `weave.get_tags_and_aliases()` exist as module functions in Weave but only as client methods in Weft, which is how the guides call them (row 15.11).
- Weft: `internal/weft/**`, `cmd/weft`, `web/weft-ui/src/**`, `internal/weft/webui/static`, `sdk/python/src/weft/**`, `sdk/typescript/src/**`, `plugins/weft-*/**`, `docs/weft/API.md`.

**Method.**
1. **Status rule.** **Impl** needs code plus a test or a live check in this audit. **Partial** names the gap. **Missing** means no code was found. Docs and commit messages are not evidence.
2. **Server.** `go build -o /tmp/weft-audit3 ./cmd/weft`, run on `127.0.0.1:6530` with a fresh scratch database and seeded with `scripts/weft_seed_demo.py`. The container's `HTTPS_PROXY` (a CONNECT-only relay on `127.0.0.1:33021`) was left in place. The egress checks also used instances with `NO_PROXY=*` (`:6531`), `--remote-scorer-allow-insecure-http` (`:6532`), `--egress-allow 127.0.0.1:6520,localhost:6519` (`:6533`) and `--egress-allow-private` (`:6534`), and the SDK suites ran against `:6535`. Every instance and helper was stopped at the end. For the mixed-DNS checks, `/etc/hosts` was edited for the test and then restored.
3. **Embedded UI.** A `vite build` of `web/weft-ui` produced asset names identical to `internal/weft/webui/static`, so the binary serves the current UI source.
4. **Real libraries.** The real `verdict`, `openai-agents` (`agents.realtime`), `websockets`, `anthropic` 2.4.0, `typesafe_sdk` 0.7.5 and `@openai/agents-realtime` 0.21.0 were used. Each ran against a local server speaking the provider's wire format, and Verdict, Anthropic and TypeSafe also ran under weave 0.54.0 against Weft, for comparison. Playwright (Chromium) drove the embedded UI. Presigned image URLs were answered by a Playwright route, since the browser has no direct network.
5. **Endpoint coverage and shapes.** Not re-run: the commits under audit add or change no route registrations (`git diff 7962c9e 4ef1de7 -- internal/weft/server/`), so §17 is carried over.
6. **Official client.** `scripts/weft_weave_compat_check.py` was run with weave 0.54.0.
7. **Unchanged rows.** Rows whose code was not touched keep the evidence from the previous audits. Spot numbers A1–A15 in those rows refer to the second audit's log (this file at `7962c9e`). The full suites below were re-run and pass.
8. **Test suites (all re-run, all pass):**
   - `go test ./internal/weft/... ./cmd/weft/...`
   - Python SDK: 247 tests, live tests included (`WEFT_TEST_SERVER_URL`)
   - TypeScript SDK: 109 tests, live included; `tsc --noEmit` clean
   - UI: vitest 130 tests; `tsc --noEmit` clean
   - Harness plugins (`node --test plugins/weft-*/test/*.test.mjs`): 32 tests, 31 pass and 1 skipped. The skipped one is the real-Pi test, which needs `WEFT_TEST_PI_MODULES` and was not re-run, since the Pi plugin did not change.

Status key: **Impl** = implemented, **Partial** = present with stated gaps, **Missing** = no code found.

## Summary

| Feature group | Impl | Partial | Missing |
|---|---|---|---|
| 1. Tracing | 25 | 0 | 0 |
| 2. Integrations | 20 | 0 | 0 |
| 3. Objects and versioning | 9 | 0 | 0 |
| 4. Datasets | 7 | 0 | 0 |
| 5. Models | 4 | 0 | 0 |
| 6. Prompts | 3 | 0 | 0 |
| 7. Evaluations, scorers, guardrails | 13 | 0 | 0 |
| 8. Leaderboards | 3 | 0 | 0 |
| 9. Feedback, annotations, queues | 7 | 0 | 0 |
| 10. Monitors, signals, automations | 5 | 0 | 0 |
| 11. Agents | 9 | 0 | 0 |
| 12. Playground | 9 | 0 | 0 |
| 13. Costs | 4 | 0 | 0 |
| 14. UI views and export | 14 | 0 | 0 |
| 15. SDKs and client APIs | 11 | 0 | 0 |
| 16. Admin, settings, security | 11 | 0 | 0 |
| **Total (154 rows)** | **154** | **0** | **0** |

Hosted-W&B-only features are **out of scope and not counted** in these totals or listed as gaps. They are listed in §18 with the reason for each: billing, SaaS organisation management, the W&B Models integration, W&B Inference, W&B Registry and the self-managed W&B deployment.

**API endpoints:** Weft routes **129 of 129** live OpenAPI method/path pairs and **118 of 118** vendored-SDK pairs. **29 of 29** checked response shapes contain every required top-level property (carried over from the second audit; no routes changed). **Official client:** `weft_weave_compat_check.py` passed 10 of 10 checks with weave 0.54.0 (B11).

**Claimed fixes found broken or incomplete, and new problems** (details in §19 and §21):
- **Egress guard behind an HTTP(S) proxy (B10).** (1) After the first proxied request, the proxy's own `host:port` is dialled without checks for any later destination. With this container's loopback proxy, a RemoteScorer URL `http://127.0.0.1:33021/__agentproxy/status` reached the proxy, and the same URL was refused before the first proxied request. (2) Behind a proxy the destination is checked against a local pre-resolution, not the dialled address. A name the Weft host cannot resolve goes to the proxy unchecked (`127.0.0.1.nip.io` reached the proxy, which reset it), and DNS rebinding between the two lookups is not caught. `API.md` says the dial-time check covers rebinding and that, with a proxy, the destination is checked before hand-off. It does not say that unresolvable names pass, or that rebinding is not covered in that case. Without a proxy, neither issue applies. Row 16.11 is Partial for this reason.
- **Inconsistent mixed-DNS handling (B10).** A public name that also has a private record (simulated for `example.com` in `/etc/hosts`) is refused through the proxy, because every pre-resolved address must be public. Without a proxy it is allowed, because the dialer skips the private address. Both are safe, but the result depends on whether a proxy is configured.
- **A 44.9 MB Linux `weft` binary is committed at the repository root** (added in `0355d2d`, still tracked at `4ef1de7`). `.gitignore` covers `bin/` but not `/weft`.
- **Minor.** For URIs in an unregistered bucket, the resolver retries a failed batch one URI at a time even though the refusal is per bucket: 40 such URIs on one call page made 14 requests, all 400 (only the visible ones are resolved, and refusals are cached for a minute). `patchRealtimeSession()` with no argument imports `@openai/agents-realtime` relative to the Weft SDK, so it returns `false` when the package is not resolvable from there; passing the module works.

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
| BYOB media references | tracking/byob-references | Impl | **Server:** registered customer buckets (`s3://` SigV4, `gs://` V4 signing, team or project scope, `prefix`) are presigned by `/files/resolve_url` (`server/refbuckets.go`, `objstore/gcs.go`; `TestReferenceBucketsS3AndGCS`). **UI:** a string value that is exactly one `s3://`/`gs://` object URI, in any field, renders as media (`isReferenceUri`/`referenceUriSpec` in `components/media/mediaUtils.ts`). Resolution is lazy, batched per bucket and cached until expiry (`uriResolver.ts`, `useResolvedUri.ts`; `uriResolver.test.ts`, `refUri.test.ts`). Spot B1 (Playwright on the embedded UI, which is identical to a fresh `vite build`): an op returning the bare string `"s3://cust-media/frames/000123.png"` renders an `<img>` from the SigV4 URL after one resolve request. `gs://…/a.mp4` and `gs://…/a.wav` render `<video>` and `<audio>` from `GOOG4-RSA-SHA256` URLs, and `Content.from_uri` still renders. Strings that are not media objects make no request: prose that mentions a URI, `https://…png`, a folder (`…/frames/`) and an ARN stay text, and `.parquet` or extension-less keys get a **Get download link** button that resolves only when clicked. An unregistered bucket shows "not rendered: bucket not registered for this project" after a 400. Weft also renders audio and PDFs inline, where Weave renders images and video |
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
| Verdict | integrations/verdict | Impl | `integrations/verdict.py` (`WeftVerdictTracer` on every `Pipeline`) keeps a finished block's call resolvable, so the next block, which Verdict starts with the finished block as parent, nests under it. Tests: `test_parity_gaps.py`, live `test_verdict_real_library_one_trace_live`. Spot B2 with the **real `verdict` package**: `Pipeline("double") >> MapUnit >> MapUnit` gave 5 calls in **1 trace** on each of 3 runs. The tree is the same as weave 0.54.0's for the same script against Weft: `double` → `Map` → (`Map`, `MapUnit`), and `MapUnit`. Weft's op names carry a `verdict.` prefix (`verdict.Map`); Weave's do not |
| OpenAI Realtime audio | integrations/openai-realtime-audio | Impl | Python `openai_realtime.py` patches the `openai` package's realtime connections and, for Realtime hosts (plus `WEFT_REALTIME_HOSTS`), `websockets.connect` (the transport of the Agents SDK `RealtimeRunner`), `websocket-client` and `aiohttp`. TS `openai_realtime.ts` adds `patchRealtimeSession`, `instrumentRealtimeSession` and `wrapRealtimeSession` for `@openai/agents-realtime`. User audio (cut at VAD markers or commits) and model audio are saved as WAV `Audio` (PCM16 and G.711). Tests: `test_realtime_audio.py` and TS `realtime.test.ts` (the TS one uses a fake `RealtimeSession`). Spot B3, against a local server speaking the Realtime event protocol, with nothing patched by hand beyond `weft.init`: raw `websockets.connect` and the **real OpenAI Agents SDK `RealtimeRunner`** each gave `openai.realtime.session` → `openai.realtime.response`, with usage 150/80. The response's input WAV (24 kHz mono, 7,200 frames) and output WAV (2,400 frames) are byte-identical to the PCM sent and received. The **real `@openai/agents-realtime` 0.21.0** `RealtimeSession`, after `patchRealtimeSession(module)`, gave the same tree and the same WAVs. Called with no argument, `patchRealtimeSession()` imports the package from the SDK's own location; in the audit layout (SDK build and package in unrelated directories) that import failed and it returned `false` |
| TypeSafe (`typesafe_sdk` `system_one` → chat spans) | weave 0.54.0 `integrations/typesafe` (no docs page) | Impl | `integrations/typesafe.py` (sync and async clients) and TS `typesafe.ts` record each `system_one` as an LLM `chat` span: `gen_ai.provider.name=typesafe`, request and response model, response id from `x-typesafe-request-id`, the questions, JSON output and usage. Tests: `test_parity_audit2.py` (2), live `test_typesafe_live`, TS `parityAudit2.test.ts` (2). Spot B4 with the **real `typesafe_sdk` 0.7.5** against a local endpoint, run under Weft and under weave 0.54.0: same chat-span input, output and usage (120/12 under `jev-2026-10`). One difference in structure: inside an op, Weft nests the chat span under the op, while Weave starts a separate `invoke_agent typesafe SDK` trace that points back to the op through `weave.parent_call.*`. Outside an op both record `invoke_agent` → `chat` |
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
| Remote scorers (bearer, headers, OAuth client credentials) | evaluation/remote-scorers, remote-scorer-signals | Impl | `RemoteScorer` (`evals/scorers_extra.go`), `evals/oauth.go`; `TestRemoteScorerOAuth`; spot A12 (the call works; egress controls are row 16.11, spot B9) |
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
| Agents BYOB references | agents-byob-references | Impl | `conversation.py`/`conversation.ts` write `{"type": "uri"}` parts (`attach_media(uri=…)`), which `detectPart` renders, and the Agents conversation view shows media tool results under the tool row (`AgentTimeline.tsx`, `ChatView.tsx`). Spot B1: on the conversation page, an LLM `attach_media(uri="s3://…/000125.png")` and a tool result `"s3://…/000126.png"` render as two `<img>` from one batched resolve request. The LLM and tool call pages render them too |
| Deep link to a conversation (`weave.trace.urls.agent_conversation_path`) | custom-agents-quickstart | Impl | Python `weft.urls.agent_conversation_path(entity, project, conversation_id)` (Weave's signature) and TS `agentConversationPath` / `client.conversationUrl` build `<server>/<entity>/<project>/conversations/<id>`, with the id URL-encoded. Weave's helper builds `…/weave/agents/conversations/<id>`, and the Weft UI redirects that form (`weaveLinks.ts`; `weaveLinks.test.ts`). Spot B6: both URLs open the conversation page |

## 12. Playground

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Built-in providers | tools/playground | Impl | `llm/llm.go`, `/llm/models`; `llm` tests |
| Custom OpenAI-compatible providers | tools/custom-runtimes | Impl | `Provider` objects; `TestCustomRuntimeApply` |
| Params, tools, JSON mode, streaming | tools/playground | Impl | `PlaygroundPage.tsx`, `/completions/create_stream`; `playground.test.ts` |
| Side-by-side comparison | tools/playground | Impl | `PlaygroundPage.tsx` panes; `playground.test.ts` |
| Load from call or prompt; template variables | tools/playground | Impl | `fillVars`, `?call=`/`?prompt=`; `playground.test.ts` |
| Save as model | tools/playground | Impl | `playground.test.ts` |
| Custom runtimes (REST, UI, SDK registration) | tools/custom-runtimes | Impl | REST `server/runtimes.go`, UI binding, and SDK registration: Python `client.register_custom_runtime(name=, base_url=, runtime_ids=[str \| weft.CustomRuntimeID(id=, max_tokens=)], api_key_secret=, headers=)` and TS `client.registerCustomRuntime({name, baseUrl, runtimeIds, apiKeySecret, headers})`. Tests: `TestCustomRuntimeApply`, `test_register_custom_runtime`, live `test_custom_runtime_and_threads_live`, TS `parityAudit2.test.ts`. Spot B6: Weave's docs example ran in both SDKs with only `weave` → `weft` changed. `GET /v2/{e}/{p}/runtimes` lists both IDs (max tokens 4096 and 8192), and registering again with one ID removed the other. A runtime registered from Python for a local OpenAI-compatible server answered `/completions/create` with model `local/fake-1`; the server sent the stored secret as the bearer token and the custom header. That ran on a server with `--egress-allow-private`; the default server refuses a loopback base URL (row 16.11) |
| History of past runs | tools/playground | Impl | `HistoryPanel.tsx`; `playground.test.ts` |
| Image generation (`/image/create`, Image mode) | OpenAPI | Impl | `files_media.go`, `llm/images.go`, `ImageMode.tsx`; `TestImageCreateMock` |

## 13. Costs

| Feature | Weave | Weft status | Evidence |
|---|---|---|---|
| Automatic costs with roll-up, including OTel spans | tracking/costs | Impl | `store/costs.go`; `TestDefaultPriceLookup`, `TestRootCostsNotDoubled`; compat check (no double counting) |
| Custom costs add, query, purge (SDK, API, UI) | tracking/costs, cookbooks/custom_model_cost | Impl | `/cost/*`, Settings → Costs; tests |
| `/calls/usage`, `/trace/usage` | OpenAPI | Impl | `compat_extra.go`; shape checks |
| Cache-aware pricing (cache read and cache write tokens priced at their own rates) | custom-agents-quickstart "Record token usage and cost" | Impl | Prices carry `cache_read_input_token_cost` and `cache_creation_input_token_cost` (`store/costs.go`), and the built-in table includes cache rates (Settings → Costs shows and edits them). Each call's own usage entry is normalised before summing: OpenAI `prompt_tokens_details.cached_tokens`, Anthropic net `input_tokens`, Gemini `cached_content_token_count`, OTel `gen_ai.usage.cache_read.input_tokens`. Tests: `store/costs_cache_test.go`, `server/costs_cache_test.go`, `test_cached_tokens.py`, `cachedTokens.test.ts`. Spot B5: the worked example (`claude-sonnet-4-5`, 10,000 prompt tokens of which 9,000 are cache reads, 100 output) costs **$0.0072**, as in Weave. It does so when logged with `LLM.record`, through Weft's Anthropic integration against a local Messages endpoint returning `input_tokens` 1,000 plus `cache_read_input_tokens` 9,000, through weave 0.54.0's Anthropic integration, and over OTLP. Roll-ups do not double count: a parent of two such calls costs $0.0144 from the Weft SDK, from the weave client (which sends a cumulative parent summary) and over OTLP with the children sent before the parent. `/calls/stats` gives $0.0288 for 4 LLM calls; Agents `total_cost_usd`, `/agents/conversations/chat` and `/threads/query` `cost_usd` give $0.0144 |

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
| Official `weave` client against Weft | n/a | Impl | `scripts/weft_weave_compat_check.py`: 10/10 with weave 0.54.0 (B11); `wandbcompat.go` |
| `get_evaluations`, `evaluate_model`, compare, leaderboard helpers | SDK | Impl | `_client.py`; `test_client.py`, `test_parity.py`; TS `sdkExtras.test.ts` |
| W&B login shim (`/graphql`) | n/a | Impl | `wandbcompat.go`; `TestGraphQLShim`; compat check logs in |
| TypeScript SDK breadth | reference/typescript-sdk | Impl | calls, ops, objects, evals, agent tracing, SavedView, BYOB, WAL, OTel helper, integrations, NER redaction, registry, run linkage, custom runtimes, Realtime sessions; 109 tests |
| SavedView query/apply API (Py and TS) | tools/saved-views | Impl | `_saved_view.py`, `savedView.ts`; tests |
| Client-side server-response cache | platform/server-caching | Impl | `_cache.py`, `WEFT_USE_SERVER_CACHE`, `WEFT_SERVER_CACHE_DIR`, `WEFT_SERVER_CACHE_SIZE_LIMIT`; tests. Python only, like Weave |
| Environment variables | core-types/env-vars | Impl | `WEFT_*` equivalents cover disable, capture code, print link, parallelism, WAL, retry attempts and interval, max queue size, HTTP timeout, log level, PII redaction, autopatch (`WEFT_AUTOPATCH`, `WEFT_DISABLED_INTEGRATIONS`), server cache and the harness `WEFT_AGENT_NAME`/`WEFT_CAPTURE_CONTENT`. New: `WEFT_DEBUG_HTTP` (Weave's `WEAVE_DEBUG_HTTP`) prints every request and response to stderr with Authorization redacted, and `WEFT_DISABLE_WAL_SENDER` (with the WAL on) writes requests to the WAL and sends nothing, for a later run to replay. Both SDKs; tests `test_debug_http`, `test_disable_wal_sender` and two in TS `parityAudit2.test.ts`. Spot B7 (Python): the debug output listed method, URL, headers and bodies, and the API key never appeared. With the sender disabled, the call was not on the server and the WAL held 3 lines; the next run without the switch sent it and removed the WAL |
| Lower-level client methods used in the guides | tracking/threads, core-types/prompts-version, tracking/feedback | Impl | `client.threads_query(ThreadsQueryReq(…))` and `threads_query_stream`, with Weave's request and response types in `weft.trace_server_interface` (`ThreadsQueryReq`, `SortBy`, `ThreadSchema`); `weft.require_current_call()` with `NoCurrentCallError`; `client.list_aliases()`; `client.get_tags_and_aliases(ref)`. Tests: `test_threads_query`, live `test_custom_runtime_and_threads_live`. Spot B6: Weave's threads examples (sort by `turn_count`; `sortable_datetime_after`) ran with Weft's imports and returned 3 threads with 3, 2 and 1 turns and `datetime` fields. `require_current_call` returned the op's call id inside an op and raised outside one. `list_aliases` → `['staging']`; `get_tags_and_aliases` → `(['prod-ready'], ['latest', 'staging'])`. Like the guides, Weft offers these as client methods; Weave also has module-level `weave.list_aliases()`/`weave.get_tags_and_aliases()`, which `weft` lacks |

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
| Reference buckets: register per team or project, test connection, list, delete | tracking/byob-references "Register the reference bucket" | Impl | `/reference_buckets/create|test|list|delete` (`server/refbuckets.go`; server-admin only, `TestReferenceBucketsNeedServerAdmin`), plus Settings → **Buckets** (`BucketsTab.tsx`; `buckets.test.tsx`). The tab lists the project's and the team's buckets; server admins get a Register dialog (scope, provider, bucket, prefix, region, endpoint, credentials secret, test object key) and per-row **Test** and Remove. Credentials come from stored secrets or `AWS_*` and are never returned. Spot B8: the tab listed the team S3 and project GCS registrations. **Test** on the S3 row reached real S3 through the egress guard and the container's proxy, and showed AWS's 403 for the fake key. Weft still does not check the bucket's CORS as Weave does; the tab and `API.md` say so |
| Outbound request controls for remote scorers (host allowlist, private, loopback and metadata blocking, `http://` opt-in) | evaluation/remote-scorers (`WF_SCORING_WORKER_REMOTE_SCORER_*`) | Impl | **Fixed after this audit (commit after 4ef1de7), verified by the lead with this audit's own reproduction, not by an independent re-audit:** the proxy is permitted only for the request that chose it (per-request context, not a process-wide set), names that cannot be resolved locally behind a proxy are refused (`reason=unresolved`) unless listed in `--egress-allow`, and every resolved address is checked before dialling with or without a proxy. Live, through this container's proxy: proxy status URL refused, public https scorer through the proxy works, proxy status URL refused again, `127.0.0.1.nip.io` refused. Tests: `TestProxyAddressNotReachableDirectly`, `TestMixedResolutionRefusedDirect` (both fail on 4ef1de7), updated `TestClientChecksDestinationBehindProxy`. Remaining limit, documented in API.md: the proxy re-resolves the name, so DNS changes between the two lookups are not caught. Original audit finding: `internal/weft/egress` guards every user-supplied destination: remote scorers, `predict_url`, OAuth token URLs, webhooks, provider and custom-runtime base URLs, bucket tests. Each dialled address is checked; loopback, private, link-local, CGNAT, cloud-metadata and reserved addresses are refused, including their IPv4-mapped, NAT64 and 6to4 forms. `--egress-allow` (hosts, `host:port`, `*.domain`, IPs, CIDRs), `--egress-allow-private` (metadata still refused) and `--remote-scorer-allow-insecure-http`; RemoteScorer and `predict_url` do not follow redirects. Tests: `egress/egress_test.go`, `server/egress_test.go`, `evals/egress_test.go`, `evals/remote_https_test.go`, `cmd/weft/egress_test.go`. Spot B9: `http://` scorer URLs are refused. `https://` to `127.0.0.1`, `localhost`, `[::1]`, `[::ffff:127.0.0.1]`, `169.254.169.254`, `10.0.0.1`, `[fd00:ec2::254]` and `10.0.0.5.nip.io` return "destination not allowed", and the listener got no request. Public hosts through the container's HTTPS proxy work, and an IPv6-only name is not refused. The allowlist, allow-private and no-redirect switches behaved as documented. **Gaps, both only with an HTTP(S) proxy in the environment (B10):** (1) after one request has gone through the proxy, the proxy's `host:port` is dialled unchecked for any later destination, so with a loopback proxy a scorer URL `http://127.0.0.1:33021/…` reached the proxy (it was refused before the first proxied request); (2) with a proxy the check uses a local pre-resolution, not the dialled address. A name the Weft host cannot resolve is handed to the proxy unchecked (`127.0.0.1.nip.io` here), and a name that re-resolves differently at the proxy is not caught. `API.md` says the dial-time check covers DNS rebinding and that, with a proxy, the destination is checked before hand-off; it does not say that unresolvable names pass or that rebinding is then not covered |

## 17. API endpoint coverage

Method/path pairs, from `endpoint_coverage.py` in the second audit. The live probe agreed on every pair. Not re-run here: no route registrations changed between `7962c9e` and `4ef1de7`.

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

Rows whose status changed after verification (12):

| Row | Before | Now | Reason |
|---|---|---|---|
| BYOB media references (1.16) | Partial | Impl | Plain `s3://`/`gs://` strings render as image, video and audio from presigned URLs; non-media strings make no request (B1) |
| Verdict (2.17) | Partial | Impl | Real `verdict`: 1 trace per pipeline on 3 of 3 runs, same tree as weave 0.54.0 (B2) |
| OpenAI Realtime audio (2.18) | Partial | Impl | Byte-exact WAV input and output audio; real Agents SDK `RealtimeRunner`, raw `websockets` and real `@openai/agents-realtime` `RealtimeSession` traced (B3) |
| TypeSafe (2.19) | Missing | Impl | Real `typesafe_sdk` 0.7.5: same chat span as Weave; nests under the op instead of a linked trace (B4) |
| Agents BYOB references (11.8) | Partial | Impl | `attach_media(uri=…)` parts and `s3://` tool results render on the conversation page (B1) |
| Conversation deep link (11.9) | Partial | Impl | `weft.urls.agent_conversation_path`, TS `agentConversationPath`; Weave-format links redirect (B6) |
| Custom runtimes (12.7) | Partial | Impl | `register_custom_runtime` / `registerCustomRuntime` with Weave's docs example; a registered runtime answered the playground endpoint (B6) |
| Cache-aware pricing (13.4) | Missing | Impl | The worked example costs $0.0072 by four routes; no double counting in roll-ups, stats, Agents or threads (B5) |
| Environment variables (15.10) | Partial | Impl | `WEFT_DEBUG_HTTP` (key redacted), `WEFT_DISABLE_WAL_SENDER` (WAL only, replayed later) (B7) |
| Lower-level client methods (15.11) | Partial | Impl | `threads_query` with Weave's request types, `require_current_call`, `list_aliases`, `get_tags_and_aliases` (B6) |
| Reference-bucket registration (16.10) | Partial | Impl | Settings → Buckets with Register dialog and Test; CORS still not checked (B8) |
| Outbound request controls (16.11) | Missing | **Partial** | The guard, allowlist, allow-private, https-only and no-redirect work (B9). Behind an HTTP(S) proxy, the proxy address is reachable after first use, and names are checked by local pre-resolution only (B10) |

Unchanged status, new detail:
- **Automatic costs with roll-up** (13.1) stays Impl. Cache prices do not double count in parents for the Weft SDK, the weave client's cumulative summaries or OTLP children sent first (B5).
- **TypeScript SDK breadth** (15.7) now has 109 tests, including custom-runtime registration and Realtime sessions.

## 20. Remaining gaps

Ordered by user impact. Hosted-W&B-only features are not gaps (§18).

1. ~~Egress guard behind a proxy (16.11)~~ and ~~the committed `weft` binary~~: fixed after this audit (see row 16.11; the binary is untracked and `/weft` is in `.gitignore`, though it remains in the branch history).
2. **Smaller items.** Bucket CORS is not checked (Weave checks it; the UI warns instead). The resolver retries each URI of an unregistered bucket separately after the batch fails. Module-level `weft.list_aliases()`/`weft.get_tags_and_aliases()` do not exist (client methods do). TypeSafe calls inside an op nest under the op rather than forming Weave's separate linked trace. `patchRealtimeSession()` without an argument depends on module resolution from the SDK's location.

## 21. Spot-check log

All checks ran against `127.0.0.1:6530` on a build of `4ef1de7`, plus the egress instances listed in Method 2. Scripts are in the audit scratchpad (`audit3/scripts/`; `byob.py`, `media.js`, `verdict_real.py` and `verdict_weave.py` are the second audit's scripts pointed at `:6530`). **FAIL** marks a claimed fix that did not hold, and **GAP** marks a newly found problem.

- **B1 BYOB rendering (Playwright).** Setup: team `demo` registered `s3://cust-media` with `skip_test`, and project `demo/byob3` registered `gs://cust-gcs` with a service-account secret. Presigned URLs were answered by a Playwright route with a 1×1 PNG.
  - Images and media: the bare string `"s3://cust-media/frames/000123.png"` rendered an `<img>` (SigV4 URL) after 1 resolve request; it rendered the same way in a nested output field. `gs://cust-gcs/clips/a.mp4` and `a.wav` rendered `<video>` and `<audio>` (`GOOG4-RSA-SHA256`). `Content.from_uri` still rendered. A 40-URI list: the visible 13 were resolved in one batch; the rest stayed text until scrolled near.
  - Not media: `"see s3://…png"`, `https://example.com/a.png`, `s3://cust-media/frames/` and `arn:aws:s3:::…` stayed text, with no request. `…/part-0001.parquet` and `…/models/checkpoint` showed **Get download link**, with no request until clicked. `s3://other-bucket/x.png`: 1 request → 400, and the note "not rendered: bucket not registered for this project". The Traces table shows URIs as text and makes no requests.
  - Agents: on the conversation page, `attach_media(uri="s3://…/000125.png")` and the tool result `"s3://…/000126.png"` rendered as two `<img>` from one batched request. The LLM and tool call pages rendered them as well. No page errors.
  - When the image itself fails to load (no route), each URI is re-resolved once and then falls back to text.
- **B2 Verdict.** `verdict_real.py`, 3 runs: 15 calls in 3 traces, each `verdict.double` → `verdict.Map` → (`verdict.Map`, `verdict.MapUnit`), `verdict.MapUnit`. `verdict_weave.py` (weave 0.54.0): 5 calls in 1 trace with the same tree.
- **B3 Realtime.** `realtime_e2e.py`, with `WEFT_REALTIME_HOSTS=127.0.0.1` and a local websocket server speaking GA Realtime events (`session.created`, VAD markers, `conversation.item.added`, `response.output_audio.delta`, `response.done` with usage). Nothing was patched by hand beyond `weft.init`.
  - `websockets.connect` and the real `agents.realtime.RealtimeRunner` each gave `openai.realtime.session` → `openai.realtime.response`.
  - Audio: downloaded from `/file/content`, the input WAV (24 kHz, mono, 7,200 frames) and the output WAV (2,400 frames) equal the PCM sent and received; usage was 150/80.
  - TS: `rt.mjs` with the real `@openai/agents-realtime` 0.21.0 `RealtimeSession` over `OpenAIRealtimeWebSocket` and `patchRealtimeSession(module)` gave the same tree and WAVs. `patchRealtimeSession()` without the module returned `false` in this layout.
- **B4 TypeSafe.** Real `typesafe_sdk` 0.7.5 against a local `/v1/systemone`, with `classify(msg)` wrapping `system_one` and one bare `system_one` call.
  - Weft: `classify` → `chat` (provider `typesafe`, request model `jev-latest`, response model `jev-2026-10`, response id `req_abc`, questions, JSON output, usage 120/12, also rolled up into `classify`). The bare call gave `invoke_agent` (`typesafe SDK`) → `chat`.
  - Weave 0.54.0: `classify` on its own trace, plus `invoke_agent typesafe SDK` → `chat jev-latest` linked by `weave.parent_call.id`, with the same usage. The bare call gave the same structure as in Weft.
- **B5 Cache pricing** (`claude-sonnet-4-5`; $3, $15 and $0.30 per 1M tokens).
  - Weft SDK, Anthropic integration against a local Messages endpoint (`input_tokens` 1,000, `cache_read_input_tokens` 9,000, `output_tokens` 100): each call has gross `prompt_tokens` 10,000 and costs $0.0072 (uncached $0.003 + cache read $0.0027 + output $0.0015). The parent op costs $0.0144.
  - `LLM.record(usage=Usage(input_tokens=10000, output_tokens=100, cache_read_input_tokens=9000))`: $0.0072 per call and $0.0144 for `invoke_agent`. Agents `total_cost_usd`, chat `total_cost_usd` and `/threads/query` `cost_usd` were 0.0144.
  - weave 0.54.0 with its Anthropic integration: each call $0.0072, and the parent (a cumulative client-side summary) $0.0144, not $0.0288.
  - OTLP: two child spans with `gen_ai.usage.cache_read.input_tokens` 9,000, sent before their parent in separate requests: $0.0072 each and $0.0144 for the parent.
  - `/calls/stats` on the project: 40,000 prompt tokens and $0.0288 for 4 LLM calls.
- **B6 SDK helpers** (`sdk_extras.py`, `rcr.mjs`).
  - Custom runtimes: Weave's docs example (`support-v12`, `CustomRuntimeID("support-canary", 8192)`, secret, header) returned a `CustomRuntimeApplyRes` and showed in `GET /v2/demo/sdk3/runtimes`. Re-registering with only `support-v13` left one ID. The TS `registerCustomRuntime` example returned `{data, error: null}`. On `:6534`, a runtime for `http://127.0.0.1:6525/v1` answered `/completions/create` (`local/fake-1`) with bearer `agent-key-1` and `X-Tenant-ID: t1`; on `:6530` the same call returned "calling provider: destination not allowed".
  - Threads: `threads_query(ThreadsQueryReq(sort_by=[SortBy(field="turn_count", direction="desc")]))` → `th-2` 3, `th-1` 2, `th-0` 1; the `sortable_datetime_after` example → 3 threads with `datetime` fields.
  - Other helpers: `require_current_call()` returned the op's id inside an op and raised `NoCurrentCallError` outside. `list_aliases()` → `['staging']`; `get_tags_and_aliases()` → `(['prod-ready'], ['latest', 'staging'])`.
  - Links: `agent_conversation_path("demo", "sdk3", "conv/1 x")` → `http://127.0.0.1:6530/demo/sdk3/conversations/conv%2F1%20x`. `/demo/support-bot/weave/agents/conversations/<id>` redirected to the conversation page.
- **B7 SDK environment variables.** `WEFT_DEBUG_HTTP=1` printed time, thread, method, URL, headers and bodies for every request; the API key `sekrit-token-123` appeared 0 times. `WEFT_ENABLE_WAL=1 WEFT_DISABLE_WAL_SENDER=1`: the call with input 7 was not on the server and the WAL had 3 lines. The next run without the switch sent it (inputs 1, 1, 7 and 9 present) and removed the WAL file.
- **B8 Buckets tab.** The tab listed `s3://cust-media/` (team demo, S3 us-east-1, server environment) and `gs://cust-gcs/` (project, GCSCRED) as "not tested", with a CORS warning. **Test** on the S3 row sent `POST /reference_buckets/test`, which reached S3 through the proxy and showed "reading the test object failed: 403 The AWS Access Key Id you provided does not exist". The Register dialog has scope, provider, bucket, prefix, region, endpoint, credentials secret and test object key. No page errors.
- **B9 Egress guard** (RemoteScorer via `/calls/score`; a loopback listener on `:6519` counted hits).
  - Default server: `http://127.0.0.1:6519/…` → "RemoteScorer url must use https". `https://` to `127.0.0.1`, `localhost`, `[::ffff:127.0.0.1]`, `[::1]`, `169.254.169.254`, `10.0.0.1`, `[fd00:ec2::254]` and `10.0.0.5.nip.io` → "remote scorer: destination not allowed", with 0 listener hits. `0x7f000001` and `2130706433` went to the proxy, which answered 403.
  - Public hosts through the proxy work: postman-echo and httpbin echoed the scorer body, `api.anthropic.com` answered 401, and a built-in OpenAI provider call returned OpenAI's 401 for a fake key. `ipv6.google.com` and an `/etc/hosts` IPv6-only name were not refused by Weft (the proxy answered 502). With `NO_PROXY=*`, the IPv6 dial failed for lack of IPv6, not by policy.
  - `--egress-allow 127.0.0.1:6520,localhost:6519`: `http://localhost:6519/ok` reached the listener; `http://127.0.0.1:6519` was refused (hostname entries match the URL's name); `:6520`, which answers 307, failed with "remote scorer returned 307" and the redirect was not followed.
  - `--egress-allow-private`: `127.0.0.1` reached the listener; `169.254.169.254` and `0.0.0.0` were still refused.
  - Seed data, the official client and all SDK suites ran unaffected.
- **B10 Egress guard behind a proxy (GAP).**
  - Proxy address: on a server with the container's `HTTPS_PROXY=http://127.0.0.1:33021` and `--remote-scorer-allow-insecure-http`, `http://127.0.0.1:33021/__agentproxy/status` → "destination not allowed". Then `https://postman-echo.com/post` succeeded through the proxy, and the same URL then returned "remote scorer returned 405: agent-proxy relay: this proxy only accepts HTTPS CONNECT tunnels…": the request reached the proxy. `http://localhost:33021/…` stayed refused, because the set is keyed by the proxy's literal `host:port`. Cause: `egress.NewTransport` stores the proxy address in a process-wide `proxies` set, and `dial` skips all checks for addresses in it.
  - Local pre-resolution: `https://127.0.0.1.nip.io:6519/x` took 6.3 s and failed with "read tcp …->127.0.0.1:33021: connection reset by peer". The name did not resolve locally (this resolver drops loopback answers), so it was handed to the proxy unchecked, and the proxy refused it. `10.0.0.5.nip.io`, which resolves locally, was refused by Weft.
  - Mixed DNS: with `10.0.0.7` and `104.20.23.154` both listed for `example.com` in `/etc/hosts`, in either order, `https://example.com/` was refused through the proxy but reached `example.com` (405) on the `NO_PROXY=*` server. With the hosts file restored, both reached it.
- **B11 Official client.** `weft_weave_compat_check.py`: 10/10 with weave 0.54.0, including "usage rolled up once, not double counted".
- **B12 UI smoke** (Playwright): Traces, a call page, Agents (dashboard: 40 spans, 6 conversations, $0.0181), a conversation page, the Weave-format conversation link, Evaluations (2 seeded runs), and Settings General, Costs (input, output, cache read and cache write columns) and Buckets. No page errors, no console errors, no 5xx. The built-in cache prices checked against Anthropic's published rates, for example `claude-sonnet-4-5` $0.30 and `claude-fable-5-1` $0.25 per 1M cache reads.
