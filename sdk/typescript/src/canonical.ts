import { createHash } from 'node:crypto';

const LS = new RegExp(String.fromCharCode(0x2028), 'g');
const PS = new RegExp(String.fromCharCode(0x2029), 'g');

/**
 * Canonical JSON as computed by the Weft server: keys sorted (UTF-8 byte
 * order), no whitespace, no HTML escaping, numbers in the text form the client
 * sent. Since the SDK sends JSON.stringify output, digests computed locally
 * match the server's.
 */
export function canonicalJson(value: unknown): string {
  return encode(value)
    .replace(LS, '\\u2028')
    .replace(PS, '\\u2029')
    .replace(/\\ud[89a-f][0-9a-f]{2}/g, '\ufffd');
}

function encode(v: unknown): string {
  if (v === null || v === undefined) return 'null';
  if (Array.isArray(v)) return '[' + v.map((x) => (x === undefined ? 'null' : encode(x))).join(',') + ']';
  if (typeof v === 'object') {
    const keys = Object.keys(v as object)
      .filter((k) => (v as Record<string, unknown>)[k] !== undefined)
      .sort((a, b) => Buffer.compare(Buffer.from(a, 'utf8'), Buffer.from(b, 'utf8')));
    return '{' + keys.map((k) => JSON.stringify(k) + ':' + encode((v as Record<string, unknown>)[k])).join(',') + '}';
  }
  return JSON.stringify(v);
}

export function b64digest(data: Uint8Array | string): string {
  return createHash('sha256').update(data).digest('base64url');
}

/** Digest of a JSON-safe value (matches the server's object digest). */
export function digest(value: unknown): string {
  return b64digest(Buffer.from(canonicalJson(value), 'utf8'));
}
