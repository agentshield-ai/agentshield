import { getCurrentCall } from './context.js';
import { autoSummarizeValues } from './summarize.js';
import { requireClient } from './global.js';
import { WeftObject } from './objects.js';
import { op, type Op } from './op.js';
import { WeftHTTPError } from './http.js';
import { isRefUri } from './refs.js';
import { toJson } from './serialize.js';
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
    if (!this._scoreOp) this._scoreOp = op(this, this.score, { name: `${this.className}.score`, kind: 'scorer' });
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
  score({ modelOutput, datasetRow }: ScoreArgs): Promise<unknown> {
    return runJudge(this.model, this.scoringPrompt, this.responseFormat, modelOutput, datasetRow);
  }
}

// ---------------------------------------------------------------------------
// LLM-judge presets (prompts identical to the Python SDK's)
export const HALLUCINATION_PROMPT = "You are verifying an AI response against its input and context.\n\n<input>\n{inputs}\n</input>\n\n<response>\n{output}\n</response>\n\nIs every factual claim in the response supported by the input/context? Reply with JSON: {{\"hallucination_free\": true|false, \"reasoning\": \"...\"}}";
export const SUMMARIZATION_PROMPT = "You are grading a summary of a source text.\n\n<source>\n{inputs}\n</source>\n\n<summary>\n{output}\n</summary>\n\nRate how faithful, complete and concise the summary is from 0 (useless) to 1 (excellent). Reply with JSON: {{\"summarization_score\": <number 0..1>, \"is_faithful\": true|false, \"reasoning\": \"...\"}}";
export const RELEVANCE_PROMPT = "You are judging whether a response is relevant to the request.\n\n<request>\n{inputs}\n</request>\n\n<response>\n{output}\n</response>\n\nReply with JSON: {{\"is_relevant\": true|false, \"relevance_score\": <number 0..1>, \"reasoning\": \"...\"}}";

