import { readFileSync, writeFileSync } from 'node:fs';
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
 *
 * Media already in your own bucket is logged by reference, without uploading:
 * `Content.fromUri('s3://bucket/key')` (also `Image`, `Audio`, `Video`, `PDF`, ...)
 * serialises as `{_type, uri, mimetype | mime_type, filename}`. `https://` URLs
 * and `weft-file://<digest>` work too; `client.resolveUrl()` gives download URLs.
 */
export class Content {
  private bytes: Uint8Array | null;
  readonly mimetype: string;
  readonly filename: string | null;
  /** Set for `fromUri` content (stored by reference). */
  readonly uri: string | null = null;
  private refSize: number | null = null;
  protected static defaultMimetype = 'application/octet-stream';
  /** `_type` used when serialised. */
  get mediaType(): string {
    return 'Content';
  }

  constructor(data: Uint8Array | string, opts: { mimetype?: string; filename?: string | null } = {}) {
    this.bytes = typeof data === 'string' ? new Uint8Array(Buffer.from(data, 'utf8')) : data;
    this.filename = opts.filename ?? null;
    this.mimetype = opts.mimetype ?? guess(this.filename, this.bytes) ?? (this.constructor as typeof Content).defaultMimetype;
  }

  /**
   * Reference media that already lives elsewhere (`s3://bucket/key`, `https://...`,
   * `weft-file://<digest>`) without uploading it. Read it with `await content.load()`.
   */
  static fromUri<T extends typeof Content>(this: T, uri: string, opts: { mimetype?: string; filename?: string; size?: number } = {}): InstanceType<T> {
    if (typeof uri !== 'string' || !uri.includes('://')) throw new Error(`expected a URI such as s3://bucket/key, got ${String(uri)}`);
    const path = uri.split('?')[0].replace(/\/+$/, '');
    const filename = opts.filename ?? (basename(path.split('://')[1] ?? '') || null);
    const mimetype = opts.mimetype ?? (filename ? EXT_MIME[extname(filename).toLowerCase()] : undefined);
    const c = new this(new Uint8Array(0), { mimetype, filename }) as InstanceType<T>;
    const w = c as unknown as { bytes: Uint8Array | null; uri: string; refSize: number | null };
    w.bytes = null;
    w.uri = uri;
    w.refSize = opts.size ?? null;
    return c;
  }

  get isReference(): boolean {
    return this.uri !== null;
  }

  /** The bytes (throws for unloaded `fromUri` content; use `load()`). */
  get data(): Uint8Array {
    if (!this.bytes) throw new Error(`content ${this.uri} is a reference; call await content.load() first`);
    return this.bytes;
  }

  /** Fetch the bytes of `fromUri` content (through `client.resolveUrl` for non-HTTP URIs). */
  async load(): Promise<Uint8Array> {
    if (this.bytes) return this.bytes;
    let url = this.uri!;
    if (!/^https?:\/\//.test(url)) {
      const { requireClient } = await import('./global.js');
      url = (await requireClient().resolveUrl(url)) as string;
    }
    const res = await fetch(url);
    if (!res.ok) throw new Error(`fetching ${this.uri} failed: HTTP ${res.status}`);
    this.bytes = new Uint8Array(await res.arrayBuffer());
    return this.bytes;
  }

  /** A download URL for this content (its reference, or its uploaded file digest). */
  async resolveUrl(opts: { expiresIn?: number } = {}): Promise<string> {
    const { requireClient } = await import('./global.js');
    return (await requireClient().resolveUrl(this.uri ?? this.digest, opts)) as string;
  }

  /** Write the bytes to a file. */
  save(path: string): void {
    writeFileSync(path, this.data);
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
    if (!this.bytes && this.refSize !== null) return this.refSize;
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
    if (this.uri !== null) {
      const out: Record<string, unknown> =
        this.mediaType === 'Content'
          ? { _type: 'Content', mimetype: this.mimetype, filename: this.filename, uri: this.uri }
          : { _type: this.mediaType, uri: this.uri, mime_type: this.mimetype, filename: this.filename };
      if (this.refSize !== null) out.size = this.refSize;
      return out;
    }
    const fileDigest = client ? client.uploadFile(this.data, this.mimetype) : this.digest;
    if (this.mediaType === 'Content') return { _type: 'Content', mimetype: this.mimetype, filename: this.filename, file_digest: fileDigest };
    return { _type: this.mediaType, file_digest: fileDigest, mime_type: this.mimetype, filename: this.filename };
  }
}

/** An image file (`Image.fromPath('cat.png')`, `Image.fromUri('s3://...')`). */
export class Image extends Content {
  protected static defaultMimetype = 'image/png';
  get mediaType(): string {
    return 'Image';
  }
}

/** An audio file. */
export class Audio extends Content {
  protected static defaultMimetype = 'audio/wav';
  get mediaType(): string {
    return 'Audio';
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
