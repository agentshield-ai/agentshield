// Resolves media URIs (s3://, gs://, weft-file://, https://) to download URLs
// through POST /api/v1/files/resolve, with batching, caching and expiry.
//
// - URIs of one bucket requested in the same tick (per project) go out as one
//   request, with `per_uri_errors` so the server reports each unresolvable URI
//   (for example in a bucket that is not registered) instead of failing the
//   batch. A server without per-URI errors fails the whole batch with a 400:
//   "not registered" then fails every URI of the batch (one bucket) at once;
//   any other 400 is retried one URI at a time to tell the good from the bad.
//   Other failures (401, 403, 5xx, network) fail the batch without retries.
// - A presigned URL is cached until shortly before `expires_at`; after that
//   the next lookup resolves it again. http(s) URLs resolve to themselves
//   without a request.
// - Failures are cached briefly (errorTtlMs) so a page full of URIs in an
//   unregistered bucket does not hammer the server; invalidate() clears the
//   cache (the Buckets settings tab calls it after a change).

export type ResolveFn = (
  projectId: string,
  uris: string[],
) => Promise<{ download_urls: string[]; expires_at: string | null; errors?: ({ code: string; message: string } | null)[] }>

export type Resolved =
  | { ok: true; url: string; expiresAt: number | null }
  | { ok: false; reason: string; status?: number }

interface Entry {
  value: Resolved
  /** When the entry stops being usable (ms since epoch); null never. */
  staleAt: number | null
}

interface Pending {
  projectId: string
  uris: Map<string, { resolve: (r: Resolved) => void }[]>
  timer: ReturnType<typeof setTimeout> | null
}

export interface ResolverOptions {
  now?: () => number
  /** Window for collecting URIs into one request. */
  batchDelayMs?: number
  /** How long a failure is remembered. */
  errorTtlMs?: number
  /** Re-resolve this long before a presigned URL expires (capped at a quarter of its lifetime). */
  refreshMarginMs?: number
  /** Max URIs per request (the server allows 1000). */
  maxBatch?: number
}

