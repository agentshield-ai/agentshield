import { Call, type CallData } from './call.js';
import { b64digest, digest } from './canonical.js';
import { currentContext, getCurrentCall } from './context.js';
import { getClient, setGlobalClient } from './global.js';
import { HttpClient, VERSION, WeftHTTPError } from './http.js';
import { uuid7 } from './ids.js';
import { loadOp } from './loader.js';
import { Dataset, WeftObject } from './objects.js';
import { loadPlatformObject } from './platform.js';
import { op as makeOp, isOp, type OpMeta } from './op.js';
import { Ref, isRefUri, splitProject } from './refs.js';
import { Redactor, type RedactSettings } from './redact.js';
import { Sender, type SenderOptions } from './sender.js';
import { getRef, toJson } from './serialize.js';

export const DEFAULT_SERVER_URL = 'http://localhost:6345';

export interface InitOptions {
  serverUrl?: string;
  apiKey?: string;
  /** Disable tracing (also WEFT_DISABLED=true). */
  disabled?: boolean;
  sender?: SenderOptions;
  /** Redaction settings (default: redact credential-like keys). */
  settings?: RedactSettings;
}

export interface GetCallsOptions {
  filter?: Record<string, unknown>;
  query?: Record<string, unknown>;
  sortBy?: Array<{ field: string; direction: 'asc' | 'desc' }>;
  limit?: number;
  offset?: number;
  search?: string;
  includeCosts?: boolean;
  includeFeedback?: boolean;
}

const NAME_RE = /[^A-Za-z0-9_.-]+/g;

export function sanitizeName(name: string): string {
  return name.trim().replace(NAME_RE, '-').replace(/^-+|-+$/g, '').slice(0, 128) || 'object';
}

function nowIso(): string {
  // microsecond-style precision is not available; milliseconds are fine
  return new Date().toISOString();
}

export function formatError(e: unknown): string {
  if (e instanceof Error) {
    const head = `${e.name}: ${e.message}`;
    return e.stack ? `${head}\n\n${e.stack}` : head;
  }
  return `Error: ${String(e)}`;
}

