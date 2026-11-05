// Sign-in screen: single sign-on (when the server has OIDC configured)
// alongside API-key entry. One white panel on the page, the new look
// (DESIGN.md §7.2): the mark, a title, one sentence, then the one way in.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { setApiKey } from '../api/client'
import { ssoInfo, ssoLoginUrl } from '../api/session'
import { Logo } from './Layout'
import './common.css'

export function LoginScreen({ onKey }: { onKey: (key: string) => void }) {
  const sso = useQuery({ queryKey: ['sso-info'], queryFn: ssoInfo, retry: false, staleTime: 60_000 })
  const [key, setKey] = useState('')
  const params = new URLSearchParams(window.location.search)
  const ssoError = params.get('sso_error')
  const returnTo = window.location.pathname === '/' ? '/' : window.location.pathname + window.location.search
  const on = !!sso.data?.enabled
  let host = ''
  try {
    host = sso.data?.issuer ? new URL(sso.data.issuer).host : ''
  } catch {
    host = sso.data?.issuer ?? ''
  }
  return (
    <main className="login">
      <div className="login-card">
        <div className="login-head">
          <Logo size={34} />
          <h1>Sign in to Weft</h1>
          <p>{on ? `Use your organisation's sign-in${host ? ` (${host})` : ''}, or an API key.` : 'This server needs an API key. Ask an admin for one, or create one with the server’s admin key.'}</p>
        </div>
        {ssoError && (
          <div className="error-box" role="alert">
            {ssoError}
          </div>
        )}
        {on && (
          <>
            <a className="qbtn primary" href={ssoLoginUrl(returnTo)} data-testid="sso-login" onClick={() => setApiKey(null)}>
              Sign in with SSO
            </a>
            <p className="login-or">or use an API key</p>
          </>
        )}
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (key.trim()) onKey(key.trim())
          }}
        >
          <input
            className="input"
            type="password"
            placeholder="API key"
            aria-label="API key"
            autoComplete="off"
            value={key}
            onChange={(e) => setKey(e.target.value)}
            autoFocus={!on}
          />
          <button className={`qbtn ${on ? '' : 'primary'}`} type="submit" disabled={!key.trim()}>
            Continue
          </button>
        </form>
      </div>
    </main>
  )
}
