import type { WeftClient } from './client.js';
import { requireClient } from './global.js';
import { Ref } from './refs.js';
import { toJson, type Publishable } from './serialize.js';

/** Base class for versioned objects (published with `weft.publish`). */
export abstract class WeftObject implements Publishable {
  name?: string;
  description?: string | null;
  __weftRef?: Ref;

  constructor(opts: { name?: string; description?: string | null } = {}) {
    this.name = opts.name;
    this.description = opts.description ?? null;
    Object.defineProperty(this, '__weftRef', { value: undefined, writable: true, enumerable: false });
  }

  get ref(): Ref | undefined {
    return this.__weftRef;
  }

  /** Class name stored as `_type` / `_class_name`. */
  abstract get className(): string;

  bases(): string[] {
    return ['Object', 'BaseModel'];
  }

  /** Field values (excluding name/description). */
  abstract fields(): Record<string, unknown>;

  protected header(): Record<string, unknown> {
    return { _type: this.className, _class_name: this.className, _bases: this.bases(), name: this.name ?? null, description: this.description ?? null };
  }

  /** JSON value used when this object appears unpublished inside call inputs/outputs. */
  toVal(client: WeftClient | null, _publishNested = false): unknown {
    const val = this.header();
    for (const [k, v] of Object.entries(this.fields())) val[k] = toJson(v, client);
    return val;
  }

  /** Stored value with nested objects published locally (no network wait). */
  localVal(client: WeftClient): Record<string, unknown> {
    const val = this.header();
    for (const [k, v] of Object.entries(this.fields())) val[k] = client.publishNestedLocal(v);
    return val;
  }

  /** JSON value stored when publishing (nested objects/tables published first). */
  async publishVal(client: WeftClient): Promise<Record<string, unknown>> {
    const val = this.header();
    for (const [k, v] of Object.entries(this.fields())) val[k] = await client.publishNested(v);
    return val;
  }
}

/** A versioned list of rows; rows are uploaded as a server table on publish. */
export class Dataset<R extends Record<string, unknown> = Record<string, unknown>> extends WeftObject {
  rows: R[];
  rowDigests?: string[];
  tableRef?: Ref;

  constructor(opts: { name?: string; description?: string | null; rows: R[] }) {
    super(opts);
    this.rows = [...opts.rows];
    Object.defineProperty(this, 'rowDigests', { value: undefined, writable: true, enumerable: false });
    Object.defineProperty(this, 'tableRef', { value: undefined, writable: true, enumerable: false });
  }

  get className(): string {
    return 'Dataset';
  }

  get length(): number {
    return this.rows.length;
  }

  [Symbol.iterator](): Iterator<R> {
    return this.rows[Symbol.iterator]();
  }

  fields(): Record<string, unknown> {
    return { rows: this.tableRef ? this.tableRef.uri() : this.rows };
  }

  async publishVal(client: WeftClient): Promise<Record<string, unknown>> {
    if (!this.tableRef || !this.rowDigests || this.tableRef.projectId !== client.projectId) {
      const { ref, rowDigests } = await client.publishTable(this.rows);
      this.tableRef = ref;
      this.rowDigests = rowDigests;
    }
    return { ...this.header(), rows: this.tableRef.uri() };
  }

  localVal(client: WeftClient): Record<string, unknown> {
    if (!this.tableRef || !this.rowDigests || this.tableRef.projectId !== client.projectId) {
      const { ref, rowDigests } = client.publishTableLocal(this.rows);
      this.tableRef = ref;
      this.rowDigests = rowDigests;
    }
    return { ...this.header(), rows: this.tableRef.uri() };
  }

