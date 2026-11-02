import { createServer, type IncomingMessage, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import { b64digest, digest } from '../src/canonical.js';

export interface Recorded {
  method: string;
  path: string;
  body: any;
  headers: IncomingMessage['headers'];
}

/** In-process fake Weft server recording requests. */
export class FakeWeft {
  requests: Recorded[] = [];
  calls = new Map<string, any>();
  objs = new Map<string, any[]>();
  tables = new Map<string, Array<{ digest: string; val: any }>>();
  files = new Map<string, Buffer>();
  feedback: any[] = [];
  failPaths = new Map<string, number>();
  url = '';
  private server: Server;

  constructor() {
    this.server = createServer(async (req, res) => {
      const chunks: Buffer[] = [];
      for await (const c of req) chunks.push(c as Buffer);
      const raw = Buffer.concat(chunks);
      const ctype = req.headers['content-type'] ?? '';
      const body = ctype.startsWith('application/json') && raw.length ? JSON.parse(raw.toString()) : null;
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
        if (req.method === 'GET' && path === '/auth/whoami') return send(200, { user_id: 'anonymous', auth_required: false });
        if (req.method === 'GET' && path === '/server_info') return send(200, { version: 'fake', auth_required: false, otel_endpoint: '/otel/v1/traces' });
        const out = this.handle(path, body, raw, ctype);
        if (typeof out === 'string') return send(200, out, 'application/jsonl');
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

  opBase(c: any): string {
    const op: string = c.op_name ?? '';
    return op.startsWith('weft:///') ? op.split('/').pop()!.replace(/:[^:]*$/, '') : op;
  }

  callsByOp(name: string): any[] {
    return [...this.calls.values()].filter((c) => this.opBase(c) === name).sort((a, b) => (a.started_at < b.started_at ? -1 : a.started_at > b.started_at ? 1 : a.id < b.id ? -1 : 1));
  }

  children(id: string): any[] {
    return [...this.calls.values()].filter((c) => c.parent_id === id).sort((a, b) => (a.id < b.id ? -1 : 1));
  }

  private handle(path: string, body: any, raw: Buffer, ctype: string): unknown {
    switch (path) {
      case '/call/upsert_batch':
        return {
          res: body.batch.map((it: any) => {
            const r = it.req[it.mode];
            this.calls.set(r.id, { ...(this.calls.get(r.id) ?? {}), ...r });
            return it.mode === 'start' ? { id: r.id, trace_id: r.trace_id } : {};
          }),
        };
      case '/obj/create': {
        const o = body.obj;
        const d = digest(o.val);
        const versions = this.objs.get(o.object_id) ?? [];
        this.objs.set(o.object_id, versions);
        const existing = versions.find((v) => v.digest === d);
        if (existing) return { digest: d, object_id: o.object_id, version_index: existing.version_index };
        versions.push({ digest: d, val: o.val, version_index: versions.length, kind: o.val._type === 'Op' ? 'op' : 'object' });
        return { digest: d, object_id: o.object_id, version_index: versions.length - 1 };
      }
      case '/obj/read': {
        const versions = this.objs.get(body.object_id)!;
        const dg: string = body.digest;
        const v = dg === 'latest' ? versions.at(-1) : /^v\d+$/.test(dg) ? versions[Number(dg.slice(1))] : versions.find((x) => x.digest === dg);
        if (!v) throw new Error('not found');
        return { obj: { ...v, project_id: body.project_id, object_id: body.object_id } };
      }
      case '/table/create': {
        const rows = body.table.rows;
        const rds = rows.map((r: unknown) => digest(r));
        const td = digest(rds);
        this.tables.set(td, rows.map((r: unknown, i: number) => ({ digest: rds[i], val: r })));
        return { digest: td, row_digests: rds };
      }
      case '/table/update': {
        const rows = [...this.tables.get(body.base_digest)!];
        const added: string[] = [];
        for (const u of body.updates) {
          rows.push({ digest: digest(u.append.row), val: u.append.row });
          added.push(digest(u.append.row));
        }
        const td = digest(rows.map((r) => r.digest));
        this.tables.set(td, rows);
        return { digest: td, updated_row_digests: added };
      }
      case '/table/query': {
        const rows = this.tables.get(body.digest)!;
        const off = body.offset ?? 0;
        return { rows: rows.slice(off, off + (body.limit ?? rows.length)) };
      }
      case '/file/create': {
        const boundary = /boundary=(.*)$/.exec(ctype)![1];
        const parts = raw.toString('latin1').split(`--${boundary}`);
        const filePart = parts.find((p) => p.includes('filename='))!;
        const content = Buffer.from(filePart.slice(filePart.indexOf('\r\n\r\n') + 4, -2), 'latin1');
        const d = b64digest(content);
        this.files.set(d, content);
        return { digest: d };
      }
      case '/calls/stream_query': {
        const f = body.filter ?? {};
        let calls = [...this.calls.values()];
        if (f.op_names) calls = calls.filter((c) => f.op_names.includes(c.op_name) || f.op_names.includes(this.opBase(c)));
        if (f.parent_ids) calls = calls.filter((c) => f.parent_ids.includes(c.parent_id));
        calls.sort((a, b) => (a.started_at < b.started_at ? -1 : 1));
        if (body.limit) calls = calls.slice(0, body.limit);
        return calls.map((c) => JSON.stringify(c)).join('\n') + '\n';
      }
      case '/call/read':
        return { call: this.calls.get(body.id) ?? null };
      case '/call/update':
        this.calls.get(body.call_id).display_name = body.display_name;
        return {};
      case '/calls/delete':
        for (const id of body.call_ids) this.calls.get(id).deleted_at = 'now';
        return {};
      case '/feedback/create': {
        const fb = { ...body, id: `fb${this.feedback.length}` };
        this.feedback.push(fb);
        return { id: fb.id };
      }
      case '/feedback/query': {
        const val = body.query.$expr.$eq[1].$literal;
        return { result: this.feedback.filter((f) => f.weave_ref === val) };
      }
      case '/cost/create':
        return { ids: Object.keys(body.costs).map((k, i) => [`c${i}`, k]) };
      case '/completions/create': {
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
        };
      }
      case '/refs/read_batch':
        return { vals: body.refs.map(() => null) };
    }
    throw new Error(`unhandled ${path}`);
  }
}
