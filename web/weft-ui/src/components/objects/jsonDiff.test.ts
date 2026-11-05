import { describe, expect, it } from 'vitest'
import { pathWords } from './JsonDiff'

describe('pathWords', () => {
  it.each([
    ['messages.0.content', 'Message 1 › content'],
    ['_type', 'Type'],
    ['default_params.temperature', 'Default params › temperature'],
    ['0', 'Item 1'],
    ['scorers.2', 'Scorer 3'],
  ])('%s', (path, want) => expect(pathWords(path)).toBe(want))
})
