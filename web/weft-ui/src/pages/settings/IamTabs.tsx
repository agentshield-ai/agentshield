// Settings sections for access: who has which role on this project
// (members), and the audit log (filters, paging, CSV export). Each opens
// with the sentence that answers it.
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { iamApi, ROLE_HELP, ROLES, type AuditFilter, type ProjectMember, type Role } from '../../api/iam'
import { ErrorBox, Loading, Modal, Pager, Spinner } from '../../components/common'
import { ConfirmButton } from '../../components/objects/bits'
import { SettingsEmpty, SettingsRow, SettingsSection, SettingsSentence, StatusWords } from '../../components/settings/SettingsList'
import { PlainTable } from '../../components/ui/PlainTable'
import { fmtDate } from '../../lib/format'
import { relativeTime } from '../../lib/summaries'
import { auditSentence, membersSentence } from '../../lib/summaries/project'

const roleWord = (r: Role | '') => (r ? r[0].toUpperCase() + r.slice(1) : 'None')

// ================================================================ members

export function MembersTab({ projectId }: { projectId: string }) {
  const qc = useQueryClient()
  const members = useQuery({ queryKey: ['project-members', projectId], queryFn: () => iamApi.projectMembers(projectId), retry: false })
  const me = useQuery({ queryKey: ['auth-me'], queryFn: iamApi.me, retry: false })
  const [adding, setAdding] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  const canManage = members.data?.can_manage ?? false
  const entity = members.data?.entity ?? projectId.split('/')[0]
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ['project-members', projectId] })
    void qc.invalidateQueries({ queryKey: ['auth-me'] })
  }
  const setRole = async (m: ProjectMember, role: Role | '') => {
    setErr(null)
    try {
      if (role) await iamApi.projectMemberSet(projectId, m.user_id, role)
      else await iamApi.projectMemberRemove(projectId, m.user_id)
      refresh()
    } catch (e) {
      setErr(e)
    }
  }
  return (
    <>
      {members.data && <SettingsSentence>{membersSentence(projectId, members.data.members)}</SettingsSentence>}
      {me.data && (
        <SettingsSection title="You">
          <SettingsRow
            label={<span className="mono">{me.data.user?.id ?? me.data.user_id}</span>}
            detail={`${me.data.server_admin ? 'Server admin · ' : ''}signed in with ${me.data.auth_method.replace('_', ' ')}`}
          />
        </SettingsSection>
      )}
      <SettingsSection
        title="People with a role here"
        action={
          <button type="button" className="qbtn primary" disabled={!canManage} onClick={() => setAdding(true)}>
            Add a member
          </button>
        }
        note={
          <>
            A user's role here is the stronger of their role in team <span className="mono">{entity}</span> and their role on this project. Viewers read;
            members also write traces, assets and feedback; admins also manage members, keys and settings. Server admins are admins everywhere, and a user's
            API keys carry the user's roles.
            {!canManage && members.data && ' Changing members needs the admin role on this project or its team.'}
            {me.data?.server_admin && (
              <>
                {' '}
                Users and teams are managed on <Link to="/admin">Users &amp; teams</Link>.
              </>
            )}
          </>
        }
      >
        {members.error || err ? (
          <SettingsEmpty>
            <ErrorBox error={members.error ?? err} />
          </SettingsEmpty>
        ) : null}
        {members.isLoading ? (
          <SettingsEmpty>
            <Spinner />
          </SettingsEmpty>
        ) : !members.data || members.data.members.length === 0 ? (
          members.data && <SettingsEmpty>Nobody has a role on this project yet. Server admins and the server's own keys still have access.</SettingsEmpty>
        ) : (
          members.data.members.map((m) => (
              <SettingsRow
                key={m.user_id}
                muted={m.disabled}
                label={
                  <>
                    <span className="mono">{m.user_id}</span>
                    {m.name && m.name !== m.user_id && <span className="quiet-note" style={{ display: 'inline' }}> · {m.name}</span>}
                  </>
                }
                detail={
                  <>
                    {[
                      m.email,
                      `${roleWord(m.role)} here`,
                      m.team_role ? `${m.team_role} in team ${entity}` : null,
                      m.server_admin ? 'server admin' : null,
                      m.disabled ? 'disabled' : null,
                    ]
                      .filter(Boolean)
                      .join(' · ')}
                  </>
                }
                control={
                  canManage ? (
                    <>
                      <select className="input" aria-label={`project role of ${m.user_id}`} value={m.project_role} onChange={(e) => void setRole(m, e.target.value as Role | '')}>
                          <option value="">No project role</option>
                          {ROLES.map((r) => (
                            <option key={r} value={r}>
                              {roleWord(r)}
                            </option>
                          ))}
                        </select>
                      {m.project_role && (
                        <ConfirmButton
                          className="qbtn sm plain danger"
                          title={`Remove ${m.user_id}'s project role?`}
                          body={m.team_role ? `They keep their ${m.team_role} role from team ${entity}.` : 'They lose access to this project.'}
                          confirmLabel="Remove"
                          onConfirm={() => setRole(m, '')}
                        >
                          Remove
                        </ConfirmButton>
                      )}
                    </>
                  ) : (
                    <span className="quiet-note">Project role: {roleWord(m.project_role)}</span>
                  )
                }
              />
            ))
        )}
      </SettingsSection>
      {adding && (
        <AddMemberModal
          projectId={projectId}
          existing={new Set(members.data?.members.filter((m) => m.project_role).map((m) => m.user_id))}
          onClose={() => setAdding(false)}
          onDone={() => {
            setAdding(false)
            refresh()
          }}
        />
      )}
    </>
  )
}

