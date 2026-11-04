/**
 * Per-integration settings, the TypeScript counterpart of the Python SDK's
 * `autopatch_settings`:
 *
 * ```ts
 * await weft.init('proj', { autopatchSettings: {
 *   openai: { traceSettings: { postprocessInputs: scrub, postprocessOutput: scrub } },
 *   langchain: { enabled: false },
 * } });
 * ```
 *
 * Each value is `{ enabled?, traceSettings? }` (or a boolean for `enabled`).
 * `traceSettings` applies to every span the integration records: `name` (span
 * name override), `spanDisplayName` (string or `(span) => string`),
 * `postprocessInputs(inputs) => inputs`, `postprocessOutput(output) => output`,
 * `kind`, `color` and `attributes` (merged into each span's attributes).
 *
 * Wrappers also take the same `traceSettings` per client
 * (`wrapOpenAI(client, { postprocessOutput })`); those run after the global ones.
 *
 * Environment (read at call time): `WEFT_AUTOPATCH=false` disables every
 * integration, `WEFT_DISABLED_INTEGRATIONS=openai,langchain` the listed ones,
 * `WEFT_AUTOPATCH_<NAME>=false` one. A disabled integration calls straight
 * through without recording anything.
 */
import type { Call } from '../call.js';
import { syntheticOp, type WeftClient } from '../client.js';
import { tracingClient } from '../global.js';
import { deprecated } from '../deprecation.js';
import type { OpMeta, OpOptions } from '../op.js';

export interface IntegrationTraceSettings {
  /** Replace the name of every span the integration records. */
  name?: string;
  /** Display name for each span, or a function of the span. */
  spanDisplayName?: string | ((span: Call) => string);
  /** @deprecated Use `spanDisplayName`. */
  callDisplayName?: string | ((call: Call) => string);
  postprocessInputs?: (inputs: Record<string, unknown>) => Record<string, unknown>;
  postprocessOutput?: (output: unknown) => unknown;
  kind?: string;
  color?: string;
  /** Merged into each span's attributes. */
  attributes?: Record<string, unknown>;
}

/** @deprecated Use `IntegrationTraceSettings`. */
export type IntegrationOpSettings = IntegrationTraceSettings;

export interface IntegrationSettings {
  /** Trace this integration (default true). */
  enabled?: boolean;
  /** Applied to every span the integration records. */
  traceSettings?: IntegrationTraceSettings;
  /** @deprecated Use `traceSettings`. */
  opSettings?: IntegrationTraceSettings;
}

/** Options every wrapper takes: trace settings for this client, flat or as `traceSettings`. */
export type WrapperTraceOptions = IntegrationTraceSettings & {
  traceSettings?: IntegrationTraceSettings;
  /** Replace the name of every span this wrapper records (same as `traceSettings.name`). */
  spanName?: string;
  /** @deprecated Use `traceSettings`. */
  opSettings?: IntegrationTraceSettings;
  /** @deprecated Use `spanName`. */
  opName?: string;
};

export type AutopatchSettings = Record<string, IntegrationSettings | boolean | undefined>;

/** Alternative spellings accepted as integration names. */
export const INTEGRATION_ALIASES: Record<string, string> = {
  'openai-agents': 'openai_agents',
  '@typesafe-ai/sdk': 'typesafe',
  'typesafe-sdk': 'typesafe',
  '@openai/agents-realtime': 'openai_realtime',
  '@openai/agents': 'openai_agents',
  agents: 'openai_agents',
  'claude-agent-sdk': 'claude_agent_sdk',
  '@anthropic-ai/claude-agent-sdk': 'claude_agent_sdk',
  'llama-index': 'llamaindex',
  llama_index: 'llamaindex',
  '@langchain/core': 'langchain',
  langchainjs: 'langchain',
  '@modelcontextprotocol/sdk': 'mcp',
  modelcontextprotocol: 'mcp',
  '@mistralai/mistralai': 'mistral',
  mistralai: 'mistral',
  'cohere-ai': 'cohere',
  'groq-sdk': 'groq',
  'together-ai': 'together',
  '@cerebras/cerebras_cloud_sdk': 'cerebras',
  '@aws-sdk/client-bedrock-runtime': 'bedrock',
  'bedrock-runtime': 'bedrock',
  '@google-cloud/vertexai': 'vertexai',
  vertex: 'vertexai',
  '@google/genai': 'google_genai',
  'google-genai': 'google_genai',
  genai: 'google_genai',
  ai: 'vercel_ai',
  'vercel-ai': 'vercel_ai',
  vercel: 'vercel_ai',
  '@anthropic-ai/sdk': 'anthropic',
};

