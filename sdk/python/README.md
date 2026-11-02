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
| Redaction | `settings={"redact_keys": [...], "redact_pii": True}` | `WEFT_REDACT_KEYS=0` disables, `WEFT_REDACT_PII=1` | credential keys |
| PII entities and engine | `settings={"redact_pii_fields": [...], "redact_pii_exclude_fields": [...], "redact_pii_engine": "auto"}` | `WEFT_REDACT_PII_FIELDS`, `WEFT_REDACT_PII_EXCLUDE_FIELDS`, `WEFT_REDACT_PII_ENGINE` | Weave's entity list; Presidio if installed |
| Integrations | `autopatch=False`, `autopatch_settings={...}` | `WEFT_AUTOPATCH=0`, `WEFT_DISABLED_INTEGRATIONS=a,b`, `WEFT_AUTOPATCH_<NAME>=0` | all on |
| Attributes on every call | `settings={"global_attributes": {...}}` | | none |
| Print a link per root call | `settings={"print_call_link": False}` | `WEFT_PRINT_CALL_LINK` | on |
| Store op source code | `settings={"capture_code": False}` | `WEFT_CAPTURE_CODE` | on |
| On-disk write-ahead log | `settings={"enable_disk_fallback": True, "wal_dir": ...}` | `WEFT_ENABLE_WAL`, `WEFT_WAL_DIR` | off (`~/.weft/wal`) |
| Send queue size | `settings={"max_queue_size": 10000}` | | 10000 |
| Retries | `settings={"retry_max_attempts": 3, "retry_backoff": 0.25, "retry_max_backoff": 4.0}` | | as shown |
| Cache of immutable server reads | `settings={"use_server_cache": False, "server_cache_dir": ..., "server_cache_size_limit": 10**9}` | `WEFT_USE_SERVER_CACHE`, `WEFT_SERVER_CACHE_DIR`, `WEFT_SERVER_CACHE_SIZE_LIMIT` | on, `~/.cache/weft`, 1 GB |

Root calls print `weft: <server>/<entity>/<project>/calls/<id>` to stderr
(`call.ui_url` gives the same link).

**Durability.** With `enable_disk_fallback`, anything that cannot be sent goes
to a JSONL file instead of being dropped. That covers batches that still fail
after retries, items that overflow the queue, and whatever is still queued at
exit. The next `weft.init()` against the same server and project replays it.

**Server-response cache.** Like Weave's caching middleware, reads that can
never change are kept in memory and in an on-disk SQLite file
(`~/.cache/weft/server_cache.sqlite`) and reused across processes:
`/obj/read` by digest (not `latest`, `v3` or an alias), `/table/query` and
`/table/query_stats` by table digest, `/file/content` and object/table refs in
`/refs/read_batch`. Deleting an object through the client drops its cached
reads. `client.cache_stats()` gives hits/misses/skips and
`client.clear_server_cache()` empties it.

A bare project name `"p"` is stored as `"default/p"`. When the server uses
`user=key` API keys, calls, objects and feedback are attributed to that user
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
    "redact_pii_fields": ["PERSON", "LOCATION", "ORGANIZATION", "EMAIL_ADDRESS"],  # default: Weave's list + ORGANIZATION
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
    kind="retriever",                      # agent|llm|tool|chain|retriever|embedding|scorer|guardrail|...
    color="blue",                          # red|orange|yellow|green|blue|purple
    enable_code_capture=False,             # don't store this op's source
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

with weft.tracing_disabled():              # run without recording anything
    answer("off the record")
