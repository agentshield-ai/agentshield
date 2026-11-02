/** Shared fixtures for parityGaps.test.ts and parityGaps.integration.test.ts. */
export const enc = (s: string) => new TextEncoder().encode(s);

export async function* gen<T>(items: T[]): AsyncGenerator<T> {
  for (const i of items) {
    if (i instanceof Error) throw i;
    yield i;
  }
}

export const MODEL = 'anthropic.claude-3-5-sonnet-20240620-v1:0';

export const tr = (inner: any, outer: any = {}) => ({ trace: { agentId: 'AGENT1', agentAliasId: 'ALIAS1', sessionId: 'sess-42', trace: inner, ...outer } });
export const modelIO = (step: string, tid: string, u: [number, number], content: string, extraOut: any = {}, outer: any = {}) => [
  tr(
    {
      [step]: {
        modelInvocationInput: {
          traceId: tid,
          text: JSON.stringify({ system: 'You are a weather agent.', messages: [{ role: 'user', content: 'Weather in Oslo?' }] }),
          type: 'ORCHESTRATION',
          foundationModel: MODEL,
        },
      },
    },
    outer,
  ),
  tr({ [step]: { modelInvocationOutput: { traceId: tid, rawResponse: { content }, metadata: { usage: { inputTokens: u[0], outputTokens: u[1] } }, ...extraOut } } }, outer),
];

/** A recorded InvokeAgent response stream (pre-processing, orchestration with a tool, a KB lookup, a collaborator, a guardrail). */
export function agentEvents(sessionId = 'sess-42'): any[] {
  const chain = [{ agentAliasArn: 'arn:sup' }, { agentAliasArn: 'arn:researcher' }];
  const ev: any[] = [
    ...modelIO('preProcessingTrace', 't-pre', [100, 10], '<thinking>ok</thinking>', { parsedResponse: { isValid: true, rationale: 'fine' } }),
    ...modelIO('orchestrationTrace', 't-0', [200, 20], 'call get_weather'),
    tr({ orchestrationTrace: { rationale: { traceId: 't-0', text: 'I need the weather.' } } }),
    tr({
      orchestrationTrace: {
        invocationInput: {
          traceId: 't-0',
          invocationType: 'ACTION_GROUP',
          actionGroupInvocationInput: { actionGroupName: 'weather', function: 'get_weather', parameters: [{ name: 'city', type: 'string', value: 'Oslo' }], executionType: 'LAMBDA', invocationId: 'inv-1' },
        },
      },
    }),
    tr({ orchestrationTrace: { observation: { traceId: 't-0', type: 'ACTION_GROUP', actionGroupInvocationOutput: { text: '{"temp": 12}' } } } }),
    ...modelIO('orchestrationTrace', 't-1', [250, 30], 'search the kb'),
    tr({ orchestrationTrace: { invocationInput: { traceId: 't-1', invocationType: 'KNOWLEDGE_BASE', knowledgeBaseLookupInput: { knowledgeBaseId: 'KB1', text: 'Oslo climate' } } } }),
    tr({ orchestrationTrace: { observation: { traceId: 't-1', type: 'KNOWLEDGE_BASE', knowledgeBaseLookupOutput: { retrievedReferences: [{ content: { text: 'Oslo is cold.' } }] } } } }),
    tr({ orchestrationTrace: { invocationInput: { traceId: 't-2', invocationType: 'AGENT_COLLABORATOR', agentCollaboratorInvocationInput: { agentCollaboratorName: 'researcher', input: { text: 'Forecast for Oslo?' } } } } }),
    ...modelIO('orchestrationTrace', 't-c0', [50, 5], 'rain tomorrow', {}, { collaboratorName: 'researcher', callerChain: chain }),
    tr({ orchestrationTrace: { observation: { traceId: 't-2', type: 'AGENT_COLLABORATOR', agentCollaboratorInvocationOutput: { agentCollaboratorName: 'researcher', output: { text: 'Rain tomorrow.' } } } } }),
    tr({ guardrailTrace: { action: 'NONE', traceId: 't-g', inputAssessments: [{}] } }),
    ...modelIO('orchestrationTrace', 't-3', [300, 40], 'final'),
    tr({ orchestrationTrace: { observation: { traceId: 't-3', type: 'FINISH', finalResponse: { text: 'It is 12°C in Oslo; rain tomorrow.' } } } }),
    { chunk: { bytes: enc('It is 12°C in Oslo; '), attribution: { citations: [{ retrievedReferences: [{ location: 's3://kb/oslo.txt' }] }] } } },
    { chunk: { bytes: enc('rain tomorrow.') } },
  ];
  return JSON.parse(JSON.stringify(ev, (_k, v) => (v instanceof Uint8Array ? { __b: new TextDecoder().decode(v) } : v)).replaceAll('sess-42', sessionId), (_k, v) =>
    v && typeof v === 'object' && '__b' in v ? enc(v.__b) : v,
  );
}