export function canonicalIntegration(name: string): string {
  const n = String(name).trim().toLowerCase();
  return INTEGRATION_ALIASES[n] ?? n.replace(/-/g, '_');
}

const FALSE = new Set(['0', 'false', 'no', 'off', 'disabled']);
let settings = new Map<string, IntegrationSettings>();

function coerce(v: IntegrationSettings | boolean): IntegrationSettings {
  if (typeof v === 'boolean') return { enabled: v };
  if (!v || typeof v !== 'object') throw new TypeError('integration settings must be an object or a boolean');
  const allowed = new Set(['enabled', 'traceSettings', 'trace_settings', 'opSettings', 'op_settings']);
  for (const k of Object.keys(v)) if (!allowed.has(k)) throw new TypeError(`unknown integration setting "${k}"`);
  const anyV = v as any;
  const old = anyV.opSettings ?? anyV.op_settings;
  if (old !== undefined) deprecated('integration settings opSettings', 'traceSettings');
  const ts = anyV.traceSettings ?? anyV.trace_settings ?? old;
  const out: IntegrationSettings = { enabled: v.enabled, traceSettings: ts ? { ...ts } : undefined };
  Object.defineProperty(out, 'opSettings', {
    enumerable: false,
    get() {
      deprecated('IntegrationSettings.opSettings', 'IntegrationSettings.traceSettings');
      return out.traceSettings;
    },
  });
  return out;
}

/** Replace all integration settings (`undefined` resets to defaults). */
export function configureIntegrations(s?: AutopatchSettings | null): void {
  const next = new Map<string, IntegrationSettings>();
  for (const [k, v] of Object.entries(s ?? {})) if (v !== undefined && v !== null) next.set(canonicalIntegration(k), coerce(v));
  settings = next;
}

/** Set one integration's settings. */
export function updateIntegration(name: string, s: IntegrationSettings | boolean): void {
  settings.set(canonicalIntegration(name), coerce(s));
}

export function getIntegrationSettings(name: string): IntegrationSettings {
  return settings.get(canonicalIntegration(name)) ?? {};
}

function envDisabled(name: string): boolean {
  const all = process.env.WEFT_AUTOPATCH;
  if (all !== undefined && FALSE.has(all.trim().toLowerCase())) return true;
  const list = process.env.WEFT_DISABLED_INTEGRATIONS ?? process.env.WEFT_DISABLE_INTEGRATIONS ?? '';
  if (list) {
    const names = new Set(list.split(',').map((x) => x.trim()).filter(Boolean).map(canonicalIntegration));
    if (names.has(name) || names.has('all') || names.has('*')) return true;
  }
  const one = process.env[`WEFT_AUTOPATCH_${name.toUpperCase()}`];
  return one !== undefined && FALSE.has(one.trim().toLowerCase());
}

/** Whether an integration should record calls now (settings and environment). */
export function integrationEnabled(name: string | undefined): boolean {
  if (!name) return true;
  const n = canonicalIntegration(name);
  if (envDisabled(n)) return false;
  return settings.get(n)?.enabled !== false;
}

/** The client to record with for this integration, or null when tracing is off for it. */
export function integrationClient(name: string | undefined): WeftClient | null {
  const c = tracingClient();
  if (!c || !integrationEnabled(name)) return null;
  return c;
}

