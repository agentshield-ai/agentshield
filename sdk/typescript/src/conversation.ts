/**
 * Agent conversation tracing: conversations, turns, LLM calls, tools and sub-agents.
 *
 * Mirrors Weave's conversation SDK (`weave.start_conversation` & co.) with the
 * same semantics; names are camelCase (`startConversation`, `startTurn`,
 * `startLLM`, `startTool`, `startSubagent`, `logTurn`, `logConversation`).
 * Each span is recorded as a Weft call the server's agents index reads:
 * turns and sub-agents are `invoke_agent` calls (`attributes.gen_ai.agent.name`,
 * `attributes.weft.span_kind = "agent"`), LLM calls are `chat` calls with
 * `inputs.messages`, an OpenAI-style output and `summary.usage`, tools are
 * `execute_tool` calls. A conversation's id is the calls' `thread_id`; a turn's
 * `turn_id` is its own id. Spans are sent when they end.
 *
 * ```ts
 * const conv = weft.startConversation({ agentName: 'support' });
 * await conv.startTurn({ userMessage: 'Where is my order?' }).run(async (turn) => {
 *   await turn.startLLM({ model: 'gpt-4o-mini', providerName: 'openai' }).run((llm) => {
 *     llm.inputMessages = [weft.Message.user('Where is my order?')];
 *     llm.output('Let me check.');
 *   });
 *   await turn.startTool({ name: 'lookup', arguments: { id: 42 } }).run((tool) => { tool.result = { status: 'shipped' }; });
 *   turn.outputMessages = [weft.Message.assistant('It has shipped.')];
 * });
 * conv.end();
 * ```
 *
 * `span.run(fn)` is the equivalent of Python's `with`: the span is current
 * while `fn` runs (ops called inside become its children), errors are
 * recorded and re-thrown, and the span ends afterwards. `start()`/`end()` work
 * too; children created with `turn.startLLM()` & co. always nest under their
 * parent.
 */
import { logger } from './log.js';
import { randomUUID } from 'node:crypto';
import { Call, type CallData } from './call.js';
import { formatError, syntheticOp, type WeftClient } from './client.js';
import { currentContext, getCurrentCall, runWith, toTeam, type TeamInfo, type WeftContext } from './context.js';
import { reasoningStep } from './reasoning.js';
import { tracingClient } from './global.js';
import { VERSION } from './http.js';
import { uuid7 } from './ids.js';
import { bytesToMedia, toJson } from './serialize.js';

// ---------------------------------------------------------------------------
// data types

export type MessagePart =
  | { type: 'text'; content: string }
  | { type: 'reasoning'; content: string }
  | { type: 'tool_call'; id?: string; name: string; arguments?: unknown }
  | { type: 'tool_call_response'; id?: string; response?: unknown }
  | { type: 'blob'; mime_type?: string; modality?: string; content: string | Uint8Array }
  | { type: 'uri'; mime_type?: string; modality?: string; uri: string }
  | { type: 'file'; mime_type?: string; modality?: string; file_id: string };

export interface Message {
  role: 'user' | 'assistant' | 'system' | 'tool';
  content?: string;
  toolCallId?: string;
  toolName?: string;
  parts?: MessagePart[];
}

/** Message builders (as in Weave's `Message.user(...)` etc.). */
export const Message = {
  user: (text: string): Message => ({ role: 'user', content: text }),
  system: (text: string): Message => ({ role: 'system', content: text }),
  assistant: (text = '', opts: { toolCalls?: Array<{ id?: string; name: string; arguments?: unknown }> } = {}): Message => {
    if (!opts.toolCalls?.length) return { role: 'assistant', content: text };
    const parts: MessagePart[] = text ? [{ type: 'text', content: text }] : [];
    for (const tc of opts.toolCalls) parts.push({ type: 'tool_call', id: tc.id ?? '', name: tc.name, arguments: tc.arguments ?? '' });
    return { role: 'assistant', parts };
  },
  toolResult: (callId: string, output: unknown): Message => ({ role: 'tool', parts: [{ type: 'tool_call_response', id: callId, response: output }] }),
};

export interface Usage {
  inputTokens?: number;
  outputTokens?: number;
  /** Provider-reported total (input + output when absent). */
  totalTokens?: number | null;
  reasoningTokens?: number;
  cacheCreationInputTokens?: number;
  cacheReadInputTokens?: number;
}

export interface LogResult {
  conversationId: string;
  traceIds: string[];
  rootSpanIds: string[];
  spanCount: number;
}

interface ConvInfo {
  conversationId: string;
  conversationName: string;
  includeContent: boolean;
  attributes: Record<string, unknown>;
  team?: TeamInfo | null;
}

/** A team for the `team` options: `{id, name?}`, an id, or `[id, name]`. */
export type TeamLike = TeamInfo | string | [string, string?];

// ---------------------------------------------------------------------------
// helpers

function jsonString(v: unknown): string {
  if (v === null || v === undefined) return '';
  if (typeof v === 'string') return v;
  return JSON.stringify(v);
}

function parseMaybeJson(v: unknown): unknown {
  if (typeof v !== 'string') return v ?? null;
  if (v === '') return null;
  try {
    return JSON.parse(v);
  } catch {
    return v;
  }
}

function spanName(op: string, target: string): string {
  const t = (target ?? '').trim();
  return t ? `${op} ${t}` : op;
}

function setPath(d: Record<string, any>, dotted: string, value: unknown): void {
  const parts = dotted.split('.');
  let cur = d;
  for (const p of parts.slice(0, -1)) {
    if (!cur[p] || typeof cur[p] !== 'object' || Array.isArray(cur[p])) cur[p] = {};
    cur = cur[p];
  }
  cur[parts[parts.length - 1]] = value;
}

function nest(flat: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const k of Object.keys(flat).sort((a, b) => a.split('.').length - b.split('.').length)) setPath(out, k, flat[k]);
  return out;
}

function deepMerge(base: Record<string, any>, extra: Record<string, any>): Record<string, any> {
  const out: Record<string, any> = { ...base };
  for (const [k, v] of Object.entries(extra)) {
    if (v && typeof v === 'object' && !Array.isArray(v) && out[k] && typeof out[k] === 'object' && !Array.isArray(out[k])) out[k] = deepMerge(out[k], v);
    else out[k] = v;
  }
  return out;
}

function parseDataUrl(url: string): [string, string] {
  if (!url.startsWith('data:')) return ['', ''];
  const rest = url.slice(5);
  const i = rest.indexOf(',');
  return [rest.slice(0, i).split(';')[0], rest.slice(i + 1)];
}

function mediaValue(mimeType: string | undefined, modality: string | undefined, data: Uint8Array, client: WeftClient | null): Record<string, unknown> {
  const mt = mimeType || 'application/octet-stream';
  const top = mt.split('/')[0];
  const mod = modality || (['image', 'audio', 'video'].includes(top) ? top : '');
  const v = bytesToMedia(data, client, mt);
  if (typeof v === 'string') return { type: 'uri', uri: v, mime_type: mt, ...(mod ? { modality: mod } : {}) };
  const d = (v as Record<string, unknown>).file_digest;
  if (mod === 'image') return { type: 'image', _type: 'Image', file_digest: d, mime_type: mt };
  if (mod === 'audio') return { type: 'audio', _type: 'Audio', file_digest: d, mime_type: mt };
  if (mod === 'video') return { type: 'file', _type: 'Video', file_digest: d, mime_type: mt, modality: 'video' };
  return { type: 'file', _type: 'Content', file_digest: d, mimetype: mt, mime_type: mt };
}

