# weft — Python SDK

Python client for **Weft**, an OpenTelemetry-native LLM tracing and evaluation
server. It speaks Weft's own API (`/api/v1`, [API.md](../../docs/weft/API.md)):
traced functions record **spans**, versioned values are **objects** (datasets,
models, prompts, scorers, functions), and evaluations, scorers and feedback
work on both. Earlier SDK names still work for one release; see
[CHANGELOG.md](CHANGELOG.md).

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

@weft.trace
def retrieve(query: str) -> list[str]:
    return ["doc one", "doc two"]

@weft.trace
def answer(question: str) -> str:
    docs = retrieve(question)        # nested span: child of answer()
    return f"{len(docs)} docs for {question!r}"

answer("what is weft?")

ref = weft.save(weft.StringPrompt("Answer {question} briefly."), "answer-prompt")
prompt = weft.get(ref)               # or weft.get("answer-prompt"), "answer-prompt:v0"
```

Every call of a `@weft.trace` function is recorded as a *span* (name, inputs,
output, error, timing, parent/child structure; the function itself is saved as
a versioned `function` object and names its spans). Spans are batched and sent
by a background thread (`POST /api/v1/spans/batch`); if the server is
unreachable the SDK logs a warning and your code keeps running. Pending data is
flushed at exit (`weft.flush()` / `weft.finish()` do it explicitly).

### Configuration

| Setting | Argument | Environment | Default |
|---|---|---|---|
| Project | `weft.init(project)` | `WEFT_PROJECT` | required |
| Server | `server_url=` | `WEFT_SERVER_URL` | `http://localhost:6345` |
| API key | `api_key=` | `WEFT_API_KEY` | none |
| Disable tracing | | `WEFT_DISABLED=true` | off |
| Evaluation concurrency | | `WEFT_PARALLELISM` | 10 |
| Exit flush timeout (s) | | `WEFT_FLUSH_TIMEOUT` | 10 |
| Redaction | `settings={"redact_keys": [...], "redact_pii": True}` | `WEFT_REDACT_KEYS=0` disables, `WEFT_REDACT_PII=1` | credential keys |
| PII entities and engine | `settings={"redact_pii_fields": [...], "redact_pii_exclude_fields": [...], "redact_pii_engine": "auto"}` | `WEFT_REDACT_PII_FIELDS`, `WEFT_REDACT_PII_EXCLUDE_FIELDS`, `WEFT_REDACT_PII_ENGINE` | `DEFAULT_PII_ENTITIES`; Presidio if installed |
| Integrations | `autopatch=False`, `autopatch_settings={...}` | `WEFT_AUTOPATCH=0`, `WEFT_DISABLED_INTEGRATIONS=a,b`, `WEFT_AUTOPATCH_<NAME>=0` | all on |
| Attributes on every call | `settings={"global_attributes": {...}}` | | none |
| Print a link per root call | `settings={"print_call_link": False}` | `WEFT_PRINT_CALL_LINK` | on |
| Store op source code | `settings={"capture_code": False}` | `WEFT_CAPTURE_CODE` | on |
| Record model reasoning (thinking, reasoning summaries, `weft.reasoning()`) | `settings={"capture_reasoning": False}` | `WEFT_CAPTURE_REASONING` | on |
| On-disk write-ahead log | `settings={"enable_disk_fallback": True, "wal_dir": ...}` | `WEFT_ENABLE_WAL`, `WEFT_WAL_DIR` | off (`~/.weft/wal`) |
| Write the WAL, send nothing (needs the WAL on) | `settings={"disable_wal_sender": True}` | `WEFT_DISABLE_WAL_SENDER` | off |
| Print every HTTP request and response to stderr | | `WEFT_DEBUG_HTTP=1` | off |
| Send queue size (0 = unbounded) | `settings={"max_queue_size": 10000}` | `WEFT_MAX_CALLS_QUEUE_SIZE` | 10000 |
| Retries of a failed request | `settings={"retry_max_attempts": 3}` | `WEFT_RETRY_MAX_ATTEMPTS` | 3 |
| First / longest retry delay (s) | `settings={"retry_backoff": 0.25, "retry_max_backoff": 4.0}` | `WEFT_RETRY_BACKOFF`, `WEFT_RETRY_MAX_INTERVAL` | 0.25, 4 |
| HTTP timeout (s) | `settings={"http_timeout": 30}` or `WeftClient(timeout=)` | `WEFT_HTTP_TIMEOUT` | 30 |
| Log level of the `weft` logger | `settings={"log_level": "DEBUG"}` | `WEFT_LOG_LEVEL` | unchanged (Python's default) |
| Run linkage | `settings={"run_id": ..., "run_step": ...}`, `weft.set_run()` | | none |
| Cache of immutable server reads | `settings={"use_server_cache": False, "server_cache_dir": ..., "server_cache_size_limit": 10**9}` | `WEFT_USE_SERVER_CACHE`, `WEFT_SERVER_CACHE_DIR`, `WEFT_SERVER_CACHE_SIZE_LIMIT` | on, `~/.cache/weft`, 1 GB |

Explicit settings and arguments win over the environment variables. Invalid
numbers are ignored with a warning.

**WAL.** Weft's write-ahead log is a disk fallback: requests are sent from
memory, and only what cannot be sent (server down, queue full, process
exiting) is appended to `<wal_dir>/<server>_<project>.jsonl`, which the next
`weft.init` for that server and project replays.
`WEFT_DISABLE_WAL_SENDER` (write the WAL, never send): with `WEFT_ENABLE_WAL` on, every write (call
starts and ends, op and object versions, files) goes to the WAL file, nothing
is sent, and nothing is replayed at start-up. Reads still go to the server.
Run once more without the flag to send the file. Without `WEFT_ENABLE_WAL`
the flag does nothing.

**`WEFT_DEBUG_HTTP=1`** prints each request
(time, thread, method, URL, headers with `Authorization` redacted, JSON body)
and its response (elapsed time, status, headers, body) to stderr; bodies
over 64 KB are cut and file uploads are summarised. It is read on every
request.

**Run linkage.** Spans can be linked to a training or experiment run: after
`weft.set_run("run-abc", step=lambda: trainer.global_step)` (an int or a
callable; `weft.set_run(None)` clears it) spans carry `run_id`
(`<entity>/<project>/<run id>`) and `run_step`, and `run_step_end` when
they finish. Fetched spans expose
`user_id`, `run_id`, `run_step`, `run_step_end`, and `username`
when queried with `include_usernames=True` (whether these come back depends on
the server version).

Root spans print `weft: <server>/<entity>/<project>/calls/<id>` to stderr
(`span.ui_url` gives the same link).

**Durability.** With `enable_disk_fallback`, anything that cannot be sent goes
to a JSONL file instead of being dropped. That covers batches that still fail
after retries, items that overflow the queue, and whatever is still queued at
exit. The next `weft.init()` against the same server and project replays it.

**Server-response cache.** Reads that can never change are kept in memory and in an on-disk SQLite file
(`~/.cache/weft/server_cache.sqlite`) and reused across processes:
object versions read by digest (`GET /api/v1/objects/{name}/versions/{digest}`;
not `latest`, `v3` or an alias, and tags and aliases are always read fresh),
`POST /api/v1/tables/query` and `/tables/stats` by table digest, `GET
/api/v1/files/{digest}`, and object/table refs in `POST /api/v1/refs/read`.
Deleting an object through the client drops its cached reads. `client.cache_stats()` gives hits/misses/skips and
`client.clear_server_cache()` empties it.

A bare project name `"p"` is stored as `"default/p"`. When the server uses
`user=key` API keys, spans, objects and feedback are attributed to that user
(`client.whoami()` shows it).

### Redaction

By default, values under dict keys named `api_key`, `apikey`, `authorization`,
`auth`, `password`, `secret`, `token` or `access_token` are replaced with
`"REDACTED"`. Matching is case-insensitive and on the exact key, at any depth
in call inputs and outputs, and happens before anything leaves the process.

```python
weft.init("my-project", settings={
    "redact_keys": ["api_key", "ssn"],   # replaces the default list
    "redact_pii": True,                  # also mask PII entities in strings
})
```

Set `WEFT_REDACT_KEYS=0` to turn key redaction off.

**PII entities.** With `redact_pii` (or `WEFT_REDACT_PII=1`), strings in
inputs and outputs have entities replaced with `<ENTITY_TYPE>`. Regular
expressions always cover e-mail addresses, US SSNs, credit cards, phone
numbers and IP addresses. Names (`PERSON`), places (`LOCATION`),
organisations (`ORGANIZATION`) and Presidio's other entities need an NER
engine, picked by `redact_pii_engine`:

| `redact_pii_engine` | NER | Install |
|---|---|---|
| `"auto"` (default) | Presidio when installed, else regex only | `pip install 'weft[pii]'` and `python -m spacy download en_core_web_lg` |
| `"presidio"` | Presidio `AnalyzerEngine` (error if missing) | as above |
| `"transformers"` | Hugging Face NER pipeline, `redact_pii_model` (default `dslim/bert-base-NER`) | `pip install 'weft[scorers]'` |
| `"regex"` | none | |

```python
weft.init("my-project", settings={
    "redact_pii": True,
    "redact_pii_fields": ["PERSON", "LOCATION", "ORGANIZATION", "EMAIL_ADDRESS"],  # default: DEFAULT_PII_ENTITIES
    "redact_pii_exclude_fields": ["LOCATION"],
    # or bring your own analyzer: Presidio-style .analyze(text=, entities=, language=)
    # or a callable(text) -> [(entity_type, start, end), ...]
    # "redact_pii_analyzer": my_analyzer,
})
# "Alice Smith from Acme in Berlin" -> "<PERSON> from <ORGANIZATION> in Berlin"
```

The engine loads on first use. If it fails at run time (for example a missing
spaCy model), weft logs a warning and falls back to the regular expressions;
the call is still recorded.

## Tracing

```python
@weft.trace                                   # sync, async, generators, async generators
async def stream_tokens(prompt: str):
    for tok in prompt.split():
        yield tok                          # yielded items become the span output

@weft.trace(
    name="search",                         # function name (default: function qualname)
    call_display_name=lambda span: f"search: {span.inputs['q']}",
    postprocess_inputs=lambda d: {k: v for k, v in d.items() if k != "api_key"},
    postprocess_output=lambda out: out[:10],
    tracing_sample_rate=0.1,               # applies to root spans; children follow the root
    kind="retriever",                      # agent|llm|tool|chain|retriever|embedding|scorer|guardrail|...
    color="blue",                          # red|orange|yellow|green|blue|purple
    enable_code_capture=False,             # don't store this function's source
)
def search(q: str, api_key: str) -> list: ...

result, span = search.call("weft", api_key="...")   # (result, Span); never raises
span.id, span.trace_id, span.name, span.error

weft.get_current_span()                    # innermost span in this context
weft.require_current_span()                # the same, raising weft.NoCurrentSpanError outside a traced function

with weft.attributes({"env": "prod", "user_id": "u1"}):
    search("x", api_key="...")             # attributes stored on every span in the block

with weft.thread("conversation-42"):       # the spans' session_id; top-level spans are turns
    answer("hi")
    answer("and then?")

with weft.tracing_disabled():              # run without recording anything
    answer("off the record")
```

`kind` and `color` are stored on every span as `attributes.weft.span_kind` and
`attributes.weft.color`. The integrations below tag LLM spans with
`kind="llm"`, and built-in scorers use `kind="scorer"`.

- **Exceptions** are recorded as the span's `error` (`"ValueError: message"`
  plus the traceback) and re-raised.
