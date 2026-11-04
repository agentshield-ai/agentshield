/**
 * Prompt-cache counts reach summary.usage the way the server prices them
 * (docs/weft/API.md "Cached tokens"): a gross prompt_tokens with
 * cache_read_input_tokens / cache_creation_input_tokens as parts of it.
 */
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import * as weft from '../src/index.js';
import { FakeWeft } from './fakeServer.js';

let fake: FakeWeft;
let client: weft.WeftClient;

beforeEach(async () => {
  fake = await new FakeWeft().start();
  client = await weft.init('ts-entity/cache', { serverUrl: fake.url, sender: { flushIntervalMs: 5 }, settings: { printSpanLink: false }, autopatchSettings: {} });
});

afterEach(async () => {
  await weft.finish();
  await fake.close();
});

async function settle(): Promise<void> {
  await new Promise((r) => setTimeout(r, 10));
  await client.flush();
}

async function* gen<T>(items: T[]) {
  for (const i of items) yield i;
}

const ANTHROPIC = { input_tokens: 1000, output_tokens: 100, cache_read_input_tokens: 9000, cache_creation_input_tokens: 500 };
const GROSS = { prompt_tokens: 10500, completion_tokens: 100, cache_read_input_tokens: 9000, cache_creation_input_tokens: 500 };