```

`kind` and `color` are stored on every call as `attributes.weft.span_kind` and
`attributes.weft.color`. They are mirrored to `attributes.weave.kind` and
`.color` for Weave tooling. The integrations below tag LLM calls with
`kind="llm"`, and built-in scorers use `kind="scorer"`.

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

Any of these can be returned from or passed to an op, or stored in objects and
dataset rows. `weft.Image` and `weft.Audio` wrap image and audio files the same way.

**Bring your own bucket.** Media that already lives in your storage is logged
by reference, without uploading:

```python
weft.Image.from_uri("s3://my-bucket/photos/cat.png")     # {"_type": "Image", "uri", "mime_type", "filename"}
weft.Content.from_uri("https://example.com/report.pdf", size=1234)
weft.Video.from_uri("s3://my-bucket/clip.mp4"); weft.PDF.from_uri("weft-file://<digest>")
client.resolve_url("s3://my-bucket/photos/cat.png")       # /files/resolve_url: presigned for the server's S3 backend
client.resolve_url([content, file_digest], expires_in=600)  # Content values, digests, weft-file:// and https:// URIs
```

Reading a reference (`.data`, `.as_text()`, `.save()`) downloads it, through
`resolve_url` for non-HTTP URIs.

## Agents: conversations, turns, LLM calls and tools

The same API as Weave's conversation SDK. Each turn, LLM call, tool and
sub-agent is recorded as a call the server's agents index understands, so
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
            tool.result = lookup(7)            # @weft.op calls inside become children
        with turn.start_subagent(name="writer") as sub:
            with sub.start_llm(model="gpt-4o") as w:
                w.output("Your order has shipped.")
        turn.output_messages = [weft.Message.assistant("Order 7 has shipped.")]
```

- Module functions use the current conversation/turn: `weft.start_turn`,
  `weft.start_llm`, `weft.start_tool`, `weft.start_subagent`, `weft.end_turn`,
  `weft.get_current_turn()`, ... Spans also work without `with`
  (`span.start()` / `span.end()`).
- A turn is an `invoke_agent` call (`attributes.gen_ai.agent.name`,
  `attributes.weft.span_kind="agent"`) and the root of its own trace; the
  conversation id is its `thread_id` and the turn id its own id. Pass
  `continue_parent_trace=True` to nest turns under the current call. LLM calls
  are `chat` calls with `inputs.messages`, an OpenAI-style output and
  `summary.usage` (so costs work); tools are `execute_tool` calls.
- `llm.think(...)`, `llm.attach_media(content=..., mime_type=...)` (uploaded),
  `span.set_attributes({...})`, `span.record_error(exc)`, and
  `start_conversation(include_content=False, attributes={...})` behave as in Weave.
- Spans are sent when they end. `turn.call_id` / `turn.trace_id` / `turn.call`
  give the recorded call.

**Batch logging** of finished spans (queue workers, replays):

```python
weft.log_turn(conversation_id="c-1", agent_name="support",
              messages=[weft.Message.user("hi")], output_messages=[weft.Message.assistant("hello")],
              spans=[weft.LLM(model="gpt-4o-mini", started_at=t0, ended_at=t1, usage=weft.Usage(input_tokens=5))])
res = weft.log_conversation(turns=[weft.Turn(messages=[...], spans=[weft.Tool(name="t", result=1)])],
                            agent_name="support")
res.conversation_id, res.root_span_ids, res.trace_ids
```

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
| `openai_realtime` | openai | Realtime connections (`client.realtime.connect`, `client.beta.realtime.connect`, sync/async) | `openai.realtime.session`, `.response`, `.tool.<name>` |
| `anthropic` | anthropic | `messages.create` (sync/async/`stream=True`), `messages.stream()` | `anthropic.messages.create`, `anthropic.messages.stream` |
| `litellm` | litellm | `completion`, `acompletion` (incl. stream) | `litellm.completion`, `litellm.acompletion` |
| `google_genai` | google-genai | `models.generate_content(_stream)`, sync and `client.aio` | `google.genai.models.generate_content(_stream)` |
| `vertexai` | google-cloud-aiplatform | `GenerativeModel.generate_content(_async)` (incl. `stream=True`), `ChatSession.send_message(_async)`, `ImageGenerationModel.generate_images` | `vertexai.GenerativeModel.generate_content`, ... |
| `bedrock` | boto3 | `converse`, `converse_stream`, `invoke_model`, `invoke_model_with_response_stream` | `bedrock.converse`, ... |
| `mistral` | mistralai | `chat.complete`, `chat.stream` (+ `_async`) | `mistralai.chat.complete`, `mistralai.chat.stream` |
| `cohere` | cohere | `ClientV2`/`Client` `chat`, `chat_stream` (sync/async) | `cohere.chat`, `cohere.chat_stream` |
| `groq`, `together`, `cerebras` | groq, together, cerebras-cloud-sdk | `chat.completions.create` (OpenAI-shaped) | `groq.chat.completions.create`, `together...`, `cerebras...` |
| `huggingface` | huggingface_hub | `InferenceClient`/`AsyncInferenceClient` `chat_completion` (incl. `client.chat.completions.create`, streams), `text_generation`, and task methods (`summarization`, `translation`, `text_to_image`, ...) | `huggingface_hub.InferenceClient.chat_completion`, ... |

