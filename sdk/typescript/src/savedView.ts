/**
 * SavedView: a saved traces/evaluations view with a fluent query API (like `weave.SavedView`).
 *
 * ```ts
 * const view = await new weft.SavedView({ label: 'Slow errors' })
 *   .filterOp('answer')
 *   .addFilter('summary.weave.status', 'equals', 'error')
 *   .addFilter('summary.weave.latency_ms', '>', 1000)
 *   .sortBy('started_at', 'desc')
 *   .save();
 * const calls = await view.getCalls({ limit: 20 });
 * const same = await weft.SavedView.load('Slow-errors');
 * ```
 *
 * The stored object follows the Weft UI convention: `{view_type, label,
 * definition: {filter, query, sort_by, cols, search, columns, pin, page_size}}`.
 */
import type { Call } from './call.js';
import { getClient, requireClient } from './global.js';
import { WeftObject } from './objects.js';
import { Ref, isRefUri } from './refs.js';

export const KNOWN_COLUMNS = [
  'id', 'display_name', 'op_name', 'inputs', 'output', 'attributes', 'summary', 'started_at', 'ended_at',
  'exception', 'func_name', 'parent_id', 'project_id', 'trace_id', 'thread_id', 'turn_id', 'ui_url',
  'wb_user_id', 'wb_run_id', 'feedback',
];
const COLUMN_ALIASES: Record<string, string> = { Status: 'summary.weave.status' };

/** User operator → Weave's stored operator. */
export const OPERATOR_MAP: Record<string, string> = {
  contains: '(string): contains',
  'does not contain': '(string): notContains',
  equals: '(string): equals',
  'does not equal': '(string): notEquals',
  in: '(string): in',
  '=': '(number): =',
  '≠': '(number): !=',
  '!=': '(number): !=',
  '<': '(number): <',
  '≤': '(number): <=',
  '<=': '(number): <=',
  '>': '(number): >',
  '≥': '(number): >=',
  '>=': '(number): >=',
  is: '(bool): is',
  after: '(date): after',
  before: '(date): before',
  'is empty': '(any): isEmpty',
  'is not empty': '(any): isNotEmpty',
};
const VALUELESS = new Set(['(any): isEmpty', '(any): isNotEmpty']);
const NO_FLOAT_CONVERT = new Set(['summary.weave.latency_ms', 'started_at', 'ended_at']);
const DEFAULT_PIN = { left: ['CustomCheckbox', 'op_name'], right: [] as string[] };
export const CALLS_FILTER_KEYS = new Set([
  'op_names', 'input_refs', 'output_refs', 'parent_ids', 'trace_ids', 'call_ids', 'trace_roots_only',
  'wb_user_ids', 'wb_run_ids', 'thread_ids', 'turn_ids',
]);

export interface ViewFilter {
  field: string;
  /** Operator in Weave's stored form, e.g. "(string): contains". */
  operator: string;
  value?: unknown;
}

/** Seconds since the epoch from a number, numeric string, ISO date or Date. */
export function toSeconds(v: unknown): number | null {
  if (v === null || v === undefined || v === '') return null;
  if (typeof v === 'number') return v;
  if (v instanceof Date) return v.getTime() / 1000;
  const n = Number(v);
  if (!Number.isNaN(n)) return n;
  const t = Date.parse(String(v));
  return Number.isNaN(t) ? null : t / 1000;
}

function fieldExpr(field: string): Record<string, unknown> {
  return NO_FLOAT_CONVERT.has(field) ? { $getField: field } : { $convert: { input: { $getField: field }, to: 'double' } };
}

