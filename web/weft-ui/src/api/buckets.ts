// Reference buckets ("bring your own bucket"): customer S3 / GCS buckets whose
// objects /api/v1/files/resolve presigns. See docs/weft/API.md "Reference buckets".
import { get, post } from './client'

export type BucketProvider = 's3' | 'gcs'

export interface ReferenceBucket {
  id: string
  scope: 'entity' | 'project'
  entity: string
  project_id: string | null
  provider: BucketProvider
  bucket: string
  prefix: string
  region: string
  endpoint: string
  path_style: boolean | null
  secret_name: string
  test_object_key: string
  verified_at: string | null
  created_at: string
  created_by: string | null
}

/** Body of /reference_buckets/create and /reference_buckets/test. */
export interface ReferenceBucketReq {
  entity?: string
  project_id?: string
  provider: BucketProvider
  bucket: string
  prefix?: string
  region?: string
  endpoint?: string
  path_style?: boolean
  secret_name?: string
  test_object_key: string
  skip_test?: boolean
}

/** One part of a bucket test; `origin` is the browser origin the CORS check used. */
export interface BucketCheck {
  ok: boolean
  error: string | null
  origin?: string
}

/**
 * /reference_buckets/test result: ok when the test object could be read
 * (`read`) and the bucket's CORS rules let the Weft origin GET it (`cors`: a
 * preflight plus the GET's Access-Control-Allow-Origin). `read` is null when
 * the test could not start, `cors` when the bucket could not be reached.
 */
export interface BucketTestResult {
  ok: boolean
  error: string | null
  read?: BucketCheck | null
  cors?: BucketCheck | null
}

export const bucketsApi = {
  /** The buckets a project may use: its own and its team's. */
  list: (project_id: string) => get<{ buckets: ReferenceBucket[] }>(`/reference_buckets/list?project_id=${encodeURIComponent(project_id)}`),
  create: (req: ReferenceBucketReq) => post<{ bucket: ReferenceBucket }>('/reference_buckets/create', req),
  test: (req: ReferenceBucketReq) => post<BucketTestResult>('/reference_buckets/test', req),
  delete: (id: string) => post<object>('/reference_buckets/delete', { id }),
}

/** The URI prefix a registration covers, e.g. s3://bucket/prefix/. */
export function bucketUri(b: Pick<ReferenceBucket, 'provider' | 'bucket' | 'prefix'>): string {
  return `${b.provider === 'gcs' ? 'gs' : 's3'}://${b.bucket}/${b.prefix ?? ''}`
}

export interface BucketDraft {
  scope: 'entity' | 'project'
  provider: BucketProvider
  bucket: string
  prefix: string
  region: string
  endpoint: string
  pathStyle: boolean
  secretName: string
  testObjectKey: string
}

export const emptyDraft = (): BucketDraft => ({
  scope: 'entity',
  provider: 's3',
  bucket: '',
  prefix: '',
  region: '',
  endpoint: '',
  pathStyle: false,
  secretName: '',
  testObjectKey: '',
})

/** The request for a draft: trims fields, drops S3-only fields for GCS, and scopes to the team or project. */
export function bucketRequest(d: BucketDraft, projectId: string): ReferenceBucketReq {
  const s3 = d.provider === 's3'
  const req: ReferenceBucketReq = {
    provider: d.provider,
    bucket: d.bucket.trim().replace(/^(s3|gs):\/\//, '').replace(/\/+$/, ''),
    test_object_key: d.testObjectKey.trim().replace(/^\/+/, ''),
  }
  if (d.scope === 'project') req.project_id = projectId
  else req.entity = projectId.split('/')[0]
  const prefix = d.prefix.trim().replace(/^\/+/, '')
  if (prefix) req.prefix = prefix
  if (s3 && d.region.trim()) req.region = d.region.trim()
  if (d.endpoint.trim()) req.endpoint = d.endpoint.trim()
  if (s3 && d.pathStyle) req.path_style = true
  if (d.secretName) req.secret_name = d.secretName
  return req
}

/** Client-side checks before calling the server (which validates again). */
export function draftProblem(d: BucketDraft): string | null {
  const bucket = d.bucket.trim().replace(/^(s3|gs):\/\//, '').replace(/\/+$/, '')
  if (!bucket) return 'Bucket name is required.'
  if (!/^[a-z0-9][a-z0-9._-]{1,220}[a-z0-9]$/.test(bucket)) return 'Bucket name: lowercase letters, digits, dots, dashes and underscores only.'
  if (!d.testObjectKey.trim()) return 'Test object key is required: the key of an object that exists in the bucket.'
  const prefix = d.prefix.trim().replace(/^\/+/, '')
  if (prefix && !d.testObjectKey.trim().replace(/^\/+/, '').startsWith(prefix)) return 'The test object key must start with the prefix.'
  if (d.endpoint.trim() && !/^https?:\/\//.test(d.endpoint.trim())) return 'Endpoint must be an http(s) URL.'
  if (d.provider === 'gcs' && !d.secretName) return 'GCS buckets need a secret holding a service-account key.'
  return null
}
