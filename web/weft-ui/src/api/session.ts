// Browser sessions (OIDC single sign-on) and CSRF protection.
//
// A signed-in browser holds an HttpOnly session cookie plus a readable
// `weft_csrf` cookie. The server rejects cookie-authenticated non-GET
// requests unless they echo that token in `X-Weft-CSRF`. Rather than touch
// every fetch call site, installCsrfFetch wraps window.fetch once so all
// same-origin mutating requests carry the header. API-key requests ignore it.

export const CSRF_HEADER = 'X-Weft-CSRF'
const API_BASE: string = (import.meta.env?.VITE_WEFT_API as string | undefined) ?? ''

export function readCookie(name: string, cookieString?: string): string | null {
  const src = cookieString ?? (typeof document !== 'undefined' ? document.cookie : '')
  for (const part of src.split(';')) {
    const i = part.indexOf('=')
    if (i < 0) continue
    if (part.slice(0, i).trim() === name) return decodeURIComponent(part.slice(i + 1).trim())
  }
  return null
}

export function csrfToken(): string | null {
  return readCookie('weft_csrf')
}

const SAFE = new Set(['GET', 'HEAD', 'OPTIONS'])

/** Whether a request to url needs the CSRF header (same origin and not a safe method). */
export function needsCsrf(method: string | undefined, url: string, origin: string): boolean {
  if (SAFE.has((method ?? 'GET').toUpperCase())) return false
  try {
    return new URL(url, origin).origin === origin
  } catch {
    return false
  }
}

let installed = false

export function installCsrfFetch() {
  if (installed || typeof window === 'undefined' || typeof window.fetch !== 'function') return
  installed = true
  const orig = window.fetch.bind(window)
  window.fetch = (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    const method = init?.method ?? (input instanceof Request ? input.method : 'GET')
    const token = csrfToken()
    if (token && needsCsrf(method, url, window.location.origin)) {
      const headers = new Headers(init?.headers ?? (input instanceof Request ? input.headers : undefined))
      if (!headers.has(CSRF_HEADER)) headers.set(CSRF_HEADER, token)
      return orig(input, { ...init, headers })
    }
    return orig(input, init)
  }
}

export interface SsoInfo {
  enabled: boolean
  login_url?: string
  issuer?: string
}

export async function ssoInfo(): Promise<SsoInfo> {
  const res = await fetch(API_BASE + '/auth/sso')
  if (!res.ok) return { enabled: false }
  return res.json() as Promise<SsoInfo>
}

export function ssoLoginUrl(returnTo: string): string {
  return `${API_BASE}/auth/oidc/login?return_to=${encodeURIComponent(returnTo || '/')}`
}

export async function logout(): Promise<void> {
  const token = csrfToken()
  await fetch(API_BASE + '/auth/logout', { method: 'POST', headers: token ? { [CSRF_HEADER]: token } : {} }).catch(() => undefined)
}

/** Whether this browser holds a session (the CSRF cookie is set with it). */
export function hasSession(): boolean {
  return csrfToken() !== null
}
