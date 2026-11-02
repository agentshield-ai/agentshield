import type { Call } from './call.js';
import type { WeftClient } from './client.js';
import { tracingClient } from './global.js';
import { Dataset, Model, WeftObject } from './objects.js';
import { isOp, op, type Op } from './op.js';
import { Scorer } from './scorers.js';
import { autoSummarizeValues } from './summarize.js';
import { digest } from './canonical.js';
import { isRefUri } from './refs.js';
import { toJson } from './serialize.js';

type AnyFn = (...args: any[]) => any;

/** Summarise scores: booleans → {true_count, true_fraction}, numbers → {mean}, objects recursively. */
export const autoSummarize = autoSummarizeValues;

export interface EvalRowResult {
  output: unknown;
  scores: Record<string, unknown>;
  model_latency: number | null;
}

/** A model: an op called as `model({datasetRow})`. */
export type ModelFn = AnyFn;
/** A scorer: a Scorer object, or an op called as `scorer({modelOutput, datasetRow})`. */
export type ScorerLike = Scorer | AnyFn;

export function scorerName(s: Scorer | Op<AnyFn>): string {
  return s instanceof Scorer ? s.scorerName : s.__weftOp.name;
}

async function runScorer(s: Scorer | Op<AnyFn>, modelOutput: unknown, row: Record<string, unknown>): Promise<unknown> {
  if (s instanceof Scorer) return s.scoreOp({ modelOutput, datasetRow: s.mapRow(row) });
  return s({ modelOutput, datasetRow: row });
}

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
      try {
        scores[scorerName(scorer)] = await runScorer(scorer, output, row);
      } catch {
        scores[scorerName(scorer)] = null;
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
      const name = scorerName(scorer);
      const vals = evalTable.map((r) => r.scores[name]);
      const s = scorer instanceof Scorer ? scorer.summarize(vals) : autoSummarizeValues(vals);
      if (s !== null) summary[name] = s;
    }
    const lat = evalTable.map((r) => r.model_latency).filter((x): x is number => typeof x === 'number');
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
  scorers: ScorerLike[];
  trials?: number;
  /** Transform each row before it is passed to the model as `datasetRow`. */
  preprocessModelInput?: (row: Record<string, unknown>) => Record<string, unknown>;
  /** Rows evaluated concurrently (default WEFT_PARALLELISM or 10). */
  maxConcurrency?: number;
}

/**
 * Evaluate a model op over a dataset with scorers (Scorer objects or ops).
 * Records Evaluation.evaluate → Evaluation.predict_and_score → model + scorers,
 * and Evaluation.summarize; resolves to the summary.
 */
export class Evaluation extends WeftObject {
  dataset: Dataset;
  scorers: Array<Scorer | Op<AnyFn>>;
  trials: number;
  preprocessModelInput?: (row: Record<string, unknown>) => Record<string, unknown>;
  maxConcurrency: number;

