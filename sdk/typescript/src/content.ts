import { readFileSync } from 'node:fs';
import { basename, extname } from 'node:path';
import { b64digest } from './canonical.js';
import type { WeftClient } from './client.js';
import { sniffMime } from './serialize.js';

const EXT_MIME: Record<string, string> = {
  '.txt': 'text/plain',
  '.md': 'text/markdown',
  '.html': 'text/html',
  '.htm': 'text/html',
  '.json': 'application/json',
  '.csv': 'text/csv',
  '.pdf': 'application/pdf',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif': 'image/gif',
  '.webp': 'image/webp',
  '.svg': 'image/svg+xml',
  '.mp3': 'audio/mpeg',
  '.wav': 'audio/wav',
  '.mp4': 'video/mp4',
  '.webm': 'video/webm',
  '.mov': 'video/quicktime',
  '.zip': 'application/zip',
};

function guess(filename: string | null, data: Uint8Array): string | null {
  if (filename) {
    const m = EXT_MIME[extname(filename).toLowerCase()];
    if (m) return m;
  }
  const s = sniffMime(data);
  return s === 'application/octet-stream' ? null : s;
}

/**
 * Arbitrary file content, uploaded through /file/create when serialised as
 * `{_type: "Content", mimetype, filename, file_digest}`.
 */
export class Content {
  readonly data: Uint8Array;
  readonly mimetype: string;
  readonly filename: string | null;
  protected static defaultMimetype = 'application/octet-stream';
  /** `_type` used when serialised. */
  get mediaType(): string {
    return 'Content';
  }

  constructor(data: Uint8Array | string, opts: { mimetype?: string; filename?: string | null } = {}) {
    this.data = typeof data === 'string' ? new Uint8Array(Buffer.from(data, 'utf8')) : data;
    this.filename = opts.filename ?? null;
    this.mimetype = opts.mimetype ?? guess(this.filename, this.data) ?? (this.constructor as typeof Content).defaultMimetype;
  }

  static fromPath<T extends typeof Content>(this: T, path: string, opts: { mimetype?: string } = {}): InstanceType<T> {
    return new this(new Uint8Array(readFileSync(path)), { mimetype: opts.mimetype, filename: basename(path) }) as InstanceType<T>;
  }

  static fromBytes<T extends typeof Content>(this: T, data: Uint8Array, opts: { mimetype?: string; filename?: string; extension?: string } = {}): InstanceType<T> {
    const filename = opts.filename ?? (opts.extension ? `file.${opts.extension.replace(/^\./, '')}` : undefined);
    return new this(data, { mimetype: opts.mimetype, filename }) as InstanceType<T>;
  }

  static fromText<T extends typeof Content>(this: T, text: string, opts: { mimetype?: string; filename?: string } = {}): InstanceType<T> {
    return new this(new Uint8Array(Buffer.from(text, 'utf8')), { mimetype: opts.mimetype ?? 'text/plain', filename: opts.filename }) as InstanceType<T>;
  }

  get size(): number {
    return this.data.byteLength;
  }

  get digest(): string {
    return b64digest(this.data);
  }

  asText(): string {
    return Buffer.from(this.data).toString('utf8');
  }

  /** @internal */
  weftSerialize(client: WeftClient | null): Record<string, unknown> {
    const fileDigest = client ? client.uploadFile(this.data, this.mimetype) : this.digest;
    if (this.mediaType === 'Content') return { _type: 'Content', mimetype: this.mimetype, filename: this.filename, file_digest: fileDigest };
    return { _type: this.mediaType, file_digest: fileDigest, mime_type: this.mimetype, filename: this.filename };
  }
}

export class Video extends Content {
  protected static defaultMimetype = 'video/mp4';
  get mediaType(): string {
    return 'Video';
  }
}

export class PDF extends Content {
  protected static defaultMimetype = 'application/pdf';
  get mediaType(): string {
    return 'PDF';
  }
}

/** HTML rendered by the UI: `new weft.Html('<b>hi</b>')`. */
export class Html extends Content {
  constructor(data: Uint8Array | string, opts: { mimetype?: string; filename?: string | null } = {}) {
    super(data, { mimetype: opts.mimetype ?? 'text/html', filename: opts.filename });
  }
  get mediaType(): string {
    return 'Html';
  }
}

/** Markdown rendered by the UI: `new weft.Markdown('# Title')`. */
export class Markdown extends Content {
  constructor(data: Uint8Array | string, opts: { mimetype?: string; filename?: string | null } = {}) {
    super(data, { mimetype: opts.mimetype ?? 'text/markdown', filename: opts.filename });
  }
  get mediaType(): string {
    return 'Markdown';
  }
}
