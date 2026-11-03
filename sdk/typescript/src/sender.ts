import { logger } from './log.js';
import { appendFileSync, existsSync, mkdirSync, readFileSync, renameSync, unlinkSync } from 'node:fs';
import { dirname } from 'node:path';
import { HttpClient, WeftHTTPError } from './http.js';

export type Item =
  | { kind: 'call_start' | 'call_end'; payload: Record<string, unknown>; cb?: (r: any) => void; prepared?: boolean }
  | { kind: 'post'; path: string; payload: unknown; cb?: (r: any) => void }
  | { kind: 'file'; path: string; fields: Record<string, string>; data: Uint8Array; filename: string; mime: string; cb?: (r: any) => void };

export interface SenderOptions {
  maxQueue?: number;
  batchSize?: number;
  flushIntervalMs?: number;
  maxRetries?: number;
  backoffMs?: number;
  maxBackoffMs?: number;
  /** Write-ahead log file: unsent items are appended here and replayed on the next start. */
  walPath?: string;
  /** With `walPath`: write every item to the WAL and send nothing (WEFT_DISABLE_WAL_SENDER). */
  walOnly?: boolean;
}

/** Append-only JSONL file of unsent requests. */
export class WriteAheadLog {
  constructor(readonly path: string) {}

  append(items: Item[]): void {
    if (!items.length) return;
    mkdirSync(dirname(this.path), { recursive: true });
    const lines = items
      .map((it) => {
        const { cb: _cb, ...rest } = it as Item & { cb?: unknown };
        if (rest.kind === 'file') return JSON.stringify({ ...rest, data: Buffer.from(rest.data).toString('base64') });
        return JSON.stringify(rest);
      })
      .join('\n');
    appendFileSync(this.path, lines + '\n', 'utf8');
  }

  drain(): Item[] {
    if (!existsSync(this.path)) return [];
    const tmp = `${this.path}.${process.pid}.${Date.now()}.replay`;
    try {
      renameSync(this.path, tmp);
    } catch {
      return [];
    }
    const out: Item[] = [];
    try {
      for (const line of readFileSync(tmp, 'utf8').split('\n')) {
        if (!line.trim()) continue;
        try {
          const d = JSON.parse(line);
          if (d.kind === 'file') d.data = new Uint8Array(Buffer.from(d.data, 'base64'));
          out.push(d as Item);
        } catch {
          /* skip corrupt line */
        }
      }
    } finally {
      try {
        unlinkSync(tmp);
      } catch {
        /* ignore */
      }
    }
    return out;
  }

  count(): number {
    if (!existsSync(this.path)) return 0;
    return readFileSync(this.path, 'utf8').split('\n').filter((l) => l.trim()).length;
  }
}

/**
 * Background batching sender. Call starts/ends are coalesced into
 * /call/upsert_batch; other writes are sent one by one, in order. The queue is
 * bounded and failures are retried then dropped with a warning, so user code
 * never blocks on or crashes because of the server.
 */
export class Sender {
  private queue: Item[] = [];
  private running = false;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private waiters: Array<() => void> = [];
  private lastWarn = 0;
  dropped = 0;
  failedRequests = 0;
  sentRequests = 0;
  readonly maxQueue: number;
  readonly batchSize: number;
  flushIntervalMs: number;
  maxRetries: number;
  backoffMs: number;
  maxBackoffMs: number;
  readonly wal: WriteAheadLog | null;
  /** Everything goes to the WAL, nothing is sent (WEFT_DISABLE_WAL_SENDER). */
  readonly walOnly: boolean;
  walWritten = 0;
  /**
   * Async step run on call start/end items just before they are sent (e.g.
   * NER redaction through a remote analyzer). It may mutate the payloads;
   * an item is prepared once.
   */
  prepare: ((items: Item[]) => Promise<void>) | null = null;
  private lastFailureRetryable = false;

  constructor(
    private readonly http: HttpClient,
    opts: SenderOptions = {},
  ) {
    const mq = opts.maxQueue ?? 10_000;
    this.maxQueue = mq > 0 ? mq : Number.POSITIVE_INFINITY; // 0 = unbounded
    this.batchSize = opts.batchSize ?? 100;
    this.flushIntervalMs = opts.flushIntervalMs ?? 100;
    this.maxRetries = opts.maxRetries ?? 3;
    this.backoffMs = opts.backoffMs ?? 250;
    this.maxBackoffMs = opts.maxBackoffMs ?? 4000;
    this.wal = opts.walPath ? new WriteAheadLog(opts.walPath) : null;
    this.walOnly = !!(opts.walOnly && this.wal);
  }

  /** Re-enqueue items saved by a previous process; returns how many. */
  replayWal(): number {
    if (!this.wal || this.walOnly) return 0;
    const items = this.wal.drain();
    for (const it of items) this.enqueue(it);
    return items.length;
  }

