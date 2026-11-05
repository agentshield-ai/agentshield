// Who is signed in, at the foot of the sidebar: a monogram, the name and how
// they signed in, then quiet links to Users & teams and Sign out.
import { Link } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { iamApi } from '../api/iam'
import { getApiKey, setApiKey } from '../api/client'
import { logout } from '../api/session'
import { Monogram } from './ui/Monogram'
import './common.css'

export function UserMenu() {
  const qc = useQueryClient()
  const me = useQuery({ queryKey: ['auth-me'], queryFn: iamApi.me, retry: false, staleTime: 30_000 })
  const m = me.data
  if (!m || !m.auth_required) return null
  const signedIn = m.auth_method === 'session' || !!getApiKey()
  const name = m.user?.id ?? (m.auth_method === 'static_key' && m.user_id === 'anonymous' ? 'static key' : m.user_id)
  const role = m.server_admin ? 'Server admin' : m.read_only ? 'Read-only' : 'Roles by team and project'
  const via = m.auth_method === 'session' ? 'SSO' : m.auth_method === 'static_key' ? 'server key' : 'API key'
  return (
    <div className="user-menu" data-testid="user-menu">
      <div className="user-menu-box">
        <Monogram name={name} variant="user" size={28} />
        <div className="user-menu-text">
          <span className="user-menu-name" title={m.user?.email ?? name}>
            {name}
          </span>
          <span className="user-menu-role">
            {role} · {via}
          </span>
        </div>
      </div>
      <div className="user-menu-links">
        <Link to="/admin">Users &amp; teams</Link>
        {signedIn && (
          <button
            type="button"
            onClick={async () => {
              if (m.auth_method === 'session') await logout()
              else setApiKey(null)
              qc.clear()
              window.location.assign('/')
            }}
          >
            Sign out
          </button>
        )}
      </div>
    </div>
  )
}
