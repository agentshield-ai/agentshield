import { createServer, type IncomingMessage, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import { digest, fileDigest, tableDigest } from '../src/canonical.js';

export interface Recorded {
  method: string;
  path: string;
  body: any;
  headers: IncomingMessage['headers'];
}

const KINDS: Record<string, string> = {
  Dataset: 'dataset',
  Model: 'model',
  Evaluation: 'evaluation',
  Scorer: 'scorer',
  Prompt: 'prompt',
  SavedView: 'saved_view',
  Leaderboard: 'leaderboard',
  Monitor: 'monitor',
  AnnotationSpec: 'annotation_spec',
};

function kindOf(val: any): string {
  if (!val || typeof val !== 'object') return 'object';
  if (val._type === 'Op') return 'function';
  for (const c of [val._class_name, ...(val._bases ?? [])]) if (KINDS[c]) return KINDS[c];
  return 'object';
}

/**
 * In-process fake Weft server serving Weft's own /api/v1 routes and recording
 * requests. Stored spans use Weft's names (`name`, `error`, `session_id`),
 * objects `{name, digest, version, kind, value}`, table rows `{digest, value}`.
 */
export class FakeWeft {
  requests: Recorded[] = [];
  calls = new Map<string, any>();
  objs = new Map<string, any[]>();
  tables = new Map<string, Array<{ digest: string; value: any }>>();
  files = new Map<string, Buffer>();
  feedback: any[] = [];
  failPaths = new Map<string, number>();
  failStatus: number | null = null;
  tags = new Map<string, string[]>(); // `${name}|${digest}`
  aliases = new Map<string, string>(); // `${name}|${alias}` -> digest
  embeddingsEnabled = false;
  /** Extra routes for individual tests: path -> handler(body, or the query of a GET). */
  routes = new Map<string, (body: any) => unknown>();
  url = '';
  private server: Server;

  constructor() {
    this.server = createServer(async (req, res) => {
      const chunks: Buffer[] = [];
      for await (const c of req) chunks.push(c as Buffer);
      const raw = Buffer.concat(chunks);
      const ctype = req.headers['content-type'] ?? '';
      const body = ctype.startsWith('application/json') && raw.length ? JSON.parse(raw.toString()) : null;
      if (this.failStatus) {
        this.requests.push({ method: req.method ?? '', path: req.url ?? '', body, headers: req.headers });
        res.writeHead(this.failStatus, { 'Content-Type': 'application/json' });
        return res.end(JSON.stringify({ reason: 'injected' }));
      }
      const path = req.url ?? '';
      this.requests.push({ method: req.method ?? '', path, body, headers: req.headers });
      const send = (status: number, data: unknown, type = 'application/json') => {
        res.writeHead(status, { 'Content-Type': type });
        res.end(typeof data === 'string' ? data : JSON.stringify(data));
      };
      const left = this.failPaths.get(path) ?? 0;
      if (left > 0) {
        this.failPaths.set(path, left - 1);
        return send(503, { reason: 'injected' });
      }
      try {
        const [p, qs] = path.split('?');
        const q = new URLSearchParams(qs ?? '');
        if (req.method === 'PUT') {
          const m = /^\/api\/v1\/runtimes\/([^/]+)$/.exec(p);
          if (m) {
            const name = decodeURIComponent(m[1]);
            const ids = body.runtime_ids.map((r: any) => ({ id: r.id, max_tokens: r.max_tokens ?? 4096, playground_id: `${name}/${r.id}` }));
            return send(200, { name, base_url: body.base_url, api_key_secret: body.api_key_secret ?? null, headers: body.headers ?? {}, runtime_ids: ids, kind: 'openai' });
          }
          return send(404, { reason: 'no route' });
        }
        if (req.method === 'GET') {
          const route = this.routes.get(p);
          if (route) return send(200, route(Object.fromEntries(q)));
          let m = /^\/api\/v1\/files\/([^/]+)$/.exec(p);
          if (m) {
            const d = decodeURIComponent(m[1]);
            if (!this.files.has(d)) return send(404, { reason: 'no file' });
            res.writeHead(200, { 'Content-Type': 'application/octet-stream' });
            return res.end(this.files.get(d));
          }
          m = /^\/api\/v1\/objects\/([^/]+)(?:\/versions\/([^/]+))?$/.exec(p);
          if (m) {
            const v = this.version(decodeURIComponent(m[1]), decodeURIComponent(m[2] ?? 'latest'));
            if (!v) return send(404, { reason: 'not found' });
            return send(200, this.object(q.get('project_id') ?? '', v));
          }
          m = /^\/api\/v1\/spans\/([^/]+)$/.exec(p);
          if (m) {
            const c = this.calls.get(decodeURIComponent(m[1]));
            return c ? send(200, c) : send(404, { reason: 'not found' });
          }
          if (p === '/auth/whoami') return send(200, { user_id: 'anonymous', auth_required: false });
          if (p === '/api/v1/server') return send(200, { name: 'weft', version: 'fake', auth_required: false, otel_endpoint: '/otel/v1/traces' });
          return send(404, { reason: 'no route' });
        }
        const out = this.handle(path, body, raw, ctype);
        if (typeof out === 'string') return send(200, out, 'application/x-ndjson');
        return send(200, out);
      } catch (e) {
        return send(404, { reason: String(e) });
      }
    });
  }

  async start(): Promise<this> {
    await new Promise<void>((r) => this.server.listen(0, '127.0.0.1', r));
    this.url = `http://127.0.0.1:${(this.server.address() as AddressInfo).port}`;
    return this;
  }

  close(): Promise<void> {
    return new Promise((r) => this.server.close(() => r()));
  }

  /** The function (base) name of a span. */
  opBase(c: any): string {
    const n: string = c.name ?? '';
    return n.startsWith('weft:///') ? n.split('/').pop()!.replace(/:[^:]*$/, '') : n;
  }

  callsByOp(name: string): any[] {
    return [...this.calls.values()].filter((c) => this.opBase(c) === name).sort((a, b) => (a.started_at < b.started_at ? -1 : a.started_at > b.started_at ? 1 : a.id < b.id ? -1 : 1));
  }

  children(id: string): any[] {
    return [...this.calls.values()].filter((c) => c.parent_id === id).sort((a, b) => (a.id < b.id ? -1 : 1));
  }

  private version(name: string, version: string): any | undefined {
    const versions = this.objs.get(name) ?? [];
    if (version === 'latest') return versions.at(-1);
    if (/^v\d+$/.test(version)) return versions.find((v) => v.version === Number(version.slice(1)));
    const aliased = this.aliases.get(`${name}|${version}`);
    return versions.find((v) => v.digest === (aliased ?? version));
  }

  private object(projectId: string, v: any): any {
    const [e, p] = projectId.split('/');
    return {
      ...v,
      project_id: projectId,
      ref: `weft:///${e}/${p}/${v.kind === 'function' ? 'function' : 'object'}/${v.name}:${v.digest}`,
      tags: this.tags.get(`${v.name}|${v.digest}`) ?? [],
      aliases: [...this.aliases.entries()].filter(([k, d]) => k.startsWith(`${v.name}|`) && d === v.digest).map(([k]) => k.split('|')[1]).sort(),
    };
  }

  private handle(path: string, body: any, raw: Buffer, ctype: string): unknown {
    const extra = this.routes.get(path);
    if (extra) return extra(body);
    let m = /^\/api\/v1\/objects\/([^/]+)\/aliases(\/remove)?$/.exec(path);
    if (m && !['tags', 'aliases'].includes(m[1])) {
      const name = decodeURIComponent(m[1]);
      for (const a of body.aliases) {
        if (m[2]) this.aliases.delete(`${name}|${a}`);
        else this.aliases.set(`${name}|${a}`, this.version(name, body.version)!.digest);
      }
      return {};
    }
    m = /^\/api\/v1\/objects\/([^/]+)\/versions\/([^/]+)\/tags(\/remove)?$/.exec(path);
    if (m) {
      const name = decodeURIComponent(m[1]);
      const key = `${name}|${this.version(name, decodeURIComponent(m[2]))!.digest}`;
      const cur = this.tags.get(key) ?? [];
      if (m[3]) this.tags.set(key, cur.filter((t) => !body.tags.includes(t)));
      else {
        for (const t of body.tags) if (!cur.includes(t)) cur.push(t);
        this.tags.set(key, cur);
      }
      return {};
    }
    switch (path) {
      case '/api/v1/objects/tags': {
        const items = [...new Set([...this.tags.values()].flat())].sort();
        return { items, total_count: items.length };
      }
      case '/api/v1/objects/aliases': {
        const items = [...new Set([...this.aliases.keys()].map((k) => k.split('|')[1]))].sort();
        return { items, total_count: items.length };
      }
      case '/api/v1/objects/delete': {
        const versions = this.objs.get(body.name) ?? [];
        const ds: string[] | undefined = body.digests;
        const keep = ds ? versions.filter((v) => !ds.includes(v.digest) && !ds.includes(`v${v.version}`)) : [];
        this.objs.set(body.name, keep);
        return { num_deleted: versions.length - keep.length };
      }
      case '/api/v1/objects/save': {
        if (!/^[A-Za-z0-9_.-]{1,128}$/.test(body.name)) throw new Error(`invalid name ${body.name}`);
        const d = digest(body.value);
        const versions = this.objs.get(body.name) ?? [];
        this.objs.set(body.name, versions);
        let v = versions.find((x) => x.digest === d);
        if (!v) {
          const kind = kindOf(body.value);
          v = { name: body.name, digest: d, value: body.value, version: versions.length, kind, class: kind === 'function' ? null : (body.value?._class_name ?? null) };
          versions.push(v);
        }
        return { name: body.name, digest: d, version: v.version, kind: v.kind, ref: this.object(body.project_id, v).ref };
      }
      case '/api/v1/objects/query': {
        const f = body.filter ?? {};
        const items: any[] = [];
        for (const [name, versions] of this.objs) {
          if (f.names && !f.names.includes(name)) continue;
          for (const v of f.latest_only ? versions.slice(-1) : versions) if (!f.kinds || f.kinds.includes(v.kind)) items.push(this.object(body.project_id, v));
        }
        return { items, total_count: items.length };
      }
      case '/api/v1/embeddings': {
        if (!this.embeddingsEnabled) throw new Error('no route');
        return { response: { model: body.inputs.model, data: body.inputs.input.map((t: string) => ({ embedding: [t.length, 1] })) } };
      }
      case '/remote/score':
        return { score: 0.9, received: body };
      case '/api/v1/spans/batch':
        return {
          items: body.items.map((it: any) => {
            const keys = Object.keys(it);
            if (keys.length !== 1) throw new Error('expected exactly one of start, end or span');
            const r = it[keys[0]];
            this.calls.set(r.id, { ...(this.calls.get(r.id) ?? {}), ...r });
            return keys[0] === 'end' ? { id: r.id } : { id: r.id, trace_id: r.trace_id };
          }),
        };
      case '/api/v1/tables/create': {
        const rows = body.rows;
        const rds = rows.map((r: unknown) => digest(r));
        const td = tableDigest(rds);
        this.tables.set(td, rows.map((r: unknown, i: number) => ({ digest: rds[i], value: r })));
        return { digest: td, row_digests: rds };
      }
      case '/api/v1/tables/update': {
        const rows = [...this.tables.get(body.digest)!];
        const added: string[] = [];
        for (const u of body.updates) {
          rows.push({ digest: digest(u.append.row), value: u.append.row });
          added.push(digest(u.append.row));
        }
        const td = tableDigest(rows.map((r) => r.digest));
        this.tables.set(td, rows);
        return { digest: td, updated_row_digests: added };
      }
      case '/api/v1/tables/query': {
        const rows = this.tables.get(body.digest)!;
        const off = body.offset ?? 0;
        return { items: rows.slice(off, off + (body.limit ?? rows.length)).map((r, i) => ({ ...r, index: off + i })), total_count: rows.length };
      }
      case '/api/v1/files/upload': {
        const boundary = /boundary=(.*)$/.exec(ctype)![1];
        const parts = raw.toString('latin1').split(`--${boundary}`);
        const filePart = parts.find((p) => p.includes('filename='))!;
        const content = Buffer.from(filePart.slice(filePart.indexOf('\r\n\r\n') + 4, -2), 'latin1');
        const d = fileDigest(content);
        this.files.set(d, content);
        return { digest: d, size_bytes: content.length };
      }
      case '/api/v1/spans/query':
      case '/api/v1/spans/stream': {
        const f = body.filter ?? {};
        const known = new Set(['names', 'ids', 'trace_ids', 'parent_ids', 'session_ids', 'turn_ids', 'user_ids', 'run_ids', 'input_refs', 'output_refs', 'trace_roots_only']);
        for (const k of Object.keys(f)) if (!known.has(k)) throw new Error(`unknown filter field ${k}`);
        let calls = [...this.calls.values()];
        if (f.names) calls = calls.filter((c) => f.names.includes(c.name) || f.names.includes(this.opBase(c)));
        if (f.parent_ids) calls = calls.filter((c) => f.parent_ids.includes(c.parent_id));
        if (f.session_ids) calls = calls.filter((c) => f.session_ids.includes(c.session_id));
        calls.sort((a, b) => (a.started_at < b.started_at ? -1 : 1));
        const total = calls.length;
        if (path === '/api/v1/spans/query') return { items: body.limit === 0 ? [] : calls.slice(0, body.limit ?? 100), total_count: total };
        if (body.limit) calls = calls.slice(0, body.limit);
        return calls.map((c) => JSON.stringify(c)).join('\n') + '\n';
      }
      case '/api/v1/spans/update':
        this.calls.get(body.id).display_name = body.display_name;
        return {};
      case '/api/v1/spans/delete':
        for (const id of body.ids) this.calls.get(id).deleted_at = 'now';
        return {};
      case '/api/v1/feedback/create': {
        const fb = { ...body, id: `fb${this.feedback.length}` };
        this.feedback.push(fb);
        return { id: fb.id };
      }
      case '/api/v1/feedback/query': {
        const val = body.query.$expr.$eq[1].$literal;
        const items = this.feedback.filter((f) => f.ref === val);
        return { items, total_count: items.length };
      }
      case '/api/v1/feedback/delete':
        this.feedback = this.feedback.filter((f) => !(body.ids ?? []).includes(f.id));
        return {};
      case '/api/v1/prices/create':
        return { items: Object.keys(body.prices).map((k, i) => ({ id: `c${i}`, llm_id: k })) };
      case '/api/v1/completions': {
        const inputs = body.inputs;
        const last = inputs.messages.filter((m: any) => m.role === 'user').at(-1).content;
        const content = inputs.response_format?.type === 'json_object' ? JSON.stringify({ correct: true, echo: last }) : last;
        return {
          response: {
            id: 'x',
            model: inputs.model,
            choices: [{ index: 0, message: { role: 'assistant', content }, finish_reason: 'stop' }],
            usage: { prompt_tokens: 7, completion_tokens: 3, total_tokens: 10 },
          },
          span_id: null,
        };
      }
      case '/api/v1/refs/read':
        return { items: body.refs.map(() => null) };
    }
    throw new Error(`unhandled ${path}`);
  }
}
