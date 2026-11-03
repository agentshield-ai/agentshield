import { describe, expect, it } from 'vitest'
import type { SessionSummary } from '../api/v1'
import { APPROVAL_HEAVY, computeAttention, costSpikeThreshold, lowestScore, median, roundUp } from './attention'
import { sessionMatchesClientFilters, sessionsParams, type FilterState } from './filters'

let n = 0
const s = (over: Partial<SessionSummary>): SessionSummary =>
  ({
    session_id: `s${++n}`,
    name: '',
    title: `Session ${n}`,
    started_at: '2026-11-03T09:00:00Z',
    last_activity: '2026-11-03T09:00:05Z',
    duration_ms: 5000,
    agents: ['a'],
    team_id: null,
    team_name: null,
    turn_count: 1,
    trace_count: 1,
    span_count: 3,
    llm_call_count: 1,
    tool_call_count: 1,
    handoff_count: 0,
    reasoning_step_count: 0,
    detection_count: {},
    error_count: 0,
    input_tokens: 10,
    output_tokens: 10,
    reasoning_tokens: 0,
    cost_usd: 0.01,
    status: 'ok',
    models: [],
    tools: [],
    scores: {},
    ...over,
  }) as SessionSummary

describe('attention cards', () => {
  const sessions = [
    s({ status: 'error', title: 'Broke' }),
    s({ status: 'error' }),
    s({ detection_count: { block: 1 } }),
    s({ detection_count: { require_approval: APPROVAL_HEAVY } }),
    s({ detection_count: { require_approval: APPROVAL_HEAVY - 1 } }),
    s({ scores: { Goal: { mean: 0.2, count: 1 }, Other: { mean: 0.9, count: 1 } } }),
    s({ scores: { Goal: { mean: 0.7, count: 1 } } }),
    s({ cost_usd: 0.5 }),
  ]
  const cards = Object.fromEntries(computeAttention(sessions).map((c) => [c.id, c]))

  it('counts failed sessions and opens status:error', () => {
    expect(cards.failed.count).toBe(2)
    expect(cards.failed.filter).toEqual({ status: 'error' })
    expect(cards.failed.detail).toContain('Broke')
  })

  it('counts blocked and approval-heavy sessions once each', () => {
    expect(cards.security.count).toBe(2)
    expect(cards.security.filter).toEqual({ has: 'flagged' })
    const both = computeAttention([s({ detection_count: { block: 1, require_approval: 9 } })]).find((c) => c.id === 'security')!
    expect(both.count).toBe(1)
  })

  it('counts sessions that went on after a failed step, apart from failed sessions', () => {
    const cs = computeAttention([s({ error_count: 1 }), s({ status: 'error', error_count: 2 }), s({})])
    const step = cs.find((c) => c.id === 'failed_step')!
    expect(step.count).toBe(1)
    expect(step.filter).toEqual({ status: 'failed_step' })
    expect(cs.find((c) => c.id === 'failed')!.count).toBe(1)
  })

  // What /api/v1/sessions does with the parameters sessionsParams sends.
  const server = (x: SessionSummary, p: Record<string, string>) =>
    (!p.status || x.status === p.status) &&
    (!p.has_detection ||
      (p.has_detection === 'any' ? Object.values(x.detection_count).some((v) => (v ?? 0) > 0) : (x.detection_count as Record<string, number>)[p.has_detection] > 0))
  const list = (pool: SessionSummary[], f: FilterState) => pool.filter((x) => server(x, sessionsParams(f, 'all')) && sessionMatchesClientFilters(x, f))

  it('each card opens a list with exactly the sessions it counts', () => {
    const pool = [
      ...sessions,
      s({ error_count: 1 }),
      s({ detection_count: { block: 2, require_approval: 4 } }),
      s({ detection_count: { allow: 3 } }),
      s({ cost_usd: 0.0016 }),
      s({ cost_usd: 0.0016 }),
      s({ cost_usd: 0.0016 }),
      s({ cost_usd: 0.0048 }),
    ]
    for (const c of computeAttention(pool)) {
      if (!Object.keys(c.filter).length) continue
      expect(list(pool, c.filter).length, c.id).toBe(c.count)
    }
  })

  it('flags any session-level score below 0.5', () => {
    expect(cards.low_score.count).toBe(1)
    expect(cards.low_score.filter).toEqual({ scoreBelow: { value: 0.5 } })
    expect(lowestScore(sessions[5])).toBe(0.2)
    expect(lowestScore(sessions[0])).toBeNull()
  })

  it('finds cost spikes relative to the median', () => {
    expect(median([1, 3, 2])).toBe(2)
    expect(median([1, 2, 3, 4])).toBe(2.5)
    expect(costSpikeThreshold(sessions)).toBe(0.03)
    expect(cards.cost.count).toBe(1)
    expect(cards.cost.filter).toEqual({ costMin: 0.03 })
  })

  it('states thresholds as money without floating-point noise', () => {
    expect(roundUp(0.0016 * 3)).toBe(0.0048)
    expect(roundUp(0.0123)).toBe(0.013)
    expect(roundUp(250)).toBe(250)
    const cs = computeAttention([0.0016, 0.0016, 0.0016, 0.0016, 0.0016].map((c) => s({ cost_usd: c })))
    expect(cs.find((c) => c.id === 'cost')!.detail).toBe('None at or above $0.0048.')
  })

  it('needs enough priced sessions before calling anything a spike', () => {
    const few = computeAttention([s({ cost_usd: 1 }), s({ cost_usd: 100 })]).find((c) => c.id === 'cost')!
    expect(few.count).toBe(0)
    expect(few.filter).toEqual({})
  })

  it('every card explains how it is counted', () => {
    for (const c of Object.values(cards)) expect(c.explain.length).toBeGreaterThan(40)
  })
})
