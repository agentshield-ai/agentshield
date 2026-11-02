/**
 * Per-integration settings, the TypeScript counterpart of the Python SDK's
 * `autopatch_settings`:
 *
 * ```ts
 * await weft.init('proj', { autopatchSettings: {
 *   openai: { opSettings: { postprocessInputs: scrub, postprocessOutput: scrub } },
 *   langchain: { enabled: false },
 * } });
 * ```
 *
 * Each value is `{ enabled?, opSettings? }` (or a boolean for `enabled`).
 * `opSettings` applies to every call the integration records: `name` (op
 * name override), `callDisplayName` (string or `(call) => string`),
 * `postprocessInputs(inputs) => inputs`, `postprocessOutput(output) => output`,
 * `kind`, `color` and `attributes` (merged into each call's attributes).
 *
 * Wrappers also take the same `opSettings` per client
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
import type { OpMeta, OpOptions } from '../op.js';

export interface IntegrationOpSettings {
  /** Replace the op name of every call the integration records. */
  name?: string;
  callDisplayName?: string | ((call: Call) => string);
  postprocessInputs?: (inputs: Record<string, unknown>) => Record<string, unknown>;
  postprocessOutput?: (output: unknown) => unknown;
  kind?: string;
  color?: string;
  /** Merged into each call's attributes. */
  attributes?: Record<string, unknown>;
}

export interface IntegrationSettings {
  /** Trace this integration (default true). */
  enabled?: boolean;
  opSettings?: IntegrationOpSettings;
}

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
  const allowed = new Set(['enabled', 'opSettings', 'op_settings']);
  for (const k of Object.keys(v)) if (!allowed.has(k)) throw new TypeError(`unknown integration setting "${k}"`);
  const ops = (v as any).opSettings ?? (v as any).op_settings;
  return { enabled: v.enabled, opSettings: ops ? { ...ops } : undefined };
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
  callDisplayName?: string | ((call: Call) => string);
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
  local?: IntegrationOpSettings,
): OpMeta {
  const g = integration ? getIntegrationSettings(integration).opSettings ?? {} : {};
  const name = local?.name ?? g.name ?? opName;
  const k = local?.kind ?? g.kind ?? kind;
  const base = syntheticOp(name, k);
  const options: OpOptions = {
    ...base.options,
    kind: k,
    color: local?.color ?? g.color ?? defaults.color,
    callDisplayName: local?.callDisplayName ?? g.callDisplayName ?? defaults.callDisplayName,
    postprocessInputs: compose(defaults.postprocessInputs, g.postprocessInputs, local?.postprocessInputs),
    postprocessOutput: compose(defaults.postprocessOutput, g.postprocessOutput, local?.postprocessOutput),
  };
  return { ...base, options };
}

function mergeAttrs(into: Record<string, any>, extra: Record<string, unknown> | undefined): void {
  for (const [k, v] of Object.entries(extra ?? {})) {
    if (v && typeof v === 'object' && !Array.isArray(v) && into[k] && typeof into[k] === 'object') into[k] = { ...into[k], ...(v as object) };
    else into[k] = v;
  }
}

/** Attributes stamped on every call of an integration, plus the settings' `attributes`. */
export function integrationAttributes(integration: string | undefined, extra?: Record<string, unknown>, local?: IntegrationOpSettings): Record<string, unknown> {
  const out: Record<string, any> = integration ? { weft: { integration: canonicalIntegration(integration) } } : {};
  if (integration) mergeAttrs(out, getIntegrationSettings(integration).opSettings?.attributes);
  mergeAttrs(out, local?.attributes);
  mergeAttrs(out, extra);
  return out;
}

/** Split wrapper options into op settings (anything `IntegrationOpSettings` names). */
export function pickOpSettings(o: Record<string, any> | undefined): IntegrationOpSettings | undefined {
  if (!o) return undefined;
  const out: IntegrationOpSettings = {};
  let any = false;
  for (const k of ['callDisplayName', 'postprocessInputs', 'postprocessOutput', 'kind', 'color', 'attributes', 'opName'] as const) {
    if (o[k] !== undefined) {
      (out as any)[k === 'opName' ? 'name' : k] = o[k];
      any = true;
    }
  }
  if (o.opSettings) Object.assign(out, o.opSettings), (any = true);
  return any ? out : undefined;
}