  /** Move everything still queued to the write-ahead log (synchronous; used at exit). */
  spillPending(): number {
    if (!this.wal) return 0;
    const items = this.queue.splice(0);
    this.spill(items);
    return items.length;
  }

  private spill(items: Item[]): void {
    if (!this.wal || !items.length) return;
    try {
      this.wal.append(items);
      this.walWritten += items.length;
    } catch (e) {
      this.warn(`weft: could not write the write-ahead log: ${(e as Error).message}`);
    }
  }

  get pending(): number {
    return this.queue.length + (this.running ? 1 : 0);
  }

  enqueue(item: Item): boolean {
    if (this.walOnly) {
      this.spill([item]);
      return true;
    }
    if (this.queue.length >= this.maxQueue) {
      if (this.wal) {
        this.spill([item]);
        return true;
      }
      this.dropped++;
      this.warn(`weft: send queue full (${this.maxQueue}); dropping ${item.kind}`);
      return false;
    }
    this.queue.push(item);
    this.schedule(this.queue.length >= this.batchSize ? 0 : this.flushIntervalMs);
    return true;
  }

  /** Resolves once everything queued so far has been sent (or dropped). */
  flush(): Promise<void> {
    if (!this.queue.length && !this.running) return Promise.resolve();
    const p = new Promise<void>((resolve) => this.waiters.push(resolve));
    this.schedule(0);
    return p;
  }

  private warn(msg: string): void {
    const now = Date.now();
    if (now - this.lastWarn > 5000) {
      this.lastWarn = now;
      logger.warn(msg);
    }
  }

  private schedule(delay: number): void {
    if (this.running) return;
    if (this.timer && delay > 0) return;
    if (this.timer) clearTimeout(this.timer);
    this.timer = setTimeout(() => {
      this.timer = null;
      void this.drain();
    }, delay);
    if (delay > 0 && typeof this.timer === 'object' && this.timer && 'unref' in this.timer) this.timer.unref();
  }

  private async drain(): Promise<void> {
    if (this.running) return;
    this.running = true;
    try {
      while (this.queue.length) {
        const first = this.queue[0];
        if (first.kind === 'call_start' || first.kind === 'call_end') {
          const items: Item[] = [];
          while (items.length < this.batchSize && this.queue.length && (this.queue[0].kind === 'call_start' || this.queue[0].kind === 'call_end')) {
            items.push(this.queue.shift()!);
          }
          if (this.prepare) {
            const todo = items.filter((it) => !(it as { prepared?: boolean }).prepared);
            if (todo.length) {
              try {
                await this.prepare(todo);
              } catch (e) {
                this.warn(`weft: preparing calls failed: ${(e as Error).message}`);
              }
              for (const it of todo) (it as { prepared?: boolean }).prepared = true;
            }
          }
          const batch = items.map((it) => {
            const mode = it.kind === 'call_start' ? 'start' : 'end';
            return { mode, req: { [mode]: (it as { payload: unknown }).payload } };
          });
          const res = await this.retry(() => this.http.post('/call/upsert_batch', { batch }), 'call/upsert_batch');
          if (res !== undefined) for (const it of items) it.cb?.(res);
          else if (this.lastFailureRetryable) this.spill(items);
        } else {
          const it = this.queue.shift()!;
          const res =
            it.kind === 'post'
              ? await this.retry(() => this.http.post(it.path, it.payload), it.path)
              : it.kind === 'file'
                ? await this.retry(() => this.http.postFile(it.path, it.fields, it.data, it.filename, it.mime), it.path)
                : undefined;
          if (res !== undefined) it.cb?.(res);
          else if (this.lastFailureRetryable) this.spill([it]);
        }
      }
    } finally {
      this.running = false;
      if (!this.queue.length) {
        const w = this.waiters;
        this.waiters = [];
        w.forEach((r) => r());
      } else {
        this.schedule(0);
      }
    }
  }

  private async retry<T>(fn: () => Promise<T>, what: string): Promise<T | undefined> {
    let delay = this.backoffMs;
    for (let attempt = 0; ; attempt++) {
      try {
        const r = await fn();
        this.sentRequests++;
        return r;
      } catch (e) {
        const retryable = e instanceof WeftHTTPError ? e.retryable : false;
        this.lastFailureRetryable = retryable;
        if (!retryable || attempt >= this.maxRetries) {
          this.failedRequests++;
          this.warn(`weft: failed to send ${what}: ${(e as Error).message}`);
          return undefined;
        }
      }
      await new Promise((r) => setTimeout(r, delay));
      delay = Math.min(delay * 2, this.maxBackoffMs);
    }
  }
}
