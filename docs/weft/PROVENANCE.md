# Weft provenance

Weft started out as a server, UI and SDKs that match W&B Weave's features and
API. It is meant to be its own product: compatible with Weave where that helps
users, but with its own code, prompts and documentation. This page records how
that is checked, what the last check found and what was done about it. See
also [DESIGN.md §9](DESIGN.md#9-provenance).

Last run: 2026-11-03.

## Sources compared against

| Source | Version | Licence |
|---|---|---|
| Weave Python SDK and trace server (`weave/`), from github.com/wandb/weave | `master` at `db13674` (2026-11-02), and the installed wheel `weave==0.54.0` | Apache-2.0 |
| Weave TypeScript SDK (`sdks/node/src`), same checkout; plus the npm tarball `weave@0.16.10` (compiled `dist/`) | as above | Apache-2.0 |
| Weave web UI (`weave-js/src/components/PagePanelComponents/Home/Browse3`) | tag `v0.51.56` (July 2025), the last release that had the UI in the open repository | Apache-2.0 |
| Weave documentation, docs.coreweave.com/products/wandb/weave (all 351 pages in the site's page index, including the API references) | fetched 2026-11-03 | no licence to reuse is assumed |

Weave's current UI (Agents, Signals, Monitors) is not public, so UI copy was
compared against the v0.51.56 UI and against the documentation, which
describes the current UI's screens and labels.

## What was scanned

`sdk/python/src/weft/**`, `sdk/typescript/src/**`, `internal/weft/**`,
`web/weft-ui/src/**`, `plugins/weft-*/**`, `scripts/weft_*`,
`docs/weft/*.md` and the SDK READMEs: code, scorer and LLM-judge prompts,
signal and monitor presets, UI strings and docs. Built output (`dist/`,
`internal/weft/webui/static`) and `node_modules` are skipped.

## Method

[`scripts/weft_provenance_scan.py`](../../scripts/weft_provenance_scan.py)
(standard library only):

1. **Normalise.** Every file becomes a stream of word tokens: identifiers are
   split at camelCase and snake_case boundaries and lower-cased; punctuation
   and whitespace are dropped. Re-wrapping, re-quoting, re-indenting or
   switching between `wrap_openai` and `wrapOpenAI` does not hide a match.
2. **Exact runs.** Each 8-token shingle of the reference corpus is indexed.
   Consecutive matching shingles in a Weft file are merged into a run, and
   runs of 16 or more tokens are reported with the Weft `file:line`, the
   reference file and line, and the text. For prose a second pass used
   6-token shingles and a 9-token minimum, keeping runs that read as English.
3. **Near-verbatim windows** (`--fuzzy`). A window of 24 four-token n-grams is
   flagged when at least half of them occur in a single reference file
   (n-grams found in more than 25 reference files count as common phrasing
   and are ignored). This catches paraphrases with small edits, which the
   exact pass misses.
4. **Markers.** A grep for `port of`, `ported`, `copied`, `adapted from`,
   `based on weave` and similar.
5. **Review.** Every match was classified by hand as copied expression (then
   rewritten) or as an interface fact or common idiom (then recorded in the
   baseline, below).

Matches that are only API names, field names or wire formats needed for
compatibility (method names, parameter names, JSON keys, route paths, stored
enum values) are interface facts, not expression, and were left as they are.

## Findings and what was done

The first run reported 158 exact runs of 16+ tokens and 266 near-verbatim
windows. After review:

### Copied expression: rewritten

| Weft location | Source | Overlap | Action |
|---|---|---|---|
| `internal/weft/store/agents_signals.go`, `SignalPresets` | Weave docs, *View agent signals*, preset tables | 8 preset descriptions were close paraphrases (exact runs of 16, 15 and 12 tokens; the longest was the jailbreak preset's list of attack techniques); the preset names matched too | All 10 descriptions rewritten (they double as judge instructions, so their meaning is kept); display names changed to *Frustrated user*, *Jailbreak attempt*, *Not safe for work*, *Weak answer*, *Benign intent*, *Workplace safe* and sentence case for the rest. Preset IDs are stored data and are unchanged. `SignalsView.tsx` empty state updated to the new names. |
| `internal/weft/server/refbuckets.go` (`testBucket` comment) and `docs/weft/API.md`, *Test connection* | Weave docs, *Reference buckets* (BYOB), step 5 | quoted sentence, 18 tokens | Rewritten without the quote. |
| `sdk/python/src/weft/integrations/typesafe.py` | `weave/integrations/typesafe/typesafe_sdk.py` (Apache-2.0) | declared "a port of Weave 0.54's `weave/integrations/typesafe`"; same function decomposition and near-identical bodies (runs of 71, 52, 49 and 45 tokens) | **Rewritten** from the behaviour described in the module docstring and the tests: new structure (`_SystemOneSpan` with `begin`/`succeed`/`fail`, one wrapper factory), new helpers. Behaviour unchanged (unit and live tests pass). |
| `sdk/typescript/src/integrations/typesafe.ts` | `sdks/node/src/integrations/typesafe.ts` (Apache-2.0) | declared "a port of Weave's TypeSafe integration"; same open/fill/close/trace decomposition (runs of 46, 25, 21 tokens) | **Rewritten** the same way (`SystemOneSpan` class). |
| `sdk/python/src/weft/conversation.py` | `weave/conversation/types.py`, `conversation.py`, `agent_context.py` | docstrings of `_to_json_string`, `_parse_data_url`, `ToolCallPart`, `ToolCallResponsePart`, `BlobPart`, `Usage`, `LLM.output`, `LLM.think`; the messages "Exactly one of content, uri, or file_id must be provided", the `add_event` deprecation and the `agent_name_override` error; the bodies of `_to_json_string`, `_parse_data_url` and `_resolve_turn_timestamps` | Rewritten. |
| `sdk/typescript/src/conversation.ts` | `sdks/node/src/genai/llm.ts`, `weave/conversation/conversation.py` | `output`/`think` doc comments, the `attachMedia` error | Rewritten. |
| `sdk/python/src/weft/_saved_view.py`, `sdk/typescript/src/savedView.ts` | `weave/flow/saved_view.py` | 8 error messages (`Column "x" is not known`, `Operator x not supported` / `does not support a value` / `requires a value`, `Column index "x" out of bounds`, `Column "x" not found`, `Invalid date value`, `Unsupported operator`) | Rewritten. |
| `sdk/python/src/weft/_context.py` | `weave/trace/context/call_context.py` | `NoCurrentCallError` message | Rewritten. |
| `sdk/python/src/weft/_object.py`, `sdk/typescript/src/objects.ts` | `weave/dataset/dataset.py`, `weave/trace/table.py` | 3 messages (multiple-splits warning, empty `select`, rows type) | Rewritten. |
| `sdk/python/src/weft/local_scorers.py` | `weave/scorers/coherence_scorer.py` | "Cannot provide both `chat_history` and `context`" | Rewritten. |
| `sdk/python/src/weft/_custom_runtime.py` | `weave/trace/custom_runtime.py` | pydantic `Field` descriptions (35-token run; they appear in JSON schemas) | Rewritten. |
| `sdk/typescript/src/client.ts`, `RegisterCustomRuntimeOptions` | `sdks/node/src/weaveClient.ts` | doc comments ("Complete desired list; omitted IDs are removed from an existing runtime.") | Rewritten. |
| `sdk/python/src/weft/_client.py`, module-level `add_tags` / `remove_tags` / `remove_aliases` | `weave/trace/api.py` | one-line docstrings | Rewritten. |
| `sdk/python/src/weft/api_types.py` (was `trace_server_interface.py`), module docstring | Weave docs, *Threads* | usage example (22 tokens) | Replaced with a different example. |
| `sdk/python/src/weft/integrations/verdict.py`, `start_call` | `weave/integrations/verdict/verdict_sdk.py` | parent-context inheritance (41 tokens) | Rewritten. |

No scorer or LLM-judge prompt, monitor preset, guardrail text or UI string
(other than the signal names above) matched Weave's code, UI or docs.

### Interface facts and idioms: kept

What remains is recorded, per pair of Weft file and reference file, in
[`scripts/weft_provenance_baseline.json`](../../scripts/weft_provenance_baseline.json)
with a reason. In summary:

- **Wire format of the trace API** (shared with Weave's where that was
  kept): field lists of calls, objects,
  feedback, costs, threads, annotation queues, agent spans and stats in
  `docs/weft/API.md`, `internal/weft/store`, `internal/weft/server`,
  `web/weft-ui/src/api/*` and the SDK call types.
- **Public SDK API kept compatible with Weave**: class, function, parameter
  and field names and signatures in the conversation SDK (`conversation.py`,
  `conversation.ts`: about half of `conversation.py`'s lines fall in matched
  windows, almost all of them field declarations, signatures and
  keyword pass-through), `__init__` exports, saved views (including the
  stored operator values such as `(string): contains`), `urls.py`, custom
  runtimes, tags and aliases. The method bodies are Weft's own; copied
  docstrings and messages were rewritten (above).
- **Local scorers**: the thresholds and result formats of the local model
  scorers. Their default models are openly licensed models by other authors
  (`PleIAs/celadon`, `maximuspowers/bias-type-classifier`,
  `cross-encoder/nli-deberta-v3-xsmall`,
  `pszemraj/electra-small-discriminator-CoLA`,
  `vectara/hallucination_evaluation_model`,
  `cross-encoder/ms-marco-MiniLM-L6-v2`; licences in sdk/python/README.md).
- **Standard vocabularies**: OpenTelemetry GenAI semantic-convention
  attribute names (OTLP mapping and its test vectors), Presidio entity names,
  common e-mail and SSN regular expressions, provider API-key environment
  variable names, the TypeSafe request-id header and capture variable.
- **Common idioms**: OpenTelemetry SDK setup snippets, a Blob download link,
  `toLocaleString` date options, Ctrl/Cmd+Enter handling, import blocks,
  library callback signatures (DSPy, LlamaIndex, Verdict).

## Attribution decisions

- **No code is kept as an attributed port.** The only code that declared
  itself a port (the TypeSafe integrations) was rewritten, because it was
  short enough to rewrite from its behaviour description. The conversation
  SDK, saved views and local scorers are re-implementations of Weave's public
  API and behaviour; what they share with Weave's source is interface, not
  expression, after the rewrites above.
- Weave's repository has no NOTICE file, so there is no Weave notice to carry.
  If Weave code is ever ported again, keep its Apache-2.0 notice, mark the
  file as modified (Apache-2.0 §4(b)) and add an entry to `NOTICE`.
- Weave's documentation is not reused; Weft's docs are written from Weft's
  behaviour.
- Weave is named only where compatibility with its client is the subject:
  the optional adapter (`internal/weft/compat`, off by default),
  [COMPATIBILITY.md](COMPATIBILITY.md), this page, `NOTICE`,
  `scripts/weft_weave_compat_check.py`, `scripts/weft_provenance_scan.py` and
  its baseline. Everything else describes Weft's behaviour in Weft's terms
  (2026-11-03: comments, identifiers, UI copy, docs, SDK docs and examples
  were rewritten; Weft's own feedback types, attribute keys and field names
  replaced the ones it had shared with Weave, see COMPATIBILITY.md).

## Third-party code in the embedded UI

`make weft-ui` bundles the UI's npm dependencies into
`internal/weft/webui/static`, which is committed and embedded in the `weft`
binary. The minified bundle keeps no licence comments (the committed build has
no `@license` or `Copyright` comment in any asset), but the MIT, ISC and BSD
licences require the copyright notice and licence text in every copy. So:

- [`scripts/weft_ui_licenses.mjs`](../../scripts/weft_ui_licenses.mjs) writes
  `internal/weft/webui/static/THIRD_PARTY_LICENSES.txt` from the licence
  files in `web/weft-ui/node_modules`, for every non-dev package in
  `package-lock.json` except `@types/*` (a superset of what tree-shaking
  keeps). Packages that ship no licence file get their upstream text from
  the script (`victory-vendor`); vendored libraries' licences
  (`victory-vendor/lib-vendor/d3-*`) are included.
- `make weft-ui` runs it after `vite build` (which empties the output
  directory); `make weft-test` and CI (`.github/workflows/weft.yml`, *Web UI*
  job) check that the committed file is current.
- The file is embedded with the UI and served at `/THIRD_PARTY_LICENSES.txt`.
  `NOTICE` points to it.

Current content: 133 packages: 118 MIT, 12 ISC (d3 modules, internmap,
@ungap/structured-clone), 2 BSD-3-Clause (d3-ease, react-transition-group),
1 MIT AND ISC (victory-vendor). The TypeScript SDK has no runtime
dependencies and the UI does not bundle it.

## Re-running the scan

```bash
# Download the reference corpus: Weave repo (weave/, sdks/node/src), the
# last open Weave UI (v0.51.56) and the Weave docs pages.
python3 scripts/weft_provenance_scan.py fetch --cache ~/.cache/weft-prov

# Compare. Exits 1 with --fail-on-new if a file pair is new or grew.
python3 scripts/weft_provenance_scan.py scan --cache ~/.cache/weft-prov --fuzzy --fail-on-new

# Optional extra reference trees, e.g. an installed wheel or `npm pack weave`:
python3 scripts/weft_provenance_scan.py scan --cache ~/.cache/weft-prov --fuzzy \
    --source .venv/lib/python3.13/site-packages/weave --source /tmp/weave-npm/package/dist

# Lower thresholds for prose (short copied sentences):
python3 scripts/weft_provenance_scan.py scan --cache ~/.cache/weft-prov -k 6 --min-tokens 9 --json prose.json
```

`--weave-ref` pins the Weave commit for `fetch` (default `master`). The
baseline holds, for each reviewed pair of Weft file and reference file, the
matched token count. A pair is reported again when it is new or its count grows
by more than `--slack` tokens (default 24). Review every reported match; rewrite
anything that is expression, and record the rest with
`scan ... --update-baseline --reason "..."`. Results depend on the corpus (the
n-gram commonness cut-off sees fewer duplicates without the wheel), so the
baseline covers both the fetched corpus alone and the corpus plus the
installed wheel and npm tarball.

## Open items

- The `weft` and `agentshield` binaries also contain Go modules under MIT,
  BSD and Apache licences; their notices for binary releases are not covered
  here.