- **Methods**: `self` is recorded as a ref when the object has been saved,
  as its field values for an unsaved `weft.Object`, and as `repr()` otherwise.
- **Concurrency**: asyncio tasks inherit the current span. Threads do not
  inherit it automatically; use `weft.ThreadPoolExecutor`, `weft.Thread` or
  `contextvars.copy_context().run(...)`.
- **LLM usage**: report the span's *own* usage with
  `weft.add_usage(weft.get_current_span(), model, prompt_tokens, completion_tokens)`.
  The server adds up descendants, so a root span's `summary.usage` covers the
  whole trace.
- **Values** are converted to JSON: pydantic models and dataclasses become
  dicts. Saved objects, traced functions and dataset rows become ref strings. `bytes` and
  PIL images are uploaded via `POST /api/v1/files/upload` and stored as
  `{"_type": "Image", "file_digest", "mime_type"}`. Video, PDF and audio bytes
  are detected the same way.

### Files and rich media

```python
weft.Content.from_path("report.pdf")              # {"_type": "Content", "mimetype", "filename", "file_digest"}
weft.Content.from_bytes(data, mimetype="application/zip", filename="a.zip")
weft.Content.from_text("plain text", filename="notes.txt")
weft.Video.from_path("demo.mp4"); weft.PDF.from_path("paper.pdf")
weft.Html("<b>rendered</b>"); weft.Markdown("# rendered")   # {"_type": "Html"|..., "file_digest", "mime_type", "filename"}
client.read_file(file_digest)                     # bytes back
```

Any of these can be returned from or passed to a traced function, or stored in objects and
dataset rows. `weft.Image` and `weft.Audio` wrap image and audio files the same way.

**Bring your own bucket.** Media that already lives in your storage is logged
by reference, without uploading:

```python
weft.Image.from_uri("s3://my-bucket/photos/cat.png")     # {"_type": "Image", "uri", "mime_type", "filename"}
weft.Content.from_uri("https://example.com/report.pdf", size=1234)
weft.Video.from_uri("s3://my-bucket/clip.mp4"); weft.PDF.from_uri("weft-file://<digest>")
client.resolve_url("s3://my-bucket/photos/cat.png")       # POST /api/v1/files/resolve: presigned for the server's S3 backend
client.resolve_url([content, file_digest], expires_in=600)  # Content values, digests, weft-file:// and https:// URIs
```

Reading a reference (`.data`, `.as_text()`, `.save()`) downloads it, through
`resolve_url` for non-HTTP URIs.

## Agents: conversations, turns, LLM calls and tools

`weft.start_conversation()` and its turns, LLM calls, tools and sub-agents
record each as a span the server's agents index understands, so
conversations show up in **Agents** (`/agents/*`), Threads and Traces:

```python
with weft.start_conversation(agent_name="support", conversation_name="Order help") as conv:
    with conv.start_turn(user_message="Where is order 7?", system_instructions=["Be brief."]) as turn:
        with turn.start_llm(model="gpt-4o-mini", provider_name="openai") as llm:
            llm.input_messages = [weft.Message.user("Where is order 7?")]
            llm.output_messages = [weft.Message.assistant("", tool_calls=[
                weft.conversation.ToolCallPart(id="c1", name="lookup", arguments={"order_id": 7})])]
            llm.usage = weft.Usage(input_tokens=20, output_tokens=6)
        with turn.start_tool(name="lookup", arguments={"order_id": 7}, tool_call_id="c1") as tool:
            tool.result = lookup(7)            # @weft.trace spans inside become children
        with turn.start_subagent(name="writer") as sub:
            with sub.start_llm(model="gpt-4o") as w:
                w.output("Your order has shipped.")
        turn.output_messages = [weft.Message.assistant("Order 7 has shipped.")]
```

- Module functions use the current conversation/turn: `weft.start_turn`,
  `weft.start_llm`, `weft.start_tool`, `weft.start_subagent`, `weft.end_turn`,
  `weft.get_current_turn()`, ... Spans also work without `with`
  (`span.start()` / `span.end()`).
- A turn is an `invoke_agent` span (`attributes.gen_ai.agent.name`,
  `attributes.weft.span_kind="agent"`) and the root of its own trace; the
  conversation id is its `session_id` and the turn id its own id. Pass
  `continue_parent_trace=True` to nest turns under the current span. LLM calls
  are `chat` spans with `inputs.messages`, an OpenAI-style output and
  `summary.usage` (so costs work); tools are `execute_tool` spans.
