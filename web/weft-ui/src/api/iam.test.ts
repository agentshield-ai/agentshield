import { describe, expect, it } from 'vitest'
import { effectiveRole, type Me } from './iam'
import { needsCsrf, readCookie } from './session'

describe('CSRF helpers', () => {
  it('reads cookies by name', () => {
    const c = 'weft_session=abc; weft_csrf=tok%2Den; other=1'
    expect(readCookie('weft_csrf', c)).toBe('tok-en')
    expect(readCookie('missing', c)).toBeNull()
    expect(readCookie('weft_csrf', '')).toBeNull()
  })
  it('adds the header only to same-origin writes', () => {
    const o = 'https://weft.example.com'
    expect(needsCsrf('POST', '/calls/query', o)).toBe(true)
    expect(needsCsrf('delete', 'https://weft.example.com/x', o)).toBe(true)
    expect(needsCsrf('GET', '/projects', o)).toBe(false)
    expect(needsCsrf(undefined, '/projects', o)).toBe(false)
    expect(needsCsrf('POST', 'https://evil.example/collect', o)).toBe(false)
  })
})

describe('effectiveRole', () => {
  const base: Me = {
    user: null,
    user_id: 'bob',
    auth_required: true,
    auth_method: 'session',
    server_admin: false,
    read_only: false,
    projects: null,
    key_id: null,
    roles: { entities: { acme: 'viewer' }, projects: { 'acme/app': 'member' } },
    sso_enabled: true,
  }
  it('takes the stronger of team and project roles', () => {
    expect(effectiveRole(base, 'acme/app')).toBe('member')
    expect(effectiveRole(base, 'acme/other')).toBe('viewer')
    expect(effectiveRole(base, 'beta/app')).toBeNull()
  })
  it('caps read-only keys and lifts server admins', () => {
    expect(effectiveRole({ ...base, read_only: true }, 'acme/app')).toBe('viewer')
    expect(effectiveRole({ ...base, server_admin: true }, 'beta/app')).toBe('admin')
  })
})
