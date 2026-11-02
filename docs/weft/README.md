# Weft

Weft is an OpenTelemetry-native tracing and evaluation platform for LLM
applications and agents. Its feature set follows W&B Weave: nested traces of
every LLM and tool call, versioned datasets, models, prompts and scorers,
evaluations with side-by-side comparison, leaderboards, human and automated
feedback, online monitors, cost tracking and an LLM playground.

It ships as one Go binary (`cmd/weft`) that holds the API, the OTLP receiver
and the web UI, backed by a single SQLite file. Python and TypeScript SDKs
live in `sdk/`.

```
 your app ──@weft.op / wrapOpenAI──▶ /call/upsert_batch ┐
 any OTel / OpenInference / OpenLLMetry ──▶ /otel/v1/traces ┤──▶ weft (Go) ──▶ SQLite
 browser ──────────────────────────▶ web UI + JSON API ┘        │
                                                                └─▶ LLM providers (playground, judges, monitors)
```

## Run it

```bash
make weft-ui      # rebuild the React UI into internal/weft/webui/static (Node 20.19+; the built UI is committed)
make weft         # build bin/weft
./bin/weft        # http://127.0.0.1:6345, data in ~/.weft/weft.db
```

| Flag | Env | Default |
|---|---|---|
| `--addr` | `WEFT_ADDR` | `127.0.0.1:6345` |
| `--db` | `WEFT_DB` | `~/.weft/weft.db` |
| `--api-keys` | `WEFT_API_KEYS` | empty (auth disabled); comma-separated, each `key` or `user=key` |
| `--cors-origins` | `WEFT_CORS_ORIGINS` | empty |
| `--no-ui` | | serve the UI |
| `--log-level` | `WEFT_LOG_LEVEL` | `info` (`debug` logs every request) |
| | `WEFT_SECRET_KEY` | key for encrypting stored provider secrets; default is a random key in `<db>.key` |

