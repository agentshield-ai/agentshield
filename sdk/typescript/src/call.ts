import type { WeftClient } from './client.js';
import { currentContext, storage } from './context.js';
import { isOp, op } from './op.js';
import { Ref, isRefUri } from './refs.js';
import { Scorer } from './scorers.js';

export interface ApplyScorerResult {
  result: unknown;
  scoreCall: Call | null;
  feedbackId: string | null;
}

export interface CallData {
  id: string;
  project_id: string;
  op_name: string;
  trace_id: string;
  parent_id?: string | null;
  display_name?: string | null;
  thread_id?: string | null;
  turn_id?: string | null;
  started_at?: string;
  ended_at?: string | null;
  inputs?: Record<string, unknown>;
  output?: unknown;
  exception?: string | null;
  attributes?: Record<string, unknown>;
  summary?: Record<string, any>;
  feedback?: unknown[];
  [k: string]: unknown;
}

/** A call: created locally by an op, or fetched from the server. */
export class Call {
  id: string;
  projectId: string;
  opName: string;
  traceId: string;
  parentId: string | null;
  displayName: string | null;
  threadId: string | null;
  turnId: string | null;
  startedAt: string | undefined;
  endedAt: string | null;
  inputs: Record<string, unknown>;
  output: unknown;
  exception: string | null;
  attributes: Record<string, unknown>;
  summary: Record<string, any>;
  /** W&B linkage (`wb_user_id`, `wb_username` with `includeUsernames`, `wb_run_id`, `wb_run_step`, `wb_run_step_end`). */
  wbUserId: string | null;
  wbUsername: string | null;
  wbRunId: string | null;
  wbRunStep: number | null;
  wbRunStepEnd: number | null;
  raw: CallData;
  /** Unserialised inputs/output of calls traced in this process. */
  rawInputs?: Record<string, unknown>;
  rawOutput?: unknown;
  /** @internal */ ended = false;
  /** @internal */ fetched = false;
  /** @internal True for calls recorded by conversation spans (turns, LLM, tool, subagent), which Weave records as OTel spans. */
  conversationSpan = false;
  /** @internal The conversation span (Turn, LLM, Tool, SubAgent, Handoff) recording this call. */
  span: unknown = null;
  /** @internal Attributes changed after the start was sent: the start is sent again before the end. */
  resendStart = false;
  /** @internal `reasoning()` steps recorded while the call ran. */
  reasoningSteps: Array<Record<string, unknown>> | null = null;
  /** @internal An LLM call was recorded under this one (its reasoning is not repeated here). */
  llmDescendant = false;

  constructor(
    data: CallData,
    private readonly client: WeftClient | null,
  ) {
    this.raw = data;
    this.id = data.id;
    this.projectId = data.project_id;
    this.opName = data.op_name;
    this.traceId = data.trace_id;
    this.parentId = data.parent_id ?? null;
    this.displayName = data.display_name ?? null;
    this.threadId = data.thread_id ?? null;
    this.turnId = data.turn_id ?? null;
    this.startedAt = data.started_at;
    this.endedAt = data.ended_at ?? null;
    this.inputs = data.inputs ?? {};
    this.output = data.output ?? null;
    this.exception = data.exception ?? null;
    this.attributes = data.attributes ?? {};
    this.summary = data.summary ?? {};
    this.wbUserId = (data.wb_user_id as string | undefined) ?? null;
    this.wbUsername = (data.wb_username as string | undefined) ?? null;
    this.wbRunId = (data.wb_run_id as string | undefined) ?? null;
    this.wbRunStep = (data.wb_run_step as number | undefined) ?? null;
    this.wbRunStepEnd = (data.wb_run_step_end as number | undefined) ?? null;
  }

  /** The call as a plain record with the server's field names (one row of a table / DataFrame). */
  toRecord(): Record<string, unknown> {
    const r: Record<string, unknown> = {
      id: this.id,
      project_id: this.projectId,
      op_name: this.opName,
      display_name: this.displayName,
      trace_id: this.traceId,
      parent_id: this.parentId,
      thread_id: this.threadId,
      turn_id: this.turnId,
      started_at: this.startedAt ?? null,
      ended_at: this.endedAt,
      inputs: this.inputs,
      output: this.output,
      exception: this.exception,
      attributes: this.attributes,
      summary: this.summary,
      status: this.status,
      wb_user_id: this.wbUserId,
      wb_username: this.wbUsername,
      wb_run_id: this.wbRunId,
      wb_run_step: this.wbRunStep,
      wb_run_step_end: this.wbRunStepEnd,
    };
    for (const k of ['deleted_at', 'storage_size_bytes', 'feedback']) if (this.raw[k] !== undefined) r[k] = this.raw[k];
    return r;
  }

  get opBaseName(): string {
    if (isRefUri(this.opName)) {
      try {
        return Ref.parse(this.opName).name;
      } catch {
        return this.opName;
      }
    }
    return this.opName;
  }