/** One filter as a calls-query operation. */
export function filterToClause(f: ViewFilter): Record<string, unknown> {
  const get = { $getField: f.field };
  const v = f.value;
  switch (f.operator) {
    case '(any): isEmpty':
      return { $eq: [get, { $literal: '' }] };
    case '(any): isNotEmpty':
      return { $not: [{ $eq: [get, { $literal: '' }] }] };
    case '(string): contains':
      return { $contains: { input: get, substr: { $literal: v } } };
    case '(string): notContains':
      return { $not: [{ $contains: { input: get, substr: { $literal: v } } }] };
    case '(string): equals':
      return { $eq: [get, { $literal: v }] };
    case '(string): notEquals':
      return { $not: [{ $eq: [get, { $literal: v }] }] };
    case '(string): in': {
      const vals = Array.isArray(v) ? v : String(v).split(',').map((x) => x.trim());
      return { $or: vals.map((x) => ({ $eq: [get, { $literal: x }] })) };
    }
    case '(bool): is':
      return { $eq: [get, { $literal: v === true ? 'True' : v === false ? 'False' : String(v) }] };
    case '(date): after':
    case '(date): before': {
      const secs = toSeconds(v);
      if (secs === null) throw new Error(`not a date or timestamp: ${JSON.stringify(v)}`);
      const gt = { $gt: [get, { $literal: secs }] };
      return f.operator === '(date): after' ? gt : { $not: [gt] };
    }
  }
  if (f.operator.startsWith('(number): ')) {
    const num = Number(v);
    if (Number.isNaN(num)) throw new Error(`Invalid number: ${String(v)}`);
    const lhs = fieldExpr(f.field);
    const lit = { $literal: num };
    switch (f.operator.slice('(number): '.length)) {
      case '=':
        return { $eq: [lhs, lit] };
      case '!=':
        return { $not: [{ $eq: [lhs, lit] }] };
      case '>':
        return { $gt: [lhs, lit] };
      case '>=':
        return { $gte: [lhs, lit] };
      case '<':
        return { $not: [{ $gte: [lhs, lit] }] };
      case '<=':
        return { $not: [{ $gt: [lhs, lit] }] };
    }
  }
  throw new Error(`cannot translate filter operator ${JSON.stringify(f.operator)}`);
}

export function filtersToQuery(filters: ViewFilter[]): Record<string, unknown> | null {
  return filters.length ? { $expr: { $and: filters.map(filterToClause) } } : null;
}

function fieldOf(op: any): string | null {
  if (op && typeof op === 'object') {
    if (typeof op.$getField === 'string') return op.$getField;
    if (op.$convert?.input?.$getField) return op.$convert.input.$getField;
  }
  return null;
}

function literalOf(op: any): unknown {
  if (op && typeof op === 'object' && '$literal' in op) return op.$literal;
  throw new Error(`not a literal: ${JSON.stringify(op)}`);
}

function clauseToFilter(c: any): ViewFilter {
  if (c.$eq) {
    const [a, b] = c.$eq;
    const field = fieldOf(a);
    const val = literalOf(b);
    if (!field) throw new Error(JSON.stringify(c));
    if (a.$convert) return { field, operator: '(number): =', value: val };
    if (val === '') return { field, operator: '(any): isEmpty' };
    if (val === 'True' || val === 'False') return { field, operator: '(bool): is', value: val === 'True' };
    return { field, operator: '(string): equals', value: val };
  }
  if (c.$contains) {
    const field = fieldOf(c.$contains.input);
    if (!field) throw new Error(JSON.stringify(c));
    return { field, operator: '(string): contains', value: literalOf(c.$contains.substr) };
  }
  for (const [key, op] of [['$gt', '(number): >'], ['$gte', '(number): >=']] as const) {
    if (c[key]) {
      const [a, b] = c[key];
      const field = fieldOf(a);
      if (!field) throw new Error(JSON.stringify(c));
      if (key === '$gt' && (field === 'started_at' || field === 'ended_at')) return { field, operator: '(date): after', value: literalOf(b) };
      return { field, operator: op, value: literalOf(b) };
    }
  }
  if (c.$not) {
    const f = clauseToFilter(c.$not[0]);
    const flip: Record<string, string> = {
      '(any): isEmpty': '(any): isNotEmpty',
      '(string): contains': '(string): notContains',
      '(string): equals': '(string): notEquals',
      '(number): =': '(number): !=',
      '(number): >=': '(number): <',
      '(number): >': '(number): <=',
      '(date): after': '(date): before',
    };
    if (!flip[f.operator]) throw new Error(JSON.stringify(c));
    return { ...f, operator: flip[f.operator] };
  }
  if (Array.isArray(c.$or)) {
    const fs = c.$or.map(clauseToFilter);
    if (fs.length && fs.every((x: ViewFilter) => x.operator === '(string): equals' && x.field === fs[0].field)) {
      return { field: fs[0].field, operator: '(string): in', value: fs.map((x: ViewFilter) => x.value) };
    }
  }
  throw new Error(`query is not a list of simple filters: ${JSON.stringify(c)}`);
}

