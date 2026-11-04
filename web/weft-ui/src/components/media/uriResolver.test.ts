import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { shortReason, UriResolver, type ResolveFn } from './uriResolver'

class HttpError extends Error {
  constructor(
    public status: number,
    msg: string,
  ) {
    super(msg)
  }
}

const iso = (ms: number) => new Date(ms).toISOString()

describe('UriResolver', () => {
  let now = Date.parse('2026-11-02T12:00:00Z')
  beforeEach(() => {
    vi.useFakeTimers()
    now = Date.parse('2026-11-02T12:00:00Z')
  })
  afterEach(() => vi.useRealTimers())

  function setup(handler?: ResolveFn) {
    const calls: { projectId: string; uris: string[] }[] = []
    const fn: ResolveFn = async (projectId, uris) => {
      calls.push({ projectId, uris })
      if (handler) return handler(projectId, uris)
      return { download_urls: uris.map((u) => `https://signed/${u.slice(5)}?n=${calls.length}`), expires_at: iso(now + 3600_000) }
    }
    const r = new UriResolver(fn, { now: () => now, batchDelayMs: 5, errorTtlMs: 60_000, refreshMarginMs: 60_000 })
    return { r, calls }
  }

  async function settle<T>(p: Promise<T>): Promise<T> {
    await vi.advanceTimersByTimeAsync(10)
    return p
  }

  it('batches URIs requested together into one request per project and bucket, and dedupes', async () => {
    const { r, calls } = setup()
    const a = r.resolve('e/p', 's3://b/a.png')
    const b = r.resolve('e/p', 's3://b/b.png')
    const a2 = r.resolve('e/p', 's3://b/a.png')
    const other = r.resolve('e/q', 's3://b/a.png')
    const otherBucket = r.resolve('e/p', 'gs://b/c.png')
    await vi.advanceTimersByTimeAsync(10)
    expect(calls).toEqual([
      { projectId: 'e/p', uris: ['s3://b/a.png', 's3://b/b.png'] },
      { projectId: 'e/q', uris: ['s3://b/a.png'] },
      { projectId: 'e/p', uris: ['gs://b/c.png'] },
    ])
    expect((await otherBucket).ok).toBe(true)
    expect(await a).toEqual({ ok: true, url: 'https://signed/b/a.png?n=1', expiresAt: now + 3600_000 })
    expect(await a2).toEqual(await a)
    expect((await b).ok).toBe(true)
    expect((await other).ok).toBe(true)
  })

  it('serves cached URLs until shortly before they expire, then resolves again', async () => {
    const { r, calls } = setup()
    const first = await settle(r.resolve('e/p', 's3://b/a.png'))
    expect(r.peek('e/p', 's3://b/a.png')).toEqual(first)
    now += 30 * 60_000
    expect(await r.resolve('e/p', 's3://b/a.png')).toEqual(first)
    expect(calls).toHaveLength(1)
    expect(r.staleAt('e/p', 's3://b/a.png')).toBe(Date.parse('2026-11-02T12:59:00Z'))
    now = Date.parse('2026-11-02T12:59:30Z') // inside the refresh margin
    expect(r.peek('e/p', 's3://b/a.png')).toBeUndefined()
    const second = await settle(r.resolve('e/p', 's3://b/a.png'))
    expect(calls).toHaveLength(2)
    expect(second.ok && second.url).toBe('https://signed/b/a.png?n=2')
  })

  it('caps the refresh margin at a quarter of a short lifetime', async () => {
    const { r } = setup(async (_p, uris) => ({ download_urls: uris.map(() => 'https://x'), expires_at: iso(now + 8_000) }))
    await settle(r.resolve('e/p', 's3://b/a.png'))
    expect(r.staleAt('e/p', 's3://b/a.png')).toBe(now + 6_000)
  })

  it('uses per-URI errors: one request per bucket, no retries, failures remembered briefly', async () => {
    const { r, calls } = setup(async (_p, uris) => ({
      download_urls: uris.map((u) => (u.includes('unreg') ? '' : 'https://ok/' + u.slice(5))),
      expires_at: null,
      errors: uris.map((u) =>
        u.includes('unreg') ? { code: 'bucket_not_registered', message: `cannot resolve "${u}": the bucket is not registered as a reference bucket for e/p` } : null,
      ),
    }))
    const good = r.resolve('e/p', 's3://reg/a.png')
    const bad = r.resolve('e/p', 's3://reg/unreg-prefix/b.png') // same bucket, outside the registered prefix
    const many = Array.from({ length: 40 }, (_, i) => r.resolve('e/p', `s3://unreg/${i}.png`))
    await vi.advanceTimersByTimeAsync(10)
    expect(await good).toEqual({ ok: true, url: 'https://ok/reg/a.png', expiresAt: null })
    expect(await bad).toEqual({ ok: false, reason: 'bucket not registered for this project', status: 400 })
    for (const p of many) expect(await p).toEqual({ ok: false, reason: 'bucket not registered for this project', status: 400 })
    // 42 URIs in two buckets: two requests.
    expect(calls).toHaveLength(2)
    expect(r.requests).toBe(2)
    // The failure is cached for errorTtlMs, the success (no expiry) for good.
    await r.resolve('e/p', 's3://reg/unreg-prefix/b.png')
    await r.resolve('e/p', 's3://unreg/7.png')
    expect(calls).toHaveLength(2)
    now += 61_000
    expect(r.peek('e/p', 's3://reg/a.png')?.ok).toBe(true)
    expect(r.peek('e/p', 's3://reg/unreg-prefix/b.png')).toBeUndefined()
  })

  it('maps per-URI error codes to short reasons', async () => {
    const codes = ['not_project_file', 'file_not_found', 'invalid_uri', 'bucket_error']
    const { r } = setup(async (_p, uris) => ({
      download_urls: uris.map(() => ''),
      expires_at: null,
      errors: uris.map((_u, i) => ({ code: codes[i], message: i === 3 ? 'reference bucket s3://k: secret "S" does not exist (Settings → Secrets)' : 'x' })),
    }))
    const ps = codes.map((c) => r.resolve('e/p', `s3://k/${c}.png`))
    await vi.advanceTimersByTimeAsync(10)
    expect((await Promise.all(ps)).map((x) => !x.ok && x.reason)).toEqual([
      'outside this project’s files',
      'file not found',
      'not a valid object URI',
      'bucket credentials secret is missing',
    ])
  })

  it('without per-URI errors, fails a whole unregistered bucket at once', async () => {
    const { r, calls } = setup(async (_p, uris) => {
      if (uris.some((u) => u.startsWith('s3://unreg/'))) throw new HttpError(400, `cannot resolve "${uris[0]}": the bucket is not registered as a reference bucket for e/p`)
      return { download_urls: uris.map((u) => 'https://ok/' + u.slice(5)), expires_at: null }
    })
    const many = Array.from({ length: 40 }, (_, i) => r.resolve('e/p', `s3://unreg/${i}.png`))
    const good = r.resolve('e/p', 's3://reg/a.png')
    await vi.advanceTimersByTimeAsync(10)
    for (const p of many) expect(await p).toEqual({ ok: false, reason: 'bucket not registered for this project', status: 400 })
    expect((await good).ok).toBe(true)
    expect(calls).toHaveLength(2)
  })

  it('without per-URI errors, retries another 400 one URI at a time', async () => {
    const { r, calls } = setup(async (_p, uris) => {
      if (uris.some((u) => u.includes('bad'))) throw new HttpError(400, `uri "${uris[0]}" is not a file of project e/p in the configured bucket`)
      return { download_urls: uris.map((u) => 'https://ok/' + u.slice(5)), expires_at: null }
    })
    const good = r.resolve('e/p', 's3://bkt/a.png')
    const bad = r.resolve('e/p', 's3://bkt/bad.png')
    await vi.advanceTimersByTimeAsync(10)
    expect(await good).toEqual({ ok: true, url: 'https://ok/bkt/a.png', expiresAt: null })
    expect(await bad).toEqual({ ok: false, reason: 'outside this project’s files', status: 400 })
    expect(calls.map((c) => c.uris.join(' '))).toEqual(['s3://bkt/a.png s3://bkt/bad.png', 's3://bkt/a.png', 's3://bkt/bad.png'])
  })

  it('does not retry per URI when the whole request is refused', async () => {
    const { r, calls } = setup(async () => {
      throw new HttpError(403, 'no access to project e/p')
    })
    const ps = ['a', 'b', 'c'].map((k) => r.resolve('e/p', `s3://b/${k}.png`))
    await vi.advanceTimersByTimeAsync(10)
    for (const p of ps) expect(await p).toEqual({ ok: false, reason: 'no access to this project', status: 403 })
    expect(calls).toHaveLength(1)
  })

  it('resolves http(s) URLs without a request and forgets on invalidate', async () => {
    const { r, calls } = setup()
    expect(await r.resolve('e/p', 'https://cdn/x.png')).toEqual({ ok: true, url: 'https://cdn/x.png', expiresAt: null })
    expect(calls).toHaveLength(0)
    await settle(r.resolve('e/p', 's3://b/a.png'))
    await settle(r.resolve('e/q', 's3://b/a.png'))
    r.invalidate('e/p')
    expect(r.peek('e/p', 's3://b/a.png')).toBeUndefined()
    expect(r.peek('e/q', 's3://b/a.png')).toBeDefined()
    r.invalidate()
    expect(r.peek('e/q', 's3://b/a.png')).toBeUndefined()
  })

  it('does not cache a result that arrives after an invalidate', async () => {
    let release: () => void = () => {}
    const { r } = setup(
      (_p, uris) =>
        new Promise((res) => {
          release = () => res({ download_urls: uris.map(() => 'https://old'), expires_at: null })
        }),
    )
    const p = r.resolve('e/p', 's3://b/a.png')
    await vi.advanceTimersByTimeAsync(10)
    r.invalidate()
    release()
    expect((await p).ok).toBe(true)
    expect(r.peek('e/p', 's3://b/a.png')).toBeUndefined()
  })

  it('splits large batches', async () => {
    const calls: number[] = []
    const r = new UriResolver(
      async (_p, uris) => {
        calls.push(uris.length)
        return { download_urls: uris.map(() => 'https://x'), expires_at: null }
      },
      { batchDelayMs: 1, maxBatch: 2 },
    )
    const ps = ['a', 'b', 'c', 'd', 'e'].map((k) => r.resolve('e/p', `s3://b/${k}.png`))
    await vi.advanceTimersByTimeAsync(5)
    await Promise.all(ps)
    expect(calls).toEqual([2, 2, 1])
  })
})

