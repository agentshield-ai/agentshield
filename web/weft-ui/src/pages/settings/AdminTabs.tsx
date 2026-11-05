// Settings sections for administration: API keys, data retention, ingest
// sampling, sensitive-data redaction and deleting the project. Each opens
// with a sentence that answers it, then labelled rows with their controls on
// the right (components/settings/SettingsList). Revoking a key, purging and
// deleting the project cannot be undone, so they ask first.
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { adminApi, type ApiKeyCreate, type ManagedApiKey, type RedactionPattern, type SensitiveDataSettings } from '../../api/admin'
import { getApiKey, setApiKey } from '../../api/client'
import { CopyButton, ErrorBox, Modal, Spinner } from '../../components/common'
import { ConfirmButton, Switch } from '../../components/objects/bits'
import { Icon } from '../../components/ui/Icons'
import { HelpLink } from '../../components/ui/PageHeader'
import { SettingsEmpty, SettingsRow, SettingsSection, SettingsSentence, StatusWords } from '../../components/settings/SettingsList'
import { fmtDate, fmtNumber } from '../../lib/format'
import { joinClauses, relativeTime } from '../../lib/summaries'
import { apiKeysSentence, retentionSentence, samplingSentence, sensitiveSentence } from '../../lib/summaries/project'

function usePrincipal() {
  return useQuery({ queryKey: ['auth-principal', getApiKey()], queryFn: adminApi.principal, retry: false })
}

function AdminNote({ admin }: { admin: boolean | undefined }) {
  if (admin !== false) return null
  return <p className="quiet-note">Changing this needs an admin API key. The key in this browser is not one.</p>
}

const accessWords = (k: { admin: boolean; read_only: boolean }) => (k.admin ? 'Admin' : k.read_only ? 'Read-only' : 'Read and write')

// ================================================================ API keys