  /**
   * A new dataset version with `rows` appended. When this dataset is
   * published the rows are appended server side (/table/update) and the new
   * version is published under the same name.
   */
  async addRows(rows: R[]): Promise<Dataset<R>> {
    const next = new Dataset<R>({ name: this.name, description: this.description, rows: [...this.rows, ...rows] });
    if (!this.__weftRef) return next;
    const client = requireClient();
    if (!this.tableRef || !this.rowDigests || this.tableRef.projectId !== client.projectId) {
      const t = await client.publishTable(this.rows);
      this.tableRef = t.ref;
      this.rowDigests = t.rowDigests;
    }
    const { ref, appended } = await client.appendTable(this.tableRef, rows);
    next.tableRef = ref;
    next.rowDigests = [...this.rowDigests, ...appended];
    await client.publish(next, this.__weftRef.name);
    return next;
  }

  /** A new (unpublished) dataset with the rows at `indices`, in that order. */
  select(indices: Iterable<number>): Dataset<R> {
    const idx = [...indices];
    if (!idx.length) throw new Error('Cannot select rows with an empty set of indices.');
    return new Dataset<R>({ name: this.name, description: this.description, rows: idx.map((i) => {
      const r = this.rows[i];
      if (r === undefined) throw new RangeError(`row index ${i} out of range`);
      return r;
    }) });
  }

  /** Column names (keys of the first row). */
  get columnNames(): string[] {
    return this.rows.length ? Object.keys(this.rows[0]) : [];
  }

  get numRows(): number {
    return this.rows.length;
  }

  /** One row `{inputs, output}` per call (`self` dropped). */
  static fromCalls(calls: Iterable<{ inputs: Record<string, unknown>; output: unknown }>, name?: string): Dataset {
    const rows = [...calls].map((c) => {
      const { self: _self, ...inputs } = c.inputs ?? {};
      return { inputs, output: c.output };
    });
    return new Dataset({ name, rows });
  }

  /** Ref to one row (`.../attr/rows/id/<row digest>`), available once published. */
  rowRef(index: number): Ref | undefined {
    const d = this.rowDigests?.[index];
    return this.__weftRef && d ? this.__weftRef.withExtra('attr', 'rows', 'id', d) : undefined;
  }
}

/** A generic model object (name plus free-form attributes), as created by EvaluationLogger. */
export class Model extends WeftObject {
  attributes: Record<string, unknown>;

  constructor(opts: { name?: string; description?: string | null; attributes?: Record<string, unknown> } = {}) {
    super(opts);
    this.attributes = opts.attributes ?? {};
  }

  get className(): string {
    return 'Model';
  }

  fields(): Record<string, unknown> {
    return { ...this.attributes };
  }
}

/** A generic published object loaded from the server (class not known locally). */
export type LoadedObject = Record<string, unknown> & { __weftRef?: Ref };

// ---------------------------------------------------------------------------
// prompts and server-runnable models

/** `str.format`-style substitution that leaves unknown `{placeholders}` untouched. */
export function safeFormat(template: string, values: Record<string, unknown>): string {
  return template.replace(/\{([A-Za-z_][A-Za-z0-9_]*)\}/g, (m, k: string) => (k in values ? String(values[k]) : m));
}

/** Base class for prompts. */
export abstract class Prompt extends WeftObject {
  bases(): string[] {
    return ['Prompt', 'Object', 'BaseModel'];
  }
  abstract format(values?: Record<string, unknown>): unknown;
}

/** A text template: `new StringPrompt({ content: 'Hello {name}' }).format({ name: 'x' })`. */
export class StringPrompt extends Prompt {
  content: string;

  constructor(opts: { name?: string; description?: string | null; content?: string } | string = {}) {
    const o = typeof opts === 'string' ? { content: opts } : opts;
    super(o);
    this.content = o.content ?? '';
  }

  get className(): string {
    return 'StringPrompt';
  }

  fields(): Record<string, unknown> {
    return { content: this.content };
  }

  format(values: Record<string, unknown> = {}): string {
    return safeFormat(this.content, values);
  }
}

export type PromptMessage = { role: string; content: unknown; [k: string]: unknown };

