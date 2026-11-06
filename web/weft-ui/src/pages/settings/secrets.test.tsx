import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { api } from '../../api/client'
import { parseAllowedHosts, SecretModal } from './SettingsModals'

function mockFetch(body: unknown, status = 200) {
  const fn = vi.fn(async (_url: string, _init?: RequestInit) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fn)
  return fn
}

afterEach(() => vi.unstubAllGlobals())

describe('secrets API calls', () => {
  it('lists the secrets a project can use', async () => {
    const f = mockFetch({ secrets: [] })
    await api.secretsList('demo/support-bot')
    expect(f.mock.calls[0][0]).toBe('/api/v1/secrets/list?project_id=demo%2Fsupport-bot')
    await api.secretsList()
    expect(f.mock.calls[1][0]).toBe('/secrets/list')
  })
  it('sets and deletes a secret in a scope', async () => {
    const f = mockFetch({})
    await api.secretsSet({ name: 'K', value: 'v', scope: 'project', project_id: 'demo/app', allowed_hosts: ['api.example.com'] })
    expect(f.mock.calls[0][0]).toBe('/api/v1/secrets/set')
    expect(JSON.parse(f.mock.calls[0][1]!.body as string)).toEqual({
      name: 'K', value: 'v', scope: 'project', project_id: 'demo/app', allowed_hosts: ['api.example.com'],
    })
    await api.secretsDelete({ name: 'K', scope: 'team', team: 'demo' })
    expect(f.mock.calls[1][0]).toBe('/api/v1/secrets/delete')
    expect(JSON.parse(f.mock.calls[1][1]!.body as string)).toEqual({ name: 'K', scope: 'team', team: 'demo' })
  })
})

describe('secret dialog', () => {
  const noop = () => {}
  it('offers only the scopes the caller may manage', () => {
    const member = renderToStaticMarkup(<SecretModal name="K" projectId="demo/app" serverAdmin={false} teamAdmin={false} onClose={noop} onSaved={noop} />)
    expect(member).toContain('This project (demo/app)')
    expect(member).not.toContain('Team demo')
    expect(member).not.toContain('>Server<')
    const admin = renderToStaticMarkup(<SecretModal name="K" projectId="demo/app" serverAdmin teamAdmin onClose={noop} onSaved={noop} />)
    expect(admin).toContain('Team demo')
    expect(admin).toContain('>Server<')
    expect(admin).toContain('Allowed hosts')
  })
  it('starts from the scope and hosts of the secret in use', () => {
    const html = renderToStaticMarkup(
      <SecretModal
        name="K"
        projectId="demo/app"
        serverAdmin
        teamAdmin
        current={{ name: 'K', source: 'db', scope: 'server', scope_id: '', kind: 'secret', allowed_hosts: ['api.example.com'] }}
        onClose={noop}
        onSaved={noop}
      />,
    )
    expect(html).toMatch(/<option value="server" selected="">/)
    expect(html).toContain('value="api.example.com"')
  })
  it('splits allowed hosts', () => {
    expect(parseAllowedHosts(' a.example.com, *.corp.example:8443\nb ')).toEqual(['a.example.com', '*.corp.example:8443', 'b'])
    expect(parseAllowedHosts('')).toEqual([])
  })
})