export function ApiKeysTab({ projectId, browserKey }: { projectId: string; browserKey?: ReactNode }) {
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
  const cannot = p?.admin === false && !p?.rbac
  return (
    <>
      {keys.data && p && <SettingsSentence>{apiKeysSentence(keys.data.keys, p.auth_required)}</SettingsSentence>}
      {browserKey}
      {p && (
        <SettingsSection title="You">
          <SettingsRow
            label={
              <>
                <span className="mono">{p.user_id}</span>
                {p.key_name && <span className="quiet-note" style={{ display: 'inline' }}> · key {p.key_name}</span>}
              </>
            }
            detail={
              <>
                {accessWords(p)}
                {keys.data && keys.data.static_key_count > 0 && ` · ${keys.data.static_key_count} more ${keys.data.static_key_count === 1 ? 'key' : 'keys'} from the server's --api-keys`}
              </>
            }
          />
        </SettingsSection>
      )}
      <SettingsSection
        title={
          <>
            Keys <HelpLink doc="project.md#api-keys" topic="API keys" />
          </>
        }
        action={
          <>
            <label className="checkbox small">
              <input type="checkbox" checked={showRevoked} onChange={(e) => setShowRevoked(e.target.checked)} /> Show revoked keys
            </label>
            <button type="button" className="qbtn primary" disabled={cannot} onClick={() => setCreating(true)}>
              Create a key
            </button>
          </>
        }
        note={p && !p.auth_required ? 'The first key must be an admin key; creating it turns sign-in on.' : 'A key’s secret is shown once, when it is created.'}
      >
        {keys.error ? (
          <SettingsEmpty>
            <ErrorBox error={keys.error} />
          </SettingsEmpty>
        ) : keys.isLoading ? (
          <SettingsEmpty>
            <Spinner />
          </SettingsEmpty>
        ) : !keys.data || keys.data.keys.length === 0 ? (
          <SettingsEmpty>{cannot ? 'No keys yet.' : 'Create a key for an SDK, a plugin or AgentShield to send data with.'}</SettingsEmpty>
        ) : (
          keys.data.keys.map((k) => (
            <SettingsRow
              key={k.id}
              muted={!!k.revoked_at}
              label={
                <>
                  {k.name}
                  {k.id === p?.key_id && <span className="quiet-note" style={{ display: 'inline' }}> · this browser's key</span>}
                </>
              }
              detail={
                <>
                  <span className="mono">{k.prefix}…</span> · {accessWords(k)} · {k.all_projects ? 'all projects' : joinClauses(k.projects)} · user <span className="mono">{k.user_id}</span>
                  <br />
                  <span title={fmtDate(k.created_at)}>
                    Created {relativeTime(k.created_at)}
                    {k.created_by && ` by ${k.created_by}`}
                  </span>
                  {' · '}
                  <span title={k.last_used_at ? fmtDate(k.last_used_at) : undefined}>{k.last_used_at ? `last used ${relativeTime(k.last_used_at)}` : 'never used'}</span>
                </>
              }
              control={
                k.revoked_at ? (
                  <span className="quiet-note" title={fmtDate(k.revoked_at)}>
                    Revoked {relativeTime(k.revoked_at)}
                  </span>
                ) : (
                  <ConfirmButton
                    className="qbtn sm"
                    title={`Revoke “${k.name}”?`}
                    body="Requests with this key fail at once. This cannot be undone."
                    confirmLabel="Revoke"
                    disabled={cannot}
                    onConfirm={async () => {
                      await adminApi.apiKeyRevoke(k.id)
                      refresh()
                    }}
                  >
                    Revoke
                  </ConfirmButton>
                )
              }
            />
          ))
        )}
      </SettingsSection>
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
    </>
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
          <button type="button" className="qbtn" onClick={onClose}>
            Cancel
          </button>
          <button
            className="qbtn primary"
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
      {access === 'admin' && scope !== 'all' && <div className="hint" style={{ color: 'var(--warn)' }}>Admin keys cover all projects; choose All projects.</div>}
      {firstKey && <div className="hint">This is the first key: it must be an admin key, and creating it turns authentication on.</div>}
      <ErrorBox error={err} />
    </Modal>
  )
}

function SecretModal({ created, onClose, onUse }: { created: { key: ManagedApiKey; secret: string; stored: boolean }; onClose: () => void; onUse: () => void }) {
  const [stored, setStored] = useState(created.stored)
  return (
    <Modal
      title={`Key “${created.key.name}” created`}
      onClose={onClose}
      footer={
        <button type="button" className="qbtn primary" onClick={onClose}>
          Done
        </button>
      }
    >
      <p style={{ margin: 0 }}>Copy the secret now. It is stored hashed and cannot be shown again.</p>
      <div className="row" style={{ alignItems: 'flex-start' }}>
        <code className="secret-once grow" data-testid="new-api-key">
          {created.secret}
        </code>
        <CopyButton text={created.secret} label="Copy the secret" />
      </div>
      <p className="quiet-note mono">export WEFT_API_KEY={created.secret}</p>
      {stored ? (
        <StatusWords tone="ok">Saved as this browser's key</StatusWords>
      ) : (
        <button
          type="button"
          className="qbtn sm"
          style={{ alignSelf: 'flex-start' }}
          onClick={() => {
            setApiKey(created.secret)
            setStored(true)
            onUse()
          }}
        >
          Use this key in this browser
        </button>
      )}
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
  const noAdmin = principal.data?.admin === false
  return (
    <>
      {ttl.isLoading ? <Spinner /> : <SettingsSentence>{retentionSentence(current)}</SettingsSentence>}
      <SettingsSection title="Retention" note="A background job deletes old spans and their feedback every hour, permanently. Datasets, prompts and other saved assets are versioned and kept.">
        <SettingsRow
          label="Delete spans after"
          labelFor="retention-days"
          detail="Days, from 1 to 36,500. Leave empty to keep everything."
          control={
            <form
              className="row wrap"
              onSubmit={(e) => {
                e.preventDefault()
                if (valid) void save(n)
              }}
            >
              <input id="retention-days" className="input mono" type="number" min={1} max={36500} value={days} onChange={(e) => setDays(e.target.value)} placeholder="e.g. 30" style={{ width: 110 }} />
              <button className="qbtn sm" type="submit" disabled={!valid || busy || noAdmin}>
                Save
              </button>
              <button className="qbtn sm" type="button" disabled={!current || busy || noAdmin} onClick={() => void save(null)}>
                Keep forever
              </button>
            </form>
          }
        />
        <SettingsRow
          label="Purge now"
          detail={purged ?? (current ? `Delete spans older than ${current} days at once, without waiting for the hourly job.` : 'Set a retention period first.')}
          control={
            <ConfirmButton
              className="qbtn sm danger"
              title={`Purge data older than ${current} days now?`}
              body="Spans and their feedback older than the retention period are deleted permanently."
              confirmLabel="Purge"
              disabled={!current || noAdmin}
              onConfirm={async () => {
                const r = await adminApi.ttlPurge(projectId)
                setPurged(`Deleted ${fmtNumber(r.purged.spans)} spans and ${fmtNumber(r.purged.feedback)} pieces of feedback.`)
                void qc.invalidateQueries({ queryKey: ['project-stats', projectId] })
              }}
            >
              Purge now
            </ConfirmButton>
          }
        />
      </SettingsSection>
      <AdminNote admin={principal.data?.admin} />
      <ErrorBox error={err ?? ttl.error} />
    </>
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
    <>
      {cur.data && <SettingsSentence>{samplingSentence(cur.data.sample_rate, cur.data.dry_run)}</SettingsSentence>}
      <SettingsSection
        title="Ingest sampling"
        note="The choice is made per trace from its id, so a trace's spans are kept or dropped together, from any SDK or OpenTelemetry exporter. Dropped spans are never stored."
        action={
          <>
            {saved && !dirty && <StatusWords tone="ok">Saved</StatusWords>}
            <button
              type="button"
              className="qbtn primary"
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
          </>
        }
      >
        {cur.isLoading ? (
          <SettingsEmpty>
            <Spinner />
          </SettingsEmpty>
        ) : (
          <>
            <SettingsRow
              label="Traces kept"
              labelFor="sample-pct"
              control={
                <span className="row wrap" style={{ gap: 10 }}>
                  <input type="range" min={0} max={100} step={1} value={pct} onChange={(e) => setPct(Number(e.target.value))} style={{ width: 200, maxWidth: '100%' }} aria-label="Share of traces kept" />
                  <input id="sample-pct" className="input mono" type="number" min={0} max={100} step={0.1} value={pct} onChange={(e) => setPct(Math.min(100, Math.max(0, Number(e.target.value))))} style={{ width: 80 }} />
                  <span className="quiet-note">%</span>
                </span>
              }
            />
            <SettingsRow
              label="Dry run"
              detail={
                <>
                  Keep everything, but mark the spans sampling would drop (attribute <span className="mono">weft_ingest_sampling</span>).
                </>
              }
              control={<Switch on={dry} onChange={setDry} label="Dry run" />}
            />
          </>
        )}
      </SettingsSection>
      <AdminNote admin={principal.data?.admin} />
      <ErrorBox error={err ?? cur.error} />
    </>
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
  const setPattern = (i: number, p: Partial<RedactionPattern>) => draft && setDraft({ ...draft, custom_patterns: draft.custom_patterns.map((x, j) => (j === i ? { ...x, ...p } : x)) })
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
  const savedState = cur.data
  return (
    <>
      {savedState && (
        <SettingsSentence>
          {sensitiveSentence(
            savedState.policy,
            savedState.entities.length === 0 ? (savedState.available_entities ?? []).length : savedState.entities.length,
            (savedState.available_entities ?? []).length,
            savedState.custom_patterns.length,
          )}
        </SettingsSentence>
      )}
      {!draft ? (
        <Spinner />
      ) : (
        <>
          <SettingsSection
            title="Redaction"
            note="Data stored earlier is not changed."
            action={
              <>
                {saved && !dirty && <StatusWords tone="ok">Saved</StatusWords>}
                <button
                  type="button"
                  className="qbtn primary"
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
              </>
            }
          >
            <SettingsRow
              label="Redact sensitive data as it arrives"
              detail={
                <>
                  Inputs, outputs, errors and attributes: matches become placeholders such as <span className="mono">&lt;EMAIL_ADDRESS&gt;</span> before anything is stored.
                </>
              }
              control={<Switch on={draft.policy === 'pii-v1'} onChange={(on) => setDraft({ ...draft, policy: on ? 'pii-v1' : 'off' })} label="Redact sensitive data" />}
            />
          </SettingsSection>
          <SettingsSection title="Detectors" note={draft.policy === 'off' ? 'The detectors apply when redaction is on.' : undefined}>
            {all.map((e) => (
              <SettingsRow
                key={e.name}
                muted={draft.policy === 'off'}
                label={<span className="mono">{e.placeholder}</span>}
                detail={e.description}
                control={
                  <Switch
                    on={draft.policy !== 'off' && enabled(e.name)}
                    disabled={draft.policy === 'off'}
                    onChange={() => toggle(e.name)}
                    label={`Detect ${e.placeholder}`}
                    title={draft.policy === 'off' ? 'Turn redaction on to choose detectors' : undefined}
                  />
                }
              />
            ))}
          </SettingsSection>
          <SettingsSection
            title="Your own patterns"
            note="RE2 regular expressions; a match becomes <NAME>."
            action={
              <button type="button" className="qbtn primary" onClick={() => setDraft({ ...draft, custom_patterns: [...draft.custom_patterns, { name: '', pattern: '' }] })}>
                Add a pattern
              </button>
            }
          >
            {draft.custom_patterns.length === 0 ? (
              <SettingsEmpty>None.</SettingsEmpty>
            ) : (
              draft.custom_patterns.map((p, i) => (
                <SettingsRow
                  key={i}
                  label={`Pattern ${i + 1}`}
                  control={
                    <span className="row wrap" style={{ gap: 6 }}>
                      <input className="input mono" aria-label={`Pattern ${i + 1} name`} placeholder="NAME" value={p.name} onChange={(e) => setPattern(i, { name: e.target.value.replace(/[^\w-]/g, '').toUpperCase() })} style={{ width: 130 }} />
                      <input className="input mono" aria-label={`Pattern ${i + 1} expression`} placeholder="ACME-\d{4,}" value={p.pattern} onChange={(e) => setPattern(i, { pattern: e.target.value })} style={{ width: 200, maxWidth: '100%' }} />
                      <button
                        type="button"
                        className="qbtn sm plain danger"
                        aria-label={`Remove pattern ${i + 1}`}
                        title="Remove this pattern"
                        onClick={() => setDraft({ ...draft, custom_patterns: draft.custom_patterns.filter((_, j) => j !== i) })}
                      >
                        <Icon name="trash" size={14} />
                      </button>
                    </span>
                  }
                />
              ))
            )}
          </SettingsSection>
          <SettingsSection plain title="Try it">
            <textarea className="input mono" rows={3} value={sample} onChange={(e) => setSample(e.target.value)} aria-label="Text to try the settings on" />
            {previewErr ? (
              <ErrorBox error={previewErr} />
            ) : (
              <div className="s-code" data-testid="redaction-preview">
                {preview}
              </div>
            )}
          </SettingsSection>
        </>
      )}
      <AdminNote admin={principal.data?.admin} />
      <ErrorBox error={err ?? cur.error} />
    </>
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
    <>
      <SettingsSentence>{`Deleting ${projectId} removes every span, asset, dataset table, file, piece of feedback, price and setting in it, permanently. Writing to it again starts it empty.`}</SettingsSentence>
      <SettingsSection title="Delete this project">
        <SettingsRow
          label="Type the project id to confirm"
          labelFor="delete-confirm"
          control={
            <span className="row wrap" style={{ gap: 8 }}>
              <input id="delete-confirm" className="input mono" value={confirm} onChange={(e) => setConfirm(e.target.value)} placeholder={projectId} style={{ width: 240, maxWidth: '100%' }} />
              <button
                type="button"
                className="qbtn danger-solid"
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
            </span>
          }
        />
      </SettingsSection>
      <AdminNote admin={principal.data?.admin} />
      <ErrorBox error={err} />
    </>
  )
}
