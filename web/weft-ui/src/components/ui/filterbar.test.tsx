import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { FilterBar } from './FilterBar'
import { snippets } from './EmptyState'

function render(url: string, page: 'sessions' | 'security' = 'sessions') {
  const qc = new QueryClient({ defaultOptions: { queries: { enabled: false } } })
  return renderToString(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[url]}>
        <Routes>
          <Route path="/:entity/:project/sessions" element={<FilterBar page={page} defaultTime="24h" />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

const text = (html: string) => html.replace(/<[^>]+>/g, ' ').replace(/&quot;/g, '"').replace(/&#x27;/g, "'").replace(/\s+/g, ' ')

describe('FilterBar', () => {
  it('shows one chip per filter in the URL, each removable', () => {
    const html = render('/demo/agents/sessions?agent=coder&has=block&score_below=0.5&tab=x')
    const t = text(html)
    expect(t).toContain('Agent coder')
    expect(t).toContain('Security Has a block')
    expect(t).toContain('Score below 0.5')
    expect(html).toContain('aria-label="Remove filter Agent"')
    expect(html).toContain('aria-label="Remove filter Security"')
    expect(t).toContain('Clear all')
  })

  it('reflects text and time from the URL and marks the box for the / shortcut', () => {
    const html = render('/demo/agents/sessions?q=oslo&time=7d')
    expect(html).toMatch(/data-filter-input[^>]*value="oslo"|value="oslo"[^>]*data-filter-input/)
    expect(html).toMatch(/<option value="7d" selected/)
    expect(html).not.toContain('Remove filter')
  })

  it('offers only the chips a page supports, with the same syntax', () => {
    const html = render('/demo/agents/sessions?agent=coder&team=t1&cost_min=1', 'security')
    expect(html).toContain('Remove filter Agent')
    expect(html).not.toContain('Remove filter Team')
    expect(html).not.toContain('Remove filter Cost')
  })
})

describe('setup snippets', () => {
  it('use the current server and project', () => {
    const s = snippets('https://weft.example', 'acme/agents', true)
    expect(s.python.code).toContain('weft.init("acme/agents", server_url="https://weft.example")')
    expect(s.typescript.code).toContain("serverUrl: 'https://weft.example'")
    expect(s.otel.code).toContain('https://weft.example/otel/v1/traces')
    expect(s.otel.code).toContain('project_id=acme/agents,Authorization=Bearer')
    expect(s['claude-code'].code).toContain('--project acme/agents')
    expect(s.agentshield.code).toContain('project: acme/agents')
    expect(snippets('http://x', 'p/q', false).otel.code).not.toContain('Authorization')
  })
})
