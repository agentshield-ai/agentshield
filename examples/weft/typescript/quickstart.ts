/**
 * Weft TypeScript quickstart: tracing, an OpenAI-shaped client, and an evaluation.
 *
 *   cd sdk/typescript && npm install && npm run build
 *   WEFT_SERVER_URL=http://localhost:6345 node examples/weft/typescript/quickstart.ts
 *
 * (Node >= 22.18 runs .ts files directly; on older Node compile with tsc first.)
 * In your own project: `npm install weft-sdk` and `import * as weft from 'weft-sdk'`.
 */
import * as weft from '../../../sdk/typescript/dist/index.js';

const client = await weft.init('ts-quickstart');

// --- tracing -----------------------------------------------------------------
const retrieve = weft.trace(
  async function retrieve(query: string) {
    return [`doc about ${query}`, 'another doc'];
  },
  { kind: 'retriever' },
);

// A stand-in for `new OpenAI()`; wrapOpenAI works the same on the real client.
const llmClient = weft.wrapOpenAI({
  chat: {
    completions: {
      async create(body: { model: string; messages: Array<{ role: string; content: string }> }) {
        const last = body.messages[body.messages.length - 1].content;
        return {
          id: 'local',
          model: body.model,
          choices: [{ index: 0, message: { role: 'assistant', content: `Answer: ${last.slice(0, 40)}` }, finish_reason: 'stop' }],
          usage: { prompt_tokens: 25, completion_tokens: 8, total_tokens: 33 },
        };
      },
    },
  },
});

const answer = weft.trace(
  async function answer(question: string) {
    const docs = await retrieve(question);
    const res = await llmClient.chat.completions.create({
      model: 'gpt-4o-mini',
      messages: [
        { role: 'system', content: `Context: ${docs.join(' | ')}` },
        { role: 'user', content: question },
      ],
    });
    return res.choices[0].message.content;
  },
  { kind: 'agent', color: 'blue', callDisplayName: (call) => `answer: ${String(call.inputs.question).slice(0, 20)}` },
);

await weft.withSession('demo-conversation', async () => {
  await weft.attributes({ env: 'dev' }, async () => {
    console.log(await answer('What is Weft?'));
    console.log(await answer('Does it speak OpenTelemetry?'));
  });
});

// --- evaluation --------------------------------------------------------------
const dataset = new weft.Dataset({
  name: 'capitals-ts',
  rows: [
    { country: 'France', expected: 'Paris' },
    { country: 'Japan', expected: 'Tokyo' },
    { country: 'Australia', expected: 'Canberra' },
  ],
});

const capitalModel = weft.trace(async function capitalModel({ datasetRow }: { datasetRow: { country: string } }) {
  const known: Record<string, string> = { France: 'Paris', Japan: 'Tokyo', Australia: 'Sydney' };
  return known[datasetRow.country] ?? 'unknown';
});

const exactMatch = weft.trace(function exactMatch({ modelOutput, datasetRow }: { modelOutput: string; datasetRow: { expected: string } }) {
  return modelOutput === datasetRow.expected;
});

const lengthDelta = weft.trace(function lengthDelta({ modelOutput, datasetRow }: { modelOutput: string; datasetRow: { expected: string } }) {
  return { delta: Math.abs(modelOutput.length - datasetRow.expected.length) };
});

const evaluation = new weft.Evaluation({
  name: 'capitals-ts-eval',
  dataset,
  scorers: [exactMatch, lengthDelta, new weft.ExactMatchScorer({ name: 'exact_builtin' }), new weft.LevenshteinScorer({ name: 'similarity' })],
});
console.log('summary:', JSON.stringify(await evaluation.evaluate({ model: capitalModel })));

// --- reading data back ---------------------------------------------------------
const [latest] = await client.spans({ filter: { names: ['answer'] }, sortBy: [{ field: 'started_at', direction: 'desc' }], limit: 1 });
await latest.feedback.addReaction('👍');
const guard = await latest.applyScorer(new weft.RuleScorer({ name: 'non_empty', rule: { $gt: [{ $getField: 'output' }, { $literal: '' }] } }));
console.log('guardrail:', guard.result);

// attach a document, then tag the dataset version
const attach = weft.trace(function attachNotes() {
  return weft.Content.fromText('Capitals reviewed by the geography team.', { filename: 'notes.txt' });
});
attach();
await client.addTags(dataset.ref!, ['reviewed']);
console.log('latest answer call:', latest.id, 'usage:', JSON.stringify(latest.summary.usage));

await weft.finish();