/** Translate a view query back into filters (throws when it is not a list of simple filters). */
export function queryToFilters(query: Record<string, any> | null | undefined): ViewFilter[] {
  if (!query) return [];
  const expr = query.$expr ?? query;
  const clauses = Array.isArray(expr?.$and) ? expr.$and : [expr];
  return clauses.map(clauseToFilter);
}

function pathParts(p: string | Array<string | number>): Array<string | number> {
  return Array.isArray(p) ? [...p] : String(p).split('.').map((x) => (/^\d+$/.test(x) ? Number(x) : x));
}

function pathStr(p: string | Array<string | number>): string {
  return Array.isArray(p) ? p.map(String).join('.') : p;
}

function checkColumn(path: string): void {
  if (!KNOWN_COLUMNS.includes(String(pathParts(path)[0]))) throw new Error(`unknown column ${JSON.stringify(path)}: the first path segment must be one of KNOWN_COLUMNS`);
}

export interface SavedViewGetCallsOptions {
  limit?: number;
  offset?: number;
  includeCosts?: boolean;
  includeFeedback?: boolean;
  allColumns?: boolean;
}

/** A saved traces/evaluations view: build it fluently, `save()`, `load()`, and run it with `getCalls()`. */
export class SavedView extends WeftObject {
  viewType: string;
  label: string | null;
  definition: Record<string, any>;

  constructor(opts: { name?: string; description?: string | null; viewType?: string; label?: string | null; definition?: Record<string, unknown> } = {}) {
    super(opts);
    this.viewType = opts.viewType ?? 'traces';
    this.label = opts.label === undefined ? 'SavedView' : opts.label;
    this.definition = { ...(opts.definition ?? {}) };
  }

  get className(): string {
    return 'SavedView';
  }

  fields(): Record<string, unknown> {
    return { view_type: this.viewType, label: this.label, definition: this.definition };
  }

  private get projectRef(): { entity: string; project: string } | null {
    if (this.__weftRef) return { entity: this.__weftRef.entity, project: this.__weftRef.project };
    const c = getClient();
    return c ? { entity: c.entity, project: c.project } : null;
  }

  private touch(): this {
    delete this.definition.ui_params; // the UI prefers ui_params over the definition, so they go stale
    return this;
  }

  rename(label: string): this {
    this.label = label;
    return this;
  }

  /** The query as filters. */
  get filters(): ViewFilter[] {
    return queryToFilters(this.definition.query);
  }