- `llm.think(...)`, `llm.attach_media(content=..., mime_type=...)` (uploaded),
  `span.set_attributes({...})`, `span.record_error(exc)` and
  `start_conversation(include_content=False, attributes={...})` are available.
- Spans are sent when they end. `turn.call_id` / `turn.trace_id` / `turn.call`
  give the recorded span (`weft.Span`).

**Batch logging** of finished spans (queue workers, replays):

```python
weft.log_turn(conversation_id="c-1", agent_name="support",
              messages=[weft.Message.user("hi")], output_messages=[weft.Message.assistant("hello")],
              spans=[weft.LLM(model="gpt-4o-mini", started_at=t0, ended_at=t1, usage=weft.Usage(input_tokens=5))])
res = weft.log_conversation(turns=[weft.Turn(messages=[...], spans=[weft.Tool(name="t", result=1)])],
                            agent_name="support")
res.conversation_id, res.root_span_ids, res.trace_ids
```

## Agent teams, hand-offs, reasoning and sessions

The server derives sessions, teams, the hand-offs between agents and each
model's reasoning from what the SDK records ([DESIGN.md](../../docs/weft/DESIGN.md)
§2), and shows them under **Sessions**, **Teams** and the native API
(`/api/v1/sessions/{id}`, `/graph`, `/reasoning`, `/api/v1/teams`).

