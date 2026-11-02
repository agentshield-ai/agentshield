/**
 * OpenAI Realtime API (the `openai` package's `OpenAIRealtimeWS` from
 * `openai/realtime/ws` and `OpenAIRealtimeWebSocket` from
 * `openai/realtime/websocket`), mirroring the Python SDK's integration.
 *
 * ```ts
 * import { OpenAIRealtimeWS } from 'openai/realtime/ws';
 * const rt = weft.wrapOpenAIRealtime(new OpenAIRealtimeWS({ model: 'gpt-realtime' }));
 * // or every connection: weft.instrumentOpenAIRealtime(OpenAIRealtimeWS)
 * ```
 *
 * Client events (`rt.send(...)`) and server events (everything the
 * connection emits as `event`) become calls:
 *
 * - `openai.realtime.session` (agent): one per connection, until `close()`;
 *   output = the final session configuration and event counts.
 * - `openai.realtime.response` (llm): `response.created` … `response.done`;
 *   inputs = the conversation items added since the previous response (text,
 *   input-audio transcripts, function-call outputs), instructions, voice and
 *   modalities; output = output items, streamed text/transcript and audio
 *   size. Usage (`response.usage`, with cached and audio token details) is
 *   recorded under the session's model.
 * - `openai.realtime.tool.<name>` (tool): a function call requested by the
 *   model, ended when the client sends its `function_call_output`.
 *
 * Audio is never recorded, only its size. For any other websocket client feed
 * the events to a `RealtimeTracer` yourself (`onClientEvent`,
 * `onServerEvent`, `close`).
 */
import type { Call } from '../call.js';
import type { WeftClient } from '../client.js';
import type { OpMeta } from '../op.js';
import { addUsage } from '../usage.js';
import { patchWith } from './common.js';
import { integrationAttributes, integrationClient, integrationOp } from './settings.js';

const INTEGRATION = 'openai_realtime';
const TEXT_DELTAS = new Set(['response.text.delta', 'response.output_text.delta', 'response.audio_transcript.delta', 'response.output_audio_transcript.delta']);
const AUDIO_DELTAS = new Set(['response.audio.delta', 'response.output_audio.delta']);
const RESPONSE_KEYS = ['instructions', 'voice', 'modalities', 'output_modalities', 'tools', 'temperature', 'max_response_output_tokens'];

type Ev = Record<string, any>;
type Span = { call: Call; meta: OpMeta };

function toEvent(e: unknown): Ev | null {
  if (typeof e === 'string' || e instanceof Uint8Array) {
    try {
      e = JSON.parse(typeof e === 'string' ? e : new TextDecoder().decode(e));
    } catch {
      return null;
    }
  }
  return e && typeof e === 'object' && !Array.isArray(e) ? (e as Ev) : null;
}

function b64Len(s: unknown): number {
  if (typeof s !== 'string') return 0;
  const pad = s.endsWith('==') ? 2 : s.endsWith('=') ? 1 : 0;
  return Math.floor((s.length * 3) / 4) - pad;
}

/** Drop base64 audio from items, keeping transcripts and sizes. */
export function stripAudio(v: unknown, depth = 0): unknown {
  if (depth > 8) return v;
  if (Array.isArray(v)) return v.map((x) => stripAudio(x, depth + 1));
  if (v && typeof v === 'object') {
    const out: Record<string, unknown> = {};
    for (const [k, x] of Object.entries(v)) {
      if ((k === 'audio' || k === 'delta') && typeof x === 'string' && x.length > 64) out[`${k}_bytes`] = b64Len(x);
      else out[k] = stripAudio(x, depth + 1);
    }
    return out;
  }
  return v;
}

function parseJson(v: unknown): unknown {
  if (typeof v !== 'string') return v;
  try {
    return JSON.parse(v);
  } catch {
    return v;
  }
}

/** Builds Weft calls from a Realtime session's client and server events. */
export class RealtimeTracer {
  model: string | undefined;
  session: Record<string, unknown> = {};
  sessionCall: Span | null = null;
  client: WeftClient | null = null;
  pending: Ev[] = [];
  pendingAudioBytes = 0;
  responseOverrides: Ev = {};
  responses = new Map<string, { span: Span; text: string; audioBytes: number }>();
  tools = new Map<string, Span>();
  counts = { responses: 0, client_events: 0, server_events: 0 };
  closed = false;

  constructor(opts: { model?: string } = {}) {
    this.model = opts.model;
  }

  private start(name: string, kind: string, inputs: Record<string, unknown>, parent: Call | null, displayName?: string): Span | null {
    if (!this.client) return null;
    const meta = integrationOp(INTEGRATION, name, kind);
    try {
      const call = this.client.createCall(meta, inputs, { parent, displayName, attributes: integrationAttributes(INTEGRATION) });
      return { call, meta };
    } catch {
      return null;
    }
  }