async function runJudge(model: string, scoringPrompt: string, responseFormat: string | null, modelOutput: unknown, row: Record<string, unknown>): Promise<unknown> {
  const client = requireClient();
  let modelId = model;
  if (isRefUri(modelId)) {
    const m = await client.get(modelId);
    modelId = m?.llm_model_id ?? modelId;
  }
  const values: Record<string, unknown> = { ...row, inputs: row, output: modelOutput };
  const prompt = safeFormat(scoringPrompt, values);
  const req: Record<string, unknown> = { model: modelId, messages: [{ role: 'user', content: prompt }] };
  if (responseFormat && responseFormat !== 'text') req.response_format = { type: responseFormat };
  const resp = await client.completionsCreate(req, false);
  recordOpenAIUsage(getCurrentCall(), resp, modelId);
  const content: string = resp?.choices?.[0]?.message?.content ?? '';
  if (responseFormat !== 'json_object') return content;
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

abstract class JudgePreset extends Scorer {
  model: string;
  scoringPrompt: string;
  constructor(prompt: string, opts: { name?: string; model?: string; scoringPrompt?: string; columnMap?: Record<string, string> } = {}) {
    super(opts);
    this.model = opts.model ?? 'gpt-4o-mini';
    this.scoringPrompt = opts.scoringPrompt ?? prompt;
  }
  protected scorerFields(): Record<string, unknown> {
    return { model: this.model, scoring_prompt: this.scoringPrompt };
  }
  score({ modelOutput, datasetRow }: ScoreArgs): Promise<unknown> {
    return runJudge(this.model, this.scoringPrompt, 'json_object', modelOutput, datasetRow);
  }
}

/** LLM judge: is the output free of claims unsupported by the inputs? → `{hallucination_free, reasoning}`. */
export class HallucinationFreeScorer extends JudgePreset {
  constructor(opts: { name?: string; model?: string; scoringPrompt?: string; columnMap?: Record<string, string> } = {}) {
    super(HALLUCINATION_PROMPT, opts);
  }
  get className(): string {
    return 'HallucinationFreeScorer';
  }
}

/** LLM judge for summaries → `{summarization_score, is_faithful, reasoning}`. */
export class SummarizationScorer extends JudgePreset {
  constructor(opts: { name?: string; model?: string; scoringPrompt?: string; columnMap?: Record<string, string> } = {}) {
    super(SUMMARIZATION_PROMPT, opts);
  }
  get className(): string {
    return 'SummarizationScorer';
  }
}

/** LLM judge: is the output relevant to the inputs? → `{is_relevant, relevance_score, reasoning}`. */
export class RelevanceScorer extends JudgePreset {
  constructor(opts: { name?: string; model?: string; scoringPrompt?: string; columnMap?: Record<string, string> } = {}) {
    super(RELEVANCE_PROMPT, opts);
  }
  get className(): string {
    return 'RelevanceScorer';
  }
}

/** Minimal XML well-formedness check (tags, attributes, comments, CDATA, PIs, one root). */
export function isWellFormedXml(src: string): boolean {
  const s = src.trim();
  if (!s.startsWith('<')) return false;
  const stack: string[] = [];
  let roots = 0;
  let i = 0;
  const name = /^[A-Za-z_:][\w:.-]*/;
  while (i < s.length) {
    if (s[i] !== '<') {
      const next = s.indexOf('<', i);
      const text = s.slice(i, next < 0 ? s.length : next);
      if (!stack.length && text.trim()) return false;
      if (/&(?!(?:[A-Za-z]+|#\d+|#x[0-9a-fA-F]+);)/.test(text)) return false;
      if (next < 0) break;
      i = next;
      continue;
    }
    if (s.startsWith('<!--', i)) {
      const end = s.indexOf('-->', i + 4);
      if (end < 0) return false;
      i = end + 3;
    } else if (s.startsWith('<![CDATA[', i)) {
      const end = s.indexOf(']]>', i);
      if (end < 0 || !stack.length) return false;
      i = end + 3;
    } else if (s.startsWith('<?', i)) {
      const end = s.indexOf('?>', i);
      if (end < 0) return false;
      i = end + 2;
    } else if (s.startsWith('<!', i)) {
      const end = s.indexOf('>', i);
      if (end < 0 || stack.length) return false;
      i = end + 1;
    } else if (s.startsWith('</', i)) {
      const end = s.indexOf('>', i);
      if (end < 0) return false;
      const tag = s.slice(i + 2, end).trim();
      if (stack.pop() !== tag) return false;
      i = end + 1;
    } else {
      const end = s.indexOf('>', i);
      if (end < 0) return false;
      let body = s.slice(i + 1, end);
      const selfClosing = body.endsWith('/');
      if (selfClosing) body = body.slice(0, -1);
      const m = name.exec(body);
      if (!m) return false;
      const attrs = body.slice(m[0].length);
      if (!/^(\s+[A-Za-z_:][\w:.-]*\s*=\s*("[^"<]*"|'[^'<]*'))*\s*$/.test(attrs)) return false;
      if (!stack.length) roots++;
      if (roots > 1) return false;
      if (!selfClosing) stack.push(m[0]);
      i = end + 1;
    }
  }
  return roots === 1 && stack.length === 0;
}

/** Whether the output is well-formed XML. */
export class ValidXMLScorer extends Scorer {
  get className(): string {
    return 'ValidXMLScorer';
  }
  score({ modelOutput }: ScoreArgs): boolean {
    return typeof modelOutput === 'string' && isWellFormedXml(modelOutput);
  }
}

/** Minimal JSON Schema validation (same rules as the Python SDK). */
export function schemaErrors(value: unknown, schema: Record<string, any>, path = '$'): string[] {
  const errs: string[] = [];
  const types: Record<string, (v: unknown) => boolean> = {
    object: (v) => !!v && typeof v === 'object' && !Array.isArray(v),
    array: (v) => Array.isArray(v),
    string: (v) => typeof v === 'string',
    integer: (v) => Number.isInteger(v),
    number: (v) => typeof v === 'number',
    boolean: (v) => typeof v === 'boolean',
    null: (v) => v === null,
  };
  if (schema.type !== undefined) {
    const allowed: string[] = Array.isArray(schema.type) ? schema.type : [schema.type];
    if (!allowed.some((t) => (types[t] ?? (() => true))(value))) return [`${path}: expected ${allowed.join('|')}`];
  }
  if (Array.isArray(schema.enum) && !schema.enum.some((e: unknown) => JSON.stringify(e) === JSON.stringify(value))) errs.push(`${path}: not in enum`);
  if ('const' in schema && JSON.stringify(schema.const) !== JSON.stringify(value)) errs.push(`${path}: const mismatch`);
  if (types.object(value)) {
    const obj = value as Record<string, unknown>;
    const props = schema.properties ?? {};
    for (const r of schema.required ?? []) if (!(r in obj)) errs.push(`${path}: missing ${r}`);
    for (const [k, v] of Object.entries(obj)) {
      if (k in props) errs.push(...schemaErrors(v, props[k], `${path}.${k}`));
      else if (schema.additionalProperties === false) errs.push(`${path}: unexpected property ${k}`);
      else if (schema.additionalProperties && typeof schema.additionalProperties === 'object') errs.push(...schemaErrors(v, schema.additionalProperties, `${path}.${k}`));
    }
  }
  if (Array.isArray(value)) {
    if (schema.items && typeof schema.items === 'object') value.forEach((v, i) => errs.push(...schemaErrors(v, schema.items, `${path}[${i}]`)));
    if (schema.minItems !== undefined && value.length < schema.minItems) errs.push(`${path}: too few items`);
    if (schema.maxItems !== undefined && value.length > schema.maxItems) errs.push(`${path}: too many items`);
  }
  if (typeof value === 'string') {
    if (schema.minLength !== undefined && value.length < schema.minLength) errs.push(`${path}: too short`);
    if (schema.maxLength !== undefined && value.length > schema.maxLength) errs.push(`${path}: too long`);
    if (schema.pattern !== undefined && !new RegExp(schema.pattern).test(value)) errs.push(`${path}: pattern mismatch`);
  }
  if (typeof value === 'number') {
    if (schema.minimum !== undefined && value < schema.minimum) errs.push(`${path}: below minimum`);
    if (schema.maximum !== undefined && value > schema.maximum) errs.push(`${path}: above maximum`);
    if (schema.exclusiveMinimum !== undefined && value <= schema.exclusiveMinimum) errs.push(`${path}: not above exclusiveMinimum`);
    if (schema.exclusiveMaximum !== undefined && value >= schema.exclusiveMaximum) errs.push(`${path}: not below exclusiveMaximum`);
  }
  for (const sub of schema.allOf ?? []) errs.push(...schemaErrors(value, sub, path));
  if (schema.anyOf && !schema.anyOf.some((sub: any) => !schemaErrors(value, sub, path).length)) errs.push(`${path}: matches none of anyOf`);
  if (schema.oneOf && schema.oneOf.filter((sub: any) => !schemaErrors(value, sub, path).length).length !== 1) errs.push(`${path}: must match exactly one of oneOf`);
  return errs;
}

/** Whether the output (a JSON string or value) validates against `schema`. */
export class JSONSchemaScorer extends Scorer {
  schema: Record<string, unknown>;
  constructor(opts: { name?: string; schema: Record<string, unknown>; columnMap?: Record<string, string> }) {
    super(opts);
    this.schema = opts.schema;
  }
  get className(): string {
    return 'JSONSchemaScorer';
  }
  protected scorerFields(): Record<string, unknown> {
    return { schema: this.schema };
  }
  score({ modelOutput }: ScoreArgs): boolean {
    let v = modelOutput;
    if (typeof v === 'string') {
      try {
        v = JSON.parse(v);
      } catch {
        return false;
      }
    }
    return schemaErrors(v, this.schema).length === 0;
  }
}

/** OpenAI moderation → `{flagged, categories}`. Locally needs OPENAI_API_KEY; the server runs it with its own key. */
export class OpenAIModerationScorer extends Scorer {
  get className(): string {
    return 'OpenAIModerationScorer';
  }
  async score({ modelOutput }: ScoreArgs): Promise<unknown> {
    const key = process.env.OPENAI_API_KEY;
    if (!key) throw new Error('OpenAIModerationScorer runs locally only with OPENAI_API_KEY set; publish it and let the server run it');
    const res = await fetch('https://api.openai.com/v1/moderations', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${key}` },
      body: JSON.stringify({ input: typeof modelOutput === 'string' ? modelOutput : JSON.stringify(modelOutput) }),
    });
    const r = (await res.json()).results?.[0] ?? {};
    const cats = Object.fromEntries(Object.entries(r.categories ?? {}).filter(([, v]) => v));
    return { flagged: !!r.flagged, categories: cats };
  }
}

/** Cosine similarity of output and `row[targetColumn]` embeddings (needs the server's /embeddings/create to run locally). */
export class EmbeddingSimilarityScorer extends Scorer {
  targetColumn: string;
  model: string;
  constructor(opts: { name?: string; targetColumn?: string; model?: string; columnMap?: Record<string, string> } = {}) {
    super(opts);
    this.targetColumn = opts.targetColumn ?? 'expected';
    this.model = opts.model ?? 'text-embedding-3-small';
  }
  get className(): string {
    return 'EmbeddingSimilarityScorer';
  }
  protected scorerFields(): Record<string, unknown> {
    return { target_column: this.targetColumn, model: this.model };
  }
  async score({ modelOutput, datasetRow }: ScoreArgs): Promise<number> {
    const client = requireClient();
    const texts = [asText(modelOutput), asText(datasetRow[this.targetColumn])];
    let res: any;
    try {
      res = await client.http.post('/embeddings/create', { project_id: client.projectId, inputs: { model: this.model, input: texts } });
    } catch (e) {
      if (e instanceof WeftHTTPError && [404, 405, 501].includes(e.status)) {
        throw new Error('EmbeddingSimilarityScorer cannot run locally: this Weft server has no /embeddings/create endpoint. Publish the scorer and run it server side.');
      }
      throw e;
    }
    const data = (res.response ?? res).data ?? [];
    const [a, b] = [data[0].embedding as number[], data[1].embedding as number[]];
    const dot = a.reduce((s, x, i) => s + x * b[i], 0);
    const na = Math.sqrt(a.reduce((s, x) => s + x * x, 0));
    const nb = Math.sqrt(b.reduce((s, x) => s + x * x, 0));
    return na && nb ? dot / (na * nb) : 0;
  }
}

function getPath(doc: unknown, path: string): unknown {
  let cur: any = doc;
  for (const part of path.split('.')) {
    if (cur && typeof cur === 'object') cur = Array.isArray(cur) ? cur[Number(part)] : cur[part];
    else return null;
    if (cur === undefined) return null;
  }
  return cur;
}

/** Evaluate a Weft query-language operation (API.md) against a document. */
export function evaluateExpr(expr: any, doc: Record<string, unknown>): any {
  if (expr && typeof expr === 'object' && !Array.isArray(expr) && Object.keys(expr).length === 1 && '$expr' in expr) expr = expr.$expr;
  if (!expr || typeof expr !== 'object' || Array.isArray(expr) || Object.keys(expr).length !== 1) return expr;
  const [op, arg] = Object.entries(expr)[0] as [string, any];
  const ev = (x: any) => evaluateExpr(x, doc);
  switch (op) {
    case '$literal':
      return arg;
    case '$getField':
      return getPath(doc, arg);
    case '$and':
      return arg.every((x: any) => !!ev(x));
    case '$or':
      return arg.some((x: any) => !!ev(x));
    case '$not':
      return !ev(Array.isArray(arg) ? arg[0] : arg);
    case '$eq':
      return JSON.stringify(ev(arg[0])) === JSON.stringify(ev(arg[1]));
    case '$gt':
    case '$gte':
    case '$lt':
    case '$lte': {
      const a = ev(arg[0]);
      const b = ev(arg[1]);
      if (a === null || b === null || a === undefined || b === undefined) return false;
      return op === '$gt' ? a > b : op === '$gte' ? a >= b : op === '$lt' ? a < b : a <= b;
    }
    case '$in': {
      const v = JSON.stringify(ev(arg[0]));
      return (arg[1] as any[]).some((x) => JSON.stringify(ev(x)) === v);
    }
    case '$contains': {
      const hay = String(ev(arg.input) ?? '');
      const needle = String(ev(arg.substr) ?? '');
      return arg.case_insensitive ? hay.toLowerCase().includes(needle.toLowerCase()) : hay.includes(needle);
    }
    case '$convert': {
      const v = ev(arg.input);
      switch (arg.to) {
        case 'double':
          return v === null || v === undefined || Number.isNaN(Number(v)) ? null : Number(v);
        case 'int':
          return v === null || v === undefined || Number.isNaN(Number(v)) ? null : Math.trunc(Number(v));
        case 'string':
          return v === null || v === undefined ? '' : typeof v === 'string' ? v : String(v);
        case 'bool':
          return !!v;
        case 'exists':
          return v !== null && v !== undefined;
      }
      throw new Error(`unknown $convert target ${arg.to}`);
    }
  }
  throw new Error(`unsupported operator ${op}`);
}

/** A query-language rule evaluated on `{output, inputs, <columns>}`; with `threshold`, numbers score `value >= threshold`. */
export class RuleScorer extends Scorer {
  rule: Record<string, unknown>;
  threshold: number | null;
  constructor(opts: { name?: string; rule: Record<string, unknown>; threshold?: number | null; columnMap?: Record<string, string> }) {
    super(opts);
    this.rule = opts.rule;
    this.threshold = opts.threshold ?? null;
  }
  get className(): string {
    return 'RuleScorer';
  }
  protected scorerFields(): Record<string, unknown> {
    return { rule: this.rule, threshold: this.threshold };
  }
  score({ modelOutput, datasetRow }: ScoreArgs): unknown {
    const v = evaluateExpr(this.rule, { ...datasetRow, inputs: datasetRow, output: modelOutput });
    return this.threshold !== null && typeof v === 'number' ? v >= this.threshold : v;
  }
}

/**
 * Score with an HTTP endpoint: POSTs `{output, inputs}` JSON to `url` and returns the JSON response.
 * `headersSecret` names a secret with extra headers (JSON object, or a bare bearer token); locally it
 * is read from the environment variable of that name.
 */
export class RemoteScorer extends Scorer {
  url: string;
  headersSecret: string | null;
  timeout: number;
  constructor(opts: { name?: string; url: string; headersSecret?: string | null; timeout?: number; columnMap?: Record<string, string> }) {
    super(opts);
    this.url = opts.url;
    this.headersSecret = opts.headersSecret ?? null;
    this.timeout = opts.timeout ?? 30;
  }
  get className(): string {
    return 'RemoteScorer';
  }
  protected scorerFields(): Record<string, unknown> {
    return { url: this.url, headers_secret: this.headersSecret, timeout: this.timeout };
  }
  async score({ modelOutput, datasetRow }: ScoreArgs): Promise<unknown> {
    const headers: Record<string, string> = { 'Content-Type': 'application/json', Accept: 'application/json' };
    if (this.headersSecret) {
      const raw = process.env[this.headersSecret];
      if (raw === undefined) throw new Error(`RemoteScorer: environment variable ${this.headersSecret} (headers_secret) is not set`);
      try {
        const extra = JSON.parse(raw);
        if (!extra || typeof extra !== 'object' || Array.isArray(extra)) throw new Error();
        for (const [k, v] of Object.entries(extra)) headers[k] = String(v);
      } catch {
        headers.Authorization = `Bearer ${raw}`;
      }
    }
    const res = await fetch(this.url, {
      method: 'POST',
      headers,
      body: JSON.stringify({ output: toJson(modelOutput), inputs: toJson(datasetRow) }),
      signal: AbortSignal.timeout(this.timeout * 1000),
    });
    const text = await res.text();
    try {
      return JSON.parse(text);
    } catch {
      return text;
    }
  }
}
