// Settings → Buckets: reference buckets ("bring your own bucket"). Media whose
// URI points into a registered S3 or GCS bucket renders inline in traces and
// agent conversations, presigned by /api/v1/files/resolve. Registering, testing
// and removing need a server admin (the endpoints enforce it); everyone with
// access to the project can see what is registered.
import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { bucketRequest, bucketsApi, bucketUri, draftProblem, emptyDraft, type BucketDraft, type BucketTestResult, type ReferenceBucket } from '../../api/buckets'
import { iamApi } from '../../api/iam'
import { ErrorBox, Modal, Spinner } from '../../components/common'
import { ConfirmButton } from '../../components/objects/bits'
import { SettingsEmpty, SettingsRow, SettingsSection, SettingsSentence, StatusWords } from '../../components/settings/SettingsList'
import { uriResolver } from '../../components/media/useResolvedUri'
import { fmtDate } from '../../lib/format'
import { relativeTime } from '../../lib/summaries'
import { bucketsSentence } from '../../lib/summaries/project'
import { HelpLink } from '../../components/ui/PageHeader'
import { Icon } from '../../components/ui/Icons'

type TestState = { busy: true } | ({ busy: false } & BucketTestResult)

/** The read and CORS parts of a test result, as short lines with a glyph each. */
export function TestResult({ r }: { r: BucketTestResult }) {
  const parts: { label: string; ok: boolean; error: string | null }[] = []
  if (r.read) parts.push({ label: 'Read', ok: r.read.ok, error: r.read.error })
  if (r.cors) parts.push({ label: `CORS for ${r.cors.origin ?? 'this origin'}`, ok: r.cors.ok, error: r.cors.error?.replace(/^CORS: /, '') ?? null })
  if (!parts.length) return <StatusWords tone="error">{r.error ?? 'test failed'}</StatusWords>
  return (
    <span className="col" style={{ gap: 2, alignItems: 'flex-start' }}>
      {parts.map((p) =>
        p.ok ? (
          <StatusWords key={p.label} tone="ok">
            {p.label}: passed
          </StatusWords>
        ) : (
          <StatusWords key={p.label} tone="error">
            {p.label}: {p.error ?? 'failed'}
          </StatusWords>
        ),
      )}
    </span>
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

/** The registered buckets as settings rows: where, for whom, how it signs in, the last test; Test and Remove for admins. */
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
  if (!buckets.length) return <SettingsEmpty>Register a bucket to show the media stored in it inline.</SettingsEmpty>
  return (
    <>
      {buckets.map((b) => {
        const t = tests[b.id]
        return (
          <SettingsRow
            key={b.id}
            label={<span className="mono">{bucketUri(b)}</span>}
            detail={
              <>
                <span title={b.scope === 'project' ? (b.project_id ?? '') : `every project of team ${b.entity}`}>{b.scope === 'project' ? 'This project' : `team ${b.entity}`}</span>
                {' · '}
                {b.provider === 'gcs' ? 'Google Cloud Storage' : 'S3'}
                {b.region && ` · ${b.region}`}
                {b.endpoint && (
                  <>
                    {' · '}
                    <span className="mono">{b.endpoint}</span>
                    {b.path_style ? ' (path-style)' : ''}
                  </>
                )}
                {' · '}
                {b.secret_name ? (
                  <>
                    credentials in <span className="mono">{b.secret_name}</span>
                  </>
                ) : (
                  'credentials from the server environment (AWS)'
                )}
              </>
            }
            control={
              <>
                {t?.busy ? (
                  <span className="quiet-note">testing…</span>
                ) : t ? (
                  t.ok ? (
                    <span title={t.cors?.origin ? `read and CORS for ${t.cors.origin} passed` : undefined}>
                      <StatusWords tone="ok">test passed</StatusWords>
                    </span>
                  ) : (
                    <TestResult r={t} />
                  )
                ) : b.verified_at ? (
                  <span title={fmtDate(b.verified_at)}>
                    <StatusWords tone="ok">verified {relativeTime(b.verified_at)}</StatusWords>
                  </span>
                ) : (
                  <StatusWords tone="warn">not tested</StatusWords>
                )}
                {canManage && (
                  <>
                    <button type="button" className="qbtn sm" disabled={t?.busy} onClick={() => onTest(b)} title={`Read ${b.test_object_key} through a presigned URL and check the bucket's CORS rules for this origin`}>
                      Test
                    </button>
                    <ConfirmButton
                      className="qbtn sm plain danger"
                      title={`Remove ${bucketUri(b)}?`}
                      body="Media in this bucket stops rendering; the URIs show as text again. Nothing in the bucket is deleted."
                      confirmLabel="Remove"
                      ariaLabel={`Remove ${bucketUri(b)}`}
                      tooltip="Remove this bucket"
                      onConfirm={() => onDelete(b)}
                    >
                      <Icon name="trash" size={14} />
                    </ConfirmButton>
                  </>
                )}
              </>
            }
          />
        )
      })}
    </>
  )
}

export function BucketsTab({ projectId }: { projectId: string }) {
  const qc = useQueryClient()
  const me = useQuery({ queryKey: ['auth-me'], queryFn: iamApi.me, retry: false })
  const list = useQuery({ queryKey: ['reference-buckets', projectId], queryFn: () => bucketsApi.list(projectId).then((r) => r.buckets), retry: false })
  const [adding, setAdding] = useState(false)
  const [tests, setTests] = useState<Record<string, TestState | undefined>>({})
  const admin = me.data?.server_admin === true
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
    <>
      {list.data && <SettingsSentence>{bucketsSentence(list.data)}</SettingsSentence>}
      <SettingsSection
        title={
          <>
            Reference buckets <HelpLink doc="project.md#buckets" topic="Buckets" />
          </>
        }
        action={
          admin ? (
            <button type="button" className="qbtn primary" onClick={() => setAdding(true)}>
              Register a bucket
            </button>
          ) : null
        }
        note={
          <>
            Its CORS rules must allow GET and HEAD from <span className="mono">{origin}</span>.{!admin && me.data && ' Registering buckets needs a server admin.'}
          </>
        }
      >
        {list.error ? (
          <SettingsEmpty>
            <ErrorBox error={list.error} />
          </SettingsEmpty>
        ) : list.isLoading ? (
          <SettingsEmpty>
            <Spinner />
          </SettingsEmpty>
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
      </SettingsSection>
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
    </>
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
          <button type="button" className="qbtn" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="qbtn" disabled={!!problem || !!busy} onClick={() => void run('test')}>
            {busy === 'test' ? 'Testing…' : 'Test connection'}
          </button>
          <button type="button" className="qbtn primary" disabled={!!problem || !!busy} onClick={() => void run('save')}>
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
            <option value="">{s3 ? 'Server environment (only if the server allows it, AWS only)' : 'Choose a secret…'}</option>
            {(secrets.data ?? [])
              .filter((s) => s.source !== 'env')
              .map((s) => (
                <option key={s.name} value={s.name}>
                  {s.name}
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