**Agent and RAG frameworks.**

| Settings key | Library | Traced (span kind) | Op name(s) |
|---|---|---|---|
| `langchain` | langchain-core, LangGraph | every chain, chat model/LLM, tool and retriever run; LangGraph graphs (agent) and nodes, with `attributes.langgraph` (node, step, triggers) | `langchain.<Type>.<name>`, `langgraph.graph`, `langgraph.node.<node>` |
| `llamaindex` | llama-index-core | every instrumented span (query engines, retrievers, LLMs, embeddings, agents, workflows, tools) via `WeftSpanHandler` | `llama_index.<Class>.<method>` |
| `openai_agents` | openai-agents | traces and agent/function/generation/response/handoff/guardrail spans | `openai_agents.trace`, `agent.<name>`, `tool.<name>`, ... |
| `dspy` | dspy | modules, LM calls, tools | `dspy.<Module>`, `dspy.LM`, `dspy.Tool.<name>` |
| `crewai` | crewai | `Crew.kickoff*` (agent), `Agent.execute_task` (agent), `Task.execute_*` (chain), `LLM.call` (llm), tools, `Flow.kickoff*` (workflow) | `crewai.Crew.kickoff`, `crewai.Agent.execute_task`, ... |
| `smolagents` | smolagents | agent `run` (agent) and `step`, every `Model.generate` (llm), `Tool.__call__` (tool) | `smolagents.Agent.run`, `smolagents.<Model>.generate`, `smolagents.Tool` |
| `instructor` | instructor | `create`, `create_with_completion`, `create_partial`, `create_iterable` (sync/async; llm) | `instructor.Instructor.create`, ... |
| `mcp` | mcp, fastmcp | client `call_tool` (tool), `read_resource`, `get_prompt`, `list_*`; server (`FastMCP`) `call_tool`, `read_resource`, `get_prompt` | `mcp.ClientSession.call_tool`, `mcp.FastMCP.call_tool`, `fastmcp.Client.call_tool`, ... |
| `claude_agent_sdk` | claude-agent-sdk | `query()` (agent) and `ClaudeSDKClient` turns (agent, one thread per client), each assistant message (llm), each tool use (tool; `Task` sub-agents nest their messages) | `claude_agent_sdk.query`, `.turn`, `.llm`, `.tool.<name>` |
| `haystack` | haystack-ai | pipelines (chain) and components (`llm`, `retriever`, `embedding`, `reranker`, `tool`, `agent`) via `WeftHaystackTracer` | `haystack.pipeline.run`, `haystack.<ComponentType>` |
| `autogen` | autogen-agentchat / -core / -ext | agents and teams `run`/`run_stream`/`on_messages` (agent), model clients `create`/`create_stream` (llm), tools, memory, code executors | `autogen.Agent.run`, `autogen.ChatCompletionClient.create`, ... |
| `agno` | agno | `Agent`/`Team` `run`/`arun` (agent), `Workflow` (workflow), `Model.response` (llm), tool calls | `agno.Agent.run`, `agno.Model.response`, ... |
| `google_adk` | google-adk | `Runner.run(_async)`, agents, tools, LLM calls | `google_adk.Runner.run`, `google_adk.Agent.run`, ... |
| `pydantic_ai` | pydantic-ai | `Agent.run` (agent), tool calls, `Model.request` (llm) | `pydantic_ai.Agent.run`, ... |
| `notdiamond`, `verifiers`, `gepa` | | model routing, RL environments/rubrics, `gepa.optimize` | see `weft/integrations/frameworks.py` |

