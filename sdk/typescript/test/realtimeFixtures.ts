/** Realtime fixtures: GA-format events and a RealtimeSession stand-in shaped like @openai/agents-realtime. */
export function pcm16(ms: number, freq = 440, rate = 24000): Buffer {
  const n = Math.floor((rate * ms) / 1000);
  const b = Buffer.alloc(n * 2);
  for (let i = 0; i < n; i++) b.writeInt16LE(Math.round(12000 * Math.sin((2 * Math.PI * freq * i) / rate)), i * 2);
  return b;
}

export const b64 = (b: Uint8Array) => Buffer.from(b).toString('base64');

export const SESSION = {
  type: 'realtime',
  object: 'realtime.session',
  id: 'sess_1',
  model: 'gpt-realtime',
  output_modalities: ['audio'],
  instructions: 'Be brief.',
  audio: { input: { format: { type: 'audio/pcm', rate: 24000 }, turn_detection: { type: 'server_vad' } }, output: { format: { type: 'audio/pcm', rate: 24000 }, voice: 'alloy' } },
};
export const USAGE = { total_tokens: 230, input_tokens: 150, output_tokens: 80, input_token_details: { text_tokens: 30, audio_tokens: 120, cached_tokens: 0 }, output_token_details: { text_tokens: 20, audio_tokens: 60 } };

let evN = 0;
export function responseEvents(rid: string, itemId: string, out: Buffer, transcript = 'Hello there.'): any[] {
  const evs: any[] = [
    { type: 'response.created', response: { id: rid, object: 'realtime.response', status: 'in_progress', output: [] } },
    { type: 'response.output_item.added', response_id: rid, output_index: 0, item: { id: itemId, type: 'message', role: 'assistant', status: 'in_progress', content: [] } },
    { type: 'response.content_part.added', response_id: rid, item_id: itemId, output_index: 0, content_index: 0, part: { type: 'audio', transcript: '' } },
  ];
  for (let i = 0; i < out.length; i += 4800) evs.push({ type: 'response.output_audio.delta', response_id: rid, item_id: itemId, output_index: 0, content_index: 0, delta: b64(out.subarray(i, i + 4800)) });
  evs.push(
    { type: 'response.output_audio_transcript.delta', response_id: rid, item_id: itemId, output_index: 0, content_index: 0, delta: transcript },
    { type: 'response.output_audio.done', response_id: rid, item_id: itemId, output_index: 0, content_index: 0 },
    { type: 'response.done', response: { id: rid, object: 'realtime.response', status: 'completed', output: [{ id: itemId, type: 'message', role: 'assistant', status: 'completed', content: [{ type: 'output_audio', transcript }] }], usage: USAGE } },
  );
  return evs.map((e) => ({ event_id: `event_${++evN}`, ...e }));
}

// ------------------------------------------------------------------ a RealtimeSession stand-in
export class Emitter {
  private ls = new Map<string, Array<(...a: any[]) => void>>();
  on(n: string, fn: (...a: any[]) => void) {
    this.ls.set(n, [...(this.ls.get(n) ?? []), fn]);
    return this;
  }
  off(n: string, fn: (...a: any[]) => void) {
    this.ls.set(n, (this.ls.get(n) ?? []).filter((f) => f !== fn));
    return this;
  }
  emit(n: string, ...a: any[]) {
    for (const fn of this.ls.get(n) ?? []) fn(...a);
    return true;
  }
}

/** Like OpenAIRealtimeWebSocket: `_onMessage` emits each raw server event as '*'. */
export class FakeTransport extends Emitter {
  sent: any[] = [];
  status = 'disconnected';
  async connect(_opts: unknown) {
    this.status = 'connected';
    this.emit('connection_change', 'connected');
    this.serverSend({ type: 'session.created', event_id: 'event_0', session: SESSION });
  }
  serverSend(ev: any) {
    const raw = JSON.parse(JSON.stringify(ev));
    this.emit('*', raw);
    if (raw.type === 'response.output_audio.delta') this.emit('audio', { type: 'audio', data: Buffer.from(raw.delta, 'base64').buffer, responseId: raw.response_id });
  }
  sendEvent(event: any) {
    this.sent.push(event);
  }
  sendAudio(audio: ArrayBuffer, { commit = false } = {}) {
    this.sendEvent({ type: 'input_audio_buffer.append', audio: b64(new Uint8Array(audio)) });
    if (commit) this.sendEvent({ type: 'input_audio_buffer.commit' });
  }
  close() {
    this.status = 'disconnected';
    this.emit('connection_change', 'disconnected');
  }
}

export class RealtimeSession extends Emitter {
  #transport: FakeTransport;
  #attached = false;
  options: any;
  constructor(_agent: unknown, options: any = {}) {
    super();
    this.options = options;
    this.#transport = options.transport ?? new FakeTransport();
  }
  get transport() {
    return this.#transport;
  }
  async connect(options: any) {
    if (!this.#attached) {
      this.#transport.on('*', (e: any) => this.emit('transport_event', e));
      this.#attached = true;
    }
    await this.#transport.connect(options);
    this.emit('history_updated', []);
  }
  sendAudio(audio: ArrayBuffer, options: any = {}) {
    this.#transport.sendAudio(audio, options);
  }
  sendMessage(text: string) {
    this.#transport.sendEvent({ type: 'conversation.item.create', item: { type: 'message', role: 'user', content: [{ type: 'input_text', text }] } });
    this.#transport.sendEvent({ type: 'response.create' });
  }
  close() {
    this.#transport.close();
  }
}

