import { describe, expect, it } from 'vitest'
import { guideLink, slugger } from './helpLinks'

describe('guide anchors and links', () => {
  it('spells anchors like the guides cross-reference them', () => {
    const s = slugger()
    expect(s('Run it')).toBe('run-it')
    expect(s('Custom charts, dashboards and export')).toBe('custom-charts-dashboards-and-export')
    expect(s('7. UI')).toBe('7-ui')
    expect(s('Agents')).toBe('agents')
    expect(s('Agents')).toBe('agents-1')
    expect(s('`/call/start` (Weft)')).toBe('callstart-weft')
  })
  it('maps links', () => {
    expect(guideLink('API.md#objects')).toEqual({ kind: 'guide', to: '/help/API.md#objects' })
    expect(guideLink('./README.md')).toEqual({ kind: 'guide', to: '/help/README.md' })
    expect(guideLink('#scores')).toEqual({ kind: 'anchor', hash: 'scores' })
    expect(guideLink('https://opentelemetry.io/')).toEqual({ kind: 'external', href: 'https://opentelemetry.io/' })
    expect(guideLink('../../scripts/x.py')).toEqual({ kind: 'text' })
    expect(guideLink('javascript:alert(1)')).toEqual({ kind: 'text' })
    // From a user guide in docs/weft/guides to the reference, and back.
    expect(guideLink('../README.md#agentshield-integration')).toEqual({ kind: 'guide', to: '/help/README.md#agentshield-integration' })
    expect(guideLink('guides/sessions.md')).toEqual({ kind: 'guide', to: '/help/sessions.md' })
    expect(guideLink('../../sdk/python/README.md')).toEqual({ kind: 'text' })
  })
})

// Every "Read the guide" link points at a user guide, at a heading it has.
describe('page help links', () => {
  const guides = import.meta.glob('../../../../docs/weft/guides/*.md', { query: '?raw', import: 'default', eager: true }) as Record<string, string>
  const sources = import.meta.glob(['../**/*.{ts,tsx}', '!../**/*.test.*'], { query: '?raw', import: 'default', eager: true }) as Record<string, string>
  const guide = (name: string) => Object.entries(guides).find(([p]) => p.endsWith(`/guides/${name}`))?.[1]
  const links = Object.entries(sources).flatMap(([f, src]) =>
    [...src.matchAll(/(?:\bdoc\s*[:=]\s*|docsUrl\(|^\s*)['"]([A-Za-z0-9_-]+\.md)(#[a-z0-9-]+)?['"]/gm)].map((m) => ({ f, doc: m[1], hash: m[2] })),
  )

  it('finds the links', () => {
    expect(links.length).toBeGreaterThan(20)
  })

  it('go to user guides that exist, at headings that exist', () => {
    for (const { f, doc, hash } of links) {
      const md = guide(doc)
      expect(md, `${f}: ${doc} is not a user guide`).toBeTypeOf('string')
      if (!hash) continue
      const slug = slugger()
      const ids = md!
        .split('\n')
        .filter((l) => /^#{1,4} /.test(l))
        .map((l) => slug(l.replace(/^#+ /, '')))
      expect(ids, `${f}: ${doc}${hash}`).toContain(hash.slice(1))
    }
  })
})
