// Settings tabs for administration: managed API keys, data retention,
// ingest sampling, sensitive-data redaction and project deletion.
import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { adminApi, type ApiKeyCreate, type ManagedApiKey, type RedactionPattern, type SensitiveDataSettings } from '../../api/admin'
import { getApiKey, setApiKey } from '../../api/client'
import { CopyButton, Empty, ErrorBox, Loading, Modal, Section, Spinner } from '../../components/common'
import { ConfirmButton, Switch } from '../../components/objects/bits'
import { fmtNumber, fmtRelative } from '../../lib/format'

function usePrincipal() {
  return useQuery({ queryKey: ['auth-principal', getApiKey()], queryFn: adminApi.principal, retry: false })
}

function AdminNote({ admin }: { admin: boolean | undefined }) {
  if (admin !== false) return null
  return <div className="hint">Changing this needs an admin API key. The key in this browser is not one.</div>
}

// ================================================================ API keys

export function ApiKeysTab({ projectId }: { projectId: string }) {
  const qc = useQueryClient()
  const principal = usePrincipal()
  const [showRevoked, setShowRevoked] = useState(false)
  const [creating, setCreating] = useState(false)
  const [created, setCreated] = useState<{ key: ManagedApiKey; secret: string; stored: boolean } | null>(null)
  const keys = useQuery({ queryKey: ['api-keys', showRevoked], queryFn: () => adminApi.apiKeys(showRevoked), retry: false })
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ['api-keys'] })
    void qc.invalidateQueries({ queryKey: ['auth-principal'] })
    void qc.invalidateQueries({ queryKey: ['server-info'] })
  }
  const p = principal.data
  return (
    <div className="col" style={{ gap: 12 }}>
      <Section
        title="API keys"
        actions={
          <div className="row">
            <label className="checkbox small">
              <input type="checkbox" checked={showRevoked} onChange={(e) => setShowRevoked(e.target.checked)} /> Show revoked
            </label>
            <button className="btn primary sm" disabled={p?.admin === false && !p?.rbac} onClick={() => setCreating(true)}>
              + Create key
            </button>
          </div>
        }
      >
        <div className="col" style={{ gap: 10 }}>
          <div className="hint">
            Keys are stored hashed; the secret is shown once when you create a key. A key can be limited to projects and made read-only. Admin keys can
            also manage keys, secrets and project settings. Keys passed with <span className="mono">--api-keys</span> keep working and count as admin. A key
            whose user is a registered user (Users &amp; teams) carries that user's team and project roles; without admin rights you see and create
            only your own keys.
          </div>
          {p && (
            <div className="kv">
              <div className="k">signed in as</div>
              <div className="v">
                <span className="mono">{p.user_id}</span>
                {p.key_name && <span className="muted"> · {p.key_name}</span>}{' '}
                {p.admin ? <span className="badge accent">admin</span> : p.read_only ? <span className="badge">read-only</span> : <span className="badge">read-write</span>}
              </div>
              <div className="k">server auth</div>
              <div className="v">
                {p.auth_required ? 'required' : 'not required'}
                {keys.data && keys.data.static_key_count > 0 && <span className="muted"> · {keys.data.static_key_count} static key(s) from --api-keys</span>}
              </div>
            </div>
          )}
          {p && !p.auth_required && (
            <div className="hint" style={{ color: 'var(--warn)' }}>
              The server is open. Creating the first key turns authentication on, so the first key must be an admin key; it is saved in this browser automatically.
            </div>
          )}
          <ErrorBox error={keys.error} />
          {keys.isLoading ? (
            <Loading />
          ) : !keys.data || keys.data.keys.length === 0 ? (
            keys.data && <Empty>No managed keys yet.</Empty>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>Key</th>
                    <th>User</th>
                    <th>Scope</th>
                    <th>Access</th>
                    <th>Created</th>
                    <th>Last used</th>
                    <th style={{ width: 90 }} />
                  </tr>
                </thead>
                <tbody>
                  {keys.data.keys.map((k) => (
                    <tr key={k.id} style={k.revoked_at ? { opacity: 0.55 } : undefined}>
                      <td>
                        {k.name}
                        {k.id === p?.key_id && <span className="badge success" style={{ marginLeft: 6 }}>this browser</span>}
                      </td>
                      <td className="mono small">{k.prefix}…</td>
                      <td className="mono small">{k.user_id}</td>
                      <td>
                        {k.all_projects ? (
                          <span className="muted small">all projects</span>
                        ) : (
                          <span className="chips">
                            {k.projects.map((x) => (
                              <span key={x} className="chip mono">
                                {x}
                              </span>
                            ))}
                          </span>
                        )}
                      </td>
                      <td>{k.admin ? <span className="badge accent">admin</span> : k.read_only ? <span className="badge">read-only</span> : <span className="badge">read-write</span>}</td>
                      <td className="muted small" title={k.created_at}>
                        {fmtRelative(k.created_at)}
                        {k.created_by && ` by ${k.created_by}`}
                      </td>
                      <td className="muted small" title={k.last_used_at ?? ''}>
                        {k.last_used_at ? fmtRelative(k.last_used_at) : 'never'}
                      </td>
                      <td>
                        {k.revoked_at ? (
                          <span className="badge error" title={k.revoked_at}>
                            revoked
                          </span>
                        ) : (
                          <ConfirmButton
                            className="btn sm ghost danger"
                            title={`Revoke "${k.name}"?`}
                            body="Requests with this key fail immediately. This cannot be undone."
                            confirmLabel="Revoke"
                            disabled={p?.admin === false && !p?.rbac}
                            onConfirm={async () => {
                              await adminApi.apiKeyRevoke(k.id)
                              refresh()
                            }}
                          >
                            Revoke
                          </ConfirmButton>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </Section>
      {creating && (
        <CreateKeyModal
          projectId={projectId}
          firstKey={!!p && !p.auth_required}
          selfOnly={!!p && !p.admin}
          onClose={() => setCreating(false)}
          onCreated={(r) => {
            // On an open server the first key turns auth on: keep the UI signed in.
            const store = !!p && !p.auth_required
            if (store) setApiKey(r.secret)
            setCreating(false)
            setCreated({ ...r, stored: store })
            refresh()
          }}
        />
      )}
      {created && <SecretModal created={created} onClose={() => setCreated(null)} onUse={refresh} />}
    </div>
  )
}

function CreateKeyModal({
  projectId,
  firstKey,
  selfOnly,
  onClose,
  onCreated,
}: {
  projectId: string
  firstKey: boolean
  selfOnly?: boolean
  onClose: () => void
  onCreated: (r: { key: ManagedApiKey; secret: string }) => void
}) {
  const [name, setName] = useState('')
  const [user, setUser] = useState('')
  const [scope, setScope] = useState<'all' | 'this' | 'list'>('all')
  const [list, setList] = useState(projectId)
  const [access, setAccess] = useState<'admin' | 'write' | 'read'>(firstKey ? 'admin' : 'write')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  const projects = scope === 'all' ? [] : scope === 'this' ? [projectId] : list.split(/[\s,]+/).filter(Boolean)
  const valid = name.trim() && (scope === 'all' || projects.length > 0) && !(access === 'admin' && scope !== 'all')
  return (
    <Modal
      title="Create API key"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button
            className="btn primary"
            disabled={!valid || busy}
            onClick={async () => {
              setBusy(true)
              setErr(null)
              try {
                const req: ApiKeyCreate = { name: name.trim(), projects, admin: access === 'admin', read_only: access === 'read' }
                if (user.trim()) req.user_id = user.trim()
                onCreated(await adminApi.apiKeyCreate(req))
              } catch (e) {
                setErr(e)
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? 'Creating…' : 'Create key'}
          </button>
        </>
      }
    >
      <div className="form-grid">
        <label className="field">
          Name
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="CI ingestion" autoFocus />
        </label>
        <label className="field" hidden={selfOnly}>
          User (recorded as user_id)
          <input className="input mono" value={user} onChange={(e) => setUser(e.target.value.replace(/[^\w.@-]/g, ''))} placeholder="default" />
        </label>
        <label className="field">
          Projects
          <select className="input" value={scope} onChange={(e) => setScope(e.target.value as typeof scope)}>
            <option value="all">All projects</option>
            <option value="this">This project ({projectId})</option>
            <option value="list">Choose projects…</option>
          </select>
        </label>
        <label className="field">
          Access
          <select className="input" value={access} onChange={(e) => setAccess(e.target.value as typeof access)}>
            <option value="write" disabled={firstKey}>
              Read and write
            </option>
            <option value="read" disabled={firstKey}>
              Read-only
            </option>
            {!selfOnly && <option value="admin">Admin (all projects, manages keys and settings)</option>}
          </select>
        </label>
      </div>
      {scope === 'list' && (
        <label className="field">
          Project ids (entity/project), separated by commas or spaces
          <input className="input mono" value={list} onChange={(e) => setList(e.target.value)} />
        </label>
      )}
      {access === 'admin' && scope !== 'all' && <div className="hint" style={{ color: 'var(--error)' }}>Admin keys cover all projects.</div>}
      {firstKey && <div className="hint">This is the first key: it must be an admin key, and creating it turns authentication on.</div>}
      <ErrorBox error={err} />
    </Modal>
  )
}

function SecretModal({ created, onClose, onUse }: { created: { key: ManagedApiKey; secret: string; stored: boolean }; onClose: () => void; onUse: () => void }) {
  const [stored, setStored] = useState(created.stored)
  return (
    <Modal
      title={`Key "${created.key.name}" created`}
      onClose={onClose}
      footer={
        <button className="btn primary" onClick={onClose}>
          Done
        </button>
      }
    >
      <div className="col" style={{ gap: 10 }}>
        <div className="hint">Copy the secret now. It is stored hashed and cannot be shown again.</div>
        <div className="row">
          <code className="mono grow" data-testid="new-api-key" style={{ padding: '6px 8px', border: '1px solid var(--border)', borderRadius: 6, wordBreak: 'break-all' }}>
            {created.secret}
          </code>
          <CopyButton text={created.secret} />
        </div>
        <div className="hint mono">export WEFT_API_KEY={created.secret}</div>
        {stored ? (
          <span className="badge success">saved as this browser's key</span>
        ) : (
          <button
            className="btn sm"
            onClick={() => {
              setApiKey(created.secret)
              setStored(true)
              onUse()
            }}
          >
            Use this key in this browser
          </button>
        )}
      </div>
    </Modal>
  )
}

// ================================================================ retention

export function RetentionTab({ projectId }: { projectId: string }) {
  const qc = useQueryClient()
  const principal = usePrincipal()
  const ttl = useQuery({ queryKey: ['ttl', projectId], queryFn: () => adminApi.ttlRead(projectId) })
  const [days, setDays] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  const [purged, setPurged] = useState<string | null>(null)
  useEffect(() => setDays(ttl.data?.retention_days ? String(ttl.data.retention_days) : ''), [ttl.data])
  const save = async (v: number | null) => {
    setBusy(true)
    setErr(null)
    try {
      await adminApi.ttlUpdate(projectId, v)
      await qc.invalidateQueries({ queryKey: ['ttl', projectId] })
    } catch (e) {
      setErr(e)
    } finally {
      setBusy(false)
    }
  }
  const n = Number(days)
  const valid = days !== '' && Number.isInteger(n) && n >= 1 && n <= 36500
  const current = ttl.data?.retention_days ?? null
  return (
    <div className="col" style={{ gap: 12, maxWidth: 720 }}>
      <Section title="Data retention">
        <div className="col" style={{ gap: 10 }}>
          <div className="hint">
            Calls (and feedback on them) older than the retention period are deleted permanently by a background job that runs hourly. Objects, datasets
            and files are versioned assets and are kept.
          </div>
          {ttl.isLoading ? (
            <Spinner />
          ) : (
            <div className="kv">
              <div className="k">current policy</div>
              <div className="v">{current ? <span className="badge warn">delete after {current} days</span> : <span className="badge">keep forever</span>}</div>
            </div>
          )}
          <form
            className="row wrap"
            onSubmit={(e) => {
              e.preventDefault()
              if (valid) void save(n)
            }}
          >
            <label className="field">
              Retention (days)
              <input className="input mono" type="number" min={1} max={36500} value={days} onChange={(e) => setDays(e.target.value)} placeholder="e.g. 30" style={{ width: 140 }} />
            </label>
            <button className="btn primary" type="submit" disabled={!valid || busy || principal.data?.admin === false} style={{ alignSelf: 'flex-end' }}>
              Save
            </button>
            <button className="btn" type="button" disabled={!current || busy || principal.data?.admin === false} style={{ alignSelf: 'flex-end' }} onClick={() => void save(null)}>
              Keep forever
            </button>
            <ConfirmButton
              className="btn danger"
              title={`Purge data older than ${current} days now?`}
              body="Spans and their feedback older than the retention period are deleted permanently."
              confirmLabel="Purge"
              disabled={!current || principal.data?.admin === false}
              onConfirm={async () => {
                const r = await adminApi.ttlPurge(projectId)
                setPurged(`Deleted ${fmtNumber(r.purged.calls)} calls and ${fmtNumber(r.purged.feedback)} feedback items.`)
                void qc.invalidateQueries({ queryKey: ['project-stats', projectId] })
              }}
            >
              Purge now
            </ConfirmButton>
          </form>
          {purged && <div className="hint">{purged}</div>}
          <AdminNote admin={principal.data?.admin} />
          <ErrorBox error={err ?? ttl.error} />
        </div>
      </Section>
    </div>
  )
}

// ================================================================ sampling

export function SamplingTab({ projectId }: { projectId: string }) {
  const qc = useQueryClient()
  const principal = usePrincipal()
  const cur = useQuery({ queryKey: ['sampling', projectId], queryFn: () => adminApi.samplingRead(projectId) })
  const [pct, setPct] = useState(100)
  const [dry, setDry] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  const [saved, setSaved] = useState(false)
  useEffect(() => {
    if (cur.data) {
      setPct(Math.round(cur.data.sample_rate * 1000) / 10)
      setDry(cur.data.dry_run)
    }
  }, [cur.data])
  const dirty = !!cur.data && (Math.abs(cur.data.sample_rate * 100 - pct) > 1e-9 || cur.data.dry_run !== dry)
  return (
    <div className="col" style={{ gap: 12, maxWidth: 720 }}>
      <Section title="Ingest sampling">
        <div className="col" style={{ gap: 10 }}>
          <div className="hint">
            Keep only a share of incoming traces. The decision is made per trace by hashing its trace id, so a root call and all of its children are kept or
            dropped together, from any SDK or OpenTelemetry exporter. Dropped calls are never stored.
          </div>
          {cur.isLoading ? (
            <Spinner />
          ) : (
            <>
              <div className="row wrap" style={{ alignItems: 'center', gap: 12 }}>
                <input type="range" min={0} max={100} step={1} value={pct} onChange={(e) => setPct(Number(e.target.value))} style={{ width: 260 }} aria-label="Sample rate" />
                <input className="input mono" type="number" min={0} max={100} step={0.1} value={pct} onChange={(e) => setPct(Math.min(100, Math.max(0, Number(e.target.value))))} style={{ width: 90 }} />
                <span className="muted">% of traces kept</span>
              </div>
              <label className="checkbox">
                <Switch on={dry} onChange={setDry} /> Dry run: keep everything, but mark spans that would be dropped (attribute <span className="mono">weft_ingest_sampling</span>)
              </label>
              <div className="row">
                <button
                  className="btn primary"
                  disabled={!dirty || busy || principal.data?.admin === false}
                  onClick={async () => {
                    setBusy(true)
                    setErr(null)
                    setSaved(false)
                    try {
                      await adminApi.samplingUpdate(projectId, { sample_rate: pct / 100, dry_run: dry })
                      await qc.invalidateQueries({ queryKey: ['sampling', projectId] })
                      setSaved(true)
                    } catch (e) {
                      setErr(e)
                    } finally {
                      setBusy(false)
                    }
                  }}
                >
                  Save
                </button>
                {saved && !dirty && <span className="badge success">saved</span>}
              </div>
            </>
          )}
          <AdminNote admin={principal.data?.admin} />
          <ErrorBox error={err ?? cur.error} />
        </div>
      </Section>
    </div>
  )
}

// ================================================================ sensitive data

const SAMPLE_TEXT = 'Contact jane.doe@example.com or +1 415-555-0132. Card 4111 1111 1111 1111, SSN 123-45-6789, from 10.0.0.12 with key sk-proj-abcdefghijklmnopqrstuv.'

export function SensitiveDataTab({ projectId }: { projectId: string }) {
  const qc = useQueryClient()
  const principal = usePrincipal()
  const cur = useQuery({ queryKey: ['sensitive', projectId], queryFn: () => adminApi.sensitiveRead(projectId) })
  const [draft, setDraft] = useState<SensitiveDataSettings | null>(null)
  const [sample, setSample] = useState(SAMPLE_TEXT)
  const [preview, setPreview] = useState<string | null>(null)
  const [previewErr, setPreviewErr] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  const [saved, setSaved] = useState(false)
  useEffect(() => {
    if (cur.data) setDraft({ policy: cur.data.policy, entities: cur.data.entities, custom_patterns: cur.data.custom_patterns })
  }, [cur.data])
  const all = useMemo(() => cur.data?.available_entities ?? [], [cur.data])
  // An empty entity list means "all detectors".
  const enabled = (name: string) => !draft || draft.entities.length === 0 || draft.entities.includes(name)
  const toggle = (name: string) => {
    if (!draft) return
    const on = all.map((e) => e.name).filter((n) => (n === name ? !enabled(n) : enabled(n)))
    setDraft({ ...draft, entities: on.length === all.length ? [] : on })
  }
  const setPattern = (i: number, p: Partial<RedactionPattern>) =>
    draft && setDraft({ ...draft, custom_patterns: draft.custom_patterns.map((x, j) => (j === i ? { ...x, ...p } : x)) })
  useEffect(() => {
    if (!draft) return
    const t = setTimeout(() => {
      adminApi
        .sensitivePreview({ ...draft, policy: 'pii-v1' }, sample)
        .then((r) => {
          setPreview(r.text)
          setPreviewErr(null)
        })
        .catch((e) => setPreviewErr(e))
    }, 250)
    return () => clearTimeout(t)
  }, [draft, sample])
  const dirty = !!draft && !!cur.data && JSON.stringify(draft) !== JSON.stringify({ policy: cur.data.policy, entities: cur.data.entities, custom_patterns: cur.data.custom_patterns })
  return (
    <div className="col" style={{ gap: 12, maxWidth: 860 }}>
      <Section title="Sensitive data redaction">
        <div className="col" style={{ gap: 12 }}>
          <div className="hint">
            When on, the server replaces personal data and credentials in call inputs, outputs, exceptions and attributes with placeholders such as{' '}
            <span className="mono">&lt;EMAIL_ADDRESS&gt;</span> before anything is stored. It applies to SDK calls and OpenTelemetry spans alike. Data stored
            earlier is not changed.
          </div>
          {!draft ? (
            <Spinner />
          ) : (
            <>
              <label className="checkbox">
                <Switch on={draft.policy === 'pii-v1'} onChange={(on) => setDraft({ ...draft, policy: on ? 'pii-v1' : 'off' })} />
                Redact sensitive data at ingest <span className="muted mono small">(policy {draft.policy})</span>
              </label>
              <div className="col" style={{ gap: 6 }}>
                <div className="small muted">Detectors</div>
                <div className="col" style={{ gap: 4 }}>
                  {all.map((e) => (
                    <label key={e.name} className="checkbox">
                      <input type="checkbox" checked={enabled(e.name)} onChange={() => toggle(e.name)} />
                      <span className="mono small">{e.placeholder}</span> <span className="muted small">{e.description}</span>
                    </label>
                  ))}
                </div>
              </div>
              <div className="col" style={{ gap: 6 }}>
                <div className="small muted">Custom patterns (RE2 regular expressions; matches become &lt;NAME&gt;)</div>
                {draft.custom_patterns.map((p, i) => (
                  <div key={i} className="row">
                    <input className="input mono" placeholder="NAME" value={p.name} onChange={(e) => setPattern(i, { name: e.target.value.replace(/[^\w-]/g, '').toUpperCase() })} style={{ width: 160 }} />
                    <input className="input mono grow" placeholder="ACME-\d{4,}" value={p.pattern} onChange={(e) => setPattern(i, { pattern: e.target.value })} />
                    <button className="btn sm ghost danger" title="Remove pattern" onClick={() => setDraft({ ...draft, custom_patterns: draft.custom_patterns.filter((_, j) => j !== i) })}>
                      ✕
                    </button>
                  </div>
                ))}
                <div>
                  <button className="btn sm" onClick={() => setDraft({ ...draft, custom_patterns: [...draft.custom_patterns, { name: '', pattern: '' }] })}>
                    + Add pattern
                  </button>
                </div>
              </div>
              <div className="col" style={{ gap: 6 }}>
                <div className="small muted">Preview with these settings</div>
                <textarea className="input mono" rows={3} value={sample} onChange={(e) => setSample(e.target.value)} aria-label="Preview input" />
                {previewErr ? <ErrorBox error={previewErr} /> : <div className="mono small" data-testid="redaction-preview" style={{ padding: '6px 8px', background: 'var(--panel-2)', borderRadius: 6, whiteSpace: 'pre-wrap' }}>{preview}</div>}
              </div>
              <div className="row">
                <button
                  className="btn primary"
                  disabled={!dirty || busy || principal.data?.admin === false}
                  onClick={async () => {
                    setBusy(true)
                    setErr(null)
                    setSaved(false)
                    try {
                      await adminApi.sensitiveUpdate(projectId, { ...draft, custom_patterns: draft.custom_patterns.filter((p) => p.name || p.pattern) })
                      await qc.invalidateQueries({ queryKey: ['sensitive', projectId] })
                      setSaved(true)
                    } catch (e) {
                      setErr(e)
                    } finally {
                      setBusy(false)
                    }
                  }}
                >
                  Save
                </button>
                {saved && !dirty && <span className="badge success">saved</span>}
              </div>
            </>
          )}
          <AdminNote admin={principal.data?.admin} />
          <ErrorBox error={err ?? cur.error} />
        </div>
      </Section>
    </div>
  )
}

// ================================================================ delete project

export function DeleteProjectTab({ projectId }: { projectId: string }) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const principal = usePrincipal()
  const [confirm, setConfirm] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  return (
    <div className="col" style={{ gap: 12, maxWidth: 720 }}>
      <Section title="Delete project">
        <div className="col" style={{ gap: 10 }}>
          <div className="hint">
            Permanently deletes every call, object, dataset table, file, feedback item, price, setting and other record of{' '}
            <span className="mono">{projectId}</span>. This cannot be undone. Writing to the project again recreates it empty.
          </div>
          <label className="field">
            Type the project id to confirm
            <input className="input mono" value={confirm} onChange={(e) => setConfirm(e.target.value)} placeholder={projectId} style={{ maxWidth: 360 }} />
          </label>
          <div>
            <button
              className="btn danger"
              disabled={confirm.trim() !== projectId || busy || principal.data?.admin === false}
              onClick={async () => {
                setBusy(true)
                setErr(null)
                try {
                  await adminApi.projectDelete(projectId)
                  await qc.invalidateQueries()
                  navigate('/')
                } catch (e) {
                  setErr(e)
                } finally {
                  setBusy(false)
                }
              }}
            >
              {busy ? 'Deleting…' : 'Delete this project'}
            </button>
          </div>
          <AdminNote admin={principal.data?.admin} />
          <ErrorBox error={err} />
        </div>
      </Section>
    </div>
  )
}
