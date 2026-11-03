import { describe, expect, it } from 'vitest'
import type { Call } from '../../api/types'
import { callFieldValue, callGroupValue, scatterSeries, withChart } from './dashboardUtils'

const call = (id: string, over: Partial<Call> & Record<string, unknown>): Call =>
  ({
    id,
    op_name: 'weft:///e/p/op/answer:abc',
    started_at: '2026-01-01T00:00:00Z',
    ended_at: '2026-01-01T00:00:02Z',
    inputs: {},
    output: null,
    attributes: {},
    summary: {},
    ...over,
  }) as unknown as Call

describe('scatter charts', () => {
  const a = call('a', { summary: { usage: { 'gpt-4o': { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 } } }, attributes: { env: 'prod' } })
  const b = call('b', { ended_at: '2026-01-01T00:00:00.500Z', output: { score: '0.5' }, feedback: [{ feedback_type: 'weft.scorer.judge', payload: { output: { ok: true } } }] } as never)
  it('reads per-call metric values', () => {
    expect(callFieldValue(a, 'latency_ms')).toBe(2000)
    expect(callFieldValue(a, 'total_tokens')).toBe(15)
    expect(callFieldValue(b, 'total_tokens')).toBeNull()
    expect(callFieldValue(b, 'output.score')).toBe(0.5)
    expect(callFieldValue(b, 'feedback.weft.scorer.judge.output.ok')).toBe(1)
    expect(callFieldValue(a, 'attributes.env')).toBeNull()
  })
  it('groups calls', () => {
    expect(callGroupValue(a, 'model')).toBe('gpt-4o')
    expect(callGroupValue(a, 'op_name')).toBe('answer')
    expect(callGroupValue(a, 'attributes.env')).toBe('prod')
    expect(callGroupValue(b, 'attributes.env')).toBe('(none)')
    expect(callGroupValue(a, '')).toBe('')
  })
  it('keeps calls with both values and folds extra groups into Other', () => {
    const s = scatterSeries([a, b], 'latency_ms', 'total_tokens')
    expect(s).toEqual([{ group: '', points: [{ x: 2000, y: 15, id: 'a', label: 'answer' }] }])
    const many = Array.from({ length: 4 }, (_, i) => call(`c${i}`, { attributes: { g: `g${i}` } }))
    const g = scatterSeries(many, 'latency_ms', 'latency_ms', 'attributes.g', 2)
    expect(g.map((x) => [x.group, x.points.length])).toEqual([
      ['g0', 1],
      ['g1', 1],
      ['__other__', 2],
    ])
  })
  it('switches a panel to scatter with two fields', () => {
    const p = { id: 'p', title: '', chart: 'line' as const, metrics: [{ agg: 'p95' as const, field: 'latency_ms' }] }
    expect(withChart(p, 'scatter').metrics).toEqual([
      { agg: 'avg', field: 'latency_ms' },
      { agg: 'avg', field: 'total_tokens' },
    ])
    expect(withChart({ ...p, metrics: [{ agg: 'count' as const }] }, 'scatter').metrics.map((m) => m.field)).toEqual(['latency_ms', 'total_tokens'])
    expect(withChart(p, 'bar').metrics).toBe(p.metrics)
  })
})
