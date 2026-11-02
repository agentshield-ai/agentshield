import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import * as weft from '../src/index.js';
import { FakeWeft } from './fakeServer.js';

let fake: FakeWeft;
let client: weft.WeftClient;

beforeEach(async () => {
  fake = await new FakeWeft().start();
  client = await weft.init('ts-entity/ts-proj', { serverUrl: fake.url, sender: { flushIntervalMs: 5 } });
});

afterEach(async () => {
  await weft.finish();
  await fake.close();
});

function chatChunks(model: string) {
  return [
    { id: 'c', model, choices: [{ index: 0, delta: { role: 'assistant', content: 'Hel' } }] },
    { id: 'c', model, choices: [{ index: 0, delta: { content: 'lo' } }] },
    { id: 'c', model, choices: [{ index: 0, delta: { tool_calls: [{ index: 0, id: 't1', function: { name: 'get', arguments: '{"a"' } }] } }] },
    { id: 'c', model, choices: [{ index: 0, delta: { tool_calls: [{ index: 0, function: { arguments: ':1}' } }] }, finish_reason: 'tool_calls' }] },
    { id: 'c', model, choices: [], usage: { prompt_tokens: 5, completion_tokens: 2, total_tokens: 7 } },
  ];
}

class FakeStream<T> {
  controller = { abort() {} };
  constructor(private items: T[]) {}
  async *[Symbol.asyncIterator]() {
    for (const i of this.items) yield i;
  }
  toReadableStream() {
    return 'readable';
  }
}

function fakeOpenAI() {
  const seen: any = {};
  return {
    seen,
    chat: {
      completions: {
        async create(body: any) {
          seen.body = body;
          if (body.stream) return new FakeStream(chatChunks(body.model));
          return {
            id: 'c1',
            model: body.model,
            choices: [{ index: 0, message: { role: 'assistant', content: 'hello' }, finish_reason: 'stop' }],
            usage: { prompt_tokens: 11, completion_tokens: 4, total_tokens: 15 },
          };
        },
      },
    },
    responses: {
      async create(body: any) {
        return { id: 'r1', model: body.model, output_text: 'hi', usage: { input_tokens: 3, output_tokens: 1, total_tokens: 4 } };
      },
    },
  };
}

describe('integrations', () => {
  it('wrapOpenAI traces chat completions, streaming and responses', async () => {
    const oai = weft.wrapOpenAI(weft.wrapOpenAI(fakeOpenAI()));
    const agent = weft.op(async function agent(q: string) {
      const r = await oai.chat.completions.create({ model: 'gpt-4o-mini', messages: [{ role: 'user', content: q }] });
      return (r as any).choices[0].message.content;
    });
    expect(await agent('hi')).toBe('hello');
    const stream: any = await oai.chat.completions.create({ model: 'gpt-4o', messages: [], stream: true });
    expect(oai.seen.body.stream_options).toEqual({ include_usage: true });
    expect(stream.toReadableStream()).toBe('readable');
    const parts = [];
    for await (const ch of stream) parts.push(ch);
    expect(parts).toHaveLength(5);
    await oai.responses.create({ model: 'gpt-4.1', input: 'hi' });
    await client.flush();
    const calls = fake.callsByOp('openai.chat.completions.create');
    expect(calls).toHaveLength(2);
    const [root] = fake.callsByOp('agent');
    expect(calls[0].parent_id).toBe(root.id);
    expect(calls[0].inputs).toEqual({ model: 'gpt-4o-mini', messages: [{ role: 'user', content: 'hi' }] });
    expect(calls[0].summary.usage).toEqual({ 'gpt-4o-mini': { requests: 1, prompt_tokens: 11, completion_tokens: 4, total_tokens: 15 } });
    expect(root.summary).toBeUndefined();
    const msg = calls[1].output.choices[0].message;
    expect(msg.content).toBe('Hello');
    expect(msg.tool_calls[0].function).toEqual({ name: 'get', arguments: '{"a":1}' });
    expect(calls[1].summary.usage['gpt-4o'].total_tokens).toBe(7);
    const [resp] = fake.callsByOp('openai.responses.create');
    expect(resp.summary.usage['gpt-4.1']).toEqual({ requests: 1, prompt_tokens: 3, completion_tokens: 1, total_tokens: 4 });
  });

  it('wrapAnthropic traces messages.create and streams', async () => {
    const events = [
      { type: 'message_start', message: { id: 'm', model: 'claude-haiku-4-5', role: 'assistant', content: [], usage: { input_tokens: 12, output_tokens: 1 } } },
      { type: 'content_block_start', index: 0, content_block: { type: 'text', text: '' } },
      { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: 'Hi ' } },
      { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: 'there' } },
      { type: 'content_block_stop', index: 0 },
      { type: 'message_delta', delta: { stop_reason: 'end_turn' }, usage: { output_tokens: 6 } },
      { type: 'message_stop' },
    ];
    const final = { id: 'm2', model: 'claude-sonnet-4-5', content: [{ type: 'text', text: 'Bonjour' }], usage: { input_tokens: 9, output_tokens: 2 } };
    const anth = weft.wrapAnthropic({
      messages: {
        async create(body: any) {
          return body.stream ? new FakeStream(events) : { ...final, model: body.model };
        },
        stream(_body: any) {
          return { finalMessage: async () => final, on() {} };
        },
      },
    });
    await anth.messages.create({ model: 'claude-sonnet-4-5', max_tokens: 10, messages: [] });
    const s: any = await anth.messages.create({ model: 'claude-haiku-4-5', max_tokens: 10, messages: [], stream: true });
    for await (const _ of s) {
      /* consume */
    }
    await anth.messages.stream({ model: 'claude-sonnet-4-5', messages: [] }).finalMessage();
    await new Promise((r) => setTimeout(r, 5));
    await client.flush();
    const calls = fake.callsByOp('anthropic.messages.create');
    expect(calls[0].summary.usage['claude-sonnet-4-5']).toEqual({ requests: 1, prompt_tokens: 9, completion_tokens: 2, total_tokens: 11 });
    expect(calls[1].output.content[0].text).toBe('Hi there');
    expect(calls[1].output.stop_reason).toBe('end_turn');
    expect(calls[1].summary.usage['claude-haiku-4-5']).toEqual({ requests: 1, prompt_tokens: 12, completion_tokens: 6, total_tokens: 18 });
    const [st] = fake.callsByOp('anthropic.messages.stream');
    expect(st.output.content[0].text).toBe('Bonjour');
  });

  it('passes through untraced when not initialised', async () => {
    await weft.finish();
    const oai = weft.wrapOpenAI(fakeOpenAI());
    const r: any = await oai.chat.completions.create({ model: 'm', messages: [], stream: false });
    expect(r.model).toBe('m');
    expect(fake.requests).toHaveLength(0);
  });
});