function partValue(p: MessagePart, client: WeftClient | null): Record<string, unknown> {
  switch (p.type) {
    case 'blob': {
      const data = typeof p.content === 'string' ? new Uint8Array(Buffer.from(p.content, 'base64')) : p.content;
      return mediaValue(p.mime_type, p.modality, data, client);
    }
    case 'uri': {
      if (p.uri.startsWith('data:')) {
        const [mt, payload] = parseDataUrl(p.uri);
        return partValue({ type: 'blob', mime_type: p.mime_type || mt, modality: p.modality, content: payload }, client);
      }
      return { type: 'uri', uri: p.uri, ...(p.mime_type ? { mime_type: p.mime_type } : {}), ...(p.modality ? { modality: p.modality } : {}) };
    }
    case 'file':
      return { type: 'file', file_id: p.file_id, ...(p.mime_type ? { mime_type: p.mime_type } : {}), ...(p.modality ? { modality: p.modality } : {}) };
    case 'reasoning':
      return { type: 'thinking', thinking: p.content };
    case 'text':
      return { type: 'text', text: p.content };
    default:
      return { ...(p as Record<string, unknown>) };
  }
}

/** A Message as an OpenAI-style chat message (tool_calls, tool_call_id, content parts). */
export function chatMessage(msg: Message, client: WeftClient | null, extra?: Array<Record<string, unknown>>): Record<string, unknown> {
  const out: Record<string, unknown> = { role: msg.role };
  if (msg.toolName) out.name = msg.toolName;
  if (!msg.parts?.length) {
    if (msg.role === 'tool' && msg.toolCallId) out.tool_call_id = msg.toolCallId;
    out.content = extra?.length ? [...(msg.content ? [{ type: 'text', text: msg.content }] : []), ...extra] : (msg.content ?? '');
    return out;
  }
  const texts: string[] = [];
  const content: Array<Record<string, unknown>> = [];
  const toolCalls: Array<Record<string, unknown>> = [];
  let responses = 0;
  for (const p of msg.parts) {
    if (p.type === 'text') {
      texts.push(p.content);
      content.push({ type: 'text', text: p.content });
    } else if (p.type === 'tool_call') {
      toolCalls.push({ id: p.id ?? '', type: 'function', function: { name: p.name, arguments: jsonString(p.arguments) } });
    } else if (p.type === 'tool_call_response') {
      responses++;
      out.tool_call_id = p.id ?? '';
      const r = jsonString(p.response);
      texts.push(r);
      content.push({ type: 'text', text: r });
    } else content.push(partValue(p, client));
  }
  if (extra?.length) content.push(...extra);
  if (responses && responses === msg.parts.length) out.role = 'tool';
  out.content = content.length > texts.length ? content : texts.join('\n');
  if (toolCalls.length) out.tool_calls = toolCalls;
  return out;
}

function messagesWithSystem(system: string[], messages: Message[], client: WeftClient | null, media?: Array<Record<string, unknown>>): Array<Record<string, unknown>> {
  const out: Array<Record<string, unknown>> = system.filter(Boolean).map((s) => ({ role: 'system', content: s }));
  let lastUser = -1;
  messages.forEach((m, i) => {
    if (m.role === 'user') lastUser = i;
  });
  messages.forEach((m, i) => out.push(chatMessage(m, client, i === lastUser ? media : undefined)));
  if (media?.length && lastUser < 0) out.push({ role: 'user', content: [...media] });
  return out;
}

function systemPartsJson(instructions: string[]): string {
  return JSON.stringify(instructions.map((s) => ({ type: 'text', content: s })));
}

function errorType(e: unknown): string {
  return e instanceof Error ? e.name || e.constructor?.name || 'Error' : typeof e;
}

function iso(d: Date): string {
  return d.toISOString();
}

// ---------------------------------------------------------------------------
// active conversation / turn / llm (async context first, then the last started one)

let activeConversation: Conversation | null = null;
let activeTurn: Turn | null = null;
let activeLLM: LLM | null = null;

export function getCurrentConversation(): Conversation | null {
  const c = currentContext().conversation as Conversation | undefined;
  return c ?? (activeConversation && !activeConversation.ended ? activeConversation : null);
}

export function getCurrentTurn(): Turn | null {
  const t = currentContext().turn as Turn | undefined;
  return t ?? (activeTurn && !activeTurn.ended ? activeTurn : null);
}

export function getCurrentLLM(): LLM | null {
  const l = currentContext().llm as LLM | undefined;
  return l ?? (activeLLM && !activeLLM.ended ? activeLLM : null);
}

let agentNameOverride: string | null = null;

/** Name the agent of turns and sub-agents started in `fn` that do not name one. */
export function withAgentNameOverride<T>(agentName: string, fn: () => T): T {
  if (!agentName?.trim()) throw new Error('agentName must be a non-empty string');
  const prev = agentNameOverride;
  agentNameOverride = agentName;
  try {
    return fn();
  } finally {
    agentNameOverride = prev;
  }
}

function evalLinkAttributes(): Record<string, unknown> {
  const ctx = currentContext().evalSpan;
  if (!ctx) return {};
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(ctx)) if (v !== null && v !== undefined) out[`weave.eval.${k}`] = v;
  return out;
}

function activeTracingClient(): WeftClient | null {
  return tracingClient();
}

// ---------------------------------------------------------------------------
// span base

type Payload = [Record<string, unknown>, unknown, Record<string, unknown>, Record<string, unknown>];

export abstract class SpanBase {
  startedAt: Date | null = null;
  endedAt: Date | null = null;
  /** The Weft call recording this span (null until started, or when tracing is off). */
  call: Call | null = null;
  /** @internal */ client: WeftClient | null = null;
  /** @internal */ conv: ConvInfo | null = null;
  /** @internal */ explicitParent: SpanBase | null = null;
  /** @internal */ fallbackParent: SpanBase | null = null;
  /** @internal */ started = false;
  /** @internal */ ended = false;
  protected newTrace = false;
  protected extraAttrs: Record<string, unknown> = {};
  protected ctxAttrs: Record<string, unknown> = {};
  protected evalAttrs: Record<string, unknown> = {};
  protected exception: string | null = null;
  protected errType: string | null = null;
  /** @internal */ team: TeamInfo | null = null;
  /** @internal `reasoning()` steps recorded on this span. */ reasoningSteps: Array<Record<string, unknown>> = [];
  protected abstract readonly opName: string;
  protected abstract readonly kind: string;

  get callId(): string | null {
    return this.call?.id ?? null;
  }

  get traceId(): string | null {
    return this.call?.traceId ?? null;
  }

  protected abstract displayName(): string;
  protected abstract payload(conv: ConvInfo, client: WeftClient): Payload;

