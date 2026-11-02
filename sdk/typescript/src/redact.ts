/**
 * Redaction of sensitive values in call inputs/outputs. Values of dict keys
 * named like credentials are replaced with "REDACTED" (case-insensitive, exact
 * key match). Configure with `init(project, { settings: { redactKeys, redactPii } })`;
 * disable key redaction with WEFT_REDACT_KEYS=0.
 *
 * PII redaction (`redactPii`, env WEFT_REDACT_PII) replaces entities inside
 * strings with `<ENTITY_TYPE>`. Regular expressions always handle e-mail
 * addresses, US SSNs, credit cards, phone numbers and IP addresses (applied
 * when the call is recorded). Names, locations, organisations and the other
 * NER entities need an analyzer, run in the background sender before a call
 * leaves the process:
 *
 * - `redactPiiAnalyzer`: your function `(text, entities) => spans` (sync or
 *   async), spans being `{ entity_type | entityType, start, end, score? }` or
 *   `[entity, start, end, score?]` (a JS NER model, a cloud DLP API, ...);
 * - `redactPiiEngine: 'presidio'` with `redactPiiPresidioUrl` (env
 *   WEFT_REDACT_PII_ENGINE / WEFT_PRESIDIO_ANALYZER_URL): a Presidio Analyzer
 *   REST service (`POST <url>/analyze`), e.g. the
 *   `mcr.microsoft.com/presidio-analyzer` container.
 *
 * `redactPiiFields` / `redactPiiExcludeFields` (env WEFT_REDACT_PII_FIELDS /
 * WEFT_REDACT_PII_EXCLUDE_FIELDS, comma-separated) choose the entities
 * (default `DEFAULT_PII_ENTITIES`, the same list as the Python SDK);
 * `redactPiiScoreThreshold` drops low-confidence spans. If the analyzer
 * fails, a warning is logged and the regex redaction stands.
 */
import { logger } from './log.js';

export const REDACTED = 'REDACTED';
export const DEFAULT_REDACT_KEYS = ['api_key', 'apikey', 'authorization', 'auth', 'password', 'secret', 'token', 'access_token'];

const PII: Array<[RegExp, string]> = [
  [/[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/g, '<EMAIL_ADDRESS>'],
  [/\b\d{3}-\d{2}-\d{4}\b/g, '<US_SSN>'],
  [/\b(?:\d[ -]?){13,16}\b/g, '<CREDIT_CARD>'],
  [/(?<![\w])(?:\+?\d{1,3}[ .-]?)?\(?\d{3}\)?[ .-]?\d{3}[ .-]?\d{4}\b/g, '<PHONE_NUMBER>'],
  [/\b(?:\d{1,3}\.){3}\d{1,3}\b/g, '<IP_ADDRESS>'],
];

/** Entities redacted by default when `redactPii` is on (Weave's defaults plus ORGANIZATION). */
export const DEFAULT_PII_ENTITIES = [
  'CREDIT_CARD',
  'CRYPTO',
  'EMAIL_ADDRESS',
  'IBAN_CODE',
  'IP_ADDRESS',
  'LOCATION',
  'PERSON',
  'ORGANIZATION',
  'PHONE_NUMBER',
  'US_SSN',
  'US_BANK_NUMBER',
  'US_DRIVER_LICENSE',
  'US_PASSPORT',
  'UK_NHS',
  'UK_NINO',
  'ES_NIF',
  'IN_AADHAAR',
  'IN_PAN',
  'FI_PERSONAL_IDENTITY_CODE',
];

const REGEX_ENTITIES = new Set(['EMAIL_ADDRESS', 'US_SSN', 'CREDIT_CARD', 'PHONE_NUMBER', 'IP_ADDRESS']);

/** Regex redaction (e-mail, SSN, credit card, phone, IP); `entities` limits which. */
export function redactPiiText(s: string, entities?: Iterable<string>): string {
  const allowed = entities ? new Set(entities) : null;
  return PII.reduce((acc, [re, repl]) => (allowed && !allowed.has(repl.slice(1, -1)) ? acc : acc.replace(re, repl)), s);
}

/** One entity found by an analyzer. */
export type PiiSpan =
  | { entity_type?: string; entityType?: string; entity?: string; start: number; end: number; score?: number }
  | [string, number, number, number?];

/** A NER analyzer: the spans of `entities` in `text`. */
export type PiiAnalyzer = (text: string, entities: string[]) => PiiSpan[] | Promise<PiiSpan[]>;

/**
 * An analyzer backed by a Presidio Analyzer REST service (`POST <url>/analyze`
 * with `{ text, language, entities, score_threshold }`).
 */
export function presidioAnalyzer(url: string, opts: { language?: string; scoreThreshold?: number; timeoutMs?: number; headers?: Record<string, string> } = {}): PiiAnalyzer {
  const endpoint = url.replace(/\/+$/, '').replace(/\/analyze$/, '') + '/analyze';
  return async (text, entities) => {
    const body: Record<string, unknown> = { text, language: opts.language ?? 'en', entities };
    if (opts.scoreThreshold !== undefined) body.score_threshold = opts.scoreThreshold;
    const res = await fetch(endpoint, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...(opts.headers ?? {}) },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(opts.timeoutMs ?? 10_000),
    });
    if (!res.ok) throw new Error(`presidio analyzer returned HTTP ${res.status}: ${(await res.text().catch(() => '')).slice(0, 200)}`);
    const out = await res.json();
    if (!Array.isArray(out)) throw new Error('presidio analyzer returned a non-list response');
    return out as PiiSpan[];
  };
}