  /** Add `field operator value`: contains, does not contain, equals, does not equal, in, =, !=, <, <=, >, >=, is, after, before, is empty, is not empty. */
  addFilter(field: string, operator: string, value?: unknown): this {
    field = COLUMN_ALIASES[field] ?? field;
    checkColumn(field);
    const op = OPERATOR_MAP[operator] ?? (Object.values(OPERATOR_MAP).includes(operator) ? operator : null);
    if (!op) throw new Error(`unknown filter operator ${JSON.stringify(operator)}`);
    if (VALUELESS.has(op) && value !== undefined) throw new Error(`filter operator ${JSON.stringify(operator)} takes no value`);
    if (!VALUELESS.has(op) && value === undefined) throw new Error(`filter operator ${JSON.stringify(operator)} needs a value`);
    const f: ViewFilter = VALUELESS.has(op) ? { field, operator: op } : { field, operator: op, value };
    filterToClause(f);
    const fs = this.filters;
    if (!fs.some((x) => JSON.stringify(x) === JSON.stringify(f))) {
      fs.push(f);
      this.definition.query = filtersToQuery(fs);
    }
    return this.touch();
  }

  removeFilter(indexOrField: number | string): this {
    let fs = this.filters;
    if (typeof indexOrField === 'number') fs.splice(indexOrField, 1);
    else fs = fs.filter((f) => f.field !== indexOrField);
    const q = filtersToQuery(fs);
    if (q) this.definition.query = q;
    else delete this.definition.query;
    return this.touch();
  }

  removeFilters(): this {
    delete this.definition.query;
    return this.touch();
  }

  /** Only calls of one op (a name, `name:version` or op ref); null clears it. */
  filterOp(opName: string | null): this {
    const flt = { ...(this.definition.filter ?? {}) };
    if (opName === null) delete flt.op_names;
    else {
      let uri = opName;
      if (!isRefUri(opName)) {
        const p = this.projectRef;
        if (!p) throw new Error('Must specify an op ref when no project is known');
        uri = `weft:///${p.entity}/${p.project}/op/${opName}${opName.includes(':') ? '' : ':*'}`;
      }
      flt.op_names = [uri];
    }
    if (Object.keys(flt).length) this.definition.filter = flt;
    else delete this.definition.filter;
    return this.touch();
  }

  /** Set CallsFilter fields (`trace_roots_only`, `thread_ids`, `wb_user_ids`, ...); null removes one. */
  setFilter(fields: Record<string, unknown>): this {
    const bad = Object.keys(fields).filter((k) => !CALLS_FILTER_KEYS.has(k));
    if (bad.length) throw new Error(`unknown call filter fields: ${bad.join(', ')}`);
    const flt = { ...(this.definition.filter ?? {}) };
    for (const [k, v] of Object.entries(fields)) {
      if (v === null || v === undefined) delete flt[k];
      else flt[k] = v;
    }
    this.definition.filter = flt;
    return this.touch();
  }

  /** Free-text search over op name, display name, inputs, output and exception. */
  search(text: string | null): this {
    if (text) this.definition.search = text;
    else delete this.definition.search;
    return this.touch();
  }

  addSort(field: string, direction: 'asc' | 'desc'): this {
    if (direction !== 'asc' && direction !== 'desc') throw new Error("direction must be 'asc' or 'desc'");
    this.definition.sort_by = [...(this.definition.sort_by ?? []), { field, direction }];
    return this.touch();
  }

  sortBy(field: string, direction: 'asc' | 'desc'): this {
    this.definition.sort_by = [];
    return this.addSort(field, direction);
  }

  pageSize(n: number): this {
    this.definition.page_size = n;
    return this.touch();
  }

  private cols(): string[] {
    return [...(this.definition.cols ?? [])];
  }

  showColumn(col: string): this {
    const cols = this.cols();
    if (!cols.includes(col)) cols.push(col);
    this.definition.cols = cols;
    return this.touch();
  }

  hideColumn(col: string): this {
    this.definition.cols = this.cols().filter((c) => c !== col);
    return this.touch();
  }

  addColumn(path: string | Array<string | number>, label?: string): this {
    return this.insertColumn((this.definition.columns ?? []).length, path, label);
  }

  addColumns(...columns: string[]): this {
    for (const c of columns) this.addColumn(c);
    return this;
  }