  protected convInfo(): ConvInfo {
    if (this.conv) return this.conv;
    const c = getCurrentConversation();
    return c ? c.info() : { conversationId: '', conversationName: '', includeContent: true, attributes: {} };
  }

  protected pin<T extends SpanBase>(child: T): T {
    child.explicitParent = this;
    if (!child.conv && this.conv) child.conv = this.conv;
    return child;
  }

  /** Start the span (its call id, parent and trace are fixed now). */
  start(): this {
    if (this.started) return this;
    this.started = true;
    this.startedAt ??= new Date();
    this.conv ??= this.convInfo();
    const client = this.client ?? activeTracingClient();
    if (!client) return this;
    this.client = client;
    this.ctxAttrs = { ...currentContext().attributes };
    this.team = this.conv.team ?? currentContext().team ?? null;
    this.evalAttrs = evalLinkAttributes();
    let parent: Call | null = null;
    if (this.explicitParent) {
      this.explicitParent.start();
      parent = this.explicitParent.call;
    } else if (!this.newTrace) {
      parent = getCurrentCall();
      if (!parent && this.fallbackParent && !this.fallbackParent.ended) {
        this.fallbackParent.start();
        parent = this.fallbackParent.call;
      }
    }
    const id = uuid7();
    const threadId = this.conv.conversationId || parent?.threadId || null;
    const turnId = this instanceof Turn ? id : (parent?.turnId ?? null);
    const data: CallData = {
      id,
      project_id: client.projectId,
      op_name: client.opRef(syntheticOp(this.opName, this.kind)).uri(),
      trace_id: parent ? parent.traceId : uuid7(),
      parent_id: parent ? parent.id : null,
      display_name: this.displayName(),
      started_at: iso(this.startedAt),
    };
    this.call = new Call(data, client);
    this.call.conversationSpan = true;
    this.call.span = this;
    this.call.threadId = threadId;
    this.call.turnId = turnId;
    return this;
  }

  /** End the span and send it. */
  end(): void {
    if (this.ended) return;
    this.ended = true;
    this.endedAt ??= new Date();
    if (!this.started) this.start();
    this.onEnd();
    this.emit();
  }

  /** @internal */ protected onEnd(): void {}

  /** Context patch while `run` executes (current call, turn, llm). */
  protected runPatch(): Partial<WeftContext> {
    const ctx = currentContext();
    return this.call ? { stack: [...ctx.stack, this.call] } : {};
  }

  /**
   * Start the span, run `fn` with it as the current call (ops inside become
   * its children), record a thrown error, end the span and return fn's result.
   */
  async run<T>(fn: (span: this) => T | Promise<T>): Promise<T> {
    this.start();
    try {
      return await runWith(this.runPatch(), () => fn(this));
    } catch (e) {
      this.recordError(e);
      throw e;
    } finally {
      this.end();
    }
  }

  /** Mark the span failed (it still has to end). */
  recordError(error: unknown): this {
    if (this.ended) {
      logger.warn('weft: recordError ignored: span already ended');
      return this;
    }
    this.exception = formatError(error);
    this.errType = errorType(error);
    return this;
  }

  /** Add attributes to the span (any time before it ends). */
  setAttributes(attributes: Record<string, unknown>): this {
    if (this.ended) {
      logger.warn(`weft: setAttributes(${Object.keys(attributes).join(', ')}) ignored: span already ended`);
      return this;
    }
    Object.assign(this.extraAttrs, attributes);
    return this;
  }

  /** @internal */
  emit(): void {
    const client = this.client;
    const call = this.call;
    if (!client || !call) return;
    const conv = this.conv ?? { conversationId: '', conversationName: '', includeContent: true, attributes: {} };
    let inputs: Record<string, unknown>, output: unknown, gen: Record<string, unknown>, summary: Record<string, unknown>;
    try {
      [inputs, output, gen, summary] = this.payload(conv, client);
    } catch (e) {
      logger.warn(`weft: could not record ${this.opName} span: ${(e as Error).message}`);
      return;
    }
    const flat: Record<string, unknown> = { ...gen };
    if (conv.conversationId) flat['gen_ai.conversation.id'] = conv.conversationId;
    if (this.team) {
      flat['weft.team.id'] = this.team.id;
      if (this.team.name) flat['weft.team.name'] = this.team.name;
    }
    if (this.errType) flat['error.type'] = this.errType;
    Object.assign(flat, this.evalAttrs);
    let attrs: Record<string, any> = { ...client.globalAttributes };
    attrs = deepMerge(attrs, this.ctxAttrs);
    attrs = deepMerge(attrs, conv.attributes);
    attrs = deepMerge(attrs, nest(flat));
    attrs = deepMerge(attrs, this.extraAttrs);
    const weftAttrs: Record<string, unknown> = { client_version: VERSION, source: 'typescript-sdk', node_version: process.version, span_kind: this.kind };
    if (attrs.weft && typeof attrs.weft === 'object') Object.assign(weftAttrs, attrs.weft);
    if (this.reasoningSteps.length && client.captureReasoning) {
      weftAttrs.reasoning = [...((weftAttrs.reasoning as unknown[]) ?? []), ...client.reasoningAttr(this.reasoningSteps)];
    }
    attrs.weft = weftAttrs;
    attrs.weave = { ...(attrs.weave && typeof attrs.weave === 'object' ? attrs.weave : {}), kind: this.kind };
    const serAttrs = toJson(attrs, client) as Record<string, unknown>;
    const serInputs = client.redactor.apply(client.scrubReasoning(toJson(inputs, client))) as Record<string, unknown>;
    const serOutput = client.redactor.apply(client.scrubReasoning(toJson(output, client)));
    call.attributes = serAttrs;
    call.inputs = serInputs;
    call.output = serOutput;
    call.endedAt = iso(this.endedAt ?? new Date());
    call.exception = this.exception;
    call.summary = summary;
    call.ended = true;
    const start: Record<string, unknown> = {
      project_id: call.projectId,
      id: call.id,
      op_name: call.opName,
      trace_id: call.traceId,
      parent_id: call.parentId,
      started_at: call.startedAt,
      attributes: serAttrs,
      inputs: serInputs,
      display_name: call.displayName,
    };
    if (call.threadId) start.thread_id = call.threadId;
    if (call.turnId) start.turn_id = call.turnId;
    const end: Record<string, unknown> = { project_id: call.projectId, id: call.id, ended_at: call.endedAt, output: serOutput };
    if (this.exception) end.exception = this.exception;
    if (Object.keys(summary).length) end.summary = toJson(summary, client);
    client.sender.enqueue({ kind: 'call_start', payload: start });
    client.sender.enqueue({ kind: 'call_end', payload: end });
  }
}

// ---------------------------------------------------------------------------
// Tool

export interface ToolOptions {
  name?: string;
  arguments?: unknown;
  result?: unknown;
  toolCallId?: string;
  toolType?: string;
  toolDescription?: string;
  toolDefinitions?: string;
  startedAt?: Date;
  endedAt?: Date;
}

