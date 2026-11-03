// Settings → Buckets: reference buckets ("bring your own bucket"). Media whose
// URI points into a registered S3 or GCS bucket renders inline in traces and
// agent conversations, presigned by /files/resolve_url. Registering, testing
// and deleting need a server admin (the endpoints enforce it); everyone with
// access to the project can see what is registered.
import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { bucketRequest, bucketsApi, bucketUri, draftProblem, emptyDraft, type BucketDraft, type BucketTestResult, type ReferenceBucket } from '../../api/buckets'
import { iamApi } from '../../api/iam'
import { ErrorBox, Loading, Modal, Section } from '../../components/common'
import { ConfirmButton } from '../../components/objects/bits'
import { uriResolver } from '../../components/media/useResolvedUri'
import { fmtRelative } from '../../lib/format'

type TestState = { busy: true } | ({ busy: false } & BucketTestResult)

/** The read and CORS parts of a test result, as short lines. */
export function TestResult({ r }: { r: BucketTestResult }) {
  const parts: { label: string; ok: boolean; error: string | null }[] = []
  if (r.read) parts.push({ label: 'Read', ok: r.read.ok, error: r.read.error })
  if (r.cors) parts.push({ label: `CORS for ${r.cors.origin ?? 'this origin'}`, ok: r.cors.ok, error: r.cors.error?.replace(/^CORS: /, '') ?? null })
  if (!parts.length) return <span style={{ color: 'var(--error)' }}>{r.error ?? 'test failed'}</span>
  return (
    <div className="col" style={{ gap: 2 }}>
      {parts.map((p) =>
        p.ok ? (
          <span key={p.label}>
            <span className="badge success">{p.label}: passed</span>
          </span>
        ) : (
          <span key={p.label} style={{ color: 'var(--error)' }}>
            {p.label}: {p.error ?? 'failed'}
          </span>
        ),
      )}
    </div>
  )
}

/** The request that re-tests a saved registration. */
export function retestRequest(b: ReferenceBucket) {
  return {
    ...(b.project_id ? { project_id: b.project_id } : { entity: b.entity }),
    provider: b.provider,
    bucket: b.bucket,
    prefix: b.prefix || undefined,
    region: b.region || undefined,
    endpoint: b.endpoint || undefined,
    path_style: b.path_style ?? undefined,
    secret_name: b.secret_name || undefined,
    test_object_key: b.test_object_key,
  }
}

