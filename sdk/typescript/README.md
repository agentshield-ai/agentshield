# weft-sdk — TypeScript SDK

TypeScript/JavaScript client for **Weft**, an OpenTelemetry-native LLM tracing
and evaluation server. ESM with type declarations, Node >= 18, and no runtime
dependencies (it uses the global `fetch` and `AsyncLocalStorage`).

```bash
cd sdk/typescript && npm install && npm run build   # from this repository
# in an app: npm install weft-sdk
```

## Quickstart

```ts
import * as weft from 'weft-sdk';

const client = await weft.init('my-project', { serverUrl: 'http://localhost:6345', apiKey: process.env.WEFT_API_KEY });

const retrieve = weft.op(async function retrieve(query: string) {
  return ['doc one', 'doc two'];
});

const answer = weft.op(async function answer(question: string) {
  const docs = await retrieve(question);       // child call
  return `${docs.length} docs for ${question}`;
});

await answer('what is weft?');
await weft.finish();                           // flush (also attempted on process beforeExit)
```

Environment variables: `WEFT_SERVER_URL` (default `http://localhost:6345`),
`WEFT_API_KEY`, `WEFT_PROJECT`, `WEFT_DISABLED=true`, `WEFT_PARALLELISM`
(evaluation concurrency, default 10), and `WEFT_REDACT_KEYS=0` (turns off key
redaction).

### Redaction