/** One tool execution (an `execute_tool` call). `arguments`/`result` take any JSON value. */
export class Tool extends SpanBase {
  protected readonly opName = 'execute_tool';
  protected readonly kind = 'tool';
  name: string;
  arguments: unknown;
  result: unknown;
  toolCallId: string;
  toolType: string;
  toolDescription: string;
  toolDefinitions: string;
  durationMs = 0;

  constructor(opts: ToolOptions = {}) {
    super();
    this.name = opts.name ?? '';
    this.arguments = opts.arguments ?? '';
    this.result = opts.result ?? '';
    this.toolCallId = opts.toolCallId ?? '';
    this.toolType = opts.toolType ?? '';
    this.toolDescription = opts.toolDescription ?? '';
    this.toolDefinitions = opts.toolDefinitions ?? '';
    this.startedAt = opts.startedAt ?? null;
    this.endedAt = opts.endedAt ?? null;
  }

  protected displayName(): string {
    return spanName('execute_tool', this.name);
  }

  protected onEnd(): void {
    if (this.startedAt && this.endedAt) this.durationMs = Math.max(0, this.endedAt.getTime() - this.startedAt.getTime());
  }

  protected payload(conv: ConvInfo): Payload {
    const attrs: Record<string, unknown> = { 'gen_ai.operation.name': 'execute_tool', 'gen_ai.tool.name': this.name };
    if (this.toolCallId) attrs['gen_ai.tool.call.id'] = this.toolCallId;
    if (this.toolType) attrs['gen_ai.tool.type'] = this.toolType;
    if (this.toolDescription) attrs['gen_ai.tool.description'] = this.toolDescription;
    if (this.toolDefinitions) attrs['gen_ai.tool.definitions'] = this.toolDefinitions;
    if (!conv.includeContent) return [{}, null, attrs, {}];
    const args = parseMaybeJson(this.arguments);
    const inputs = args && typeof args === 'object' && !Array.isArray(args) ? (args as Record<string, unknown>) : args === null ? {} : { arguments: args };
    return [inputs, parseMaybeJson(this.result), attrs, {}];
  }
}

// ---------------------------------------------------------------------------
// LLM

export interface LLMOptions {
  model?: string;
  providerName?: string;
  systemInstructions?: string[];
  inputMessages?: Message[];
  outputMessages?: Message[];
  usage?: Usage;
  startedAt?: Date;
  endedAt?: Date;
}

/** One LLM API call (a `chat` call). */
export class LLM extends SpanBase {
  protected readonly opName = 'chat';
  protected readonly kind = 'llm';
  model: string;
  providerName: string;
  responseId = '';
  responseModel = '';
  outputType = '';
  systemInstructions: string[];
  usage: Usage;
  reasoning = '';
  finishReasons: string[] = [];
  inputMessages: Message[];
  outputMessages: Message[];
  mediaAttachments: Array<Record<string, unknown>> = [];
  requestTemperature: number | null = null;
  requestMaxTokens: number | null = null;
  requestTopP: number | null = null;
  requestFrequencyPenalty: number | null = null;
  requestPresencePenalty: number | null = null;
  requestSeed: number | null = null;
  requestStopSequences: string[] = [];
  requestChoiceCount: number | null = null;

  constructor(opts: LLMOptions = {}) {
    super();
    this.model = opts.model ?? '';
    this.providerName = opts.providerName ?? '';
    this.systemInstructions = opts.systemInstructions ?? [];
    this.inputMessages = opts.inputMessages ?? [];
    this.outputMessages = opts.outputMessages ?? [];
    this.usage = opts.usage ?? {};
    this.startedAt = opts.startedAt ?? new Date();
    this.endedAt = opts.endedAt ?? null;
  }

  protected displayName(): string {
    return spanName('chat', this.model);
  }

  /** Add `content` as an assistant reply at the end of `outputMessages`. */
  output(content: string): this {
    this.outputMessages.push({ role: 'assistant', content });
    return this;
  }

  /** Record the model's reasoning text for this call. */
  think(content: string): this {
    this.reasoning = content;
    return this;
  }

  /** Set several fields at once (only the ones given). */
  record(fields: Partial<Pick<LLM, 'inputMessages' | 'outputMessages' | 'usage' | 'reasoning' | 'responseId' | 'responseModel' | 'finishReasons' | 'outputType'>>): this {
    for (const [k, v] of Object.entries(fields)) if (v !== undefined) (this as any)[k] = v;
    return this;
  }

  /** Attach media (bytes, base64 text, a URI or a provider file id) to the last user message; bytes are uploaded. */
  attachMedia(opts: { content?: Uint8Array | string; uri?: string; fileId?: string; mimeType?: string; modality?: string }): this {
    const n = [opts.content, opts.uri, opts.fileId].filter((x) => x !== undefined && x !== '' && x !== null).length;
    if (n !== 1) throw new Error('attachMedia takes one source: content, uri or fileId');
    let modality = opts.modality ?? '';
    if (!modality && opts.mimeType) {
      const top = opts.mimeType.split('/')[0];
      if (['image', 'audio', 'video'].includes(top)) modality = top;
    }
    const client = this.client ?? activeTracingClient();
    if (opts.content !== undefined && opts.content !== '') {
      const data = typeof opts.content === 'string' ? new Uint8Array(Buffer.from(opts.content, 'base64')) : opts.content;
      this.mediaAttachments.push(mediaValue(opts.mimeType, modality, data, client));
    } else if (opts.uri) {
      this.mediaAttachments.push(partValue({ type: 'uri', uri: opts.uri, mime_type: opts.mimeType, modality }, client));
    } else {
      this.mediaAttachments.push(partValue({ type: 'file', file_id: opts.fileId!, mime_type: opts.mimeType, modality }, client));
    }
    return this;
  }

  /** Attach a media URL (`data:` URLs are decoded and uploaded); empty URLs are ignored. */
  attachMediaUrl(url: string, opts: { modality?: string } = {}): this {
    if (!url) return this;
    if (url.startsWith('data:')) {
      const [mimeType, payload] = parseDataUrl(url);
      return this.attachMedia({ content: payload, mimeType, modality: opts.modality });
    }
    return this.attachMedia({ uri: url, modality: opts.modality });
  }

  protected runPatch(): Partial<WeftContext> {
    return { ...super.runPatch(), llm: this };
  }

  protected onEnd(): void {
    if (activeLLM === this) activeLLM = null;
  }

