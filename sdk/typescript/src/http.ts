export const VERSION = '0.1.0';

export class WeftHTTPError extends Error {
  constructor(
    public readonly status: number,
    public readonly reason: string,
    public readonly path: string,
  ) {
    super(status ? `${path}: HTTP ${status}: ${reason}` : `${path}: ${reason}`);
  }

  get retryable(): boolean {
    return this.status === 0 || this.status === 429 || this.status >= 500;
  }
}

export class HttpClient {
  constructor(
    public readonly baseUrl: string,
    public readonly apiKey?: string,
    public readonly timeoutMs = 30_000,
  ) {
    this.baseUrl = baseUrl.replace(/\/+$/, '');
  }

  headers(contentType: string | null = 'application/json'): Record<string, string> {
    const h: Record<string, string> = { 'User-Agent': `weft-typescript/${VERSION}`, Accept: 'application/json' };
    if (contentType) h['Content-Type'] = contentType;
    if (this.apiKey) h.Authorization = `Bearer ${this.apiKey}`;
    return h;
  }

  async raw(method: string, path: string, body?: BodyInit, contentType: string | null = 'application/json', timeoutMs?: number): Promise<Response> {
    let res: Response;
    try {
      res = await fetch(this.baseUrl + path, {
        method,
        headers: this.headers(body === undefined ? null : contentType),
        body,
        signal: AbortSignal.timeout(timeoutMs ?? this.timeoutMs),
      });
    } catch (e) {
      throw new WeftHTTPError(0, `connection failed: ${(e as Error).message}`, path);
    }
    if (!res.ok) {
      let reason = await res.text().catch(() => '');
      try {
        reason = JSON.parse(reason).reason ?? reason;
      } catch {
        /* keep text */
      }
      throw new WeftHTTPError(res.status, reason || res.statusText, path);
    }
    return res;
  }

  async post<T = any>(path: string, body: unknown, timeoutMs?: number): Promise<T> {
    const res = await this.raw('POST', path, JSON.stringify(body), 'application/json', timeoutMs);
    const text = await res.text();
    return (text.trim() ? JSON.parse(text) : {}) as T;
  }

  async get<T = any>(path: string): Promise<T> {
    const res = await this.raw('GET', path);
    const text = await res.text();
    return (text.trim() ? JSON.parse(text) : {}) as T;
  }

  async postJsonl<T = any>(path: string, body: unknown): Promise<T[]> {
    const res = await this.raw('POST', path, JSON.stringify(body));
    const text = await res.text();
    return text
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean)
      .map((l) => JSON.parse(l) as T);
  }

  async postFile(path: string, fields: Record<string, string>, data: Uint8Array, filename: string, mime: string): Promise<any> {
    const form = new FormData();
    for (const [k, v] of Object.entries(fields)) form.append(k, v);
    form.append('file', new Blob([data], { type: mime }), filename);
    const res = await this.raw('POST', path, form, null);
    const text = await res.text();
    return text.trim() ? JSON.parse(text) : {};
  }
}
