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

Without a warning, for one release: `call_ref` in `feedback.add` extras (now
`span_ref`), `callId` in `evaluateModel`'s result and `versionIndex` in
`linkToRegistry`'s (now `id` and `version`), and write-ahead logs written by
earlier versions (replayed with the new field names).