**Sessions.** A session is `gen_ai.conversation.id`: the conversation id of
`weft.start_conversation(conversation_id=...)` (the spans' `session_id`), or
the session/thread/group id an integration records (OpenAI Agents
`group_id`, LangGraph `thread_id`, Claude Agent SDK session id, Google ADK
`session_id`, Bedrock Agents `sessionId`; see the integration table). Spans
without one belong to no session; their children inherit it.

**Teams.** Declare the team of an agent run; every call and span recorded
inside carries `weft.team.id` / `weft.team.name`:

```python
with weft.team("trip-planners", "Trip planners"):
    run_orchestrator()

# or for one conversation (also for log_turn / log_conversation)
with weft.start_conversation(agent_name="orchestrator", team=weft.Team("trip-planners", "Trip planners")) as conv:
    ...
```

`team=` takes a `weft.Team`, an id, `(id, name)` or `{"id": ..., "name": ...}`;
`weft.get_current_team()` returns the active one. Without a team the server
derives one from the agents that took part (`auto:a+b`).

**Hand-offs.** Two shapes, both recorded as a span with
`weft.handoff.to_agent`, `weft.handoff.from_agent` (default: the agent
running now) and `weft.handoff.kind` (`handoff`: the other agent takes over;
`delegate`: it works for the caller and returns):

```python
# control passes to billing now; linked to billing's next run in the trace
weft.handoff("billing", input="refund order 7")

# a delegation: the researcher's run happens inside, as a child of the hand-off
with weft.start_handoff("researcher", input=task, kind="delegate") as h:
    with conv.start_turn(agent_name="researcher", user_message=task) as rt:
        ...
    h.output = facts
```

`turn.handoff(...)`, `turn.start_handoff(...)` and `subagent.start_handoff(...)`
do the same from a given span. Nested runs of a *different* agent are
delegations even without an explicit hand-off: `turn.start_subagent(...)`,
an `@weft.trace(kind="agent")` called by another agent, or a turn of another
agent started while one is running (the running turn stays open and the new
one nests under it, in the same trace). The server records the `delegate`
edge and, when the delegate finishes, the `return`.

**Reasoning.** Model reasoning is recorded automatically by the integrations,
in the shapes the server reads reasoning steps from:

| Source | Recorded as |
|---|---|
| Anthropic `thinking` / `redacted_thinking` blocks (also streamed, also `messages.stream()`) | the response content blocks (`kind: thinking` / `redacted`) |
| OpenAI Responses `reasoning` items, `reasoning_tokens` | the output items (summaries: `kind: summary`) and `summary.usage` |
| OpenAI-compatible chat `reasoning_content` (DeepSeek, vLLM) / `reasoning` (OpenRouter), streamed too | the message field |
| Gemini parts with `thought: true` (streamed thoughts are joined), `thoughts_token_count` | the thought part, `reasoning_tokens` |
| Bedrock Converse `reasoningContent` (also `converse_stream`) | `thinking` / `redacted_thinking` blocks of the output |
| Claude Agent SDK thinking blocks | the LLM call's content blocks |
| OpenAI Agents SDK response spans | the response's reasoning items |

Redacted thinking (Anthropic `redacted_thinking`, Bedrock `redactedContent`,
Responses items with only `encrypted_content`) is kept as redacted, with its
encrypted payload replaced by a digest (`sha256:...`), never as text. When a
framework's LLM call wraps a traced provider call, the reasoning stays on the
provider call only, so it is not counted twice.

Record a step yourself with `weft.reasoning(text, kind="thinking")` (`kind`
is `thinking`, `summary` or `redacted`; `tokens=` is optional). It goes on
the innermost running span or op and is linked by the server to the tool
call or hand-off that follows it; `llm.think(text)` adds a thinking part to
an LLM call's output.

```python
with conv.start_turn(agent_name="researcher", user_message=task) as rt:
    weft.reasoning("Search the web first, then pick three facts.")
    facts = web_search("Oslo facts")      # the step's next_span_id
```

`settings={"capture_reasoning": False}` (or `WEFT_CAPTURE_REASONING=false`)
removes reasoning from everything recorded (thinking blocks, reasoning items,
`reasoning_content`, thought parts, `weft.reasoning()`); token counts are
kept. PII redaction (`redact_pii`) applies to reasoning text like any other
text. A runnable two-agent example is
[`examples/weft/python/agent_team.py`](../../examples/weft/python/agent_team.py).

## Integrations (auto-patched by `weft.init`)

`weft.init()` patches every supported library that is installed. Patching is
idempotent, and nothing happens for libraries that are not installed. A
library that is already imported is patched at once; one imported later is
patched as soon as its import finishes, so `weft.init()` never imports
frameworks you do not use. Pass `autopatch=False` to opt out entirely.

**LLM SDKs.** Each request becomes an `llm` call.

| Settings key | Library | Traced | Op name(s) |
|---|---|---|---|
| `openai` | openai | `chat.completions.create`, `responses.create` (sync/async/stream) | `openai.chat.completions.create`, `openai.responses.create` |
| `openai_realtime` | openai, websockets, websocket-client, aiohttp | Realtime connections: `client.realtime.connect` / `client.beta.realtime.connect` (sync/async), the OpenAI Agents SDK's `RealtimeRunner`, and raw `websockets.connect`, `websocket.WebSocketApp` and `aiohttp` `ws_connect` clients; audio saved as WAV | `openai.realtime.session`, `.response`, `.tool.<name>` |
| `anthropic` | anthropic | `messages.create` (sync/async/`stream=True`), `messages.stream()` | `anthropic.messages.create`, `anthropic.messages.stream` |
| `litellm` | litellm | `completion`, `acompletion` (incl. stream) | `litellm.completion`, `litellm.acompletion` |
| `google_genai` | google-genai | `models.generate_content(_stream)`, sync and `client.aio` | `google.genai.models.generate_content(_stream)` |
| `vertexai` | google-cloud-aiplatform | `GenerativeModel.generate_content(_async)` (incl. `stream=True`), `ChatSession.send_message(_async)`, `ImageGenerationModel.generate_images` | `vertexai.GenerativeModel.generate_content`, ... |
| `bedrock` | boto3 | `converse`, `converse_stream`, `invoke_model`, `invoke_model_with_response_stream`; Bedrock Agents `invoke_agent`, `invoke_inline_agent` (see below) | `bedrock.converse`, ..., `bedrock_agent.invoke_agent` |
| `mistral` | mistralai | `chat.complete`, `chat.stream` (+ `_async`) | `mistralai.chat.complete`, `mistralai.chat.stream` |
| `cohere` | cohere | `ClientV2`/`Client` `chat`, `chat_stream` (sync/async) | `cohere.chat`, `cohere.chat_stream` |
| `groq`, `together`, `cerebras` | groq, together, cerebras-cloud-sdk | `chat.completions.create` (OpenAI-shaped) | `groq.chat.completions.create`, `together...`, `cerebras...` |
| `huggingface` | huggingface_hub | `InferenceClient`/`AsyncInferenceClient` `chat_completion` (incl. `client.chat.completions.create`, streams), `text_generation`, and task methods (`summarization`, `translation`, `text_to_image`, ...) | `huggingface_hub.InferenceClient.chat_completion`, ... |

**Agent and RAG frameworks.**

| Settings key | Library | Traced (span kind) | Op name(s) |
|---|---|---|---|
| `langchain` | langchain-core, LangGraph | every chain, chat model/LLM, tool and retriever run; LangGraph graphs (workflow; session = `thread_id`) and nodes, with `attributes.langgraph` (node, step, triggers); a node that ran a model is an agent named after it | `langchain.<Type>.<name>`, `langgraph.graph`, `langgraph.node.<node>` |
| `llamaindex` | llama-index-core | every instrumented span (query engines, retrievers, LLMs, embeddings, agents, workflows, tools) via `WeftSpanHandler` | `llama_index.<Class>.<method>` |
| `openai_agents` | openai-agents | traces (workflow; session = `group_id`, else the trace id) and agent (`gen_ai.agent.name`)/function/generation/response/handoff (`weft.handoff.*`)/guardrail spans | `openai_agents.trace`, `agent.<name>`, `tool.<name>`, ... |
| `dspy` | dspy | modules, LM calls, tools | `dspy.<Module>`, `dspy.LM`, `dspy.Tool.<name>` |
| `crewai` | crewai | `Crew.kickoff*` (workflow; a named crew is the team, the crew id the session), `Agent.execute_task` (agent, `gen_ai.agent.name` = role), `Task.execute_*` (chain), `LLM.call` (llm), tools (`gen_ai.tool.name`; the delegation tools are hand-offs), `Flow.kickoff*` (workflow) | `crewai.Crew.kickoff`, `crewai.Agent.execute_task`, ... |
| `smolagents` | smolagents | agent `run` (agent) and `step`, every `Model.generate` (llm), `Tool.__call__` (tool) | `smolagents.Agent.run`, `smolagents.<Model>.generate`, `smolagents.Tool` |
| `instructor` | instructor | `create`, `create_with_completion`, `create_partial`, `create_iterable` (sync/async; llm) | `instructor.Instructor.create`, ... |
| `mcp` | mcp, fastmcp | client `call_tool` (tool), `read_resource`, `get_prompt`, `list_*`; server (`FastMCP`) `call_tool`, `read_resource`, `get_prompt` | `mcp.ClientSession.call_tool`, `mcp.FastMCP.call_tool`, `fastmcp.Client.call_tool`, ... |
| `claude_agent_sdk` | claude-agent-sdk | `query()` (agent `claude-agent-sdk`) and `ClaudeSDKClient` turns (agent), session = the Claude session id; each assistant message (llm, with its thinking blocks), each tool use (tool, `gen_ai.tool.call.id` = the tool-use id; `Task` sub-agents are agents named by `subagent_type` and nest their messages) | `claude_agent_sdk.query`, `.turn`, `.llm`, `.tool.<name>` |
| `haystack` | haystack-ai | pipelines (chain) and components (`llm`, `retriever`, `embedding`, `reranker`, `tool`, `agent`) via `WeftHaystackTracer` | `haystack.pipeline.run`, `haystack.<ComponentType>` |
| `verdict` | verdict | `Pipeline` runs (workflow) and units such as `JudgeUnit` (scorer), via Verdict's tracer interface | `verdict.<pipeline name>`, `verdict.<Unit>` |
| `typesafe` | typesafe-sdk | `TypeSafeClient.system_one` / `AsyncTypeSafeClient.system_one` (llm, in its own `typesafe SDK` turn unless a turn is running; inside an op that turn is a separate trace linked by `weft.parent_call.*`) | `chat` |
| `autogen` | autogen-agentchat / -core / -ext | agents `run`/`run_stream`/`on_messages` (agent, `gen_ai.agent.name`), teams (workflow; a team with its own `name` is the Weft team), model clients `create`/`create_stream` (llm), tools (Swarm `transfer_to_<agent>` tools are hand-offs), memory, code executors | `autogen.Agent.run`, `autogen.ChatCompletionClient.create`, ... |
| `agno` | agno | `Agent`/`Team` `run`/`arun` (agent), `Workflow` (workflow), `Model.response` (llm), tool calls | `agno.Agent.run`, `agno.Model.response`, ... |
| `google_adk` | google-adk | `Runner.run(_async)` (workflow; session = `session_id`), agents (`gen_ai.agent.name`), tools (with the function call id; `transfer_to_agent` is a hand-off), LLM calls | `google_adk.Runner.run`, `google_adk.Agent.run`, ... |
| `pydantic_ai` | pydantic-ai | `Agent.run` (agent, `gen_ai.agent.name`; an agent run inside another agent's tool is a delegation), tool calls (with the tool call id), `Model.request` (llm) | `pydantic_ai.Agent.run`, ... |
| `notdiamond`, `verifiers`, `gepa` | | model routing, RL environments/rubrics, `gepa.optimize` | see `weft/integrations/frameworks.py` |

Each LLM request becomes a call (`kind="llm"`):
- **Inputs** are the request kwargs.
- **Output** is the response; streams are accumulated into a single response.
- **Usage** is recorded as
  `summary.usage = {model: {requests, prompt_tokens, completion_tokens, total_tokens}}`.
  Prompt caching is kept the way the server prices it: `prompt_tokens` is the gross
  prompt count and `cache_read_input_tokens` / `cache_creation_input_tokens`
  are the parts of it read from and written to the cache. Anthropic and
  Bedrock report cache counts outside their input count, so the SDK adds them
  in; OpenAI's `prompt_tokens_details` / `input_tokens_details.cached_tokens`
  (kept as well) and Gemini's `cached_content_token_count` are already inside
  it. Reasoning tokens are kept as `reasoning_tokens`.
- Every integration call carries `attributes.weft.integration` (the settings key).

For streamed OpenAI chat completions the SDK adds
`stream_options={"include_usage": True}` unless you set `stream_options`
yourself. Groq reports streamed usage in `x_groq.usage`. Bedrock usage comes
from the response, the stream metadata or the
`x-amzn-bedrock-*-token-count` headers.

**No double counting.** Wrapper libraries call provider SDKs that are also
patched:
- **LiteLLM** and **Vertex AI chat sessions:** the provider calls they make are
  not traced again.
- **Frameworks** (LangChain, LlamaIndex, OpenAI Agents, DSPy, CrewAI,
  smolagents, Instructor, Haystack, AutoGen, Agno, ADK, Pydantic AI): the
  framework's span is the parent, so provider spans and your own traced functions nest
  under it. A framework LLM call records usage only when no nested call
  already did.
- **Claude Agent SDK:** usage and cost from the final `ResultMessage` are
  recorded once, on the query/turn call.

**OpenAI-compatible endpoints and NVIDIA NIM.** The `openai` integration tags
each call with `attributes.weft.provider` and `base_url` from the client's base
URL: `nvidia_nim` for `integrate.api.nvidia.com` (NVIDIA NIM), and `azure_openai`,
`openrouter`, `groq`, `together`, `cerebras`, `fireworks`, `deepseek`, `xai`,
`local` ... A self-hosted NIM (or vLLM) server reads as `openai_compatible`:

```python
from openai import OpenAI
nim = OpenAI(base_url="https://integrate.api.nvidia.com/v1", api_key=os.environ["NVIDIA_API_KEY"])
nim.chat.completions.create(model="meta/llama-3.1-8b-instruct", messages=[...])   # traced, provider=nvidia_nim
```

LangChain's `ChatNVIDIA` (langchain-nvidia-ai-endpoints) is traced by the
LangChain tracer. To give one client instance of an OpenAI-shaped SDK its own
op names (when the `openai` class patch is off):

```python
from weft.integrations import patch_openai_compatible
patch_openai_compatible(client, name="vllm")   # -> vllm.chat.completions.create
```

**LangChain and LangGraph.** `weft.init()` registers `WeftTracer` through
LangChain's configure hook, so every run is traced. Set
`WEFT_TRACE_LANGCHAIN=false` to opt out. You can also pass it explicitly:

```python
from weft.integrations.langchain import WeftTracer
chain.invoke({"question": "..."}, config={"callbacks": [WeftTracer()]})
```

**Callback-style integrations.** OpenAI Agents
(`agents.add_trace_processor(WeftTracingProcessor())`), DSPy
(`WeftDSPyCallback` in `dspy.settings.callbacks`), LlamaIndex
(`WeftSpanHandler` on the root instrumentation dispatcher) and Haystack
(`haystack.tracing.enable_tracing(WeftHaystackTracer())`, with content
tracing on so component inputs and outputs are kept; set
`HAYSTACK_CONTENT_TRACING_ENABLED=false` first to keep them out) are
registered for you.

**OpenAI Realtime.** Each realtime connection is a session call. Every
response (`response.created` → `response.done`) is an LLM call whose inputs
are the conversation items added since the previous response (text, input
audio, transcripts, function-call outputs) plus instructions and voice, and
whose output holds the response items, the streamed text/transcript and the
audio size. Usage includes cached and audio token details. Function calls are
tool calls that end when you send their `function_call_output`.

Audio is saved as WAV files (`weft.Audio`): the user's audio
(`input_audio_buffer.append`, cut per item at the server-VAD speech markers,
or at each commit without VAD) on the item's `input_audio` content part, and
the model's audio (`response.output_audio.delta`) on the `output_audio` part
of the response output. PCM16 at the session's rate (24 kHz by default) and
G.711 u-law/A-law are written. Each clip is capped at
`WEFT_REALTIME_MAX_AUDIO_BYTES` (default 25 MiB of raw audio, about 9 minutes
of 24 kHz PCM16; the server accepts files up to 64 MiB); a longer clip is cut
there and marked `audio_truncated`. `WEFT_REALTIME_MAX_AUDIO_BYTES=0` records
sizes only.

Besides the `openai` package, the integration patches websocket clients, so
the OpenAI Agents SDK (`agents.realtime.RealtimeRunner`,
which connects with `websockets.connect`) and hand-written clients are traced
without changes: `websockets.connect`, `websocket.WebSocketApp`
(websocket-client) and `aiohttp.ClientSession.ws_connect`. Only Realtime URLs
are traced (host `api.openai.com` or `*.openai.azure.com` with `realtime` in
the path; add hosts with `WEFT_REALTIME_HOSTS=host1,host2`). The connection
objects are not replaced, so `async with`, `async for` and `isinstance` keep
working. With autopatching off, wrap one client yourself (any URL):

```python
from weft.integrations import wrap_websocket_async, wrap_websocket_connect, wrap_websocket_sync
connect = wrap_websocket_connect(websockets.connect)      # async with connect(url) as ws: ...
ws = wrap_websocket_async(await websockets.connect(url))  # or an aiohttp ws_connect response
App = wrap_websocket_sync(websocket.WebSocketApp)         # App(url, on_message=...)
```

For anything else, call `RealtimeTracer().on_client_event(e)` /
`.on_server_event(e)` / `.close()`.

**MCP.** Set `WEFT_MCP_TRACE_LIST_OPERATIONS=false` to skip the `list_*`
methods.

**AWS Bedrock Agents.** `invoke_agent` and `invoke_inline_agent` on any
`bedrock-agent-runtime` client become an `invoke_agent` agent call, a turn of
the conversation `sessionId` (so Bedrock sessions show under **Agents** and
**Threads**). The `completion` stream is wrapped; while you iterate it, the
trace events (`enableTrace=True`) become child calls:

```
bedrock_agent.invoke_agent        agent: inputText, the streamed answer, citations, returnControl
├─ bedrock_agent.model_invocation llm: pre-processing / orchestration / post-processing / routing
│                                 prompts, raw and parsed responses, token usage per foundation model
├─ bedrock_agent.action_group     tool: action group function or API path, parameters, result
├─ bedrock_agent.knowledge_base   retriever: query and retrieved references
├─ bedrock_agent.code_interpreter tool
├─ bedrock_agent.guardrail        guardrail assessments
├─ bedrock_agent.return_control   tool: functions handed back to your application
└─ bedrock_agent.collaborator     agent: a multi-agent collaborator; its own trace nests here
```

A `failureTrace` ends the open steps and the agent call with the failure
reason. Without `enableTrace` only the agent call (answer, citations) is
recorded. The call ends when the stream is consumed or closed.

**Verdict.** Every Verdict `Pipeline` (including `run(tracers=...)`) gets a
Weft tracer through Verdict's own tracing interface: the
pipeline run is a `workflow` call (`verdict.<pipeline name>`) and each unit a
child call (`verdict.JudgeUnit`, ...; judge-like units have kind `scorer`)
with its input schema and output. Units run in Verdict's thread pool; they
still nest correctly, and LLM requests made inside a unit (through litellm,
openai, instructor ...) nest under the unit. Chained blocks
(`Pipeline() >> a >> b`) stay in one trace: Verdict starts `b` with the
finished `a` as its parent, so `b` nests under `a`.

**TypeSafe.** `system_one(state, questions)` becomes one LLM call of provider `typesafe`: the state is
the user message, the questions are the `typesafe.questions` attribute, the
answers are the assistant message (`gen_ai.output.type` `json`), with the
response model, token usage and request id. It nests under the running turn,
else in its own `typesafe SDK` turn. The integration creates no op call, so
that turn starts its own trace even inside an `@weft.trace`;
when an op is running, the turn and the LLM call carry a link to it,
`attributes.weft.parent_call.trace_id` and `.id`, which the server exposes as the agent span fields
`parent_call_trace_id` / `parent_call_id` (so
`{"$eq": [{"$getField": "parent_call_id"}, {"$literal": call.id}]}` on
`/agents/spans/query` finds the spans an op produced). A failed call records the error
type and HTTP status, not TypeSafe's error message (it carries the response
body). `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=false`,
`patch_typesafe(capture_content=False)` or a conversation with
`include_content=False` drops state, questions and answers.

### Per-integration settings

`autopatch_settings` is a dict (or `AutopatchSettings`) keyed by
the settings keys above. Each value has `enabled` and `op_settings`
(`name`, `call_display_name`, `postprocess_inputs`, `postprocess_output`,
`kind`, `color`, `attributes`), applied to every call the integration records:

```python
from weft.integrations import IntegrationSettings, OpSettings

def scrub(inputs):
    return {**inputs, "messages": "<hidden>"}

weft.init("my-project", autopatch_settings={
    "openai": {"op_settings": {"postprocess_inputs": scrub,
                               "postprocess_output": lambda r: {"id": r.get("id")},
                               "call_display_name": "chat"}},
    "crewai": {"enabled": False},
    "anthropic": IntegrationSettings(op_settings=OpSettings(color="purple", attributes={"team": "search"})),
})
```

`postprocess_*` change only what is recorded; your code still gets the real
response, and usage is still recorded. Disabling an integration also stops an
already-patched one from recording. From the environment:
`WEFT_AUTOPATCH=0` (all off), `WEFT_DISABLED_INTEGRATIONS=openai,crewai`, or
`WEFT_AUTOPATCH_<NAME>=0` (e.g. `WEFT_AUTOPATCH_LLAMAINDEX=0`).
`weft.integrations.autopatch()` returns each integration's status (`patched`,
`deferred` until imported, `disabled`, `absent`), and `patch("crewai")` patches
one by name.

