// Who is signed in, with links to user/team admin and sign-out (sessions).
import { Link } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { iamApi } from '../api/iam'
import { getApiKey, setApiKey } from '../api/client'
import { logout } from '../api/session'

export function UserMenu() {
  const qc = useQueryClient()
  const me = useQuery({ queryKey: ['auth-me'], queryFn: iamApi.me, retry: false, staleTime: 30_000 })
  const m = me.data
  if (!m || !m.auth_required) return null
  const signedIn = m.auth_method === 'session' || !!getApiKey()
  return (
    <div className="user-menu small" data-testid="user-menu" style={{ padding: '10px 12px', borderTop: '1px solid var(--border)', marginTop: 12 }}>
      <div className="mono" title={m.user?.email ?? undefined}>
        {m.user?.id ?? (m.auth_method === 'static_key' && m.user_id === 'anonymous' ? 'static key' : m.user_id)}
      </div>
      <div className="faint">
        {m.server_admin ? 'server admin' : m.read_only ? 'read-only' : 'roles by team/project'} ·{' '}
        {m.auth_method === 'session' ? 'SSO session' : m.auth_method === 'static_key' ? '--api-keys' : 'API key'}
      </div>
      <div className="row" style={{ gap: 8, marginTop: 6 }}>
        <Link to="/admin">Users &amp; teams</Link>
        {signedIn && (
          <button
            className="btn sm ghost"
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
