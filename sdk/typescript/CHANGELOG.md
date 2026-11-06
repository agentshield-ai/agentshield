# Changelog

## Unreleased: Weft's own trace and object API

The SDK now speaks only Weft's own API (`/api/v1`, `docs/weft/API.md`) and
uses its names ([DESIGN.md](../../docs/weft/DESIGN.md) §10). It needs a
server with that API (this repository's server, which ships with the SDK);
the older route shapes are no longer used.

### Security

- **`weft.get()` no longer evaluates code stored on the server (breaking).**
  Reading a function (`weft.get('fn')`, `client.get(ref)`, `ref(...).get()`)
  used to build it with `new Function` from the stored source, so any project
  writer could run code on every machine that read it. It now returns an
  inert `weft.StoredOp` (`name`, `code`, `language`, `ref`; not callable).
  Opt in explicitly for projects you trust: `weft.loadOp(ref, { trustCode: true })`
  / `client.loadOp(ref | stored, { trustCode: true })`,
  `weft.get(ref, { trustCode: true })`, or `WEFT_ALLOW_CODE_LOAD=1`. `loadOp`
  without trust throws `OpLoadError`.
- **No redirects.** Requests use `redirect: 'manual'` and every 3xx fails with
  a `WeftHTTPError` carrying the status, instead of resending the API key and
  the body to the redirect target.
- **Redaction covers common secret names and values.** Key names are
  normalised (case, `-`, `_`, `.`) and matched against more defaults and
  patterns (`openai_api_key`, `client_secret`, `refresh_token`,
  `aws_secret_access_key`, `private_key`, `passwd`, `credentials`,
  `X-Api-Key`, `Cookie`, `Proxy-Authorization`, ...); `Bearer ...`, `sk-...`,
  `AKIA...`, GitHub/Slack tokens, PEM private keys and URL passwords are
  redacted inside strings (`weft.isSecretKey`, `weft.redactSecretValues`). A
  custom `redactKeys` list is matched exactly (normalised).
  `WEFT_REDACT_KEYS=0` turns both off.
- A warning is logged once when an API key is sent over plain `http://` to a
  non-loopback host.
- The write-ahead log (`enableDiskFallback`) is created `0600` in a `0700`
  directory; existing ones are tightened.

### New names

- `weft.trace(fn, opts)` traces a function; every call of it is a **span**.
- `await weft.save(obj, name)` saves a version and returns its ref;
  `await weft.get(ref)` reads one (a `Ref`, a `weft:///` URI, `'name'`,
  `'name:v3'`, `'name:latest'` or `'name:<alias>'`).
- `client.spans(opts)` and `client.span(id)` (null when the span does not
  exist) query spans; `client.spanRecords`, `client.countSpans`,
  `client.deleteSpans`, `client.evaluationRuns()`, `client.listAliases()`,
  `client.readObject(projectId, name, version)`.
- `weft.Span` with `name`, `baseName`, `error`, `sessionId`, `turnId`;
  `weft.SpanFeedback`; `weft.getCurrentSpan()`.
- Ref kinds `function` and `span`; refs with the earlier kinds `op` and
  `call` are still read.
- Span filters use `names`, `ids`, `session_ids` (the earlier `op_names`,
  `call_ids`, `thread_ids` are translated).
- `SavedView.spans()` / `toSpansQuery()`; `ApplyScorerResult.scoreSpan`;
  feedback takes `span_ref`.
- `client.createSpan(...)` / `client.finishSpan(span, ...)` start and end a
  span explicitly; `weft.trace(fn, { spanDisplayName })` (also in an
  integration's `traceSettings`) sets each span's display name.
- `autopatchSettings: { <integration>: { traceSettings } }` (type
  `IntegrationTraceSettings`) sets the options of the spans an integration
  records; each wrapper takes the same fields flat or as `traceSettings`, and
  `spanName` renames every span it records (type `WrapperTraceOptions`).
- `SavedView.filterName(name)` shows only spans of one function.
- `client.getSessions({ sessionIds, ... })` lists sessions
  (`POST /api/v1/spans/sessions`); items carry `session_id` and `span_count`.
- `ev.logPrediction(inputs, output, { span })` links a prediction to existing
  spans; `ScoreLogger.predictAndScoreSpan` / `predictSpan` and
  `EvaluationLogger.span` are its spans.
- Conversation spans (`Turn`, `LLM`, `Tool`, ...) expose their recorded span
  as `.span` / `.spanId`.
- `Dataset.fromSpans(spans)`; `client.spanUrl(span)`.
- `WEFT_MAX_SPANS_QUEUE_SIZE` sets the send queue size.
- `client.evaluateModel(...)` resolves to `{ id, status, summary }`;
  `client.compareEvaluations(runIds)`; `client.leaderboard(name, version)`;
  `linkToRegistry` resolves to `{ version, ... }`; `client.addCost` resolves
  to `[{ id, llm_id }]`.

### Platform objects and UI links

Weft's other APIs use its names too ([DESIGN.md](../../docs/weft/DESIGN.md)
§10.5, *Other APIs*), and so do the objects the SDK saves:

- `weft.Monitor` and `weft.AnnotationSpec` save `names` (the span names they
  apply to; earlier `op_names` / `op_scope`, deprecated below). Versions
  saved with the earlier names load with `names` filled in.
- `weft.SavedView` stores its span filter with Weft's names (`names`, `ids`,
  `session_ids`); `setFilter()` takes the earlier ones (`op_names`,
  `call_ids`, `thread_ids`) and stores them under Weft's, and views saved
  with them still work.
