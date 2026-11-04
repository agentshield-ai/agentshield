# weft-sdk — TypeScript SDK

TypeScript/JavaScript client for **Weft**, an OpenTelemetry-native LLM tracing
and evaluation server. ESM with type declarations, Node >= 18, and no runtime
dependencies (it uses the global `fetch` and `AsyncLocalStorage`). It speaks
Weft's own API (`/api/v1`, [API.md](../../docs/weft/API.md)): traced functions
record **spans**, versioned values are **objects**. Earlier SDK names still
work for one release; see [CHANGELOG.md](CHANGELOG.md).

```bash
cd sdk/typescript && npm install && npm run build   # from this repository
# in an app: npm install weft-sdk
```

## Quickstart

```ts
import * as weft from 'weft-sdk';

const client = await weft.init('my-project', { serverUrl: 'http://localhost:6345', apiKey: process.env.WEFT_API_KEY });

const retrieve = weft.trace(async function retrieve(query: string) {
  return ['doc one', 'doc two'];
});

const answer = weft.trace(async function answer(question: string) {
  const docs = await retrieve(question);       // child span
  return `${docs.length} docs for ${question}`;
});

await answer('what is weft?');
const ref = await weft.save(new weft.StringPrompt({ content: 'Answer {question} briefly.' }), 'answer-prompt');
const prompt = await weft.get(ref);            // or weft.get('answer-prompt'), 'answer-prompt:v0'
const [span] = await client.spans({ filter: { names: ['answer'] }, limit: 1 });   // span.name, span.error, span.sessionId
await weft.finish();                           // flush (also attempted on process beforeExit)
```

Environment variables: `WEFT_SERVER_URL` (default `http://localhost:6345`),
`WEFT_API_KEY`, `WEFT_PROJECT`, `WEFT_DISABLED=true`, `WEFT_PARALLELISM`
(evaluation concurrency, default 10), and `WEFT_REDACT_KEYS=0` (turns off key
redaction).

### Redaction

By default, values under keys named `api_key`, `apikey`, `authorization`,
`auth`, `password`, `secret`, `token` or `access_token` are replaced with
`"REDACTED"` in span inputs and outputs. Matching is case-insensitive and on
the exact key, at any depth.

```ts
await weft.init('my-project', { settings: { redactKeys: ['api_key', 'ssn'], redactPii: true } });
```

`redactPii` (or `WEFT_REDACT_PII=true`) also masks emails, phone, card and SSN
numbers, and IP addresses inside strings with `<ENTITY_TYPE>` placeholders.

**NER (names, locations, organisations, ...).** Plug in an analyzer, as the
Python SDK does with Presidio. It runs in the background sender just before a
call leaves the process, so it can be async (a remote service) without
slowing your code:

```ts
// a Presidio Analyzer REST service (e.g. docker run -p 5002:3000 mcr.microsoft.com/presidio-analyzer)
await weft.init('my-project', { settings: { redactPii: true, redactPiiEngine: 'presidio', redactPiiPresidioUrl: 'http://localhost:5002' } });

// or any function returning spans: { entity_type, start, end, score? } or [entity, start, end, score?]
await weft.init('my-project', {
  settings: { redactPii: true, redactPiiAnalyzer: async (text, entities) => myNer(text), redactPiiScoreThreshold: 0.5 },
});
// "Alice Smith from Acme in Berlin" -> "<PERSON> from <ORGANIZATION> in <LOCATION>"
```

| Setting | Environment | Default |
|---|---|---|
| `redactPiiEngine`: `regex`, `presidio`, `auto` | `WEFT_REDACT_PII_ENGINE` | `auto` (Presidio when a URL is set, else regex) |
| `redactPiiPresidioUrl` | `WEFT_PRESIDIO_ANALYZER_URL` | none |
| `redactPiiFields`, `redactPiiExcludeFields` | `WEFT_REDACT_PII_FIELDS`, `WEFT_REDACT_PII_EXCLUDE_FIELDS` (comma-separated) | `weft.DEFAULT_PII_ENTITIES` (same list as Python) |
| `redactPiiScoreThreshold`, `redactPiiLanguage` | | 0, `en` |

Regex entities are always applied when the call is recorded; the analyzer
handles the rest (Presidio is asked only for the entities regex does not
cover; a custom analyzer gets all of them). If the analyzer fails, a warning is
logged once, regex redaction stands and NER is skipped for that client. The
locally visible `call.inputs`/`call.output` keep the regex-only version; what
is sent and stored is fully redacted. The server can also apply NER
(sensitive-data settings).

### Settings

```ts
await weft.init('my-project', {
  settings: {
    globalAttributes: { app: 'support-bot' },   // added to every call
    printCallLink: true,                         // "weft: <server>/<entity>/<project>/spans/<id>" per root span (WEFT_PRINT_CALL_LINK)
    captureCode: true,                           // store traced functions' source (WEFT_CAPTURE_CODE)
    captureReasoning: true,                      // model reasoning: thinking, summaries, weft.reasoning() (WEFT_CAPTURE_REASONING)
    enableDiskFallback: true,                    // on-disk write-ahead log (WEFT_ENABLE_WAL, dir: walDir / WEFT_WAL_DIR)
    disableWalSender: false,                     // with the WAL on: write everything to it, send nothing (WEFT_DISABLE_WAL_SENDER)
    maxQueueSize: 10_000, batchSize: 100,      // WEFT_MAX_CALLS_QUEUE_SIZE (0 = unbounded)
    retryMaxAttempts: 3,                       // WEFT_RETRY_MAX_ATTEMPTS
    retryBackoffMs: 250, retryMaxBackoffMs: 4000,   // WEFT_RETRY_BACKOFF, WEFT_RETRY_MAX_INTERVAL (seconds)
    httpTimeoutMs: 30_000,                     // WEFT_HTTP_TIMEOUT (seconds)
    logLevel: 'warning',                       // debug, info, warning, error, critical (WEFT_LOG_LEVEL)
    runId: 'run-abc', runStep: () => step, // run linkage (also client.setRun / weft.setRun)
  },
});
```

