import type { WeftClient } from './client.js';
import { Ref, isRefUri } from './refs.js';

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
  raw: CallData;
  /** @internal */ ended = false;

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
