import type { WeftClient } from './client.js';
import { currentContext, storage } from './context.js';
import { deprecated } from './deprecation.js';
import { isOp, op } from './op.js';
import { Ref, isRefUri } from './refs.js';
import { Scorer } from './scorers.js';

export interface ApplyScorerResult {
  result: unknown;
  /** The scorer's own span. */
  scoreSpan: Span | null;
  /** @deprecated use `scoreSpan` */
  scoreCall: Span | null;
  feedbackId: string | null;
}

/** A span as the server sends it (docs/weft/API.md, Span schema). */
export interface SpanData {
  id: string;
  project_id: string;
  name: string;
  trace_id: string;
  parent_id?: string | null;
  display_name?: string | null;
  session_id?: string | null;
  turn_id?: string | null;
  started_at?: string;
  ended_at?: string | null;
  inputs?: Record<string, unknown>;
  output?: unknown;
  error?: string | null;
  attributes?: Record<string, unknown>;
  summary?: Record<string, any>;
  feedback?: unknown[];
  [k: string]: unknown;
}

/** Earlier name of SpanData. */
export type CallData = SpanData;

/**
 * A span: one traced function or model call, recorded locally by a traced
 * function or fetched from the server. `name` is the function ref (or a plain
 * span name), `error` the error text of a failed span, `sessionId` / `turnId`
 * the conversation and turn.
 */
export class Span {
  id: string;
  projectId: string;
  name: string;
  traceId: string;
  parentId: string | null;
  displayName: string | null;
  sessionId: string | null;
  turnId: string | null;
  startedAt: string | undefined;
  endedAt: string | null;
  inputs: Record<string, unknown>;
  output: unknown;
  error: string | null;
  attributes: Record<string, unknown>;
  summary: Record<string, any>;
  /** User and run linkage (`user_id`, `username` with `includeUsernames`, `run_id`, `run_step`, `run_step_end`). */
  userId: string | null;
  username: string | null;
  runId: string | null;
  runStep: number | null;
  runStepEnd: number | null;
  raw: SpanData;
  /** Unserialised inputs/output of spans traced in this process. */
  rawInputs?: Record<string, unknown>;
  rawOutput?: unknown;
  /** @internal */ ended = false;
  /** @internal */ fetched = false;
  /** @internal True for spans recorded by conversation spans (turns, LLM, tool, subagent) rather than by traced functions. */
  conversationSpan = false;
  /** @internal The conversation span (Turn, LLM, Tool, SubAgent, Handoff) recording this span. */
  span: unknown = null;
  /** @internal Attributes changed after the start was sent: the start is sent again before the end. */
  resendStart = false;
  /** @internal `reasoning()` steps recorded while the span ran. */
  reasoningSteps: Array<Record<string, unknown>> | null = null;
  /** @internal An LLM span was recorded under this one (its reasoning is not repeated here). */
  llmDescendant = false;

  constructor(
    data: SpanData,
    private readonly client: WeftClient | null,
  ) {
    this.raw = data;
    this.id = data.id;
    this.projectId = data.project_id;
    this.name = data.name ?? '';
    this.traceId = data.trace_id;
    this.parentId = data.parent_id ?? null;
    this.displayName = data.display_name ?? null;
    this.sessionId = data.session_id ?? null;
    this.turnId = data.turn_id ?? null;
    this.startedAt = data.started_at;
    this.endedAt = data.ended_at ?? null;
    this.inputs = data.inputs ?? {};
    this.output = data.output ?? null;
    this.error = data.error ?? null;
    this.attributes = data.attributes ?? {};
    this.summary = data.summary ?? {};
    this.userId = (data.user_id as string | undefined) ?? null;
    this.username = (data.username as string | undefined) ?? null;
    this.runId = (data.run_id as string | undefined) ?? null;
    this.runStep = (data.run_step as number | undefined) ?? null;
    this.runStepEnd = (data.run_step_end as number | undefined) ?? null;
  }

