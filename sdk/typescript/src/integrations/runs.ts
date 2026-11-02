import type { Call } from '../call.js';
import { getCurrentCall } from '../context.js';
import { getClient } from '../global.js';
import type { OpMeta } from '../op.js';
import { addUsage } from '../usage.js';
import { integrationAttributes, integrationClient, integrationOp, type IntegrationOpDefaults, type IntegrationOpSettings } from './settings.js';

export interface RunUsage {
  model?: string | null;
  promptTokens?: number | null;
  completionTokens?: number | null;
  totalTokens?: number | null;
}

interface Run {
  call: Call;
  meta: OpMeta;
  children: number;
}

/**
 * Maps a framework's runs/spans (LangChain runs, OpenAI Agents spans,
 * LlamaIndex events) onto calls. A run whose parent is unknown nests under the
 * current call. LLM usage is only recorded on a run when nothing was traced
 * under it, so a framework LLM run wrapping a traced SDK call never counts
 * tokens twice.
 */
export class RunTracker {
  private runs = new Map<unknown, Run>();

  constructor(
    readonly integration: string,
    readonly local?: IntegrationOpSettings,
  ) {}

  start(
    runId: unknown,
    opName: string,
    inputs: Record<string, unknown>,
    opts: { parentId?: unknown; kind?: string; displayName?: string; attributes?: Record<string, unknown>; startedAt?: string | Date; defaults?: IntegrationOpDefaults } = {},
  ): Call | null {
    const client = integrationClient(this.integration);
    if (!client) return null;
    try {
      const parentRun = opts.parentId !== undefined && opts.parentId !== null ? this.runs.get(opts.parentId) : undefined;
      if (parentRun) parentRun.children++;
      const parent = parentRun ? parentRun.call : getCurrentCall();
      const meta = integrationOp(this.integration, opName, opts.kind, opts.defaults, this.local);
      const call = client.createCall(meta, inputs, {
        parent: parent ?? null,
        displayName: opts.displayName,
        attributes: integrationAttributes(this.integration, opts.attributes, this.local),
        startedAt: opts.startedAt,
      });
      this.runs.set(runId, { call, meta, children: 0 });
      return call;
    } catch (e) {
      console.warn(`weft: could not start ${opName}: ${(e as Error).message}`);
      return null;
    }
  }

  get(runId: unknown): Call | null {
    return this.runs.get(runId)?.call ?? null;
  }

  has(runId: unknown): boolean {
    return this.runs.has(runId);
  }

  /** Count a call traced elsewhere (e.g. a wrapped SDK) as a child of the run. */
  addChild(runId: unknown): void {
    const r = this.runs.get(runId);
    if (r) r.children++;
  }

  end(runId: unknown, output?: unknown, error?: unknown, usage?: RunUsage | null, endedAt?: string | Date): Call | null {
    const r = this.runs.get(runId);
    if (!r) return null;
    this.runs.delete(runId);
    try {
      if (usage && r.children === 0 && (usage.promptTokens || usage.completionTokens)) {
        addUsage(r.call, usage.model ?? undefined, usage.promptTokens ?? 0, usage.completionTokens ?? 0, usage.totalTokens ?? undefined);
      }
      const client = getClient();
      client?.finishCall(r.call, output, error ?? undefined, r.meta, endedAt);
    } catch (e) {
      console.warn(`weft: could not finish run: ${(e as Error).message}`);
    }
    return r.call;
  }

  /** End every open run (e.g. on shutdown). */
  endAll(error?: unknown): void {
    for (const id of [...this.runs.keys()]) this.end(id, undefined, error);
  }
}

/** Turn framework error values ({message}, strings, Errors) into Errors. */
export function asError(err: unknown): unknown {
  if (err == null) return undefined;
  if (err instanceof Error) return err;
  if (typeof err === 'object') {
    const m = (err as any).message ?? JSON.stringify(err);
    const d = (err as any).data;
    return new Error(d ? `${m}: ${typeof d === 'string' ? d : JSON.stringify(d)}` : String(m));
  }
  return new Error(String(err));
}
