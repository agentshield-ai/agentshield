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
import { Icon } from '../../components/ui/Icons'
import { HelpLink } from '../../components/ui/PageHeader'
import { SearchField } from '../../components/ui/SearchField'
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
            detail={`${me.data.server_admin ? 'Server admin · ' : ''}${me.data.auth_method === 'none' ? 'the server is open: no sign-in' : `signed in with ${authWords(me.data.auth_method)}`}`}
          />
        </SettingsSection>
      )}
      <SettingsSection
        title={
          <>
            People with a role here <HelpLink doc="project.md#members" topic="Members and roles" />
          </>
        }
        action={
          <button type="button" className="qbtn primary" disabled={!canManage} onClick={() => setAdding(true)}>
            Add a member
          </button>
        }
        note={
          <>
            {!canManage && members.data ? 'Changing members needs the admin role on this project or its team.' : `The stronger of a person's role in team ${entity} and here applies.`}
            {me.data?.server_admin && (
              <>
                {' '}
                <Link to="/admin">Users &amp; teams</Link>
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
          members.data && <SettingsEmpty>{canManage ? 'Add a member to give someone a role on this project.' : 'No members yet.'}</SettingsEmpty>
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
                          ariaLabel={`Remove ${m.user_id}'s project role`}
                          tooltip="Remove the project role"
                          onConfirm={() => setRole(m, '')}
                        >
                          <Icon name="trash" size={14} />
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
  // One search box: "/api/v1/spans" searches paths, anything else who made the request.
  const [search, setSearch] = useState('')
  const [method, setMethod] = useState('')
  const [outcome, setOutcome] = useState('')
  const [since, setSince] = useState('')
  const [until, setUntil] = useState('')
  const [trayOpen, setTrayOpen] = useState(false)
  const [offset, setOffset] = useState(0)
  const [exporting, setExporting] = useState(false)
  const [exportErr, setExportErr] = useState<unknown>(null)
  const term = search.trim()
  const filter: AuditFilter = {
    project_id: scope === 'project' ? projectId : undefined,
    actor: term && !term.startsWith('/') ? term : undefined,
    method: method || undefined,
    path_prefix: term.startsWith('/') ? term : undefined,
    outcome: outcome || undefined,
    since: toRFC3339(since),
    until: toRFC3339(until),
  }
  const trayCount = [scope === 'all', method, outcome, since, until].filter(Boolean).length
  const filtered = !!(term || method || outcome || since || until)
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
        title={
          <>
            Entries <HelpLink doc="project.md#audit-log" topic="Audit log" />
          </>
        }
        action={
          <>
            <SearchField label="Search by who or by path" value={search} onChange={(v) => reset(() => setSearch(v))} placeholder="Who, or /path" width={200} />
            <button type="button" className={`qbtn ${trayCount ? 'on' : ''}`} aria-expanded={trayOpen || trayCount > 0} aria-controls="audit-tray" onClick={() => setTrayOpen((o) => !o)}>
              Filter{trayCount ? <span className="qbtn-count">{trayCount}</span> : null}
            </button>
            <button type="button" className="qbtn" disabled={exporting || !!q.error} onClick={() => void download()} data-testid="audit-export">
              {exporting ? 'Exporting…' : 'Export CSV'}
            </button>
          </>
        }
        note="Request bodies and secrets are never recorded."
      >
        {(trayOpen || trayCount > 0) && (
          <div className="s-tray" id="audit-tray" role="group" aria-label="Filter the audit log">
            {me.data?.server_admin && (
              <label>
                Projects
                <select className="input" value={scope} onChange={(e) => reset(() => setScope(e.target.value as 'project' | 'all'))}>
                  <option value="project">This project</option>
                  <option value="all">All projects</option>
                </select>
              </label>
            )}
            <label>
              Method
              <select className="input" value={method} onChange={(e) => reset(() => setMethod(e.target.value))}>
                <option value="">Any</option>
                {['POST', 'PUT', 'DELETE', 'PATCH', 'GET'].map((m) => (
                  <option key={m}>{m}</option>
                ))}
              </select>
            </label>
            <label>
              Result
              <select className="input" value={outcome} onChange={(e) => reset(() => setOutcome(e.target.value))}>
                <option value="">Any</option>
                {OUTCOMES.map((o) => (
                  <option key={o} value={o}>
                    {OUTCOME_WORDS[o]}
                  </option>
                ))}
              </select>
            </label>
            <label>
              From
              <input className="input" type="datetime-local" value={since} onChange={(e) => reset(() => setSince(e.target.value))} />
            </label>
            <label>
              To
              <input className="input" type="datetime-local" value={until} onChange={(e) => reset(() => setUntil(e.target.value))} />
            </label>
            {trayCount > 0 && (
              <button
                type="button"
                className="qbtn sm"
                onClick={() =>
                  reset(() => {
                    setScope('project')
                    setMethod('')
                    setOutcome('')
                    setSince('')
                    setUntil('')
                  })
                }
              >
                Clear filters
              </button>
            )}
          </div>
        )}
        <ErrorBox error={q.error ?? exportErr} />
        {q.isLoading ? (
          <Loading />
        ) : !q.data || q.data.entries.length === 0 ? null : (
          <>
            <div data-testid="audit-table" className="audit-box">
              <table className="audit-table">
                <caption className="sr-only">Audit log entries</caption>
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
                      <td className="a-when" title={fmtDate(e.ts)}>
                        {relativeTime(e.ts)}
                      </td>
                      <td className="a-who">
                        <span className="a-actor">{e.actor}</span>
                        <div className="quiet-note">
                          {authWords(e.auth_method)}
                          {e.ip && ` · ${e.ip}`}
                        </div>
                      </td>
                      <td className="a-req">
                        <span className="mono small">
                          {e.method} {e.path}
                        </span>
                        {scope === 'all' && e.project_id && <div className="quiet-note mono">{e.project_id}</div>}
                        <IdList ids={[...e.object_ids, ...e.span_ids]} />
                        {e.detail && <div className="quiet-note">{e.detail}</div>}
                      </td>
                      <td className="a-res" title={`HTTP ${e.status}`}>
                        <StatusWords tone={e.outcome === 'success' ? 'ok' : e.outcome === 'failed' ? 'warn' : 'error'}>{OUTCOME_WORDS[e.outcome] ?? e.outcome}</StatusWords>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <Pager offset={offset} limit={PAGE} total={q.data.total_count} onChange={setOffset} />
          </>
        )}
      </SettingsSection>
    </>
  )
}

/** How a request was signed in, in words: "API key", "browser session", "no sign-in". */
export function authWords(method: string): string {
  return method === 'none' || !method ? 'no sign-in' : method.replace(/_/g, ' ')
}
