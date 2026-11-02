/**
 * Redaction of sensitive values in call inputs/outputs. Values of dict keys
 * named like credentials are replaced with "REDACTED" (case-insensitive, exact
 * key match). Configure with `init(project, { settings: { redactKeys, redactPii } })`;
 * disable key redaction with WEFT_REDACT_KEYS=0.
 */
export const REDACTED = 'REDACTED';
export const DEFAULT_REDACT_KEYS = ['api_key', 'apikey', 'authorization', 'auth', 'password', 'secret', 'token', 'access_token'];

const PII: Array<[RegExp, string]> = [
  [/[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/g, '<EMAIL_ADDRESS>'],
  [/\b\d{3}-\d{2}-\d{4}\b/g, '<US_SSN>'],
  [/\b(?:\d[ -]?){13,16}\b/g, '<CREDIT_CARD>'],
  [/(?<![\w])(?:\+?\d{1,3}[ .-]?)?\(?\d{3}\)?[ .-]?\d{3}[ .-]?\d{4}\b/g, '<PHONE_NUMBER>'],
  [/\b(?:\d{1,3}\.){3}\d{1,3}\b/g, '<IP_ADDRESS>'],
];

export function redactPiiText(s: string): string {
  return PII.reduce((acc, [re, repl]) => acc.replace(re, repl), s);
}

export interface RedactSettings {
  /** Keys whose values are redacted (replaces the defaults). */
  redactKeys?: string[];
  /** Also replace emails, phone numbers, card numbers, SSNs and IPs inside strings. */
  redactPii?: boolean;
}

export class Redactor {
  readonly keys: Set<string>;
  readonly pii: boolean;

  constructor(settings: RedactSettings = {}) {
    const env = (process.env.WEFT_REDACT_KEYS ?? '1').trim().toLowerCase();
    const enabled = !['0', 'false', 'no', 'off'].includes(env);
    this.keys = new Set(enabled ? (settings.redactKeys ?? DEFAULT_REDACT_KEYS).map((k) => k.toLowerCase()) : []);
    this.pii = !!settings.redactPii;
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
    if (typeof v === 'string' && this.pii) return redactPiiText(v);
    return v;
  }
}