The environment variables are the Python SDK's (`WEFT_*`, seconds); explicit
settings and `sender` options win over them.
`weft.setLogLevel()` changes the level later; the SDK's messages go through
`weft.logger` (to `console`). With `runId`/`setRun(runId, step)` calls
carry `run_id` (`<entity>/<project>/<run id>`), `run_step` and
`run_step_end`, linking them to a training or experiment run.

With `enableDiskFallback`, anything that cannot be sent goes to a JSONL file
instead of being dropped. That covers batches that still fail after retries,
queue overflow, and what is still queued when the process exits. The next
`init()` for the same server and project replays it. With `disableWalSender` /
`WEFT_DISABLE_WAL_SENDER` and the WAL on, every write goes to the file and
nothing is sent or replayed until a run without it. `WEFT_DEBUG_HTTP=1`
prints every request and response (method, URL,
headers with `Authorization` redacted, JSON bodies, status, elapsed time) to
stderr.

## Tracing

- `weft.trace(fn, { name, kind, color, tracingSampleRate, enableCodeCapture, callDisplayName, parameterNames, postprocessInputs, postprocessOutput, accumulator })`
  traces sync and async functions, generators, async generators and functions
  returning async iterables. Streamed items are accumulated into the output (an
  array by default).
- Input names come from the function's parameter list. A single destructured
  object parameter (`async ({ datasetRow }) => ...`) records that object's
  keys. Override with `parameterNames`.
- `kind` (`agent`, `llm`, `tool`, `chain`, `retriever`, `embedding`,
  `scorer`, ...) and `color` are stored as `attributes.weft.span_kind` and
  `attributes.weft.color`.
  `tracingSampleRate` samples root spans, and a root's children follow it.
  `weft.withTracingDisabled(() => ...)` runs code without recording anything.
- `weft.trace(this, fn)` traces a method and records the instance as `self` (its
  ref when it has been saved).
- `await traced.call(...args)` resolves to `[result, span]`. Errors are captured
  on the span instead of being thrown.
- Errors are recorded as the span's `error` (`"<Name>: <message>"` plus the stack), then rethrown.
- Parent/child tracking uses `AsyncLocalStorage`, so it follows `await`,
  `Promise.all` and timers.
- `weft.attributes({ env: 'prod' }, () => ...)` attaches attributes to spans
  started inside the callback. `weft.withThread('conversation-id', () => ...)`
  sets the spans' `session_id`, and spans started directly inside are turns.
- `weft.getCurrentSpan()` returns the current span, and
  `weft.addUsage(span, model, promptTokens, completionTokens)` records the
  span's own LLM usage. The server adds up descendants.
- Spans are batched to `POST /api/v1/spans/batch` by a background sender with a
  bounded queue, retries and backoff. If the server is down you get a warning,
  never a crash. Each traced function is saved as a versioned `function`
  object; its spans are named by its ref, whose digest (lowercase hex SHA-256
  of canonical JSON, [DESIGN.md](../../docs/weft/DESIGN.md) §10.3) the SDK
  computes locally.

## LLM clients

```ts
import OpenAI from 'openai';
import Anthropic from '@anthropic-ai/sdk';

const openai = weft.wrapOpenAI(new OpenAI());          // chat.completions.create, responses.create
const groq = weft.wrapOpenAI(groqClient, { name: 'groq' });
const anthropic = weft.wrapAnthropic(new Anthropic()); // messages.create, messages.stream
const ai = weft.wrapGoogleGenAI(new GoogleGenAI({}));  // models.generateContent(Stream)
const together = weft.wrapOpenAICompatible(new OpenAI({ baseURL: 'https://api.together.xyz/v1' }), { name: 'together' });

// Vercel AI SDK
import { wrapLanguageModel } from 'ai';
const model = wrapLanguageModel({ model: openai('gpt-4o-mini'), middleware: weft.weftMiddleware() });
```

Each request becomes a call with `kind: 'llm'` (for example
`openai.chat.completions.create`, `google.genai.models.generate_content`,
`vercel_ai.generate` or `vercel_ai.stream`). `wrapOpenAICompatible` does not
add `stream_options`; Groq's `x_groq.usage` is picked up automatically.
The request body is recorded as inputs and the response as output. Streams are
accumulated into one response. `summary.usage` is
`{model: {requests, prompt_tokens, completion_tokens, total_tokens}}`, plus
`cache_read_input_tokens` / `cache_creation_input_tokens` as parts of a gross
`prompt_tokens` (the server's pricing; Anthropic and Bedrock cache counts are added
to their input count, OpenAI `*_tokens_details.cached_tokens` and Gemini
`cachedContentTokenCount` are already in it) and `reasoning_tokens`. Streamed
OpenAI chat requests get `stream_options: { include_usage: true }` unless you
set `stream_options` yourself. The wrapped methods return plain promises, so
the OpenAI-specific `APIPromise` helpers such as `.withResponse()` are not
available on wrapped calls.

