// Settings tabs for access control: project members and roles, and the
// audit log (filters, paging, CSV export).
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { iamApi, ROLE_HELP, ROLES, type AuditFilter, type ProjectMember, type Role } from '../../api/iam'
import { Empty, ErrorBox, Loading, Modal, Pager, Section } from '../../components/common'
import { ConfirmButton } from '../../components/objects/bits'
import { fmtRelative } from '../../lib/format'

function RoleBadge({ role }: { role: Role | '' }) {
  if (!role) return <span className="faint small">—</span>
  return <span className={`badge ${role === 'admin' ? 'accent' : role === 'member' ? 'success' : ''}`}>{role}</span>
}

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
    <div className="col" style={{ gap: 12 }}>
      <Section
        title="Members"
        actions={
          <button className="btn primary sm" disabled={!canManage} onClick={() => setAdding(true)}>
            + Add member
          </button>
        }
      >
        <div className="col" style={{ gap: 10 }}>
          <div className="hint">
            A user's role on <span className="mono">{projectId}</span> is the stronger of their role in team <span className="mono">{entity}</span> and their
            role on this project. Viewers read; members also write traces, objects and feedback; admins also manage members, keys and settings. Server
            admins are admins everywhere. API keys of a user carry that user's roles.
          </div>
          {me.data && (
            <div className="kv">
              <div className="k">you</div>
              <div className="v">
                <span className="mono">{me.data.user?.id ?? me.data.user_id}</span>{' '}
                {me.data.server_admin ? <span className="badge accent">server admin</span> : null}{' '}
                <span className="muted small">via {me.data.auth_method.replace('_', ' ')}</span>
              </div>
            </div>
          )}
          {!canManage && members.data && <div className="hint">Changing members needs the admin role on this project or its team.</div>}
          <ErrorBox error={members.error ?? err} />
          {members.isLoading ? (
            <Loading />
          ) : !members.data || members.data.members.length === 0 ? (
            members.data && <Empty>No users have roles on this project yet. Server admins and static keys still have access.</Empty>
          ) : (
            <div className="table-wrap">
              <table className="table" data-testid="members-table">
                <thead>
                  <tr>
                    <th>User</th>
                    <th>Effective role</th>
                    <th>Team ({entity})</th>
                    <th>Project role</th>
                    <th style={{ width: 90 }} />
                  </tr>
                </thead>
                <tbody>
                  {members.data.members.map((m) => (
                    <tr key={m.user_id} style={m.disabled ? { opacity: 0.55 } : undefined}>
                      <td>
                        <span className="mono">{m.user_id}</span>
                        {m.name && m.name !== m.user_id && <span className="muted"> · {m.name}</span>}
                        {m.email && <div className="faint small">{m.email}</div>}
                        {m.server_admin && <span className="badge accent" style={{ marginLeft: 6 }}>server admin</span>}
                        {m.disabled && <span className="badge error" style={{ marginLeft: 6 }}>disabled</span>}
                      </td>
                      <td>
                        <RoleBadge role={m.role} />
                      </td>
                      <td>
                        <RoleBadge role={m.team_role} />
                      </td>
                      <td>
                        {canManage ? (
                          <select
                            className="input"
                            aria-label={`project role of ${m.user_id}`}
                            value={m.project_role}
                            onChange={(e) => void setRole(m, e.target.value as Role | '')}
                          >
                            <option value="">none</option>
                            {ROLES.map((r) => (
                              <option key={r} value={r}>
                                {r}
                              </option>
                            ))}
                          </select>
                        ) : (
                          <RoleBadge role={m.project_role} />
                        )}
                      </td>
                      <td>
                        {canManage && m.project_role && (
                          <ConfirmButton
                            className="btn sm ghost danger"
                            title={`Remove ${m.user_id}'s project role?`}
                            body={m.team_role ? `They keep their ${m.team_role} role from team ${entity}.` : 'They lose access to this project.'}
                            confirmLabel="Remove"
                            onConfirm={() => setRole(m, '')}
                          >
                            Remove
                          </ConfirmButton>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {me.data?.server_admin && (
            <div className="hint">
              Manage users and teams on the <Link to="/admin">admin page</Link>.
            </div>
          )}
        </div>
      </Section>
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
    </div>
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
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button
            className="btn primary"
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
const PAGE = 50

/** Converts a datetime-local value to RFC 3339 (UTC). */
function toRFC3339(local: string): string | undefined {
  if (!local) return undefined
  const d = new Date(local)
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString()
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
    <div className="col" style={{ gap: 12 }}>
      <Section
        title="Audit log"
        actions={
          <button className="btn sm" disabled={exporting || !!q.error} onClick={() => void download()} data-testid="audit-export">
            {exporting ? 'Exporting…' : 'Export CSV'}
          </button>
        }
      >
        <div className="col" style={{ gap: 10 }}>
          <div className="hint">
            Every write, admin action, sign-in and refused request is recorded with who, when, the endpoint, project, object and call ids, and the outcome.
            Request bodies and secrets are never recorded.
            {q.data && (q.data.retention_days > 0 ? ` Entries are kept for ${q.data.retention_days} days.` : ' Entries are kept forever.')}
          </div>
          <div className="row wrap" style={{ gap: 8 }}>
            {me.data?.server_admin && (
              <select className="input" aria-label="scope" value={scope} onChange={(e) => reset(() => setScope(e.target.value as 'project' | 'all'))}>
                <option value="project">This project</option>
                <option value="all">All projects</option>
              </select>
            )}
            <input className="input mono" style={{ width: 140 }} placeholder="actor" aria-label="actor" value={actor} onChange={(e) => reset(() => setActor(e.target.value))} />
            <select className="input" aria-label="method" value={method} onChange={(e) => reset(() => setMethod(e.target.value))}>
              <option value="">any method</option>
              {['POST', 'PUT', 'DELETE', 'PATCH', 'GET'].map((m) => (
                <option key={m}>{m}</option>
              ))}
            </select>
            <input className="input mono" style={{ width: 180 }} placeholder="path prefix, e.g. /call" aria-label="path prefix" value={path} onChange={(e) => reset(() => setPath(e.target.value))} />
            <select className="input" aria-label="outcome" value={outcome} onChange={(e) => reset(() => setOutcome(e.target.value))}>
              <option value="">any outcome</option>
              {OUTCOMES.map((o) => (
                <option key={o}>{o}</option>
              ))}
            </select>
            <label className="small muted row" style={{ gap: 4 }}>
              from <input className="input" type="datetime-local" value={since} onChange={(e) => reset(() => setSince(e.target.value))} />
            </label>
            <label className="small muted row" style={{ gap: 4 }}>
              to <input className="input" type="datetime-local" value={until} onChange={(e) => reset(() => setUntil(e.target.value))} />
            </label>
          </div>
          <ErrorBox error={q.error ?? exportErr} />
          {q.isLoading ? (
            <Loading />
          ) : !q.data || q.data.entries.length === 0 ? (
            q.data && <Empty>No matching entries.</Empty>
          ) : (
            <>
              <div className="table-wrap">
                <table className="table" data-testid="audit-table">
                  <thead>
                    <tr>
                      <th>When</th>
                      <th>Actor</th>
                      <th>Request</th>
                      <th>Project</th>
                      <th>Objects / calls</th>
                      <th>Outcome</th>
                      <th>Detail</th>
                    </tr>
                  </thead>
                  <tbody>
                    {q.data.entries.map((e) => (
                      <tr key={e.id}>
                        <td className="small muted" title={e.ts}>
                          {fmtRelative(e.ts)}
                        </td>
                        <td>
                          <span className="mono small">{e.actor}</span>
                          <div className="faint small">
                            {e.auth_method.replace('_', ' ')}
                            {e.ip && ` · ${e.ip}`}
                          </div>
                        </td>
                        <td className="mono small">
                          {e.method} {e.path}
                        </td>
                        <td className="mono small">{e.project_id ?? <span className="faint">—</span>}</td>
                        <td className="mono small" style={{ maxWidth: 220, overflow: 'hidden', textOverflow: 'ellipsis' }}>
                          {[...e.object_ids, ...e.call_ids].join(', ') || <span className="faint">—</span>}
                        </td>
                        <td>
                          <span className={`badge ${e.outcome === 'success' ? 'success' : e.outcome === 'failed' ? 'warn' : 'error'}`} title={String(e.status)}>
                            {e.outcome}
                          </span>
                        </td>
                        <td className="small">{e.detail}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <Pager offset={offset} limit={PAGE} total={q.data.total_count} onChange={setOffset} />
            </>
          )}
        </div>
      </Section>
    </div>
  )
}
