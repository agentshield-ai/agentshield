import { WeftObject } from './objects.js';
import { Ref } from './refs.js';

function refUri(v: unknown): string {
  if (v instanceof Ref) return v.uri();
  if (v instanceof WeftObject) {
    if (!v.__weftRef) throw new Error(`${v.className} must be published before it can be referenced`);
    return v.__weftRef.uri();
  }
  return String(v);
}

/** Schema for human feedback stored as `wandb.annotation.<name>`. */
export class AnnotationSpec extends WeftObject {
  fieldSchema: Record<string, unknown>;
  uniqueAmongCreators: boolean;
  opScope: string[] | null;

  constructor(opts: { name?: string; description?: string | null; fieldSchema?: Record<string, unknown>; uniqueAmongCreators?: boolean; opScope?: string[] | null }) {
    super(opts);
    this.fieldSchema = opts.fieldSchema ?? { type: 'string' };
    this.uniqueAmongCreators = opts.uniqueAmongCreators ?? true;
    this.opScope = opts.opScope ?? null;
  }

  get className(): string {
    return 'AnnotationSpec';
  }

  fields(): Record<string, unknown> {
    return { field_schema: this.fieldSchema, unique_among_creators: this.uniqueAmongCreators, op_scope: this.opScope };
  }

  /** Validate a value against `fieldSchema` (type, enum, min/max, length). */
  valueIsValid(value: unknown): boolean {
    const s = this.fieldSchema as Record<string, any>;
    const t = s.type;
    const typeOk: Record<string, (v: unknown) => boolean> = {
      integer: (v) => Number.isInteger(v),
      number: (v) => typeof v === 'number',
      string: (v) => typeof v === 'string',
      boolean: (v) => typeof v === 'boolean',
      object: (v) => !!v && typeof v === 'object' && !Array.isArray(v),
      array: (v) => Array.isArray(v),
    };
    if (t && typeOk[t] && !typeOk[t](value)) return false;
    if (Array.isArray(s.enum) && !s.enum.includes(value)) return false;
    if (typeof value === 'number') {
      if (s.minimum !== undefined && value < s.minimum) return false;
      if (s.maximum !== undefined && value > s.maximum) return false;
    }
    if (typeof value === 'string') {
      if (s.maxLength !== undefined && value.length > s.maxLength) return false;
      if (s.minLength !== undefined && value.length < s.minLength) return false;
    }
    return true;
  }
}

export interface LeaderboardColumn {
  /** Evaluation object (published) or its ref/URI. */
  evaluationObjectRef: Ref | WeftObject | string;
  scorerName: string;
  summaryMetricPath: string;
  shouldMinimize?: boolean;
}

/** Compares models across evaluation metrics (computed by /leaderboard/compute). */
export class Leaderboard extends WeftObject {
  columns: LeaderboardColumn[];

  constructor(opts: { name?: string; description?: string | null; columns: LeaderboardColumn[] }) {
    super(opts);
    this.columns = opts.columns;
  }

  get className(): string {
    return 'Leaderboard';
  }

  fields(): Record<string, unknown> {
    return {
      columns: this.columns.map((c) => ({
        evaluation_object_ref: refUri(c.evaluationObjectRef),
        scorer_name: c.scorerName,
        summary_metric_path: c.summaryMetricPath,
        should_minimize: c.shouldMinimize ?? false,
      })),
    };
  }
}

/** Online evaluation: the server scores matching calls with server-runnable scorers. */
export class Monitor extends WeftObject {
  active: boolean;
  samplingRate: number;
  opNames: string[];
  query: Record<string, unknown> | null;
  scorers: unknown[];

  constructor(opts: { name?: string; description?: string | null; active?: boolean; samplingRate?: number; opNames?: string[]; query?: Record<string, unknown> | null; scorers?: unknown[] }) {
    super(opts);
    this.active = opts.active ?? true;
    this.samplingRate = opts.samplingRate ?? 1;
    this.opNames = opts.opNames ?? [];
    this.query = opts.query ? ('$expr' in opts.query ? opts.query : { $expr: opts.query }) : null;
    this.scorers = opts.scorers ?? [];
  }

  get className(): string {
    return 'Monitor';
  }

  fields(): Record<string, unknown> {
    return { active: this.active, sampling_rate: this.samplingRate, op_names: this.opNames, query: this.query, scorers: this.scorers };
  }
}

/** A saved traces/evaluations view. */
export class SavedView extends WeftObject {
  viewType: string;
  label: string | null;
  definition: Record<string, unknown>;

  constructor(opts: { name?: string; description?: string | null; viewType?: string; label?: string | null; definition?: Record<string, unknown> }) {
    super(opts);
    this.viewType = opts.viewType ?? 'traces';
    this.label = opts.label ?? null;
    this.definition = opts.definition ?? {};
  }

  get className(): string {
    return 'SavedView';
  }

  fields(): Record<string, unknown> {
    return { view_type: this.viewType, label: this.label, definition: this.definition };
  }
}

/** Rebuild platform objects loaded from the server (null for other values). */
export function loadPlatformObject(val: any, ref: Ref | undefined): WeftObject | null {
  const base = { name: val.name ?? undefined, description: val.description ?? null };
  let obj: WeftObject | null = null;
  switch (val._class_name) {
    case 'AnnotationSpec':
      obj = new AnnotationSpec({ ...base, fieldSchema: val.field_schema, uniqueAmongCreators: val.unique_among_creators, opScope: val.op_scope });
      break;
    case 'Leaderboard':
      obj = new Leaderboard({
        ...base,
        columns: (val.columns ?? []).map((c: any) => ({
          evaluationObjectRef: c.evaluation_object_ref,
          scorerName: c.scorer_name,
          summaryMetricPath: c.summary_metric_path,
          shouldMinimize: c.should_minimize,
        })),
      });
      break;
    case 'Monitor':
      obj = new Monitor({ ...base, active: val.active, samplingRate: val.sampling_rate, opNames: val.op_names, query: val.query, scorers: val.scorers });
      break;
    case 'SavedView':
      obj = new SavedView({ ...base, viewType: val.view_type, label: val.label, definition: val.definition });
      break;
  }
  if (obj) obj.__weftRef = ref;
  return obj;
}