## Objects, refs and prompts

```python
class RetrieverConfig(weft.Object):        # pydantic model with name/description
    top_k: int = 5

ref = weft.save(RetrieverConfig(name="retriever", top_k=3))   # POST /api/v1/objects/save
ref.uri()                      # weft:///default/my-project/object/retriever:<digest>
weft.get(ref)                  # -> RetrieverConfig(top_k=3)

weft.get("retriever")          # latest
weft.get("retriever:v0")       # by version index
weft.get("retriever:latest")   # or an alias: "retriever:production"
weft.get(ref.uri())            # any weft:/// URI

prompt = weft.StringPrompt("Answer briefly: {question}")
prompt.format(question="2+2?")
chat = weft.MessagesPrompt([{"role": "system", "content": "You are {persona}"}])
weft.save(chat, name="chat-prompt")
```

Saving is content-addressed: saving an identical object again gives the same
digest, and the server keeps one version. A digest is the lowercase hex
SHA-256 of the value's canonical JSON ([DESIGN.md](../../docs/weft/DESIGN.md)
§10.3); the SDK computes it locally for traced functions (their spans name the
function ref before the server answers) and checks it against the server's.
Refs name `object`, `function`, `table` and `span` kinds; refs written with
the earlier kind names (`op`, `call`) are read as `function` and `span`.

