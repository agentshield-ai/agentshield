import { describe, expect, it } from 'vitest'
import { isOfflineModel, modelDisplayName, modelInProse } from './modelWords'

describe('modelDisplayName', () => {
  it.each([
    ['mock/echo', 'offline echo model'],
    ['mock/json', 'offline JSON judge'],
    ['mock/other', 'offline test model'],
    ['gpt-4o-mini', 'gpt-4o-mini'],
    ['anthropic/claude-sonnet-4', 'anthropic/claude-sonnet-4'],
    ['', ''],
    [null, ''],
  ])('%s', (id, want) => expect(modelDisplayName(id)).toBe(want))
})

describe('modelInProse', () => {
  it.each([
    ['mock/echo', 'the offline echo model'],
    ['mock/json', 'the offline JSON judge'],
    ['gpt-4o', 'gpt-4o'],
  ])('%s', (id, want) => expect(modelInProse(id)).toBe(want))
  it('knows offline ids', () => expect([isOfflineModel('mock/echo'), isOfflineModel('gpt-4o'), isOfflineModel(undefined)]).toEqual([true, false, false]))
})