  protected payload(conv: ConvInfo, client: WeftClient): Payload {
    const attrs: Record<string, unknown> = { 'gen_ai.operation.name': 'chat', 'gen_ai.request.model': this.model };
    if (this.providerName) attrs['gen_ai.provider.name'] = this.providerName;
    if (this.responseId) attrs['gen_ai.response.id'] = this.responseId;
    if (this.responseModel) attrs['gen_ai.response.model'] = this.responseModel;
    if (this.outputType) attrs['gen_ai.output.type'] = this.outputType;
    if (this.finishReasons.length) attrs['gen_ai.response.finish_reasons'] = [...this.finishReasons];
    const params: Array<[string, string, unknown]> = [
      ['temperature', 'temperature', this.requestTemperature],
      ['max_tokens', 'max_tokens', this.requestMaxTokens],
      ['top_p', 'top_p', this.requestTopP],
      ['frequency_penalty', 'frequency_penalty', this.requestFrequencyPenalty],
      ['presence_penalty', 'presence_penalty', this.requestPresencePenalty],
      ['seed', 'seed', this.requestSeed],
      ['stop', 'stop_sequences', this.requestStopSequences.length ? [...this.requestStopSequences] : null],
      ['n', 'choice.count', this.requestChoiceCount],
    ];
    const inputs: Record<string, unknown> = this.model ? { model: this.model } : {};
    for (const [k, attr, v] of params) {
      if (v !== null && v !== undefined) {
        inputs[k] = v;
        attrs[`gen_ai.request.${attr}`] = v;
      }
    }
    const summary: Record<string, unknown> = {};
    const u = this.usage;
    const inTok = u.inputTokens ?? 0;
    const outTok = u.outputTokens ?? 0;
    if (inTok || outTok || u.totalTokens || u.reasoningTokens || u.cacheReadInputTokens || u.cacheCreationInputTokens) {
      const entry: Record<string, number> = {
        requests: 1,
        prompt_tokens: inTok,
        completion_tokens: outTok,
        total_tokens: u.totalTokens ?? inTok + outTok,
      };
      if (u.reasoningTokens) entry.reasoning_tokens = u.reasoningTokens;
      if (u.cacheReadInputTokens) entry.cache_read_input_tokens = u.cacheReadInputTokens;
      if (u.cacheCreationInputTokens) entry.cache_creation_input_tokens = u.cacheCreationInputTokens;
      summary.usage = { [this.responseModel || this.model || 'unknown']: entry };
    }
    if (!conv.includeContent) return [inputs, null, attrs, summary];
    if (this.systemInstructions.length) attrs['gen_ai.system_instructions'] = systemPartsJson(this.systemInstructions);
    inputs.messages = messagesWithSystem(this.systemInstructions, this.inputMessages, client, this.mediaAttachments);
    const outs = this.outputMessages.map((m) => chatMessage(m, client));
    const hasReasoningPart = this.outputMessages.some((m) => m.parts?.some((p) => p.type === 'reasoning'));
    if (this.reasoning && !hasReasoningPart) {
      const rpart = { type: 'thinking', thinking: this.reasoning };
      let idx = -1;
      this.outputMessages.forEach((m, i) => {
        if (m.role === 'assistant') idx = i;
      });
      if (idx < 0) idx = outs.length - 1;
      if (idx < 0) outs.push({ role: 'assistant', content: [rpart] });
      else {
        const c = outs[idx].content;
        const parts = typeof c === 'string' ? (c ? [{ type: 'text', text: c }] : []) : Array.isArray(c) ? c : [];
        outs[idx].content = [rpart, ...parts];
      }
    }
    let output: unknown = null;
    if (outs.length) {
      const fr = this.finishReasons;
      output = {
        object: 'chat.completion',
        model: this.responseModel || this.model,
        choices: outs.map((m, i) => ({ index: i, message: m, finish_reason: fr[i] ?? fr[fr.length - 1] ?? null })),
        ...(this.responseId ? { id: this.responseId } : {}),
      };
    }
    return [inputs, output, attrs, summary];
  }
}

// ---------------------------------------------------------------------------
// SubAgent

function agentAttrs(o: { agentName: string; conv: ConvInfo; model: string; agentId: string; agentDescription: string; agentVersion: string }): Record<string, unknown> {
  const attrs: Record<string, unknown> = { 'gen_ai.operation.name': 'invoke_agent', 'gen_ai.agent.name': o.agentName };
  if (o.conv.conversationName) attrs['gen_ai.conversation.name'] = o.conv.conversationName;
  if (o.model) attrs['gen_ai.request.model'] = o.model;
  if (o.agentId) attrs['gen_ai.agent.id'] = o.agentId;
  if (o.agentDescription) attrs['gen_ai.agent.description'] = o.agentDescription;
  if (o.agentVersion) attrs['gen_ai.agent.version'] = o.agentVersion;
  return attrs;
}

export interface SubAgentOptions {
  name?: string;
  model?: string;
  agentId?: string;
  agentDescription?: string;
  agentVersion?: string;
  systemInstructions?: string[];
  inputMessages?: Message[];
  outputMessages?: Message[];
  startedAt?: Date;
  endedAt?: Date;
}

/** A delegated agent invocation within a turn (a nested `invoke_agent` call). */
export class SubAgent extends SpanBase {
  protected readonly opName = 'invoke_agent';
  protected readonly kind = 'agent';
  name: string;
  model: string;
  agentId: string;
  agentDescription: string;
  agentVersion: string;
  systemInstructions: string[];
  inputMessages: Message[];
  outputMessages: Message[];
  toolName = '';
  toolCallId = '';
  toolCallArguments: unknown = '';
  toolCallResult: unknown = '';

  constructor(opts: SubAgentOptions = {}) {
    super();
    this.name = opts.name || agentNameOverride || '';
    this.model = opts.model ?? '';
    this.agentId = opts.agentId ?? '';
    this.agentDescription = opts.agentDescription ?? '';
    this.agentVersion = opts.agentVersion ?? '';
    this.systemInstructions = opts.systemInstructions ?? [];
    this.inputMessages = opts.inputMessages ?? [];
    this.outputMessages = opts.outputMessages ?? [];
    this.startedAt = opts.startedAt ?? null;
    this.endedAt = opts.endedAt ?? null;
  }

  protected displayName(): string {
    return spanName('invoke_agent', this.name);
  }

  startLLM(opts: { model?: string; providerName?: string; systemInstructions?: string[] } = {}): LLM {
    const llm = this.pin(new LLM({ ...opts, model: opts.model || this.model }));
    activeLLM = llm;
    return llm;
  }

  startTool(opts: { name: string; arguments?: unknown; toolCallId?: string }): Tool {
    return this.pin(new Tool(opts));
  }

  startSubagent(opts: { name: string; model?: string }): SubAgent {
    return this.pin(new SubAgent({ ...opts, model: opts.model || this.model }));
  }

  /** Start a hand-off from this sub-agent (use `.run(fn)`). */
  startHandoff(toAgent: string, opts: HandoffOptions = {}): Handoff {
    return this.pin(new Handoff({ ...opts, toAgent, fromAgent: opts.fromAgent || this.name }));
  }

  /** Set several fields at once (only the ones given). */
  record(fields: Partial<Pick<SubAgent, 'name' | 'model' | 'systemInstructions' | 'inputMessages' | 'outputMessages' | 'toolName' | 'toolCallId' | 'toolCallArguments' | 'toolCallResult' | 'agentId' | 'agentDescription' | 'agentVersion'>>): this {
    for (const [k, v] of Object.entries(fields)) if (v !== undefined) (this as any)[k] = v;
    return this;
  }

