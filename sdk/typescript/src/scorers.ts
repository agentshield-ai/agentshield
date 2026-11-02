import { getCurrentCall } from './context.js';
import { autoSummarizeValues } from './summarize.js';
import { requireClient } from './global.js';
import { WeftObject } from './objects.js';
import { op, type Op } from './op.js';
import { isRefUri } from './refs.js';
import { recordOpenAIUsage } from './usage.js';

export interface ScoreArgs {
  modelOutput: unknown;
  datasetRow: Record<string, unknown>;
}

/**
 * Base class for scorer objects. Subclass and implement
 * `score({ modelOutput, datasetRow })`. `columnMap` maps the column names the
 * scorer expects to dataset column names.
 */
export abstract class Scorer extends WeftObject {
  columnMap: Record<string, string> | null;
  private _scoreOp?: Op<(args: ScoreArgs) => unknown>;

  constructor(opts: { name?: string; description?: string | null; columnMap?: Record<string, string> } = {}) {
    super(opts);
    this.columnMap = opts.columnMap ?? null;
    Object.defineProperty(this, '_scoreOp', { enumerable: false, writable: true, value: undefined });
  }

  bases(): string[] {
    return ['Scorer', 'Object', 'BaseModel'];
  }

  abstract score(args: ScoreArgs): unknown | Promise<unknown>;

  /** Scorer-specific fields (snake_case, as stored). */
  protected scorerFields(): Record<string, unknown> {
    return {};
  }

  fields(): Record<string, unknown> {
    return { column_map: this.columnMap, ...this.scorerFields() };
  }

  /** Name used for summary keys and feedback (`name`, else the class name). */
  get scorerName(): string {
    return this.name ?? this.className;
  }

  /** The traced `<Class>.score` op bound to this scorer. */
  get scoreOp(): Op<(args: ScoreArgs) => unknown> {
    if (!this._scoreOp) this._scoreOp = op(this, this.score, { name: `${this.className}.score` });
    return this._scoreOp;
  }

  /** Apply `columnMap` to a dataset row. */
  mapRow(row: Record<string, unknown>): Record<string, unknown> {
    if (!this.columnMap) return row;
    const out = { ...row };
    for (const [arg, col] of Object.entries(this.columnMap)) if (col in row) out[arg] = row[col];
    return out;
  }

  summarize(scores: unknown[]): Record<string, unknown> | null {
    return autoSummarizeValues(scores);
  }
}

function asText(v: unknown): string {
  if (typeof v === 'string') return v;
  if (v === null || v === undefined) return '';
  try {
    return JSON.stringify(v);
  } catch {
    return String(v);
  }
}

/** `output == row[targetColumn]` (strings compared after trimming). */
export class ExactMatchScorer extends Scorer {
  targetColumn: string;
  constructor(opts: { name?: string; targetColumn?: string; columnMap?: Record<string, string> } = {}) {
    super(opts);
    this.targetColumn = opts.targetColumn ?? 'expected';
  }
  get className(): string {
    return 'ExactMatchScorer';
  }
  protected scorerFields(): Record<string, unknown> {
    return { target_column: this.targetColumn };
  }
  score({ modelOutput, datasetRow }: ScoreArgs): boolean {
    const target = datasetRow[this.targetColumn];
    return modelOutput === target || asText(modelOutput).trim() === asText(target).trim();
  }
}

/** Whether the output contains `row[targetColumn]`. */
export class ContainsScorer extends Scorer {
  targetColumn: string;
  caseInsensitive: boolean;
  constructor(opts: { name?: string; targetColumn?: string; caseInsensitive?: boolean; columnMap?: Record<string, string> } = {}) {
    super(opts);
    this.targetColumn = opts.targetColumn ?? 'expected';
    this.caseInsensitive = opts.caseInsensitive ?? true;
  }
  get className(): string {
    return 'ContainsScorer';
  }
  protected scorerFields(): Record<string, unknown> {
    return { target_column: this.targetColumn, case_insensitive: this.caseInsensitive };
  }
  score({ modelOutput, datasetRow }: ScoreArgs): boolean {
    const t = asText(datasetRow[this.targetColumn]);
    const o = asText(modelOutput);
    return this.caseInsensitive ? o.toLowerCase().includes(t.toLowerCase()) : o.includes(t);
  }
}