describe('cached tokens', () => {
  it('Anthropic messages.create and stream: gross prompt tokens plus cache counts', async () => {
    const events = [
      { type: 'message_start', message: { id: 'm', model: 'claude-sonnet-4-5', role: 'assistant', content: [], usage: { ...ANTHROPIC, output_tokens: 1 } } },
      { type: 'content_block_start', index: 0, content_block: { type: 'text', text: '' } },
      { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: 'Hi' } },
      { type: 'content_block_stop', index: 0 },
      { type: 'message_delta', delta: { stop_reason: 'end_turn' }, usage: { output_tokens: 100 } },
      { type: 'message_stop' },
    ];
    const anth = weft.wrapAnthropic({
      messages: {
        async create(body: any) {
          return body.stream ? gen(events) : { id: 'm2', model: 'claude-sonnet-4-5', content: [{ type: 'text', text: 'Hi' }], usage: ANTHROPIC };
        },
      },
    });
    await anth.messages.create({ model: 'claude-sonnet-4-5', max_tokens: 10, messages: [] });
    const s: any = await anth.messages.create({ model: 'claude-sonnet-4-5', max_tokens: 10, messages: [], stream: true });
    for await (const _ of s) {
      /* consume */
    }
    await settle();
    const calls = fake.callsByOp('anthropic.messages.create');
    expect(calls).toHaveLength(2);
    for (const c of calls) expect(c.summary.usage['claude-sonnet-4-5']).toEqual({ requests: 1, total_tokens: 10600, ...GROSS });
  });

  it('OpenAI chat and Responses keep cached prompt tokens', async () => {
    const oai = weft.wrapOpenAI({
      chat: {
        completions: {
          async create(body: any) {
            return {
              id: 'c',
              model: body.model,
              choices: [{ index: 0, message: { role: 'assistant', content: 'hi' }, finish_reason: 'stop' }],
              usage: { prompt_tokens: 2000, completion_tokens: 50, total_tokens: 2050, prompt_tokens_details: { cached_tokens: 1536, audio_tokens: 0 }, completion_tokens_details: { reasoning_tokens: 10 } },
            };
          },
        },
      },
      responses: {
        async create(body: any) {
          return { id: 'r', model: body.model, output: [], usage: { input_tokens: 3000, output_tokens: 70, total_tokens: 3070, input_tokens_details: { cached_tokens: 2048 }, output_tokens_details: { reasoning_tokens: 40 } } };
        },
      },
    } as any);
    await oai.chat.completions.create({ model: 'gpt-4o', messages: [] });
    await oai.responses.create({ model: 'gpt-5', input: 'hi' });
    await settle();
    const [chat] = fake.callsByOp('openai.chat.completions.create');
    expect(chat.summary.usage['gpt-4o']).toMatchObject({ prompt_tokens: 2000, cache_read_input_tokens: 1536, prompt_tokens_details: { cached_tokens: 1536 }, reasoning_tokens: 10 });
    const [resp] = fake.callsByOp('openai.responses.create');
    expect(resp.summary.usage['gpt-5']).toMatchObject({ prompt_tokens: 3000, cache_read_input_tokens: 2048, input_tokens_details: { cached_tokens: 2048 }, reasoning_tokens: 40 });
  });

  it('Gemini keeps cachedContentTokenCount', async () => {
    const usageMetadata = { promptTokenCount: 5000, candidatesTokenCount: 200, thoughtsTokenCount: 50, totalTokenCount: 5250, cachedContentTokenCount: 4096 };
    const ai = weft.wrapGoogleGenAI({
      models: {
        async generateContent(_p: any) {
          return { candidates: [{ index: 0, content: { role: 'model', parts: [{ text: 'a' }] } }], modelVersion: 'gemini-2.5-pro', usageMetadata };
        },
        async generateContentStream(_p: any) {
          return gen([
            { candidates: [{ index: 0, content: { role: 'model', parts: [{ text: 'a' }] } }] },
            { candidates: [{ index: 0, content: { role: 'model', parts: [{ text: 'b' }] } }], usageMetadata },
          ]);
        },
      },
    });
    await ai.models.generateContent({ model: 'gemini-2.5-pro', contents: 'hi' });
    for await (const _ of await ai.models.generateContentStream({ model: 'gemini-2.5-pro', contents: 'hi' })) {
      /* consume */
    }
    await settle();
    const calls = [...fake.calls.values()].filter((c: any) => c.summary?.usage?.['gemini-2.5-pro']);
    expect(calls).toHaveLength(2);
    for (const c of calls) expect(c.summary.usage['gemini-2.5-pro']).toMatchObject({ prompt_tokens: 5000, completion_tokens: 250, cache_read_input_tokens: 4096, reasoning_tokens: 50, total_tokens: 5250 });
  });

  it('Bedrock Converse, ConverseStream, InvokeModel and its stream: gross prompt tokens plus cache counts', async () => {
    class ConverseCommand {
      constructor(public input: any) {}
    }
    class ConverseStreamCommand {
      constructor(public input: any) {}
    }
    class InvokeModelCommand {
      constructor(public input: any) {}
    }
    class InvokeModelWithResponseStreamCommand {
      constructor(public input: any) {}
    }
    const enc = (o: unknown) => new TextEncoder().encode(JSON.stringify(o));
    const usage = { inputTokens: 1000, outputTokens: 100, totalTokens: 10600, cacheReadInputTokens: 9000, cacheWriteInputTokens: 500 };
    const br = weft.wrapBedrock({
      async send(cmd: any) {
        switch (cmd.constructor.name) {
          case 'ConverseCommand':
            return { output: { message: { role: 'assistant', content: [{ text: 'hi' }] } }, stopReason: 'end_turn', usage };
          case 'ConverseStreamCommand':
            return { stream: gen([{ contentBlockDelta: { delta: { text: 'hi' } } }, { messageStop: { stopReason: 'end_turn' } }, { metadata: { usage } }]) };
          case 'InvokeModelCommand':
            return { body: enc({ content: [{ type: 'text', text: 'ok' }], usage: ANTHROPIC }) };
          default:
            return {
              body: gen([
                { chunk: { bytes: enc({ type: 'message_start', message: { model: 'claude', usage: { ...ANTHROPIC, output_tokens: 1 } } }) } },
                { chunk: { bytes: enc({ type: 'content_block_delta', delta: { type: 'text_delta', text: 'A' } }) } },
                { chunk: { bytes: enc({ type: 'message_delta', delta: { stop_reason: 'end_turn' }, usage: { output_tokens: 100 } }) } },
                { chunk: { bytes: enc({ type: 'message_stop', 'amazon-bedrock-invocationMetrics': { inputTokenCount: 1000, outputTokenCount: 100, cacheReadInputTokenCount: 9000, cacheWriteInputTokenCount: 500 } }) } },
              ]),
            };
        }
      },
    });
    const modelId = 'anthropic.claude-3-5-sonnet-20240620-v1:0';
    await br.send(new ConverseCommand({ modelId, messages: [] }));
    const cs: any = await br.send(new ConverseStreamCommand({ modelId, messages: [] }));
    for await (const _ of cs.stream) {
      /* consume */
    }
    await br.send(new InvokeModelCommand({ modelId, body: JSON.stringify({ messages: [] }) }));
    const is: any = await br.send(new InvokeModelWithResponseStreamCommand({ modelId, body: JSON.stringify({ messages: [] }) }));
    for await (const _ of is.body) {
      /* consume */
    }
    await settle();
    const calls = [...fake.calls.values()].filter((c: any) => c.summary?.usage?.[modelId]);
    expect(calls).toHaveLength(4);
    for (const c of calls) expect(c.summary.usage[modelId]).toMatchObject(GROSS);
    expect(fake.callsByOp('bedrock.converse')[0].summary.usage[modelId].total_tokens).toBe(10600);
  });
});
