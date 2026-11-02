# weft — Python SDK

Python client for **Weft**, an OpenTelemetry-native LLM tracing and evaluation
server. The API follows the familiar ops / calls / objects / datasets / models /
evaluations / scorers / feedback model, so code written for that style of SDK
ports with little more than an import change.

- Runtime dependency: `pydantic>=2` only (HTTP uses the standard library).
- Python 3.9+.
- Optional extras: `otel` (OpenTelemetry exporter), `images` (Pillow).

```bash
pip install -e sdk/python            # from this repository
pip install -e 'sdk/python[otel]'    # + OpenTelemetry export
```

## Quickstart

```python
import weft

weft.init("my-project")              # or "entity/project"

@weft.op
def retrieve(query: str) -> list[str]:
    return ["doc one", "doc two"]

@weft.op
def answer(question: str) -> str:
    docs = retrieve(question)        # nested call: child of answer()
    return f"{len(docs)} docs for {question!r}"

answer("what is weft?")
```

Every `@weft.op` invocation is recorded as a *call* (inputs, output, exception,
timing, parent/child structure). Calls are batched and sent by a background
thread; if the server is unreachable the SDK logs a warning and your code keeps
running. Pending data is flushed at exit (`weft.flush()` / `weft.finish()` do it
explicitly).

### Configuration

| Setting | Argument | Environment | Default |
|---|---|---|---|
| Project | `weft.init(project)` | `WEFT_PROJECT` | required |
| Server | `server_url=` | `WEFT_SERVER_URL` | `http://localhost:6345` |
| API key | `api_key=` | `WEFT_API_KEY` | none |
| Disable tracing | | `WEFT_DISABLED=true` | off |
| Evaluation concurrency | | `WEFT_PARALLELISM` | 10 |
| Exit flush timeout (s) | | `WEFT_FLUSH_TIMEOUT` | 10 |

A bare project name `"p"` is stored as `"default/p"`.

## Tracing

```python
@weft.op                                   # sync, async, generators, async generators
async def stream_tokens(prompt: str):
    for tok in prompt.split():
        yield tok                          # yielded items become the call output

@weft.op(
    name="search",                         # op name (default: function qualname)
    call_display_name=lambda call: f"search: {call.inputs['q']}",
    postprocess_inputs=lambda d: {k: v for k, v in d.items() if k != "api_key"},
    postprocess_output=lambda out: out[:10],
    tracing_sample_rate=0.1,               # applies to root calls; children follow the root
)
def search(q: str, api_key: str) -> list: ...

result, call = search.call("weft", api_key="...")   # (result, Call); never raises
call.id, call.trace_id, call.exception

weft.get_current_call()                    # innermost call in this context

with weft.attributes({"env": "prod", "user_id": "u1"}):
    search("x", api_key="...")             # attributes stored on every call in the block

with weft.thread("conversation-42"):       # thread_id; top-level calls are turns
    answer("hi")
    answer("and then?")
```

- **Exceptions** are recorded (`"ValueError: message"` plus the traceback) and
  re-raised.
- **Methods**: `self` is recorded as a ref when the object has been published,
  as its field values for an unpublished `weft.Object`, and as `repr()` otherwise.
- **Concurrency**: asyncio tasks inherit the current call. Threads do not
  inherit it automatically; use `weft.ThreadPoolExecutor`, `weft.Thread` or
  `contextvars.copy_context().run(...)`.
- **LLM usage**: report the call's *own* usage with
  `weft.add_usage(weft.get_current_call(), model, prompt_tokens, completion_tokens)`.
  The server adds up descendants, so a root call's `summary.usage` covers the
  whole trace.
- **Values** are converted to JSON: pydantic models and dataclasses become
  dicts. Published objects, ops and dataset rows become ref strings. `bytes` and
  PIL images are uploaded via `/file/create` and stored as
  `{"_type": "Image", "file_digest", "mime_type"}`.