/** Whether `pattern` matches anywhere in the output. */
export class RegexScorer extends Scorer {
  pattern: string;
  constructor(opts: { name?: string; pattern: string; columnMap?: Record<string, string> }) {
    super(opts);
    this.pattern = opts.pattern;
  }
  get className(): string {
    return 'RegexScorer';
  }
  protected scorerFields(): Record<string, unknown> {
    return { pattern: this.pattern };
  }
  score({ modelOutput }: ScoreArgs): boolean {
    return new RegExp(this.pattern).test(asText(modelOutput));
  }
}

/** Whether the output is valid JSON (objects and arrays count as valid). */
export class JSONValidScorer extends Scorer {
  get className(): string {
    return 'JSONValidScorer';
  }
  score({ modelOutput }: ScoreArgs): boolean {
    if (modelOutput !== null && typeof modelOutput === 'object') return true;
    if (typeof modelOutput !== 'string') return false;
    try {
      JSON.parse(modelOutput);
      return true;
    } catch {
      return false;
    }
  }
}

export function levenshtein(a: string, b: string): number {
  if (a === b) return 0;
  let prev = Array.from({ length: b.length + 1 }, (_, i) => i);
  for (let i = 1; i <= a.length; i++) {
    const cur = [i];
    for (let j = 1; j <= b.length; j++) cur.push(Math.min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (a[i - 1] === b[j - 1] ? 0 : 1)));
    prev = cur;
  }
  return prev[b.length];
}

/** Normalised Levenshtein similarity (0..1) between output and `row[targetColumn]`. */
export class LevenshteinScorer extends Scorer {
  targetColumn: string;
  constructor(opts: { name?: string; targetColumn?: string; columnMap?: Record<string, string> } = {}) {
    super(opts);
    this.targetColumn = opts.targetColumn ?? 'expected';
  }
  get className(): string {
    return 'LevenshteinScorer';
  }
  protected scorerFields(): Record<string, unknown> {
    return { target_column: this.targetColumn };
  }
  score({ modelOutput, datasetRow }: ScoreArgs): number {
    const a = asText(modelOutput);
    const b = asText(datasetRow[this.targetColumn]);
    const longest = Math.max(a.length, b.length);
    return longest === 0 ? 1 : 1 - levenshtein(a, b) / longest;
  }
}

function safeFormat(template: string, values: Record<string, unknown>): string {
  return template.replace(/\{\{|\}\}|\{([A-Za-z_][\w]*)\}/g, (m, key) => {
    if (m === '{{') return '{';
    if (m === '}}') return '}';
    return key in values ? asText(values[key]) : m;
  });
}

/**
 * An LLM judge. `scoringPrompt` placeholders: `{output}` and dataset columns.
 * Runs through the Weft server's /completions/create (track_llm_call: false),
 * so provider keys stay on the server; usage is recorded on the scorer call.
 */
export class LLMAsAJudgeScorer extends Scorer {
  model: string;
  scoringPrompt: string;
  responseFormat: string | null;
  constructor(opts: { name?: string; model?: string; scoringPrompt: string; responseFormat?: string | null; columnMap?: Record<string, string> }) {
    super(opts);
    this.model = opts.model ?? 'gpt-4o-mini';
    this.scoringPrompt = opts.scoringPrompt;
    this.responseFormat = opts.responseFormat === undefined ? 'json_object' : opts.responseFormat;
  }
  get className(): string {
    return 'LLMAsAJudgeScorer';
  }
  protected scorerFields(): Record<string, unknown> {
    return { model: this.model, scoring_prompt: this.scoringPrompt, response_format: this.responseFormat };
  }
  async score({ modelOutput, datasetRow }: ScoreArgs): Promise<unknown> {
    const client = requireClient();
    let modelId = this.model;
    if (isRefUri(modelId)) {
      const m = await client.get(modelId);
      modelId = m?.llm_model_id ?? modelId;
    }
    const prompt = safeFormat(this.scoringPrompt, { ...datasetRow, output: modelOutput });
    const req: Record<string, unknown> = { model: modelId, messages: [{ role: 'user', content: prompt }] };
    if (this.responseFormat && this.responseFormat !== 'text') req.response_format = { type: this.responseFormat };
    const resp = await client.completionsCreate(req, false);
    recordOpenAIUsage(getCurrentCall(), resp, modelId);
    const content: string = resp?.choices?.[0]?.message?.content ?? '';
    if (this.responseFormat !== 'json_object') return content;
    try {
      return JSON.parse(content);
    } catch {
      const m = /\{[\s\S]*\}/.exec(content);
      if (m) {
        try {
          return JSON.parse(m[0]);
        } catch {
          /* fall through */
        }
      }
      return { raw: content };
    }
  }
}