**Registry.** `link_prompt_to_registry`, plus `link_to_registry` for any
object (datasets, models, scorers, ...), link a saved version into a
collection of a registry project (`POST /api/v1/registry/link`; the registry is
kept by the Weft server):

```python
weft.link_prompt_to_registry(prompt, target_path="prompts/support-bot", aliases=["production"])
weft.link_to_registry(dataset, target_path="my-team/datasets-registry/golden-sets")  # entity/registry/collection
weft.link_to_registry("chat-prompt:v2", target_path="prompts-registry/chat")          # name:version, ref or URI
client.query_registry("prompts", "support-bot")       # versions, newest first, with aliases
client.get_from_registry("prompts/support-bot", "production")  # alias, "v0", 0 or "latest"
```

`target_path` is `<registry_project>/<collection>` (in your entity) or
`<entity>/<registry_project>/<collection>`; any project can be a registry
project. An unsaved object is saved
first. Linking a version already in the collection returns its index;
aliases move to the version they were given last.

**Loading functions runs stored code.** `weft.get("my_fn")` returns a runnable
`weft.Op` (a traced function). The SDK rebuilds it from the stored source by `exec`-ing it in a
fresh namespace, strips its decorators and wraps it with `@weft.trace` again. Its
spans are linked to the same function ref. A saved `Model`/`Scorer` subclass
that is not defined in the loading process comes back as a rebuilt class whose
function-ref methods are loaded the same way, so `loaded_model.predict(...)` works.
Only self-contained functions can be rebuilt; anything they reference from
their original module (imports, helpers) has to exist at call time. When the
code can't be rebuilt you get `weft.OpLoadError`. **Only load functions from servers
and projects you trust.** Nested objects are saved
first and stored as refs. Objects whose class is not defined locally load as
`weft.WeftObject`, a dict that also supports attribute access.

## Datasets and models

```python
ds = weft.Dataset(name="qa", rows=[{"question": "2+2", "expected": "4"}])
weft.save(ds)                     # rows uploaded as a table
for row in weft.get("qa").rows:
    ...
ds2 = ds.add_rows([{"question": "3+3", "expected": "6"}])   # POST /api/v1/tables/update + new version
weft.Dataset.from_spans(client.spans(filter={"names": ["answer"]}))
ds.select([0, 2])                    # new dataset with those rows
weft.Dataset.from_pandas(df); ds.to_pandas()                  # needs pandas
weft.Dataset.from_hf(hf_dataset, split="train"); ds.to_hf()   # needs `pip install datasets`

class MyModel(weft.Model):
    system_prompt: str

    @weft.trace
    def predict(self, question: str) -> str:
        ...
```

`weft.LLMStructuredCompletionModel` is a model the server can run too
(`POST /api/v1/evaluations/run`). When run locally its `predict` calls the server's
`POST /api/v1/completions`, so provider keys stay on the server:

```python
model = weft.LLMStructuredCompletionModel(
    name="qa-model", llm_model_id="gpt-4o-mini",
    default_params={"messages_template": [{"role": "user", "content": "{question}"}],
                    "temperature": 0, "response_format": "text"})
```

## Evaluations

```python
from weft import scorers

@weft.trace
def exact(expected: str, output: str) -> bool:      # args matched to dataset columns
    return expected == output

class Brevity(weft.Scorer):
    @weft.trace
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
# also accepts a plain @weft.trace function as the model
```

Rows run concurrently (`WEFT_PARALLELISM`, default 10). The spans recorded:

```
Evaluation.evaluate            inputs {self: <evaluation ref>, model: <model ref>}; output = summary
├── Evaluation.predict_and_score   inputs {self, model, example: <dataset row ref>}
│   ├── MyModel.predict            output = model output
│   └── scorer spans               (exact, Brevity.score, ExactMatchScorer.score, ...)
└── Evaluation.summarize
```

Summary shapes: booleans → `{"true_count", "true_fraction"}`, numbers →
`{"mean"}`, dicts are summarised key by key, plus `model_latency: {"mean"}`.
Built-in scorers serialise in the server's format, so the same `Evaluation`
can run server side with `client.evaluate_model(...)` (`POST /api/v1/evaluations/run`) or be attached to a Monitor.

### Built-in scorers

All built-in scorers run locally. Every one except `PydanticScorer` also
serialises for server-side runs.

