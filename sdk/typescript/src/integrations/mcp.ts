/**
 * Model Context Protocol (`@modelcontextprotocol/sdk`) integration.
 *
 * Client side: `weft.wrapMCPClient(client)` traces `callTool` (tool kind,
 * display name `MCP tool: <name>`), `readResource`, `getPrompt` and the
 * `list*` methods of a `Client` instance (or pass the `Client` class to
 * `instrumentMCP({ Client })` to patch every instance).
 *
 * Server side: `weft.wrapMCPServer(server)` (an `McpServer` or a low-level
 * `Server`) traces each `tools/call`, `resources/read` and `prompts/get`
 * request it handles; ops and wrapped LLM clients used by the tool function
 * nest under the request's call.
 *
 * Set `WEFT_MCP_TRACE_LIST_OPERATIONS=false` to skip the list methods.
 */
import type { Call } from '../call.js';
import { patchWith, withCall } from './common.js';
import { integrationAttributes, integrationClient, integrationOp, pickOpSettings, type IntegrationOpSettings } from './settings.js';

const INTEGRATION = 'mcp';

function listOps(): boolean {
  return !['0', 'false', 'no', 'off'].includes((process.env.WEFT_MCP_TRACE_LIST_OPERATIONS ?? 'true').trim().toLowerCase());
}

interface MethodSpec {
  op: string;
  kind?: string;
  display?: (params: any) => string;
  attrs?: (params: any) => Record<string, unknown>;
}

const toolDisplay = (p: any) => (p?.name ? `MCP tool: ${p.name}` : 'MCP tool');
const resourceDisplay = (p: any) => (p?.uri ? `MCP resource: ${p.uri}` : 'MCP resource');
const promptDisplay = (p: any) => (p?.name ? `MCP prompt: ${p.name}` : 'MCP prompt');
const toolAttrs = (p: any) => ({ 'gen_ai.operation.name': 'execute_tool', 'gen_ai.tool.name': p?.name, 'gen_ai.tool.type': 'mcp' });

const CLIENT_METHODS: Record<string, MethodSpec> = {
  callTool: { op: 'mcp.client.call_tool', kind: 'tool', display: toolDisplay, attrs: toolAttrs },
  readResource: { op: 'mcp.client.read_resource', kind: 'retriever', display: resourceDisplay },
  getPrompt: { op: 'mcp.client.get_prompt', display: promptDisplay },
  listTools: { op: 'mcp.client.list_tools' },
  listResources: { op: 'mcp.client.list_resources' },
  listResourceTemplates: { op: 'mcp.client.list_resource_templates' },
  listPrompts: { op: 'mcp.client.list_prompts' },
};

const SERVER_METHODS: Record<string, MethodSpec> = {
  'tools/call': { op: 'mcp.server.call_tool', kind: 'tool', display: toolDisplay, attrs: toolAttrs },
  'resources/read': { op: 'mcp.server.read_resource', kind: 'retriever', display: resourceDisplay },
  'prompts/get': { op: 'mcp.server.get_prompt', display: promptDisplay },
  'tools/list': { op: 'mcp.server.list_tools' },
  'resources/list': { op: 'mcp.server.list_resources' },
  'prompts/list': { op: 'mcp.server.list_prompts' },
};

function isList(op: string): boolean {
  return op.includes('.list_');
}

function plainParams(p: any): Record<string, unknown> {
  if (!p || typeof p !== 'object') return p === undefined ? {} : { params: p };
  const { _meta, ...rest } = p;
  return rest;
}

function mcpError(res: any): Error | undefined {
  if (res && typeof res === 'object' && res.isError) {
    const text = Array.isArray(res.content) ? res.content.map((c: any) => c?.text ?? '').join(' ').trim() : '';
    return new Error(text || 'MCP tool returned isError');
  }
  return undefined;
}