By default, values under keys named `api_key`, `apikey`, `authorization`,
`auth`, `password`, `secret`, `token` or `access_token` are replaced with
`"REDACTED"` in call inputs and outputs. Matching is case-insensitive and on
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
    printCallLink: true,                         // "weft: <server>/<entity>/<project>/calls/<id>" per root call (WEFT_PRINT_CALL_LINK)
    captureCode: true,                           // store op source (WEFT_CAPTURE_CODE)
    enableDiskFallback: true,                    // on-disk write-ahead log (WEFT_ENABLE_WAL, dir: walDir / WEFT_WAL_DIR)
    disableWalSender: false,                     // with the WAL on: write everything to it, send nothing (WEFT_DISABLE_WAL_SENDER)
    maxQueueSize: 10_000, batchSize: 100,      // WEFT_MAX_CALLS_QUEUE_SIZE (0 = unbounded)
    retryMaxAttempts: 3,                       // WEFT_RETRY_MAX_ATTEMPTS
    retryBackoffMs: 250, retryMaxBackoffMs: 4000,   // WEFT_RETRY_BACKOFF, WEFT_RETRY_MAX_INTERVAL (seconds)
    httpTimeoutMs: 30_000,                     // WEFT_HTTP_TIMEOUT (seconds)
    logLevel: 'warning',                       // debug, info, warning, error, critical (WEFT_LOG_LEVEL)
    wbRunId: 'run-abc', wbRunStep: () => step, // W&B run linkage (also client.setWbRun / weft.setWbRun)
  },
});
```

The environment variables use Weave's names with a `WEFT_` prefix and seconds
like the Python SDK; explicit settings and `sender` options win over them.
`weft.setLogLevel()` changes the level later; the SDK's messages go through
`weft.logger` (to `console`). With `wbRunId`/`setWbRun(runId, step)` calls
carry `wb_run_id` (`<entity>/<project>/<run id>`), `wb_run_step` and
`wb_run_step_end`, as Weave does for an active `wandb` run.

With `enableDiskFallback`, anything that cannot be sent goes to a JSONL file
instead of being dropped. That covers batches that still fail after retries,
queue overflow, and what is still queued when the process exits. The next
`init()` for the same server and project replays it. Weave's WAL is different
(every request is written first and a background "WAL sender" drains it), so
its `WEAVE_DISABLE_WAL_SENDER` maps to `disableWalSender` /
`WEFT_DISABLE_WAL_SENDER`: with the WAL on, every write goes to the file and
nothing is sent or replayed until a run without it. `WEFT_DEBUG_HTTP=1`
(Weave's `WEAVE_DEBUG_HTTP`) prints every request and response (method, URL,
headers with `Authorization` redacted, JSON bodies, status, elapsed time) to
stderr.

## Tracing

- `weft.op(fn, { name, kind, color, tracingSampleRate, enableCodeCapture, callDisplayName, parameterNames, postprocessInputs, postprocessOutput, accumulator })`
  traces sync and async functions, generators, async generators and functions
  returning async iterables. Streamed items are accumulated into the output (an
  array by default).
- Input names come from the function's parameter list. A single destructured
  object parameter (`async ({ datasetRow }) => ...`) records that object's
  keys. Override with `parameterNames`.
- `kind` (`agent`, `llm`, `tool`, `chain`, `retriever`, `embedding`,
  `scorer`, ...) and `color` are stored as `attributes.weft.span_kind` and
  `attributes.weft.color`, mirrored to `attributes.weave.kind` and `.color`.
  `tracingSampleRate` samples root calls, and a root's children follow it.
  `weft.withTracingDisabled(() => ...)` runs code without recording anything.
- `weft.op(this, fn)` traces a method and records the instance as `self` (its
  ref when it has been published).
- `await myOp.call(...args)` resolves to `[result, call]`. Errors are captured
  on the call instead of being thrown.
- Errors are recorded as `"<Name>: <message>"` plus the stack, then rethrown.
- Parent/child tracking uses `AsyncLocalStorage`, so it follows `await`,
  `Promise.all` and timers.
- `weft.attributes({ env: 'prod' }, () => ...)` attaches attributes to calls
  started inside the callback. `weft.withThread('conversation-id', () => ...)`
  sets `thread_id`, and calls started directly inside are turns.
- `weft.getCurrentCall()` returns the current call, and
  `weft.addUsage(call, model, promptTokens, completionTokens)` records the
  call's own LLM usage. The server adds up descendants.
- Calls are batched to `/call/upsert_batch` by a background sender with a
  bounded queue, retries and backoff. If the server is down you get a warning,
  never a crash.

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
`{model: {requests, prompt_tokens, completion_tokens, total_tokens}}`. Streamed
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

Audio is saved as WAV files (`weft.Audio`), as in Weave: the user's audio
(`input_audio_buffer.append`, cut per item at the server-VAD speech markers or
at each commit) on the item's `input_audio` part, and the model's audio
(`response.output_audio.delta`) on the response's `output_audio` part. PCM16
at the session's rate and G.711 u-law/A-law are written. Each clip is capped
at `WEFT_REALTIME_MAX_AUDIO_BYTES` (default 25 MiB; the server takes files up
to 64 MiB) and marked `audio_truncated` past it; `0` records sizes only.

The OpenAI Agents SDK's `RealtimeSession` (`@openai/agents-realtime`, the
route Weave documents for TypeScript) is traced by Weave's
`patchRealtimeSession()`: it imports the package (or takes it:
`patchRealtimeSession(await import('@openai/agents-realtime'))`) and patches
`RealtimeSession.prototype.connect`, so every session, including ones created
earlier, is traced from its `connect()`. The session's `transport_event`s
(every raw server event) and its transport's `sendEvent` (which `sendAudio`,
`sendMessage` and tool outputs go through) feed the same tracer, so a session
records the same calls and audio as above. A disconnect or `close()` ends the
session call; a later `connect()` starts a new one.
`weft.wrapRealtimeSession(session)` traces one session. For any other
websocket client, feed a `new weft.RealtimeTracer()` with `onClientEvent(e)`,
`onServerEvent(e)` and `close()`.

**TypeSafe.** `wrapTypeSafe(client, { captureContent? })` or
`patchTypeSafeModule(mod)` / `instrumentTypeSafe(mod)` (Weave's names)
record each `systemOne({ state, questions, model? })` as an LLM (`chat`) call
of provider `typesafe`: the state as the user message, the questions as the
`typesafe.questions` attribute, the answers, response model, usage and request
id, inside the running turn or its own `typesafe SDK` turn. Errors keep their
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
| LangChain.js | `langchain.Chain.<name>` (chain/agent), `langchain.ChatModel.<name>` / `langchain.Llm.<name>` (llm, usage from `tokenUsage` or `usage_metadata`), `langchain.Tool.<name>` (tool, `gen_ai.tool.call.id`), `langchain.Retriever.<name>`; nested by run id |
| LlamaIndex.TS | `llamaindex.query`, `llamaindex.retrieve` (nodes), `llamaindex.llm.chat` (usage from the raw response), `llamaindex.tool.<name>`, `llamaindex.agent` |
| Vercel AI SDK | `vercel_ai.generateText` / `streamText` / `generateObject` / `streamObject` (chain), `vercel_ai.generate` / `vercel_ai.stream` (llm: text, reasoning, tool calls, usage incl. reasoning and cached tokens), `vercel_ai.tool.<name>` (tool) |
| MCP | `mcp.client.call_tool` (`MCP tool: <name>`), `mcp.client.read_resource`, `mcp.client.get_prompt`, `mcp.client.list_*`; `mcp.server.call_tool` etc. with ops called by the tool nested inside. `WEFT_MCP_TRACE_LIST_OPERATIONS=false` skips list calls |
| Claude Agent SDK | `claude_agent_sdk.query` (an agent turn; conversation = the SDK session id), `claude_agent_sdk.llm` per assistant message (usage once per message id, cache tokens included), `claude_agent_sdk.tool.<name>` ending at its result; sub-agent messages nest under their `Task`/`Agent` call |
| OpenAI Agents JS | `openai_agents.trace`, `agent.<name>`, `tool.<name>`, `openai_agents.generation` / `.response` (usage), `openai_agents.handoff`, `guardrail.<name>`, with the SDK's start and end times |

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
const ref = await weft.publish(ds);      // rows uploaded as a table; content addressed
ref.uri();                               // weft:///default/my-project/object/qa:<digest>
const loaded = await weft.ref('qa:latest').get();   // Dataset (also "qa", "qa:v0", full URIs)
const ds2 = await loaded.addRows([{ question: '3+3', expected: '6' }]);   // /table/update + new version
await weft.publish({ temperature: 0.2 }, 'config');
```

