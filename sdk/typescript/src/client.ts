import { logger, parseLogLevel, setLogLevel, type LogLevel } from './log.js';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { Span, type SpanData } from './call.js';
import { digest, fileDigest, tableDigest } from './canonical.js';
import { deprecated } from './deprecation.js';
import { currentContext, getCurrentCall, teamAttributes } from './context.js';
import { scrubReasoning } from './reasoning.js';
import { getClient, setGlobalClient } from './global.js';
import { HttpClient, VERSION, WeftHTTPError } from './http.js';
import { uuid7 } from './ids.js';
import { loadOp } from './loader.js';
import { Content } from './content.js';
import { Dataset, WeftObject, loadPromptOrModel } from './objects.js';
import { SavedView } from './savedView.js';
import { loadPlatformObject } from './platform.js';
import { op as makeOp, isOp, spanDisplayNameOption, type OpMeta } from './op.js';
import { Ref, isRefUri, splitProject } from './refs.js';
import { Redactor, type RedactSettings } from './redact.js';
import { Sender, type SenderOptions } from './sender.js';
import { configureIntegrations, type AutopatchSettings } from './integrations/settings.js';
import { getRef, toJson } from './serialize.js';

export const DEFAULT_SERVER_URL = 'http://localhost:6345';

/**
 * Link to a conversation in the Agents view. The base is `serverUrl`, else
 * the global client's server, else `WEFT_SERVER_URL`, else the default.
 */
export function agentConversationPath(entityName: string, projectName: string, conversationId: string, serverUrl?: string): string {
  const base = (serverUrl ?? globalServerUrl() ?? process.env.WEFT_SERVER_URL ?? DEFAULT_SERVER_URL).replace(/\/+$/, '');
  const enc = encodeURIComponent;
  return `${base}/${enc(entityName)}/${enc(projectName)}/conversations/${enc(conversationId)}`;
}

function globalServerUrl(): string | undefined {
  return getClient()?.serverUrl;
}

export interface InitOptions {
  serverUrl?: string;
  apiKey?: string;
  /** Disable tracing (also WEFT_DISABLED=true). */
  disabled?: boolean;
  sender?: SenderOptions;
  /** Client settings (redaction, attributes, links, durability). */
  settings?: ClientSettings;
  /**
   * Per-integration settings, keyed by integration name (`openai`, `langchain`,
   * `mcp`, ...): `{ enabled?, traceSettings?: { name, spanDisplayName,
   * postprocessInputs, postprocessOutput, kind, color, attributes } }`.
   * Replaces earlier settings when given.
   */
  autopatchSettings?: AutopatchSettings;
}

export interface ClientSettings extends RedactSettings {
  /** Attributes added to every call. */
  globalAttributes?: Record<string, unknown>;
  /** Print `<server>/<entity>/<project>/spans/<id>` for each root span (default true; env WEFT_PRINT_SPAN_LINK). */
  printSpanLink?: boolean;
  /** @deprecated Use printSpanLink. */
  printCallLink?: boolean;
  /** Store op source code (default true; env WEFT_CAPTURE_CODE). */
  captureCode?: boolean;
  /**
   * Record model reasoning: thinking blocks, reasoning summaries and
   * `weft.reasoning()` steps (default true; env WEFT_CAPTURE_REASONING).
   * Redacted thinking is always kept as redacted, without its payload.
   */
  captureReasoning?: boolean;
  /** Keep unsent data in an on-disk write-ahead log, replayed on the next start (env WEFT_ENABLE_WAL). */
  enableDiskFallback?: boolean;
  /** WAL directory (default ~/.weft/wal; env WEFT_WAL_DIR). */
  walDir?: string;
  /**
   * With the WAL on, write every request to it and send nothing; a later run
   * without this setting replays it (env WEFT_DISABLE_WAL_SENDER).
   */
  disableWalSender?: boolean;
  maxQueueSize?: number;
  /** Span starts and ends per POST /api/v1/spans/batch request. */
  batchSize?: number;
  /** Retries of a failed request (default 3; env WEFT_RETRY_MAX_ATTEMPTS). */
  retryMaxAttempts?: number;
  /** First retry delay (default 250 ms; env WEFT_RETRY_BACKOFF, seconds). */
  retryBackoffMs?: number;
  /** Longest retry delay (default 4000 ms; env WEFT_RETRY_MAX_INTERVAL, seconds). */
  retryMaxBackoffMs?: number;
  /** HTTP request timeout (default 30000 ms; env WEFT_HTTP_TIMEOUT, seconds). */
  httpTimeoutMs?: number;
  /** SDK log level: debug, info, warning (default), error, critical (env WEFT_LOG_LEVEL). */
  logLevel?: LogLevel | string;
  /** Link calls to a run (`run_id`, `run_step`); see `WeftClient.setRun`. */
  runId?: string;
  runStep?: number | (() => number | undefined);
}

function envNumber(name: string, scale = 1): number | undefined {
  const raw = (process.env[name] ?? '').trim();
  if (!raw) return undefined;
  const n = Number(raw);
  if (!Number.isFinite(n)) {
    logger.warn(`weft: ignoring ${name}=${JSON.stringify(raw)} (not a number)`);
    return undefined;
  }
  return n * scale;
}

/** Sender options from WEFT_RETRY_MAX_ATTEMPTS, WEFT_RETRY_BACKOFF, WEFT_RETRY_MAX_INTERVAL, WEFT_MAX_SPANS_QUEUE_SIZE. */
export function senderOptionsFromEnv(): SenderOptions {
  const out: SenderOptions = {};
  const retries = envNumber('WEFT_RETRY_MAX_ATTEMPTS');
  if (retries !== undefined) out.maxRetries = Math.max(0, Math.floor(retries));
  const backoff = envNumber('WEFT_RETRY_BACKOFF', 1000);
  if (backoff !== undefined) out.backoffMs = backoff;
  const maxBackoff = envNumber('WEFT_RETRY_MAX_INTERVAL', 1000);
  if (maxBackoff !== undefined) out.maxBackoffMs = maxBackoff;
  let queue = envNumber('WEFT_MAX_SPANS_QUEUE_SIZE');
  if (queue === undefined) {
    queue = envNumber('WEFT_MAX_CALLS_QUEUE_SIZE');
    if (queue !== undefined) deprecated('WEFT_MAX_CALLS_QUEUE_SIZE', 'WEFT_MAX_SPANS_QUEUE_SIZE');
  }
  queue ??= envNumber('WEFT_MAX_QUEUE_SIZE');
  if (queue !== undefined) out.maxQueue = Math.floor(queue);
  return out;
}

/** `printSpanLink` / WEFT_PRINT_SPAN_LINK; the earlier printCallLink / WEFT_PRINT_CALL_LINK are read with one warning. */
function printSpanLinkSetting(st: ClientSettings): boolean {
  if (st.printSpanLink !== undefined) return st.printSpanLink;
  if (st.printCallLink !== undefined) {
    deprecated('settings.printCallLink', 'settings.printSpanLink');
    return st.printCallLink;
  }
  if (!(process.env.WEFT_PRINT_SPAN_LINK ?? '').trim() && (process.env.WEFT_PRINT_CALL_LINK ?? '').trim()) {
    deprecated('WEFT_PRINT_CALL_LINK', 'WEFT_PRINT_SPAN_LINK');
    return settingBool(undefined, 'WEFT_PRINT_CALL_LINK', true);
  }
  return settingBool(undefined, 'WEFT_PRINT_SPAN_LINK', true);
}

function settingBool(v: boolean | undefined, env: string, dflt: boolean): boolean {
  if (v !== undefined) return v;
  const raw = (process.env[env] ?? '').trim().toLowerCase();
  if (!raw) return dflt;
  return ['1', 'true', 'yes', 'on'].includes(raw);
}

export const CODE_CAPTURE_DISABLED = '// Code capture disabled\n';

/** A runtime ID for `registerCustomRuntime`. */
export interface CustomRuntimeId {
  id: string;
  /** Default 4096 (set by the server). */
  maxTokens?: number;
}

