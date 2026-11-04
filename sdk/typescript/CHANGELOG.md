# Changelog

## Unreleased: Weft's own trace and object API

The SDK now speaks only Weft's own API (`/api/v1`, `docs/weft/API.md`) and
uses its names ([DESIGN.md](../../docs/weft/DESIGN.md) §10). It needs a
server with that API (this repository's server, which ships with the SDK);
the older route shapes are no longer used.

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
- `client.evaluateModel(...)` resolves to `{ id, status, summary }`;
  `client.compareEvaluations(runIds)`; `client.leaderboard(name, version)`;
  `linkToRegistry` resolves to `{ version, ... }`; `client.addCost` resolves
  to `[{ id, llm_id }]`; `client.getThreads` items carry `session_id`.

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

These earlier names still work; each prints one console warning the first
time it is used:

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
| `SavedView.getCalls()`, `SavedView.toCallsQuery()` | `SavedView.spans()`, `SavedView.toSpansQuery()` |
| `new weft.Monitor({ opNames })`, `monitor.opNames` | `new weft.Monitor({ names })`, `monitor.names` |
| `new weft.AnnotationSpec({ opScope })`, `spec.opScope` | `new weft.AnnotationSpec({ names })`, `spec.names` |
| `weft.withThread(threadId, fn)` (and the `ThreadContext` type) | `weft.withSession(sessionId, fn)` (`SessionContext`) |
| `client.updateCall(span, attrs, { threadId })` | `client.updateSpan(span, attrs, { sessionId })` |
| setting `printCallLink`, env `WEFT_PRINT_CALL_LINK`, `client.printCallLink` | `printSpanLink`, `WEFT_PRINT_SPAN_LINK`, `client.printSpanLink` |

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
`weft.linked_calls` with `{call_id}`, on `logPrediction({ call })`
predictions) and `weft.eval.prediction_span_id` (was
`weft.eval.predict_and_score_call_id`, on spans recorded inside a
prediction). The server still reads the old keys, so spans sent by earlier
SDK versions keep their links. The Agents API span fields for the TypeSafe
link are `linked_parent_span_id` / `linked_parent_trace_id` (were
`parent_call_id` / `parent_call_trace_id`, still accepted in queries).
