/**
 * Bedrock Agents and OpenAI Realtime tracing, NER PII redaction, the prompt
 * registry and calls as records, with the TypeScript SDK.
 *
 *   cd sdk/typescript && npm install && npm run build
 *   WEFT_SERVER_URL=http://localhost:6345 node examples/weft/typescript/agents_and_redaction.ts
 *
 * Runs offline: a stand-in BedrockAgentRuntimeClient replays a recorded
 * InvokeAgent stream, a stand-in realtime connection replays server events, and
 * a tiny NER function stands in for a Presidio Analyzer (set
 * WEFT_PRESIDIO_ANALYZER_URL to use a real one, e.g. the
 * mcr.microsoft.com/presidio-analyzer container).
 */
import * as weft from '../../../sdk/typescript/dist/index.js';

// A stand-in NER analyzer: finds two known names. Any (text, entities) => spans function works.
const tinyNer: weft.PiiAnalyzer = (text) => {
  const spans: weft.PiiSpan[] = [];
  for (const name of ['Ada Lovelace', 'Grace Hopper']) {
    const i = text.indexOf(name);
    if (i >= 0) spans.push({ entity_type: 'PERSON', start: i, end: i + name.length, score: 0.9 });
  }
  return spans;
};

const client = await weft.init('ts-agents-redaction', {
  settings: {
    printCallLink: false,
    redactPii: true,
    ...(process.env.WEFT_PRESIDIO_ANALYZER_URL ? { redactPiiEngine: 'presidio' as const } : { redactPiiAnalyzer: tinyNer }),
    retryMaxAttempts: 5, // or WEFT_RETRY_MAX_ATTEMPTS
  },
});

// --- NER redaction: names are replaced before the call leaves the process ---------
const greet = weft.op(function greet(message: string) {
  return `Hello Ada Lovelace, your receipt went to ada@example.com`;
});
await greet('Ada Lovelace asked about her order');

// --- Bedrock Agents --------------------------------------------------------------
class InvokeAgentCommand {
  input: Record<string, unknown>;
  constructor(input: Record<string, unknown>) {
    this.input = input;
  }
}
const enc = (s: string) => new TextEncoder().encode(s);
async function* recorded(sessionId: string) {
  const tr = (inner: unknown) => ({ trace: { agentId: 'WEATHER', sessionId, trace: inner } });
  const model = 'anthropic.claude-3-5-sonnet-20240620-v1:0';
  yield tr({ orchestrationTrace: { modelInvocationInput: { traceId: 't-0', text: 'Weather in Oslo?', foundationModel: model } } });
  yield tr({ orchestrationTrace: { modelInvocationOutput: { traceId: 't-0', rawResponse: { content: 'call get_weather' }, metadata: { usage: { inputTokens: 420, outputTokens: 25 } } } } });
  yield tr({ orchestrationTrace: { invocationInput: { traceId: 't-0', actionGroupInvocationInput: { actionGroupName: 'weather', function: 'get_weather', parameters: [{ name: 'city', value: 'Oslo' }] } } } });
  yield tr({ orchestrationTrace: { observation: { traceId: 't-0', actionGroupInvocationOutput: { text: '{"temp_c": 12}' } } } });
  yield { chunk: { bytes: enc('It is 12°C in Oslo.') } };
}
class BedrockAgentRuntimeClient {
  async send(cmd: InvokeAgentCommand) {
    return { sessionId: cmd.input.sessionId, completion: recorded(String(cmd.input.sessionId)) };
  }
}
const agents = weft.wrapBedrockAgent(new BedrockAgentRuntimeClient());
const res: any = await agents.send(new InvokeAgentCommand({ agentId: 'WEATHER', agentAliasId: 'PROD', sessionId: `s-${Date.now()}`, inputText: 'Weather in Oslo?', enableTrace: true }));
let answer = '';
for await (const ev of res.completion) if (ev.chunk) answer += new TextDecoder().decode(ev.chunk.bytes);
console.log('agent:', answer);

// --- OpenAI Realtime (a stand-in for openai/realtime/ws's OpenAIRealtimeWS) --------
class FakeRealtime {
  url = new URL('wss://api.openai.com/v1/realtime?model=gpt-realtime');
  _emit(_name: string, ..._args: unknown[]) {}
  send(_event: unknown) {}
  close() {}
}
const rt = weft.wrapOpenAIRealtime(new FakeRealtime());
rt.send({ type: 'conversation.item.create', item: { type: 'message', role: 'user', content: [{ type: 'input_text', text: 'Say hi' }] } });
for (const e of [
  { type: 'session.created', session: { model: 'gpt-realtime', voice: 'alloy' } },
  { type: 'response.created', response: { id: 'r1' } },
  { type: 'response.output_text.delta', response_id: 'r1', delta: 'Hi!' },
  { type: 'response.done', response: { id: 'r1', status: 'completed', usage: { input_tokens: 12, output_tokens: 3, total_tokens: 15 } } },
]) {
  rt._emit('event', e);
}
rt.close();

// --- Registry and records ---------------------------------------------------------
const prompt = new weft.StringPrompt({ name: 'weather-prompt', content: 'You are a concise weather assistant. Question: {q}' });
await weft.publish(prompt);
console.log('registry:', await weft.linkPromptToRegistry(prompt, { targetPath: 'prompts-registry/weather', aliases: ['production'] }));
console.log('production prompt:', (await client.getFromRegistry('prompts-registry/weather', 'production')).format({ q: 'Oslo?' }));

await client.flush();
for (const r of await client.getCallsRecords({ sortBy: [{ field: 'started_at', direction: 'asc' }] })) {
  console.log(String(r.display_name ?? r.op_name).slice(0, 60), JSON.stringify(r.inputs).slice(0, 80));
}
await weft.finish();
