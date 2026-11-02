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
make weft-ui      # build the React UI into internal/weft/webui/static (needs Node 18+)
make weft         # build bin/weft
./bin/weft        # http://127.0.0.1:6345, data in ~/.weft/weft.db
```

| Flag | Env | Default |
|---|---|---|
| `--addr` | `WEFT_ADDR` | `127.0.0.1:6345` |
| `--db` | `WEFT_DB` | `~/.weft/weft.db` |
| `--api-keys` | `WEFT_API_KEYS` | empty (auth disabled) |
| `--cors-origins` | `WEFT_CORS_ORIGINS` | empty |
| `--no-ui` | | serve the UI |
| `--log-level` | `WEFT_LOG_LEVEL` | `info` |

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

**TypeScript SDK** (`sdk/typescript`): `init`, `op`, `wrapOpenAI`, `Dataset`,
`Evaluation`.

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
| Feedback | Emoji reactions, notes, schema-driven human annotations, scorer results. Feedback is queryable from trace filters. |
| Monitors | Online evaluation: active monitors score sampled calls of chosen ops as they finish and attach the scores as feedback. |
| Playground | Chat with OpenAI, Anthropic, Gemini, Groq, Mistral, DeepSeek, xAI, OpenRouter, Ollama or a custom OpenAI-compatible provider. Streaming, side-by-side models, load from a call or saved prompt, save as prompt. |

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
| `docs/weft/API.md` | API contract |

## Security notes

- Without `--api-keys` the server is open to anyone who can reach it. It
  binds to loopback by default and logs a warning on other addresses.
- All API keys are equal: any key holder can read every project and use the
  stored provider secrets through the playground and LLM judges. A `Provider`
  object sends the secret named in `api_key_name` to its `base_url`, so only
  give keys to people you would trust with those secrets.
- Secrets are stored unencrypted in the SQLite file. Protect the file like a
  credential.
- Uploaded files are served with `Content-Security-Policy: sandbox` and
  `nosniff` so they cannot run script in the UI's origin.
