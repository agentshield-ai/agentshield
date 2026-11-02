import { describe, expect, it } from 'vitest'
import { weaveLinkTarget } from './weaveLinks'

describe('weaveLinkTarget', () => {
  it('maps agent conversation links', () => {
    expect(weaveLinkTarget('e', 'p', 'agents/conversations/abc-123')).toBe('/e/p/conversations/abc-123')
  })
  it('keeps sections Weft has', () => {
    expect(weaveLinkTarget('e', 'p', 'calls/0193')).toBe('/e/p/calls/0193')
    expect(weaveLinkTarget('e', 'p', 'objects/ds/versions/abc')).toBe('/e/p/objects/ds/versions/abc')
    expect(weaveLinkTarget('e', 'p', 'agents/')).toBe('/e/p/agents')
  })
  it('sends unknown sections to traces', () => {
    expect(weaveLinkTarget('e', 'p', 'boards/x')).toBe('/e/p/traces')
    expect(weaveLinkTarget('e', 'p', '')).toBe('/e/p/traces')
  })
})