  private end(s: Span | null | undefined, out: unknown, err?: unknown): void {
    if (s && this.client) this.client.finishCall(s.call, out, err, s.meta);
  }

  private ensureSession(): Span | null {
    if (this.sessionCall || this.closed) return this.sessionCall;
    const client = integrationClient(INTEGRATION);
    if (!client) return null;
    this.client = client;
    const { id: _i, object: _o, client_secret: _c, ...cfg } = this.session as Ev;
    this.sessionCall = this.start('openai.realtime.session', 'agent', { model: this.model ?? null, session: stripAudio(cfg) }, null, this.model ? `Realtime: ${this.model}` : undefined);
    return this.sessionCall;
  }

  onClientEvent(event: unknown): void {
    const e = toEvent(event);
    if (!e) return;
    this.counts.client_events++;
    const t = e.type ?? '';
    if (t === 'session.update') {
      const sess = e.session ?? {};
      Object.assign(this.session, sess);
      this.model = sess.model ?? this.model;
    } else if (t === 'input_audio_buffer.append') {
      this.pendingAudioBytes += b64Len(e.audio);
    } else if (t === 'response.create') {
      this.responseOverrides = { ...(e.response ?? {}) };
    } else if (t === 'conversation.item.create') {
      const item = e.item ?? {};
      if (item.type === 'function_call_output') {
        const tool = this.tools.get(item.call_id);
        if (tool) {
          this.tools.delete(item.call_id);
          this.end(tool, parseJson(item.output));
        }
      }
      this.pending.push(stripAudio(item) as Ev);
    }
  }

  onServerEvent(event: unknown): void {
    const e = toEvent(event);
    if (!e) return;
    this.counts.server_events++;
    const t: string = e.type ?? '';
    if (t === 'session.created' || t === 'session.updated') {
      const sess = e.session ?? {};
      Object.assign(this.session, sess);
      this.model = sess.model ?? this.model;
      if (t === 'session.created') this.ensureSession();
    } else if (t === 'conversation.item.input_audio_transcription.completed') {
      const it = this.pending.find((p) => p.id === e.item_id);
      if (it) it.transcript = e.transcript;
      else this.pending.push({ type: 'input_audio_transcript', item_id: e.item_id, transcript: e.transcript });
    } else if (t === 'conversation.item.created' || t === 'conversation.item.added') {
      const item = e.item ?? {};
      if (item.role === 'user' && !(item.id && this.pending.some((p) => p.id === item.id))) this.pending.push(stripAudio(item) as Ev);
    } else if (t === 'response.created') {
      this.responseStart(e.response ?? {});
    } else if (TEXT_DELTAS.has(t)) {
      const st = this.responses.get(e.response_id);
      if (st) st.text += e.delta ?? '';
    } else if (AUDIO_DELTAS.has(t)) {
      const st = this.responses.get(e.response_id);
      if (st) st.audioBytes += b64Len(e.delta);
    } else if (t === 'response.output_item.done') {
      const item = e.item ?? {};
      if (item.type === 'function_call' && this.ensureSession()) {
        const args = parseJson(item.arguments);
        const name = item.name ?? 'function';
        const s = this.start(`openai.realtime.tool.${name}`, 'tool', args && typeof args === 'object' && !Array.isArray(args) ? (args as Record<string, unknown>) : { arguments: args }, this.sessionCall!.call, name);
        if (s) this.tools.set(item.call_id, s);
      }
    } else if (t === 'response.done') {
      this.responseEnd(e.response ?? {});
    } else if (t === 'error') {
      const err = e.error ?? {};
      for (const rid of [...this.responses.keys()]) this.responseEnd({ id: rid, status: 'failed' }, new Error(err.message ?? JSON.stringify(err)));
    }
  }

  private responseStart(resp: Ev): void {
    if (!this.ensureSession()) return;
    const rid = resp.id ?? `resp_${this.responses.size}`;
    const ov = this.responseOverrides;
    this.responseOverrides = {};
    const inputs: Record<string, unknown> = { model: this.model ?? null, input: [...this.pending] };
    if (this.pendingAudioBytes) inputs.input_audio_bytes = this.pendingAudioBytes;
    for (const k of RESPONSE_KEYS) {
      const v = ov[k] ?? this.session[k];
      if (v !== undefined && v !== null) inputs[k] = v;
    }
    this.pending = [];
    this.pendingAudioBytes = 0;
    const span = this.start('openai.realtime.response', 'llm', inputs, this.sessionCall!.call);
    if (!span) return;
    this.responses.set(rid, { span, text: '', audioBytes: 0 });
    this.counts.responses++;
  }

