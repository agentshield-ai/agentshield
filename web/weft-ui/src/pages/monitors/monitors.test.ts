import { describe, expect, it } from 'vitest'
import { activityOf, monitorWords } from './MonitorsPage'

describe('monitor rows', () => {
  const now = Date.parse('2026-11-05T12:00:00Z')
  it('reads the last score and the scores of the last 24 hours (newest first)', () => {
    const items = [{ created_at: '2026-11-05T11:00:00Z' }, { created_at: '2026-11-05T01:00:00Z' }, { created_at: '2026-11-03T01:00:00Z' }]
    expect(activityOf(items, 500, now)).toEqual({ lastAt: '2026-11-05T11:00:00Z', today: 2, capped: false })
    expect(activityOf([], 500, now)).toEqual({ lastAt: null, today: 0, capped: false })
    expect(activityOf(items.slice(0, 2), 2, now).capped).toBe(true)
  })
  it('reads a stored monitor, older field names included', () => {
    expect(monitorWords({ active: true, sampling_rate: 0.5, op_names: ['answer'], scopes: ['call'], scorers: ['weft:///e/p/object/goal:abc'] })).toEqual({
      scorers: ['goal'],
      scopes: ['span'],
      names: ['answer'],
      rate: 0.5,
      active: true,
    })
  })
})