## Integrations (auto-patched by `weft.init`)

When the `openai` or `anthropic` packages are installed, `weft.init()` patches
them (pass `autopatch=False` to opt out):

| Library | Methods | Op name |
|---|---|---|
| openai | `chat.completions.create` (sync/async, `stream=True`) | `openai.chat.completions.create` |
| openai | `responses.create` (sync/async, streaming) | `openai.responses.create` |
| anthropic | `messages.create` (sync/async, `stream=True`) | `anthropic.messages.create` |
| anthropic | `messages.stream()` context manager | `anthropic.messages.stream` |

Each LLM request becomes a call with the request kwargs as inputs, the response
as output (streams are accumulated into a single response), and
`summary.usage = {model: {requests, prompt_tokens, completion_tokens, total_tokens}}`.
For streamed OpenAI chat completions the SDK adds
`stream_options={"include_usage": True}` unless you set `stream_options`
yourself.

To trace a single client instance of any OpenAI-shaped SDK (Groq, Together,
vLLM, ...):

```python
from weft.integrations import patch_openai_compatible
patch_openai_compatible(groq_client, name="groq")   # -> groq.chat.completions.create
```

## Objects, refs and prompts

```python
class RetrieverConfig(weft.Object):        # pydantic model with name/description
    top_k: int = 5

ref = weft.publish(RetrieverConfig(name="retriever", top_k=3))
ref.uri()       # weft:///default/my-project/object/retriever:<digest>
ref.get()       # -> RetrieverConfig(top_k=3)

weft.ref("retriever").get()          # latest
weft.ref("retriever:v0").get()       # by version index
weft.ref("retriever:latest").get()
weft.ref(ref.uri()).get()

prompt = weft.StringPrompt("Answer briefly: {question}")
prompt.format(question="2+2?")
chat = weft.MessagesPrompt([{"role": "system", "content": "You are {persona}"}])
weft.publish(chat, name="chat-prompt")
```

Publishing is content-addressed: publishing an identical object again gives
the same digest, and the server keeps one version. Nested objects are published
first and stored as refs. Objects whose class is not defined locally load as
`weft.WeftObject`, a dict that also supports attribute access.

## Datasets and models

```python
ds = weft.Dataset(name="qa", rows=[{"question": "2+2", "expected": "4"}])
weft.publish(ds)                     # rows uploaded as a table
for row in weft.ref("qa").get().rows:
    ...
ds2 = ds.add_rows([{"question": "3+3", "expected": "6"}])   # new version
weft.Dataset.from_calls(client.get_calls(filter={"op_names": ["answer"]}))

class MyModel(weft.Model):
    system_prompt: str

    @weft.op
    def predict(self, question: str) -> str:
        ...
```

`weft.LLMStructuredCompletionModel` is a model the server can run too
(`/evaluate_model`). When run locally its `predict` calls the server's
`/completions/create`, so provider keys stay on the server:

```python
model = weft.LLMStructuredCompletionModel(
    name="qa-model", llm_model_id="gpt-4o-mini",
    default_params={"messages_template": [{"role": "user", "content": "{question}"}],
                    "temperature": 0, "response_format": "text"})
```

## Evaluations

```python
from weft import scorers

@weft.op
def exact(expected: str, output: str) -> bool:      # args matched to dataset columns
    return expected == output

class Brevity(weft.Scorer):
    @weft.op
    def score(self, *, output, reference) -> dict:
        return {"shorter": len(output) <= len(reference)}

evaluation = weft.Evaluation(
    name="qa-eval",
    dataset=ds,
    scorers=[
        exact,
        Brevity(column_map={"reference": "expected"}),     # score arg -> column
        scorers.ExactMatchScorer(name="em", target_column="expected"),
        scorers.ContainsScorer(target_column="expected", case_insensitive=True),
        scorers.RegexScorer(pattern=r"^\d+$"),
        scorers.JSONValidScorer(),
        scorers.LevenshteinScorer(target_column="expected"),
        scorers.LLMAsAJudgeScorer(model="gpt-4o-mini",
                                  scoring_prompt="Is {output} a correct answer to {question}? Reply JSON {{\"correct\": bool}}"),
    ],
    trials=1,
    preprocess_model_input=None,
)
summary = asyncio.run(evaluation.evaluate(MyModel(name="m", system_prompt="...")))
# also accepts a plain @weft.op function as the model
```