Files and rich media are uploaded through `/file/create`:

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
await client.resolveUrl('s3://my-bucket/photos/cat.png');      // /files/resolve_url (presigned for the server's S3 backend)
await client.resolveUrl([content, fileDigest], { expiresIn: 600 });
await weft.Content.fromUri('weft-file://<digest>').load();   // download the bytes
```

Prompts and server-runnable models (same shapes as Python; they load back as their classes):

```ts
new weft.StringPrompt({ name: 'greet', content: 'Hello {name}' }).format({ name: 'Ada' });
new weft.MessagesPrompt({ name: 'qa', messages: [{ role: 'user', content: '{question}' }] }).format({ question: '2+2' });
const m = new weft.LLMStructuredCompletionModel({ name: 'qa-model', llmModelId: 'gpt-4o-mini',
  defaultParams: { messages_template: [{ role: 'user', content: '{question}' }], temperature: 0 } });
await m.predict({ question: '2+2' });          // via the server's /completions/create
await client.evaluateModel(evaluation, m);     // server-side evaluation; polls until done
```

Dataset helpers: `ds.select([0, 2])`, `ds.columnNames`, `ds.numRows`,
`weft.Dataset.fromCalls(calls)`.

Object versions:

```ts
await client.setAliases(ref, 'production');      // then weft.ref('qa:production')
await client.removeAliases('qa', 'production');
await client.addTags(ref, ['reviewed']); await client.removeTags(ref, ['reviewed']);
await client.getTags(ref); await client.getAliases(ref); await client.listTags();
await client.deleteObjectVersion('qa:v0'); await client.deleteObjectVersions('qa', ['v1']); await client.deleteAllObjectVersions('qa');
```

**Loading ops evaluates stored code.** `await weft.ref('myOp').get()` returns a
runnable op built from the stored JavaScript source with `new Function`. Its
calls are linked to the same op ref. Only self-contained functions work; a
function that closes over module variables throws `ReferenceError` when
called. When the code can't be rebuilt (for example a Python op) you get
`OpLoadError`. Only load ops from servers you trust.

`AnnotationSpec`, `Leaderboard`, `Monitor` and `SavedView` serialise in the
shapes documented in API.md and load back as their classes:

```ts
const spec = new weft.AnnotationSpec({ name: 'quality', fieldSchema: { type: 'integer', minimum: 1, maximum: 5 } });
await call.feedback.addAnnotation(spec, 4);
await weft.publish(new weft.Leaderboard({ name: 'qa-board', columns: [
  { evaluationObjectRef: evaluation, scorerName: 'exact', summaryMetricPath: 'true_fraction' },
] }));
await weft.publish(new weft.Monitor({ name: 'judge-chats', opNames: ['openai.chat.completions.create'], samplingRate: 0.2, scorers: [judge] }));
```

`SavedView` has Weave's fluent query API; views open in the UI and UI views load here:

```ts
const view = await new weft.SavedView({ label: 'Slow errors' })
  .filterOp('answer')
  .addFilter('summary.weave.status', 'equals', 'error')
  .addFilter('summary.weave.latency_ms', '>', 1000)
  .sortBy('started_at', 'desc')
  .addColumn('inputs.question', 'Question')
  .save();                                  // named after the label