| Scorer | Fields | Result |
|---|---|---|
| `ExactMatchScorer` / `ContainsScorer` / `LevenshteinScorer` | `target_column` (+ `case_insensitive`) | bool / bool / similarity 0..1 |
| `RegexScorer` | `pattern` | bool |
| `JSONValidScorer` / `ValidXMLScorer` | | bool |
| `JSONSchemaScorer` | `schema` | bool (output validates) |
| `LLMAsAJudgeScorer` | `model`, `scoring_prompt`, `response_format` | judge JSON |
| `HallucinationFreeScorer` / `SummarizationScorer` / `RelevanceScorer` | `model`, `scoring_prompt` (preset) | `{hallucination_free}` / `{summarization_score, is_faithful}` / `{is_relevant, relevance_score}` + `reasoning` |
| `OpenAIModerationScorer` | | `{flagged, categories}` (locally needs `openai` and `OPENAI_API_KEY`) |
| `EmbeddingSimilarityScorer` | `target_column`, `model` | cosine similarity (locally uses the server's `POST /api/v1/embeddings`) |
| `RuleScorer` | `rule` (query-language operation), `threshold` | the rule's value; with `threshold`, numbers score `value >= threshold` |
| `RemoteScorer` | `url`, `headers_secret`, `timeout` | JSON response of `POST url {"output", "inputs"}` |
| `PydanticScorer` (local only) | `model` (a pydantic class) | bool (output parses) |

### Local model scorers

Local small-model scorers run in your process with
`pip install 'weft[scorers]'` (transformers, torch, presidio). The base
install does not pull these in; a missing dependency raises an `ImportError`
naming the extra. Models load on first use (or `scorer.load()`) from
`model_name_or_path` (a Hugging Face id or local path; by default the openly
licensed models below, `weft.local_scorers.MODEL_PATHS`), on `device` (`"cpu"`, `"cuda"`, `"mps"`).
`TrustScorer` takes one `<scorer>_model_name_or_path` per scorer it runs.

| Scorer | Default model | Licence | Why this model |
|---|---|---|---|
| `ToxicityScorer` | [`PleIAs/celadon`](https://huggingface.co/PleIAs/celadon) | Apache-2.0 | A DeBERTa-v3 classifier with five heads (race/origin, gender/sex, religion, ability, violence) of four levels each: exactly the five 0-3 categories the scorer reports. |
| `BiasScorer` | [`maximuspowers/bias-type-classifier`](https://huggingface.co/maximuspowers/bias-type-classifier) | MIT | Multi-label bias types (from the GUS-Net corpus) with `gender` and `racial` labels, read by name (`label_map`). |
| `CoherenceScorer` | [`cross-encoder/nli-deberta-v3-xsmall`](https://huggingface.co/cross-encoder/nli-deberta-v3-xsmall) | Apache-2.0 | A small NLI cross-encoder on (query, output): an answer that contradicts the query fails (`incoherent_labels`, default `["contradiction"]`). |
| `FluencyScorer` | [`pszemraj/electra-small-discriminator-CoLA`](https://huggingface.co/pszemraj/electra-small-discriminator-CoLA) | Apache-2.0 | A small CoLA acceptability classifier; the score is the probability of `acceptable` (`fluent_labels`). |
| `HallucinationScorer` | [`vectara/hallucination_evaluation_model`](https://huggingface.co/vectara/hallucination_evaluation_model) (HHEM-2.1-Open) | Apache-2.0 | The open hallucination detector: the score is 1 minus the probability that the context supports the output. Loaded with its own code (`trust_remote_code`). |
| `ContextRelevanceScorer` | [`cross-encoder/ms-marco-MiniLM-L6-v2`](https://huggingface.co/cross-encoder/ms-marco-MiniLM-L6-v2) | Apache-2.0 | A widely used query-passage relevance cross-encoder; the score is the token-weighted mean relevance of the context documents. |

Each default can be replaced with any model of the same kind
(`model_name_or_path=...`); the result format stays the same.

| Scorer (`weft.scorers` / `weft.local_scorers`) | `score(...)` | `metadata` (+ `passed`) |
|---|---|---|
| `ToxicityScorer` | `output` | five category levels; fails at `total_threshold`/`category_threshold` |
| `BiasScorer` | `output` | `gender_bias(_score)`, `racial_bias(_score)` |
| `CoherenceScorer` | `query`, `output` (`chat_history` or `context`) | `coherence_label`, `coherence_id`, `score` |
| `FluencyScorer` | `output` | `score` (fluent probability) |
| `HallucinationScorer` | `query`, `context`, `output` | `score` (higher = more hallucinated) |
| `ContextRelevanceScorer` | `query`, `output` (the context) | `score`, `all_spans` |
| `TrustScorer` | `query`, `context`, `output` | `trust_level`, critical and advisory issues, per-scorer scores |
| `PresidioScorer` (guardrail) | `output` | `detected_entities`, `reason`, `anonymized_text` |

They return `LocalScorerResult` (`{"passed", "metadata"}` with attribute
access) and work in `Evaluation`s and with `span.apply_scorer`. They serialise
with their settings (never the model), so evaluations show them in the UI, but
only the process that has the model can run them.

Judge prompts can use `{output}`, `{inputs}` (all columns as JSON) and any
column name. They run through the server's `POST /api/v1/completions`, so
`model="mock/json"` works offline. `RuleScorer` evaluates its rule against
`{"output": ..., "inputs": {...}, <columns>}`, for example:

```python
scorers.RuleScorer(rule={"$contains": {"input": {"$getField": "output"},
                                       "substr": {"$getField": "expected"}, "case_insensitive": True}})
```

`RemoteScorer` reads its headers from the server secret named
`headers_secret`. Locally it reads the environment variable of the same name,
which holds either a JSON object of headers or a bare token sent as a bearer
token.

### EvaluationLogger

When you run predictions yourself:

```python
ev = weft.EvaluationLogger(model="my-model", dataset="my-dataset")
pred = ev.log_prediction(inputs={"question": "2+2"}, output="4")
pred.log_score("correct", True)
pred.finish()
ev.log_summary()            # same span structure as Evaluation

# one-liner per example; scores as a dict
ev.log_example({"question": "3+3"}, "6", {"correct": True})
```

Without an output the predict span stays open, so work done inside the block
becomes its children (and conversation spans started there carry
`weft.eval.*` link attributes):

```python
with ev.log_prediction({"question": "2+2"}) as pred:
    pred.output = my_agent("2+2")
    pred.log_score("correct", pred.output == "4")
```

**Linking existing traces (agent evals).** Score traces that already exist
instead of re-recording them: `call=` takes a `Span`, a span id or URI, a
conversation span (`Turn`, `LLM`, ...), a `LogResult`, a `Conversation`, or a
list of these.

```python
_, call = my_agent.call("2+2")
pred = ev.log_prediction({"question": "2+2"}, call=call)   # output and latency taken from the span
pred = ev.log_prediction({"question": "hi"}, output="hello", call=turn, example_id="ex-1")
```

The links are stored on the prediction as `attributes.weft.linked_calls`,
`attributes.weft.genai_span_ref` (`[{"trace_id", "span_id"}]`, the field of
the v2 predictions API) and `attributes.weft.linked_conversation_ids`, so
`client.spans(query={"$eq": [{"$getField": "attributes.weft.genai_span_ref.0.span_id"}, {"$literal": call_id}]})`
finds the predictions of a trace.

Pass a `Dataset` or a dataset ref/URI as `dataset=` to link predictions to its
rows. When a prediction's inputs equal a row, its `example` input is that row's
ref (`.../attr/rows/id/<row digest>`), as in `Evaluation`. Comparisons and
leaderboards then line up rows across runs.

## Guardrails and online scoring

`span.apply_scorer` scores an existing span. Argument names are matched to the
span's inputs and `output` receives its output. The scorer's span is recorded as
its own trace, and `weft.scorer.<scorer>` feedback with `runnable_ref` and
`span_ref` is attached to the scored span. The result is returned, so an app
can block on it:

```python
result, span = generate.call(prompt)
check = await span.apply_scorer(toxicity, additional_scorer_kwargs={"threshold": 0.8})
if check.result["flagged"]:
    result = "Sorry, I can't help with that."
span.apply_scorer_sync(weft.scorers.RegexScorer(pattern=r"^\w"))   # sync helper
```

The result is an `ApplyScorerResult(result, score_span, feedback_id)`. Runnable
feedback can be queried, for example
`feedback.weft.scorer.toxicity.output.flagged`.

## Object versions: aliases, tags, deletion

```python
ref = weft.save(ds)
client.set_aliases(ref, "production")          # then weft.get("qa:production")
client.remove_aliases("qa", "production")
client.get_aliases(ref)
client.add_tags(ref, ["reviewed"]); client.remove_tags(ref, ["reviewed"]); client.get_tags(ref)
client.list_tags()                             # all tags in the project
client.list_aliases()                          # all aliases in the project
client.get_tags_and_aliases(ref)               # (tags, aliases) of one version
client.delete_object_version(ref)              # or "qa:v0"
client.delete_object_versions("qa", ["v1", "<digest>"])
client.delete_all_object_versions("qa")
```

The tag and alias functions also exist at module level on the
global client: `weft.add_tags`, `weft.remove_tags`, `weft.get_tags`,
`weft.set_aliases`, `weft.remove_aliases`, `weft.get_aliases`,
`weft.get_tags_and_aliases`, `weft.list_tags` and `weft.list_aliases` (they
raise `RuntimeError` before `weft.init`).

`obj_ref` arguments accept a ref, a URI, `"name:v1"`, `"name:latest"` or a
saved object. They use `/api/v1/objects/{name}/aliases` and
`/api/v1/objects/{name}/versions/{version}/tags`.

## Annotations, monitors, leaderboards and saved views

```python
spec = weft.AnnotationSpec(name="quality", field_schema={"type": "integer", "minimum": 1, "maximum": 5})
span.feedback.add_annotation(spec, 4)            # validated; feedback weft.annotation.quality

weft.save(weft.Monitor(name="judge-chats", op_names=["openai.chat.completions.create"],
                          sampling_rate=0.2, scorers=[judge]))     # server scores matching spans

weft.save(weft.Leaderboard(name="qa-board", columns=[
    weft.LeaderboardColumn(evaluation_object_ref=evaluation, scorer_name="exact",
                           summary_metric_path="true_fraction"),
]))
board = client.leaderboard("qa-board")           # POST /api/v1/leaderboards/compute

weft.save(weft.SavedView(name="errors", label="Errors",
                            definition={"filter": {"trace_roots_only": True}}))
```

All four serialise in the shapes documented in API.md and load back as their
classes.

### Saved views as queries

`SavedView` has a fluent API. Views saved here open in the UI's
Traces/Evaluations pages, and views saved in the UI load here:

```python
view = (weft.SavedView(label="Slow errors")
        .filter_op("answer")                                   # name, name:version or function ref
        .add_filter("summary.weft.status", "equals", "error")
        .add_filter("summary.weft.latency_ms", ">", 1000)
        .add_filter("started_at", "after", "2026-01-01")
        .sort_by("started_at", "desc")
        .add_column("inputs.question", "Question")
        .save())                                               # named after the label
spans = view.spans(limit=20)                                   # applied as a span query
view.to_spans_query()                                          # the raw POST /api/v1/spans/query body
weft.SavedView.load("Slow-errors"); client.get_saved_views(view_type="traces")
```

Operators: `contains`, `does not contain`, `equals`, `does not equal`, `in`,
`=`, `!=`, `<`, `<=`, `>`, `>=`, `is`, `after`, `before`, `is empty`,
`is not empty`. Also `remove_filter`, `remove_filters`, `set_filter(trace_roots_only=..., ...)`,
`search(text)`, `add_sort`, `show_column`/`hide_column`, `pin_column_left`/`_right`,
`page_size`, `ui_url()`. A view's stored filter keeps the UI's field names
(`op_names`, `thread_ids`); `spans()` sends them as Weft's (`names`,
`session_ids`). Note that Weft stores a missing error as null, so `is empty` /
`is not empty` on `error` follow empty-string semantics; filter on
`summary.weft.status` instead.

## Querying spans and feedback

```python
client = weft.init("my-project")
for span in client.spans(filter={"names": ["answer"], "trace_roots_only": True},       # POST /api/v1/spans/stream
                         query={"$gt": [{"$getField": "summary.weft.latency_ms"}, {"$literal": 1000}]},
                         sort_by=[{"field": "started_at", "direction": "desc"}],
                         limit=50):
    print(span.id, span.base_name, span.inputs, span.output, span.error, span.session_id, span.status)

span = client.span(span_id, include_costs=True)   # GET /api/v1/spans/{id}; None when there is none
span.feedback.add_reaction("👍")
span.feedback.add_note("looks right")
span.feedback.add("my.feedback", {"score": 3})
list(span.feedback)
span.set_display_name("golden example")
span.delete()                                     # or client.delete_spans([...])

span.children()
span.feedback.add("weft.annotation.quality", {"value": 4}, annotation_ref=spec_ref)

client.spans(columns=["id", "inputs.question"], expand_columns=["inputs.example"],
             include_costs=True, include_feedback=True, include_usernames=True)
df = client.spans(filter={"names": ["answer"]}, limit=1000).to_pandas()    # needs pandas
flat = client.spans(limit=100).to_pandas(flatten=True)       # inputs.x, summary.usage... columns
rows = client.spans(limit=100).to_records()                  # plain dicts, no pandas needed
client.get_feedback(query={"$eq": [{"$getField": "feedback_type"}, {"$literal": "weft.note"}]}, limit=50)
client.get_evaluations()                         # Evaluation objects
client.evaluation_runs()                         # Evaluation.evaluate spans
client.compare_evaluations([run_id_a, run_id_b])
client.evaluate_model(evaluation, llm_model)     # server-side run; polls until done
                                                 # -> {"id", "status", "summary"}
ids = client.add_cost("my-model", prompt_token_cost=1e-6, completion_token_cost=2e-6)
client.query_costs(["my-model"]); client.purge_costs(ids)
client.server_info(); client.whoami()
client.resolve_url(file_digest)                  # download URL for a file or media reference

from weft.trace_server_interface import SortBy, ThreadsQueryReq
res = client.threads_query(ThreadsQueryReq(       # sessions (POST /api/v1/spans/sessions); also threads_query_stream
    sort_by=[SortBy(field="last_updated", direction="desc")], limit=50,
    sortable_datetime_after=datetime.datetime.now() - datetime.timedelta(days=1)))
for t in res.threads:
    print(t.session_id, t.turn_count, t.span_count, t.started_at, t.last_updated, t.p50_turn_duration_ms)

client.register_custom_runtime(                  # an OpenAI-compatible endpoint
    name="my-vllm", base_url="https://vllm.internal/v1",
    runtime_ids=["llama-3-8b", weft.CustomRuntimeID(id="qwen2-7b", max_tokens=8192)],  # the complete list
    api_key_secret="VLLM_KEY", headers={"X-Team": "ml"})   # -> model ids "my-vllm/llama-3-8b", ...

weft.urls.agent_conversation_path(entity, project, conversation_id)  # UI link to a conversation
weft.urls.redirect_call(entity, project, span_id)                    # UI link to a span
```

## OpenTelemetry

Weft ingests OTLP/HTTP at `<server>/otel/v1/traces`. Spans from any
OpenTelemetry, OpenInference or OpenLLMetry instrumentation become spans
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

## Breaking changes

The trace and object API names of [DESIGN.md](../../docs/weft/DESIGN.md) §10
(spans, `weft.trace`, `weft.save`, `weft.get`, Weft digests) and the earlier
names they replace are listed in [CHANGELOG.md](CHANGELOG.md).

### 0.1.0: Weft's own names

Names that came from another tracing product were renamed, with no aliases
(the server's optional compatibility adapter, `--weave-compat`, still serves
that product's own client; see `docs/weft/COMPATIBILITY.md`). Migrate as
follows:

| Before | Now | Migration |
|---|---|---|
| `weft.set_wb_run(run_id, step)`, `client.set_wb_run(...)` | `weft.set_run(run_id, step)`, `client.set_run(...)` | rename the call |
| `weft.init(settings={"wb_run_id": ..., "wb_run_step": ...})` | `settings={"run_id": ..., "run_step": ...}` | rename the keys |
| `call.wb_user_id`, `call.wb_username` | `span.user_id`, `span.username` | rename the attribute |
| `call.wb_run_id`, `call.wb_run_step`, `call.wb_run_step_end` | `span.run_id`, `span.run_step`, `span.run_step_end` | rename the attribute |
| query fields and sort keys `wb_user_id`, `wb_run_id` | `user_id`, `run_id` | rename in `$getField` / `sort_by` |
| span filter / `SavedView.set_filter(wb_user_ids=..., wb_run_ids=...)` | `user_ids=...`, `run_ids=...` | rename the keyword |
| feedback field `weave_ref` (payloads, `$getField`) | `ref` | rename the field |
| feedback type `wandb.reaction.1` | `weft.reaction` | rename the type string |
| feedback type `wandb.note.1` | `weft.note` | rename the type string |
| feedback type `wandb.annotation.<name>` | `weft.annotation.<name>` | rename the type string |
| feedback type `wandb.runnable.<scorer>` | `weft.scorer.<scorer>` | rename the type string |

Data already stored is migrated by the server the first time it opens an old
database (columns, feedback types and saved queries), so only code that names
the old fields needs changing.

## Tests

```bash
pip install -e 'sdk/python[otel,test]'     # extras: scorers (local model scorers), pii (Presidio NER)
pytest sdk/python/tests                                   # unit tests (in-process fake server)
WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 pytest sdk/python/tests   # + live tests against a server (incl. the digest cross-check)
```

Some tests use real libraries when they are installed and are skipped
otherwise: `verdict` (chained Verdict pipelines), `websockets`,
`websocket-client`, `aiohttp` and `openai-agents` (Realtime over a local
websocket server). They are not in the `test` extra because `verdict` pulls
in litellm, scikit-learn and datasets.

Examples live in `examples/weft/python/`.