/** Options of `registerCustomRuntime`. */
export interface RegisterCustomRuntimeOptions {
  name: string;
  /** Where the runtime serves the OpenAI chat API, e.g. `http://gpu-box:8000/v1`. */
  baseUrl: string;
  /** Every model the runtime should offer. Registering again replaces the list, so IDs left out are dropped. */
  runtimeIds: ReadonlyArray<string | CustomRuntimeId>;
  /** Stored secret whose value goes in the `Authorization: Bearer` header. */
  apiKeySecret?: string;
  headers?: Readonly<Record<string, string>>;
}

/** The server's reply (`kind` and `ref` name what was registered). */
export interface CustomRuntimeApplyRes {
  name: string;
  base_url: string;
  api_key_secret: string | null;
  headers: Record<string, string>;
  runtime_ids: Array<{ id: string; max_tokens?: number; playground_id: string }>;
  kind?: string;
  ref?: string;
}
export type RegisterCustomRuntimeResult = CustomRuntimeApplyRes;

/** Options of `client.getSessions`. */
export interface GetSessionsOptions {
  limit?: number;
  offset?: number;
  /** Only these sessions. */
  sessionIds?: string[];
  /** @deprecated Use `sessionIds`. */
  threadIds?: string[];
  /** Only sessions whose first turn started after this time (ISO). */
  after?: string;
  /** Only sessions whose first turn started before this time (ISO). */
  before?: string;
  /** Fields: last_updated, started_at, turn_count, span_count, total_tokens. */
  sortBy?: Array<{ field: string; direction: 'asc' | 'desc' }>;
}

export interface SpansOptions {
  filter?: Record<string, unknown>;
  query?: Record<string, unknown>;
  sortBy?: Array<{ field: string; direction: 'asc' | 'desc' }>;
  limit?: number;
  offset?: number;
  search?: string;
  includeCosts?: boolean;
  includeFeedback?: boolean;
  /** Projection hint (dotted paths). */
  columns?: string[];
  /** Resolve refs at these paths (e.g. "inputs.example"). */
  expandColumns?: string[];
  /** Ask the server for `username` (`include_usernames`). */
  includeUsernames?: boolean;
  /** Ask the server for `storage_size_bytes`. */
  includeStorageSize?: boolean;
  /** Ask the server for `summary.weft.run_status`, the status the shared status filter reads (`include_run_status`). */
  includeRunStatus?: boolean;
  /** Include soft-deleted spans. */
  includeDeleted?: boolean;
}

/** Earlier name of SpansOptions. */
export type GetCallsOptions = SpansOptions;

/** Span filter keys of earlier versions, mapped to Weft's (docs/weft/API.md, SpansQuery). */
const LEGACY_FILTER_KEYS: Record<string, string> = { op_names: 'names', call_ids: 'ids', thread_ids: 'session_ids' };

function spanFilter(f: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(f)) out[LEGACY_FILTER_KEYS[k] ?? k] = v;
  return out;
}

/** Path of an object (and version) under /api/v1/objects. */
function objectPath(name: string, version?: string): string {
  const p = `/api/v1/objects/${encodeURIComponent(name)}`;
  return version === undefined ? p : `${p}/versions/${encodeURIComponent(version)}`;
}

/** A registry version as returned by `POST /api/v1/registry/query`. */
export interface RegistryVersion {
  version_index: number;
  version: string;
  ref: string;
  source_project_id?: string;
  object_id?: string;
  digest?: string;
  base_object_class?: string | null;
  aliases: string[];
  created_at?: string;
  user_id?: string | null;
}

export interface RegistryCollection {
  name: string;
  versions: RegistryVersion[];
}

const NAME_RE = /[^A-Za-z0-9_.-]+/g;

export function sanitizeName(name: string): string {
  return name.trim().replace(NAME_RE, '-').replace(/^-+|-+$/g, '').slice(0, 128) || 'object';
}

function nowIso(): string {
  // microsecond-style precision is not available; milliseconds are fine
  return new Date().toISOString();
}

function toIso(t: string | Date): string {
  if (t instanceof Date) return t.toISOString();
  const d = new Date(t);
  return Number.isNaN(d.getTime()) ? nowIso() : d.toISOString();
}

export function formatError(e: unknown): string {
  if (e instanceof Error) {
    const head = `${e.name}: ${e.message}`;
    return e.stack ? `${head}\n\n${e.stack}` : head;
  }
  return `Error: ${String(e)}`;
}

const synthetic = new Map<string, OpMeta>();
export function syntheticOp(name: string, kind?: string): OpMeta {
  const key = `${name}|${kind ?? ''}`;
  let m = synthetic.get(key);
  if (!m) {
    const fn = makeOp(function recorded() {}, { name, kind });
    m = fn.__weftOp;
    m.code = `// ${name}: recorded by the weft SDK\n`;
    synthetic.set(key, m);
  }
  return m;
}

/** Span kinds whose inputs and outputs may hold model reasoning. */
const REASONING_KINDS = new Set(['llm', 'agent']);

function deepMergeAttrs(base: Record<string, any>, extra: Record<string, any>): Record<string, any> {
  const out: Record<string, any> = { ...base };
  for (const [k, v] of Object.entries(extra)) {
    if (v && typeof v === 'object' && !Array.isArray(v) && out[k] && typeof out[k] === 'object' && !Array.isArray(out[k])) out[k] = deepMergeAttrs(out[k], v);
    else out[k] = v;
  }
  return out;
}

export class WeftClient {
  readonly entity: string;
  readonly project: string;
  readonly serverUrl: string;
  readonly http: HttpClient;
  readonly sender: Sender;
  disabled: boolean;
  readonly redactor: Redactor;
  private uploaded = new Set<string>();

  readonly settings: ClientSettings;
  readonly captureCode: boolean;
  readonly captureReasoning: boolean;
  readonly printSpanLink: boolean;
  readonly globalAttributes: Record<string, unknown>;
  private runLink: { runId: string; step?: number | (() => number | undefined) } | null = null;

  constructor(project: string, opts: InitOptions = {}) {
    [this.entity, this.project] = splitProject(project);
    this.serverUrl = (opts.serverUrl ?? process.env.WEFT_SERVER_URL ?? DEFAULT_SERVER_URL).replace(/\/+$/, '');
    const st = (this.settings = opts.settings ?? {});
    if (st.logLevel !== undefined) {
      const l = parseLogLevel(st.logLevel);
      if (l) setLogLevel(l);
      else logger.warn(`weft: ignoring log level ${JSON.stringify(st.logLevel)}`);
    }
    const timeoutMs = st.httpTimeoutMs ?? envNumber('WEFT_HTTP_TIMEOUT', 1000) ?? 30_000;
    this.http = new HttpClient(this.serverUrl, opts.apiKey ?? process.env.WEFT_API_KEY, timeoutMs);
    this.captureCode = settingBool(st.captureCode, 'WEFT_CAPTURE_CODE', true);
    this.captureReasoning = settingBool(st.captureReasoning, 'WEFT_CAPTURE_REASONING', true);
    this.printSpanLink = printSpanLinkSetting(st);
    this.globalAttributes = { ...(st.globalAttributes ?? {}) };
    let walPath: string | undefined;
    if (settingBool(st.enableDiskFallback, 'WEFT_ENABLE_WAL', false)) {
      const dir = st.walDir ?? process.env.WEFT_WAL_DIR ?? join(homedir(), '.weft', 'wal');
      walPath = join(dir, `${this.serverUrl}_${this.projectId}`.replace(/[^A-Za-z0-9_.-]+/g, '_').replace(/^_+|_+$/g, '') + '.jsonl');
    }
    this.sender = new Sender(this.http, {
      ...senderOptionsFromEnv(),
      ...opts.sender,
      ...(st.maxQueueSize !== undefined ? { maxQueue: st.maxQueueSize } : {}),
      ...(st.batchSize !== undefined ? { batchSize: st.batchSize } : {}),
      ...(st.retryMaxAttempts !== undefined ? { maxRetries: st.retryMaxAttempts } : {}),
      ...(st.retryBackoffMs !== undefined ? { backoffMs: st.retryBackoffMs } : {}),
      ...(st.retryMaxBackoffMs !== undefined ? { maxBackoffMs: st.retryMaxBackoffMs } : {}),
      walPath,
      walOnly: settingBool(st.disableWalSender, 'WEFT_DISABLE_WAL_SENDER', false),
    });
    this.disabled = !!opts.disabled;
    this.redactor = new Redactor(st);
    if (this.redactor.ner) this.sender.prepare = (items) => this.redactor.redactItems(items);
    if (st.runId) this.setRun(st.runId, st.runStep);
    if (walPath && !this.disabled) this.sender.replayWal();
  }