Rows run concurrently (`WEFT_PARALLELISM`, default 10). The calls recorded:

```
Evaluation.evaluate            inputs {self: <evaluation ref>, model: <model ref>}; output = summary
├── Evaluation.predict_and_score   inputs {self, model, example: <dataset row ref>}
│   ├── MyModel.predict            output = model output
│   └── scorer calls               (exact, Brevity.score, ExactMatchScorer.score, ...)
└── Evaluation.summarize
```

Summary shapes: booleans → `{"true_count", "true_fraction"}`, numbers →
`{"mean"}`, dicts are summarised key by key, plus `model_latency: {"mean"}`.
Built-in scorers serialise in the server's format, so the same `Evaluation`
can run server side with `/evaluate_model` or be attached to a Monitor.

### EvaluationLogger

When you run predictions yourself:

```python
ev = weft.EvaluationLogger(model="my-model", dataset="my-dataset")
pred = ev.log_prediction(inputs={"question": "2+2"}, output="4")
pred.log_score("correct", True)
pred.finish()
ev.log_summary()            # same call structure as Evaluation
```

## Querying calls and feedback

```python
client = weft.init("my-project")
for call in client.get_calls(filter={"op_names": ["answer"], "trace_roots_only": True},
                             query={"$gt": [{"$getField": "summary.weave.latency_ms"}, {"$literal": 1000}]},
                             sort_by=[{"field": "started_at", "direction": "desc"}],
                             limit=50):
    print(call.id, call.inputs, call.output, call.status)

call = client.get_call(call_id, include_costs=True)
call.feedback.add_reaction("👍")
call.feedback.add_note("looks right")
call.feedback.add("my.feedback", {"score": 3})
list(call.feedback)
call.set_display_name("golden example")
call.delete()

client.get_evaluations()                         # Evaluation objects
client.add_cost("my-model", prompt_token_cost=1e-6, completion_token_cost=2e-6)
client.server_info()
```

## OpenTelemetry

Weft ingests OTLP/HTTP at `<server>/otel/v1/traces`. Spans from any
OpenTelemetry, OpenInference or OpenLLMetry instrumentation become calls
(GenAI and OpenInference attributes are mapped to inputs, outputs and usage).

```python
import weft.otel
provider = weft.otel.configure(project="my-project")   # server_url=, api_key= optional

# e.g. OpenInference:
# from openinference.instrumentation.openai import OpenAIInstrumentor
# OpenAIInstrumentor().instrument(tracer_provider=provider)

from opentelemetry import trace
tracer = trace.get_tracer("my-app")
with tracer.start_as_current_span("pipeline") as span:
    weft.otel.set_inputs(span, {"question": "hi"})     # weft.inputs attribute
    weft.otel.set_output(span, {"answer": "hello"})    # weft.output attribute
```

`configure` installs a `TracerProvider` with an `OTLPSpanExporter` that sends
the `project_id` header (and `Authorization: Bearer <key>` when an API key is
set). Without the SDK you can configure any exporter the same way:

```bash
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://localhost:6345/otel/v1/traces
OTEL_EXPORTER_OTLP_TRACES_HEADERS=project_id=my-entity/my-project
```

## Tests

```bash
pip install -e 'sdk/python[otel,test]'
pytest sdk/python/tests                                   # unit tests (in-process fake server)
WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 pytest sdk/python/tests/test_integration_server.py
```

Examples live in `examples/weft/python/`.