/** A chat template: `{role, content}` messages with `{placeholders}`. */
export class MessagesPrompt extends Prompt {
  messages: PromptMessage[];

  constructor(opts: { name?: string; description?: string | null; messages?: PromptMessage[] } | PromptMessage[] = {}) {
    const o = Array.isArray(opts) ? { messages: opts } : opts;
    super(o);
    this.messages = o.messages ?? [];
  }

  get className(): string {
    return 'MessagesPrompt';
  }

  fields(): Record<string, unknown> {
    return { messages: this.messages };
  }

  format(values: Record<string, unknown> = {}): PromptMessage[] {
    return this.messages.map((m) => {
      const c = m.content;
      if (typeof c === 'string') return { ...m, content: safeFormat(c, values) };
      if (Array.isArray(c))
        return { ...m, content: c.map((p: any) => (p && typeof p.text === 'string' ? { ...p, text: safeFormat(p.text, values) } : p)) };
      return { ...m };
    });
  }
}

/**
 * A server-runnable chat model: `defaultParams.messages_template` formatted with
 * the row's columns. `predict` calls the Weft server's `/completions/create`;
 * the model can also be evaluated server side with `client.evaluateModel`.
 */
export class LLMStructuredCompletionModel extends Model {
  llmModelId: string;
  defaultParams: Record<string, any>;

  constructor(opts: { name?: string; description?: string | null; llmModelId: string; defaultParams?: Record<string, any> }) {
    super(opts);
    this.llmModelId = opts.llmModelId;
    this.defaultParams = opts.defaultParams ?? {};
  }

  get className(): string {
    return 'LLMStructuredCompletionModel';
  }

  bases(): string[] {
    return ['Model', 'Object', 'BaseModel'];
  }

  fields(): Record<string, unknown> {
    return { llm_model_id: this.llmModelId, default_params: this.defaultParams };
  }

  /** Format the template with `inputs`, call `/completions/create`, return the message content (parsed for json_object). */
  async predict(inputs: Record<string, unknown>): Promise<unknown> {
    const client = requireClient();
    const { messages_template, response_format, ...params } = this.defaultParams;
    const template = messages_template ?? [{ role: 'user', content: '{input}' }];
    const req: Record<string, unknown> = { model: this.llmModelId, messages: new MessagesPrompt({ messages: template }).format(inputs) };
    for (const k of ['temperature', 'max_tokens', 'top_p', 'frequency_penalty', 'presence_penalty', 'stop', 'n', 'tools'])
      if (params[k] !== undefined && params[k] !== null) req[k] = params[k];
    if (response_format && typeof response_format === 'object') req.response_format = response_format;
    else if (response_format && response_format !== 'text') req.response_format = { type: response_format };
    const resp = await client.completionsCreate(req);
    const content = resp?.choices?.[0]?.message?.content;
    const rf = typeof response_format === 'object' ? response_format?.type : response_format;
    if (rf === 'json_object' && typeof content === 'string') {
      try {
        return JSON.parse(content);
      } catch {
        return content;
      }
    }
    return content;
  }
}

/** Rebuild prompts and LLMStructuredCompletionModel values loaded from the server (null otherwise). */
export function loadPromptOrModel(val: any, ref: Ref | undefined): WeftObject | null {
  const base = { name: val.name ?? undefined, description: val.description ?? null };
  let obj: WeftObject | null = null;
  switch (val._class_name) {
    case 'StringPrompt':
      obj = new StringPrompt({ ...base, content: val.content ?? '' });
      break;
    case 'MessagesPrompt':
      obj = new MessagesPrompt({ ...base, messages: val.messages ?? [] });
      break;
    case 'LLMStructuredCompletionModel':
      obj = new LLMStructuredCompletionModel({ ...base, llmModelId: val.llm_model_id, defaultParams: val.default_params ?? {} });
      break;
  }
  if (obj) obj.__weftRef = ref;
  return obj;
}