  insertColumn(idx: number, path: string | Array<string | number>, label?: string): this {
    if (typeof path === 'string' && COLUMN_ALIASES[path]) {
      label ??= path;
      path = COLUMN_ALIASES[path];
    }
    const p = pathStr(path);
    checkColumn(p);
    const columns = [...(this.definition.columns ?? [])];
    columns.splice(idx, 0, { path: pathParts(path), label: label ?? null });
    this.definition.columns = columns;
    const cols = this.cols();
    if (!cols.includes(p)) cols.splice(Math.min(idx, cols.length), 0, p);
    this.definition.cols = cols;
    return this.touch();
  }

  setColumns(...columns: string[]): this {
    this.definition.columns = [];
    this.definition.cols = [];
    return this.addColumns(...columns);
  }

  removeColumn(path: number | string | Array<string | number>): this {
    let columns: any[] = [...(this.definition.columns ?? [])];
    let p: string | null = null;
    if (typeof path === 'number') {
      if (path >= 0 && path < columns.length) p = pathStr(columns.splice(path, 1)[0].path);
    } else {
      p = pathStr(typeof path === 'string' ? (COLUMN_ALIASES[path] ?? path) : path);
      columns = columns.filter((c) => pathStr(c.path ?? []) !== p);
    }
    if (p !== null) this.definition.cols = this.cols().filter((c) => c !== p);
    if (columns.length) this.definition.columns = columns;
    else delete this.definition.columns;
    return this.touch();
  }

  removeColumns(...columns: string[]): this {
    if (!columns.length) {
      delete this.definition.columns;
      delete this.definition.cols;
      return this.touch();
    }
    for (const c of columns) this.removeColumn(c);
    return this;
  }

  renameColumn(path: number | string, label: string): this {
    const i = this.columnIndex(path);
    const columns = [...(this.definition.columns ?? [])];
    if (i >= columns.length) throw new Error('only explicitly added columns can be renamed');
    columns[i] = { ...columns[i], label };
    this.definition.columns = columns;
    return this;
  }

  columnIndex(path: number | string): number {
    const cols = this.getTableColumns();
    if (typeof path === 'number') {
      if (path < 0 || path >= cols.length) throw new Error(`no column at index ${path}`);
      return path;
    }
    const p = COLUMN_ALIASES[path] ?? path;
    const i = cols.findIndex((c) => c.path === p);
    if (i < 0) throw new Error(`the view has no column ${JSON.stringify(p)}`);
    return i;
  }

  private pin(): { left: string[]; right: string[] } {
    const pin = this.definition.pin ?? DEFAULT_PIN;
    return { left: [...(pin.left ?? [])], right: [...(pin.right ?? [])] };
  }

  pinColumnLeft(col: string): this {
    const pin = this.pin();
    pin.right = pin.right.filter((c) => c !== col);
    if (!pin.left.includes(col)) pin.left.push(col);
    this.definition.pin = pin;
    return this;
  }

  pinColumnRight(col: string): this {
    const pin = this.pin();
    pin.left = pin.left.filter((c) => c !== col);
    if (!pin.right.includes(col)) pin.right.push(col);
    this.definition.pin = pin;
    return this;
  }

  unpinColumn(col: string): this {
    const pin = this.pin();
    pin.left = pin.left.filter((c) => c !== col);
    pin.right = pin.right.filter((c) => c !== col);
    this.definition.pin = pin;
    return this;
  }

  /** `[{path, label}]` of the grid: explicit columns, else the defaults plus visible `cols`. */
  getTableColumns(): Array<{ path: string; label: string }> {
    if (this.definition.columns?.length) {
      return this.definition.columns.map((c: any) => ({ path: pathStr(c.path ?? []), label: c.label ?? pathStr(c.path ?? []) }));
    }
    const paths = ['id', 'summary.weave.trace_name', 'summary.weave.status', 'started_at'];
    for (const c of this.cols()) if (!paths.includes(c) && KNOWN_COLUMNS.includes(String(pathParts(c)[0]))) paths.push(c);
    const pin = this.pin();
    for (const p of pin.right) {
      const i = paths.indexOf(p);
      if (i >= 0) paths.push(...paths.splice(i, 1));
    }
    for (const p of [...pin.left].reverse()) {
      const i = paths.indexOf(p);
      if (i >= 0) paths.unshift(...paths.splice(i, 1));
    }
    const headers: Record<string, string> = { id: 'ID', 'summary.weave.trace_name': 'Trace', 'summary.weave.status': 'Status', started_at: 'Called' };
    return paths.map((p) => ({ path: p, label: headers[p] ?? p }));
  }