  /**
   * Link calls started from now on to a training or experiment run: `run_id` (sent as
   * `<entity>/<project>/<runId>` unless already qualified) and `run_step`
   * / `run_step_end` (an int, or a function read at call start and end).
   * `null` clears it.
   */
  setRun(runId: string | null, step?: number | (() => number | undefined)): void {
    if (!runId) {
      this.runLink = null;
      return;
    }
    const id = runId.split('/').length >= 3 ? runId : `${this.projectId}/${runId}`;
    this.runLink = { runId: id, step };
  }

  private runNow(): { runId: string; step?: number } | null {
    if (!this.runLink) return null;
    let step: unknown = this.runLink.step;
    if (typeof step === 'function') {
      try {
        step = (step as () => unknown)();
      } catch {
        step = undefined;
      }
    }
    const n = Number(step);
    return { runId: this.runLink.runId, step: step === undefined || step === null || !Number.isFinite(n) ? undefined : Math.trunc(n) };
  }

  /** UI link for a span. */
  spanUrl(span: Span | string): string {
    return `${this.serverUrl}/${this.projectId}/spans/${typeof span === 'string' ? span : span.id}`;
  }

  /** @deprecated Use `spanUrl`. */
  callUrl(call: Span | string): string {
    deprecated('WeftClient.callUrl()', 'WeftClient.spanUrl()');
    return this.spanUrl(call);
  }

  get projectId(): string {
    return `${this.entity}/${this.project}`;
  }

  flush(): Promise<void> {
    return this.sender.flush();
  }

  /** `GET /api/v1/server`: version, features, limits and the digest scheme. */
  serverInfo(): Promise<any> {
    return this.http.get('/api/v1/server');
  }