Provider keys for the playground, LLM judges and monitors come from the
environment (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`, …) or
from **Settings → Secrets** in the UI. The `mock/echo` and `mock/json` models
work offline.

For UI development: `./bin/weft` in one shell, `cd web/weft-ui && npm run dev`
in another (Vite proxies API paths to `WEFT_SERVER_URL`, default `:6345`).

## Send data

**Python SDK** (`sdk/python`):

```python
import weft

weft.init("my-team/my-app")

@weft.op
def answer(question: str) -> str:
    return client.chat.completions.create(model="gpt-4o-mini",
        messages=[{"role": "user", "content": question}]).choices[0].message.content
```

OpenAI and Anthropic clients are patched automatically, so their calls appear
as child calls with token usage and cost.

**Any OpenTelemetry exporter.** Point an OTLP/HTTP trace exporter at
`http://<host>:6345/otel/v1/traces` and send the project in a header:

```bash
export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:6345/otel/v1/traces
export OTEL_EXPORTER_OTLP_TRACES_HEADERS="project_id=my-team/my-app,authorization=Bearer <key>"
```

Spans that follow the GenAI semantic conventions, OpenInference or
OpenLLMetry become calls with chat-formatted inputs and outputs, token usage
and costs. The full mapping is in [API.md](API.md#opentelemetry-ingest).

Datasets, models, scorers and evaluations follow the same pattern:

```python
dataset = weft.Dataset(name="capitals", rows=[{"question": "France?", "expected": "Paris"}])

@weft.op
def exact(expected: str, output: str) -> bool:
    return expected == output

evaluation = weft.Evaluation(dataset=dataset, scorers=[exact, weft.scorers.LevenshteinScorer()])
await evaluation.evaluate(my_model)        # recorded as an evaluation run, comparable in the UI
```

`weft.otel.configure(project=...)` installs an OpenTelemetry tracer provider
that exports to Weft, so OpenInference or OpenLLMetry instrumentation needs no
further setup. See [sdk/python/README.md](../../sdk/python/README.md).

**TypeScript SDK** (`sdk/typescript`, package `weft-sdk`):

```ts
import * as weft from 'weft-sdk'
await weft.init('my-team/my-app')
const answer = weft.op(async function answer(question: string) { /* ... */ })
const openai = weft.wrapOpenAI(new OpenAI())
```

It also provides `Dataset`, `Evaluation`, `EvaluationLogger`, built-in scorers,
`publish`/`ref`, `wrapAnthropic`, `attributes` and `withThread`. See
[sdk/typescript/README.md](../../sdk/typescript/README.md).

Both SDKs also support guardrails (`call.apply_scorer` / `call.applyScorer`
score a finished call and attach the result as feedback), loading published
ops back as runnable functions (this executes the stored code), object classes
for annotation specs, monitors, leaderboards and saved views, and redaction of
credential-like keys (`api_key`, `password`, `token`, …) before data leaves
the process.

**The official `weave` Python client** works unchanged. Point it at Weft:

```bash
export WANDB_BASE_URL=http://127.0.0.1:6345      # W&B login and project setup (GraphQL shim)
export WF_TRACE_SERVER_URL=http://127.0.0.1:6345 # traces, objects, feedback
export WANDB_API_KEY=<a Weft API key, or any 40 characters when auth is off>
```

```python
import weave
weave.init("my-team/my-app")   # ops, datasets, models, evaluations, feedback work as usual
```

Weft answers the GraphQL calls weave makes at login, accepts its `v2`
call endpoints, uses Weave's digest scheme so client-computed refs resolve,
and returns `weave:///` refs to weave clients. Weave's client rolls child
usage into parent summaries; Weft records those as cumulative so tokens and
cost are not counted twice. `scripts/weft_weave_compat_check.py` drives the
official client end to end and runs in CI (weave 0.54.0).

**Demo data:** `python3 scripts/weft_seed_demo.py` fills `demo/support-bot` with
agent traces, threads, OTLP spans, datasets, evaluations, a leaderboard, a
monitor and feedback, using offline mock models.

## Feature map

| Area | What Weft does |
|---|---|
| Traces | Calls with inputs, output, exception, attributes, summary. Trace tree and timeline views, filters (op, status, free text, any field via the query builder), sorting, saved views, charts (volume, errors, latency percentiles, tokens, cost), export, bulk delete, compare calls. |
| Usage and cost | Token usage rolls up from every descendant to the trace root, including OTLP spans that arrive out of order. Built-in prices for common models; per-project price overrides. |
| Threads | Calls sharing a `thread_id` group into conversations with turn latency and token totals. |
| Objects | Content-addressed, versioned objects (datasets, models, prompts, scorers, ops, anything JSON). Aliases, version diffs, refs (`weft:///entity/project/object/name:digest`). |
| Datasets | Row tables with paging and editing; create from CSV/JSON or from selected calls. |
| Evaluations | SDK-run evaluations (`weft.Evaluation`, `EvaluationLogger`) and server-run evaluations of prompt-based models with built-in scorers and LLM judges. Comparison view with metric deltas and per-example diffs. |
| Leaderboards | Columns of evaluation × scorer × metric, computed from the latest run per model. |
| Feedback | Emoji reactions, notes, schema-driven human annotations, scorer results. Feedback is queryable from trace filters. Any server-runnable scorer can be run on a call from its Scores tab (`/calls/score`). |
| Monitors | Online evaluation: active monitors score sampled calls of chosen ops as they finish and attach the scores as feedback. |
| Playground | Chat with OpenAI, Anthropic, Gemini, Groq, Mistral, DeepSeek, xAI, OpenRouter, Ollama or a custom OpenAI-compatible provider. Streaming, side-by-side models, `{variable}` placeholders with inputs, load from a call or saved prompt, save as prompt or as an evaluable model. |

## Layout

| Path | Contents |
|---|---|
| `cmd/weft` | server entry point |
| `internal/weft/store` | SQLite schema, call roll-up, query language, objects, tables, feedback, costs |
| `internal/weft/otlp` | OTLP decoding and span → call mapping |
| `internal/weft/llm` | provider routing, Anthropic translation, streaming, mock models |
| `internal/weft/evals` | scorers, server-side evaluation, comparison, leaderboards, monitors |
| `internal/weft/server` | HTTP API, auth, UI serving |
| `web/weft-ui` | React UI |
| `sdk/python`, `sdk/typescript` | client SDKs |
| `examples/weft` | runnable SDK examples |
| `scripts/weft_seed_demo.py` | demo data seeding |
| `docs/weft/API.md` | API contract |

## Testing

```bash
make weft-test                                     # Go, UI type check, Python SDK unit tests
cd web/weft-ui && npm test                         # UI unit tests
WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 \
  python -m pytest sdk/python                      # SDK integration tests against a running server
cd sdk/typescript && WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 npx vitest run
```

CI runs all of these in `.github/workflows/weft.yml` and fails if the
committed UI build is stale.

## Security notes

- Without `--api-keys` the server is open to anyone who can reach it. It
  binds to loopback by default and logs a warning on other addresses.
- All API keys are equal: any key holder can read every project and use the
  stored provider secrets through the playground and LLM judges. A `Provider`
  object sends the secret named in `api_key_name` to its `base_url`, so only
  give keys to people you would trust with those secrets.
- Provider secrets are encrypted at rest with AES-256-GCM. The key comes
  from `WEFT_SECRET_KEY` or a random key file created next to the database
  (`<db>.key`, mode 0600). Keep the key file off backups that hold the
  database, or set `WEFT_SECRET_KEY` from a secret manager. Plaintext secrets
  from older versions are re-encrypted at startup.
- Uploaded files are served with `Content-Security-Policy: sandbox` and
  `nosniff` so they cannot run script in the UI's origin.