  protected payload(conv: ConvInfo, client: WeftClient): Payload {
    const attrs = agentAttrs({ agentName: this.name, conv, model: this.model, agentId: this.agentId, agentDescription: this.agentDescription, agentVersion: this.agentVersion });
    if (this.toolName) attrs['gen_ai.tool.name'] = this.toolName;
    if (this.toolCallId) attrs['gen_ai.tool.call.id'] = this.toolCallId;
    if (!conv.includeContent) return [{}, null, attrs, {}];
    if (jsonString(this.toolCallArguments)) attrs['gen_ai.tool.call.arguments'] = jsonString(this.toolCallArguments);
    if (jsonString(this.toolCallResult)) attrs['gen_ai.tool.call.result'] = jsonString(this.toolCallResult);
    if (this.systemInstructions.length) attrs['gen_ai.system_instructions'] = systemPartsJson(this.systemInstructions);
    const msgs = messagesWithSystem(this.systemInstructions, this.inputMessages, client);
    const inputs: Record<string, unknown> = msgs.length ? { messages: msgs } : {};
    const output = this.outputMessages.length ? { messages: this.outputMessages.map((m) => chatMessage(m, client)) } : null;
    return [inputs, output, attrs, {}];
  }
}

// ---------------------------------------------------------------------------
// Hand-offs

const HANDOFF_KINDS = new Set(['handoff', 'delegate', 'return']);

function agentOfCall(call: Call): string {
  const span = call.span;
  if (span instanceof Turn) return span.agentName;
  if (span instanceof SubAgent) return span.name;
  const a = (call.attributes ?? {}) as Record<string, any>;
  const name = a.gen_ai?.agent?.name ?? a['gen_ai.agent.name'];
  return typeof name === 'string' ? name : '';
}

/**
 * The agent running now: the innermost agent span (turn, sub-agent, or a call
 * with `gen_ai.agent.name`) on the call stack, else the current turn's.
 */
export function currentAgentName(): string {
  const stack = currentContext().stack;
  for (let i = stack.length - 1; i >= 0; i--) {
    const n = agentOfCall(stack[i]);
    if (n) return n;
  }
  const t = getCurrentTurn();
  return t && !t.ended ? t.agentName : '';
}

export interface HandoffOptions {
  /** What the receiving agent is given. */
  input?: unknown;
  /** `handoff` (the other agent takes over, default) or `delegate` (it works for the caller and returns). */
  kind?: 'handoff' | 'delegate' | 'return' | string;
  /** Defaults to the agent running now. */
  fromAgent?: string;
  startedAt?: Date;
  endedAt?: Date;
}

/**
 * Control passing from one agent to another (`weft.handoff.*`, DESIGN §2.1).
 * Agent runs started inside `startHandoff(...).run(fn)` are its children, so
 * the server links the hand-off to them; an instant `handoff(...)` is linked
 * to the next run of `toAgent` in the same trace.
 */
export class Handoff extends SpanBase {
  protected readonly opName = 'handoff';
  protected readonly kind = 'handoff';
  toAgent: string;
  fromAgent: string;
  handoffKind: string;
  input: unknown;
  output: unknown = null;

  constructor(opts: HandoffOptions & { toAgent: string }) {
    super();
    if (!opts.toAgent || !String(opts.toAgent).trim()) throw new Error('a hand-off needs the name of the agent it goes to');
    const k = String(opts.kind ?? 'handoff').trim().toLowerCase();
    if (!HANDOFF_KINDS.has(k)) throw new Error(`hand-off kind must be one of handoff, delegate, return, not ${JSON.stringify(opts.kind)}`);
    this.toAgent = opts.toAgent;
    this.fromAgent = opts.fromAgent ?? '';
    this.handoffKind = k;
    this.input = opts.input ?? null;
    this.startedAt = opts.startedAt ?? null;
    this.endedAt = opts.endedAt ?? null;
  }

  protected displayName(): string {
    return `${this.handoffKind === 'delegate' ? 'delegate' : 'handoff'} to ${this.toAgent}`;
  }

  start(): this {
    if (!this.started && !this.fromAgent) this.fromAgent = currentAgentName();
    return super.start();
  }

  protected payload(conv: ConvInfo): Payload {
    const attrs: Record<string, unknown> = { 'weft.handoff.to_agent': this.toAgent, 'weft.handoff.kind': this.handoffKind };
    if (this.fromAgent && this.fromAgent !== this.toAgent) attrs['weft.handoff.from_agent'] = this.fromAgent;
    if (!conv.includeContent) return [{}, null, attrs, {}];
    return [this.input === null || this.input === undefined ? {} : { input: this.input }, this.output ?? null, attrs, {}];
  }
}

/** A hand-off span; run the other agent inside `.run(fn)` so its run is the hand-off's child. */
export function startHandoff(toAgent: string, opts: HandoffOptions = {}): Handoff {
  const h = new Handoff({ ...opts, toAgent });
  h.fallbackParent = getCurrentTurn();
  return h;
}

/** Record that control passed to `toAgent` now (an instant hand-off span; `fromAgent` defaults to the running agent). */
export function handoff(toAgent: string, opts: HandoffOptions & { output?: unknown } = {}): Handoff {
  const h = startHandoff(toAgent, opts);
  h.output = opts.output ?? null;
  h.start();
  h.end();
  return h;
}

// ---------------------------------------------------------------------------
// Reasoning

/**
 * Record a reasoning step of the agent or model running now (`weft.reasoning`).
 * `kind` is `thinking` (default), `summary` or `redacted` (no text is kept).
 * The step goes on the innermost running span or op call; the server links it
 * to the tool call or hand-off that follows. Nothing is recorded when
 * `captureReasoning` is off or tracing is not initialised.
 */
export function reasoning(text: string, opts: { kind?: string; tokens?: number } | string = {}): void {
  const o = typeof opts === 'string' ? { kind: opts } : opts;
  const client = activeTracingClient();
  if (!client || currentContext().suppressed || !client.captureReasoning) return;
  const step = reasoningStep(text, o.kind, o.tokens);
  if (step.kind !== 'redacted' && !step.text.trim()) return;
  const call = getCurrentCall();
  let span = (call?.span as SpanBase | null | undefined) ?? null;
  if (!call) span = getCurrentLLM() ?? getCurrentTurn();
  if (span) {
    if (span.ended) {
      logger.warn('weft: reasoning() ignored: the current span already ended');
      return;
    }
    span.reasoningSteps.push({ ...step });
    return;
  }
  if (call && !call.ended) client.addReasoning(call, [{ ...step }]);
  else logger.debug('weft: reasoning() outside any span or op; nothing recorded');
}

// ---------------------------------------------------------------------------
// Turn and Conversation

export interface TurnOptions {
  agentName?: string;
  model?: string;
  agentId?: string;
  agentDescription?: string;
  agentVersion?: string;
  systemInstructions?: string[];
  messages?: Message[];
  outputMessages?: Message[];
  /** Child spans for batch logging (`logTurn` / `logConversation`). */
  spans?: Array<LLM | Tool | SubAgent | Handoff>;
  continueParentTrace?: boolean;
  startedAt?: Date;
  endedAt?: Date;
}

/** One user-agent exchange: an `invoke_agent` call, the root of its own trace unless `continueParentTrace`. */
export class Turn extends SpanBase {
  protected readonly opName = 'invoke_agent';
  protected readonly kind = 'agent';
  agentName: string;
  model: string;
  agentId: string;
  agentDescription: string;
  agentVersion: string;
  systemInstructions: string[];
  messages: Message[];
  outputMessages: Message[];
  spans: Array<LLM | Tool | SubAgent | Handoff>;
  continueParentTrace: boolean;
  /** @internal Recorded by logTurn/logConversation (never nested automatically). */ batch = false;