export class InvokeAgentCommand {
  constructor(public input: any) {}
}
export class InvokeInlineAgentCommand {
  constructor(public input: any) {}
}

/** A fake `BedrockAgentRuntimeClient` replaying `events`. */
export function agentRuntime(events: any[]) {
  return class BedrockAgentRuntimeClient {
    async send(cmd: any) {
      if (cmd instanceof InvokeAgentCommand || cmd instanceof InvokeInlineAgentCommand) {
        return { $metadata: { httpStatusCode: 200 }, contentType: 'application/json', sessionId: cmd.input.sessionId, completion: gen(events) };
      }
      return { other: true };
    }
  };
}

export function realtimeEvents(): any[] {
  return [
    { type: 'session.created', session: { id: 'sess_1', model: 'gpt-realtime', voice: 'alloy', instructions: 'Be brief.' } },
    { type: 'response.created', response: { id: 'resp_1', status: 'in_progress' } },
    { type: 'response.output_audio_transcript.delta', response_id: 'resp_1', delta: 'It is ' },
    { type: 'response.output_audio_transcript.delta', response_id: 'resp_1', delta: 'sunny.' },
    { type: 'response.output_audio.delta', response_id: 'resp_1', delta: 'A'.repeat(400) },
    { type: 'response.output_item.done', response_id: 'resp_1', item: { type: 'function_call', name: 'get_weather', call_id: 'call_1', arguments: '{"city": "Paris"}' } },
    {
      type: 'response.done',
      response: {
        id: 'resp_1',
        status: 'completed',
        output: [{ type: 'message', content: [{ type: 'output_audio', transcript: 'It is sunny.', audio: 'B'.repeat(200) }] }],
        usage: { total_tokens: 150, input_tokens: 100, output_tokens: 50, input_token_details: { cached_tokens: 20, audio_tokens: 60 }, output_token_details: { audio_tokens: 40 } },
      },
    },
    { type: 'response.created', response: { id: 'resp_2' } },
    { type: 'error', error: { message: 'rate limited' } },
  ];
}

/** A minimal stand-in for openai's OpenAIRealtimeWS (an emitter with `_emit`, `send`, `close`). */
export class FakeRealtimeWS {
  url = new URL('wss://api.openai.com/v1/realtime?model=gpt-realtime');
  sent: any[] = [];
  closed = false;
  listeners = new Map<string, Array<(...a: any[]) => void>>();
  on(name: string, fn: (...a: any[]) => void) {
    this.listeners.set(name, [...(this.listeners.get(name) ?? []), fn]);
  }
  _emit(name: string, ...args: any[]) {
    for (const fn of this.listeners.get(name) ?? []) fn(...args);
  }
  send(event: any) {
    this.sent.push(event);
  }
  close() {
    this.closed = true;
  }
  /** Simulate the socket delivering a server event. */
  receive(event: any) {
    this._emit('event', event);
    if (event.type !== 'error') this._emit(event.type, event);
  }
}
