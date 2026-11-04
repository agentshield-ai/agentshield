import { createHash } from 'node:crypto';

/**
 * Weft digests (docs/weft/DESIGN.md §10.3), computed exactly like the server:
 *  - objects and table rows: the lowercase hex SHA-256 of the value's canonical
 *    JSON: keys sorted by code point, no insignificant whitespace (`,` and `:`),
 *    strings as UTF-8 escaping only `"`, `\` and U+0000-U+001F (`\b \f \n \r \t`,
 *    otherwise `\u00xx`), numbers with the text this SDK sends (JSON.stringify),
 *    `true`, `false`, `null`;
 *  - tables: the digest of the JSON list of their row digests;
 *  - files: the lowercase hex SHA-256 of their bytes.
 *
 * The server keeps number text verbatim and hashes the same canonical form,
 * so local digests equal server digests (test/digest.test.ts checks the
 * shared vectors in sdk/testdata and, with WEFT_TEST_SERVER_URL, a live server).
 */

const SHORT: Record<string, string> = { '"': '\\"', '\\': '\\\\', '\n': '\\n', '\r': '\\r', '\t': '\\t', '\b': '\\b', '\f': '\\f' };

function jsonString(s: string): string {
  let out = '"';
  for (let i = 0; i < s.length; i++) {
    const ch = s[i];
    const c = s.charCodeAt(i);
    if (SHORT[ch]) out += SHORT[ch];
    else if (c < 0x20) out += '\\u' + c.toString(16).padStart(4, '0');
    else if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length && s.charCodeAt(i + 1) >= 0xdc00 && s.charCodeAt(i + 1) <= 0xdfff) {
      out += ch + s[i + 1];
      i++;
    } else if (c >= 0xd800 && c <= 0xdfff) out += '�'; // a lone surrogate reaches the server as U+FFFD
    else out += ch;
  }
  return out + '"';
}

/** Code point order (the byte order of UTF-8), not UTF-16 code unit order. */
function cpCompare(a: string, b: string): number {
  const x = [...a];
  const y = [...b];
  for (let i = 0; i < x.length && i < y.length; i++) {
    const d = x[i].codePointAt(0)! - y[i].codePointAt(0)!;
    if (d) return d;
  }
  return x.length - y.length;
}

/** Weft's canonical JSON of a value: the text its digest hashes. */
export function canonicalJson(v: unknown): string {
  if (v === null || v === undefined) return 'null';
  if (typeof v === 'string') return jsonString(v);
  if (typeof v === 'number' || typeof v === 'boolean') return JSON.stringify(v) ?? 'null';
  if (Array.isArray(v)) return '[' + v.map((x) => canonicalJson(x)).join(',') + ']';
  if (typeof v === 'object') {
    const obj = v as Record<string, unknown>;
    const keys = Object.keys(obj)
      .filter((k) => obj[k] !== undefined)
      .sort(cpCompare);
    return '{' + keys.map((k) => jsonString(k) + ':' + canonicalJson(obj[k])).join(',') + '}';
  }
  return JSON.stringify(v) ?? 'null';
}

/** Earlier name of canonicalJson. */
export const digestJson = canonicalJson;

/** Digest of file content: the hex SHA-256 of the bytes. */
export function fileDigest(data: Uint8Array | string): string {
  return createHash('sha256').update(data).digest('hex');
}

/** Digest of a JSON-safe value (objects, functions, table rows). */
export function digest(value: unknown): string {
  return fileDigest(Buffer.from(canonicalJson(value), 'utf8'));
}

/** Digest of a table: the digest of the JSON list of its row digests. */
export function tableDigest(rowDigests: string[]): string {
  return digest(rowDigests);
}