  uri(): string {
    return `weft:///${this.projectId}/call/${this.id}`;
  }

  /** A ref to this call. */
  get ref(): Ref {
    const [entity, project] = this.projectId.split('/');
    return new Ref(entity, project, 'call', this.id);
  }

  /** Link to this call in the Weft UI. */
  get uiUrl(): string {
    return `${this.client?.serverUrl ?? ''}/${this.projectId}/calls/${this.id}`;
  }

  /** `success`, `error`, `running` or `descendant_error` (server value when fetched). */
  get status(): string {
    const s = this.summary?.weave?.status;
    if (typeof s === 'string' && s) return s;
    if (this.exception) return 'error';
    return this.endedAt ? 'success' : 'running';
  }

  /** Direct children of this call (oldest first). */
  children(): Promise<Call[]> {
    return this.c().getCalls({ filter: { parent_ids: [this.id] }, sortBy: [{ field: 'started_at', direction: 'asc' }] });
  }

  private c(): WeftClient {
    if (!this.client) throw new Error('call is not attached to a client');
    return this.client;
  }

  async setDisplayName(name: string | null): Promise<void> {
    const c = this.c();
    await c.flush();
    this.displayName = name;
    await c.http.post('/call/update', { project_id: this.projectId, call_id: this.id, display_name: name });
  }

  async delete(): Promise<void> {
    const c = this.c();
    await c.flush();
    await c.http.post('/calls/delete', { project_id: this.projectId, call_ids: [this.id] });
  }

  /**
   * Score this call (guardrails / online evaluation). The scorer receives
   * `{ modelOutput: <call output>, datasetRow: <call inputs + additionalScorerKwargs> }`;
   * its call is recorded as its own trace and `wandb.runnable.<scorer>` feedback
   * (with runnable_ref and call_ref) is attached to this call. Resolves to the result.
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
      runnableRef = (await client.publish(scorer)).uri();
      fn = scorer.scoreOp;
      datasetRow = scorer.mapRow(row);
    } else {
      fn = isOp(scorer) ? scorer : op(scorer);
      name = fn.__weftOp.name;
      runnableRef = client.opRef(fn.__weftOp).uri();
    }
    const [result, scoreCall] = (await storage.run({ ...currentContext(), stack: [] }, () => fn.call({ modelOutput: output, datasetRow }))) as [unknown, Call | null];
    if (scoreCall?.exception) throw new Error(`scorer ${name} failed: ${scoreCall.exception.split('\n')[0]}`);
    let feedbackId: string | null = null;
    if (scoreCall) {
      feedbackId = await this.feedback.add(`wandb.runnable.${name}`, { output: result }, { runnable_ref: runnableRef, call_ref: scoreCall.uri() });
    }
    return { result, scoreCall, feedbackId };
  }

  get feedback(): CallFeedback {
    return new CallFeedback(this, this.c());
  }
}

export class CallFeedback {
  constructor(
    private readonly call: Call,
    private readonly client: WeftClient,
  ) {}

  async add(feedbackType: string, payload: Record<string, unknown>, extra: Record<string, unknown> = {}): Promise<string> {
    await this.client.flush();
    const res = await this.client.http.post('/feedback/create', {
      project_id: this.call.projectId,
      weave_ref: this.call.uri(),
      feedback_type: feedbackType,
      payload,
      ...extra,
    });
    return res.id;
  }

  addReaction(emoji: string): Promise<string> {
    return this.add('wandb.reaction.1', { emoji });
  }

  addNote(note: string): Promise<string> {
    return this.add('wandb.note.1', { note });
  }

  /** Human annotation following an AnnotationSpec (published if needed). */
  async addAnnotation(spec: { __weftRef?: Ref; name?: string; valueIsValid?: (v: unknown) => boolean }, value: unknown): Promise<string> {
    const ref = spec.__weftRef ?? (await this.client.publish(spec));
    if (spec.valueIsValid && !spec.valueIsValid(value)) throw new Error(`value ${JSON.stringify(value)} does not match annotation spec ${ref.name}`);
    return this.add(`wandb.annotation.${ref.name}`, { value }, { annotation_ref: ref.uri() });
  }

  /** Delete one feedback item by id. */
  async purge(feedbackId: string): Promise<void> {
    await this.client.http.post('/feedback/purge', {
      project_id: this.call.projectId,
      query: { $expr: { $eq: [{ $getField: 'id' }, { $literal: feedbackId }] } },
    });
  }

  async list(): Promise<any[]> {
    await this.client.flush();
    const res = await this.client.http.post('/feedback/query', {
      project_id: this.call.projectId,
      query: { $expr: { $eq: [{ $getField: 'weave_ref' }, { $literal: this.call.uri() }] } },
      sort_by: [{ field: 'created_at', direction: 'asc' }],
    });
    return res.result ?? [];
  }
}