function compose<T>(a?: (v: T) => T, b?: (v: T) => T, c?: (v: T) => T): ((v: T) => T) | undefined {
  const fs = [a, b, c].filter(Boolean) as Array<(v: T) => T>;
  if (!fs.length) return undefined;
  if (fs.length === 1) return fs[0];
  return (v: T) => fs.reduce((acc, f) => f(acc), v);
}

export interface IntegrationOpDefaults {
  spanDisplayName?: string | ((span: Call) => string);
  postprocessInputs?: (inputs: Record<string, unknown>) => Record<string, unknown>;
  postprocessOutput?: (output: unknown) => unknown;
  color?: string;
}

/**
 * The op an integration records a call under, with the integration's global
 * settings and any per-wrapper `local` settings applied. The returned meta
 * shares its op ref cache with the plain synthetic op of the same name.
 */
export function integrationOp(
  integration: string | undefined,
  opName: string,
  kind?: string,
  defaults: IntegrationOpDefaults = {},
  local?: IntegrationTraceSettings,
): OpMeta {
  const g = integration ? getIntegrationSettings(integration).traceSettings ?? {} : {};
  const name = local?.name ?? g.name ?? opName;
  const k = local?.kind ?? g.kind ?? kind;
  const base = syntheticOp(name, k);
  const options: OpOptions = {
    ...base.options,
    kind: k,
    color: local?.color ?? g.color ?? defaults.color,
    spanDisplayName: displayNameOf(local) ?? displayNameOf(g) ?? defaults.spanDisplayName,
    postprocessInputs: compose(defaults.postprocessInputs, g.postprocessInputs, local?.postprocessInputs),
    postprocessOutput: compose(defaults.postprocessOutput, g.postprocessOutput, local?.postprocessOutput),
  };
  return { ...base, options };
}

function displayNameOf(s: IntegrationTraceSettings | undefined): IntegrationTraceSettings['spanDisplayName'] {
  if (!s) return undefined;
  if (s.spanDisplayName !== undefined) return s.spanDisplayName;
  if (s.callDisplayName !== undefined) deprecated('traceSettings callDisplayName', 'spanDisplayName');
  return s.callDisplayName;
}

function mergeAttrs(into: Record<string, any>, extra: Record<string, unknown> | undefined): void {
  for (const [k, v] of Object.entries(extra ?? {})) {
    if (v && typeof v === 'object' && !Array.isArray(v) && into[k] && typeof into[k] === 'object') into[k] = { ...into[k], ...(v as object) };
    else into[k] = v;
  }
}

/** Attributes stamped on every call of an integration, plus the settings' `attributes`. */
export function integrationAttributes(integration: string | undefined, extra?: Record<string, unknown>, local?: IntegrationTraceSettings): Record<string, unknown> {
  const out: Record<string, any> = integration ? { weft: { integration: canonicalIntegration(integration) } } : {};
  if (integration) mergeAttrs(out, getIntegrationSettings(integration).traceSettings?.attributes);
  mergeAttrs(out, local?.attributes);
  mergeAttrs(out, extra);
  return out;
}

/**
 * Split wrapper options into trace settings: the flat fields `IntegrationTraceSettings`
 * names (`spanName` for `name`), then `traceSettings`. The earlier `opName` and
 * `opSettings` are read too, with a warning; the new names win.
 */
export function pickTraceSettings(o: Record<string, any> | undefined): IntegrationTraceSettings | undefined {
  if (!o) return undefined;
  const out: IntegrationTraceSettings = {};
  let any = false;
  for (const k of ['spanDisplayName', 'callDisplayName', 'postprocessInputs', 'postprocessOutput', 'kind', 'color', 'attributes'] as const) {
    if (o[k] !== undefined) {
      (out as any)[k] = o[k];
      any = true;
    }
  }
  if (o.opName !== undefined) deprecated('wrapper option opName', 'spanName');
  const name = o.spanName ?? o.opName;
  if (name !== undefined) (out.name = name), (any = true);
  if (o.opSettings) {
    deprecated('wrapper option opSettings', 'traceSettings');
    Object.assign(out, o.opSettings), (any = true);
  }
  if (o.traceSettings) Object.assign(out, o.traceSettings), (any = true);
  return any ? out : undefined;
}