  /** Publish the view (named after the label, or `<viewType>_<timestamp>`). */
  async save(): Promise<this> {
    if (!this.name) {
      const base = this.label && this.label !== 'SavedView' ? this.label : null;
      this.name = base
        ? base.trim().replace(/[^A-Za-z0-9_.-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 128) || 'view'
        : `${this.viewType}_${new Date().toISOString().replace(/[:.]/g, '-')}`;
    }
    await requireClient().publish(this, this.name);
    return this;
  }

  /** Load a view by name, `name:version`, ref or URI. */
  static async load(ref: string | Ref): Promise<SavedView> {
    const obj = await requireClient().get(ref);
    if (obj instanceof SavedView) return obj;
    if (obj && typeof obj === 'object' && 'definition' in obj) {
      const v = new SavedView({ name: obj.name, viewType: obj.view_type, label: obj.label, definition: obj.definition ?? {} });
      v.__weftRef = obj.__weftRef;
      return v;
    }
    throw new Error(`${String(ref)} is not a SavedView`);
  }

  /** Link to the view's results in the UI (once saved). */
  uiUrl(): string | null {
    if (!this.__weftRef) return null;
    const base = getClient()?.serverUrl ?? '';
    const page = this.viewType === 'evaluations' ? 'evaluations' : 'traces';
    return `${base}/${this.__weftRef.entity}/${this.__weftRef.project}/${page}?view=${this.__weftRef.name}`;
  }

  /** The `/calls/query` body this view stands for. */
  toCallsQuery(opts: SavedViewGetCallsOptions = {}): Record<string, unknown> {
    const d = this.definition;
    const body: Record<string, unknown> = {};
    const p = this.projectRef;
    if (p) body.project_id = `${p.entity}/${p.project}`;
    const flt: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(d.filter ?? {})) if (CALLS_FILTER_KEYS.has(k) && v !== null && v !== undefined) flt[k] = v;
    if (this.viewType === 'evaluations') flt.op_names = ['Evaluation.evaluate'];
    if (Object.keys(flt).length) body.filter = flt;
    if (d.query) body.query = '$expr' in d.query ? d.query : { $expr: d.query };
    if (d.search) body.search = d.search;
    if (d.sort_by?.length) body.sort_by = [...d.sort_by];
    if (d.expand_columns?.length) body.expand_columns = [...d.expand_columns];
    if (!opts.allColumns && d.columns?.length) body.columns = this.getTableColumns().map((c) => c.path);
    if (opts.limit !== undefined) body.limit = opts.limit;
    if (opts.offset !== undefined) body.offset = opts.offset;
    if (opts.includeCosts) body.include_costs = true;
    if (opts.includeFeedback) body.include_feedback = true;
    return body;
  }

  /** Calls matching the view's filter, query, search and sort. */
  getCalls(opts: SavedViewGetCallsOptions = {}): Promise<Call[]> {
    const b = this.toCallsQuery(opts) as any;
    return requireClient().getCalls({
      filter: b.filter,
      query: b.query,
      sortBy: b.sort_by,
      limit: opts.limit,
      offset: opts.offset,
      search: b.search,
      includeCosts: opts.includeCosts,
      includeFeedback: opts.includeFeedback,
      columns: b.columns,
      expandColumns: b.expand_columns,
    });
  }
}
