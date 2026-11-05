// Server administration, Users & teams (DESIGN.md §7.1 rule 1, §7.2): a
// segmented control for Users and Teams (?tab=), a sentence that answers each
// ("5 users; 1 is a server admin and 1 is disabled."), and labelled rows with
// their controls on the right. Disabling a user, signing them out and
// deleting a team ask first.
import { useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { iamApi, ROLE_HELP, ROLES, type Role, type Team, type User } from '../../api/iam'
import { ErrorBox, Modal, Spinner } from '../../components/common'
import { ConfirmButton, Switch } from '../../components/objects/bits'
import { SettingsEmpty, SettingsRow, SettingsSection, SettingsSentence } from '../../components/settings/SettingsList'
import { PageHeader } from '../../components/ui/PageHeader'
import { Icon } from '../../components/ui/Icons'
import { Segmented, segmentedTabId } from '../../components/ui/Segmented'
import { fmtDate } from '../../lib/format'
import { relativeTime } from '../../lib/summaries'
import { teamsSentence, usersSentence } from '../../lib/summaries/project'

type Tab = 'users' | 'teams'
const roleWord = (r: string) => r[0].toUpperCase() + r.slice(1)

export function AdminPage() {
  const [params, setParams] = useSearchParams()
  const tab: Tab = params.get('tab') === 'teams' ? 'teams' : 'users'
  const me = useQuery({ queryKey: ['auth-me'], queryFn: iamApi.me, retry: false })
  return (
    <div className="page-frame">
      <PageHeader
        variant="detail"
        back={{ to: '/', label: 'Projects' }}
        title="Users & teams"
        help="project.md#users-and-teams"
        helpTopic="Users and teams"
        meta={me.data && !me.data.server_admin ? 'You are not a server admin: you see your own teams and manage the members of teams you administer.' : undefined}
      />
      <ErrorBox error={me.error} />
      <Segmented
        label="Users or teams"
        role="tabs"
        panelId="admin-view"
        value={tab}
        onChange={(t) => setParams(t === 'users' ? {} : { tab: t }, { replace: true })}
        options={[
          { id: 'users', label: 'Users' },
          { id: 'teams', label: 'Teams' },
        ]}
      />
      <div role="tabpanel" id="admin-view" aria-labelledby={segmentedTabId('admin-view', tab)} className="col" style={{ gap: 26, minWidth: 0 }}>
        {tab === 'users' && <UsersTab admin={!!me.data?.server_admin} myId={me.data?.user?.id} />}
        {tab === 'teams' && (
          <TeamsTab
            admin={!!me.data?.server_admin}
            myTeamsAdmin={
              me.data
                ? Object.entries(me.data.roles.entities)
                    .filter(([, r]) => r === 'admin')
                    .map(([t]) => t)
                : []
            }
          />
        )}
      </div>
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
    <>
      {users.data && <SettingsSentence>{usersSentence(users.data.users)}</SettingsSentence>}
      <SettingsSection
        title="Users"
        action={
          <>
            <label className="checkbox small">
              <input type="checkbox" checked={showDisabled} onChange={(e) => setShowDisabled(e.target.checked)} /> Show disabled users
            </label>
            <button type="button" className="qbtn primary" disabled={!admin} onClick={() => setEditing('new')}>
              Add a user
            </button>
          </>
        }
        note="People who sign in with SSO are added automatically."
      >
        {users.error || err ? (
          <SettingsEmpty>
            <ErrorBox error={users.error ?? err} />
          </SettingsEmpty>
        ) : null}
        {users.isLoading ? (
          <SettingsEmpty>
            <Spinner />
          </SettingsEmpty>
        ) : !users.data || users.data.users.length === 0 ? (
          users.data && <SettingsEmpty>{admin ? 'Add a user to give someone their own API keys and sign-in.' : 'Only server admins add users.'}</SettingsEmpty>
        ) : (
          users.data.users.map((u) => (
            <SettingsRow
              key={u.id}
              muted={!!u.disabled_at}
              label={
                <>
                  <span className="mono">{u.id}</span>
                  {u.name !== u.id && <span className="quiet-note" style={{ display: 'inline' }}> · {u.name}</span>}
                  {u.id === myId && <span className="quiet-note" style={{ display: 'inline' }}> · you</span>}
                </>
              }
              detail={
                <>
                  {[u.email, u.admin ? 'Server admin' : 'Roles by team and project', u.disabled_at ? 'disabled' : null, u.sso ? 'signs in with SSO' : 'uses API keys'].filter(Boolean).join(' · ')}
                  <br />
                  <span title={fmtDate(u.created_at)}>Added {relativeTime(u.created_at)}</span>
                  {u.last_login_at && <span title={fmtDate(u.last_login_at)}> · last signed in {relativeTime(u.last_login_at)}</span>}
                </>
              }
              control={
                admin ? (
                  <>
                    <button type="button" className="qbtn sm" onClick={() => setEditing(u)}>
                      Edit
                    </button>
                    <ConfirmButton
                      className="qbtn sm"
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
                          type="button"
                          className="qbtn sm"
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
                          className="qbtn sm plain danger"
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
                  </>
                ) : undefined
              }
            />
          ))
        )}
      </SettingsSection>
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
    </>
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
      title={user ? `Edit ${user.id}` : 'Add a user'}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="qbtn" onClick={onClose}>
            Cancel
          </button>
          <button
            type="button"
            className="qbtn primary"
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
        <div className="field">
          Server admin
          <div className="row">
            <Switch on={admin} onChange={setAdmin} disabled={self} label="Server admin" title={self ? 'You cannot remove your own admin role' : undefined} />
            <span className="small muted">Admin of every project; manages users, teams and secrets.</span>
          </div>
        </div>
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
    <>
      {teams.data && <SettingsSentence>{teamsSentence(teams.data.teams)}</SettingsSentence>}
      <SettingsSection
        plain
        title="Teams"
        action={
          <button type="button" className="qbtn primary" disabled={!admin} onClick={() => setCreating(true)}>
            Create a team
          </button>
        }
        note="A team's roles apply to every project whose id starts with its name."
      >
        <ErrorBox error={teams.error} />
        {teams.isLoading && <Spinner />}
      </SettingsSection>
      {teams.data?.teams.map((t) => <TeamCard key={t.name} team={t} canManage={admin || myTeamsAdmin.includes(t.name)} canDelete={admin} onChange={refresh} />)}
      {creating && <CreateTeamModal onClose={() => setCreating(false)} onDone={() => (setCreating(false), refresh())} />}
    </>
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
    <SettingsSection
      title={
        <>
          <span className="mono">{team.name}</span>
          {team.display_name && <span className="quiet-note" style={{ display: 'inline' }}> · {team.display_name}</span>}
          <span className="quiet-note" style={{ display: 'inline' }}>
            {' '}
            · {team.members.length} {team.members.length === 1 ? 'member' : 'members'}
          </span>
        </>
      }
      action={
        canDelete && (
          <ConfirmButton
            className="qbtn sm plain danger"
            title={`Delete team ${team.name}?`}
            body="Members lose the roles they had through this team. Projects and their data are kept."
            ariaLabel={`Delete team ${team.name}`}
            tooltip="Delete this team"
            onConfirm={async () => {
              await iamApi.teamDelete(team.name)
              onChange()
            }}
          >
            <Icon name="trash" size={14} />
          </ConfirmButton>
        )
      }
    >
      {err ? (
        <SettingsEmpty>
          <ErrorBox error={err} />
        </SettingsEmpty>
      ) : null}
      {team.members.length === 0 && !canManage && <SettingsEmpty>No members yet.</SettingsEmpty>}
      {team.members.map((m) => (
        <SettingsRow
          key={m.user_id}
          label={<span className="mono">{m.user_id}</span>}
          detail={m.source === 'sso' ? 'From SSO groups' : 'Added by hand'}
          control={
            canManage ? (
              <>
                <select className="input" aria-label={`role of ${m.user_id}`} value={m.role} onChange={(e) => void run(() => iamApi.teamMemberSet(team.name, m.user_id, e.target.value as Role))}>
                  {ROLES.map((r) => (
                    <option key={r} value={r}>
                      {roleWord(r)}
                    </option>
                  ))}
                </select>
                <button
                  type="button"
                  className="qbtn sm plain danger"
                  aria-label={`Remove ${m.user_id} from ${team.name}`}
                  title="Remove from the team"
                  onClick={() => void run(() => iamApi.teamMemberRemove(team.name, m.user_id))}
                >
                  <Icon name="trash" size={14} />
                </button>
              </>
            ) : (
              <span className="quiet-note">{roleWord(m.role)}</span>
            )
          }
        />
      ))}
      {canManage && (
        <SettingsRow
          label="Add a member"
          control={
            <form
              className="row wrap"
              onSubmit={(e) => {
                e.preventDefault()
                if (user) void run(() => iamApi.teamMemberSet(team.name, user, role)).then(() => setUser(''))
              }}
            >
              {users.data ? (
                <select className="input" aria-label="user to add" value={user} onChange={(e) => setUser(e.target.value)}>
                  <option value="">Choose a user…</option>
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
                <input className="input mono" aria-label="user to add" placeholder="user id" value={user} onChange={(e) => setUser(e.target.value.trim())} />
              )}
              <select className="input" aria-label="role to grant" value={role} onChange={(e) => setRole(e.target.value as Role)} title={ROLE_HELP[role]}>
                {ROLES.map((r) => (
                  <option key={r} value={r}>
                    {roleWord(r)}
                  </option>
                ))}
              </select>
              <button className="qbtn sm" type="submit" disabled={!user}>
                Add
              </button>
            </form>
          }
        />
      )}
    </SettingsSection>
  )
}

function CreateTeamModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState('')
  const [display, setDisplay] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  return (
    <Modal
      title="Create a team"
      onClose={onClose}
      footer={
        <>
          <button type="button" className="qbtn" onClick={onClose}>
            Cancel
          </button>
          <button
            type="button"
            className="qbtn primary"
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
          Team name (the first part of project ids)
          <input className="input mono" value={name} onChange={(e) => setName(e.target.value.replace(/[^\w.-]/g, ''))} placeholder="my-team" autoFocus />
        </label>
        <label className="field">
          Display name
          <input className="input" value={display} onChange={(e) => setDisplay(e.target.value)} placeholder="My team" />
        </label>
      </div>
      <div className="hint">
        Projects named <span className="mono">{name || 'my-team'}/…</span> belong to this team.
      </div>
      <ErrorBox error={err} />
    </Modal>
  )
}