  private responseEnd(resp: Ev, err?: unknown): void {
    const st = this.responses.get(resp.id);
    if (!st) return;
    this.responses.delete(resp.id);
    const call = st.span.call;
    const u = resp.usage;
    if (u && typeof u === 'object') {
      const model = this.model ?? 'unknown';
      addUsage(call, model, u.input_tokens ?? 0, u.output_tokens ?? 0, u.total_tokens);
      const entry = (call.summary.usage as Record<string, Record<string, number>>)[model];
      const extra: Record<string, number> = {
        cached_tokens: u.input_token_details?.cached_tokens ?? 0,
        input_audio_tokens: u.input_token_details?.audio_tokens ?? 0,
        output_audio_tokens: u.output_token_details?.audio_tokens ?? 0,
      };
      for (const [k, v] of Object.entries(extra)) if (v) entry[k] = (entry[k] ?? 0) + v;
    }
    const out: Record<string, unknown> = { id: resp.id, status: resp.status ?? null, output: stripAudio(resp.output ?? []) };
    if (st.text) out.text = st.text;
    if (st.audioBytes) out.audio_bytes = st.audioBytes;
    if (resp.status_details) out.status_details = resp.status_details;
    if (u !== undefined) out.usage = u;
    if (err === undefined && resp.status === 'failed') err = new Error(JSON.stringify(resp.status_details ?? { status: 'failed' }));
    this.end(st.span, out, err);
  }

  /** End open responses, tools and the session (idempotent). */
  close(err?: unknown): void {
    if (this.closed) return;
    this.closed = true;
    for (const rid of [...this.responses.keys()]) this.responseEnd({ id: rid, status: 'incomplete' }, err);
    for (const s of this.tools.values()) this.end(s, null, err);
    this.tools.clear();
    if (this.sessionCall) {
      const { client_secret: _c, ...cfg } = this.session as Ev;
      this.end(this.sessionCall, { session: stripAudio(cfg), ...this.counts }, err);
    }
  }
}

const tracers = new WeakMap<object, RealtimeTracer>();

function modelOf(conn: any): string | undefined {
  try {
    const u = conn?.url;
    const s = typeof u === 'string' ? u : u?.toString?.();
    if (s) return new URL(s).searchParams.get('model') ?? undefined;
  } catch {
    /* no URL */
  }
  return undefined;
}

/** The tracer of a connection (created on first use). */
export function realtimeTracer(conn: object, opts: { model?: string } = {}): RealtimeTracer {
  let t = tracers.get(conn);
  if (!t) {
    t = new RealtimeTracer({ model: opts.model ?? modelOf(conn) });
    tracers.set(conn, t);
  }
  return t;
}

function safe(fn: () => void): void {
  try {
    fn();
  } catch {
    /* never break the connection */
  }
}

function patchConnection(owner: any, model?: string): boolean {
  const a = patchWith(owner, 'send', (orig) =>
    function (this: any, event: unknown, ...rest: unknown[]) {
      safe(() => realtimeTracer(this, { model }).onClientEvent(event));
      return orig.call(this, event, ...rest);
    },
  );
  const b = patchWith(owner, '_emit', (orig) =>
    function (this: any, name: string, ...args: unknown[]) {
      if (name === 'event') safe(() => realtimeTracer(this, { model }).onServerEvent(args[0]));
      else if (name === 'close') safe(() => realtimeTracer(this, { model }).close());
      return orig.call(this, name, ...args);
    },
  );
  const c = patchWith(owner, 'close', (orig) =>
    function (this: any, ...args: unknown[]) {
      try {
        return orig.apply(this, args);
      } finally {
        safe(() => realtimeTracer(this, { model }).close());
      }
    },
  );
  return a || b || c;
}

/**
 * Trace one Realtime connection (`OpenAIRealtimeWS`, `OpenAIRealtimeWebSocket`
 * or any object with `send(event)` and an `_emit('event', ev)` emitter).
 * Returns the same connection.
 */
export function wrapOpenAIRealtime<T extends object>(conn: T, opts: { model?: string } = {}): T {
  patchConnection(conn, opts.model);
  realtimeTracer(conn, opts);
  // End the session when the socket closes without close() being called.
  const sock: any = (conn as any).socket;
  safe(() => {
    const onClose = () => realtimeTracer(conn).close();
    if (typeof sock?.on === 'function') sock.on('close', onClose);
    else if (typeof sock?.addEventListener === 'function') sock.addEventListener('close', onClose);
  });
  return conn;
}

/** Patch a Realtime connection class (`OpenAIRealtimeWS`, `OpenAIRealtimeWebSocket`) so every connection is traced. */
export function instrumentOpenAIRealtime(...classes: any[]): boolean {
  let ok = false;
  for (const cls of classes) if (cls?.prototype) ok = patchConnection(cls.prototype) || ok;
  return ok;
}