  constructor(opts: TurnOptions = {}) {
    super();
    this.agentName = opts.agentName || agentNameOverride || '';
    this.model = opts.model ?? '';
    this.agentId = opts.agentId ?? '';
    this.agentDescription = opts.agentDescription ?? '';
    this.agentVersion = opts.agentVersion ?? '';
    this.systemInstructions = opts.systemInstructions ?? [];
    this.messages = opts.messages ?? [];
    this.outputMessages = opts.outputMessages ?? [];
    this.spans = opts.spans ?? [];
    this.continueParentTrace = !!opts.continueParentTrace;
    this.startedAt = opts.startedAt ?? new Date();
    this.endedAt = opts.endedAt ?? null;
  }

  protected displayName(): string {
    return spanName('invoke_agent', this.agentName);
  }

  start(): this {
    if (this.started) return this;
    // A turn of another agent started while an agent is running is that agent
    // delegating: it nests (same trace) so the hand-off is recorded.
    const running = !this.explicitParent && !this.batch ? currentAgentName() : '';
    const nested = !!running && running !== this.agentName && getCurrentCall() !== null;
    this.newTrace = !(this.continueParentTrace || nested);
    return super.start();
  }

  /** Append a user message to the turn's input. */
  user(content: string): this {
    this.messages.push({ role: 'user', content });
    return this;
  }

  startLLM(opts: { model?: string; providerName?: string; systemInstructions?: string[] } = {}): LLM {
    const llm = this.pin(new LLM({ ...opts, model: opts.model || this.model }));
    activeLLM = llm;
    return llm;
  }

  startTool(opts: { name: string; arguments?: unknown; toolCallId?: string }): Tool {
    return this.pin(new Tool(opts));
  }

  startSubagent(opts: { name: string; model?: string; systemInstructions?: string[] }): SubAgent {
    return this.pin(new SubAgent({ ...opts, model: opts.model || this.model }));
  }

  /** Start a hand-off from this turn's agent (use `.run(fn)`; agent runs inside it are its children). */
  startHandoff(toAgent: string, opts: HandoffOptions = {}): Handoff {
    return this.pin(new Handoff({ ...opts, toAgent, fromAgent: opts.fromAgent || this.agentName }));
  }

  /** Record an instant hand-off from this turn's agent. */
  handoff(toAgent: string, opts: HandoffOptions & { output?: unknown } = {}): Handoff {
    const h = this.startHandoff(toAgent, opts);
    h.output = opts.output ?? null;
    h.start();
    h.end();
    return h;
  }

  /** Set several fields at once (only the ones given; `messages` replaces the input). */
  record(fields: Partial<Pick<Turn, 'messages' | 'outputMessages' | 'systemInstructions' | 'agentName' | 'model' | 'agentId' | 'agentDescription' | 'agentVersion'>>): this {
    for (const [k, v] of Object.entries(fields)) if (v !== undefined) (this as any)[k] = v;
    return this;
  }

  protected runPatch(): Partial<WeftContext> {
    return { ...super.runPatch(), turn: this };
  }

  protected onEnd(): void {
    if (activeTurn === this) activeTurn = null;
  }

  protected payload(conv: ConvInfo, client: WeftClient): Payload {
    const attrs = agentAttrs({ agentName: this.agentName, conv, model: this.model, agentId: this.agentId, agentDescription: this.agentDescription, agentVersion: this.agentVersion });
    if (!conv.includeContent) return [{}, null, attrs, {}];
    if (this.systemInstructions.length) attrs['gen_ai.system_instructions'] = systemPartsJson(this.systemInstructions);
    const msgs = messagesWithSystem(this.systemInstructions, this.messages, client);
    const inputs: Record<string, unknown> = msgs.length ? { messages: msgs } : {};
    const output = this.outputMessages.length ? { messages: this.outputMessages.map((m) => chatMessage(m, client)) } : null;
    return [inputs, output, attrs, {}];
  }
}

export interface ConversationOptions {
  agentName?: string;
  model?: string;
  agentId?: string;
  agentDescription?: string;
  agentVersion?: string;
  conversationId?: string;
  conversationName?: string;
  /** false drops messages, tool arguments and results (usage and names are kept). */
  includeContent?: boolean;
  continueParentTrace?: boolean;
  /** Attributes added to every span of the conversation. */
  attributes?: Record<string, unknown>;
  /**
   * The agent team (`{id, name}`, an id or `[id, name]`): every span of the
   * conversation carries `weft.team.id` / `weft.team.name`, and calls made
   * inside `conv.run(fn)` do too.
   */
  team?: TeamLike;
}

/** A conversation: groups turns by `conversationId` (recorded as the calls' `thread_id`). */
export class Conversation {
  conversationId: string;
  conversationName: string;
  agentName: string;
  model: string;
  agentId: string;
  agentDescription: string;
  agentVersion: string;
  includeContent: boolean;
  continueParentTrace: boolean;
  attributes: Record<string, unknown>;
  team: TeamInfo | null;
  /** @internal */ ended = false;
  private currentTurn: Turn | null = null;
  private turns: Turn[] = [];

  constructor(opts: ConversationOptions = {}) {
    this.conversationId = opts.conversationId || randomUUID();
    this.conversationName = opts.conversationName ?? '';
    this.agentName = opts.agentName ?? '';
    this.model = opts.model ?? '';
    this.agentId = opts.agentId ?? '';
    this.agentDescription = opts.agentDescription ?? '';
    this.agentVersion = opts.agentVersion ?? '';
    this.includeContent = opts.includeContent ?? true;
    this.continueParentTrace = !!opts.continueParentTrace;
    this.attributes = { ...(opts.attributes ?? {}) };
    this.team = toTeam(opts.team);
  }

  /** @internal */
  info(): ConvInfo {
    return { conversationId: this.conversationId, conversationName: this.conversationName, includeContent: this.includeContent, attributes: { ...this.attributes }, team: this.team };
  }

  /**
   * Create a turn (ending the previous one if still open); it becomes the
   * current turn. Started from inside a running turn of another agent, the
   * new turn is that agent delegating: the running turn stays open and the
   * new one nests under it.
   */
  startTurn(opts: { userMessage?: string; model?: string; agentName?: string; agentId?: string; agentDescription?: string; agentVersion?: string; systemInstructions?: string[] } = {}): Turn {
    const cur = this.currentTurn;
    if (cur && !cur.ended) {
      const inside = !!cur.call && currentContext().stack.includes(cur.call);
      if (!(inside && (opts.agentName || this.agentName) !== cur.agentName)) cur.end();
    }
    const turn = new Turn({
      agentName: opts.agentName || this.agentName,
      model: opts.model || this.model,
      agentId: opts.agentId || this.agentId,
      agentDescription: opts.agentDescription || this.agentDescription,
      agentVersion: opts.agentVersion || this.agentVersion,
      systemInstructions: opts.systemInstructions ?? [],
      continueParentTrace: this.continueParentTrace,
    });
    turn.conv = this.info();
    if (opts.userMessage) turn.messages.push({ role: 'user', content: opts.userMessage });
    this.currentTurn = turn;
    this.turns = [...this.turns.filter((t) => !t.ended), turn];
    activeTurn = turn;
    return turn;
  }

