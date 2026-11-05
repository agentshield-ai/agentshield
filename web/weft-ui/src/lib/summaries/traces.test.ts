import { describe, expect, it } from 'vitest'
import { compareSentence, spanSentence, spanSessionSentence, spanSessionsSentence, statusGlyph, tracesSentence, type TracePart } from './traces'

const text = (ps: TracePart[]) => ps.map((p) => (typeof p === 'string' ? p : p.text)).join('')

describe('tracesSentence', () => {
  const cases: [Parameters<typeof tracesSentence>[0], string][] = [
    [{ total: 1240, failed: 12, roots: true, time: '24h', filtered: false }, '1,240 traces in the last 24 hours. 12 failed.'],
    [{ total: 1240, failed: 0, roots: false, time: '7d', filtered: false }, '1,240 spans in the last 7 days. None failed.'],
    [{ total: 1, failed: 1, roots: true, time: '24h', filtered: false }, '1 trace in the last 24 hours. It failed.'],
    [{ total: 3, failed: 3, roots: true, time: 'all', filtered: false }, '3 traces recorded. All 3 failed.'],
    [{ total: 3, failed: null, roots: true, time: '24h', filtered: true }, '3 traces match these filters in the last 24 hours.'],
    [{ total: 1, failed: 0, roots: true, time: '30d', filtered: true }, '1 trace matches these filters in the last 30 days. It did not fail.'],
    [{ total: 0, failed: 0, roots: true, time: '24h', filtered: false }, 'No traces in the last 24 hours.'],
    [{ total: 0, failed: null, roots: false, time: 'all', filtered: true }, 'No spans match these filters.'],
  ]
  for (const [i, want] of cases) it(want, () => expect(text(tracesSentence(i))).toBe(want))
  it('links the failed clause to the failed runs', () => {
    expect(tracesSentence({ total: 5, failed: 2, roots: true, time: '24h', filtered: false })[2]).toEqual({ text: '2 failed', status: 'error' })
  })
})

describe('statusGlyph', () => {
  const cases: [Parameters<typeof statusGlyph>[0], string, string][] = [
    ['ok', 'ok', 'Succeeded'],
    ['success', 'ok', 'Succeeded'],
    ['error', 'failed', 'Failed'],
    ['failed_step', 'warning', 'Went on after a failed step'],
    ['descendant_error', 'warning', 'Succeeded, a span under it failed'],
    ['running', 'running', 'Running'],
    ['unknown', 'unknown', 'No agent data'],
  ]
  for (const [s, kind, label] of cases) it(s, () => expect(statusGlyph(s)).toEqual({ kind, label }))
})

describe('spanSentence', () => {
  const base = { latencyMs: 4000, below: 0, belowFailed: 0, tokens: 0, cost: null }
  const cases: [Parameters<typeof spanSentence>[0], string][] = [
    [{ ...base, status: 'success', below: 6, tokens: 960, cost: 0.0062 }, 'Succeeded in 4 seconds. 6 spans ran under it. Used 960 tokens ($0.0062).'],
    [{ ...base, status: 'error', latencyMs: 150, error: 'KeyError: x\n  at line 3' }, 'Failed after 150 milliseconds with “KeyError: x”.'],
    [{ ...base, status: 'error', below: 3, belowFailed: 1 }, 'Failed after 4 seconds. 3 spans ran under it, 1 of them failed.'],
    [{ ...base, status: 'descendant_error', below: 7, belowFailed: 1 }, 'Succeeded in 4 seconds, though one of the 7 spans under it failed.'],
    [{ ...base, status: 'descendant_error', below: 2, belowFailed: 2 }, 'Succeeded in 4 seconds, though 2 spans under it failed.'],
    [{ ...base, status: 'running', latencyMs: null, cost: 0.01 }, 'Still running. Cost $0.01.'],
  ]
  for (const [i, want] of cases) it(want, () => expect(spanSentence(i)).toBe(want))
})

describe('span sessions', () => {
  const now = Date.parse('2026-11-05T12:00:00Z')
  it('the list', () => {
    expect(spanSessionsSentence({ shown: 0, more: false, offset: 0 })).toBe('No spans carry a session id yet.')
    expect(spanSessionsSentence({ shown: 38, more: false, offset: 0, latest: '2026-11-05T11:55:00Z', now })).toBe('38 sessions have spans with a session id. The latest was updated 5 min ago.')
    expect(spanSessionsSentence({ shown: 1, more: false, offset: 0 })).toBe('1 session has spans with a session id.')
    expect(spanSessionsSentence({ shown: 50, more: true, offset: 50 })).toBe('More than 100 sessions have spans with a session id.')
  })
  const cases: [Parameters<typeof spanSessionSentence>[0], string][] = [
    [{ turns: 4, spans: 41, startedAt: '2026-11-05T11:00:00Z', lastAt: '2026-11-05T11:12:00Z', cost: 0.02, failedTurns: 1 }, '4 turns and 41 spans over 12 minutes, costing $0.02. One turn failed.'],
    [{ turns: 1, spans: 3, failedTurns: 0 }, '1 turn and 3 spans. It did not fail.'],
    [{ turns: 2, spans: 3, failedTurns: 0 }, '2 turns and 3 spans. None failed.'],
    [{ turns: 2, spans: 3, failedTurns: 2 }, '2 turns and 3 spans. All 2 failed.'],
  ]
  for (const [i, want] of cases) it(want, () => expect(spanSessionSentence(i)).toBe(want))
})

describe('compareSentence', () => {
  const cases: [Parameters<typeof compareSentence>[0], string][] = [
    [{ spans: 2, fields: 14, differ: 5, missing: 0, baseline: 'answer', diffMode: false }, '5 of 14 fields differ between these 2 spans.'],
    [{ spans: 3, fields: 9, differ: 0, missing: 1, baseline: 'answer', diffMode: true }, 'All 9 fields are the same in these 3 spans. Changes are shown against answer. One span was not found.'],
    [{ spans: 1, fields: 7, differ: 0, missing: 1, diffMode: false }, 'Only one of these spans was found; pick another on Traces to compare.'],
  ]
  for (const [i, want] of cases) it(want, () => expect(compareSentence(i)).toBe(want))
})