const synthetic = new Map<string, OpMeta>();
export function syntheticOp(name: string): OpMeta {
  let m = synthetic.get(name);
  if (!m) {
    const fn = makeOp(function recorded() {}, { name });
    m = fn.__weftOp;
    m.code = `// ${name}: recorded by the weft SDK\n`;
    synthetic.set(name, m);
  }
  return m;
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

  constructor(project: string, opts: InitOptions = {}) {
    [this.entity, this.project] = splitProject(project);
    this.serverUrl = (opts.serverUrl ?? process.env.WEFT_SERVER_URL ?? DEFAULT_SERVER_URL).replace(/\/+$/, '');
    this.http = new HttpClient(this.serverUrl, opts.apiKey ?? process.env.WEFT_API_KEY);
    this.sender = new Sender(this.http, opts.sender);
    this.disabled = !!opts.disabled;
    this.redactor = new Redactor(opts.settings);
  }

  get projectId(): string {
    return `${this.entity}/${this.project}`;
  }

  flush(): Promise<void> {
    return this.sender.flush();
  }

  serverInfo(): Promise<any> {
    return this.http.get('/server_info');
  }

  // ---------------------------------------------------------------- calls
  /** Start a call explicitly; end it with finishCall. `parent: null` starts a new trace. */
  createCall(
    op: OpMeta | string,
    inputs: Record<string, unknown>,
    opts: { parent?: Call | null; displayName?: string; attributes?: Record<string, unknown> } = {},
  ): Call {
    const meta = typeof op === 'string' ? syntheticOp(op) : op;
    const parent = opts.parent === undefined ? getCurrentCall() : opts.parent;
    const id = uuid7();
    const ctx = currentContext();
    let threadId: string | null = null;
    let turnId: string | null = null;
    if (ctx.thread) {
      threadId = ctx.thread.threadId;
      turnId = !parent || parent.id === ctx.thread.entryParentId || parent.threadId !== threadId ? id : parent.turnId;
    } else if (parent?.threadId) {
      threadId = parent.threadId;
      turnId = parent.turnId;
    }
    let raw = inputs;
    if (meta.options.postprocessInputs) {
      try {
        raw = meta.options.postprocessInputs({ ...inputs });
      } catch (e) {
        console.warn(`weft: postprocessInputs failed for ${meta.name}: ${(e as Error).message}`);
      }
    }
    const ser: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(raw)) if (k !== 'self') ser[k] = toJson(v, this);
    const redacted = this.redactor.apply(ser) as Record<string, unknown>;
    const finalInputs: Record<string, unknown> = 'self' in raw ? { self: this.serializeSelf(raw.self), ...redacted } : redacted;
    const attributes = toJson({ ...ctx.attributes, ...(opts.attributes ?? {}) }, this) as Record<string, unknown>;
    if (!('weft' in attributes)) attributes.weft = { client_version: VERSION, source: 'typescript-sdk', node_version: process.version };
    const data: CallData = {
      id,
      project_id: this.projectId,
      op_name: this.opRef(meta).uri(),
      trace_id: parent ? parent.traceId : uuid7(),
      parent_id: parent ? parent.id : null,
      started_at: nowIso(),
      attributes,
      inputs: finalInputs,
    };
    const call = new Call(data, this);
    call.rawInputs = { ...inputs };
    call.threadId = threadId;
    call.turnId = turnId;
    let dn = opts.displayName ?? null;
    if (!dn && meta.options.callDisplayName) {
      try {
        const f = meta.options.callDisplayName;
        dn = typeof f === 'function' ? f(call) : f;
      } catch (e) {
        console.warn(`weft: callDisplayName failed for ${meta.name}: ${(e as Error).message}`);
      }
    }
    call.displayName = dn;
    const start: Record<string, unknown> = { ...data };
    if (dn) start.display_name = dn;
    if (threadId) start.thread_id = threadId;
    if (turnId) start.turn_id = turnId;
    this.sender.enqueue({ kind: 'call_start', payload: start });
    return call;
  }

  finishCall(call: Call, output?: unknown, error?: unknown, meta?: OpMeta): void {
    if (call.ended) return;
    call.ended = true;
    let out = output;
    if (meta?.options.postprocessOutput && error === undefined) {
      try {
        out = meta.options.postprocessOutput(out);
      } catch (e) {
        console.warn(`weft: postprocessOutput failed for ${meta.name}: ${(e as Error).message}`);
      }
    }
    call.endedAt = nowIso();
    call.rawOutput = out;
    call.output = this.redactor.apply(toJson(out, this));
    const end: Record<string, unknown> = { project_id: call.projectId, id: call.id, ended_at: call.endedAt, output: call.output };
    if (error !== undefined) {
      call.exception = formatError(error);
      end.exception = call.exception;
    }
    if (Object.keys(call.summary).length) end.summary = toJson(call.summary, this);
    this.sender.enqueue({ kind: 'call_end', payload: end });
  }

  private serializeSelf(v: unknown): unknown {
    const r = getRef(v);
    if (r) return r.uri();
    if (v instanceof WeftObject) return v.toVal(this);
    const name = v && typeof v === 'object' ? (v as object).constructor?.name : typeof v;
    return `<${name}>`;
  }

  async getCalls(opts: GetCallsOptions = {}): Promise<Call[]> {
    await this.flush();
    const body: Record<string, unknown> = { project_id: this.projectId };
    if (opts.filter) body.filter = opts.filter;
    if (opts.query) body.query = '$expr' in opts.query ? opts.query : { $expr: opts.query };
    if (opts.sortBy) body.sort_by = opts.sortBy;
    if (opts.limit !== undefined) body.limit = opts.limit;
    if (opts.offset !== undefined) body.offset = opts.offset;
    if (opts.search) body.search = opts.search;
    if (opts.includeCosts) body.include_costs = true;
    if (opts.includeFeedback) body.include_feedback = true;
    const rows = await this.http.postJsonl<CallData>('/calls/stream_query', body);
    return rows.map((d) => this.fetched(d));
  }

  async getCall(id: string, opts: { includeCosts?: boolean; includeFeedback?: boolean } = {}): Promise<Call | null> {
    await this.flush();
    const res = await this.http.post('/call/read', {
      project_id: this.projectId,
      id,
      include_costs: !!opts.includeCosts,
      include_feedback: !!opts.includeFeedback,
    });
    return res.call ? this.fetched(res.call) : null;
  }

  private fetched(d: CallData): Call {
    const c = new Call(d, this);
    c.ended = true;
    c.fetched = true;
    return c;
  }

  async addCost(llmId: string, promptTokenCost: number, completionTokenCost: number, extra: Record<string, unknown> = {}): Promise<any> {
    const res = await this.http.post('/cost/create', {
      project_id: this.projectId,
      costs: { [llmId]: { prompt_token_cost: promptTokenCost, completion_token_cost: completionTokenCost, ...extra } },
    });
    return res.ids ?? [];
  }

  // ---------------------------------------------------------------- objects
  opRef(meta: OpMeta): Ref {
    const key = `${this.serverUrl}|${this.projectId}`;
    const cached = meta.refs.get(key);
    if (cached) return cached;
    const val = { _type: 'Op', name: meta.name, code: meta.code, language: 'javascript', signature: meta.signature };
    const d = digest(val);
    const name = sanitizeName(meta.name);
    const ref = new Ref(this.entity, this.project, 'op', name, d);
    meta.refs.set(key, ref);
    this.sender.enqueue({
      kind: 'post',
      path: '/obj/create',
      payload: { obj: { project_id: this.projectId, object_id: name, val } },
      cb: (res) => {
        if (res?.digest && res.digest !== d) console.warn(`weft: server digest for op ${name} differs from local digest`);
      },
    });
    return ref;
  }

  uploadFile(data: Uint8Array, mime: string): string {
    const d = b64digest(data);
    if (!this.uploaded.has(d)) {
      this.uploaded.add(d);
      const ext = mime.split('/')[1] ?? 'bin';
      this.sender.enqueue({ kind: 'file', path: '/file/create', fields: { project_id: this.projectId }, data, filename: `file.${ext}`, mime });
    }
    return d;
  }

  private async createObj(name: string, val: unknown): Promise<Ref> {
    const objectId = sanitizeName(name);
    let d: string;
    try {
      const res = await this.http.post('/obj/create', { obj: { project_id: this.projectId, object_id: objectId, val } });
      d = res.digest;
    } catch (e) {
      if (!(e instanceof WeftHTTPError)) throw e;
      console.warn(`weft: could not publish ${objectId} (${e.message}); using a local digest`);
      d = digest(val);
    }
    return new Ref(this.entity, this.project, 'object', objectId, d);
  }

  /**
   * Publish without waiting: digests are computed locally (they match the
   * server's) and the writes are queued on the background sender in order.
   */
  publishLocal(obj: WeftObject, name?: string): Ref {
    const val = obj.localVal(this);
    const objectId = sanitizeName(name ?? obj.name ?? obj.className);
    const d = digest(val);
    this.sender.enqueue({
      kind: 'post',
      path: '/obj/create',
      payload: { obj: { project_id: this.projectId, object_id: objectId, val } },
      cb: (res) => {
        if (res?.digest && res.digest !== d) console.warn(`weft: server digest for ${objectId} differs from local digest`);
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
    let td = digest(rowDigests.slice(0, chunk));
    this.sender.enqueue({ kind: 'post', path: '/table/create', payload: { table: { project_id: this.projectId, rows: json.slice(0, chunk) } } });
    for (let i = chunk; i < json.length; i += chunk) {
      this.sender.enqueue({
        kind: 'post',
        path: '/table/update',
        payload: { project_id: this.projectId, base_digest: td, updates: json.slice(i, i + chunk).map((row) => ({ append: { row } })) },
      });
      td = digest(rowDigests.slice(0, i + chunk));
    }
    return { ref: new Ref(this.entity, this.project, 'table', td), rowDigests };
  }

  /** Append rows to a published table with /table/update. */
  async appendTable(base: Ref, rows: unknown[]): Promise<{ ref: Ref; appended: string[] }> {
    let td = base.name;
    const appended: string[] = [];
    const json = rows.map((r) => toJson(r, this));
    for (let i = 0; i < json.length; i += 1000) {
      const res = await this.http.post('/table/update', {
        project_id: this.projectId,
        base_digest: td,
        updates: json.slice(i, i + 1000).map((row) => ({ append: { row } })),
      });
      td = res.digest;
      appended.push(...(res.updated_row_digests ?? []));
    }
    return { ref: new Ref(this.entity, this.project, 'table', td), appended };
  }

  /** `GET /auth/whoami`. */
  whoami(): Promise<{ user_id: string; auth_required: boolean }> {
    return this.http.get('/auth/whoami');
  }

  /** Value for a field of an object being published: nested objects become refs. */
  async publishNested(v: unknown): Promise<unknown> {
    if (v instanceof WeftObject) return (await this.publish(v)).uri();
    if (isOp(v)) return this.opRef(v.__weftOp).uri();
    if (Array.isArray(v)) return Promise.all(v.map((x) => this.publishNested(x)));
    return toJson(v, this);
  }

  /** Publish an object (content addressed) and return its ref. */
  async publish(obj: unknown, name?: string): Promise<Ref> {
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

  async publishTable(rows: unknown[]): Promise<{ ref: Ref; rowDigests: string[] }> {
    const json = rows.map((r) => toJson(r, this));
    const chunk = 1000;
    let tdigest: string;
    let rowDigests: string[] = [];
    try {
      let res = await this.http.post('/table/create', { table: { project_id: this.projectId, rows: json.slice(0, chunk) } });
      tdigest = res.digest;
      rowDigests = [...(res.row_digests ?? [])];
      for (let i = chunk; i < json.length; i += chunk) {
        res = await this.http.post('/table/update', {
          project_id: this.projectId,
          base_digest: tdigest,
          updates: json.slice(i, i + chunk).map((row) => ({ append: { row } })),
        });
        tdigest = res.digest;
        rowDigests.push(...(res.updated_row_digests ?? []));
      }
    } catch (e) {
      if (!(e instanceof WeftHTTPError)) throw e;
      console.warn(`weft: could not publish table (${e.message}); using local digests`);
      rowDigests = json.map((r) => digest(r));
      tdigest = digest(rowDigests);
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

  /** Load what a ref points to. Datasets come back as Dataset instances. */
  async get(s: string | Ref): Promise<any> {
    const r = this.ref(s);
    await this.flush();
    if (r.kind === 'call' && !r.extra.length) return this.getCall(r.name);
    if ((r.kind === 'object' || r.kind === 'op') && !r.extra.length) {
      const res = await this.http.post('/obj/read', { project_id: r.projectId, object_id: r.name, digest: r.digest });
      const obj = res.obj;
      const real = new Ref(r.entity, r.project, obj.kind === 'op' ? 'op' : 'object', r.name, obj.digest);
      return this.fromVal(obj.val, real);
    }
    if (r.kind === 'table' && !r.extra.length) return (await this.readTable(r)).rows;
    const res = await this.http.post('/refs/read_batch', { refs: [r.uri()] });
    return this.fromVal(res.vals?.[0] ?? null, undefined);
  }

  private async readTable(r: Ref): Promise<{ rows: any[]; digests: string[] }> {
    const rows: any[] = [];
    const digests: string[] = [];
    for (let offset = 0; ; ) {
      const res = await this.http.post('/table/query', { project_id: r.projectId, digest: r.name, limit: 1000, offset });
      const page = res.rows ?? [];
      for (const row of page) {
        rows.push(row.val);
        digests.push(row.digest);
      }
      if (page.length < 1000) break;
      offset += page.length;
    }
    return { rows, digests };
  }

  private async fromVal(val: any, ref: Ref | undefined): Promise<any> {
    if (val && typeof val === 'object' && val._type === 'Op') return loadOp(val, ref, this);
    const platform = val && typeof val === 'object' ? loadPlatformObject(val, ref) : null;
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

  /** Run a chat completion through the server's /completions/create. */
  async completionsCreate(inputs: Record<string, unknown>, trackLlmCall = false): Promise<any> {
    const res = await this.http.post('/completions/create', { project_id: this.projectId, inputs, track_llm_call: trackLlmCall }, 300_000);
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
}

/** Initialise tracing for `project` ("project" or "entity/project"). */
export async function init(project?: string, opts: InitOptions = {}): Promise<WeftClient> {
  const p = project ?? process.env.WEFT_PROJECT;
  if (!p) throw new Error('weft.init requires a project name (or WEFT_PROJECT)');
  const envDisabled = ['1', 'true', 'yes', 'on'].includes((process.env.WEFT_DISABLED ?? '').toLowerCase());
  const old = getClient();
  if (old) await old.flush();
  const client = new WeftClient(p, { ...opts, disabled: opts.disabled || envDisabled });
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
