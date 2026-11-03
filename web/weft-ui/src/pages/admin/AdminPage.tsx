// Server administration: users (create, edit, admin flag, disable, sign
// out everywhere) and teams (entities) with their members' roles.
import { useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { iamApi, ROLE_HELP, ROLES, type Role, type Team, type User } from '../../api/iam'
import { Empty, ErrorBox, Loading, Modal, Section, Tabs } from '../../components/common'
import { ConfirmButton, Switch } from '../../components/objects/bits'
import { fmtRelative } from '../../lib/format'

type Tab = 'users' | 'teams'

export function AdminPage() {
  const [params, setParams] = useSearchParams()
  const tab: Tab = params.get('tab') === 'teams' ? 'teams' : 'users'
  const me = useQuery({ queryKey: ['auth-me'], queryFn: iamApi.me, retry: false })
  return (
    <div className="page">
      <div className="page-header">
        <h1>Users &amp; teams</h1>
        <Link to="/" className="muted small">
          ← Projects
        </Link>
      </div>
      <ErrorBox error={me.error} />
      {me.data && !me.data.server_admin && (
        <div className="hint" style={{ marginBottom: 12 }}>
          You are not a server admin: you can see your own teams and manage the members of teams you administer.
        </div>
      )}
      <div className="tabs-scroll">
        <Tabs<Tab>
          tabs={[
            { id: 'users', label: 'Users' },
            { id: 'teams', label: 'Teams' },
          ]}
          value={tab}
          onChange={(t) => setParams({ tab: t }, { replace: true })}
        />
      </div>
      {tab === 'users' && <UsersTab admin={!!me.data?.server_admin} myId={me.data?.user?.id} />}
      {tab === 'teams' && <TeamsTab admin={!!me.data?.server_admin} myTeamsAdmin={me.data ? Object.entries(me.data.roles.entities).filter(([, r]) => r === 'admin').map(([t]) => t) : []} />}
    </div>
  )
}

// ================================================================ users

function UsersTab({ admin, myId }: { admin: boolean; myId?: string }) {
  const qc = useQueryClient()
  const [showDisabled, setShowDisabled] = useState(false)
  const [editing, setEditing] = useState<User | 'new' | null>(null)
  const [err, setErr] = useState<unknown>(null)
  const users = useQuery({ queryKey: ['users', showDisabled], queryFn: () => iamApi.users(showDisabled), retry: false })
  const refresh = () => void qc.invalidateQueries({ queryKey: ['users'] })
  return (
    <Section
      title="Users"
      actions={
        <div className="row">
          <label className="checkbox small">
            <input type="checkbox" checked={showDisabled} onChange={(e) => setShowDisabled(e.target.checked)} /> Show disabled
          </label>
          <button className="btn primary sm" disabled={!admin} onClick={() => setEditing('new')}>
            + Add user
          </button>
        </div>
      }
    >
      <div className="col" style={{ gap: 10 }}>
        <div className="hint">
          Users own API keys and browser sessions. A user's id is recorded as the <span className="mono">user_id</span> of what they write. Users who sign in
          with SSO are created automatically; an account you add here with the same email is linked on first sign-in. Server admins can do everything.
        </div>
        <ErrorBox error={users.error ?? err} />
        {users.isLoading ? (
          <Loading />
        ) : !users.data || users.data.users.length === 0 ? (
          users.data && <Empty>No users yet.</Empty>
        ) : (
          <div className="table-wrap">
            <table className="table" data-testid="users-table">
              <thead>
                <tr>
                  <th>User</th>
                  <th>Email</th>
                  <th>Role</th>
                  <th>Sign-in</th>
                  <th>Created</th>
                  <th style={{ width: 220 }} />
                </tr>
              </thead>
              <tbody>
                {users.data.users.map((u) => (
                  <tr key={u.id} style={u.disabled_at ? { opacity: 0.55 } : undefined}>
                    <td>
                      <span className="mono">{u.id}</span>
                      {u.name !== u.id && <span className="muted"> · {u.name}</span>}
                      {u.id === myId && <span className="badge success" style={{ marginLeft: 6 }}>you</span>}
                    </td>
                    <td className="small">{u.email ?? <span className="faint">—</span>}</td>
                    <td>
                      {u.admin ? <span className="badge accent">server admin</span> : <span className="muted small">by team/project</span>}
                      {u.disabled_at && <span className="badge error" style={{ marginLeft: 6 }}>disabled</span>}
                    </td>
                    <td className="small muted">
                      {u.sso ? 'SSO' : 'API keys'}
                      {u.last_login_at && ` · ${fmtRelative(u.last_login_at)}`}
                    </td>
                    <td className="small muted" title={u.created_at}>
                      {fmtRelative(u.created_at)}
                    </td>
                    <td>
                      {admin && (
                        <div className="row" style={{ gap: 4 }}>
                          <button className="btn sm ghost" onClick={() => setEditing(u)}>
                            Edit
                          </button>
                          <ConfirmButton
                            className="btn sm ghost"
                            title={`Sign ${u.id} out everywhere?`}
                            body="Ends all of this user's browser sessions. API keys are not affected."
                            confirmLabel="Sign out"
                            onConfirm={async () => {
                              setErr(null)
                              await iamApi.userRevokeSessions(u.id)
                            }}
                          >
                            Sign out
                          </ConfirmButton>
                          {u.id !== myId &&
                            (u.disabled_at ? (
                              <button
                                className="btn sm ghost"
                                onClick={async () => {
                                  try {
                                    await iamApi.userUpdate(u.id, { disabled: false })
                                    refresh()
                                  } catch (e) {
                                    setErr(e)
                                  }
                                }}
                              >
                                Enable
                              </button>
                            ) : (
                              <ConfirmButton
                                className="btn sm ghost danger"
                                title={`Disable ${u.id}?`}
                                body="Their sessions end and their API keys stop working until you enable the account again."
                                confirmLabel="Disable"
                                onConfirm={async () => {
                                  await iamApi.userUpdate(u.id, { disabled: true })
                                  refresh()
                                }}
                              >
                                Disable
                              </ConfirmButton>
                            ))}
                        </div>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      {editing && (
        <UserModal
          user={editing === 'new' ? null : editing}
          self={editing !== 'new' && editing.id === myId}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            refresh()
          }}
        />
      )}
    </Section>
  )
}

function UserModal({ user, self, onClose, onSaved }: { user: User | null; self: boolean; onClose: () => void; onSaved: () => void }) {
  const [id, setId] = useState(user?.id ?? '')
  const [name, setName] = useState(user?.name ?? '')
  const [email, setEmail] = useState(user?.email ?? '')
  const [admin, setAdmin] = useState(user?.admin ?? false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  return (
    <Modal
      title={user ? `Edit ${user.id}` : 'Add user'}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button
            className="btn primary"
            disabled={busy || !id.trim()}
            onClick={async () => {
              setBusy(true)
              setErr(null)
              try {
                if (user) await iamApi.userUpdate(user.id, { name: name.trim() || user.id, email: email.trim() || null, admin })
                else await iamApi.userCreate({ id: id.trim(), name: name.trim() || undefined, email: email.trim() || null, admin })
                onSaved()
              } catch (e) {
                setErr(e)
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? 'Saving…' : user ? 'Save' : 'Add user'}
          </button>
        </>
      }
    >
      <div className="form-grid">
        <label className="field">
          User id
          <input className="input mono" value={id} disabled={!!user} onChange={(e) => setId(e.target.value.replace(/[^\w.@-]/g, ''))} placeholder="jane" autoFocus={!user} />
        </label>
        <label className="field">
          Display name
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="Jane Doe" />
        </label>
        <label className="field">
          Email
          <input className="input" type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="jane@example.com" />
        </label>
        <label className="field">
          Server admin
          <div className="row">
            <Switch on={admin} onChange={setAdmin} disabled={self} title={self ? 'You cannot remove your own admin role' : undefined} />
            <span className="small muted">admin of every project; manages users, teams and secrets</span>
          </div>
        </label>
      </div>
      <ErrorBox error={err} />
    </Modal>
  )
}

// ================================================================ teams

function TeamsTab({ admin, myTeamsAdmin }: { admin: boolean; myTeamsAdmin: string[] }) {
  const qc = useQueryClient()
  const teams = useQuery({ queryKey: ['teams'], queryFn: iamApi.teams, retry: false })
  const [creating, setCreating] = useState(false)
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ['teams'] })
    void qc.invalidateQueries({ queryKey: ['auth-me'] })
  }
  return (
    <div className="col" style={{ gap: 12 }}>
      <Section
        title="Teams"
        actions={
          <button className="btn primary sm" disabled={!admin} onClick={() => setCreating(true)}>
            + Create team
          </button>
        }
      >
        <div className="col" style={{ gap: 10 }}>
          <div className="hint">
            A team is an entity: the first part of a project id. Its members' roles apply to every project of the entity, including new ones. Memberships
            marked <span className="badge">sso</span> come from identity-provider groups and are refreshed at each sign-in.
          </div>
          <ErrorBox error={teams.error} />
          {teams.isLoading ? <Loading /> : teams.data && teams.data.teams.length === 0 && <Empty>No teams.</Empty>}
        </div>
      </Section>
      {teams.data?.teams.map((t) => <TeamCard key={t.name} team={t} canManage={admin || myTeamsAdmin.includes(t.name)} canDelete={admin} onChange={refresh} />)}
      {creating && <CreateTeamModal onClose={() => setCreating(false)} onDone={() => (setCreating(false), refresh())} />}
    </div>
  )
}

function TeamCard({ team, canManage, canDelete, onChange }: { team: Team; canManage: boolean; canDelete: boolean; onChange: () => void }) {
  const [user, setUser] = useState('')
  const [role, setRole] = useState<Role>('member')
  const [err, setErr] = useState<unknown>(null)
  const users = useQuery({ queryKey: ['users', false], queryFn: () => iamApi.users(false), retry: false, enabled: canManage })
  const run = async (fn: () => Promise<unknown>) => {
    setErr(null)
    try {
      await fn()
      onChange()
    } catch (e) {
      setErr(e)
    }
  }
  const memberIds = new Set(team.members.map((m) => m.user_id))
  return (
    <Section
      title={
        <span>
          <span className="mono">{team.name}</span>
          {team.display_name && <span className="muted"> · {team.display_name}</span>}
          <span className="faint small"> · {team.members.length} member(s)</span>
        </span>
      }
      actions={
        canDelete && (
          <ConfirmButton
            className="btn sm ghost danger"
            title={`Delete team ${team.name}?`}
            body="Members lose the roles they had through this team. Projects and their data are kept."
            onConfirm={async () => {
              await iamApi.teamDelete(team.name)
              onChange()
            }}
          >
            Delete team
          </ConfirmButton>
        )
      }
    >
      <div className="col" style={{ gap: 10 }} data-testid={`team-${team.name}`}>
        <ErrorBox error={err} />
        {team.members.length > 0 && (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>User</th>
                  <th>Role</th>
                  <th>Source</th>
                  <th style={{ width: 90 }} />
                </tr>
              </thead>
              <tbody>
                {team.members.map((m) => (
                  <tr key={m.user_id}>
                    <td className="mono">{m.user_id}</td>
                    <td>
                      {canManage ? (
                        <select className="input" aria-label={`role of ${m.user_id}`} value={m.role} onChange={(e) => void run(() => iamApi.teamMemberSet(team.name, m.user_id, e.target.value as Role))}>
                          {ROLES.map((r) => (
                            <option key={r} value={r}>
                              {r}
                            </option>
                          ))}
                        </select>
                      ) : (
                        <span className="badge">{m.role}</span>
                      )}
                    </td>
                    <td>
                      <span className="badge">{m.source}</span>
                    </td>
                    <td>
                      {canManage && (
                        <button className="btn sm ghost danger" onClick={() => void run(() => iamApi.teamMemberRemove(team.name, m.user_id))}>
                          Remove
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {canManage && (
          <form
            className="row wrap"
            onSubmit={(e) => {
              e.preventDefault()
              if (user) void run(() => iamApi.teamMemberSet(team.name, user, role)).then(() => setUser(''))
            }}
          >
            {users.data ? (
              <select className="input" aria-label="user to add" value={user} onChange={(e) => setUser(e.target.value)}>
                <option value="">Add a user…</option>
                {users.data.users
                  .filter((u) => !memberIds.has(u.id))
                  .map((u) => (
                    <option key={u.id} value={u.id}>
                      {u.id}
                      {u.email ? ` (${u.email})` : ''}
                    </option>
                  ))}
              </select>
            ) : (
              <input className="input mono" placeholder="user id" value={user} onChange={(e) => setUser(e.target.value.trim())} />
            )}
            <select className="input" aria-label="role to grant" value={role} onChange={(e) => setRole(e.target.value as Role)} title={ROLE_HELP[role]}>
              {ROLES.map((r) => (
                <option key={r} value={r}>
                  {r}
                </option>
              ))}
            </select>
            <button className="btn sm" type="submit" disabled={!user}>
              Add member
            </button>
          </form>
        )}
      </div>
    </Section>
  )
}

function CreateTeamModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState('')
  const [display, setDisplay] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  return (
    <Modal
      title="Create team"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button
            className="btn primary"
            disabled={busy || !name.trim()}
            onClick={async () => {
              setBusy(true)
              setErr(null)
              try {
                await iamApi.teamCreate(name.trim(), display.trim())
                onDone()
              } catch (e) {
                setErr(e)
              } finally {
                setBusy(false)
              }
            }}
          >
            Create
          </button>
        </>
      }
    >
      <div className="form-grid">
        <label className="field">
          Team (entity) name
          <input className="input mono" value={name} onChange={(e) => setName(e.target.value.replace(/[^\w.-]/g, ''))} placeholder="my-team" autoFocus />
        </label>
        <label className="field">
          Display name
          <input className="input" value={display} onChange={(e) => setDisplay(e.target.value)} placeholder="My team" />
        </label>
      </div>
      <div className="hint">Projects named <span className="mono">{name || 'my-team'}/…</span> belong to this team.</div>
      <ErrorBox error={err} />
    </Modal>
  )
}