/** A short, user-facing reason for a resolve failure. */
export function shortReason(e: unknown): { reason: string; status?: number } {
  const status = typeof (e as { status?: unknown })?.status === 'number' ? (e as { status: number }).status : undefined
  const msg = e instanceof Error ? e.message : String(e ?? '')
  if (/not registered as a reference bucket/i.test(msg)) return { reason: 'bucket not registered for this project', status }
  if (/no S3 files backend/i.test(msg)) return { reason: 'bucket not registered for this project', status }
  if (/is not a file of project/i.test(msg)) return { reason: 'outside this project’s files', status }
  if (status === 401) return { reason: 'sign in to load media', status }
  if (status === 403) return { reason: 'no access to this project', status }
  if (/does not exist \(Settings/i.test(msg)) return { reason: 'bucket credentials secret is missing', status }
  if (/Failed to fetch|NetworkError|network/i.test(msg)) return { reason: 'server unreachable', status }
  const first = msg.split('\n')[0].trim()
  return { reason: first.length > 120 ? first.slice(0, 117) + '…' : first || 'could not resolve', status }
}

/** A short reason for one per-URI error from /api/v1/files/resolve. */
export function uriErrorReason(e: { code: string; message: string }): string {
  switch (e.code) {
    case 'bucket_not_registered':
    case 'no_files_backend':
      return 'bucket not registered for this project'
    case 'not_project_file':
      return 'outside this project’s files'
    case 'file_not_found':
      return 'file not found'
    case 'invalid_uri':
      return 'not a valid object URI'
  }
  return shortReason(new Error(e.message)).reason
}

/** scheme://bucket of a URI, the batching unit. */
function bucketOf(uri: string): string {
  const m = /^([a-z0-9+.-]+:\/\/[^/]*)/i.exec(uri)
  return m ? m[1] : ''
}

export class UriResolver {
  private cache = new Map<string, Entry>()
  private inflight = new Map<string, Promise<Resolved>>()
  private pending = new Map<string, Pending>()
  private now: () => number
  private batchDelayMs: number
  private errorTtlMs: number
  private refreshMarginMs: number
  private maxBatch: number
  private generation = 0
  /** Number of /api/v1/files/resolve requests made (for tests and debugging). */
  requests = 0

  constructor(
    private fetchUrls: ResolveFn,
    opts: ResolverOptions = {},
  ) {
    this.now = opts.now ?? Date.now
    this.batchDelayMs = opts.batchDelayMs ?? 10
    this.errorTtlMs = opts.errorTtlMs ?? 60_000
    this.refreshMarginMs = opts.refreshMarginMs ?? 60_000
    this.maxBatch = opts.maxBatch ?? 100
  }

  private key(projectId: string, uri: string) {
    return projectId + '\u0000' + uri
  }

  /** A cached, still usable result, or undefined. */
  peek(projectId: string, uri: string): Resolved | undefined {
    const e = this.cache.get(this.key(projectId, uri))
    if (!e) return undefined
    if (e.staleAt != null && this.now() >= e.staleAt) return undefined
    return e.value
  }

  /** When a cached result stops being usable (ms since epoch), or null. */
  staleAt(projectId: string, uri: string): number | null {
    return this.cache.get(this.key(projectId, uri))?.staleAt ?? null
  }

  /** Forget cached results: one URI, one project, or everything. */
  invalidate(projectId?: string, uri?: string) {
    this.generation++
    if (projectId && uri) {
      this.cache.delete(this.key(projectId, uri))
      return
    }
    for (const k of [...this.cache.keys()]) if (!projectId || k.startsWith(projectId + '\u0000')) this.cache.delete(k)
  }

  resolve(projectId: string, uri: string): Promise<Resolved> {
    const hit = this.peek(projectId, uri)
    if (hit) return Promise.resolve(hit)
    if (/^https?:\/\//i.test(uri)) {
      const r: Resolved = { ok: true, url: uri, expiresAt: null }
      this.cache.set(this.key(projectId, uri), { value: r, staleAt: null })
      return Promise.resolve(r)
    }
    const k = this.key(projectId, uri)
    const running = this.inflight.get(k)
    if (running) return running
    const group = projectId + '\u0000' + bucketOf(uri)
    const p = new Promise<Resolved>((resolve) => {
      let pend = this.pending.get(group)
      if (!pend) {
        pend = { projectId, uris: new Map(), timer: null }
        this.pending.set(group, pend)
      }
      const waiters = pend.uris.get(uri) ?? []
      waiters.push({ resolve })
      pend.uris.set(uri, waiters)
      if (!pend.timer) pend.timer = setTimeout(() => void this.flush(group), this.batchDelayMs)
    })
    this.inflight.set(k, p)
    void p.finally(() => this.inflight.delete(k))
    return p
  }

  private store(projectId: string, uri: string, r: Resolved, gen: number) {
    if (gen !== this.generation) return // invalidated meanwhile: do not resurrect
    let staleAt: number | null
    if (r.ok) {
      if (r.expiresAt == null) staleAt = null
      else {
        const life = Math.max(0, r.expiresAt - this.now())
        staleAt = r.expiresAt - Math.min(this.refreshMarginMs, life / 4)
      }
    } else staleAt = this.now() + this.errorTtlMs
    this.cache.set(this.key(projectId, uri), { value: r, staleAt })
  }

  private async flush(group: string) {
    const pend = this.pending.get(group)
    this.pending.delete(group)
    if (!pend) return
    const projectId = pend.projectId
    const all = [...pend.uris.keys()]
    for (let i = 0; i < all.length; i += this.maxBatch) {
      const chunk = all.slice(i, i + this.maxBatch)
      const results = await this.fetchChunk(projectId, chunk)
      chunk.forEach((u, j) => {
        for (const w of pend.uris.get(u) ?? []) w.resolve(results[j])
      })
    }
  }

  private async fetchChunk(projectId: string, uris: string[]): Promise<Resolved[]> {
    const gen = this.generation
    const keep = (u: string, r: Resolved) => {
      this.store(projectId, u, r, gen)
      return r
    }
    try {
      this.requests++
      const res = await this.fetchUrls(projectId, uris)
      const exp = res.expires_at ? Date.parse(res.expires_at) : NaN
      return uris.map((u, i) => {
        const url = res.download_urls[i]
        const err = res.errors?.[i]
        if (url) return keep(u, { ok: true, url, expiresAt: Number.isFinite(exp) ? exp : null })
        return keep(u, { ok: false, reason: err ? uriErrorReason(err) : 'could not resolve', ...(err ? { status: 400 } : {}) })
      })
    } catch (e) {
      const r: Resolved = { ok: false, ...shortReason(e) }
      const msg = e instanceof Error ? e.message : ''
      // Only a server without per-URI errors fails a batch for one bad URI
      // (400). A batch is one bucket, so "not registered" holds for all of it.
      if (uris.length > 1 && r.status === 400 && !/not registered as a reference bucket/i.test(msg)) {
        return Promise.all(uris.map((u) => this.fetchChunk(projectId, [u]).then((x) => x[0])))
      }
      return uris.map((u) => keep(u, r))
    }
  }
}
