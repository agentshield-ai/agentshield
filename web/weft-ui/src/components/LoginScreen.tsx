// Sign-in screen: single sign-on (when the server has OIDC configured)
// alongside API-key entry.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { setApiKey } from '../api/client'
import { ssoInfo, ssoLoginUrl } from '../api/session'
import { Logo } from './Layout'

export function LoginScreen({ onKey }: { onKey: (key: string) => void }) {
  const sso = useQuery({ queryKey: ['sso-info'], queryFn: ssoInfo, retry: false, staleTime: 60_000 })
  const [key, setKey] = useState('')
  const params = new URLSearchParams(window.location.search)
  const ssoError = params.get('sso_error')
  const returnTo = window.location.pathname === '/' ? '/' : window.location.pathname + window.location.search
  return (
    <div className="empty" style={{ maxWidth: 420, margin: '10vh auto' }}>
      <div className="col" style={{ alignItems: 'center', gap: 14, width: '100%' }}>
        <Logo />
        <h2>Sign in to Weft</h2>
        {ssoError && (
          <div className="error-box" role="alert" style={{ width: '100%' }}>
            {ssoError}
          </div>
        )}
        {sso.data?.enabled && (
          <>
            <a className="btn primary" style={{ width: '100%', justifyContent: 'center' }} href={ssoLoginUrl(returnTo)} data-testid="sso-login" onClick={() => setApiKey(null)}>
              Sign in with SSO
            </a>
            {sso.data.issuer && <div className="faint small">{new URL(sso.data.issuer).host}</div>}
            <div className="muted small">or use an API key</div>
          </>
        )}
        <form
          className="row"
          style={{ width: '100%' }}
          onSubmit={(e) => {
            e.preventDefault()
            if (key.trim()) onKey(key.trim())
          }}
        >
          <input
            className="input grow"
            type="password"
            placeholder="API key"
            aria-label="API key"
            value={key}
            onChange={(e) => setKey(e.target.value)}
            autoFocus={!sso.data?.enabled}
          />
          <button className={`btn ${sso.data?.enabled ? '' : 'primary'}`} type="submit" disabled={!key.trim()}>
            Continue
          </button>
        </form>
      </div>
    </div>
  )
}
