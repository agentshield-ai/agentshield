import { HttpClient, WeftHTTPError } from './http.js';

type Item =
  | { kind: 'call_start' | 'call_end'; payload: Record<string, unknown>; cb?: (r: any) => void }
  | { kind: 'post'; path: string; payload: unknown; cb?: (r: any) => void }
  | { kind: 'file'; path: string; fields: Record<string, string>; data: Uint8Array; filename: string; mime: string; cb?: (r: any) => void };

export interface SenderOptions {
  maxQueue?: number;
  batchSize?: number;
  flushIntervalMs?: number;
  maxRetries?: number;
  backoffMs?: number;
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

  constructor(
    private readonly http: HttpClient,
    opts: SenderOptions = {},
  ) {
    this.maxQueue = opts.maxQueue ?? 10_000;
    this.batchSize = opts.batchSize ?? 100;
    this.flushIntervalMs = opts.flushIntervalMs ?? 100;
    this.maxRetries = opts.maxRetries ?? 3;
    this.backoffMs = opts.backoffMs ?? 250;
  }

  get pending(): number {
    return this.queue.length + (this.running ? 1 : 0);
  }

  enqueue(item: Item): boolean {
    if (this.queue.length >= this.maxQueue) {
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
      console.warn(msg);
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
          const batch = items.map((it) => {
            const mode = it.kind === 'call_start' ? 'start' : 'end';
            return { mode, req: { [mode]: (it as { payload: unknown }).payload } };
          });
          const res = await this.retry(() => this.http.post('/call/upsert_batch', { batch }), 'call/upsert_batch');
          if (res !== undefined) for (const it of items) it.cb?.(res);
        } else {
          const it = this.queue.shift()!;
          const res =
            it.kind === 'post'
              ? await this.retry(() => this.http.post(it.path, it.payload), it.path)
              : it.kind === 'file'
                ? await this.retry(() => this.http.postFile(it.path, it.fields, it.data, it.filename, it.mime), it.path)
                : undefined;
          if (res !== undefined) it.cb?.(res);
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
        if (!retryable || attempt >= this.maxRetries) {
          this.failedRequests++;
          this.warn(`weft: failed to send ${what}: ${(e as Error).message}`);
          return undefined;
        }
      }
      await new Promise((r) => setTimeout(r, delay));
      delay = Math.min(delay * 2, 4000);
    }
  }
}