function traced(spec: MethodSpec, orig: (...a: any[]) => any, local: IntegrationOpSettings | undefined, serverInfo: () => Record<string, unknown>) {
  return function (this: unknown, params: any, ...rest: any[]) {
    const client = integrationClient(INTEGRATION);
    if (!client || (isList(spec.op) && !listOps())) return orig.call(this, params, ...rest);
    const p = spec.op.startsWith('mcp.server.') ? params?.params : params;
    const meta = integrationOp(INTEGRATION, spec.op, spec.kind, { spanDisplayName: spec.display ? spec.display(p) : undefined }, local);
    let call: Call;
    try {
      call = client.createSpan(meta, plainParams(p), { attributes: integrationAttributes(INTEGRATION, { ...(spec.attrs?.(p) ?? {}), ...serverInfo() }, local) });
    } catch {
      return orig.call(this, params, ...rest);
    }
    let res: any;
    try {
      res = withCall(call, () => orig.call(this, params, ...rest));
    } catch (e) {
      client.finishSpan(call, undefined, e, meta);
      throw e;
    }
    return Promise.resolve(res).then(
      (r) => {
        client.finishSpan(call, r, mcpError(r), meta);
        return r;
      },
      (e) => {
        client.finishSpan(call, undefined, e, meta);
        throw e;
      },
    );
  };
}

function clientServerInfo(c: any): Record<string, unknown> {
  try {
    const v = c?.getServerVersion?.();
    return v?.name ? { mcp: { server: { name: v.name, version: v.version } } } : {};
  } catch {
    return {};
  }
}

/** Trace an MCP `Client` instance (`@modelcontextprotocol/sdk/client`). Returns the same client. */
export function wrapMCPClient<T>(client: T, opts: IntegrationOpSettings & { opSettings?: IntegrationOpSettings } = {}): T {
  const local = pickOpSettings(opts);
  for (const [name, spec] of Object.entries(CLIENT_METHODS)) {
    patchWith(client as any, name, (orig) => {
      const c = client as any;
      return traced(spec, orig, local, () => clientServerInfo(c));
    });
  }
  return client;
}

/**
 * Trace the requests an MCP server handles: pass an `McpServer` (from
 * `@modelcontextprotocol/sdk/server/mcp.js`) or a low-level `Server`. Handlers
 * registered later (e.g. the first `registerTool`) are traced too.
 */
export function wrapMCPServer<T>(server: T, opts: IntegrationOpSettings & { opSettings?: IntegrationOpSettings } = {}): T {
  const local = pickOpSettings(opts);
  const proto: any = (server as any)?.server ?? server;
  const handlers: Map<string, (...a: any[]) => any> | undefined = proto?._requestHandlers;
  if (!(handlers instanceof Map)) throw new TypeError('wrapMCPServer: expected an McpServer or a Server from @modelcontextprotocol/sdk');
  if ((handlers as any).__weftPatched) return server;
  const info = () => {
    try {
      const si = proto?._serverInfo;
      return si?.name ? { mcp: { server: { name: si.name, version: si.version } } } : {};
    } catch {
      return {};
    }
  };
  const wrap = (method: string, h: (...a: any[]) => any) => {
    const spec = SERVER_METHODS[method];
    if (!spec || typeof h !== 'function' || (h as any).__weftPatched) return h;
    const w = traced(spec, h, local, info);
    Object.defineProperty(w, '__weftPatched', { value: true });
    return w;
  };
  for (const [m, h] of handlers) handlers.set(m, wrap(m, h));
  const origSet = handlers.set.bind(handlers);
  handlers.set = (m: string, h: any) => origSet(m, wrap(m, h));
  Object.defineProperty(handlers, '__weftPatched', { value: true });
  return server;
}

/**
 * Patch MCP classes so every instance is traced: `instrumentMCP({ Client })`
 * patches `Client.prototype`; servers still need `wrapMCPServer(server)`.
 */
export function instrumentMCP(mod: { Client?: any }, opts: IntegrationOpSettings & { opSettings?: IntegrationOpSettings } = {}): boolean {
  const proto = mod?.Client?.prototype;
  if (!proto) return false;
  const local = pickOpSettings(opts);
  let any = false;
  for (const [name, spec] of Object.entries(CLIENT_METHODS)) {
    any =
      patchWith(proto, name, (orig) =>
        function (this: any, ...args: any[]) {
          const self = this;
          return traced(spec, orig, local, () => clientServerInfo(self)).apply(this, args as [any, ...any[]]);
        },
      ) || any;
  }
  return any;
}
