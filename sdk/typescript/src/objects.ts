import type { WeftClient } from './client.js';
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

  /** Ref to one row (`.../attr/rows/id/<row digest>`), available once published. */
  rowRef(index: number): Ref | undefined {
    const d = this.rowDigests?.[index];
    return this.__weftRef && d ? this.__weftRef.withExtra('attr', 'rows', 'id', d) : undefined;
  }
}

/** A generic published object loaded from the server (class not known locally). */
export type LoadedObject = Record<string, unknown> & { __weftRef?: Ref };