export function BucketsTable({
  buckets,
  canManage,
  tests,
  onTest,
  onDelete,
}: {
  buckets: ReferenceBucket[]
  canManage: boolean
  tests: Record<string, TestState | undefined>
  onTest: (b: ReferenceBucket) => void
  onDelete: (b: ReferenceBucket) => Promise<unknown>
}) {
  if (!buckets.length) return <div className="faint small">No buckets are registered for this project or its team.</div>
  return (
    <div className="table-wrap">
      <table className="table">
        <thead>
          <tr>
            <th>URI prefix</th>
            <th>Scope</th>
            <th>Location</th>
            <th>Credentials</th>
            <th>Status</th>
            {canManage && <th style={{ width: 140 }} />}
          </tr>
        </thead>
        <tbody>
          {buckets.map((b) => {
            const t = tests[b.id]
            return (
              <tr key={b.id}>
                <td className="mono small" style={{ overflowWrap: 'anywhere' }}>
                  {bucketUri(b)}
                </td>
                <td>
                  {b.scope === 'project' ? (
                    <span className="badge" title={b.project_id ?? ''}>
                      project
                    </span>
                  ) : (
                    <span className="badge accent" title={`every project of team ${b.entity}`}>
                      team {b.entity}
                    </span>
                  )}
                </td>
                <td className="small">
                  {b.provider === 'gcs' ? 'Google Cloud Storage' : 'S3'}
                  {b.region && <span className="muted"> · {b.region}</span>}
                  {b.endpoint && (
                    <div className="mono faint" style={{ overflowWrap: 'anywhere' }}>
                      {b.endpoint}
                      {b.path_style ? ' (path-style)' : ''}
                    </div>
                  )}
                </td>
                <td className="mono small">{b.secret_name || <span className="faint">server environment</span>}</td>
                <td className="small">
                  {t?.busy ? (
                    <span className="faint">testing…</span>
                  ) : t ? (
                    t.ok ? (
                      <span className="badge success" title={t.cors?.origin ? `read and CORS for ${t.cors.origin} passed` : undefined}>
                        test passed
                      </span>
                    ) : (
                      <TestResult r={t} />
                    )
                  ) : b.verified_at ? (
                    <span className="badge success" title={b.verified_at}>
                      verified {fmtRelative(b.verified_at)}
                    </span>
                  ) : (
                    <span className="badge warn">not tested</span>
                  )}
                </td>
                {canManage && (
                  <td>
                    <div className="row" style={{ justifyContent: 'flex-end' }}>
                      <button className="btn sm" disabled={t?.busy} onClick={() => onTest(b)} title={`Read ${b.test_object_key} through a presigned URL and check the bucket's CORS rules for this origin`}>
                        Test
                      </button>
                      <ConfirmButton
                        className="btn sm ghost danger"
                        title={`Remove ${bucketUri(b)}?`}
                        body="Media in this bucket stops rendering; the URIs show as text again. Nothing in the bucket is deleted."
                        confirmLabel="Remove"
                        onConfirm={() => onDelete(b)}
                      >
                        Remove
                      </ConfirmButton>
                    </div>
                  </td>
                )}
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

export function BucketsTab({ projectId }: { projectId: string }) {
  const qc = useQueryClient()
  const me = useQuery({ queryKey: ['auth-me'], queryFn: iamApi.me, retry: false })
  const list = useQuery({ queryKey: ['reference-buckets', projectId], queryFn: () => bucketsApi.list(projectId).then((r) => r.buckets), retry: false })
  const [adding, setAdding] = useState(false)
  const [tests, setTests] = useState<Record<string, TestState | undefined>>({})
  const admin = me.data?.server_admin === true
  const entity = projectId.split('/')[0]
  const origin = typeof window !== 'undefined' ? window.location.origin : ''
  const changed = () => {
    uriResolver.invalidate()
    void qc.invalidateQueries({ queryKey: ['reference-buckets', projectId] })
  }
  const test = async (b: ReferenceBucket) => {
    setTests((t) => ({ ...t, [b.id]: { busy: true } }))
    try {
      const r = await bucketsApi.test(retestRequest(b))
      setTests((t) => ({ ...t, [b.id]: { busy: false, ...r } }))
    } catch (e) {
      setTests((t) => ({ ...t, [b.id]: { busy: false, ok: false, error: e instanceof Error ? e.message : String(e) } }))
    }
  }
  return (
    <div className="col" style={{ gap: 12 }}>
      <Section
        title="Reference buckets"
        actions={
          admin ? (
            <button className="btn primary sm" onClick={() => setAdding(true)}>
              + Register bucket
            </button>
          ) : null
        }
      >
        <div className="col" style={{ gap: 10 }}>
          <div className="hint">
            Media that already lives in your own S3 or Google Cloud Storage bucket can be logged by URI: put{' '}
            <span className="mono">s3://bucket/key.png</span> or <span className="mono">gs://bucket/key.mp4</span> as a value in any call input, output
            or agent tool result. Once the bucket is registered here, for team <span className="mono">{entity}</span> or for this project, images,
            video, audio and PDFs render inline through short-lived presigned URLs; other files show as a download link. The bytes never pass through
            Weft. Until then the URIs show as text.
          </div>
          <div className="hint">
            The bucket's CORS rules must allow <span className="mono">GET</span> and <span className="mono">HEAD</span> from this Weft origin (
            <span className="mono">{origin}</span>). Registering and <b>Test</b> read the test object and check those rules (a preflight from this origin
            and the object's <span className="mono">Access-Control-Allow-Origin</span>), as Weave does.
          </div>
          {!admin && me.data && <div className="hint">Registering, testing and removing buckets needs a server admin.</div>}
          <ErrorBox error={list.error} />
          {list.isLoading ? (
            <Loading />
          ) : (
            list.data && (
              <BucketsTable
                buckets={list.data}
                canManage={admin}
                tests={tests}
                onTest={(b) => void test(b)}
                onDelete={async (b) => {
                  await bucketsApi.delete(b.id)
                  changed()
                }}
              />
            )
          )}
        </div>
      </Section>
      {adding && (
        <BucketModal
          projectId={projectId}
          onClose={() => setAdding(false)}
          onSaved={() => {
            setAdding(false)
            changed()
          }}
        />
      )}
    </div>
  )
}

function BucketModal({ projectId, onClose, onSaved }: { projectId: string; onClose: () => void; onSaved: () => void }) {
  const [d, setD] = useState<BucketDraft>(emptyDraft)
  const [busy, setBusy] = useState<'test' | 'save' | null>(null)
  const [err, setErr] = useState<unknown>(null)
  const [result, setResult] = useState<BucketTestResult | null>(null)
  const [skipTest, setSkipTest] = useState(false)
  const secrets = useQuery({ queryKey: ['secrets'], queryFn: () => api.secretsList().then((r) => r.secrets), retry: false })
  const set = (p: Partial<BucketDraft>) => {
    setD((x) => ({ ...x, ...p }))
    setResult(null)
  }
  const problem = draftProblem(d)
  const s3 = d.provider === 's3'
  const entity = projectId.split('/')[0]
  const run = async (kind: 'test' | 'save') => {
    setBusy(kind)
    setErr(null)
    try {
      const req = bucketRequest(d, projectId)
      if (kind === 'test') setResult(await bucketsApi.test(req))
      else {
        await bucketsApi.create({ ...req, skip_test: skipTest || undefined })
        onSaved()
      }
    } catch (e) {
      setErr(e)
    } finally {
      setBusy(null)
    }
  }
  return (
    <Modal
      title="Register a reference bucket"
      onClose={onClose}
      wide
      footer={
        <>
          <label className="checkbox small grow">
            <input type="checkbox" checked={skipTest} onChange={(e) => setSkipTest(e.target.checked)} /> Register without testing
          </label>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn" disabled={!!problem || !!busy} onClick={() => void run('test')}>
            {busy === 'test' ? 'Testing…' : 'Test connection'}
          </button>
          <button className="btn primary" disabled={!!problem || !!busy} onClick={() => void run('save')}>
            {busy === 'save' ? 'Registering…' : 'Register'}
          </button>
        </>
      }
    >
      <div className="grid-2">
        <label className="field">
          Scope
          <select className="input" value={d.scope} onChange={(e) => set({ scope: e.target.value as BucketDraft['scope'] })}>
            <option value="entity">Team {entity} (every project)</option>
            <option value="project">This project only</option>
          </select>
        </label>
        <label className="field">
          Provider
          <select className="input" value={d.provider} onChange={(e) => set({ provider: e.target.value as BucketDraft['provider'] })}>
            <option value="s3">Amazon S3 or S3-compatible (MinIO, R2, Ceph)</option>
            <option value="gcs">Google Cloud Storage</option>
          </select>
        </label>
        <label className="field">
          Bucket
          <input className="input mono" value={d.bucket} onChange={(e) => set({ bucket: e.target.value })} placeholder="my-media" autoFocus />
          <span className="hint">The name only, without {s3 ? 's3://' : 'gs://'}.</span>
        </label>
        <label className="field">
          Prefix (optional)
          <input className="input mono" value={d.prefix} onChange={(e) => set({ prefix: e.target.value })} placeholder="frames/" />
          <span className="hint">Only keys under it are presigned.</span>
        </label>
        {s3 && (
          <label className="field">
            Region (optional)
            <input className="input mono" value={d.region} onChange={(e) => set({ region: e.target.value })} placeholder="us-east-1" />
          </label>
        )}
        <label className="field">
          Endpoint (optional)
          <input
            className="input mono"
            value={d.endpoint}
            onChange={(e) => set({ endpoint: e.target.value })}
            placeholder={s3 ? 'https://s3.example.com (MinIO, R2)' : 'https://storage.googleapis.com'}
          />
          <span className="hint">{s3 ? 'For MinIO, R2, Ceph and other S3-compatible stores.' : 'Defaults to storage.googleapis.com.'}</span>
        </label>
        <label className="field">
          Credentials secret
          <select className="input mono" value={d.secretName} onChange={(e) => set({ secretName: e.target.value })}>
            <option value="">{s3 ? 'Server environment (AWS_ACCESS_KEY_ID …)' : 'Choose a secret…'}</option>
            {(secrets.data ?? []).map((s) => (
              <option key={s.name} value={s.name}>
                {s.name}
                {s.source === 'env' ? ' (environment)' : ''}
              </option>
            ))}
          </select>
          <span className="hint">
            {s3 ? (
              <>
                A stored secret holding <span className="mono">{'{"access_key_id": …, "secret_access_key": …}'}</span>.
              </>
            ) : (
              <>A stored secret holding a service-account key file (JSON) with read access to the bucket.</>
            )}{' '}
            Add one under Secrets &amp; providers.
          </span>
        </label>
        <label className="field">
          Test object key
          <input className="input mono" value={d.testObjectKey} onChange={(e) => set({ testObjectKey: e.target.value })} placeholder="frames/000001.png" />
          <span className="hint">An object that exists. The test reads its first byte through a presigned URL.</span>
        </label>
        {s3 && (
          <label className="checkbox small" style={{ gridColumn: '1 / -1' }}>
            <input type="checkbox" checked={d.pathStyle} onChange={(e) => set({ pathStyle: e.target.checked })} /> Path-style addressing (endpoint/bucket/key),
            usually needed for MinIO and Ceph
          </label>
        )}
      </div>
      <div className="hint" style={{ marginTop: 8 }}>
        Media will render for URIs under <span className="mono">{bucketUri({ provider: d.provider, bucket: d.bucket.trim() || 'bucket', prefix: d.prefix.trim() })}</span>.
        The test reads the test object and checks that the bucket's CORS rules allow GET from this origin (
        <span className="mono">{typeof window !== 'undefined' ? window.location.origin : ''}</span>); allow GET and HEAD.
      </div>
      {problem && (d.bucket || d.testObjectKey) && <div className="hint" style={{ color: 'var(--warn)' }}>{problem}</div>}
      {result &&
        (result.ok ? (
          <div className="hint" style={{ color: 'var(--success)' }}>
            Connection test passed: the test object was read{result.cors?.origin ? ` and CORS allows ${result.cors.origin}` : ''}.
          </div>
        ) : (
          <div className="error-box">
            Connection test failed: <TestResult r={result} />
          </div>
        ))}
      <ErrorBox error={err} />
    </Modal>
  )
}
