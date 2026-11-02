// Users, teams, roles and the audit log. See docs/weft/API.md
// "Users, teams and roles" and "Audit log".
import { ApiError, get, getApiKey, post } from './client'

const API_BASE: string = (import.meta.env?.VITE_WEFT_API as string | undefined) ?? ''

export type Role = 'viewer' | 'member' | 'admin'
export const ROLES: Role[] = ['viewer', 'member', 'admin']
export const ROLE_HELP: Record<Role, string> = {
  viewer: 'read traces, objects and evaluations',
  member: 'also write calls, objects, feedback and run evaluations',
  admin: 'also manage members, keys, settings and delete the project',
}

export interface User {
  id: string
  name: string
  email: string | null
  admin: boolean
  sso: boolean
  created_at: string
  disabled_at: string | null
  last_login_at: string | null
}

export interface TeamMember {
  user_id: string
  role: Role
  source: 'manual' | 'sso'
}

export interface Team {
  name: string
  display_name: string
  created_at: string
  members: TeamMember[]
}

export interface ProjectMember {
  user_id: string
  name: string
  email: string | null
  role: Role
  project_role: Role | ''
  team_role: Role | ''
  server_admin: boolean
  disabled: boolean
}

export interface Me {
  user: User | null
  user_id: string
  auth_required: boolean
  auth_method: 'none' | 'static_key' | 'api_key' | 'session'
  server_admin: boolean
  read_only: boolean
  projects: string[] | null
  key_id: string | null
  roles: { entities: Record<string, Role>; projects: Record<string, Role> }
  sso_enabled: boolean
}

export interface AuditEntry {
  id: number
  ts: string
  actor: string
  auth_method: string
  key_id: string | null
  method: string
  path: string
  project_id: string | null
  object_ids: string[]
  call_ids: string[]
  status: number
  outcome: 'success' | 'denied' | 'unauthenticated' | 'failed'
  ip: string
  detail: string
}

export interface AuditFilter {
  project_id?: string
  actor?: string
  method?: string
  path_prefix?: string
  outcome?: string
  since?: string
  until?: string
  limit?: number
  offset?: number
}

export const iamApi = {
  me: () => get<Me>('/auth/me'),
  users: (includeDisabled = false) => get<{ users: User[] }>(`/users/list${includeDisabled ? '?include_disabled=true' : ''}`),
  userCreate: (u: { id: string; name?: string; email?: string | null; admin?: boolean }) => post<{ user: User }>('/users/create', u),
  userUpdate: (id: string, up: { name?: string; email?: string | null; admin?: boolean; disabled?: boolean }) =>
    post<{ user: User }>('/users/update', { id, ...up }),
  userRevokeSessions: (id: string) => post<{ revoked: number }>('/users/revoke_sessions', { id }),
  teams: () => get<{ teams: Team[] }>('/teams/list'),
  teamCreate: (name: string, display_name = '') => post<{ team: Team }>('/teams/create', { name, display_name }),
  teamDelete: (name: string) => post<object>('/teams/delete', { name }),
  teamMemberSet: (team: string, user_id: string, role: Role) => post<{ team: Team }>('/teams/members/set', { team, user_id, role }),
  teamMemberRemove: (team: string, user_id: string) => post<{ team: Team }>('/teams/members/remove', { team, user_id }),
  projectMembers: (project_id: string) =>
    post<{ project_id: string; entity: string; members: ProjectMember[]; can_manage: boolean }>('/project/members/list', { project_id }),
  projectMemberSet: (project_id: string, user_id: string, role: Role) => post<object>('/project/members/set', { project_id, user_id, role }),
  projectMemberRemove: (project_id: string, user_id: string) => post<object>('/project/members/remove', { project_id, user_id }),
  audit: (f: AuditFilter) => post<{ entries: AuditEntry[]; total_count: number; retention_days: number }>('/audit/query', f),
  /** Downloads the filtered audit log as CSV (a Blob). */
  auditCsv: async (f: AuditFilter): Promise<Blob> => {
    const h: Record<string, string> = { 'Content-Type': 'application/json' }
    const key = getApiKey()
    if (key) h['Authorization'] = `Bearer ${key}`
    const res = await fetch(API_BASE + '/audit/export', { method: 'POST', headers: h, body: JSON.stringify(f) })
    if (!res.ok) {
      let msg = res.statusText
      try {
        msg = (await res.json()).reason ?? msg
      } catch {
        /* not JSON */
      }
      throw new ApiError(res.status, msg)
    }
    return res.blob()
  },
}

/** The role a user holds on a project from team and project grants. */
export function effectiveRole(me: Me | undefined, projectId: string): Role | null {
  if (!me) return null
  if (me.server_admin) return 'admin'
  const rank = (r?: Role) => (r ? ROLES.indexOf(r) + 1 : 0)
  const ent = projectId.split('/')[0]
  const a = me.roles.entities[ent]
  const b = me.roles.projects[projectId]
  const best = rank(a) >= rank(b) ? a : b
  if (!best) return null
  if (me.read_only) return 'viewer'
  return best
}
