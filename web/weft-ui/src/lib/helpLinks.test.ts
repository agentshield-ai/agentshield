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
  })
})