  /** Run `fn` with this conversation (and its team) current, then end it. */
  async run<T>(fn: (conv: this) => T | Promise<T>): Promise<T> {
    try {
      return await runWith({ conversation: this, ...(this.team ? { team: this.team } : {}) }, () => fn(this));
    } finally {
      this.end();
    }
  }

  end(): void {
    if (this.ended) return;
    this.ended = true;
    for (const t of [...this.turns].reverse()) if (!t.ended) t.end();
    if (activeConversation === this) activeConversation = null;
  }
}

// ---------------------------------------------------------------------------
// module functions

/** Create and activate a conversation. */
export function startConversation(opts: ConversationOptions = {}): Conversation {
  const c = new Conversation(opts);
  activeConversation = c;
  return c;
}

/** Start a turn in the current conversation (a disconnected turn when there is none). */
export function startTurn(opts: { userMessage?: string; model?: string; agentName?: string; systemInstructions?: string[] } = {}): Turn {
  const conv = getCurrentConversation();
  if (conv) return conv.startTurn(opts);
  const t = new Turn({ agentName: opts.agentName, model: opts.model, systemInstructions: opts.systemInstructions });
  if (opts.userMessage) t.messages.push({ role: 'user', content: opts.userMessage });
  return t;
}

/** Start an LLM call in the current turn (a disconnected one when there is none). */
export function startLLM(opts: { model?: string; providerName?: string; systemInstructions?: string[] } = {}): LLM {
  const turn = getCurrentTurn();
  return turn ? turn.startLLM(opts) : new LLM(opts);
}

/** Start a tool span; it nests under the current call (or the current turn). */
export function startTool(opts: { name: string; arguments?: unknown; toolCallId?: string }): Tool {
  const t = new Tool(opts);
  t.fallbackParent = getCurrentTurn();
  return t;
}

/** Start a sub-agent span; it nests under the current call (or the current turn). */
export function startSubagent(opts: { name: string; model?: string; systemInstructions?: string[] }): SubAgent {
  const s = new SubAgent(opts);
  s.fallbackParent = getCurrentTurn();
  return s;
}

export function endConversation(): void {
  getCurrentConversation()?.end();
}

export function endTurn(): void {
  getCurrentTurn()?.end();
}

export function endLLM(): void {
  getCurrentLLM()?.end();
}

// ---------------------------------------------------------------------------
// batch logging

function resolveTimes(startedAt: Date | null | undefined, endedAt: Date | null | undefined, spans: SpanBase[]): [Date, Date] {
  const now = new Date();
  const starts = spans.map((s) => s.startedAt).filter((d): d is Date => !!d);
  const ends = spans.map((s) => s.endedAt).filter((d): d is Date => !!d);
  const min = starts.length ? new Date(Math.min(...starts.map((d) => d.getTime()))) : now;
  const max = ends.length ? new Date(Math.max(...ends.map((d) => d.getTime()))) : now;
  return [startedAt ?? min, endedAt ?? max];
}

function emitTurn(turn: Turn, info: ConvInfo, attributes?: Record<string, unknown>): LogResult {
  const client = activeTracingClient();
  if (!client) return { conversationId: info.conversationId, traceIds: [], rootSpanIds: [], spanCount: 0 };
  [turn.startedAt, turn.endedAt] = resolveTimes(turn.startedAt, turn.endedAt, turn.spans);
  const conv: ConvInfo = attributes ? { ...info, attributes: { ...info.attributes, ...attributes } } : info;
  turn.conv = conv;
  turn.client = client;
  turn.batch = true;
  turn.start();
  for (const child of turn.spans) {
    child.conv = conv;
    child.client = client;
    child.explicitParent = turn;
    if (child instanceof Handoff && !child.fromAgent) child.fromAgent = turn.agentName;
    child.startedAt ??= turn.startedAt;
    child.endedAt ??= child.startedAt;
    child.start();
    child.ended = true;
    child.emit();
  }
  turn.ended = true;
  turn.emit();
  return {
    conversationId: info.conversationId,
    traceIds: turn.traceId ? [turn.traceId] : [],
    rootSpanIds: turn.callId ? [turn.callId] : [],
    spanCount: 1 + turn.spans.length,
  };
}

export interface LogTurnOptions extends TurnOptions {
  conversationId: string;
  conversationName?: string;
  includeContent?: boolean;
  /** Attributes added to every recorded span. */
  attributes?: Record<string, unknown>;
  /** The agent team (`{id, name}`, an id or `[id, name]`). */
  team?: TeamLike;
}

/** Record one finished turn and its child spans (`spans`, each with startedAt/endedAt). */
export function logTurn(opts: LogTurnOptions): LogResult {
  const spans = opts.spans ?? [];
  const [s, e] = resolveTimes(opts.startedAt, opts.endedAt, spans);
  const turn = new Turn({ ...opts, spans, startedAt: s, endedAt: e });
  const team = toTeam(opts.team) ?? currentContext().team ?? null;
  return emitTurn(turn, { conversationId: opts.conversationId, conversationName: opts.conversationName ?? '', includeContent: opts.includeContent ?? true, attributes: {}, team }, opts.attributes);
}

export interface LogConversationOptions extends Omit<ConversationOptions, 'conversationId'> {
  turns: Turn[];
  conversationId?: string;
}

/** Record a complete conversation; each Turn's `spans` are its children. Turn values win over conversation defaults. */
export function logConversation(opts: LogConversationOptions): LogResult {
  const sid = opts.conversationId || randomUUID();
  const info: ConvInfo = { conversationId: sid, conversationName: opts.conversationName ?? '', includeContent: opts.includeContent ?? true, attributes: {}, team: toTeam(opts.team) ?? currentContext().team ?? null };
  const result: LogResult = { conversationId: sid, traceIds: [], rootSpanIds: [], spanCount: 0 };
  for (const t of opts.turns) {
    const copy = new Turn({
      agentName: t.agentName || opts.agentName,
      model: t.model || opts.model,
      agentId: t.agentId || opts.agentId,
      agentDescription: t.agentDescription || opts.agentDescription,
      agentVersion: t.agentVersion || opts.agentVersion,
      systemInstructions: t.systemInstructions,
      messages: t.messages,
      outputMessages: t.outputMessages,
      spans: t.spans.map((s) => Object.assign(Object.create(Object.getPrototypeOf(s)), s) as LLM | Tool | SubAgent | Handoff),
      continueParentTrace: !!opts.continueParentTrace,
      startedAt: t.startedAt ?? undefined,
      endedAt: t.endedAt ?? undefined,
    });
    const r = emitTurn(copy, info, opts.attributes);
    result.traceIds.push(...r.traceIds);
    result.rootSpanIds.push(...r.rootSpanIds);
    result.spanCount += r.spanCount;
  }
  return result;
}