  // ---------------------------------------------------------------- spans
  /** Start a span explicitly; end it with finishSpan. `parent: null` starts a new trace. */
  createSpan(
    op: OpMeta | string,
    inputs: Record<string, unknown>,
    opts: { parent?: Span | null; displayName?: string; attributes?: Record<string, unknown>; startedAt?: string | Date } = {},
  ): Span {
    const meta = typeof op === 'string' ? syntheticOp(op) : op;
    const parent = opts.parent === undefined ? getCurrentCall() : opts.parent;
    const id = uuid7();
    const ctx = currentContext();
    let sessionId: string | null = null;
    let turnId: string | null = null;
    if (ctx.session) {
      sessionId = ctx.session.sessionId;
      turnId = !parent || parent.id === ctx.session.entryParentId || parent.sessionId !== sessionId ? id : parent.turnId;
    } else if (parent?.sessionId) {
      sessionId = parent.sessionId;
      turnId = parent.turnId;
    }
    let raw = inputs;
    if (meta.options.postprocessInputs) {
      try {
        raw = meta.options.postprocessInputs({ ...inputs });
      } catch (e) {
        logger.warn(`weft: postprocessInputs failed for ${meta.name}: ${(e as Error).message}`);
      }
    }
    let ser: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(raw)) if (k !== 'self') ser[k] = toJson(v, this);
    if (REASONING_KINDS.has(meta.options.kind ?? '')) ser = this.scrubReasoning(ser);
    const redacted = this.redactor.apply(ser) as Record<string, unknown>;
    const finalInputs: Record<string, unknown> = 'self' in raw ? { self: this.serializeSelf(raw.self), ...redacted } : redacted;
    const attributes = toJson({ ...this.globalAttributes, ...ctx.attributes, ...(opts.attributes ?? {}) }, this) as Record<string, unknown>;
    const weftAttrs: Record<string, unknown> = { client_version: VERSION, source: 'typescript-sdk', node_version: process.version };
    if (ctx.team) Object.assign(weftAttrs, (teamAttributes(ctx.team) as any).weft);
    if (attributes.weft && typeof attributes.weft === 'object') Object.assign(weftAttrs, attributes.weft);
    const { kind, color } = meta.options;
    if (kind) weftAttrs.span_kind = kind;
    if (color) weftAttrs.color = color;
    attributes.weft = weftAttrs;
    const data: SpanData = {
      id,
      project_id: this.projectId,
      name: this.opRef(meta).uri(),
      trace_id: parent ? parent.traceId : uuid7(),
      parent_id: parent ? parent.id : null,
      started_at: opts.startedAt ? toIso(opts.startedAt) : nowIso(),
      attributes,
      inputs: finalInputs,
    };
    const call = new Span(data, this);
    call.rawInputs = { ...inputs };
    call.sessionId = sessionId;
    call.turnId = turnId;
    if (kind === 'llm') {
      // An LLM call nested in a framework's LLM call holds the reasoning; the
      // framework call does not repeat it (see finishSpan).
      if (parent) parent.llmDescendant = true;
      for (const anc of ctx.stack) anc.llmDescendant = true;
    }
    let dn = opts.displayName ?? null;
    const dnOpt = spanDisplayNameOption(meta.options);
    if (!dn && dnOpt) {
      try {
        dn = typeof dnOpt === 'function' ? dnOpt(call) : dnOpt;
      } catch (e) {
        logger.warn(`weft: spanDisplayName failed for ${meta.name}: ${(e as Error).message}`);
      }
    }
    call.displayName = dn;
    const link = this.runNow();
    if (link) {
      call.runId = link.runId;
      if (link.step !== undefined) call.runStep = link.step;
    }
    this.sender.enqueue({ kind: 'span_start', payload: this.startPayload(call) });
    if (!parent && this.printSpanLink) process.stderr.write(`weft: ${this.spanUrl(call)}\n`);
    return call;
  }

  /** @internal The start record of a span (sent again when its attributes change). */
  startPayload(call: Span): Record<string, unknown> {
    const start: Record<string, unknown> = {
      id: call.id,
      project_id: call.projectId,
      name: call.name,
      trace_id: call.traceId,
      parent_id: call.parentId,
      started_at: call.startedAt,
      attributes: call.attributes,
      inputs: call.inputs,
    };
    if (call.displayName) start.display_name = call.displayName;
    if (call.sessionId) start.session_id = call.sessionId;
    if (call.turnId) start.turn_id = call.turnId;
    if (call.runId) {
      start.run_id = call.runId;
      if (call.runStep !== null && call.runStep !== undefined) start.run_step = call.runStep;
    }
    return start;
  }

  /** @internal Drop reasoning when `captureReasoning` is off; digest opaque reasoning payloads otherwise. */
  scrubReasoning<T>(value: T): T {
    return scrubReasoning(value, this.captureReasoning);
  }

  /** @deprecated Use printSpanLink. */
  get printCallLink(): boolean {
    deprecated('WeftClient.printCallLink', 'WeftClient.printSpanLink');
    return this.printSpanLink;
  }

  /**
   * Change a running span's attributes (deep-merged), session (`sessionId`), turn or display name.
   * The start is sent again with the new values before the end (the server
   * upserts it), so this works after the start went out.
   */
  updateSpan(span: Span, attributes?: Record<string, unknown> | null, opts: { sessionId?: string; turnId?: string; displayName?: string } = {}): void {
    this.applySpanUpdate(span, attributes, opts);
  }

  /** @deprecated Use `updateSpan(span, attributes, { sessionId })`. */
  updateCall(call: Span, attributes?: Record<string, unknown> | null, opts: { threadId?: string; sessionId?: string; turnId?: string; displayName?: string } = {}): void {
    deprecated('WeftClient.updateCall({ threadId })', 'WeftClient.updateSpan({ sessionId })');
    const { threadId, ...rest } = opts;
    this.applySpanUpdate(call, attributes, { ...rest, sessionId: rest.sessionId ?? threadId });
  }

  private applySpanUpdate(call: Span, attributes: Record<string, unknown> | null | undefined, opts: { sessionId?: string; turnId?: string; displayName?: string }): void {
    if (call.ended) {
      logger.debug(`weft: updateSpan ignored: span ${call.id} already ended`);
      return;
    }
    if (attributes && Object.keys(attributes).length) call.attributes = deepMergeAttrs(call.attributes ?? {}, toJson(attributes, this) as Record<string, unknown>);
    if (opts.sessionId !== undefined) call.sessionId = opts.sessionId;
    if (opts.turnId !== undefined) call.turnId = opts.turnId;
    if (opts.displayName !== undefined) call.displayName = opts.displayName;
    call.resendStart = true;
  }

  /** Record reasoning steps (`weft.reasoning` attribute entries) on a running span. */
  addReasoning(call: Span, steps: Array<Record<string, unknown>>): void {
    if (!this.captureReasoning || call.ended || !steps.length) return;
    (call.reasoningSteps ??= []).push(...steps);
  }

  /** @internal Reasoning steps as stored: PII redaction applies to their text. */
  reasoningAttr(steps: Array<Record<string, unknown>>): Array<Record<string, unknown>> {
    return steps.map((st) => (typeof st.text === 'string' && st.text ? { ...st, text: this.redactor.apply(st.text) } : { ...st }));
  }

  finishSpan(call: Span, output?: unknown, error?: unknown, meta?: OpMeta, endedAt?: string | Date): void {
    if (call.ended) return;
    call.ended = true;
    let out = output;
    if (meta?.options.postprocessOutput && error === undefined) {
      try {
        out = meta.options.postprocessOutput(out);
      } catch (e) {
        logger.warn(`weft: postprocessOutput failed for ${meta.name}: ${(e as Error).message}`);
      }
    }
    call.endedAt = endedAt ? toIso(endedAt) : nowIso();
    call.rawOutput = out;
    const kind = String(((call.attributes?.weft as Record<string, unknown> | undefined) ?? {}).span_kind ?? '');
    let ser = toJson(out, this);
    if (REASONING_KINDS.has(kind)) ser = kind === 'llm' && call.llmDescendant ? scrubReasoning(ser, false) : this.scrubReasoning(ser);
    call.output = this.redactor.apply(ser);
    if (call.reasoningSteps?.length) {
      const w = { ...((call.attributes.weft as Record<string, unknown>) ?? {}) };
      w.reasoning = [...((w.reasoning as unknown[]) ?? []), ...this.reasoningAttr(call.reasoningSteps)];
      call.attributes = { ...call.attributes, weft: w };
      call.reasoningSteps = null;
      call.resendStart = true;
    }
    if (call.resendStart) {
      call.resendStart = false;
      this.sender.enqueue({ kind: 'span_start', payload: this.startPayload(call) });
    }
    const end: Record<string, unknown> = { project_id: call.projectId, id: call.id, ended_at: call.endedAt, output: call.output };
    if (error !== undefined) {
      call.error = formatError(error);
      end.error = call.error;
    }
    if (Object.keys(call.summary).length) end.summary = toJson(call.summary, this);
    if (call.runId) {
      const link = this.runNow();
      if (link?.step !== undefined) call.runStepEnd = end.run_step_end = link.step;
    }
    this.sender.enqueue({ kind: 'span_end', payload: end });
  }

  /** @deprecated Use `createSpan`. */
  createCall(
    op: OpMeta | string,
    inputs: Record<string, unknown>,
    opts: { parent?: Span | null; displayName?: string; attributes?: Record<string, unknown>; startedAt?: string | Date } = {},
  ): Span {
    deprecated('WeftClient.createCall()', 'WeftClient.createSpan()');
    return this.createSpan(op, inputs, opts);
  }

  /** @deprecated Use `finishSpan`. */
  finishCall(call: Span, output?: unknown, error?: unknown, meta?: OpMeta, endedAt?: string | Date): void {
    deprecated('WeftClient.finishCall()', 'WeftClient.finishSpan()');
    this.finishSpan(call, output, error, meta, endedAt);
  }

  private serializeSelf(v: unknown): unknown {
    const r = getRef(v);
    if (r) return r.uri();
    if (v instanceof WeftObject) return v.toVal(this);
    const name = v && typeof v === 'object' ? (v as object).constructor?.name : typeof v;
    return `<${name}>`;
  }

  private spansBody(opts: SpansOptions): Record<string, unknown> {
    const body: Record<string, unknown> = { project_id: this.projectId };
    if (opts.filter) body.filter = spanFilter(opts.filter);
    if (opts.query) body.query = '$expr' in opts.query ? opts.query : { $expr: opts.query };
    if (opts.sortBy) body.sort_by = opts.sortBy;
    if (opts.limit !== undefined) body.limit = opts.limit;
    if (opts.offset !== undefined) body.offset = opts.offset;
    if (opts.search) body.search = opts.search;
    if (opts.includeCosts) body.include_costs = true;
    if (opts.includeFeedback) body.include_feedback = true;
    if (opts.columns?.length) body.columns = opts.columns;
    if (opts.expandColumns?.length) body.expand_columns = opts.expandColumns;
    if (opts.includeUsernames) body.include_usernames = true;
    if (opts.includeStorageSize) body.include_storage_size = true;
    if (opts.includeRunStatus) body.include_run_status = true;
    if (opts.includeDeleted) body.include_deleted = true;
    return body;
  }

  /**
   * Query spans (`POST /api/v1/spans/stream`). `filter` is the SpansQuery
   * filter (`names`, `ids`, `trace_ids`, `parent_ids`, `session_ids`,
   * `trace_roots_only`, ...); `query` an `$expr` over span fields.
   */
  async spans(opts: SpansOptions = {}): Promise<Span[]> {
    await this.flush();
    const rows = await this.http.postJsonl<SpanData>('/api/v1/spans/stream', this.spansBody(opts));
    return rows.map((d) => this.fetched(d));
  }

  /** One span by id (`GET /api/v1/spans/{id}`), or null. */
  async span(id: string, opts: { includeCosts?: boolean; includeFeedback?: boolean; includeUsernames?: boolean; includeStorageSize?: boolean } = {}): Promise<Span | null> {
    await this.flush();
    const q: Record<string, string> = { project_id: this.projectId };
    if (opts.includeCosts) q.include_costs = 'true';
    if (opts.includeFeedback) q.include_feedback = 'true';
    if (opts.includeUsernames) q.include_usernames = 'true';
    if (opts.includeStorageSize) q.include_storage_size = 'true';
    try {
      return this.fetched(await this.http.request<SpanData>('GET', `/api/v1/spans/${encodeURIComponent(id)}`, undefined, q));
    } catch (e) {
      if (e instanceof WeftHTTPError && e.status === 404) return null;
      throw e;
    }
  }

  /** `spans` as plain records (`Span.toRecord()`), e.g. for a table or a CSV (the TS `to_pandas`). */
  async spanRecords(opts: SpansOptions = {}): Promise<Array<Record<string, unknown>>> {
    return (await this.spans(opts)).map((c) => c.toRecord());
  }

  /** Number of spans matching `filter`/`query`/`search` (`total_count` of `POST /api/v1/spans/query`). */
  async countSpans(opts: Pick<SpansOptions, 'filter' | 'query' | 'search'> = {}): Promise<number> {
    await this.flush();
    const body = this.spansBody({ filter: opts.filter, query: opts.query, search: opts.search, limit: 0 });
    return Number((await this.http.post('/api/v1/spans/query', body)).total_count ?? 0);
  }

  /** Soft-delete spans (and their descendants). */
  async deleteSpans(ids: string[]): Promise<void> {
    await this.flush();
    await this.http.post('/api/v1/spans/delete', { project_id: this.projectId, ids });
  }

  // ---- earlier names (deprecated)
  /** @deprecated use `spans` */
  getCalls(opts: SpansOptions = {}): Promise<Span[]> {
    deprecated('WeftClient.getCalls()', 'WeftClient.spans()');
    return this.spans(opts);
  }

  /** @deprecated use `span` */
  getCall(id: string, opts: { includeCosts?: boolean; includeFeedback?: boolean; includeUsernames?: boolean; includeStorageSize?: boolean } = {}): Promise<Span | null> {
    deprecated('WeftClient.getCall()', 'WeftClient.span()');
    return this.span(id, opts);
  }

  /** @deprecated use `spanRecords` */
  getCallsRecords(opts: SpansOptions = {}): Promise<Array<Record<string, unknown>>> {
    deprecated('WeftClient.getCallsRecords()', 'WeftClient.spanRecords()');
    return this.spanRecords(opts);
  }

  /** @deprecated use `countSpans` */
  countCalls(opts: Pick<SpansOptions, 'filter' | 'query' | 'search'> = {}): Promise<number> {
    deprecated('WeftClient.countCalls()', 'WeftClient.countSpans()');
    return this.countSpans(opts);
  }

  /** @deprecated use `deleteSpans` */
  deleteCalls(callIds: string[]): Promise<void> {
    deprecated('WeftClient.deleteCalls()', 'WeftClient.deleteSpans()');
    return this.deleteSpans(callIds);
  }

  /** Query feedback (`POST /api/v1/feedback/query`); `query` is an operation or `{$expr}`. */
  async getFeedback(opts: { query?: Record<string, unknown>; limit?: number; offset?: number; sortBy?: Array<{ field: string; direction: 'asc' | 'desc' }> } = {}): Promise<any[]> {
    await this.flush();
    const body: Record<string, unknown> = { project_id: this.projectId, limit: opts.limit ?? 10000 };
    if (opts.query) body.query = '$expr' in opts.query ? opts.query : { $expr: opts.query };
    if (opts.offset !== undefined) body.offset = opts.offset;
    if (opts.sortBy) body.sort_by = opts.sortBy;
    return (await this.http.post('/api/v1/feedback/query', body)).items ?? [];
  }

  /**
   * Register a custom runtime (an OpenAI-compatible endpoint), replacing its
   * complete configuration if it exists
   * (`PUT /api/v1/runtimes/{name}`). Each ID is then usable as
   * the model `<name>/<id>`. Resolves to `{data, error: null}`; failures throw
   * `WeftHTTPError`.
   */
  async registerCustomRuntime(options: RegisterCustomRuntimeOptions): Promise<{ data: RegisterCustomRuntimeResult; error: null }> {
    const body = {
      project_id: this.projectId,
      base_url: options.baseUrl,
      api_key_secret: options.apiKeySecret,
      headers: { ...(options.headers ?? {}) },
      runtime_ids: options.runtimeIds.map((r) => (typeof r === 'string' ? { id: r } : { id: r.id, max_tokens: r.maxTokens })),
    };
    const data = await this.http.request<RegisterCustomRuntimeResult>('PUT', `/api/v1/runtimes/${encodeURIComponent(options.name)}`, body);
    return { data, error: null };
  }

  /** UI link to a conversation in the Agents view (`<server>/<entity>/<project>/conversations/<id>`). */
  conversationUrl(conversationId: string): string {
    return agentConversationPath(this.entity, this.project, conversationId, this.serverUrl);
  }

  /**
   * Sessions: spans grouped by `session_id`, with turn counts, times and
   * turn-duration percentiles (`POST /api/v1/spans/sessions`). Each item has
   * `session_id`; `sessionIds` limits the result to those sessions.
   */
  async getSessions(opts: GetSessionsOptions = {}): Promise<any[]> {
    await this.flush();
    const filter: Record<string, unknown> = {};
    if (opts.threadIds) deprecated('WeftClient.getSessions({ threadIds })', 'WeftClient.getSessions({ sessionIds })');
    const ids = [...(opts.sessionIds ?? []), ...(opts.threadIds ?? [])];
    if (ids.length) filter.session_ids = ids;
    if (opts.after) filter.started_after = opts.after;
    if (opts.before) filter.started_before = opts.before;
    const body: Record<string, unknown> = { project_id: this.projectId };
    if (Object.keys(filter).length) body.filter = filter;
    if (opts.limit !== undefined) body.limit = opts.limit;
    if (opts.offset !== undefined) body.offset = opts.offset;
    if (opts.sortBy) body.sort_by = opts.sortBy.map((s) => (s.field === 'start_time' ? { ...s, field: 'started_at' } : s));
    const items: any[] = (await this.http.post('/api/v1/spans/sessions', body)).items ?? [];
    return items.map((t) => ({ ...t, start_time: t.started_at }));
  }

  /** @deprecated Use `getSessions({ sessionIds })`; its items have no `thread_id`. */
  async getThreads(opts: GetSessionsOptions = {}): Promise<any[]> {
    deprecated('WeftClient.getThreads({ threadIds })', 'WeftClient.getSessions({ sessionIds })');
    const { threadIds, ...rest } = opts;
    const sessionIds = [...(rest.sessionIds ?? []), ...(threadIds ?? [])];
    const items = await this.getSessions({ ...rest, sessionIds: sessionIds.length ? sessionIds : undefined });
    return items.map((t) => ({ ...t, thread_id: t.session_id }));
  }

  /** Latest version of every Evaluation object (loaded values). */
  async getEvaluations(): Promise<any[]> {
    return this.latestObjects('evaluation');
  }

  /** Evaluation runs: the `Evaluation.evaluate` spans (newest first). */
  evaluationRuns(limit?: number): Promise<Span[]> {
    return this.spans({ filter: { names: ['Evaluation.evaluate'] }, limit });
  }

  /** @deprecated use `evaluationRuns` */
  getEvaluationCalls(limit?: number): Promise<Span[]> {
    deprecated('WeftClient.getEvaluationCalls()', 'WeftClient.evaluationRuns()');
    return this.evaluationRuns(limit);
  }

  /** Latest version of every SavedView (optionally of one view type). */
  async getSavedViews(viewType?: string): Promise<SavedView[]> {
    const out: SavedView[] = [];
    for (const v of await this.latestObjects('saved_view')) {
      const sv = v instanceof SavedView ? v : new SavedView({ name: v.name, viewType: v.view_type, label: v.label, definition: v.definition ?? {} });
      if (!(v instanceof SavedView)) sv.__weftRef = v.__weftRef;
      if (!viewType || sv.viewType === viewType) out.push(sv);
    }
    return out;
  }

  /** Load a SavedView by name, `name:version` or ref. */
  getSavedView(name: string | Ref): Promise<SavedView> {
    return SavedView.load(name);
  }

  private async latestObjects(kind: string): Promise<any[]> {
    await this.flush();
    const res = await this.http.post('/api/v1/objects/query', { project_id: this.projectId, filter: { kinds: [kind], latest_only: true }, limit: 10000 });
    const out: any[] = [];
    for (const o of res.items ?? []) out.push(await this.fromVal(o.value, new Ref(this.entity, this.project, 'object', o.name, o.digest)));
    return out;
  }

  private async uriOf(obj: unknown): Promise<string> {
    if (typeof obj === 'string') return isRefUri(obj) ? obj : this.ref(obj).uri();
    if (obj instanceof Ref) return obj.uri();
    return (await this.save(obj)).uri();
  }

  /**
   * Run an evaluation server side (`POST /api/v1/evaluations/run`). The model
   * must be an LLMStructuredCompletionModel and the scorers server-runnable.
   * With `wait` (default) polls until done; resolves to `{id, status, summary}`
   * where `id` is the run (its `Evaluation.evaluate` span; `callId` is the
   * earlier name of the same value).
   */
  async evaluateModel(
    evaluation: unknown,
    model: unknown,
    opts: { wait?: boolean; timeoutMs?: number; pollIntervalMs?: number } = {},
  ): Promise<{ id: string; callId: string; status: Record<string, any>; summary: unknown }> {
    const body = { project_id: this.projectId, evaluation_ref: await this.uriOf(evaluation), model_ref: await this.uriOf(model) };
    await this.flush();
    const id: string = (await this.http.post('/api/v1/evaluations/run', body)).id;
    const result = { id, callId: id, status: { code: 'running' } as Record<string, any>, summary: null as unknown };
    if (opts.wait === false) return result;
    const deadline = Date.now() + (opts.timeoutMs ?? 600_000);
    for (;;) {
      const st = await this.evaluationStatus(id);
      result.status = st;
      if (st.code === 'complete' || st.code === 'failed') break;
      if (Date.now() > deadline) throw new Error(`evaluation ${id} still running after ${opts.timeoutMs ?? 600_000}ms`);
      await new Promise((r) => setTimeout(r, opts.pollIntervalMs ?? 500));
    }
    if (result.status.code === 'failed') throw new Error(`server evaluation ${id} failed: ${result.status.error}`);
    result.summary = (await this.span(id))?.output ?? null;
    return result;
  }

  /** `GET /api/v1/evaluations/{id}/status`: `{code: running|complete|failed|unknown, ...}`. */
  async evaluationStatus(runId: string): Promise<Record<string, any>> {
    return (await this.http.request('GET', `/api/v1/evaluations/${encodeURIComponent(runId)}/status`, undefined, { project_id: this.projectId })) ?? {};
  }

  /** `POST /api/v1/evaluations/compare` for evaluation run ids (`Evaluation.evaluate` spans). */
  async compareEvaluations(runIds: string[]): Promise<any> {
    await this.flush();
    return this.http.post('/api/v1/evaluations/compare', { project_id: this.projectId, ids: runIds });
  }

  /** Compute a leaderboard (`POST /api/v1/leaderboards/compute`) by name, ref or Leaderboard object. */
  async leaderboard(obj: string | Ref | WeftObject, version?: string): Promise<any> {
    let name: string;
    if (typeof obj === 'string' && !isRefUri(obj)) name = obj;
    else {
      const r = typeof obj === 'string' ? Ref.parse(obj) : obj instanceof Ref ? obj : (obj.__weftRef ?? (await this.save(obj)));
      name = r.name;
      version ??= r.digest;
    }
    const body: Record<string, unknown> = { project_id: this.projectId, name };
    if (version && version !== 'latest') body.version = version;
    await this.flush();
    return this.http.post('/api/v1/leaderboards/compute', body);
  }

  /**
   * Download URL(s) via `POST /api/v1/files/resolve`. `target` is a file digest, a
   * `weft-file://<digest>` / `s3://bucket/key` / `https://` URI, a Content
   * value, or an array of these (then an array is returned). Files in the
   * server's S3 backend and `s3://` references in its bucket get presigned URLs.
   */
  async resolveUrl(target: unknown, opts: { expiresIn?: number } = {}): Promise<string | string[]> {
    const one = (t: any): string => {
      if (t instanceof Content) return t.uri ?? t.digest;
      if (t && typeof t === 'object' && (t.uri || t.file_digest)) return String(t.uri ?? t.file_digest);
      return String(t);
    };
    const items = Array.isArray(target) ? target : [target];
    await this.flush();
    const body: Record<string, unknown> = { project_id: this.projectId, uris: items.map(one) };
    if (opts.expiresIn !== undefined) body.expires_in = Math.floor(opts.expiresIn);
    const urls: string[] = (await this.http.post('/api/v1/files/resolve', body)).download_urls ?? [];
    return Array.isArray(target) ? urls : urls[0];
  }

  /** Raw bytes of an uploaded file (a Content/media `file_digest`; `GET /api/v1/files/{digest}`). */
  async readFile(fileDigest: string): Promise<Uint8Array> {
    await this.flush();
    const res = await this.http.raw('GET', `/api/v1/files/${encodeURIComponent(fileDigest)}?${new URLSearchParams({ project_id: this.projectId })}`);
    return new Uint8Array(await res.arrayBuffer());
  }

  private fetched(d: SpanData): Span {
    const c = new Span(d, this);
    c.ended = true;
    c.fetched = true;
    return c;
  }

  /** Set a project price for `llmId` (`POST /api/v1/prices/create`); resolves to `[{id, llm_id}]`. */
  async addCost(llmId: string, promptTokenCost: number, completionTokenCost: number, extra: Record<string, unknown> = {}): Promise<any> {
    const res = await this.http.post('/api/v1/prices/create', {
      project_id: this.projectId,
      prices: { [llmId]: { prompt_token_cost: promptTokenCost, completion_token_cost: completionTokenCost, ...extra } },
    });
    return res.items ?? [];
  }

  /** Project prices (optionally only some LLM ids; `includeDefaults` adds the built-in prices). */
  async queryCosts(opts: { llmIds?: string[]; includeDefaults?: boolean } = {}): Promise<any[]> {
    const body: Record<string, unknown> = { project_id: this.projectId, include_defaults: !!opts.includeDefaults, limit: 10000 };
    if (opts.llmIds?.length) body.query = { $expr: { $in: [{ $getField: 'llm_id' }, opts.llmIds.map((x) => ({ $literal: x }))] } };
    return (await this.http.post('/api/v1/prices/query', body)).items ?? [];
  }

  /** Delete project prices by id (`queryCosts` rows, `addCost` results or ids). */
  async purgeCosts(ids: Array<string | [string, string] | { id: string }>): Promise<void> {
    const flat = ids.map((i) => (Array.isArray(i) ? i[0] : typeof i === 'object' ? i.id : i));
    await this.http.post('/api/v1/prices/delete', { project_id: this.projectId, ids: flat });
  }

  // ---------------------------------------------------------------- objects
  opRef(meta: OpMeta): Ref {
    const key = `${this.serverUrl}|${this.projectId}`;
    const cached = meta.refs.get(key);
    if (cached) return cached;
    const code = this.captureCode && meta.options.enableCodeCapture !== false ? meta.code : meta.code.includes('recorded by the weft SDK') ? meta.code : CODE_CAPTURE_DISABLED;
    const val = { _type: 'Op', name: meta.name, code, language: 'javascript', signature: meta.signature };
    const d = digest(val);
    const name = sanitizeName(meta.name);
    const ref = new Ref(this.entity, this.project, 'function', name, d);
    meta.refs.set(key, ref);
    this.sender.enqueue({
      kind: 'post',
      path: '/api/v1/objects/save',
      payload: { project_id: this.projectId, name, value: val },
      cb: (res) => {
        if (res?.digest && res.digest !== d) logger.warn(`weft: server digest for function ${name} differs from local digest`);
      },
    });
    return ref;
  }

  uploadFile(data: Uint8Array, mime: string): string {
    const d = fileDigest(data);
    if (!this.uploaded.has(d)) {
      this.uploaded.add(d);
      const ext = mime.split('/')[1] ?? 'bin';
      this.sender.enqueue({ kind: 'file', path: '/api/v1/files/upload', fields: { project_id: this.projectId }, data, filename: `file.${ext}`, mime });
    }
    return d;
  }

  private async createObj(name: string, val: unknown): Promise<Ref> {
    const objectName = sanitizeName(name);
    let d: string;
    let kind: string | undefined;
    try {
      const res = await this.http.post('/api/v1/objects/save', { project_id: this.projectId, name: objectName, value: val });
      d = res.digest;
      kind = res.kind;
    } catch (e) {
      if (!(e instanceof WeftHTTPError)) throw e;
      logger.warn(`weft: could not save ${objectName} (${e.message}); using a local digest`);
      d = digest(val);
    }
    return new Ref(this.entity, this.project, kind === 'function' ? 'function' : 'object', objectName, d);
  }

  /**
   * Save without waiting: digests are computed locally (they match the
   * server's) and the writes are queued on the background sender in order.
   */
  publishLocal(obj: WeftObject, name?: string): Ref {
    const val = obj.localVal(this);
    const objectId = sanitizeName(name ?? obj.name ?? obj.className);
    const d = digest(val);
    this.sender.enqueue({
      kind: 'post',
      path: '/api/v1/objects/save',
      payload: { project_id: this.projectId, name: objectId, value: val },
      cb: (res) => {
        if (res?.digest && res.digest !== d) logger.warn(`weft: server digest for ${objectId} differs from local digest`);
      },
    });
    const ref = new Ref(this.entity, this.project, 'object', objectId, d);
    obj.__weftRef = ref;
    return ref;
  }

  /** Field value for publishLocal: nested objects are published locally too. */
  publishNestedLocal(v: unknown): unknown {
    if (v instanceof WeftObject) return (v.__weftRef && v.__weftRef.projectId === this.projectId ? v.__weftRef : this.publishLocal(v)).uri();
    if (isOp(v)) return this.opRef(v.__weftOp).uri();
    if (Array.isArray(v)) return v.map((x) => this.publishNestedLocal(x));
    return toJson(v, this);
  }

  /** Queue a table create (and appends) with locally computed digests. */
  publishTableLocal(rows: unknown[]): { ref: Ref; rowDigests: string[] } {
    const json = rows.map((r) => toJson(r, this));
    const rowDigests = json.map((r) => digest(r));
    const chunk = 1000;
    let td = tableDigest(rowDigests.slice(0, chunk));
    this.sender.enqueue({ kind: 'post', path: '/api/v1/tables/create', payload: { project_id: this.projectId, rows: json.slice(0, chunk) } });
    for (let i = chunk; i < json.length; i += chunk) {
      this.sender.enqueue({
        kind: 'post',
        path: '/api/v1/tables/update',
        payload: { project_id: this.projectId, digest: td, updates: json.slice(i, i + chunk).map((row) => ({ append: { row } })) },
      });
      td = tableDigest(rowDigests.slice(0, i + chunk));
    }
    return { ref: new Ref(this.entity, this.project, 'table', td), rowDigests };
  }

  /** Append rows to a saved table (`POST /api/v1/tables/update`). */
  async appendTable(base: Ref, rows: unknown[]): Promise<{ ref: Ref; appended: string[] }> {
    let td = base.name;
    const appended: string[] = [];
    const json = rows.map((r) => toJson(r, this));
    for (let i = 0; i < json.length; i += 1000) {
      const res = await this.http.post('/api/v1/tables/update', {
        project_id: this.projectId,
        digest: td,
        updates: json.slice(i, i + 1000).map((row) => ({ append: { row } })),
      });
      td = res.digest;
      appended.push(...(res.updated_row_digests ?? []));
    }
    return { ref: new Ref(this.entity, this.project, 'table', td), appended };
  }

  // ---------------------------------------------------------------- object management
  /** One object version (`GET /api/v1/objects/{name}/versions/{version}`), or null when there is none. */
  async readObject(projectId: string, name: string, version = 'latest'): Promise<any | null> {
    try {
      return await this.http.request('GET', objectPath(name, version), undefined, { project_id: projectId });
    } catch (e) {
      if (e instanceof WeftHTTPError && e.status === 404) return null;
      throw e;
    }
  }

  /** Resolve a ref, URI, "name:v1"/"name:latest" or saved object to a ref with a concrete digest. */
  async resolveObjectRef(obj: Ref | string | { __weftRef?: Ref }): Promise<Ref> {
    let r: Ref | undefined = obj instanceof Ref ? obj : typeof obj === 'string' ? this.ref(obj) : obj.__weftRef;
    if (!r) throw new Error('object is not saved');
    if (r.digest === 'latest' || /^v\d+$/.test(r.digest)) {
      const o = await this.readObject(r.projectId, r.name, r.digest);
      if (!o) throw new Error(`object not found: ${r.uri()}`);
      r = new Ref(r.entity, r.project, r.kind, r.name, o.digest);
    }
    return r;
  }

  private async objectMeta(obj: Ref | string | { __weftRef?: Ref }): Promise<any> {
    const r = await this.resolveObjectRef(obj);
    const o = await this.readObject(r.projectId, r.name, r.digest);
    if (!o) throw new Error(`object not found: ${r.uri()}`);
    return o;
  }

  // ---------------------------------------------------------------- registry
  private registryTarget(targetPath: string): [string, string, string] {
    const parts = String(targetPath).trim().replace(/^\/+|\/+$/g, '').split('/');
    if (parts.length === 2 && parts.every(Boolean)) return [this.entity, parts[0], parts[1]];
    if (parts.length === 3 && parts.every(Boolean)) return [parts[0], parts[1], parts[2]];
    throw new Error(`targetPath must be '<registry_project>/<collection>' or '<entity>/<registry_project>/<collection>', got ${JSON.stringify(targetPath)}`);
  }

  /**
   * Link an object version into a registry collection (`POST /api/v1/registry/link`).
   * `obj` is a saved object (saved now if needed), a ref, a URI or
   * "name:version"; `targetPath` is `<registry_project>/<collection>` (in this
   * client's entity) or `<entity>/<registry_project>/<collection>`.
   */
  async linkToRegistry(obj: unknown, opts: { targetPath: string; aliases?: string[] }): Promise<{ version: number; versionIndex: number; ref: string; targetPath: string }> {
    const [entity, regProject, collection] = this.registryTarget(opts.targetPath);
    let r: Ref;
    if (obj instanceof Ref || typeof obj === 'string') r = await this.resolveObjectRef(obj);
    else if (obj && typeof obj === 'object' && (obj as { __weftRef?: Ref }).__weftRef) r = await this.resolveObjectRef(obj as { __weftRef?: Ref });
    else r = await this.save(obj);
    if (r.kind !== 'object' && r.kind !== 'function') throw new Error(`only object versions can be linked to a registry, got ${r.uri()}`);
    await this.flush();
    const res = await this.http.post('/api/v1/registry/link', {
      ref: r.uri(),
      project_id: `${entity}/${regProject}`,
      collection,
      aliases: opts.aliases ?? [],
    });
    return { version: res.version, versionIndex: res.version, ref: r.uri(), targetPath: `${entity}/${regProject}/${collection}` };
  }

  /** Link a saved prompt version into a registry collection. */
  linkPromptToRegistry(prompt: unknown, opts: { targetPath: string; aliases?: string[] }): Promise<{ version: number; versionIndex: number; ref: string; targetPath: string }> {
    return this.linkToRegistry(prompt, opts);
  }

  /** Collections and versions of a registry project (`POST /api/v1/registry/query`), newest first. */
  async queryRegistry(registryProject: string, opts: { collection?: string; alias?: string; versionIndex?: number } = {}): Promise<RegistryCollection[]> {
    const body: Record<string, unknown> = { project_id: registryProject.includes('/') ? registryProject : `${this.entity}/${registryProject}` };
    if (opts.collection) body.portfolio_name = opts.collection;
    if (opts.alias) body.alias = opts.alias;
    if (opts.versionIndex !== undefined) body.version_index = opts.versionIndex;
    return (await this.http.post('/api/v1/registry/query', body)).collections ?? [];
  }

  /** Load the object a registry version points to (`version`: "latest", "v2", 2 or an alias). */
  async getFromRegistry(targetPath: string, version: string | number = 'latest'): Promise<any> {
    const [entity, regProject, collection] = this.registryTarget(targetPath);
    const opts: { collection: string; alias?: string; versionIndex?: number } = { collection };
    if (typeof version === 'number' || /^v?\d+$/.test(version)) opts.versionIndex = Number(String(version).replace(/^v/, ''));
    else if (version && version !== 'latest') opts.alias = version;
    const cols = await this.queryRegistry(`${entity}/${regProject}`, opts);
    const v = cols[0]?.versions?.[0];
    if (!v) throw new Error(`no version ${JSON.stringify(version)} in registry collection ${entity}/${regProject}/${collection}`);
    return this.get(v.ref);
  }

  private async deleteObject(projectId: string, name: string, digests?: string[]): Promise<number> {
    const body: Record<string, unknown> = { project_id: projectId, name };
    if (digests) body.digests = digests;
    return (await this.http.post('/api/v1/objects/delete', body)).num_deleted ?? 0;
  }

  async deleteObjectVersion(obj: Ref | string | { __weftRef?: Ref }): Promise<number> {
    const r = await this.resolveObjectRef(obj);
    return this.deleteObject(r.projectId, r.name, [r.digest]);
  }

  /** Delete versions of `name` by digest (or "v0"/"latest"). */
  deleteObjectVersions(name: string, digests: string[]): Promise<number> {
    return this.deleteObject(this.projectId, name, digests);
  }

  deleteAllObjectVersions(name: string): Promise<number> {
    return this.deleteObject(this.projectId, name);
  }

  /** Point alias(es) (e.g. "production") at an object version. */
  async setAliases(obj: Ref | string | { __weftRef?: Ref }, alias: string | string[]): Promise<void> {
    const r = await this.resolveObjectRef(obj);
    const aliases = Array.isArray(alias) ? alias : [alias];
    if (!aliases.length) return;
    await this.http.post(`${objectPath(r.name)}/aliases`, { project_id: r.projectId, version: r.digest, aliases });
  }

  /** Remove alias(es) from an object (by name or ref). */
  async removeAliases(obj: Ref | string, alias: string | string[]): Promise<void> {
    const name = typeof obj === 'string' && !isRefUri(obj) && !obj.includes(':') ? obj : (await this.resolveObjectRef(obj)).name;
    const aliases = Array.isArray(alias) ? alias : [alias];
    if (!aliases.length) return;
    await this.http.post(`${objectPath(name)}/aliases/remove`, { project_id: this.projectId, aliases });
  }

  async getAliases(obj: Ref | string | { __weftRef?: Ref }): Promise<string[]> {
    return (await this.objectMeta(obj)).aliases ?? [];
  }

  async addTags(obj: Ref | string | { __weftRef?: Ref }, tags: string[]): Promise<void> {
    const r = await this.resolveObjectRef(obj);
    await this.http.post(`${objectPath(r.name, r.digest)}/tags`, { project_id: r.projectId, tags });
  }

  async removeTags(obj: Ref | string | { __weftRef?: Ref }, tags: string[]): Promise<void> {
    const r = await this.resolveObjectRef(obj);
    await this.http.post(`${objectPath(r.name, r.digest)}/tags/remove`, { project_id: r.projectId, tags });
  }

  async getTags(obj: Ref | string | { __weftRef?: Ref }): Promise<string[]> {
    return (await this.objectMeta(obj)).tags ?? [];
  }

  /** All tags used in the project. */
  async listTags(): Promise<string[]> {
    return (await this.http.post('/api/v1/objects/tags', { project_id: this.projectId })).items ?? [];
  }

  /** All distinct aliases in the project. */
  async listAliases(): Promise<string[]> {
    return (await this.http.post('/api/v1/objects/aliases', { project_id: this.projectId })).items ?? [];
  }

  /** `GET /auth/whoami`. */
  whoami(): Promise<{ user_id: string; auth_required: boolean }> {
    return this.http.get('/auth/whoami');
  }

  /** Value for a field of an object being saved: nested objects become refs. */
  async publishNested(v: unknown): Promise<unknown> {
    if (v instanceof WeftObject) return (await this.save(v)).uri();
    if (isOp(v)) return this.opRef(v.__weftOp).uri();
    if (Array.isArray(v)) return Promise.all(v.map((x) => this.publishNested(x)));
    return toJson(v, this);
  }

  /**
   * Save a version of `obj` (content addressed) and return its ref: a
   * WeftObject (Dataset, Model, Prompt, scorers, ...), a traced function or
   * any JSON value. Saving an identical value again returns the same version.
   */
  async save(obj: unknown, name?: string): Promise<Ref> {
    if (isOp(obj)) return this.opRef(obj.__weftOp);
    if (obj instanceof WeftObject) {
      const val = await obj.publishVal(this);
      const ref = await this.createObj(name ?? obj.name ?? obj.className, val);
      obj.__weftRef = ref;
      return ref;
    }
    const val = toJson(obj, this);
    return this.createObj(name ?? 'object', val);
  }

  /** @deprecated use `save` */
  publish(obj: unknown, name?: string): Promise<Ref> {
    deprecated('WeftClient.publish()', 'WeftClient.save()');
    return this.save(obj, name);
  }

  async publishTable(rows: unknown[]): Promise<{ ref: Ref; rowDigests: string[] }> {
    const json = rows.map((r) => toJson(r, this));
    const chunk = 1000;
    let tdigest: string;
    let rowDigests: string[] = [];
    try {
      let res = await this.http.post('/api/v1/tables/create', { project_id: this.projectId, rows: json.slice(0, chunk) });
      tdigest = res.digest;
      rowDigests = [...(res.row_digests ?? [])];
      for (let i = chunk; i < json.length; i += chunk) {
        res = await this.http.post('/api/v1/tables/update', {
          project_id: this.projectId,
          digest: tdigest,
          updates: json.slice(i, i + chunk).map((row) => ({ append: { row } })),
        });
        tdigest = res.digest;
        rowDigests.push(...(res.updated_row_digests ?? []));
      }
    } catch (e) {
      if (!(e instanceof WeftHTTPError)) throw e;
      logger.warn(`weft: could not save table (${e.message}); using local digests`);
      rowDigests = json.map((r) => digest(r));
      tdigest = tableDigest(rowDigests);
    }
    return { ref: new Ref(this.entity, this.project, 'table', tdigest), rowDigests };
  }

  /** Build a ref from "name", "name:v3", "name:latest", "name:<digest>" or a URI. */
  ref(s: string | Ref): Ref {
    if (s instanceof Ref) return s;
    if (isRefUri(s)) return Ref.parse(s);
    const i = s.lastIndexOf(':');
    return new Ref(this.entity, this.project, 'object', i < 0 ? s : s.slice(0, i), i < 0 ? 'latest' : s.slice(i + 1) || 'latest');
  }

  /** Read what a ref (or "name", "name:v3", "name:latest", a URI) points to. Datasets come back as Dataset instances. */
  async get(s: string | Ref): Promise<any> {
    const r = this.ref(s);
    await this.flush();
    if (r.kind === 'span' && !r.extra.length) return this.span(r.name);
    if ((r.kind === 'object' || r.kind === 'function') && !r.extra.length) {
      const obj = await this.readObject(r.projectId, r.name, r.digest);
      if (!obj) throw new WeftHTTPError(404, `object not found: ${r.uri()}`, objectPath(r.name, r.digest));
      const real = new Ref(r.entity, r.project, obj.kind === 'function' ? 'function' : 'object', r.name, obj.digest);
      return this.fromVal(obj.value, real);
    }
    if (r.kind === 'table' && !r.extra.length) return (await this.readTable(r)).rows;
    const res = await this.http.post('/api/v1/refs/read', { refs: [r.uri()] });
    return this.fromVal(res.items?.[0] ?? null, undefined);
  }

  private async readTable(r: Ref): Promise<{ rows: any[]; digests: string[] }> {
    const rows: any[] = [];
    const digests: string[] = [];
    for (let offset = 0; ; ) {
      const res = await this.http.post('/api/v1/tables/query', { project_id: r.projectId, digest: r.name, limit: 1000, offset });
      const page = res.items ?? [];
      for (const row of page) {
        rows.push(row.value);
        digests.push(row.digest);
      }
      if (page.length < 1000) break;
      offset += page.length;
    }
    return { rows, digests };
  }

  private async fromVal(val: any, ref: Ref | undefined): Promise<any> {
    if (val && typeof val === 'object' && val._type === 'Op') return loadOp(val, ref, this);
    const platform = val && typeof val === 'object' ? (loadPlatformObject(val, ref) ?? loadPromptOrModel(val, ref)) : null;
    if (platform) return platform;
    if (val && typeof val === 'object' && val._class_name === 'Dataset' && isRefUri(val.rows)) {
      const t = Ref.parse(val.rows);
      const { rows, digests } = await this.readTable(t);
      const ds = new Dataset({ name: val.name ?? undefined, description: val.description, rows });
      ds.tableRef = t;
      ds.rowDigests = digests;
      ds.__weftRef = ref;
      return ds;
    }
    if (val && typeof val === 'object' && ref) Object.defineProperty(val, '__weftRef', { value: ref, enumerable: false, writable: true });
    return val;
  }

  /** Run a chat completion through the server (`POST /api/v1/completions`). */
  async completionsCreate(inputs: Record<string, unknown>, trackLlmCall = false): Promise<any> {
    const res = await this.http.post('/api/v1/completions', { project_id: this.projectId, inputs, track: trackLlmCall }, 300_000);
    if (res.response?.error) throw new Error(`completion failed: ${res.response.error}`);
    return res.response;
  }
}