const calls = await view.getCalls({ limit: 20 });
view.toCallsQuery();                        // the raw /calls/query body
await weft.SavedView.load('Slow-errors'); await client.getSavedViews('traces');
```

Operators: `contains`, `does not contain`, `equals`, `does not equal`, `in`,
`=`, `!=`, `<`, `<=`, `>`, `>=`, `is`, `after`, `before`, `is empty`,
`is not empty`; also `removeFilter`, `setFilter({...})`, `search`, `addSort`,
`showColumn`/`hideColumn`, `pinColumnLeft`/`Right`, `pageSize`, `uiUrl()`.

## Agents: conversations, turns, LLM calls and tools

Weave's conversation SDK with camelCase names. Turns, LLM calls, tools and
sub-agents are recorded as calls the agents index understands, so they show
up in **Agents** (`/agents/*`), Threads and Traces (see the Python README for
the call shapes). `span.run(fn)` is Python's `with`: the span is current while
`fn` runs (ops inside become children), a thrown error is recorded and
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

## Evaluations

Models are called as `model({ datasetRow })` and scorers as
`scorer({ modelOutput, datasetRow })`:

```ts
const model = weft.op(async function model({ datasetRow }) {
  return String(datasetRow.question).length > 3 ? 'long' : 'short';
});
const exact = weft.op(function exact({ modelOutput, datasetRow }) {
  return modelOutput === datasetRow.expected;
});

const evaluation = new weft.Evaluation({ name: 'qa-eval', dataset: ds, scorers: [exact], trials: 1 });
const summary = await evaluation.evaluate({ model });
// { exact: { true_count, true_fraction }, model_latency: { mean } }
```

The recorded calls match the Python SDK:

```
Evaluation.evaluate          inputs {self: <evaluation ref>, model: <model op ref>}
├── Evaluation.predict_and_score  inputs {self, model, example: <dataset row ref>}
│   ├── model
│   └── scorers
└── Evaluation.summarize
```

Summary shapes: booleans → `{true_count, true_fraction}`, numbers → `{mean}`,
objects key by key, plus `model_latency: {mean}` (seconds).

### Built-in scorers

These serialise exactly like the Python SDK's, so the server can run them
(`/evaluate_model`, Monitors). They also run locally:

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
- `EmbeddingSimilarityScorer({ targetColumn, model })`. Locally it needs a
  server `/embeddings/create` endpoint; without one it fails with a clear
  error.
- `RuleScorer({ rule, threshold })`: a query-language operation evaluated on
  `{output, inputs, ...columns}`.
- `RemoteScorer({ url, headersSecret, timeout })`: POSTs `{output, inputs}` to
  `url`. Locally the headers come from the environment variable named by
  `headersSecret`.

`LLMAsAJudgeScorer` runs through the server's `/completions/create` with
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

Without an output the predict call stays open: `await pred.run(async (p) => { p.output = await agent(q); })`
records calls made inside as its children (and stamps `weave.eval.*` on
conversation spans started there), then finishes the prediction.

**Linking existing traces (agent evals):** the third argument may be options,
including `call` (a Call, call id or URI, a Turn or other span, a `LogResult`,
a `Conversation`, or an array):

```ts
const [, call] = await agent.call('2+2');
ev.logPrediction({ q: '2+2' }, undefined, { call });          // output and latency from the call
ev.logPrediction({ q: 'hi' }, 'hello', { call: turn, exampleId: 'ex-1' });
```

Links are stored as `attributes.weft.linked_calls`, `attributes.weft.genai_span_ref`
(`[{trace_id, span_id}]`, Weave's v2 field) and `attributes.weft.linked_conversation_ids`.

Pass a `Dataset` as `dataset` to link predictions to its rows. Load a
published one with `await weft.ref('qa').get()` first. A prediction whose
inputs equal a row references that row's ref.

Objects are published with locally computed digests, which match the
server's, so logging never waits on the network.

### Guardrails

```ts
const [answer, call] = await generate.call(prompt);
const check = await call!.applyScorer(toxicity, { additionalScorerKwargs: { threshold: 0.8 } });
if ((check.result as any).flagged) { /* block */ }
```

`applyScorer` calls the scorer with
`{ modelOutput: <call output>, datasetRow: <call inputs + kwargs> }`. It
records the scorer call as its own trace and attaches `wandb.runnable.<scorer>`
feedback (with `runnable_ref` and `call_ref`) to the scored call.

## Querying

```ts
const calls = await client.getCalls({ filter: { op_names: ['answer'] }, limit: 10 });
const call = await client.getCall(calls[0].id, { includeCosts: true });
await call!.feedback.addReaction('👍');
await call!.feedback.addNote('nice');
await call!.setDisplayName('golden');
await client.addCost('my-model', 1e-6, 2e-6);
await client.queryCosts({ llmIds: ['my-model'] }); await client.purgeCosts(ids);
await client.whoami();                     // { user_id, auth_required }
await client.countCalls({ filter: { op_names: ['answer'] } });
await client.deleteCalls([id]);
await client.getFeedback({ query: { $eq: [{ $getField: 'feedback_type' }, { $literal: 'wandb.note.1' }] } });
await call!.feedback.list(); await call!.feedback.purge(feedbackId);
await call!.children(); call!.status; call!.uiUrl;
await client.getThreads({ limit: 20 });
await client.registerCustomRuntime({        // Weave's registerCustomRuntime; resolves to { data, error: null }
  name: 'my-vllm', baseUrl: 'https://vllm.internal/v1',
  runtimeIds: ['llama-3-8b', { id: 'qwen2-7b', maxTokens: 8192 }],   // the complete list
  apiKeySecret: 'VLLM_KEY', headers: { 'X-Team': 'ml' },
});                                          // models "my-vllm/llama-3-8b", ...
client.conversationUrl(conversationId);      // UI link; also weft.agentConversationPath(entity, project, id)
await client.getEvaluations(); await client.getEvaluationCalls();
await client.compareEvaluations([a, b]); await client.leaderboard('qa-board');
await weft.get('qa:latest'); weft.parseUri('weft:///e/p/object/qa:<digest>');

