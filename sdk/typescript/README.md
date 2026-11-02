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
`WEFT_API_KEY`, `WEFT_PROJECT`, `WEFT_DISABLED=true`, and `WEFT_PARALLELISM`
(evaluation concurrency, default 10).

## Tracing

- `weft.op(fn, { name, callDisplayName, parameterNames, postprocessInputs, postprocessOutput, accumulator })`
  traces sync and async functions, generators, async generators and functions
  returning async iterables. Streamed items are accumulated into the output (an
  array by default).
- Input names come from the function's parameter list. A single destructured
  object parameter (`async ({ datasetRow }) => ...`) records that object's
  keys. Override with `parameterNames`.
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
```

Each request becomes a call (for example `openai.chat.completions.create`).
The request body is recorded as inputs and the response as output. Streams are
accumulated into one response. `summary.usage` is
`{model: {requests, prompt_tokens, completion_tokens, total_tokens}}`. Streamed
OpenAI chat requests get `stream_options: { include_usage: true }` unless you
set `stream_options` yourself. The wrapped methods return plain promises, so
the OpenAI-specific `APIPromise` helpers such as `.withResponse()` are not
available on wrapped calls.

## Objects and datasets

```ts
const ds = new weft.Dataset({ name: 'qa', rows: [{ question: '2+2', expected: '4' }] });
const ref = await weft.publish(ds);      // rows uploaded as a table; content addressed
ref.uri();                               // weft:///default/my-project/object/qa:<digest>
const loaded = await weft.ref('qa:latest').get();   // Dataset (also "qa", "qa:v0", full URIs)
await weft.publish({ temperature: 0.2 }, 'config');
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

## Querying

```ts
const calls = await client.getCalls({ filter: { op_names: ['answer'] }, limit: 10 });
const call = await client.getCall(calls[0].id, { includeCosts: true });
await call!.feedback.addReaction('👍');
await call!.feedback.addNote('nice');
await call!.setDisplayName('golden');
await client.addCost('my-model', 1e-6, 2e-6);
```

## OpenTelemetry

Weft also ingests OTLP/HTTP directly. Point any OpenTelemetry exporter at
`<server>/otel/v1/traces` with a `project_id` header:

```ts
new OTLPTraceExporter({ url: 'http://localhost:6345/otel/v1/traces', headers: { project_id: 'my-entity/my-project' } });
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