describe('shortReason', () => {
  it('maps server errors to short reasons', () => {
    expect(shortReason(new HttpError(400, 'cannot resolve "s3://x/y": the bucket is not registered as a reference bucket for e/p')).reason).toBe(
      'bucket not registered for this project',
    )
    expect(shortReason(new HttpError(403, 'no access to project e/p'))).toEqual({ reason: 'no access to this project', status: 403 })
    expect(shortReason(new TypeError('Failed to fetch')).reason).toBe('server unreachable')
    expect(shortReason(new Error('x'.repeat(200))).reason).toHaveLength(118)
  })
})

describe('mediaApi.resolveUrls', () => {
  afterEach(() => vi.unstubAllGlobals())
  it('asks for per-URI errors', async () => {
    const body = { download_urls: ['', 'https://x'], expires_at: null, errors: [{ code: 'bucket_not_registered', message: 'm' }, null] }
    const fn = vi.fn(async (_url: string, _init?: RequestInit) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fn)
    const { mediaApi } = await import('../../api/media')
    expect(await mediaApi.resolveUrls('e/p', ['s3://u/a.png', 'https://x'])).toEqual(body)
    expect(fn.mock.calls[0][0]).toBe('/api/v1/files/resolve')
    expect(JSON.parse(String(fn.mock.calls[0][1]?.body))).toEqual({ project_id: 'e/p', uris: ['s3://u/a.png', 'https://x'], per_uri_errors: true })
  })
})
