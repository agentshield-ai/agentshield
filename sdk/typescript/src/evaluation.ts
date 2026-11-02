import { tracingClient } from './global.js';
import { Dataset, WeftObject } from './objects.js';
import { isOp, op, type Op } from './op.js';

type AnyFn = (...args: any[]) => any;

/** Summarise scores: booleans → {true_count, true_fraction}, numbers → {mean}, objects recursively. */
export function autoSummarize(values: unknown[]): Record<string, unknown> | null {
  const vals = values.filter((v) => v !== null && v !== undefined);
  if (!vals.length) return null;
  if (vals.every((v) => typeof v === 'boolean')) {
    const tc = vals.filter(Boolean).length;
    return { true_count: tc, true_fraction: tc / vals.length };
  }
  if (vals.every((v) => typeof v === 'number' && Number.isFinite(v))) {
    return { mean: (vals as number[]).reduce((a, b) => a + b, 0) / vals.length };
  }
  if (vals.every((v) => typeof v === 'object' && !Array.isArray(v))) {
    const keys: string[] = [];
    for (const v of vals) for (const k of Object.keys(v as object)) if (!keys.includes(k)) keys.push(k);
    const out: Record<string, unknown> = {};
    for (const k of keys) {
      const s = autoSummarize(vals.map((v) => (v as Record<string, unknown>)[k]));
      if (s !== null) out[k] = s;
    }
    return Object.keys(out).length ? out : null;
  }
  return null;
}

export interface EvalRowResult {
  output: unknown;
  scores: Record<string, unknown>;
  model_latency: number;
}

/** A model: an op called as `model({datasetRow})`. */
export type ModelFn = AnyFn;
/** A scorer: an op called as `scorer({modelOutput, datasetRow})`. */
export type ScorerFn = AnyFn;

const predictAndScore = op(
  async function predict_and_score(self: Evaluation, model: Op<AnyFn>, example: Record<string, unknown>): Promise<EvalRowResult> {
    const row = { ...example };
    const modelInput = self.preprocessModelInput ? self.preprocessModelInput({ ...row }) : row;
    const start = performance.now();
    let output: unknown = null;
    try {
      output = await model({ datasetRow: modelInput });
    } catch {
      output = null; // recorded on the model's own call
    }
    const latency = (performance.now() - start) / 1000;
    const scores: Record<string, unknown> = {};
    for (const scorer of self.scorers) {
      const name = scorer.__weftOp.name;
      try {
        scores[name] = await scorer({ modelOutput: output, datasetRow: row });
      } catch {
        scores[name] = null;
      }
    }
    return { output, scores, model_latency: latency };
  },
  { name: 'Evaluation.predict_and_score' },
);

const summarize = op(
  function summarize(self: Evaluation, evalTable: EvalRowResult[]): Record<string, unknown> {
    const summary: Record<string, unknown> = {};
    for (const scorer of self.scorers) {
      const name = scorer.__weftOp.name;
      const s = autoSummarize(evalTable.map((r) => r.scores[name]));
      if (s !== null) summary[name] = s;
    }
    const lat = evalTable.map((r) => r.model_latency).filter((x) => typeof x === 'number');
    summary.model_latency = { mean: lat.length ? lat.reduce((a, b) => a + b, 0) / lat.length : 0 };
    return summary;
  },
  { name: 'Evaluation.summarize', postprocessInputs: (inputs) => ({ self: inputs.self }) },
);

const evaluateOp = op(
  async function evaluate(self: Evaluation, model: Op<AnyFn>): Promise<Record<string, unknown>> {
    const examples: Array<Record<string, unknown>> = self.dataset.rows.map((row, i) => {
      const r: Record<string, unknown> = { ...(row as Record<string, unknown>) };
      const ref = self.dataset.rowRef(i);
      if (ref) Object.defineProperty(r, '__weftRef', { value: ref, enumerable: false });
      return r;
    });
    const jobs: Array<Record<string, unknown>> = [];
    for (const ex of examples) for (let t = 0; t < self.trials; t++) jobs.push(ex);
    const results: EvalRowResult[] = new Array(jobs.length);
    let next = 0;
    const workers = Array.from({ length: Math.min(self.maxConcurrency, jobs.length) }, async () => {
      while (next < jobs.length) {
        const i = next++;
        results[i] = await predictAndScore(self, model, jobs[i]);
      }
    });
    await Promise.all(workers);
    return summarize(self, results);
  },
  { name: 'Evaluation.evaluate' },
);

export interface EvaluationOptions {
  name?: string;
  description?: string;
  dataset: Dataset | Array<Record<string, unknown>>;
  scorers: ScorerFn[];
  trials?: number;
  /** Transform each row before it is passed to the model as `datasetRow`. */
  preprocessModelInput?: (row: Record<string, unknown>) => Record<string, unknown>;
  /** Rows evaluated concurrently (default WEFT_PARALLELISM or 10). */
  maxConcurrency?: number;
}

/**
 * Evaluate a model op over a dataset with scorer ops. Records
 * Evaluation.evaluate → Evaluation.predict_and_score → model + scorers, and
 * Evaluation.summarize; resolves to the summary.
 */
export class Evaluation extends WeftObject {
  dataset: Dataset;
  scorers: Array<Op<AnyFn>>;
  trials: number;
  preprocessModelInput?: (row: Record<string, unknown>) => Record<string, unknown>;
  maxConcurrency: number;

  constructor(opts: EvaluationOptions) {
    super(opts);
    this.dataset = opts.dataset instanceof Dataset ? opts.dataset : new Dataset({ rows: opts.dataset });
    this.scorers = opts.scorers.map((s) => (isOp(s) ? s : op(s)));
    this.trials = Math.max(1, opts.trials ?? 1);
    this.preprocessModelInput = opts.preprocessModelInput;
    const env = Number(process.env.WEFT_PARALLELISM);
    this.maxConcurrency = Math.max(1, opts.maxConcurrency ?? (Number.isFinite(env) && env > 0 ? env : 10));
    for (const k of ['preprocessModelInput', 'maxConcurrency']) {
      Object.defineProperty(this, k, { enumerable: false, writable: true, value: (this as any)[k] });
    }
  }

  get className(): string {
    return 'Evaluation';
  }

  fields(): Record<string, unknown> {
    return { dataset: this.dataset, scorers: this.scorers, trials: this.trials };
  }

  async evaluate({ model }: { model: ModelFn }): Promise<Record<string, unknown>> {
    const m = isOp(model) ? model : op(model);
    const client = tracingClient();
    if (client) await client.publish(this, this.name ?? 'Evaluation');
    return evaluateOp(this, m);
  }
}
