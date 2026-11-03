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
 * Audio is recorded as WAV media, as in Weave: the user's input audio
 * (`input_audio_buffer.append`, cut per item at the server-VAD speech markers
 * or at each commit) on the `input_audio` content parts of a response's
 * inputs, and the model's audio (`response.output_audio.delta`) on the
 * `output_audio` parts of its output. PCM16 (the session's rate, default
 * 24 kHz) and G.711 u-law/A-law are supported. Each clip is capped at
 * `WEFT_REALTIME_MAX_AUDIO_BYTES` (default 25 MiB of raw audio; the server
 * accepts files up to 64 MiB) and longer clips are cut and marked
 * `audio_truncated`; `0` records sizes only.
 *
 * The OpenAI Agents SDK's `RealtimeSession` (`@openai/agents-realtime`, the
 * route Weave documents for TypeScript) is traced with
 * `weft.patchRealtimeSession()` (or `instrumentRealtimeSession(RealtimeSession)`
 * / `wrapRealtimeSession(session)`). For any other websocket client feed the
 * events to a `RealtimeTracer` yourself (`onClientEvent`, `onServerEvent`,
 * `close`).
 */
import { existsSync, readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join, resolve as resolvePath } from 'node:path';
import { pathToFileURL } from 'node:url';
import type { Call } from '../call.js';
import type { WeftClient } from '../client.js';
import type { OpMeta } from '../op.js';
import { Audio } from '../content.js';
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
  if (v && typeof v === 'object' && [Object.prototype, null].includes(Object.getPrototypeOf(v))) {
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

// ------------------------------------------------------------------ audio
/** Default per-clip cap on recorded audio (raw bytes). */
export const DEFAULT_MAX_AUDIO_BYTES = 25 * 1024 * 1024;

function envMaxAudioBytes(): number {
  const raw = (process.env.WEFT_REALTIME_MAX_AUDIO_BYTES ?? '').trim();
  if (raw) {
    const n = Number(raw);
    if (Number.isFinite(n)) return Math.max(0, Math.floor(n));
  }
  return DEFAULT_MAX_AUDIO_BYTES;
}

/** WAV parameters: format tag (1 PCM, 6 A-law, 7 u-law), bytes per sample, sample rate. */
export interface AudioFormat {
  tag: number;
  width: number;
  rate: number;
}
const PCM16: AudioFormat = { tag: 1, width: 2, rate: 24000 };
const ULAW: AudioFormat = { tag: 7, width: 1, rate: 8000 };
const ALAW: AudioFormat = { tag: 6, width: 1, rate: 8000 };

/** WAV parameters for a session audio format (beta `"pcm16"`/`"g711_ulaw"` or GA `{type: "audio/pcm", rate}`). */
export function audioFormat(fmt: unknown): AudioFormat {
  if (fmt && typeof fmt === 'object') {
    const f = fmt as Ev;
    const t = String(f.type ?? 'audio/pcm').toLowerCase();
    if (t === 'audio/pcmu' || t === 'g711_ulaw') return ULAW;
    if (t === 'audio/pcma' || t === 'g711_alaw') return ALAW;
    return { tag: 1, width: 2, rate: typeof f.rate === 'number' && f.rate > 0 ? f.rate : 24000 };
  }
  const s = String(fmt ?? 'pcm16').toLowerCase();
  if (s === 'g711_ulaw' || s === 'audio/pcmu') return ULAW;
  if (s === 'g711_alaw' || s === 'audio/pcma') return ALAW;
  return PCM16;
}

/** Wrap raw mono PCM16 (or G.711) audio in a WAV container. */
export function pcmToWav(pcm: Uint8Array, fmt: AudioFormat = PCM16, channels = 1): Uint8Array {
  let data = Buffer.from(pcm.buffer, pcm.byteOffset, pcm.byteLength);
  if (fmt.width === 2 && data.length % 2) data = Buffer.concat([data, Buffer.alloc(1)]);
  const block = channels * fmt.width;
  const pcmFmt = fmt.tag === 1;
  const fmtLen = pcmFmt ? 16 : 18;
  const factLen = pcmFmt ? 0 : 12;
  const out = Buffer.alloc(12 + 8 + fmtLen + factLen + 8 + data.length);
  let o = 0;
  out.write('RIFF', o); out.writeUInt32LE(out.length - 8, o + 4); out.write('WAVE', o + 8); o += 12;
  out.write('fmt ', o); out.writeUInt32LE(fmtLen, o + 4); o += 8;
  out.writeUInt16LE(fmt.tag, o); out.writeUInt16LE(channels, o + 2); out.writeUInt32LE(fmt.rate, o + 4);
  out.writeUInt32LE(fmt.rate * block, o + 8); out.writeUInt16LE(block, o + 12); out.writeUInt16LE(fmt.width * 8, o + 14); o += 16;
  if (!pcmFmt) {
    out.writeUInt16LE(0, o); o += 2; // cbSize
    out.write('fact', o); out.writeUInt32LE(4, o + 4); out.writeUInt32LE(Math.floor(data.length / block), o + 8); o += 12;
  }
  out.write('data', o); out.writeUInt32LE(data.length, o + 4); o += 8;
  data.copy(out, o);
  return new Uint8Array(out.buffer, out.byteOffset, out.length);
}

function b64(s: unknown): Buffer {
  return typeof s === 'string' && s ? Buffer.from(s, 'base64') : Buffer.alloc(0);
}

/** Bytes addressed by absolute offset (from the start of the session's input audio), stored as chunks. */
class ByteQueue {
  private chunks: Buffer[] = [];
  base = 0;
  length = 0;
  commitFrom = 0;

  get total(): number {
    return this.base + this.length;
  }

  append(b: Buffer, cap: number): void {
    if (!b.length) return;
    this.chunks.push(b);
    this.length += b.length;
    if (cap > 0 && this.length > cap) this.trim(this.total - cap);
  }

  trim(upto: number): void {
    let n = Math.min(Math.max(0, upto - this.base), this.length);
    while (n > 0 && this.chunks.length) {
      const c = this.chunks[0];
      if (c.length <= n) {
        this.chunks.shift();
        n -= c.length;
        this.base += c.length;
        this.length -= c.length;
      } else {
        this.chunks[0] = c.subarray(n);
        this.base += n;
        this.length -= n;
        n = 0;
      }
    }
  }

  clear(): void {
    this.base = this.total;
    this.chunks = [];
    this.length = 0;
    this.commitFrom = this.base;
  }

  segment(a: number, b: number): Buffer {
    a = Math.max(a, this.base);
    b = Math.min(b, this.total);
    if (b <= a) return Buffer.alloc(0);
    const parts: Buffer[] = [];
    let off = this.base;
    for (const c of this.chunks) {
      const end = off + c.length;
      if (end > a && off < b) parts.push(c.subarray(Math.max(0, a - off), Math.min(c.length, b - off)));
      off = end;
      if (off >= b) break;
    }
    return Buffer.concat(parts);
  }
}

type ResponseState = { span: Span; text: string; audioBytes: number; audio: Map<string | undefined, { chunks: Buffer[]; bytes: number }> };

/** Builds Weft calls from a Realtime session's client and server events. */
export class RealtimeTracer {
  model: string | undefined;
  /** Per-clip cap on recorded audio (raw bytes; 0 records sizes only). */
  maxAudioBytes: number;
  session: Record<string, unknown> = {};
  sessionCall: Span | null = null;
  client: WeftClient | null = null;
  pending: Ev[] = [];
  pendingAudioBytes = 0;
  responseOverrides: Ev = {};
  responses = new Map<string, ResponseState>();
  tools = new Map<string, Span>();
  counts = { responses: 0, client_events: 0, server_events: 0 };
  closed = false;
  private input = new ByteQueue();
  private markers = new Map<string, { start?: number; end?: number }>();
  private itemAudio = new Map<string, Buffer>();

  constructor(opts: { model?: string; maxAudioBytes?: number } = {}) {
    this.model = opts.model;
    this.maxAudioBytes = opts.maxAudioBytes !== undefined ? Math.max(0, Math.floor(opts.maxAudioBytes)) : envMaxAudioBytes();
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

  // ---------------------------------------------------------------- audio
  private inputFormat(): AudioFormat {
    const s = this.session as Ev;
    return audioFormat(s.input_audio_format ?? s.audio?.input?.format);
  }

  private outputFormat(): AudioFormat {
    const s = this.session as Ev;
    return audioFormat(s.output_audio_format ?? s.audio?.output?.format);
  }

  /** `{audio: Audio(WAV), audio_bytes}` for a content part (sizes only when capture is off). */
  private media(pcm: Buffer, fmt: AudioFormat, name: string, truncated = false): Record<string, unknown> {
    const out: Record<string, unknown> = { audio_bytes: pcm.length };
    if (!pcm.length || this.maxAudioBytes <= 0) return out;
    if (pcm.length > this.maxAudioBytes) {
      pcm = pcm.subarray(0, this.maxAudioBytes - (this.maxAudioBytes % fmt.width));
      truncated = true;
    }
    out.audio = new Audio(pcmToWav(pcm, fmt), { mimetype: 'audio/wav', filename: `${name || 'audio'}.wav` });
    if (truncated) out.audio_truncated = true;
    return out;
  }

  private msToOffset(ms: number): number {
    const f = this.inputFormat();
    return Math.floor((ms * f.rate) / 1000) * f.width;
  }

  private committed(itemId: string | undefined): void {
    if (this.maxAudioBytes <= 0 || !itemId) return;
    const m = this.markers.get(itemId);
    this.markers.delete(itemId);
    let a: number;
    let b: number;
    if (m && m.start !== undefined && m.end !== undefined) {
      a = this.msToOffset(m.start);
      b = this.msToOffset(m.end);
    } else {
      a = this.input.commitFrom;
      b = this.input.total;
    }
    b = Math.min(b, this.input.total);
    const pcm = this.input.segment(a, b);
    if (pcm.length) {
      this.itemAudio.set(itemId, pcm);
      while (this.itemAudio.size > 64) this.itemAudio.delete(this.itemAudio.keys().next().value as string);
    }
    this.input.commitFrom = Math.max(this.input.commitFrom, b);
    this.input.trim(this.input.commitFrom);
  }

  /** A copy of a conversation item with its input audio as WAV media. */
  private withAudio(item: Ev, fromClient = false): Ev {
    const content = item.content;
    if (!Array.isArray(content) || !content.some((p: any) => p?.type === 'input_audio')) return stripAudio(item) as Ev;
    const parts = content.map((p: any, i: number) => {
      if (p?.type !== 'input_audio') return stripAudio(p);
      if (p.audio instanceof Audio) return p;
      const { audio, ...rest } = p;
      let pcm = typeof audio === 'string' ? b64(audio) : Buffer.alloc(0);
      if (!pcm.length && !fromClient && item.id) {
        pcm = this.itemAudio.get(item.id) ?? pcm;
        this.itemAudio.delete(item.id);
      }
      return pcm.length ? { ...rest, ...this.media(pcm, this.inputFormat(), `${item.id ?? 'input'}_${i}`) } : rest;
    });
    const out: Ev = {};
    for (const [k, v] of Object.entries(item)) out[k] = k === 'content' ? parts : stripAudio(v);
    return out;
  }

  // ---------------------------------------------------------------- events
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
      if (this.maxAudioBytes > 0) this.input.append(b64(e.audio), 2 * this.maxAudioBytes);
    } else if (t === 'input_audio_buffer.clear') {
      this.input.clear();
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
      this.pending.push(this.withAudio(item, true));
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
    } else if (t === 'input_audio_buffer.speech_started') {
      this.markers.set(e.item_id ?? '', { start: e.audio_start_ms });
    } else if (t === 'input_audio_buffer.speech_stopped') {
      const m = this.markers.get(e.item_id ?? '') ?? {};
      m.end = e.audio_end_ms;
      this.markers.set(e.item_id ?? '', m);
    } else if (t === 'input_audio_buffer.committed') {
      this.committed(e.item_id);
    } else if (t === 'input_audio_buffer.cleared') {
      this.input.clear();
    } else if (t === 'conversation.item.input_audio_transcription.completed') {
      const it = this.pending.find((p) => p.id === e.item_id);
      if (it) it.transcript = e.transcript;
      else this.pending.push({ type: 'input_audio_transcript', item_id: e.item_id, transcript: e.transcript });
    } else if (t === 'conversation.item.created' || t === 'conversation.item.added') {
      const item = e.item ?? {};
      if (item.role === 'user' && !(item.id && this.pending.some((p) => p.id === item.id))) this.pending.push({ ...item });
    } else if (t === 'response.created') {
      this.responseStart(e.response ?? {});
    } else if (TEXT_DELTAS.has(t)) {
      const st = this.responses.get(e.response_id);
      if (st) st.text += e.delta ?? '';
    } else if (AUDIO_DELTAS.has(t)) {
      const st = this.responses.get(e.response_id);
      if (st) {
        st.audioBytes += b64Len(e.delta);
        if (this.maxAudioBytes > 0) {
          let buf = st.audio.get(e.item_id);
          if (!buf) st.audio.set(e.item_id, (buf = { chunks: [], bytes: 0 }));
          if (buf.bytes <= this.maxAudioBytes) {
            const d = b64(e.delta);
            buf.chunks.push(d);
            buf.bytes += d.length;
          }
        }
      }
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
    const items = this.pending.map((it) => (it.type === 'message' ? this.withAudio(it) : (stripAudio(it) as Ev)));
    const inputs: Record<string, unknown> = { model: this.model ?? null, input: items };
    if (this.pendingAudioBytes) inputs.input_audio_bytes = this.pendingAudioBytes;
    for (const k of RESPONSE_KEYS) {
      const v = ov[k] ?? this.session[k];
      if (v !== undefined && v !== null) inputs[k] = v;
    }
    this.pending = [];
    this.pendingAudioBytes = 0;
    const span = this.start('openai.realtime.response', 'llm', inputs, this.sessionCall!.call);
    if (!span) return;
    this.responses.set(rid, { span, text: '', audioBytes: 0, audio: new Map() });
    this.counts.responses++;
  }

  private responseOutput(st: ResponseState, output: unknown): unknown {
    const items = stripAudio(output ?? []);
    if (!st.audio.size || !Array.isArray(items)) return items;
    const fmt = this.outputFormat();
    for (const item of items as Ev[]) {
      if (!item || item.type !== 'message' || !Array.isArray(item.content)) continue;
      const buf = st.audio.get(item.id);
      if (!buf) continue;
      st.audio.delete(item.id);
      const idx = item.content.findIndex((p: any) => p && (p.type === 'audio' || p.type === 'output_audio'));
      if (idx < 0) continue;
      const { audio_bytes: _b, ...part } = item.content[idx];
      item.content[idx] = { ...part, ...this.media(Buffer.concat(buf.chunks), fmt, `${item.id ?? 'output'}_${idx}`, buf.bytes > this.maxAudioBytes) };
    }
    return items;
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
    const out: Record<string, unknown> = { id: resp.id, status: resp.status ?? null, output: this.responseOutput(st, resp.output) };
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
    this.input = new ByteQueue();
    this.itemAudio.clear();
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

// ------------------------------------------------------------------ OpenAI Agents SDK RealtimeSession
const SESSION_STATE = Symbol.for('weft.realtimeSession');
type SessionState = { tracer: RealtimeTracer };

function sessionModel(session: any): string | undefined {
  const m = session?.options?.model;
  return typeof m === 'string' ? m : undefined;
}

/**
 * Trace one `RealtimeSession` from `@openai/agents-realtime`: raw server events
 * arrive through the session's `transport_event`, client events through its
 * transport's `sendEvent` (which `sendAudio`, `sendMessage` and tool outputs go
 * through), and a disconnect or `close()` ends the session call. Each
 * `connect()` after a close starts a new session call. Returns the session.
 */
export function wrapRealtimeSession<T extends object>(session: T): T {
  const s: any = session;
  if (!s || typeof s.on !== 'function') return session;
  let st: SessionState | undefined = s[SESSION_STATE];
  if (st) {
    if (st.tracer.closed) st.tracer = new RealtimeTracer({ model: sessionModel(s) });
    return session;
  }
  st = { tracer: new RealtimeTracer({ model: sessionModel(s) }) };
  const state = st;
  Object.defineProperty(s, SESSION_STATE, { value: st, configurable: true });
  s.on('transport_event', (ev: unknown) => safe(() => state.tracer.onServerEvent(ev)));
  const transport = s.transport;
  if (transport) {
    patchWith(transport, 'sendEvent', (orig) =>
      function (this: any, event: unknown, ...rest: unknown[]) {
        safe(() => state.tracer.onClientEvent(event));
        return orig.call(this, event, ...rest);
      },
    );
    safe(() =>
      transport.on?.('connection_change', (status: unknown) => {
        if (status === 'disconnected') state.tracer.close();
      }),
    );
  }
  return session;
}

/** Patch a `RealtimeSession` class so every session is traced from its `connect()`. */
export function instrumentRealtimeSession(RealtimeSession: any): boolean {
  const proto = RealtimeSession?.prototype;
  if (!proto || typeof proto.connect !== 'function') return false;
  const a = patchWith(proto, 'connect', (orig) =>
    function (this: any, ...args: unknown[]) {
      safe(() => wrapRealtimeSession(this));
      return orig.apply(this, args);
    },
  );
  const b = patchWith(proto, 'close', (orig) =>
    function (this: any, ...args: unknown[]) {
      try {
        return orig.apply(this, args);
      } finally {
        safe(() => (this[SESSION_STATE] as SessionState | undefined)?.tracer.close());
      }
    },
  );
  return a || b;
}

const REALTIME_PACKAGE = '@openai/agents-realtime';

/** Directories the application resolves packages from: the working directory and the entry script's. */
function appBases(): string[] {
  const out: string[] = [];
  const add = (d: string | undefined) => {
    if (d && !out.includes(d)) out.push(d);
  };
  add(process.cwd());
  if (process.argv[1]) add(dirname(resolvePath(process.argv[1])));
  return out;
}

/** The target of a package.json "exports" entry under `conditions` (Node's order: the first matching key wins). */
function exportTarget(entry: unknown, conditions: string[]): string | undefined {
  if (typeof entry === 'string') return entry;
  if (Array.isArray(entry)) {
    for (const e of entry) {
      const t = exportTarget(e, conditions);
      if (t) return t;
    }
    return undefined;
  }
  if (!entry || typeof entry !== 'object') return undefined;
  const obj = entry as Record<string, unknown>;
  if ('.' in obj) return exportTarget(obj['.'], conditions);
  for (const [k, v] of Object.entries(obj)) {
    if (conditions.includes(k)) {
      const t = exportTarget(v, conditions);
      if (t) return t;
    }
  }
  return undefined;
}

/**
 * The ESM entry file of `name` as an `import` from `base` would resolve it:
 * the nearest node_modules/<name> up from base, then its "exports" ("import"
 * condition), "module" or "main".
 */
export function resolveEsmEntry(name: string, base: string): string | undefined {
  for (let dir = resolvePath(base); ; ) {
    const pkgDir = join(dir, 'node_modules', ...name.split('/'));
    const pkgFile = join(pkgDir, 'package.json');
    if (existsSync(pkgFile)) {
      try {
        const pkg = JSON.parse(readFileSync(pkgFile, 'utf8'));
        const rel =
          pkg.exports !== undefined
            ? exportTarget(pkg.exports, ['node', 'import', 'default'])
            : (pkg.module ?? pkg.main ?? 'index.js');
        if (typeof rel === 'string') {
          const file = join(pkgDir, rel);
          if (existsSync(file)) return file;
        }
      } catch {
        /* unreadable package.json: keep looking */
      }
    }
    const parent = dirname(dir);
    if (parent === dir) return undefined;
    dir = parent;
  }
}

/**
 * Every copy of the realtime package the application may use: the one this
 * SDK resolves, and, from the application's directories, its ESM build (what
 * `import` loads) and its CommonJS build (what `require` loads; dual packages
 * have one class per build).
 */
async function loadRealtimeModules(): Promise<any[]> {
  const mods: any[] = [];
  const name = REALTIME_PACKAGE;
  try {
    mods.push(await import(name));
  } catch {
    /* not resolvable from the SDK's own location (linked or global install) */
  }
  const files = new Set<string>();
  for (const base of appBases()) {
    const esm = resolveEsmEntry(name, base);
    if (esm) files.add(esm);
    try {
      files.add(createRequire(join(base, 'noop.js')).resolve(name));
    } catch {
      /* not installed for the application, or ESM-only */
    }
  }
  for (const f of files) {
    try {
      mods.push(await import(pathToFileURL(f).href));
    } catch {
      /* skip a copy that does not load */
    }
  }
  return mods;
}

/**
 * Weave's `patchRealtimeSession()`: trace every `RealtimeSession` from
 * `@openai/agents-realtime`. Pass the module (`await import('@openai/agents-realtime')`)
 * or let Weft find it: from the SDK's own location, and from the application's
 * (the working directory and the entry script's directory), both its ESM and
 * CommonJS builds. Idempotent; resolves to `false` when the package cannot be
 * loaded.
 */
export async function patchRealtimeSession(mod?: { RealtimeSession?: any }): Promise<boolean> {
  const mods: any[] = mod ? [mod] : await loadRealtimeModules();
  const classes = new Set<any>();
  for (const m of mods) {
    const cls = m?.RealtimeSession ?? m?.default?.RealtimeSession;
    if (cls?.prototype) classes.add(cls);
  }
  for (const cls of classes) instrumentRealtimeSession(cls);
  return classes.size > 0;
}