  /** The span as a plain record with the server's field names (one row of a table / DataFrame). */
  toRecord(): Record<string, unknown> {
    const r: Record<string, unknown> = {
      id: this.id,
      project_id: this.projectId,
      name: this.name,
      display_name: this.displayName,
      trace_id: this.traceId,
      parent_id: this.parentId,
      session_id: this.sessionId,
      turn_id: this.turnId,
      started_at: this.startedAt ?? null,
      ended_at: this.endedAt,
      inputs: this.inputs,
      output: this.output,
      error: this.error,
      attributes: this.attributes,
      summary: this.summary,
      status: this.status,
      user_id: this.userId,
      username: this.username,
      run_id: this.runId,
      run_step: this.runStep,
      run_step_end: this.runStepEnd,
    };
    for (const k of ['deleted_at', 'storage_size_bytes', 'feedback']) if (this.raw[k] !== undefined) r[k] = this.raw[k];
    return r;
  }

  /** The function's name for a function ref (`my_fn`), else `name`. */
  get baseName(): string {
    if (isRefUri(this.name)) {
      try {
        return Ref.parse(this.name).name;
      } catch {
        return this.name;
      }
    }
    return this.name;
  }

  // ---- earlier field names (deprecated)
  /** @deprecated use `name` */
  get opName(): string {
    deprecated('Span.opName', 'Span.name');
    return this.name;
  }
  set opName(v: string) {
    deprecated('Span.opName', 'Span.name');
    this.name = v;
  }
  /** @deprecated use `baseName` */
  get opBaseName(): string {
    deprecated('Span.opBaseName', 'Span.baseName');
    return this.baseName;
  }
  /** @deprecated use `error` */
  get exception(): string | null {
    deprecated('Span.exception', 'Span.error');
    return this.error;
  }
  set exception(v: string | null) {
    deprecated('Span.exception', 'Span.error');
    this.error = v;
  }
  /** @deprecated use `sessionId` */
  get threadId(): string | null {
    deprecated('Span.threadId', 'Span.sessionId');
    return this.sessionId;
  }
  set threadId(v: string | null) {
    deprecated('Span.threadId', 'Span.sessionId');
    this.sessionId = v;
  }

  uri(): string {
    return `weft:///${this.projectId}/span/${this.id}`;
  }

  /** A ref to this span. */
  get ref(): Ref {
    const [entity, project] = this.projectId.split('/');
    return new Ref(entity, project, 'span', this.id);
  }

  /** Link to this span in the Weft UI. */
  get uiUrl(): string {
    return `${this.client?.serverUrl ?? ''}/${this.projectId}/spans/${this.id}`;
  }

  /** `success`, `error`, `running` or `descendant_error` (server value when fetched). */
  get status(): string {
    const s = this.summary?.weft?.status;
    if (typeof s === 'string' && s) return s;
    if (this.error) return 'error';
    return this.endedAt ? 'success' : 'running';
  }

  /** Direct children of this span (oldest first). */
  children(): Promise<Span[]> {
    return this.c().spans({ filter: { parent_ids: [this.id] }, sortBy: [{ field: 'started_at', direction: 'asc' }] });
  }

  private c(): WeftClient {
    if (!this.client) throw new Error('span is not attached to a client');
    return this.client;
  }

  async setDisplayName(name: string | null): Promise<void> {
    const c = this.c();
    await c.flush();
    this.displayName = name;
    await c.http.post('/api/v1/spans/update', { project_id: this.projectId, id: this.id, display_name: name });
  }

  async delete(): Promise<void> {
    const c = this.c();
    await c.flush();
    await c.http.post('/api/v1/spans/delete', { project_id: this.projectId, ids: [this.id] });
  }

