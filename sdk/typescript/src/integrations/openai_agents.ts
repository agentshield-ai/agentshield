/**
 * OpenAI Agents SDK for JavaScript (`@openai/agents`): a tracing processor.
 *
 * ```ts
 * import { addTraceProcessor } from '@openai/agents';
 * addTraceProcessor(new weft.WeftTracingProcessor());
 * // or: weft.instrumentOpenAIAgents(await import('@openai/agents'))
 * ```
 *
 * Traces become root calls (agent kind); agent, function (tool),
 * generation/response (LLM, with usage), handoff, guardrail, MCP list-tools
 * and custom spans become nested calls with the matching span kind and
 * `gen_ai.*` attributes, keeping the SDK's start and end times.
 */
import { asError, RunTracker } from './runs.js';
import { pickOpSettings, type IntegrationOpSettings } from './settings.js';

const INTEGRATION = 'openai_agents';

function parseMaybe(v: unknown): unknown {
  if (typeof v !== 'string') return v;
  try {
    return JSON.parse(v);
  } catch {
    return v;
  }
}

function asInputs(v: unknown): Record<string, unknown> {
  const p = parseMaybe(v);
  return p && typeof p === 'object' && !Array.isArray(p) ? { ...(p as object) } : p === undefined || p === '' ? {} : { input: p };
}

function spanInfo(d: any): { op: string; kind: string; inputs: Record<string, unknown>; attrs: Record<string, unknown>; display?: string } {
  const t = d?.type ?? 'custom';
  const name = d?.name;
  switch (t) {
    case 'agent':
      return {
        op: `agent.${name || 'agent'}`,
        kind: 'agent',
        inputs: { tools: d.tools, handoffs: d.handoffs, output_type: d.output_type },
        attrs: { 'gen_ai.operation.name': 'invoke_agent', 'gen_ai.agent.name': name },
        display: name,
      };
    case 'function': {
      const attrs: Record<string, unknown> = { 'gen_ai.operation.name': 'execute_tool', 'gen_ai.tool.name': name };
      if (d.mcp_data) attrs['gen_ai.tool.type'] = 'mcp';
      return { op: `tool.${name || 'function'}`, kind: 'tool', inputs: asInputs(d.input), attrs, display: name };
    }
    case 'generation': {
      const attrs: Record<string, unknown> = { 'gen_ai.operation.name': 'chat' };
      if (d.model) attrs['gen_ai.request.model'] = d.model;
      return { op: 'openai_agents.generation', kind: 'llm', inputs: { messages: d.input, model: d.model, model_config: d.model_config }, attrs };
    }
    case 'response':
      return { op: 'openai_agents.response', kind: 'llm', inputs: { input: d._input }, attrs: { 'gen_ai.operation.name': 'chat', 'gen_ai.provider.name': 'openai' } };
    case 'handoff':
      return { op: 'openai_agents.handoff', kind: 'agent', inputs: { from_agent: d.from_agent, to_agent: d.to_agent }, attrs: {}, display: `handoff → ${d.to_agent ?? '?'}` };
    case 'guardrail':
      return { op: `guardrail.${name || 'guardrail'}`, kind: 'guardrail', inputs: {}, attrs: {} };
    case 'mcp_tools':
      return { op: 'openai_agents.mcp_list_tools', kind: 'tool', inputs: { server: d.server }, attrs: {} };
    case 'turn':
      return { op: 'openai_agents.turn', kind: 'chain', inputs: { turn: d.turn, agent_name: d.agent_name }, attrs: {} };
    case 'task':
      return { op: `openai_agents.task.${name || 'task'}`, kind: 'chain', inputs: {}, attrs: {} };
    default:
      return { op: `openai_agents.${name || t}`, kind: t, inputs: { data: d?.data }, attrs: {} };
  }
}

