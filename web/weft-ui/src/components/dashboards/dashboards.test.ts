import { describe, expect, it } from 'vitest'
import type { MetricsRes } from '../../api/dashboards'
import type { Query } from '../../api/types'
import { fmtValue, mergeFilters, mergeQueries, metricLabel, movePanel, panelRequest, timeRangeBounds, toChartData, valueKind } from './dashboardUtils'
import { curlSnippet, exportBody, pythonSnippet, shQuote, toPython } from '../traces/codeSnippets'

const now = new Date('2026-05-01T12:00:00Z')

describe('time ranges', () => {
  it('computes relative and custom bounds', () => {
    expect(timeRangeBounds('1h', now)).toEqual({ start: '2026-05-01T11:00:00.000Z', end: '2026-05-01T12:00:00.000Z' })
    expect(timeRangeBounds('all', now)).toEqual({})
    expect(timeRangeBounds(undefined, now)).toEqual({})
    expect(timeRangeBounds('custom:2026-01-01T00:00:00Z/2026-01-02T00:00:00Z')).toEqual({ start: '2026-01-01T00:00:00.000Z', end: '2026-01-02T00:00:00.000Z' })
    expect(timeRangeBounds('custom:junk/2026-01-02T00:00:00Z').start).toBeUndefined()
  })
})

describe('panel requests', () => {
  it('merges dashboard and panel filters', () => {
    expect(mergeFilters({ trace_roots_only: true }, { trace_roots_only: false })).toEqual({ trace_roots_only: false })
    expect(mergeFilters({ op_names: ['a', 'b'] }, { op_names: ['b', 'c'] })).toEqual({ op_names: ['b'] })
    expect(mergeFilters({ op_names: ['a'] }, { op_names: ['c'] })?.op_names).toEqual(['\u0000none'])
    expect(mergeFilters(undefined, { op_names: ['x'] })).toEqual({ op_names: ['x'] })
  })
  it('ands query expressions', () => {
    const a: Query = { $expr: { $eq: [{ $getField: 'op_name' }, { $literal: 'x' }] } }
    const b: Query = { $expr: { $gt: [{ $getField: 'summary.weave.latency_ms' }, { $literal: 5 }] } }
    expect(mergeQueries(a, undefined)).toEqual(a)
    expect(mergeQueries(a, b)).toEqual({ $expr: { $and: [a.$expr, b.$expr] } })
    expect(mergeQueries(undefined, { $expr: {} } as never)).toBeUndefined()
  })
  it('uses the panel range over the dashboard range and drops group_by for stats', () => {
    const req = panelRequest('e/p', { id: 'x', title: '', chart: 'stat', metrics: [{ agg: 'error_rate' }], group_by: 'model', time_range: '1h' }, { time_range: '7d', filter: { trace_roots_only: true } }, now)
    expect(req.start).toBe('2026-05-01T11:00:00.000Z')
    expect(req.group_by).toBeUndefined()
    expect(req.filter).toEqual({ trace_roots_only: true })
    expect(req.max_groups).toBe(1)
  })
})

describe('chart data', () => {
  const res: MetricsRes = {
    bucket_seconds: 3600,
    start: null,
    end: null,
    buckets: ['t0', 't1'],
    metrics: [
      { name: 'calls', agg: 'count', field: '' },
      { name: 'lat', agg: 'p95', field: 'latency_ms' },
    ],
    group_by: 'model',
    count: 3,
    truncated: false,
    totals: {},
    groups: [
      { group: 'gpt', count: 2, series: { calls: [1, 1], lat: [10, 20] }, totals: {} },
      { group: null, count: 1, series: { calls: [0, 0], lat: [null, null] }, totals: {} },
    ],
  }
  it('builds one column per group × metric and drops empty series', () => {
    const { rows, series } = toChartData(res, [])
    expect(series.map((s) => s.label)).toEqual(['gpt · calls', 'gpt · lat'])
    expect(rows[1]).toMatchObject({ t: 't1', s0_0: 1, s0_1: 20, s1_0: 0, s1_1: null })
    expect(series[1].kind).toBe('duration')
  })
  it('formats values by kind', () => {
    expect(fmtValue(0.123, 'percent')).toBe('12.3%')
    expect(fmtValue(0, 'percent')).toBe('0%')
    expect(fmtValue(1500, 'duration')).toBe('1.50s')
    expect(fmtValue(0.004, 'cost')).toBe('$0.0040')
    expect(fmtValue(null, 'number')).toBe('—')
    expect(valueKind({ agg: 'sum', field: 'cost' })).toBe('cost')
    expect(metricLabel({ agg: 'p95', field: 'latency_ms' })).toBe('p95 latency (ms)')
    expect(metricLabel({ agg: 'count' })).toBe('Calls')
  })
  it('moves panels', () => {
    const ps = ['a', 'b', 'c'].map((id) => ({ id, title: '', chart: 'line' as const, metrics: [] }))
    expect(movePanel(ps, 'b', -1).map((p) => p.id)).toEqual(['b', 'a', 'c'])
    expect(movePanel(ps, 'c', 1)).toBe(ps)
  })
})

describe('code snippets', () => {
  const spec = {
    projectId: 'e/p',
    filter: { trace_roots_only: true, op_names: [] },
    query: { $expr: { $eq: [{ $getField: 'summary.weave.status' }, { $literal: 'error' }] } } as never,
    search: "it's",
    sortBy: [{ field: 'started_at', direction: 'desc' as const }],
  }
  it('renders python literals', () => {
    expect(toPython({ a: true, b: null, c: [1, 'x'] })).toBe('{"a": True, "b": None, "c": [1, "x"]}')
  })
  it('builds a python get_calls snippet with the filters', () => {
    const py = pythonSnippet(spec, 'http://h')
    expect(py).toContain('weft.init("e/p", server_url="http://h")')
    expect(py).toContain('filter={"trace_roots_only": True}')
    expect(py).toContain('"$literal": "error"')
    expect(py).toContain(`search="it's"`)
    expect(py).not.toContain('sort_by')
  })
  it('builds an export body and a shell-safe curl', () => {
    expect(exportBody(spec, 'csv')).toEqual({ project_id: 'e/p', filter: { trace_roots_only: true }, query: spec.query, search: "it's", format: 'csv' })
    expect(shQuote("a'b")).toBe(`'a'\\''b'`)
    const c = curlSnippet(spec, 'http://h', 'csv', true)
    expect(c).toContain('/calls/export')
    expect(c).toContain('$WEFT_API_KEY')
    expect(c).toContain(`it'\\''s`)
  })
})
