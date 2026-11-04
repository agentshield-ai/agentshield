# Changelog

## Unreleased: Weft's own trace and object API

The SDK now speaks only Weft's own API (`/api/v1`, `docs/weft/API.md`) and
uses its names ([DESIGN.md](../../docs/weft/DESIGN.md) §10). It needs a
server with that API (this repository's server, which ships with the SDK);
the older route shapes are no longer used. The server's compatibility
adapter still serves the official weave client.

### New names

- `@weft.trace` traces a function; every call of it is a **span**.
- `weft.save(obj, name)` saves a version and returns its ref; `weft.get(ref)`
  reads one (`ref` is a `Ref`, a `weft:///` URI, `"name"`, `"name:v3"`,
  `"name:latest"` or `"name:<alias>"`). `obj.save(name)` on `weft.Object`.
- `client.spans(...)` (a lazy `SpansIter`) and `client.span(id)` (None when
  the span does not exist) query spans; `client.delete_spans(ids)`,
  `client.evaluation_runs()`.
- `weft.Span` with `name`, `base_name`, `error`, `session_id`, `turn_id`;
  `weft.SpanFeedback`; `weft.get_current_span()`,
  `weft.require_current_span()`, `weft.NoCurrentSpanError`.
- Refs: `weft.FunctionRef` (kind `function`) and `weft.SpanRef` (kind `span`).
  Refs with the earlier kinds `op` and `call` are still read.
- Span filters use `names`, `ids`, `session_ids` (the earlier `op_names`,
  `call_ids`, `thread_ids` are translated).
- `Dataset.from_spans(...)`, `SavedView.spans()` / `to_spans_query()`.
- `ApplyScorerResult.score_span`; feedback takes `span_ref`.
- `client.evaluate_model(...)` returns `{"id", "status", "summary"}`;
  `client.compare_evaluations(run_ids)`; `client.leaderboard(name, version)`;
  `client.link_to_registry(...)` returns `version`; `client.add_cost(...)`
  returns `[{"id", "llm_id"}]`; `client.threads_query(...)` items carry
  `session_id` and `span_count`.

### Platform objects and UI links

Weft's other APIs use its names too ([DESIGN.md](../../docs/weft/DESIGN.md)
§10.5, *Other APIs*), and so do the objects the SDK saves:

- `weft.Monitor` and `weft.AnnotationSpec` save `names` (the span names they
  apply to; earlier `op_names` / `op_scope`, deprecated below). Versions
  saved with the earlier names load with `names` filled in (the server also
  gives the latest version of each a new version with Weft's names, once).
- `weft.SavedView` stores its span filter with Weft's names (`names`, `ids`,
  `session_ids`); `set_filter()` takes the earlier ones (`op_names`,
  `call_ids`, `thread_ids`) and stores them under Weft's, and views saved
  with them still work.
- The UI links the SDK prints and `span.ui_url` point at
  `<server>/<entity>/<project>/spans/<id>` (was `/calls/<id>`, which the UI
  still redirects).

### Digests

Objects, table rows, tables and files use Weft's digest: the lowercase hex
SHA-256 of canonical JSON (sorted keys, `,`/`:` separators, UTF-8, only `"`,
`\` and control characters escaped), of the JSON list of row digests for a
table, of the bytes for a file. The SDK computes them locally (traced
functions are named by their ref before the server answers);
`tests/test_digest_crosscheck.py` checks them against the server.

### Local scorers

The local model scorers default to openly licensed models: `PleIAs/celadon`
(toxicity, Apache-2.0), `maximuspowers/bias-type-classifier` (bias, MIT),
`cross-encoder/nli-deberta-v3-xsmall` (coherence, Apache-2.0),
`pszemraj/electra-small-discriminator-CoLA` (fluency, Apache-2.0),
`vectara/hallucination_evaluation_model` (HHEM-2.1-Open, Apache-2.0) and
`cross-encoder/ms-marco-MiniLM-L6-v2` (context relevance, Apache-2.0). The
result format is unchanged; `model_name_or_path` still selects another model.
New settings: `BiasScorer.label_map`, `CoherenceScorer.incoherent_labels`,
`FluencyScorer.fluent_labels`.

### Deprecated (removed in the next release)

These earlier names still work and emit a `DeprecationWarning`:

| Earlier name | Use |
|---|---|
| `@weft.op` | `@weft.trace` |
| `weft.publish(obj, name)`, `client.publish(...)`, `Object.publish(name)` | `weft.save(obj, name)`, `client.save(...)`, `Object.save(name)` |
| `weft.ref(...).get()`, `Ref.get()` | `weft.get(ref)` |
| `client.get_calls(...)` | `client.spans(...)` |
| `client.get_call(id)` | `client.span(id)` |
| `client.delete_calls(ids)` | `client.delete_spans(ids)` |
| `client.get_evaluation_calls()` | `client.evaluation_runs()` |
| `weft.Call`, `weft.CallFeedback`, `weft.CallsIter` | `weft.Span`, `weft.SpanFeedback`, `weft.SpansIter` |
| `weft.CallRef`, `weft.OpRef` | `weft.SpanRef`, `weft.FunctionRef` |
| `weft.get_current_call()`, `weft.require_current_call()`, `weft.NoCurrentCallError` | `weft.get_current_span()`, `weft.require_current_span()`, `weft.NoCurrentSpanError` |
| `span.op_name`, `span.op_base_name`, `span.exception`, `span.thread_id` | `span.name`, `span.base_name`, `span.error`, `span.session_id` |
| `ApplyScorerResult.score_call` | `ApplyScorerResult.score_span` |
| `SavedView.get_calls()`, `SavedView.to_calls_query()` | `SavedView.spans()`, `SavedView.to_spans_query()` |
| `weft.Monitor(op_names=...)`, `monitor.op_names` | `weft.Monitor(names=...)`, `monitor.names` |
| `weft.AnnotationSpec(op_scope=...)`, `spec.op_scope` | `weft.AnnotationSpec(names=...)`, `spec.names` |

Without a warning, for one release: `call_ref=` on `feedback.add` (now
`span_ref=`), `"call_id"` in `evaluate_model`'s result and `"version_index"`
in `link_to_registry`'s (now `"id"` and `"version"`), and write-ahead logs
written by earlier versions (replayed with the new field names).
