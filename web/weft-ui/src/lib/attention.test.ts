import { describe, expect, it } from 'vitest'
import type { SessionAttention } from '../api/v1'
import { attentionCards } from './attention'
import { filterFromSessionsParams, sessionsParams } from './filters'

const resp = (over: Partial<SessionAttention> = {}): SessionAttention => ({
  items: [
    { id: 'failed', count: 1500, filter: { status: 'error' }, latest: { session_id: 's-9', title: 'Broke' } },
    { id: 'failed_step', count: 1, filter: { status: 'failed_step' }, latest: { session_id: 's-4', title: '' } },
    { id: 'security', count: 2, filter: { has_detection: 'flagged' }, latest: null },
    { id: 'low_score', count: 1, filter: { score_below: '0.5' }, latest: null },
    { id: 'cost', count: 1, filter: { cost_min: '0.0048' }, latest: null },
  ],
  sessions: 1600,
  blocked: 1,
  approval_heavy: 1,
  lowest_score: 0.2,
  cost_spike_threshold: 0.0048,
  approval_heavy_threshold: 3,
  low_score_threshold: 0.5,
  cost_spike_factor: 3,
  ...over,
})

describe('attention cards', () => {
  const cards = Object.fromEntries(attentionCards(resp()).map((c) => [c.id, c]))

  it('shows the server counts as they are (never cut at a page of sessions)', () => {
    expect(cards.failed.count).toBe(1500)
    expect(cards.failed.detail).toContain('Broke')
    expect(cards.security.detail).toBe('1 session with a blocked tool call, 1 with 3+ approval requests')
    expect(cards.low_score.detail).toBe('Lowest: 0.20')
    expect(cards.cost.detail).toBe('At or above $0.0048 each')
  })

  it('opens each card with the filter the server gave', () => {
    expect(cards.failed.filter).toEqual({ status: 'error' })
    expect(cards.failed_step.filter).toEqual({ status: 'failed_step' })
    expect(cards.security.filter).toEqual({ has: 'flagged' })
    expect(cards.low_score.filter).toEqual({ scoreBelow: { value: 0.5 } })
    expect(cards.cost.filter).toEqual({ costMin: 0.0048 })
    // ...and sends it back to /api/v1/sessions unchanged.
    for (const c of resp().items) expect(sessionsParams(filterFromSessionsParams(c.filter), 'all'), c.id).toEqual(c.filter)
  })

  it('needs enough priced sessions before calling anything a spike', () => {
    const none = attentionCards(resp({ cost_spike_threshold: null, items: resp().items.map((c) => (c.id === 'cost' ? { ...c, count: 0, filter: {} } : c)) }))
    const cost = none.find((c) => c.id === 'cost')!
    expect(cost.count).toBe(0)
    expect(cost.filter).toEqual({})
    expect(cost.detail).toBe('Needs at least four sessions with a known cost.')
  })

  it('every card explains how it is counted', () => {
    for (const c of Object.values(cards)) expect(c.explain.length).toBeGreaterThan(40)
  })
})