More providers (each request is an `llm` call with usage; streams are accumulated):

```ts
weft.wrapGroq(new Groq()); weft.wrapTogether(new Together()); weft.wrapCerebras(new Cerebras());   // OpenAI-compatible
weft.wrapMistral(new Mistral({ apiKey }));          // chat/fim/agents .complete and .stream, embeddings.create
weft.wrapCohere(new CohereClientV2({ token }));     // chat, chatStream (v2 and v1), embed, rerank
weft.wrapBedrock(new BedrockRuntimeClient({}));     // send(Converse | ConverseStream | InvokeModel | InvokeModelWithResponseStream)
weft.instrumentBedrock(BedrockRuntimeClient);       // or patch every client
weft.wrapBedrockAgent(new BedrockAgentRuntimeClient({}));   // send(InvokeAgent | InvokeInlineAgent), see below
weft.instrumentBedrockAgent(BedrockAgentRuntimeClient);
const rt = weft.wrapOpenAIRealtime(new OpenAIRealtimeWS({ model: 'gpt-realtime' }));   // see below
await weft.patchRealtimeSession();                  // every RealtimeSession of @openai/agents-realtime, see below
weft.wrapTypeSafe(new TypeSafeClient());            // @typesafe-ai/sdk systemOne; or patchTypeSafeModule(await import('@typesafe-ai/sdk'))
const vertex = weft.wrapVertexAI(new VertexAI({ project, location }));   // models from getGenerativeModel: generateContent(Stream), chats
```

**AWS Bedrock Agents.** `InvokeAgentCommand` and `InvokeInlineAgentCommand`
become an `invoke_agent` call (`bedrock_agent.invoke_agent`), a turn of the
conversation `sessionId`. While you iterate `response.completion`, the trace
events (`enableTrace: true`) become child calls, as in the Python SDK:
`bedrock_agent.model_invocation` (llm: prompt, raw/parsed response, usage per
foundation model), `bedrock_agent.action_group` and `.code_interpreter`
(tool), `.knowledge_base` (retriever), `.guardrail`, `.return_control`, and
`.collaborator` (agent; the collaborator's own trace nests under it). A
`failureTrace` fails the open steps and the agent call. For events you get
some other way, use `new weft.BedrockAgentTraceProcessor(...)`.

**OpenAI Realtime.** `wrapOpenAIRealtime(rt)` (or
`instrumentOpenAIRealtime(OpenAIRealtimeWS, OpenAIRealtimeWebSocket)`) mirrors
the Python integration: the connection is an `openai.realtime.session` (agent)
call; every `response.created` → `response.done` is an
`openai.realtime.response` (llm) call whose inputs are the items added since
the previous response (text, input audio transcripts, function outputs) plus
instructions and voice, with usage including cached and audio tokens; function
calls are `openai.realtime.tool.<name>` (tool) calls that end at your
`function_call_output`. The model comes from the connection URL.

Audio is saved as WAV files (`weft.Audio`): the user's audio
(`input_audio_buffer.append`, cut per item at the server-VAD speech markers or
at each commit) on the item's `input_audio` part, and the model's audio
(`response.output_audio.delta`) on the response's `output_audio` part. PCM16
at the session's rate and G.711 u-law/A-law are written. Each clip is capped
at `WEFT_REALTIME_MAX_AUDIO_BYTES` (default 25 MiB; the server takes files up
to 64 MiB) and marked `audio_truncated` past it; `0` records sizes only.

The OpenAI Agents SDK's `RealtimeSession` (`@openai/agents-realtime`) is
traced by `patchRealtimeSession()`: it imports the package (or takes it:
`patchRealtimeSession(await import('@openai/agents-realtime'))`) and patches
`RealtimeSession.prototype.connect`, so every session, including ones created
earlier, is traced from its `connect()`. Without an argument it looks for the
package from the SDK's own location and from the application's (the working
directory and the entry script's directory, walking up `node_modules` and
following the package's `exports`), so it also works when the SDK is linked or
installed elsewhere, and it patches both the ESM build (what `import` loads)
and the CommonJS build (what `require` loads). It resolves to `false` only
when no copy is found. The session's `transport_event`s
(every raw server event) and its transport's `sendEvent` (which `sendAudio`,
`sendMessage` and tool outputs go through) feed the same tracer, so a session
records the same calls and audio as above. A disconnect or `close()` ends the
session call; a later `connect()` starts a new one.
`weft.wrapRealtimeSession(session)` traces one session. For any other
websocket client, feed a `new weft.RealtimeTracer()` with `onClientEvent(e)`,
`onServerEvent(e)` and `close()`.

**TypeSafe.** `wrapTypeSafe(client, { captureContent? })` or
`patchTypeSafeModule(mod)` / `instrumentTypeSafe(mod)`
record each `systemOne({ state, questions, model? })` as an LLM (`chat`) call
of provider `typesafe`: the state as the user message, the questions as the
`typesafe.questions` attribute, the answers, response model, usage and request
id, inside the running turn or its own `typesafe SDK` turn. That turn is a
trace of its own even inside an op; when an op is running, the turn
and the LLM call carry a link to it, `attributes.weft.parent_call.id` and
`.trace_id`, which the server exposes as the agent span fields
`parent_call_id` / `parent_call_trace_id`. Errors keep their
type and HTTP status but not TypeSafe's message (it holds the response body).
`OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=false`,
`captureContent: false` or a conversation with `includeContent: false` drops
state, questions and answers.

## Framework integrations

```ts
// LangChain.js (and LangGraph): a callback handler
await chain.invoke(input, { callbacks: [new weft.WeftCallbackHandler()] });

// LlamaIndex.TS: subscribe to the global callback manager
import { Settings } from 'llamaindex';
weft.instrumentLlamaIndex(Settings);

// Vercel AI SDK: model middleware, plus tools and the top-level functions
const ai = weft.wrapAISDK(await import('ai'));                 // generateText/streamText/generateObject/streamObject
await ai.generateText({ model: wrapLanguageModel({ model, middleware: weft.weftMiddleware() }), tools, prompt });
const tools = weft.wrapAITools({ weather });                   // only the tools' execute functions

// MCP (@modelcontextprotocol/sdk)
weft.wrapMCPClient(client);            // callTool, readResource, getPrompt, list*
weft.wrapMCPServer(mcpServer);         // tools/call, resources/read, prompts/get handled by the server
weft.instrumentMCP({ Client });        // every Client instance

// Claude Agent SDK (@anthropic-ai/claude-agent-sdk)
const { query } = weft.wrapClaudeAgentSDK(await import('@anthropic-ai/claude-agent-sdk'));

// OpenAI Agents SDK (@openai/agents): a tracing processor
weft.instrumentOpenAIAgents(await import('@openai/agents'));   // or addTraceProcessor(new weft.WeftTracingProcessor())
```

| Integration | Calls |
|---|---|
| LangChain.js | `langchain.Chain.<name>` (chain/agent), `langchain.ChatModel.<name>` / `langchain.Llm.<name>` (llm, usage from `tokenUsage` or `usage_metadata`), `langchain.Tool.<name>` (tool, `gen_ai.tool.call.id`), `langchain.Retriever.<name>`; nested by run id. LangGraph: `langgraph.graph` (workflow; session = `thread_id`), `langgraph.node.<node>` with `langgraph.node` / `.step`; a node that ran a model is an agent named after it (consecutive agent nodes are hand-offs) |
| LlamaIndex.TS | `llamaindex.query`, `llamaindex.retrieve` (nodes), `llamaindex.llm.chat` (usage from the raw response), `llamaindex.tool.<name>`, `llamaindex.agent` |
| Vercel AI SDK | `vercel_ai.generateText` / `streamText` / `generateObject` / `streamObject` (chain), `vercel_ai.generate` / `vercel_ai.stream` (llm: text, reasoning, tool calls, usage incl. reasoning and cached tokens), `vercel_ai.tool.<name>` (tool) |
| MCP | `mcp.client.call_tool` (`MCP tool: <name>`), `mcp.client.read_resource`, `mcp.client.get_prompt`, `mcp.client.list_*`; `mcp.server.call_tool` etc. with ops called by the tool nested inside. `WEFT_MCP_TRACE_LIST_OPERATIONS=false` skips list calls |
| Claude Agent SDK | `claude_agent_sdk.query` (an agent turn; conversation = the SDK session id), `claude_agent_sdk.llm` per assistant message (usage once per message id, cache tokens included, thinking blocks), `claude_agent_sdk.tool.<name>` ending at its result (`gen_ai.tool.call.id`); `Task`/`Agent` calls are sub-agents named by `subagent_type`, with their messages nested |
| OpenAI Agents JS | `openai_agents.trace` (workflow; session = `groupId`, else the trace id), `agent.<name>` (`gen_ai.agent.name`), `tool.<name>`, `openai_agents.generation` / `.response` (usage, reasoning items and tokens), `openai_agents.handoff` (`weft.handoff.*`, the target set when it ends), `guardrail.<name>`, with the SDK's start and end times |

Every integration's calls carry `gen_ai.*` attributes (operation, model,
provider, tool name and call id, agent name), so they also appear in
**Agents**.

