import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { bucketRequest, bucketsApi, bucketUri, draftProblem, emptyDraft, type ReferenceBucket } from '../../api/buckets'
import { BucketsTable, retestRequest, TestResult } from './BucketsTab'

const bucket: ReferenceBucket = {
  id: 'rb1',
  scope: 'entity',
  entity: 'demo',
  project_id: null,
  provider: 's3',
  bucket: 'cust-media',
  prefix: 'frames/',
  region: 'eu-west-1',
  endpoint: 'http://127.0.0.1:9000',
  path_style: true,
  secret_name: 'MEDIA_S3',
  test_object_key: 'frames/000001.png',
  verified_at: '2026-11-02T10:00:00Z',
  created_at: '2026-11-02T10:00:00Z',
  created_by: 'admin',
}

function mockFetch(body: unknown, status = 200) {
  const fn = vi.fn(async (_url: string, _init?: RequestInit) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fn)
  return fn
}

afterEach(() => vi.unstubAllGlobals())

describe('reference bucket API calls', () => {
  it('lists the buckets of a project', async () => {
    const f = mockFetch({ buckets: [bucket] })
    expect(await bucketsApi.list('demo/support-bot')).toEqual({ buckets: [bucket] })
    expect(f.mock.calls[0][0]).toBe('/reference_buckets/list?project_id=demo%2Fsupport-bot')
  })
  it('creates, tests and deletes with JSON bodies', async () => {
    const f = mockFetch({ ok: false, error: 'test object s3://cust-media/frames/x was not found' })
    const req = bucketRequest({ ...emptyDraft(), bucket: 's3://cust-media/', testObjectKey: '/frames/x', prefix: 'frames/' }, 'demo/support-bot')
    expect(await bucketsApi.test(req)).toEqual({ ok: false, error: 'test object s3://cust-media/frames/x was not found' })
    expect(f.mock.calls[0][0]).toBe('/reference_buckets/test')
    expect(f.mock.calls[0][1]?.method).toBe('POST')
    expect(JSON.parse(String(f.mock.calls[0][1]?.body))).toEqual({
      provider: 's3',
      bucket: 'cust-media',
      prefix: 'frames/',
      test_object_key: 'frames/x',
      entity: 'demo',
    })
    mockFetch({ bucket })
    await bucketsApi.create({ ...req, skip_test: true })
    const g = mockFetch({})
    await bucketsApi.delete('rb1')
    expect(g.mock.calls[0][0]).toBe('/reference_buckets/delete')
    expect(JSON.parse(String(g.mock.calls[0][1]?.body))).toEqual({ id: 'rb1' })
  })
  it('surfaces the server refusal for non-admins', async () => {
    mockFetch({ reason: 'registering reference buckets needs a server admin' }, 403)
    await expect(bucketsApi.create(bucketRequest({ ...emptyDraft(), bucket: 'b-1', testObjectKey: 'k' }, 'e/p'))).rejects.toMatchObject({
      status: 403,
      message: 'registering reference buckets needs a server admin',
    })
  })
})

