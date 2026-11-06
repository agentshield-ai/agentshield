import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { FilterBar, type AdvancedFilter } from './FilterBar'
import { snippets } from './EmptyState'

function render(url: string, page: 'sessions' | 'security' | 'traces' = 'sessions', advanced?: AdvancedFilter) {
  const qc = new QueryClient({ defaultOptions: { queries: { enabled: false } } })
  return renderToString(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[url]}>
        <Routes>
          <Route path="/:entity/:project/sessions" element={<FilterBar page={page} defaultTime="24h" advanced={advanced} />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

const text = (html: string) =>
  html
    .replace(/<[^>]+>/g, ' ')
    .replace(/&quot;/g, '"')
    .replace(/&#x27;/g, "'")
    .replace(/\s+/g, ' ')

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

  it('offers every chip on every page and says how session chips apply', () => {
    const html = render('/demo/agents/sessions?agent=coder&team=t1&cost_min=1', 'security')
    expect(html).toContain('Remove filter Agent')
    expect(html).toContain('Remove filter Team')
    expect(html).toContain('Remove filter Cost at least')
    expect(text(html)).toContain('Team, Cost at least: showing detections in a session that matches these filters.')
  })
})

describe('advanced field conditions', () => {
  it('are chips in the shared bar, with no second filter button', () => {
    const html = render('/demo/agents/sessions?agent=coder', 'traces', {
      fields: ['inputs.question'],
      clauses: [{ field: 'inputs.question', op: 'contains', value: 'Oslo' }],
      onChange: () => {},
    })
    const t = text(html)
    expect(t).toContain('Field inputs.question contains Oslo')
    expect(html).toContain('aria-label="Remove filter Field: inputs.question contains Oslo"')
    expect(t).toContain('Add filter')
    expect(t).not.toContain('+ Filter')
    expect(t).toContain('Clear all')
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
    expect(s['claude-code-otel'].code).toContain('OTEL_EXPORTER_OTLP_ENDPOINT="https://weft.example/otel"')
    expect(s['claude-code-otel'].code).toContain('OTEL_EXPORTER_OTLP_HEADERS="project_id=acme/agents,Authorization=Bearer%20')
    expect(s['claude-code-otel'].code).toContain('OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf')
    expect(snippets('http://x', 'p/q', false)['claude-code-otel'].code).toContain('OTEL_EXPORTER_OTLP_HEADERS="project_id=p/q"')
    expect(s.agentshield.code).toContain('project: acme/agents')
    expect(snippets('http://x', 'p/q', false).otel.code).not.toContain('Authorization')
  })
  it('cover OpenCode and Copilot CLI built-in telemetry', () => {
    const s = snippets('https://weft.example', 'acme/agents', true)
    expect(s['opencode-otel'].code).toContain('"experimental": { "openTelemetry": true }')
    expect(s['opencode-otel'].code).toContain('OTEL_EXPORTER_OTLP_ENDPOINT="https://weft.example/otel"')
    // OpenCode passes the header value as is (no %-decoding).
    expect(s['opencode-otel'].code).toContain('OTEL_EXPORTER_OTLP_HEADERS="project_id=acme/agents,Authorization=Bearer <your API key>"')
    expect(s['copilot-otel'].code).toContain('OTEL_EXPORTER_OTLP_ENDPOINT="https://weft.example/otel"')
    expect(s['copilot-otel'].code).toContain('OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true')
    expect(s['copilot-otel'].note).toContain('not yet tested')
    expect(snippets('http://x', 'p/q', false)['copilot-otel'].code).toContain('OTEL_EXPORTER_OTLP_HEADERS="project_id=p/q"')
  })
  it('cover Codex and Gemini CLI built-in telemetry', () => {
    const s = snippets('https://weft.example', 'acme/agents', true)
    expect(s['codex-otel'].code).toContain('endpoint = "https://weft.example/otel/v1/logs"')
    expect(s['codex-otel'].code).toContain('"project_id" = "acme/agents", "Authorization" = "Bearer <your API key>"')
    expect(s['codex-otel'].code).toContain('log_user_prompt = true')
    expect(snippets('http://x', 'p/q', false)['codex-otel'].code).not.toContain('Authorization')
    expect(s['gemini-otel'].code).toContain('GEMINI_TELEMETRY_OTLP_ENDPOINT="https://weft.example/otel"')
    expect(s['gemini-otel'].code).toContain('GEMINI_TELEMETRY_OTLP_PROTOCOL=http')
    expect(s['gemini-otel'].code).toContain('GEMINI_TELEMETRY_OTLP_HEADERS="project_id=acme/agents,Authorization=Bearer <your API key>"')
    expect(snippets('http://x', 'p/q', false)['gemini-otel'].code).toContain('GEMINI_TELEMETRY_OTLP_HEADERS="project_id=p/q"')
  })
})