Each LLM request becomes a call (`kind="llm"`):
- **Inputs** are the request kwargs.
- **Output** is the response; streams are accumulated into a single response.
- **Usage** is recorded as
  `summary.usage = {model: {requests, prompt_tokens, completion_tokens, total_tokens}}`.
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
  framework's call is the parent, so provider calls and your own ops nest
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
audio transcripts, function-call outputs) plus instructions and voice, and
whose output holds the response items, the streamed text/transcript and the
audio size (audio bytes are never stored). Usage includes cached and audio
token details. Function calls are tool calls that end when you send their
`function_call_output`. For a raw websocket client, call
`RealtimeTracer().on_client_event(e)` / `.on_server_event(e)` / `.close()`.

**MCP.** Set `WEFT_MCP_TRACE_LIST_OPERATIONS=false` to skip the `list_*`
methods.

### Per-integration settings

`autopatch_settings` mirrors Weave's: a dict (or `AutopatchSettings`) keyed by
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
the same digest, and the server keeps one version.

**Loading ops runs stored code.** `weft.ref("my_fn").get()` returns a runnable
`weft.Op`. The SDK rebuilds it from the stored source by `exec`-ing it in a
fresh namespace, strips its decorators and wraps it with `@weft.op` again. Its
calls are linked to the same op ref. A published `Model`/`Scorer` subclass
that is not defined in the loading process comes back as a rebuilt class whose
op-ref methods are loaded the same way, so `loaded_model.predict(...)` works.
Only self-contained functions can be rebuilt; anything they reference from
their original module (imports, helpers) has to exist at call time. When the
code can't be rebuilt you get `weft.OpLoadError`. **Only load ops from servers
and projects you trust.** Nested objects are published
first and stored as refs. Objects whose class is not defined locally load as
`weft.WeftObject`, a dict that also supports attribute access.

## Datasets and models

```python
ds = weft.Dataset(name="qa", rows=[{"question": "2+2", "expected": "4"}])
weft.publish(ds)                     # rows uploaded as a table
for row in weft.ref("qa").get().rows:
    ...
ds2 = ds.add_rows([{"question": "3+3", "expected": "6"}])   # /table/update + new version
weft.Dataset.from_calls(client.get_calls(filter={"op_names": ["answer"]}))
ds.select([0, 2])                    # new dataset with those rows
weft.Dataset.from_pandas(df); ds.to_pandas()                  # needs pandas
weft.Dataset.from_hf(hf_dataset, split="train"); ds.to_hf()   # needs `pip install datasets`

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
| `EmbeddingSimilarityScorer` | `target_column`, `model` | cosine similarity (locally needs a server `/embeddings/create`) |
| `RuleScorer` | `rule` (query-language operation), `threshold` | the rule's value; with `threshold`, numbers score `value >= threshold` |
| `RemoteScorer` | `url`, `headers_secret`, `timeout` | JSON response of `POST url {"output", "inputs"}` |
| `PydanticScorer` (local only) | `model` (a pydantic class) | bool (output parses) |

### Local model scorers

Weave's local small-model scorers run in your process with
`pip install 'weft[scorers]'` (transformers, torch, presidio). The base
install does not pull these in; a missing dependency raises an `ImportError`
naming the extra. Models load on first use (or `scorer.load()`) from
`model_name_or_path` (a Hugging Face id or local path; by default Weave's
`wandb/Weave*ScorerV1` models), on `device` (`"cpu"`, `"cuda"`, `"mps"`).

| Scorer (`weft.scorers` / `weft.local_scorers`) | `score(...)` | `metadata` (+ `passed`) |
|---|---|---|
| `WeaveToxicityScorerV1` | `output` | five category levels; fails at `total_threshold`/`category_threshold` |
| `WeaveBiasScorerV1` | `output` | `gender_bias(_score)`, `racial_bias(_score)` |
| `WeaveCoherenceScorerV1` | `query`, `output` (`chat_history` or `context`) | `coherence_label`, `coherence_id`, `score` |
| `WeaveFluencyScorerV1` | `output` | `score` (fluent probability) |
| `WeaveHallucinationScorerV1` | `query`, `context`, `output` | `score` (higher = more hallucinated) |
| `WeaveContextRelevanceScorerV1` | `query`, `output` (the context) | `score`, `all_spans` |
| `WeaveTrustScorerV1` | `query`, `context`, `output` | `trust_level`, critical and advisory issues, per-scorer scores |
| `PresidioScorer` (guardrail) | `output` | `detected_entities`, `reason`, `anonymized_text` |

They return `WeaveScorerResult` (`{"passed", "metadata"}` with attribute
access) and work in `Evaluation`s and with `call.apply_scorer`. They serialise
with their settings (never the model), so evaluations show them in the UI, but
only the process that has the model can run them.

Judge prompts can use `{output}`, `{inputs}` (all columns as JSON) and any
column name. They run through the server's `/completions/create`, so
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
ev.log_summary()            # same call structure as Evaluation

# one-liner per example; scores as a dict
ev.log_example({"question": "3+3"}, "6", {"correct": True})
```