- The UI links the SDK prints, `span.uiUrl` and `client.spanUrl()` point at
  `<server>/<entity>/<project>/spans/<id>` (was `/calls/<id>`, which the UI
  still redirects).

### Digests

Objects, table rows, tables and files use Weft's digest: the lowercase hex
SHA-256 of canonical JSON (keys sorted by code point, `,`/`:` separators,
UTF-8, only `"`, `\` and control characters escaped), of the JSON list of row
digests for a table, of the bytes for a file (`canonicalJson`, `digest`,
`tableDigest`, `fileDigest`). `test/digest.test.ts` checks them against the
shared vectors and a live server.

### Deprecated (removed in the next release)

These earlier names still work; each emits one warning the first time it is
used. In Node it is a `DeprecationWarning` (`process.emitWarning`, code
`WEFT_DEPRECATED`), so `process.on('warning')` sees it and `--no-deprecation` /
`--throw-deprecation` apply; where `process.emitWarning` is absent (browsers)
it is a `console.warn`.

| Earlier name | Use |
|---|---|
| `weft.op(fn, opts)` | `weft.trace(fn, opts)` |
| `weft.publish(obj, name)`, `client.publish(...)` | `weft.save(obj, name)`, `client.save(...)` |
| `weft.ref(...).get()` | `weft.get(ref)` |
| `client.getCalls(opts)`, `client.getCallsRecords`, `client.countCalls`, `client.deleteCalls` | `client.spans(opts)`, `client.spanRecords`, `client.countSpans`, `client.deleteSpans` |
| `client.getCall(id)` | `client.span(id)` |
| `client.getEvaluationCalls()` | `client.evaluationRuns()` |
| `weft.Call`, `weft.CallFeedback` (and the `CallData`, `GetCallsOptions` types) | `weft.Span`, `weft.SpanFeedback` (`SpanData`, `SpansOptions`) |
| `weft.getCurrentCall()` | `weft.getCurrentSpan()` |
| `span.opName`, `span.opBaseName`, `span.exception`, `span.threadId` | `span.name`, `span.baseName`, `span.error`, `span.sessionId` |
| `ApplyScorerResult.scoreCall` | `ApplyScorerResult.scoreSpan` |
| `SavedView.getCalls()`, `SavedView.toCallsQuery()` (and the `SavedViewGetCallsOptions` type) | `SavedView.spans()`, `SavedView.toSpansQuery()` (`SavedViewSpansOptions`) |
| `new weft.Monitor({ opNames })`, `monitor.opNames` | `new weft.Monitor({ names })`, `monitor.names` |
| `new weft.AnnotationSpec({ opScope })`, `spec.opScope` | `new weft.AnnotationSpec({ names })`, `spec.names` |
| `weft.withThread(threadId, fn)` (and the `ThreadContext` type) | `weft.withSession(sessionId, fn)` (`SessionContext`) |
| `client.updateCall(span, attrs, { threadId })` | `client.updateSpan(span, attrs, { sessionId })` |
| setting `printCallLink`, env `WEFT_PRINT_CALL_LINK`, `client.printCallLink` | `printSpanLink`, `WEFT_PRINT_SPAN_LINK`, `client.printSpanLink` |
| `client.getThreads({ threadIds })` (items also carry `thread_id`), `getSessions({ threadIds })` | `client.getSessions({ sessionIds })` |
| `client.createCall(...)`, `client.finishCall(...)` | `client.createSpan(...)`, `client.finishSpan(...)` |
| `ev.logPrediction(inputs, output, { call })` | `ev.logPrediction(inputs, output, { span })` |
| `ScoreLogger.predictAndScoreCall`, `ScoreLogger.predictCall` | `ScoreLogger.predictAndScoreSpan`, `ScoreLogger.predictSpan` |
| `EvaluationLogger.call` | `EvaluationLogger.span` |
| conversation spans' `.call`, `.callId` (`turn.call`, `llm.callId`, ...) | `.span`, `.spanId` |
| `Dataset.fromCalls(spans)` | `Dataset.fromSpans(spans)` |
| `client.callUrl(span)` | `client.spanUrl(span)` |
| `weft.trace(fn, { callDisplayName })`, `traceSettings: { callDisplayName }` | `{ spanDisplayName }` |
| integration settings `opSettings` / `op_settings` (`updateIntegration`, `autopatchSettings`), `getIntegrationSettings(name).opSettings` (`traceSettings` wins when both are given) | `traceSettings`, `getIntegrationSettings(name).traceSettings` |
| wrapper options `opSettings` and `opName` (`wrapOpenAI(client, { opName })`, ...; the new names win) | `traceSettings` and `spanName` |
| type `IntegrationOpSettings` | `IntegrationTraceSettings` |
| `SavedView.filterOp(name)` | `SavedView.filterName(name)` |
| env `WEFT_MAX_CALLS_QUEUE_SIZE` (still wins over `WEFT_MAX_QUEUE_SIZE`) | `WEFT_MAX_SPANS_QUEUE_SIZE` |

A saved view's default pinned column is `name` (was `op_name`); pins saved
with the earlier column names (`op_name`, `exception`, `thread_id`) are
read as `name`, `error`, `session_id`.

Without a warning, for one release: `call_ref` in `feedback.add` extras (now
`span_ref`), `callId` in `evaluateModel`'s result and `versionIndex` in
`linkToRegistry`'s (now `id` and `version`), and write-ahead logs written by
earlier versions (replayed with the new field names).

### Span attribute keys

The SDK writes Weft's renamed span attribute keys (docs/weft/API.md, *Span
attributes*): `weft.parent_span.{id, trace_id}` (was `weft.parent_call.*`,
the TypeSafe integration's link to the running traced function),
`weft.linked_spans` with `{span_id, trace_id}` items (was
`weft.linked_calls` with `{call_id}`, on `logPrediction(..., { span })`
predictions) and `weft.eval.prediction_span_id` (was
`weft.eval.predict_and_score_call_id`, on spans recorded inside a
prediction). The server still reads the old keys, so spans sent by earlier
SDK versions keep their links. The Agents API span fields for the TypeSafe
link are `linked_parent_span_id` / `linked_parent_trace_id` (were
`parent_call_id` / `parent_call_trace_id`, still accepted in queries).
