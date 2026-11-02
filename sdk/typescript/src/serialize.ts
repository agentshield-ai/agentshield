import type { WeftClient } from './client.js';
import { Ref } from './refs.js';

const MAX_DEPTH = 64;

export function sniffMime(data: Uint8Array): string {
  const b = Buffer.from(data.buffer, data.byteOffset, Math.min(data.byteLength, 16));
  const s = (start: number, end: number) => b.subarray(start, end).toString('latin1');
  if (b[0] === 0x89 && s(1, 4) === 'PNG') return 'image/png';
  if (b[0] === 0xff && b[1] === 0xd8 && b[2] === 0xff) return 'image/jpeg';
  if (s(0, 4) === 'GIF8') return 'image/gif';
  if (s(0, 4) === 'RIFF' && s(8, 12) === 'WEBP') return 'image/webp';
  if (s(0, 4) === 'RIFF' && s(8, 12) === 'WAVE') return 'audio/wav';
  if (s(0, 3) === 'ID3') return 'audio/mpeg';
  if (s(0, 4) === '%PDF') return 'application/pdf';
  return 'application/octet-stream';
}

export function mediaType(mime: string): string {
  if (mime.startsWith('image/')) return 'Image';
  if (mime.startsWith('audio/')) return 'Audio';
  return 'File';
}

export function bytesToMedia(data: Uint8Array, client: WeftClient | null, mime?: string): unknown {
  const m = mime ?? sniffMime(data);
  if (!client) return `data:${m};base64,${Buffer.from(data).toString('base64')}`;
  return { _type: mediaType(m), file_digest: client.uploadFile(data, m), mime_type: m };
}

/** An object that can be published (Dataset, Evaluation, ...). */
export interface Publishable {
  __weftRef?: Ref;
  toVal(client: WeftClient | null, publishNested: boolean): unknown | Promise<unknown>;
}

export function getRef(v: unknown): Ref | null {
  if (v instanceof Ref) return v;
  if (v && (typeof v === 'object' || typeof v === 'function')) {
    const r = (v as { __weftRef?: unknown }).__weftRef;
    if (r instanceof Ref) return r;
  }
  return null;
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  if (v === null || typeof v !== 'object') return false;
  const proto = Object.getPrototypeOf(v);
  return proto === Object.prototype || proto === null;
}

/**
 * Convert any value into JSON-safe data. Published objects and ops become ref
 * strings, binary data is uploaded as a file, class instances are converted
 * via toJSON() or their enumerable fields.
 */
export function toJson(value: unknown, client: WeftClient | null = null, depth = 0, seen: Set<unknown> = new Set()): unknown {
  if (value === null || value === undefined) return null;
  switch (typeof value) {
    case 'string':
    case 'boolean':
      return value;
    case 'number':
      return Number.isFinite(value) ? value : String(value);
    case 'bigint':
      return value.toString();
    case 'symbol':
      return value.toString();
    case 'function': {
      const meta = (value as { __weftOp?: { name: string } }).__weftOp;
      if (meta) return client ? client.opRef(meta as never).uri() : meta.name;
      return `[Function ${(value as { name?: string }).name || 'anonymous'}]`;
    }
  }
  if (depth > MAX_DEPTH) return '<max depth>';
  const ref = getRef(value);
  if (ref) return ref.uri();
  if (seen.has(value)) return '<circular reference>';
  const rec = (v: unknown) => toJson(v, client, depth + 1, seen);
  seen.add(value);
  try {
    if (value instanceof Date) return value.toISOString();
    if (value instanceof Uint8Array) return bytesToMedia(value, client);
    if (value instanceof ArrayBuffer) return bytesToMedia(new Uint8Array(value), client);
    if (value instanceof Error) return { type: value.name, message: value.message };
    if (Array.isArray(value)) return value.map(rec);
    if (value instanceof Set) return [...value].map(rec);
    if (value instanceof Map) {
      const out: Record<string, unknown> = {};
      for (const [k, v] of value) out[String(k)] = rec(v);
      return out;
    }
    if (typeof (value as Publishable).toVal === 'function' && !isPlainObject(value)) {
      const v = (value as Publishable).toVal(client, false);
      if (!(v instanceof Promise)) return v;
    }
    if (!isPlainObject(value) && typeof (value as { toJSON?: unknown }).toJSON === 'function') {
      return rec((value as { toJSON: () => unknown }).toJSON());
    }
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value as object)) {
      if (v === undefined || k.startsWith('__weft')) continue;
      out[k] = rec(v);
    }
    return out;
  } finally {
    seen.delete(value);
  }
}