  constructor(opts: EvaluationOptions) {
    super(opts);
    this.dataset = opts.dataset instanceof Dataset ? opts.dataset : new Dataset({ rows: opts.dataset });
    this.scorers = opts.scorers.map((s) => (s instanceof Scorer || isOp(s) ? s : op(s)));
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

// ---------------------------------------------------------------------------
// imperative logger

/** Returned by `EvaluationLogger.logPrediction`; log scores, then `finish()`. */
export class ScoreLogger {
  readonly scores: Record<string, unknown> = {};
  private finished = false;

  constructor(
    private readonly ev: EvaluationLogger,
    private readonly call: Call | null,
    readonly inputs: Record<string, unknown>,
    readonly output: unknown,
    readonly modelLatency: number | null,
  ) {}

  logScore(scorer: string | Scorer, score: unknown): void {
    if (this.finished) throw new Error('cannot log a score after finish()');
    const name = typeof scorer === 'string' ? scorer : scorer.scorerName;
    const client = this.ev.client;
    if (client && this.call) {
      const c =
        typeof scorer === 'string'
          ? client.createCall(name, { output: this.output, ...this.inputs }, { parent: this.call })
          : client.createCall(scorer.scoreOp.__weftOp, { self: scorer, modelOutput: this.output, datasetRow: this.inputs }, { parent: this.call });
      client.finishCall(c, score);
    }
    this.scores[name] = score;
    this.ev.scorerNames.add(name);
  }

  finish(): void {
    if (this.finished) return;
    this.finished = true;
    if (this.ev.client && this.call) {
      this.ev.client.finishCall(this.call, { output: this.output, scores: { ...this.scores }, model_latency: this.modelLatency });
    }
  }
}

export interface EvaluationLoggerOptions {
  /** Model name, attributes (`{name, ...}`) or a Model object. */
  model?: string | Record<string, unknown> | Model;
  /** Dataset name, rows or a Dataset. */
  dataset?: string | Array<Record<string, unknown>> | Dataset;
  name?: string;
  description?: string;
  scorers?: ScorerLike[];
}

/**
 * Log an evaluation imperatively; produces the same calls as `Evaluation`:
 *
 *   const ev = new EvaluationLogger({ model: 'my-model', dataset: 'qa' });
 *   const pred = ev.logPrediction({ q: '2+2' }, '4');
 *   pred.logScore('correct', true);
 *   pred.finish();
 *   await ev.logSummary();
 *
 * Objects are published with locally computed digests, so nothing blocks.
 */
export class EvaluationLogger {
  readonly client: WeftClient | null;
  readonly model: Model;
  readonly dataset: Dataset;
  readonly evaluation: Evaluation;
  readonly scorerNames = new Set<string>();
  readonly call: Call | null = null;
  summary: Record<string, unknown> | null = null;
  private predictions: ScoreLogger[] = [];
  private finished = false;
  private rowIndex = new Map<string, number>();

  constructor(opts: EvaluationLoggerOptions = {}) {
    this.client = tracingClient();
    const m = opts.model;
    this.model =
      m instanceof Model
        ? m
        : typeof m === 'string' || m === undefined
          ? new Model({ name: m ?? 'model' })
          : (() => {
              const { name, ...attrs } = m as Record<string, unknown>;
              return new Model({ name: String(name ?? 'model'), attributes: attrs });
            })();
    const d = opts.dataset;
    if (typeof d === 'string' && isRefUri(d)) {
      throw new Error('EvaluationLogger: load the dataset first, e.g. `dataset: await weft.ref(uri).get()`');
    }
    this.dataset = d instanceof Dataset ? d : typeof d === 'string' || d === undefined ? new Dataset({ name: d ?? 'dataset', rows: [] }) : new Dataset({ rows: d });
    this.evaluation = new Evaluation({ name: opts.name ?? 'Evaluation', description: opts.description, dataset: this.dataset, scorers: opts.scorers ?? [] });
    if (this.client) {
      this.client.publishLocal(this.evaluation, this.evaluation.name);
      this.client.publishLocal(this.model);
      this.call = this.client.createCall(evaluateOp.__weftOp, { self: this.evaluation, model: this.model }, { displayName: opts.name });
      // link predictions whose inputs equal a dataset row to that row's ref
      this.dataset.rows.forEach((row, i) => {
        const key = digest(toJson(row));
        if (!this.rowIndex.has(key)) this.rowIndex.set(key, i);
      });
    }
  }

  private example(inputs: Record<string, unknown>): Record<string, unknown> {
    const i = this.rowIndex.get(digest(toJson(inputs)));
    const ref = i === undefined ? undefined : this.dataset.rowRef(i);
    const ex = { ...inputs };
    if (ref) Object.defineProperty(ex, '__weftRef', { value: ref, enumerable: false });
    return ex;
  }

  /** Log a prediction and its scores in one call (the prediction is finished). */
  logExample(inputs: Record<string, unknown>, output: unknown, scores: Record<string, unknown> = {}, modelLatency: number | null = null): ScoreLogger {
    const pred = this.logPrediction(inputs, output, modelLatency);
    for (const [name, value] of Object.entries(scores)) pred.logScore(name, value);
    pred.finish();
    return pred;
  }

  logPrediction(inputs: Record<string, unknown>, output: unknown, modelLatency: number | null = null): ScoreLogger {
    if (this.finished) throw new Error('evaluation already finished');
    let pas: Call | null = null;
    if (this.client && this.call) {
      pas = this.client.createCall(predictAndScore.__weftOp, { self: this.evaluation, model: this.model, example: this.example(inputs) }, { parent: this.call });
      const pc = this.client.createCall(`${this.model.name ?? 'Model'}.predict`, { self: this.model, ...inputs }, { parent: pas });
      this.client.finishCall(pc, output);
    }
    const sl = new ScoreLogger(this, pas, { ...inputs }, output, modelLatency);
    this.predictions.push(sl);
    return sl;
  }

  /** Finish open predictions, summarise scores (merged with `summary`) and end the evaluation. */
  async logSummary(summary?: Record<string, unknown>, autoSummarizeScores = true): Promise<Record<string, unknown>> {
    if (this.finished) return this.summary ?? {};
    for (const p of this.predictions) p.finish();
    const result: Record<string, unknown> = {};
    if (autoSummarizeScores) {
      for (const name of this.scorerNames) {
        const s = autoSummarizeValues(this.predictions.map((p) => p.scores[name]));
        if (s !== null) result[name] = s;
      }
      const lat = this.predictions.map((p) => p.modelLatency).filter((x): x is number => typeof x === 'number');
      if (lat.length) result.model_latency = { mean: lat.reduce((a, b) => a + b, 0) / lat.length };
    }
    Object.assign(result, summary ?? {});
    if (this.client && this.call) {
      const sc = this.client.createCall(summarize.__weftOp, { self: this.evaluation }, { parent: this.call });
      this.client.finishCall(sc, result);
      this.client.finishCall(this.call, result);
    }
    this.finished = true;
    this.summary = result;
    return result;
  }

  finish(): Promise<Record<string, unknown>> {
    return this.logSummary();
  }
}