/** Replace the spans in `text` with `<ENTITY>` (overlaps: the longest, then most confident, wins). */
export function applyPiiSpans(text: string, spans: PiiSpan[], entities?: Iterable<string>, scoreThreshold = 0): string {
  const allowed = entities ? new Set(entities) : null;
  const norm: Array<[string, number, number, number]> = [];
  for (const sp of spans ?? []) {
    let ent: string;
    let start: number;
    let end: number;
    let score: number;
    if (Array.isArray(sp)) [ent, start, end, score = 1] = sp as [string, number, number, number?] as [string, number, number, number];
    else {
      ent = String(sp.entity_type ?? sp.entityType ?? sp.entity ?? '');
      start = Number(sp.start);
      end = Number(sp.end);
      score = sp.score === undefined ? 1 : Number(sp.score);
    }
    ent = ent.toUpperCase();
    if (!ent || !(end > start) || start < 0 || end > text.length || score < scoreThreshold) continue;
    if (allowed && !allowed.has(ent)) continue;
    norm.push([ent, start, end, score]);
  }
  if (!norm.length) return text;
  norm.sort((a, b) => a[1] - b[1] || b[2] - b[1] - (a[2] - a[1]) || b[3] - a[3]);
  const chosen: typeof norm = [];
  for (const sp of norm) {
    if (chosen.length && sp[1] < chosen[chosen.length - 1][2]) continue;
    chosen.push(sp);
  }
  let out = text;
  for (const [ent, start, end] of chosen.reverse()) out = out.slice(0, start) + `<${ent}>` + out.slice(end);
  return out;
}

export interface RedactSettings {
  /** Keys whose values are redacted (replaces the defaults). */
  redactKeys?: string[];
  /** Replace PII inside strings: regex entities always, NER entities with an analyzer (env WEFT_REDACT_PII). */
  redactPii?: boolean;
  /** Entity types to redact (default DEFAULT_PII_ENTITIES; env WEFT_REDACT_PII_FIELDS). */
  redactPiiFields?: string[];
  /** Entity types to leave alone (env WEFT_REDACT_PII_EXCLUDE_FIELDS). */
  redactPiiExcludeFields?: string[];
  /** `regex` (default without an analyzer) or `presidio` (env WEFT_REDACT_PII_ENGINE). */
  redactPiiEngine?: 'regex' | 'presidio' | 'auto';
  /** Presidio Analyzer base URL for `redactPiiEngine: 'presidio'` (env WEFT_PRESIDIO_ANALYZER_URL). */
  redactPiiPresidioUrl?: string;
  /** A custom NER analyzer (takes precedence over the engine). */
  redactPiiAnalyzer?: PiiAnalyzer;
  /** Drop NER spans scored below this. */
  redactPiiScoreThreshold?: number;
  /** Language passed to the analyzer (default "en"). */
  redactPiiLanguage?: string;
}

function envList(name: string): string[] | undefined {
  const raw = process.env[name];
  if (!raw) return undefined;
  return raw.split(',').map((x) => x.trim().toUpperCase()).filter(Boolean);
}

function envTrue(name: string): boolean {
  return ['1', 'true', 'yes', 'on'].includes((process.env[name] ?? '').trim().toLowerCase());
}

export class Redactor {
  readonly keys: Set<string>;
  readonly pii: boolean;
  readonly piiEntities: string[];
  /** The NER analyzer, when one is configured and `redactPii` is on. */
  readonly ner: PiiAnalyzer | null;
  private readonly nerEntities: string[];
  private readonly scoreThreshold: number;
  private nerFailed = false;

