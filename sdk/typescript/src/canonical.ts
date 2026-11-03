import { createHash } from 'node:crypto';

/**
 * Digests, computed exactly like the server:
 *  - bytes/files: URL-safe base64 of SHA-256 with '-'→'X', '_'→'Y', no padding
 *  - objects and table rows: that digest of Python-style `json.dumps(v, sort_keys=True)`
 *    (", " / ": " separators, keys by code point, \uXXXX escapes, number text as sent)
 *  - tables: hex SHA-256 of the concatenated row digests
 *
 * Numbers are written with JS formatting (JSON.stringify), which is the text
 * this SDK sends; the server keeps number text verbatim, so digests match.
 */

/** U+007F is escaped like CPython json.dumps (the server matches). */
const ESCAPE_DEL = true;

const SHORT: Record<string, string> = { '"': '\\"', '\\': '\\\\', '\n': '\\n', '\r': '\\r', '\t': '\\t', '\b': '\\b', '\f': '\\f' };

function hex4(n: number): string {
  return '\\u' + n.toString(16).padStart(4, '0');
}

function pyString(s: string): string {
  let out = '"';
  for (let i = 0; i < s.length; i++) {
    const ch = s[i];
    const c = s.charCodeAt(i);
    if (SHORT[ch]) out += SHORT[ch];
    else if ((c >= 0x20 && c <= 0x7e) || (c === 0x7f && !ESCAPE_DEL)) out += ch;
    else if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length && s.charCodeAt(i + 1) >= 0xdc00 && s.charCodeAt(i + 1) <= 0xdfff) {
      out += hex4(c) + hex4(s.charCodeAt(i + 1));
      i++;
    } else if (c >= 0xd800 && c <= 0xdfff) out += '\\ufffd'; // lone surrogate reaches the server as U+FFFD
    else out += hex4(c);
  }
  return out + '"';
}

/** Code point order (Python's sorted()). */
function cpCompare(a: string, b: string): number {
  const x = [...a];
  const y = [...b];
  for (let i = 0; i < x.length && i < y.length; i++) {
    const d = x[i].codePointAt(0)! - y[i].codePointAt(0)!;
    if (d) return d;
  }
  return x.length - y.length;
}

/** The text that gets hashed (Python json.dumps(v, sort_keys=True) layout). */
export function digestJson(v: unknown): string {
  if (v === null || v === undefined) return 'null';
  if (typeof v === 'string') return pyString(v);
  if (typeof v === 'number' || typeof v === 'boolean') return JSON.stringify(v) ?? 'null';
  if (Array.isArray(v)) return '[' + v.map((x) => digestJson(x)).join(', ') + ']';
  if (typeof v === 'object') {
    const obj = v as Record<string, unknown>;
    const keys = Object.keys(obj)
      .filter((k) => obj[k] !== undefined)
      .sort(cpCompare);
    return '{' + keys.map((k) => pyString(k) + ': ' + digestJson(obj[k])).join(', ') + '}';
  }
  return JSON.stringify(v) ?? 'null';
}

/** Backwards-compatible name. */
export const canonicalJson = digestJson;

/** Digest of raw bytes (also file digests). */
export function b64digest(data: Uint8Array | string): string {
  return createHash('sha256').update(data).digest('base64url').replace(/-/g, 'X').replace(/_/g, 'Y');
}

/** Digest of a JSON-safe value (objects, ops, table rows). */
export function digest(value: unknown): string {
  return b64digest(Buffer.from(digestJson(value), 'utf8'));
}

/** Digest of a table: hex SHA-256 of its concatenated row digests. */
export function tableDigest(rowDigests: string[]): string {
  const h = createHash('sha256');
  for (const d of rowDigests) h.update(d, 'utf8');
  return h.digest('hex');
}