function spanOutput(d: any): { out: unknown; usage: any } {
  const t = d?.type ?? 'custom';
  if (t === 'function') return { out: parseMaybe(d.output), usage: null };
  if (t === 'generation') {
    const u = d.usage ?? {};
    return { out: d.output, usage: { model: d.model, promptTokens: u.input_tokens ?? u.prompt_tokens, completionTokens: u.output_tokens ?? u.completion_tokens } };
  }
  if (t === 'response') {
    const r = d._response;
    const u = r?.usage;
    return {
      out: r ?? { response_id: d.response_id },
      usage: u ? { model: r?.model, promptTokens: u.input_tokens, completionTokens: u.output_tokens, totalTokens: u.total_tokens } : null,
    };
  }
  if (t === 'guardrail') return { out: { triggered: d.triggered }, usage: null };
  if (t === 'mcp_tools') return { out: { tools: d.result }, usage: null };
  if (t === 'handoff') return { out: { to_agent: d.to_agent }, usage: null };
  if (t === 'agent' || t === 'turn' || t === 'task') return { out: null, usage: null };
  return { out: d?.data ?? null, usage: null };
}

/** An `@openai/agents` TracingProcessor that records traces and spans as Weft calls. */
export class WeftTracingProcessor {
  private tracker: RunTracker;

  constructor(opts: IntegrationOpSettings & { opSettings?: IntegrationOpSettings } = {}) {
    this.tracker = new RunTracker(INTEGRATION, pickOpSettings(opts));
  }

  start(): void {}

  async onTraceStart(trace: any): Promise<void> {
    try {
      const name = trace?.name || 'agent_run';
      const attrs: Record<string, unknown> = { openai_agents: { trace_id: trace?.traceId, group_id: trace?.groupId ?? null } };
      if (trace?.groupId) attrs['gen_ai.conversation.id'] = trace.groupId;
      if (trace?.metadata && typeof trace.metadata === 'object') (attrs.openai_agents as any).metadata = trace.metadata;
      this.tracker.start(`trace:${trace?.traceId}`, 'openai_agents.trace', { name }, { kind: 'agent', displayName: name, attributes: attrs });
    } catch {
      /* never break the agent */
    }
  }

  async onTraceEnd(trace: any): Promise<void> {
    this.tracker.end(`trace:${trace?.traceId}`);
  }

  async onSpanStart(span: any): Promise<void> {
    try {
      const d = span?.spanData;
      const info = spanInfo(d);
      const parent = span?.parentId ? `span:${span.parentId}` : `trace:${span?.traceId}`;
      this.tracker.start(`span:${span?.spanId}`, info.op, info.inputs, {
        parentId: parent,
        kind: info.kind,
        displayName: info.display,
        attributes: info.attrs,
        startedAt: span?.startedAt ?? undefined,
      });
    } catch {
      /* never break the agent */
    }
  }

  async onSpanEnd(span: any): Promise<void> {
    try {
      const { out, usage } = spanOutput(span?.spanData);
      this.tracker.end(`span:${span?.spanId}`, out, asError(span?.error), usage, span?.endedAt ?? undefined);
    } catch {
      /* never break the agent */
    }
  }

  async shutdown(): Promise<void> {
    this.tracker.endAll();
    const { getClient } = await import('../global.js');
    await getClient()?.flush();
  }

  async forceFlush(): Promise<void> {
    const { getClient } = await import('../global.js');
    await getClient()?.flush();
  }
}

/**
 * Register a `WeftTracingProcessor` with the `@openai/agents` module (uses its
 * `addTraceProcessor`). Returns the processor.
 */
export function instrumentOpenAIAgents(agents: { addTraceProcessor?: (p: any) => void }, opts: IntegrationOpSettings & { opSettings?: IntegrationOpSettings } = {}): WeftTracingProcessor {
  if (typeof agents?.addTraceProcessor !== 'function') throw new TypeError('instrumentOpenAIAgents: pass the @openai/agents module (it exports addTraceProcessor)');
  const p = new WeftTracingProcessor(opts);
  agents.addTraceProcessor(p);
  return p;
}