function AddMemberModal({ projectId, existing, onClose, onDone }: { projectId: string; existing: Set<string>; onClose: () => void; onDone: () => void }) {
  const users = useQuery({ queryKey: ['users', false], queryFn: () => iamApi.users(false), retry: false })
  const [user, setUser] = useState('')
  const [role, setRole] = useState<Role>('member')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  const options = (users.data?.users ?? []).filter((u) => !existing.has(u.id))
  return (
    <Modal
      title={`Add a member to ${projectId}`}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="qbtn" onClick={onClose}>
            Cancel
          </button>
          <button
            className="qbtn primary"
            disabled={!user || busy}
            onClick={async () => {
              setBusy(true)
              setErr(null)
              try {
                await iamApi.projectMemberSet(projectId, user, role)
                onDone()
              } catch (e) {
                setErr(e)
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? 'Adding…' : 'Add'}
          </button>
        </>
      }
    >
      <div className="form-grid">
        <label className="field">
          User
          {users.error ? (
            <input className="input mono" value={user} onChange={(e) => setUser(e.target.value.trim())} placeholder="user id" autoFocus />
          ) : (
            <select className="input" value={user} onChange={(e) => setUser(e.target.value)} autoFocus>
              <option value="">Choose a user…</option>
              {options.map((u) => (
                <option key={u.id} value={u.id}>
                  {u.id}
                  {u.email ? ` (${u.email})` : ''}
                </option>
              ))}
            </select>
          )}
        </label>
        <label className="field">
          Role
          <select className="input" value={role} onChange={(e) => setRole(e.target.value as Role)}>
            {ROLES.map((r) => (
              <option key={r} value={r}>
                {r}: {ROLE_HELP[r]}
              </option>
            ))}
          </select>
        </label>
      </div>
      <ErrorBox error={err} />
    </Modal>
  )
}

// ================================================================ audit log

const OUTCOMES = ['success', 'denied', 'unauthenticated', 'failed'] as const
const OUTCOME_WORDS: Record<string, string> = { success: 'Done', denied: 'Refused', unauthenticated: 'Not signed in', failed: 'Failed' }
const PAGE = 50

/** Converts a datetime-local value to RFC 3339 (UTC). */
function toRFC3339(local: string): string | undefined {
  if (!local) return undefined
  const d = new Date(local)
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString()
}

function IdList({ ids }: { ids: string[] }) {
  if (ids.length === 0) return null
  const shown = ids.slice(0, 2)
  return (
    <div className="quiet-note mono" title={ids.join('\n')} style={{ display: 'block' }}>
      {shown.join(', ')}
      {ids.length > shown.length && ` and ${ids.length - shown.length} more`}
    </div>
  )
}

export function AuditLogTab({ projectId }: { projectId: string }) {
  const me = useQuery({ queryKey: ['auth-me'], queryFn: iamApi.me, retry: false })
  const [scope, setScope] = useState<'project' | 'all'>('project')
  const [actor, setActor] = useState('')
  const [method, setMethod] = useState('')
  const [path, setPath] = useState('')
  const [outcome, setOutcome] = useState('')
  const [since, setSince] = useState('')
  const [until, setUntil] = useState('')
  const [offset, setOffset] = useState(0)
  const [exporting, setExporting] = useState(false)
  const [exportErr, setExportErr] = useState<unknown>(null)
  const filter: AuditFilter = {
    project_id: scope === 'project' ? projectId : undefined,
    actor: actor.trim() || undefined,
    method: method || undefined,
    path_prefix: path.trim() || undefined,
    outcome: outcome || undefined,
    since: toRFC3339(since),
    until: toRFC3339(until),
  }
  const filtered = !!(actor.trim() || method || path.trim() || outcome || since || until)
  const q = useQuery({
    queryKey: ['audit', filter, offset],
    queryFn: () => iamApi.audit({ ...filter, limit: PAGE, offset }),
    retry: false,
  })
  const reset = (fn: () => void) => {
    fn()
    setOffset(0)
  }
  const download = async () => {
    setExporting(true)
    setExportErr(null)
    try {
      const blob = await iamApi.auditCsv(filter)
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `weft-audit-${scope === 'project' ? projectId.replace('/', '_') : 'all'}.csv`
      a.click()
      setTimeout(() => URL.revokeObjectURL(url), 1000)
    } catch (e) {
      setExportErr(e)
    } finally {
      setExporting(false)
    }
  }
  return (
    <>
      {q.data && <SettingsSentence>{auditSentence(q.data.total_count, q.data.retention_days, filtered)}</SettingsSentence>}
      <SettingsSection
        plain
        title="Entries"
        action={
          <button type="button" className="qbtn" disabled={exporting || !!q.error} onClick={() => void download()} data-testid="audit-export">
            {exporting ? 'Exporting…' : 'Export CSV'}
          </button>
        }
        note="Every write, admin action, sign-in and refused request: who, when, the endpoint, the project, the assets and steps it touched, and the outcome. Request bodies and secrets are never recorded."
      >
        <div className="row wrap" style={{ gap: 8 }}>
          {me.data?.server_admin && (
            <select className="input" aria-label="scope" value={scope} onChange={(e) => reset(() => setScope(e.target.value as 'project' | 'all'))}>
              <option value="project">This project</option>
              <option value="all">All projects</option>
            </select>
          )}
          <input className="input mono" style={{ width: 140 }} placeholder="who" aria-label="actor" value={actor} onChange={(e) => reset(() => setActor(e.target.value))} />
          <select className="input" aria-label="method" value={method} onChange={(e) => reset(() => setMethod(e.target.value))}>
            <option value="">Any method</option>
            {['POST', 'PUT', 'DELETE', 'PATCH', 'GET'].map((m) => (
              <option key={m}>{m}</option>
            ))}
          </select>
          <input className="input mono" style={{ width: 200, maxWidth: '100%' }} placeholder="path, e.g. /api/v1/spans" aria-label="path prefix" value={path} onChange={(e) => reset(() => setPath(e.target.value))} />
          <select className="input" aria-label="outcome" value={outcome} onChange={(e) => reset(() => setOutcome(e.target.value))}>
            <option value="">Any outcome</option>
            {OUTCOMES.map((o) => (
              <option key={o} value={o}>
                {OUTCOME_WORDS[o]}
              </option>
            ))}
          </select>
          <label className="quiet-note">
            from <input className="input" type="datetime-local" value={since} onChange={(e) => reset(() => setSince(e.target.value))} />
          </label>
          <label className="quiet-note">
            to <input className="input" type="datetime-local" value={until} onChange={(e) => reset(() => setUntil(e.target.value))} />
          </label>
        </div>
        <ErrorBox error={q.error ?? exportErr} />
        {q.isLoading ? (
          <Loading />
        ) : !q.data || q.data.entries.length === 0 ? null : (
          <>
            <div data-testid="audit-table">
              <PlainTable caption="Audit log entries">
                <thead>
                  <tr>
                    <th scope="col">When</th>
                    <th scope="col">Who</th>
                    <th scope="col">Request</th>
                    <th scope="col">Result</th>
                  </tr>
                </thead>
                <tbody>
                  {q.data.entries.map((e) => (
                    <tr key={e.id}>
                      <td title={fmtDate(e.ts)} style={{ whiteSpace: 'nowrap' }}>
                        {relativeTime(e.ts)}
                      </td>
                      <td>
                        <span className="mono small">{e.actor}</span>
                        <div className="quiet-note">
                          {e.auth_method.replace('_', ' ')}
                          {e.ip && ` · ${e.ip}`}
                        </div>
                      </td>
                      <td>
                        <span className="mono small">
                          {e.method} {e.path}
                        </span>
                        {scope === 'all' && e.project_id && <div className="quiet-note mono">{e.project_id}</div>}
                        <IdList ids={[...e.object_ids, ...e.span_ids]} />
                        {e.detail && <div className="quiet-note">{e.detail}</div>}
                      </td>
                      <td title={`HTTP ${e.status}`} style={{ whiteSpace: 'nowrap' }}>
                        <StatusWords tone={e.outcome === 'success' ? 'ok' : e.outcome === 'failed' ? 'warn' : 'error'}>{OUTCOME_WORDS[e.outcome] ?? e.outcome}</StatusWords>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </PlainTable>
            </div>
            <Pager offset={offset} limit={PAGE} total={q.data.total_count} onChange={setOffset} />
          </>
        )}
      </SettingsSection>
    </>
  )
}