  constructor(settings: RedactSettings = {}) {
    const env = (process.env.WEFT_REDACT_KEYS ?? '1').trim().toLowerCase();
    const enabled = !['0', 'false', 'no', 'off'].includes(env);
    this.keys = new Set(enabled ? (settings.redactKeys ?? DEFAULT_REDACT_KEYS).map((k) => k.toLowerCase()) : []);
    this.pii = settings.redactPii ?? envTrue('WEFT_REDACT_PII');
    const fields = (settings.redactPiiFields ?? envList('WEFT_REDACT_PII_FIELDS') ?? DEFAULT_PII_ENTITIES).map((e) => e.toUpperCase());
    const exclude = new Set((settings.redactPiiExcludeFields ?? envList('WEFT_REDACT_PII_EXCLUDE_FIELDS') ?? []).map((e) => e.toUpperCase()));
    this.piiEntities = fields.filter((e) => !exclude.has(e));
    this.scoreThreshold = settings.redactPiiScoreThreshold ?? 0;
    let ner: PiiAnalyzer | null = null;
    if (this.pii) {
      const engine = (settings.redactPiiEngine ?? process.env.WEFT_REDACT_PII_ENGINE ?? 'auto').trim().toLowerCase();
      const url = settings.redactPiiPresidioUrl ?? process.env.WEFT_PRESIDIO_ANALYZER_URL ?? process.env.WEFT_REDACT_PII_PRESIDIO_URL;
      if (settings.redactPiiAnalyzer) ner = settings.redactPiiAnalyzer;
      else if (engine === 'presidio' || (engine === 'auto' && url)) {
        if (!url) throw new Error("redactPiiEngine 'presidio' needs redactPiiPresidioUrl (or WEFT_PRESIDIO_ANALYZER_URL)");
        ner = presidioAnalyzer(url, { language: settings.redactPiiLanguage, scoreThreshold: settings.redactPiiScoreThreshold });
      } else if (!['auto', 'regex'].includes(engine)) {
        throw new Error(`unknown redactPiiEngine ${JSON.stringify(engine)} (regex, presidio, auto)`);
      }
    }
    // A custom analyzer gets every entity; Presidio only those regex does not cover.
    this.nerEntities = settings.redactPiiAnalyzer ? this.piiEntities : this.piiEntities.filter((e) => !REGEX_ENTITIES.has(e));
    this.ner = ner && this.nerEntities.length ? ner : null;
  }

  /** NER-redact one string (regex redaction already applied or applied after). */
  async redactTextAsync(text: string): Promise<string> {
    let out = text;
    if (this.ner && !this.nerFailed && out.length > 2 && /\p{L}/u.test(out)) {
      try {
        out = applyPiiSpans(out, await this.ner(out, this.nerEntities), this.piiEntities, this.scoreThreshold);
      } catch (e) {
        this.nerFailed = true;
        logger.warn(`weft: NER PII redaction failed (${(e as Error).message}); falling back to regular expressions`);
      }
    }
    return redactPiiText(out, this.piiEntities);
  }

  /** Apply NER to every string of a JSON value (async; a new value). */
  async applyAsync(v: unknown, depth = 0, cache: Map<string, Promise<string>> = new Map()): Promise<unknown> {
    if (!this.pii || depth > 64) return v;
    if (Array.isArray(v)) return Promise.all(v.map((x) => this.applyAsync(x, depth + 1, cache)));
    if (v && typeof v === 'object') {
      const out: Record<string, unknown> = {};
      const entries = Object.entries(v);
      const vals = await Promise.all(entries.map(([k, x]) => (this.keys.has(k.toLowerCase()) ? REDACTED : this.applyAsync(x, depth + 1, cache))));
      entries.forEach(([k], i) => (out[k] = vals[i]));
      return out;
    }
    if (typeof v === 'string') {
      let p = cache.get(v);
      if (!p) {
        p = this.redactTextAsync(v);
        cache.set(v, p);
      }
      return p;
    }
    return v;
  }

  /** NER-redact the inputs/outputs of queued call start/end items (the sender's `prepare` step). */
  async redactItems(items: Array<{ kind: string; payload?: unknown }>): Promise<void> {
    const cache = new Map<string, Promise<string>>();
    await Promise.all(
      items.map(async (it) => {
        const p = it.payload as Record<string, unknown> | undefined;
        if (!p || (it.kind !== 'call_start' && it.kind !== 'call_end')) return;
        if (it.kind === 'call_start' && p.inputs && typeof p.inputs === 'object') {
          const { self, ...rest } = p.inputs as Record<string, unknown>;
          const red = (await this.applyAsync(rest, 0, cache)) as Record<string, unknown>;
          p.inputs = 'self' in (p.inputs as object) ? { self, ...red } : red;
        }
        if (it.kind === 'call_end' && p.output !== undefined) p.output = await this.applyAsync(p.output, 0, cache);
      }),
    );
  }

  get active(): boolean {
    return this.keys.size > 0 || this.pii;
  }

  apply(v: unknown, depth = 0): unknown {
    if (!this.active || depth > 64) return v;
    if (Array.isArray(v)) return v.map((x) => this.apply(x, depth + 1));
    if (v && typeof v === 'object') {
      const out: Record<string, unknown> = {};
      for (const [k, x] of Object.entries(v)) out[k] = this.keys.has(k.toLowerCase()) ? REDACTED : this.apply(x, depth + 1);
      return out;
    }
    if (typeof v === 'string' && this.pii) return redactPiiText(v, this.piiEntities);
    return v;
  }
}