// calls as plain records (the TS counterpart of Python's CallsIter.to_pandas()/to_records())
const rows = await client.getCallsRecords({ filter: { op_names: ['answer'] }, includeUsernames: true });
calls[0].toRecord();   // { id, op_name, inputs, output, status, summary, wb_run_id, ... }

// registry (Weave's link_prompt_to_registry, and any object)
await weft.linkPromptToRegistry(prompt, { targetPath: 'wandb-registry-prompts/support-bot', aliases: ['production'] });
await weft.linkToRegistry(dataset, { targetPath: 'my-team/datasets-registry/golden-sets' });
await client.queryRegistry('wandb-registry-prompts', { collection: 'support-bot' });
await client.getFromRegistry('wandb-registry-prompts/support-bot', 'production');   // alias, 'v0', 0 or 'latest'
```

`getCalls` already returns an array (`Call[]`). `targetPath` is
`<registry_project>/<collection>` (in your entity) or
`<entity>/<registry_project>/<collection>`; unpublished objects are published
first.

## OpenTelemetry

Weft also ingests OTLP/HTTP directly. Point any OpenTelemetry exporter at
`<server>/otel/v1/traces` with a `project_id` header:

```ts
new OTLPTraceExporter({ url: weft.otel.otlpEndpoint(), headers: weft.otel.otlpHeaders('my-entity/my-project') });
// or, with @opentelemetry/sdk-trace-node and an OTLP exporter package installed:
const provider = await weft.otel.configure({ project: 'my-entity/my-project' });
```

## Development

```bash
npm test                     # vitest, in-process fake server
WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 npm test   # also runs test/server.integration.test.ts
npm run typecheck && npm run build
```

`.npmrc` sets `legacy-peer-deps=true`, which works around an npm 10
crash ("Cannot read properties of null (reading 'edgesOut')") while resolving
vitest's optional peer dependencies.
