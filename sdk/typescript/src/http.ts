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

// ---------------------------------------------------------------- WEFT_DEBUG_HTTP
// Print every request and response (time, method,
// URL, headers with Authorization redacted, body; then elapsed time, status,
// headers and body) to stderr. Read on every request.
const DIV_REQUEST = '-'.repeat(21);
const DIV_RESPONSE = '----- Response below -----';
const BODY_LIMIT = 64 * 1024;

export function debugHttpEnabled(): boolean {
  return ['1', 'true', 'yes', 'on'].includes((process.env.WEFT_DEBUG_HTTP ?? '').trim().toLowerCase());
}

function debugWrite(text: string): void {
  try {
    process.stderr.write(text);
  } catch {
    /* ignore */
  }
}

function headersText(h: Iterable<[string, string]>): string {
  return [...h].map(([k, v]) => `  ${k}: ${['authorization', 'cookie', 'x-api-key'].includes(k.toLowerCase()) ? '<Redacted>' : v}`).join('\n');
}

function bodyText(body: unknown, ctype: string): string {
  if (body === undefined || body === null || body === '') return '  None';
  if (typeof body !== 'string') return body instanceof FormData ? '  <multipart/form-data>' : `  <${(body as { byteLength?: number }).byteLength ?? '?'} bytes>`;
  let text = body;
  if (ctype.includes('json') || /^[[{]/.test(text)) {
    try {
      text = JSON.stringify(JSON.parse(text), null, 4);
    } catch {
      /* not JSON */
    }
  }
  return text.length > BODY_LIMIT ? `${text.slice(0, BODY_LIMIT)}\n... (${text.length - BODY_LIMIT} more characters)` : text;
}

function debugRequest(method: string, url: string, headers: Record<string, string>, body: unknown): number {
  const ctype = headers['Content-Type'] ?? '';
  debugWrite(`${DIV_REQUEST}\nTime: ${new Date().toISOString()}\nMethod: ${method}\nURL: ${url}\nHeaders:\n${headersText(Object.entries(headers))}\nBody:\n${bodyText(body, ctype)}\n`);
  return Date.now();
}

async function debugResponse(res: Response, started: number): Promise<Response> {
  const text = await res.text().catch(() => '');
  debugWrite(
    `${DIV_RESPONSE}\nElapsed Time: ${((Date.now() - started) / 1000).toFixed(2)} seconds\nStatus Code: ${res.status}\nReason: ${res.statusText}\n` +
      `Headers:\n${headersText(res.headers as unknown as Iterable<[string, string]>)}\nBody:\n${bodyText(text, res.headers.get('content-type') ?? '')}\n`,
  );
  return new Response(text, { status: res.status, statusText: res.statusText, headers: res.headers });
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
    const headers = this.headers(body === undefined ? null : contentType);
    const debug = debugHttpEnabled();
    const started = debug ? debugRequest(method, this.baseUrl + path, headers, body) : 0;
    try {
      res = await fetch(this.baseUrl + path, {
        method,
        headers,
        body,
        signal: AbortSignal.timeout(timeoutMs ?? this.timeoutMs),
      });
    } catch (e) {
      if (debug) debugWrite(`${DIV_RESPONSE}\nError: ${(e as Error).message}\n`);
      throw new WeftHTTPError(0, `connection failed: ${(e as Error).message}`, path);
    }
    if (debug) res = await debugResponse(res, started);
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

  async request<T = any>(method: string, path: string, body?: unknown, query?: Record<string, string>): Promise<T> {
    const q = query ? '?' + new URLSearchParams(query).toString() : '';
    const res = await this.raw(method, path + q, body === undefined ? undefined : JSON.stringify(body));
    const text = await res.text();
    return (text.trim() ? JSON.parse(text) : {}) as T;
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
    form.append('file', new Blob([new Uint8Array(data)], { type: mime }), filename);
    const res = await this.raw('POST', path, form, null);
    const text = await res.text();
    return text.trim() ? JSON.parse(text) : {};
  }
}