### Integration settings

Like Python's `autopatch_settings`, per integration:

```ts
await weft.init('proj', {
  autopatchSettings: {
    openai: { opSettings: { postprocessInputs: (i) => ({ ...i, messages: '[hidden]' }), postprocessOutput: scrub } },
    langchain: { enabled: false },
    mcp: { opSettings: { callDisplayName: (call) => `mcp ${call.inputs.name}`, attributes: { team: 'tools' }, color: 'purple' } },
  },
});
weft.updateIntegration('bedrock', { opSettings: { kind: 'llm', name: 'bedrock' } });
const groq = weft.wrapGroq(client, { postprocessOutput: scrub });   // per client, after the global hooks
```

`opSettings` fields: `name` (op name), `callDisplayName`, `postprocessInputs`,
`postprocessOutput`, `kind`, `color`, `attributes`. Names: `openai`,
`anthropic`, `google_genai`, `vercel_ai`, `langchain`, `llamaindex`, `mcp`,
`claude_agent_sdk`, `openai_agents`, `mistral`, `cohere`, `groq`, `together`,
`cerebras`, `bedrock`, `vertexai` (package names such as
`@modelcontextprotocol/sdk` also work). `WEFT_AUTOPATCH=false` turns all of
them off, `WEFT_DISABLED_INTEGRATIONS=mcp,langchain` some, and
`WEFT_AUTOPATCH_<NAME>=false` one; disabled integrations call straight
through.

## Objects and datasets

