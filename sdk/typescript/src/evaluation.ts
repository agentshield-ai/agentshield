import { Call as CallClass, type Call } from './call.js';
import { currentContext, runWith, type WeftContext } from './context.js';
import { Conversation, SpanBase } from './conversation.js';
import type { WeftClient } from './client.js';
import { tracingClient } from './global.js';
import { Dataset, Model, WeftObject } from './objects.js';
import { isOp, op, type Op } from './op.js';
import { Scorer } from './scorers.js';
import { autoSummarizeValues } from './summarize.js';
import { digest } from './canonical.js';
import { Ref, isRefUri } from './refs.js';
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

/** Options of `EvaluationLogger.logPrediction`. */
export interface LogPredictionOptions {
  modelLatency?: number | null;
  /**
   * Link the prediction to traces that already exist instead of re-recording
   * them: a Call, a call id or call URI, a conversation span (Turn, LLM, ...),
   * a `LogResult`, a `Conversation`, or a list of these. The links are stored
   * on the prediction as `attributes.weft.linked_calls`,
   * `attributes.weft.genai_span_ref` (`[{trace_id, span_id}]`, as in Weave's
   * v2 predictions API) and `attributes.weft.linked_conversation_ids`. Without
   * an explicit output, a linked Call's output (and duration) is used.
   */
  call?: unknown;
  exampleId?: string;
  rowDigest?: string;
  trialIndex?: number;
  /** Eval kind stamped on linked spans (default "agent", as in Weave). */
  evalKind?: string | null;
}

interface Linked {
  calls: Array<{ call_id: string; trace_id?: string }>;
  conversationIds: string[];
  callObjects: Array<Call | string>;
}

function linkedCalls(link: unknown): Linked {
  const out: Linked = { calls: [], conversationIds: [], callObjects: [] };
  const items = Array.isArray(link) ? link : [link];
  for (const it of items) {
    if (it === null || it === undefined) continue;
    if (it instanceof CallClass) {
      out.calls.push({ call_id: it.id, trace_id: it.traceId });
      out.callObjects.push(it);
    } else if (typeof it === 'string') {
      if (isRefUri(it)) {
        const r = Ref.parse(it);
        if (r.kind !== 'call') throw new Error(`${it} is not a call ref`);
        out.calls.push({ call_id: r.name });
      } else out.calls.push({ call_id: it });
      out.callObjects.push(it);
    } else if (it instanceof SpanBase) {
      it.start();
      if (it.call) {
        out.calls.push({ call_id: it.call.id, trace_id: it.call.traceId });
        out.callObjects.push(it.call);
      }
    } else if (it instanceof Conversation) {
      out.conversationIds.push(it.conversationId);
    } else if (typeof it === 'object' && Array.isArray((it as any).rootSpanIds)) {
      const lr = it as { rootSpanIds: string[]; traceIds?: string[]; conversationId?: string };
      lr.rootSpanIds.forEach((id, i) => out.calls.push({ call_id: id, ...(lr.traceIds?.[i] ? { trace_id: lr.traceIds[i] } : {}) }));
      if (lr.conversationId) out.conversationIds.push(lr.conversationId);
    } else throw new Error(`cannot link ${typeof it === 'object' ? (it as object).constructor?.name : typeof it} to a prediction`);
  }
  out.conversationIds = [...new Set(out.conversationIds)];
  return out;
}

/**
 * Returned by `EvaluationLogger.logPrediction`; log scores, then `finish()`.
 * `await pred.run(async () => { ...; pred.output = x })` runs code as children
 * of the predict call (and stamps `weave.eval.*` on conversation spans started
 * inside), then finishes the prediction.
 */
export class ScoreLogger {
  readonly scores: Record<string, unknown> = {};
  output: unknown;
  modelLatency: number | null;
  private finished = false;
  private readonly t0 = performance.now();

  constructor(
    private readonly ev: EvaluationLogger,
    private readonly call: Call | null,
    readonly inputs: Record<string, unknown>,
    output: unknown,
    modelLatency: number | null,
    readonly predictCall: Call | null = null,
    private readonly evalContext: Record<string, unknown> | null = null,
  ) {
    this.output = output;
    this.modelLatency = modelLatency;
  }

  get predictAndScoreCall(): Call | null {
    return this.call;
  }

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