  /**
   * Score this span (guardrails / online evaluation). The scorer receives
   * `{ modelOutput: <span output>, datasetRow: <span inputs + additionalScorerKwargs> }`;
   * its span is recorded as its own trace and `weft.scorer.<scorer>` feedback
   * (with runnable_ref and span_ref) is attached to this span. Resolves to the result.
   */
  async applyScorer(
    scorer: Scorer | ((args: { modelOutput: unknown; datasetRow: Record<string, unknown> }) => unknown),
    opts: { additionalScorerKwargs?: Record<string, unknown> } = {},
  ): Promise<ApplyScorerResult> {
    const client = this.c();
    const row: Record<string, unknown> = { ...(this.rawInputs ?? this.inputs) };
    delete row.self;
    Object.assign(row, opts.additionalScorerKwargs ?? {});
    const output = this.fetched ? this.output : this.rawOutput !== undefined ? this.rawOutput : this.output;
    let name: string;
    let runnableRef: string;
    let fn: any;
    let datasetRow = row;
    if (scorer instanceof Scorer) {
      name = scorer.scorerName;
      runnableRef = (await client.save(scorer)).uri();
      fn = scorer.scoreOp;
      datasetRow = scorer.mapRow(row);
    } else {
      fn = isOp(scorer) ? scorer : op(scorer);
      name = fn.__weftOp.name;
      runnableRef = client.opRef(fn.__weftOp).uri();
    }
    const [result, scoreSpan] = (await storage.run({ ...currentContext(), stack: [] }, () => fn.call({ modelOutput: output, datasetRow }))) as [unknown, Span | null];
    if (scoreSpan?.error) throw new Error(`scorer ${name} failed: ${scoreSpan.error.split('\n')[0]}`);
    let feedbackId: string | null = null;
    if (scoreSpan) {
      feedbackId = await this.feedback.add(`weft.scorer.${name}`, { output: result }, { runnable_ref: runnableRef, span_ref: scoreSpan.uri() });
    }
    const res = { result, scoreSpan, feedbackId } as ApplyScorerResult;
    Object.defineProperty(res, 'scoreCall', {
      get: () => {
        deprecated('ApplyScorerResult.scoreCall', 'ApplyScorerResult.scoreSpan');
        return scoreSpan;
      },
      enumerable: false,
    });
    return res;
  }

  get feedback(): SpanFeedback {
    return new SpanFeedback(this, this.c());
  }
}

/** Earlier name of Span (used inside the SDK; the public `Call` export warns). */
export const Call = Span;
export type Call = Span;

/** Feedback attached to a span: list it, or add reactions, notes and payloads. */
export class SpanFeedback {
  constructor(
    private readonly call: Span,
    private readonly client: WeftClient,
  ) {}

  async add(feedbackType: string, payload: Record<string, unknown>, extra: Record<string, unknown> = {}): Promise<string> {
    await this.client.flush();
    const { call_ref: callRef, ...rest } = extra;
    if (callRef !== undefined && rest.span_ref === undefined) rest.span_ref = callRef;
    const res = await this.client.http.post('/api/v1/feedback/create', {
      project_id: this.call.projectId,
      ref: this.call.uri(),
      feedback_type: feedbackType,
      payload,
      ...rest,
    });
    return res.id;
  }

  addReaction(emoji: string): Promise<string> {
    return this.add('weft.reaction', { emoji });
  }

  addNote(note: string): Promise<string> {
    return this.add('weft.note', { note });
  }

  /** Human annotation following an AnnotationSpec (saved if needed). */
  async addAnnotation(spec: { __weftRef?: Ref; name?: string; valueIsValid?: (v: unknown) => boolean }, value: unknown): Promise<string> {
    const ref = spec.__weftRef ?? (await this.client.save(spec));
    if (spec.valueIsValid && !spec.valueIsValid(value)) throw new Error(`value ${JSON.stringify(value)} does not match annotation spec ${ref.name}`);
    return this.add(`weft.annotation.${ref.name}`, { value }, { annotation_ref: ref.uri() });
  }

  /** Delete one feedback item by id. */
  async purge(feedbackId: string): Promise<void> {
    await this.client.http.post('/api/v1/feedback/delete', { project_id: this.call.projectId, ids: [feedbackId] });
  }

  async list(): Promise<any[]> {
    await this.client.flush();
    const res = await this.client.http.post('/api/v1/feedback/query', {
      project_id: this.call.projectId,
      query: { $expr: { $eq: [{ $getField: 'ref' }, { $literal: this.call.uri() }] } },
      sort_by: [{ field: 'created_at', direction: 'asc' }],
      limit: 10000,
    });
    return res.items ?? [];
  }
}

/** Earlier name of SpanFeedback. */
export const CallFeedback = SpanFeedback;
export type CallFeedback = SpanFeedback;