let exitHookInstalled = false;
function installExitHook(): void {
  if (exitHookInstalled) return;
  exitHookInstalled = true;
  process.on('beforeExit', () => {
    const c = getClient();
    if (c && c.sender.pending > 0) void c.flush();
  });
  process.on('exit', () => {
    getClient()?.sender.spillPending();
  });
}

/** Initialise tracing for `project` ("project" or "entity/project"). */
export async function init(project?: string, opts: InitOptions = {}): Promise<WeftClient> {
  const p = project ?? process.env.WEFT_PROJECT;
  if (!p) throw new Error('weft.init requires a project name (or WEFT_PROJECT)');
  const envDisabled = ['1', 'true', 'yes', 'on'].includes((process.env.WEFT_DISABLED ?? '').toLowerCase());
  const old = getClient();
  if (old) await old.flush();
  const client = new WeftClient(p, { ...opts, disabled: opts.disabled || envDisabled });
  if (opts.autopatchSettings !== undefined) configureIntegrations(opts.autopatchSettings);
  setGlobalClient(client);
  installExitHook();
  return client;
}

/** Flush pending data and detach the global client. */
export async function finish(): Promise<void> {
  const c = getClient();
  setGlobalClient(null);
  if (c) await c.flush();
}

export async function flush(): Promise<void> {
  await getClient()?.flush();
}