  private finishPredict(): void {
    const pc = this.predictCall;
    if (this.ev.client && pc && !pc.ended) {
      if (this.modelLatency === null) this.modelLatency = (performance.now() - this.t0) / 1000;
      this.ev.client.finishCall(pc, this.output);
    }
  }

  /** Run `fn` inside the prediction (calls become children of its predict call), then finish it. */
  async run<T>(fn: (pred: this) => T | Promise<T>): Promise<T> {
    const ctx = currentContext();
    const patch: Partial<WeftContext> = {};
    if (this.predictCall && !this.predictCall.ended) patch.stack = [...ctx.stack, this.predictCall];
    if (this.evalContext) patch.evalSpan = this.evalContext;
    try {
      return await runWith(patch, () => fn(this));
    } finally {
      this.finish();
    }
  }

  finish(): void {
    if (this.finished) return;
    this.finished = true;
    this.finishPredict();
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

  /**
   * Log one prediction. Without an output (and without a link) the predict
   * call stays open: set `pred.output` inside `pred.run(...)`. The third
   * argument is the model latency in seconds, or LogPredictionOptions.
   */
  logPrediction(inputs: Record<string, unknown>, output?: unknown, modelLatencyOrOpts: number | null | LogPredictionOptions = null): ScoreLogger {
    if (this.finished) throw new Error('evaluation already finished');
    const opts: LogPredictionOptions =
      modelLatencyOrOpts !== null && typeof modelLatencyOrOpts === 'object' ? modelLatencyOrOpts : { modelLatency: modelLatencyOrOpts as number | null };
    let modelLatency = opts.modelLatency ?? null;
    const linked = opts.call !== undefined ? linkedCalls(opts.call) : { calls: [], conversationIds: [], callObjects: [] };
    if (linked.calls.length && output === undefined && linked.callObjects.length === 1 && linked.callObjects[0] instanceof CallClass) {
      const c = linked.callObjects[0] as Call;
      output = !c.fetched && c.rawOutput !== undefined ? c.rawOutput : c.output;
      if (modelLatency === null && c.startedAt && c.endedAt) modelLatency = Math.max(0, (Date.parse(c.endedAt) - Date.parse(c.startedAt)) / 1000);
    }
    const evalKind = opts.evalKind === undefined ? 'agent' : opts.evalKind;
    const weft: Record<string, unknown> = {};
    if (linked.calls.length) {
      weft.linked_calls = linked.calls;
      weft.genai_span_ref = linked.calls.map((c) => ({ trace_id: c.trace_id ?? null, span_id: c.call_id }));
    }
    if (linked.conversationIds.length) weft.linked_conversation_ids = linked.conversationIds;
    const evalMeta: Record<string, unknown> = {};
    if (opts.exampleId !== undefined) evalMeta.example_id = opts.exampleId;
    if (opts.rowDigest !== undefined) evalMeta.row_digest = opts.rowDigest;
    if (opts.trialIndex !== undefined) evalMeta.trial_index = opts.trialIndex;
    if (evalKind) evalMeta.kind = evalKind;
    if (Object.keys(evalMeta).length) weft.eval = evalMeta;
    const attributes = Object.keys(weft).length ? { weft } : undefined;
    let pas: Call | null = null;
    let pc: Call | null = null;
    let evalContext: Record<string, unknown> | null = null;
    if (this.client && this.call) {
      pas = this.client.createCall(predictAndScore.__weftOp, { self: this.evaluation, model: this.model, example: this.example(inputs) }, { parent: this.call, attributes });
      pc = this.client.createCall(`${this.model.name ?? 'Model'}.predict`, { self: this.model, ...inputs }, { parent: pas, attributes });
      if (output !== undefined || linked.calls.length) this.client.finishCall(pc, output ?? null);
      evalContext = {
        run_id: this.call.id,
        predict_and_score_call_id: pas.id,
        project_id: this.client.projectId,
        kind: evalKind,
        row_digest: opts.rowDigest ?? digest(toJson(inputs)),
        example_id: opts.exampleId ?? null,
        trial_index: opts.trialIndex ?? null,
        evaluation_name: this.evaluation.name ?? null,
      };
    }
    const sl = new ScoreLogger(this, pas, { ...inputs }, output ?? null, modelLatency, pc, evalContext);
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