Without an output the predict call stays open, so work done inside the block
becomes its children (and conversation spans started there carry
`weave.eval.*` link attributes):

```python
with ev.log_prediction({"question": "2+2"}) as pred:
    pred.output = my_agent("2+2")
    pred.log_score("correct", pred.output == "4")
```

**Linking existing traces (agent evals).** Score traces that already exist
instead of re-recording them: `call=` takes a `Call`, a call id or URI, a
conversation span (`Turn`, `LLM`, ...), a `LogResult`, a `Conversation`, or a
list of these.

```python
_, call = my_agent.call("2+2")
pred = ev.log_prediction({"question": "2+2"}, call=call)   # output and latency taken from the call
pred = ev.log_prediction({"question": "hi"}, output="hello", call=turn, example_id="ex-1")
```

The links are stored on the prediction as `attributes.weft.linked_calls`,
`attributes.weft.genai_span_ref` (`[{"trace_id", "span_id"}]`, the field of
Weave's v2 predictions API) and `attributes.weft.linked_conversation_ids`, so
`client.get_calls(query={"$eq": [{"$getField": "attributes.weft.genai_span_ref.0.span_id"}, {"$literal": call_id}]})`
finds the predictions of a trace.

Pass a `Dataset` or a dataset ref/URI as `dataset=` to link predictions to its
rows. When a prediction's inputs equal a row, its `example` input is that row's
ref (`.../attr/rows/id/<row digest>`), as in `Evaluation`. Comparisons and
leaderboards then line up rows across runs.

## Guardrails and online scoring

`call.apply_scorer` scores an existing call. Argument names are matched to the
call's inputs and `output` receives its output. The scorer call is recorded as
its own trace, and `wandb.runnable.<scorer>` feedback with `runnable_ref` and
`call_ref` is attached to the scored call. The result is returned, so an app
can block on it:

```python
result, call = generate.call(prompt)
check = await call.apply_scorer(toxicity, additional_scorer_kwargs={"threshold": 0.8})
if check.result["flagged"]:
    result = "Sorry, I can't help with that."
call.apply_scorer_sync(weft.scorers.RegexScorer(pattern=r"^\w"))   # sync helper
```

The result is an `ApplyScorerResult(result, score_call, feedback_id)`. Runnable
feedback can be queried, for example
`feedback.wandb.runnable.toxicity.output.flagged`.

## Object versions: aliases, tags, deletion

```python
ref = weft.publish(ds)
client.set_aliases(ref, "production")          # then weft.ref("qa:production").get()
client.remove_aliases("qa", "production")
client.get_aliases(ref)
client.add_tags(ref, ["reviewed"]); client.remove_tags(ref, ["reviewed"]); client.get_tags(ref)
client.list_tags()                             # all tags in the project
client.delete_object_version(ref)              # or "qa:v0"
client.delete_object_versions("qa", ["v1", "<digest>"])
client.delete_all_object_versions("qa")
```

`obj_ref` arguments accept a ref, a URI, `"name:v1"`, `"name:latest"` or a
published object. Aliases use Weave's `/objs/{id}/aliases` routes and fall
back to `/obj/alias/set` on older servers.

## Annotations, monitors, leaderboards and saved views

```python
spec = weft.AnnotationSpec(name="quality", field_schema={"type": "integer", "minimum": 1, "maximum": 5})
call.feedback.add_annotation(spec, 4)            # validated; feedback wandb.annotation.quality

weft.publish(weft.Monitor(name="judge-chats", op_names=["openai.chat.completions.create"],
                          sampling_rate=0.2, scorers=[judge]))     # server scores matching calls

weft.publish(weft.Leaderboard(name="qa-board", columns=[
    weft.LeaderboardColumn(evaluation_object_ref=evaluation, scorer_name="exact",
                           summary_metric_path="true_fraction"),
]))
board = client.leaderboard("qa-board")           # /leaderboard/compute

weft.publish(weft.SavedView(name="errors", label="Errors",
                            definition={"filter": {"trace_roots_only": True}}))
```

All four serialise in the shapes documented in API.md and load back as their
classes.

### Saved views as queries

`SavedView` has Weave's fluent API. Views saved here open in the UI's
Traces/Evaluations pages, and views saved in the UI load here:

```python
view = (weft.SavedView(label="Slow errors")
        .filter_op("answer")                                   # name, name:version or op ref
        .add_filter("summary.weave.status", "equals", "error")
        .add_filter("summary.weave.latency_ms", ">", 1000)
        .add_filter("started_at", "after", "2026-01-01")
        .sort_by("started_at", "desc")
        .add_column("inputs.question", "Question")
        .save())                                               # named after the label
calls = view.get_calls(limit=20)                               # applied as a calls query
view.to_calls_query()                                          # the raw /calls/query body
weft.SavedView.load("Slow-errors"); client.get_saved_views(view_type="traces")
```

Operators: `contains`, `does not contain`, `equals`, `does not equal`, `in`,
`=`, `!=`, `<`, `<=`, `>`, `>=`, `is`, `after`, `before`, `is empty`,
`is not empty`. Also `remove_filter`, `remove_filters`, `set_filter(trace_roots_only=..., ...)`,
`search(text)`, `add_sort`, `show_column`/`hide_column`, `pin_column_left`/`_right`,
`page_size`, `ui_url()`. Note that Weft stores a missing exception as null,
so `is empty` / `is not empty` on `exception` follow Weave's empty-string
semantics; filter on `summary.weave.status` instead.

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

call.children()
call.feedback.add("wandb.annotation.quality", {"value": 4}, annotation_ref=spec_ref)

client.get_calls(columns=["id", "inputs.question"], expand_columns=["inputs.example"],
                 include_costs=True, include_feedback=True)
client.get_feedback(query={"$eq": [{"$getField": "feedback_type"}, {"$literal": "wandb.note.1"}]}, limit=50)
client.get_evaluations()                         # Evaluation objects
client.compare_evaluations([eval_call_id_a, eval_call_id_b])
client.evaluate_model(evaluation, llm_model)     # server-side run; polls until done
                                                 # -> {"call_id", "status", "summary"}
ids = client.add_cost("my-model", prompt_token_cost=1e-6, completion_token_cost=2e-6)
client.query_costs(["my-model"]); client.purge_costs(ids)
client.server_info(); client.whoami()
client.resolve_url(file_digest)                  # download URL for a file or media reference
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
pip install -e 'sdk/python[otel,test]'     # extras: scorers (local model scorers), pii (Presidio NER)
pytest sdk/python/tests                                   # unit tests (in-process fake server)
WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 pytest sdk/python/tests/test_integration_server.py
```

Examples live in `examples/weft/python/`.