```ts
const ds = new weft.Dataset({ name: 'qa', rows: [{ question: '2+2', expected: '4' }] });
const ref = await weft.save(ds);      // rows uploaded as a table; content addressed
ref.uri();                               // weft:///default/my-project/object/qa:<digest>
const loaded = await weft.get('qa:latest');   // Dataset (also "qa", "qa:v0", full URIs)
const ds2 = await loaded.addRows([{ question: '3+3', expected: '6' }]);   // POST /api/v1/tables/update + new version
await weft.save({ temperature: 0.2 }, 'config');
```

Files and rich media are uploaded through `POST /api/v1/files/upload` (a file's digest is the hex SHA-256 of its bytes):

```ts
weft.Content.fromPath('report.pdf');     // {_type: 'Content', mimetype, filename, file_digest}
weft.Content.fromText('notes', { filename: 'notes.txt' });
weft.Video.fromPath('demo.mp4'); weft.PDF.fromBytes(bytes); new weft.Html('<b>x</b>'); new weft.Markdown('# x');
weft.Image.fromPath('cat.png'); weft.Audio.fromBytes(wav);
content.save('copy.pdf'); await client.readFile(fileDigest);
```

**Bring your own bucket.** Media already in your storage is logged by
reference, without uploading:

```ts
weft.Image.fromUri('s3://my-bucket/photos/cat.png');   // {_type: 'Image', uri, mime_type, filename}
weft.Content.fromUri('https://example.com/report.pdf', { size: 1234 });
await client.resolveUrl('s3://my-bucket/photos/cat.png');      // POST /api/v1/files/resolve (presigned for the server's S3 backend)
await client.resolveUrl([content, fileDigest], { expiresIn: 600 });
await weft.Content.fromUri('weft-file://<digest>').load();   // download the bytes
```

Prompts and server-runnable models (same shapes as Python; they load back as their classes):

```ts
new weft.StringPrompt({ name: 'greet', content: 'Hello {name}' }).format({ name: 'Ada' });
new weft.MessagesPrompt({ name: 'qa', messages: [{ role: 'user', content: '{question}' }] }).format({ question: '2+2' });
const m = new weft.LLMStructuredCompletionModel({ name: 'qa-model', llmModelId: 'gpt-4o-mini',
  defaultParams: { messages_template: [{ role: 'user', content: '{question}' }], temperature: 0 } });
await m.predict({ question: '2+2' });          // via the server's POST /api/v1/completions
await client.evaluateModel(evaluation, m);     // server-side evaluation (POST /api/v1/evaluations/run); polls until done; {id, status, summary}
```

Dataset helpers: `ds.select([0, 2])`, `ds.columnNames`, `ds.numRows`,
`weft.Dataset.fromCalls(spans)` (one row per span).

Object versions:

```ts
await client.setAliases(ref, 'production');      // then weft.get('qa:production')
await client.removeAliases('qa', 'production');
await client.addTags(ref, ['reviewed']); await client.removeTags(ref, ['reviewed']);
await client.getTags(ref); await client.getAliases(ref); await client.listTags(); await client.listAliases();
await client.deleteObjectVersion('qa:v0'); await client.deleteObjectVersions('qa', ['v1']); await client.deleteAllObjectVersions('qa');
```

Refs name `object`, `function`, `table` and `span` kinds (`weft:///<entity>/<project>/<kind>/...`);
refs written with the earlier kind names (`op`, `call`) are read as `function` and `span`.

**Loading functions evaluates stored code.** `await weft.get('myFn')` returns a
runnable traced function built from the stored JavaScript source with `new Function`. Its
spans are linked to the same function ref. Only self-contained functions work; a
function that closes over module variables throws `ReferenceError` when
called. When the code can't be rebuilt (for example a Python function) you get
`OpLoadError`. Only load functions from servers you trust.

`AnnotationSpec`, `Leaderboard`, `Monitor` and `SavedView` serialise in the
shapes documented in API.md and load back as their classes:

```ts
const spec = new weft.AnnotationSpec({ name: 'quality', fieldSchema: { type: 'integer', minimum: 1, maximum: 5 } });
await span.feedback.addAnnotation(spec, 4);
await weft.save(new weft.Leaderboard({ name: 'qa-board', columns: [
  { evaluationObjectRef: evaluation, scorerName: 'exact', summaryMetricPath: 'true_fraction' },
] }));
await weft.save(new weft.Monitor({ name: 'judge-chats', opNames: ['openai.chat.completions.create'], samplingRate: 0.2, scorers: [judge] }));
```

`SavedView` has a fluent query API; views open in the UI and UI views load here:

```ts
const view = await new weft.SavedView({ label: 'Slow errors' })
  .filterOp('answer')
  .addFilter('summary.weft.status', 'equals', 'error')
  .addFilter('summary.weft.latency_ms', '>', 1000)
  .sortBy('started_at', 'desc')
  .addColumn('inputs.question', 'Question')
  .save();                                  // named after the label
const spans = await view.spans({ limit: 20 });
view.toSpansQuery();                        // the raw POST /api/v1/spans/query body
await weft.SavedView.load('Slow-errors'); await client.getSavedViews('traces');
```

Operators: `contains`, `does not contain`, `equals`, `does not equal`, `in`,
`=`, `!=`, `<`, `<=`, `>`, `>=`, `is`, `after`, `before`, `is empty`,
`is not empty`; also `removeFilter`, `setFilter({...})`, `search`, `addSort`,
`showColumn`/`hideColumn`, `pinColumnLeft`/`Right`, `pageSize`, `uiUrl()`.

## Agents: conversations, turns, LLM calls and tools

`weft.startConversation()` and friends. Turns, LLM calls, tools and
sub-agents are recorded as spans the agents index understands, so they show
up in **Agents** (`/agents/*`), Threads and Traces (see the Python README for
the span shapes). `span.run(fn)` is Python's `with`: the span is current while
`fn` runs (traced functions inside become children), a thrown error is recorded and
re-thrown, and the span ends afterwards.

```ts
const conv = weft.startConversation({ agentName: 'support', conversationName: 'Order help' });
await conv.startTurn({ userMessage: 'Where is order 7?' }).run(async (turn) => {
  await turn.startLLM({ model: 'gpt-4o-mini', providerName: 'openai' }).run((llm) => {
    llm.inputMessages = [weft.Message.user('Where is order 7?')];
    llm.outputMessages = [weft.Message.assistant('', { toolCalls: [{ id: 'c1', name: 'lookup', arguments: { id: 7 } }] })];
    llm.usage = { inputTokens: 20, outputTokens: 6 };
  });
  await turn.startTool({ name: 'lookup', arguments: { id: 7 }, toolCallId: 'c1' }).run(async (t) => { t.result = await lookup(7); });
  await turn.startSubagent({ name: 'writer' }).run((s) => s.startLLM({ model: 'gpt-4o' }).output('Shipped.').end());
  turn.outputMessages = [weft.Message.assistant('Order 7 has shipped.')];
});
conv.end();
```

Also: `weft.startTurn/startLLM/startTool/startSubagent` (current conversation
and turn), `endTurn/endLLM/endConversation`, `getCurrentTurn()`,
`span.start()/end()`, `llm.think()`, `llm.attachMedia({ content, mimeType })`,
`span.setAttributes()`, `span.recordError()`, `startConversation({ includeContent: false,
continueParentTrace: true, attributes })`, `withAgentNameOverride(name, fn)`.
Batch logging of finished spans:

```ts
weft.logTurn({ conversationId: 'c-1', agentName: 'support', messages: [weft.Message.user('hi')],
  spans: [new weft.LLM({ model: 'gpt-4o-mini', startedAt: t0, endedAt: t1, usage: { inputTokens: 5 } })] });
const res = weft.logConversation({ agentName: 'support', turns: [new weft.Turn({ messages, spans })] });
res.conversationId; res.rootSpanIds;
```

## Agent teams, hand-offs, reasoning and sessions

The server derives sessions, teams, hand-offs and reasoning steps from what
the SDK records ([DESIGN.md](../../docs/weft/DESIGN.md) §2) and shows them
under **Sessions** and **Teams** and in the native API (`/api/v1/sessions/{id}`,
`/graph`, `/reasoning`, `/api/v1/teams`). The names match the Python SDK in
camelCase.

**Sessions** are `gen_ai.conversation.id`: a conversation's `conversationId`
(its spans' `session_id`), or the session the integrations record (OpenAI
Agents `groupId`, else the trace id; LangGraph `thread_id`; the Claude Agent
SDK session id; Bedrock Agents `sessionId`).

**Teams.** `weft.withTeam({ id, name }, fn)` (or an id, or `[id, name]`)
stamps `weft.team.id` / `weft.team.name` on every call and span started in
`fn`; `startConversation({ team })`, `logTurn({ team })` and
`logConversation({ team })` set it for one conversation (inside
`conv.run(fn)`, ops get it too). `weft.getCurrentTeam()` returns the active
team.

```ts
await weft.withTeam({ id: 'trip-planners', name: 'Trip planners' }, () => runOrchestrator());
const conv = weft.startConversation({ agentName: 'orchestrator', team: { id: 'trip-planners', name: 'Trip planners' } });
```

**Hand-offs.** `weft.handoff(toAgent, { input, kind, fromAgent, output })`
records an instant hand-off (`weft.handoff.to_agent`, `.from_agent` (default:
the agent running now), `.kind`: `handoff` or `delegate`), linked by the server
to the next run of `toAgent` in the trace. `weft.startHandoff(toAgent, opts).run(fn)`
covers the other agent's run, which becomes its child; `turn.handoff()`,
`turn.startHandoff()` and `subagent.startHandoff()` start from a given span.
Nested runs of another agent are delegations without an explicit hand-off:
`turn.startSubagent()`, or a turn of another agent started inside a running
one (`conv.startTurn({ agentName })` keeps the running turn open and nests the
new one under it).

```ts
await weft.startHandoff('researcher', { input: task, kind: 'delegate' }).run(async (h) => {
  await conv.startTurn({ agentName: 'researcher', userMessage: task }).run(async () => { /* ... */ });
  h.output = facts;
});
weft.handoff('writer', { input: 'Summarise the facts' });
```

**Reasoning.** The integrations keep reasoning in the shapes the server reads
steps from: Anthropic `thinking` / `redacted_thinking` blocks (streamed too),
OpenAI Responses `reasoning` items and `reasoning_tokens`, chat
`reasoning_content` / `reasoning` deltas (DeepSeek, vLLM, OpenRouter), Gemini
`thought: true` parts (streamed thoughts joined, `thoughtsTokenCount`),
Bedrock Converse `reasoningContent` (as `thinking` / `redacted_thinking`
blocks, also streamed), Claude Agent SDK thinking blocks and OpenAI Agents
response spans. Redacted reasoning is stored as redacted with its encrypted
payload replaced by a `sha256:` digest. When a framework's LLM call wraps a
traced provider call, the reasoning stays on the provider call only.
`weft.reasoning(text, { kind, tokens })` (or `weft.reasoning(text, 'summary')`)
records a step on the innermost running span or op; the server links it to
the tool call or hand-off that follows.

`settings: { captureReasoning: false }` (or `WEFT_CAPTURE_REASONING=false`)
removes reasoning from everything recorded; token counts are kept. PII
redaction applies to reasoning text. `weft.scrubReasoning(value, capture)` is
the transform the SDK applies. Example:
[`examples/weft/typescript/agent_team.ts`](../../examples/weft/typescript/agent_team.ts).

## Evaluations

Models are called as `model({ datasetRow })` and scorers as
`scorer({ modelOutput, datasetRow })`:

```ts
const model = weft.trace(async function model({ datasetRow }) {
  return String(datasetRow.question).length > 3 ? 'long' : 'short';
});
const exact = weft.trace(function exact({ modelOutput, datasetRow }) {
  return modelOutput === datasetRow.expected;
});

const evaluation = new weft.Evaluation({ name: 'qa-eval', dataset: ds, scorers: [exact], trials: 1 });
const summary = await evaluation.evaluate({ model });
// { exact: { true_count, true_fraction }, model_latency: { mean } }
```

The recorded spans match the Python SDK:

```
Evaluation.evaluate          inputs {self: <evaluation ref>, model: <model function ref>}
├── Evaluation.predict_and_score  inputs {self, model, example: <dataset row ref>}
│   ├── model
│   └── scorers
└── Evaluation.summarize
```

Summary shapes: booleans → `{true_count, true_fraction}`, numbers → `{mean}`,
objects key by key, plus `model_latency: {mean}` (seconds).

### Built-in scorers

These serialise exactly like the Python SDK's, so the server can run them
(`client.evaluateModel`, Monitors). They also run locally:

```ts
scorers: [
  new weft.ExactMatchScorer({ name: 'exact', targetColumn: 'expected' }),
  new weft.ContainsScorer({ targetColumn: 'expected', caseInsensitive: true }),
  new weft.RegexScorer({ pattern: '^\\d+$' }),
  new weft.JSONValidScorer(),
  new weft.LevenshteinScorer({ targetColumn: 'expected' }),
  new weft.LLMAsAJudgeScorer({ model: 'gpt-4o-mini', scoringPrompt: 'Is {output} a correct answer to {question}?' }),
]
```

More built-ins serialise exactly like their Python counterparts:
- `HallucinationFreeScorer`, `SummarizationScorer` and `RelevanceScorer`:
  LLM-judge presets with `model` and `scoringPrompt`.
- `ValidXMLScorer`.
- `JSONSchemaScorer({ schema })`.
- `OpenAIModerationScorer`. Locally it needs `OPENAI_API_KEY`.
- `EmbeddingSimilarityScorer({ targetColumn, model })`. Locally it uses the
  server's `POST /api/v1/embeddings`; without it, it fails with a clear
  error.
- `RuleScorer({ rule, threshold })`: a query-language operation evaluated on
  `{output, inputs, ...columns}`.
- `RemoteScorer({ url, headersSecret, timeout })`: POSTs `{output, inputs}` to
  `url`. Locally the headers come from the environment variable named by
  `headersSecret`.

`LLMAsAJudgeScorer` runs through the server's `POST /api/v1/completions` with
`track_llm_call: false`, so provider keys stay on the server and usage is
recorded on the scorer call. For a custom scorer object, subclass
`weft.Scorer` and implement `score({ modelOutput, datasetRow })`.

### EvaluationLogger

```ts
const ev = new weft.EvaluationLogger({ model: 'my-model', dataset: 'qa', name: 'nightly' });
const pred = ev.logPrediction({ question: '2+2' }, '4');
pred.logScore('correct', true);
pred.finish();
ev.logExample({ question: '3+3' }, '6', { correct: true });   // prediction + scores in one call
const summary = await ev.logSummary();     // same call structure as Evaluation
```

Without an output the predict span stays open: `await pred.run(async (p) => { p.output = await agent(q); })`
records spans made inside as its children (and stamps `weft.eval.*` on
conversation spans started there), then finishes the prediction.

**Linking existing traces (agent evals):** the third argument may be options,
including `call` (a Span, span id or URI, a Turn or other conversation span, a `LogResult`,
a `Conversation`, or an array):

```ts
const [, call] = await agent.call('2+2');
ev.logPrediction({ q: '2+2' }, undefined, { call });          // output and latency from the span
ev.logPrediction({ q: 'hi' }, 'hello', { call: turn, exampleId: 'ex-1' });
```

Links are stored as `attributes.weft.linked_calls`, `attributes.weft.genai_span_ref`
(`[{trace_id, span_id}]`, the v2 predictions field) and `attributes.weft.linked_conversation_ids`.

Pass a `Dataset` as `dataset` to link predictions to its rows. Load a
saved one with `await weft.get('qa')` first. A prediction whose
inputs equal a row references that row's ref.

Objects are saved with locally computed digests (Weft digests, the hex
SHA-256 of canonical JSON), which match the server's, so logging never waits
on the network.

### Guardrails

```ts
const [answer, span] = await generate.call(prompt);
const check = await span!.applyScorer(toxicity, { additionalScorerKwargs: { threshold: 0.8 } });
if ((check.result as any).flagged) { /* block */ }
```

`applyScorer` calls the scorer with
`{ modelOutput: <span output>, datasetRow: <span inputs + kwargs> }`. It
records the scorer's span as its own trace and attaches `weft.scorer.<scorer>`
feedback (with `runnable_ref` and `span_ref`) to the scored span; the result
is `{ result, scoreSpan, feedbackId }`.

## Querying

```ts
const spans = await client.spans({ filter: { names: ['answer'] }, limit: 10 });   // POST /api/v1/spans/stream
const span = await client.span(spans[0].id, { includeCosts: true });                // GET /api/v1/spans/{id}; null when absent
span!.name; span!.baseName; span!.error; span!.sessionId; span!.turnId;
await span!.feedback.addReaction('👍');
await span!.feedback.addNote('nice');
await span!.setDisplayName('golden');
await client.addCost('my-model', 1e-6, 2e-6);
await client.queryCosts({ llmIds: ['my-model'] }); await client.purgeCosts(ids);
await client.whoami();                     // { user_id, auth_required }
await client.countSpans({ filter: { names: ['answer'] } });
await client.deleteSpans([id]);
await client.getFeedback({ query: { $eq: [{ $getField: 'feedback_type' }, { $literal: 'weft.note' }] } });
await span!.feedback.list(); await span!.feedback.purge(feedbackId);
await span!.children(); span!.status; span!.uiUrl;
await client.getThreads({ limit: 20 });      // sessions (POST /api/v1/spans/sessions)
await client.registerCustomRuntime({        // an OpenAI-compatible endpoint; resolves to { data, error: null }
  name: 'my-vllm', baseUrl: 'https://vllm.internal/v1',
  runtimeIds: ['llama-3-8b', { id: 'qwen2-7b', maxTokens: 8192 }],   // the complete list
  apiKeySecret: 'VLLM_KEY', headers: { 'X-Team': 'ml' },
});                                          // models "my-vllm/llama-3-8b", ...
client.conversationUrl(conversationId);      // UI link; also weft.agentConversationPath(entity, project, id)
await client.getEvaluations(); await client.evaluationRuns();
await client.compareEvaluations([a, b]); await client.leaderboard('qa-board');
await weft.get('qa:latest'); weft.parseUri('weft:///e/p/object/qa:<digest>');

// spans as plain records (the TS counterpart of Python's SpansIter.to_pandas()/to_records())
const rows = await client.spanRecords({ filter: { names: ['answer'] }, includeUsernames: true });
spans[0].toRecord();   // { id, name, inputs, output, error, session_id, status, summary, run_id, ... }

// registry (prompt versions, and any object)
await weft.linkPromptToRegistry(prompt, { targetPath: 'prompts/support-bot', aliases: ['production'] });
await weft.linkToRegistry(dataset, { targetPath: 'my-team/datasets-registry/golden-sets' });
await client.queryRegistry('prompts', { collection: 'support-bot' });
await client.getFromRegistry('prompts/support-bot', 'production');   // alias, 'v0', 0 or 'latest'
```

`spans` already returns an array (`Span[]`). `targetPath` is
`<registry_project>/<collection>` (in your entity) or
`<entity>/<registry_project>/<collection>`; unsaved objects are saved
first.

## OpenTelemetry

Weft also ingests OTLP/HTTP directly. Point any OpenTelemetry exporter at
`<server>/otel/v1/traces` with a `project_id` header:

```ts
new OTLPTraceExporter({ url: weft.otel.otlpEndpoint(), headers: weft.otel.otlpHeaders('my-entity/my-project') });
// or, with @opentelemetry/sdk-trace-node and an OTLP exporter package installed:
const provider = await weft.otel.configure({ project: 'my-entity/my-project' });
```

## Breaking changes

### 0.1.0: Weft's own names

Names that came from another tracing product were renamed, with no aliases
(the server's optional compatibility adapter, `--weave-compat`, still serves
that product's own client; see `docs/weft/COMPATIBILITY.md`). Migrate as
follows:

| Before | Now | Migration |
|---|---|---|
| `setWbRun(runId, step)`, `client.setWbRun(...)` | `setRun(runId, step)`, `client.setRun(...)` | rename the call |
| `init({ wbRunId, wbRunStep })` | `init({ runId, runStep })` | rename the options |
| `call.wbUserId`, `call.wbUsername` | `call.userId`, `call.username` | rename the property |
| `call.wbRunId`, `call.wbRunStep`, `call.wbRunStepEnd` | `call.runId`, `call.runStep`, `call.runStepEnd` | rename the property |
| wire fields `wb_user_id`, `wb_username`, `wb_run_id`, `wb_run_step`, `wb_run_step_end` | `user_id`, `username`, `run_id`, `run_step`, `run_step_end` | rename in queries, sorts and raw API use |
| `CallsFilter` / `SavedView.setFilter` keys `wb_user_ids`, `wb_run_ids` | `user_ids`, `run_ids` | rename the keys |
| feedback field `weave_ref` (payloads, `$getField`) | `ref` | rename the field |
| feedback type `wandb.reaction.1` | `weft.reaction` | rename the type string |
| feedback type `wandb.note.1` | `weft.note` | rename the type string |
| feedback type `wandb.annotation.<name>` | `weft.annotation.<name>` | rename the type string |
| feedback type `wandb.runnable.<scorer>` | `weft.scorer.<scorer>` | rename the type string |

Data already stored is migrated by the server the first time it opens an old
database (columns, feedback types and saved queries), so only code that names
the old fields needs changing.

## Development

```bash
npm test                     # vitest, in-process fake server
WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 npm test   # also runs test/server.integration.test.ts
npm run typecheck && npm run build
```

`.npmrc` sets `legacy-peer-deps=true`, which works around an npm 10
crash ("Cannot read properties of null (reading 'edgesOut')") while resolving
vitest's optional peer dependencies.