describe('bucket form', () => {
  it('builds project-scoped S3 and GCS requests', () => {
    expect(
      bucketRequest(
        { scope: 'project', provider: 's3', bucket: 'b-1', prefix: '', region: ' us-west-2 ', endpoint: 'http://minio:9000', pathStyle: true, secretName: 'S3KEY', testObjectKey: 'a.png' },
        'e/p',
      ),
    ).toEqual({ provider: 's3', bucket: 'b-1', test_object_key: 'a.png', project_id: 'e/p', region: 'us-west-2', endpoint: 'http://minio:9000', path_style: true, secret_name: 'S3KEY' })
    // Region and path-style are S3-only.
    expect(bucketRequest({ ...emptyDraft(), provider: 'gcs', bucket: 'gs://g-1', region: 'x', pathStyle: true, secretName: 'SA', testObjectKey: 'k' }, 'e/p')).toEqual({
      provider: 'gcs',
      bucket: 'g-1',
      test_object_key: 'k',
      entity: 'e',
      secret_name: 'SA',
    })
  })
  it('validates drafts', () => {
    expect(draftProblem(emptyDraft())).toMatch(/Bucket name/)
    expect(draftProblem({ ...emptyDraft(), bucket: 'Bad Bucket', testObjectKey: 'k' })).toMatch(/lowercase/)
    expect(draftProblem({ ...emptyDraft(), bucket: 'b-1' })).toMatch(/Test object key/)
    expect(draftProblem({ ...emptyDraft(), bucket: 'b-1', prefix: 'frames/', testObjectKey: 'other/k' })).toMatch(/prefix/)
    expect(draftProblem({ ...emptyDraft(), bucket: 'b-1', testObjectKey: 'k', endpoint: 'minio:9000' })).toMatch(/Endpoint/)
    expect(draftProblem({ ...emptyDraft(), provider: 'gcs', bucket: 'b-1', testObjectKey: 'k' })).toMatch(/GCS/)
    expect(draftProblem({ ...emptyDraft(), bucket: 'b-1', testObjectKey: 'k' })).toBeNull()
  })
  it('re-tests a saved registration with its own fields', () => {
    expect(retestRequest(bucket)).toEqual({
      entity: 'demo',
      provider: 's3',
      bucket: 'cust-media',
      prefix: 'frames/',
      region: 'eu-west-1',
      endpoint: 'http://127.0.0.1:9000',
      path_style: true,
      secret_name: 'MEDIA_S3',
      test_object_key: 'frames/000001.png',
    })
    expect(bucketUri(bucket)).toBe('s3://cust-media/frames/')
    expect(bucketUri({ provider: 'gcs', bucket: 'g', prefix: '' })).toBe('gs://g/')
  })
})

describe('BucketsTable', () => {
  const props = { buckets: [bucket, { ...bucket, id: 'rb2', scope: 'project' as const, project_id: 'demo/x', verified_at: null, secret_name: '' }], tests: {}, onTest: () => {}, onDelete: async () => {} }
  it('shows test and remove controls to server admins only', () => {
    const admin = renderToStaticMarkup(<BucketsTable {...props} canManage />)
    expect(admin).toContain('s3://cust-media/frames/')
    expect(admin).toContain('Test</button>')
    expect(admin).toContain('aria-label="Remove s3://cust-media/frames/"')
    expect(admin).toContain('not tested')
    expect(admin).toContain('server environment')
    const viewer = renderToStaticMarkup(<BucketsTable {...props} canManage={false} />)
    expect(viewer).toContain('team demo')
    expect(viewer).not.toContain('Test</button>')
    expect(viewer).not.toContain('aria-label="Remove')
  })
  it('shows test results', () => {
    const html = renderToStaticMarkup(<BucketsTable {...props} canManage tests={{ rb1: { busy: false, ok: false, error: 'reading the test object failed: 403' }, rb2: { busy: true } }} />)
    expect(html).toContain('reading the test object failed: 403')
    expect(html).toContain('testing…')
  })
  it('shows the read and CORS results of a test', () => {
    const tests = {
      rb1: {
        busy: false as const,
        ok: false,
        error: 'CORS: the bucket refused a preflight from https://weft.example.com: 403 CORSResponse: CORS is not enabled for this bucket.',
        read: { ok: true, error: null },
        cors: {
          ok: false,
          origin: 'https://weft.example.com',
          error: 'CORS: the bucket refused a preflight from https://weft.example.com: 403 CORSResponse: CORS is not enabled for this bucket.',
        },
      },
      rb2: { busy: false as const, ok: true, error: null, read: { ok: true, error: null }, cors: { ok: true, error: null, origin: 'https://weft.example.com' } },
    }
    const html = renderToStaticMarkup(<BucketsTable {...props} canManage tests={tests} />)
    expect(html).toContain('Read: passed')
    expect(html).toContain('CORS for https://weft.example.com: the bucket refused a preflight')
    expect(html).not.toContain('https://weft.example.com: CORS:')
    expect(html).toContain('test passed')
    expect(html).toContain('read and CORS for https://weft.example.com passed')
    // A test that could not start shows its error.
    const failed = renderToStaticMarkup(<TestResult r={{ ok: false, error: 'secret "X" does not exist (Settings → Secrets)', read: null, cors: null }} />)
    expect(failed).toContain('secret &quot;X&quot; does not exist')
  })
})
