import { describe, expect, it } from 'vitest'
import {
  chipsOf,
  filterQueryString,
  isEmptyFilter,
  parseFilters,
  parseFilterText,
  parseScoreBelow,
  removeKey,
  scoreBelowMatches,
  sessionMatchesClientFilters,
  sessionsParams,
  stableNow,
  timeWindow,
  writeFilters,
} from './filters'

describe('filter URL sync', () => {
  it('round-trips every chip through the URL', () => {
    const p = new URLSearchParams('agent=coder&team=trip-planners&status=error&time=7d&has=block&score_below=GoalCompletionJudge:0.4&model=gpt-4o&tool=Bash&q=oslo&cost_min=0.05')
    const s = parseFilters(p)
    expect(s).toEqual({
      agent: 'coder',
      team: 'trip-planners',
      status: 'error',
      time: '7d',
      has: 'block',
      scoreBelow: { scorer: 'GoalCompletionJudge', value: 0.4 },
      model: 'gpt-4o',
      tool: 'Bash',
      q: 'oslo',
      costMin: 0.05,
    })
    expect(parseFilters(writeFilters(s))).toEqual(s)
  })

  it('keeps unrelated parameters and drops invalid values', () => {
    const base = new URLSearchParams('tab=graph&step=tool:abc&agent=old&status=bogus')
    expect(parseFilters(base)).toEqual({ agent: 'old' })
    const out = writeFilters({ agent: 'new', has: 'approval' }, base)
    expect(out.get('tab')).toBe('graph')
    expect(out.get('step')).toBe('tool:abc')
    expect(out.get('agent')).toBe('new')
    expect(out.get('has')).toBe('approval')
    expect(out.has('status')).toBe(false)
  })

  it('treats from/to as a custom range', () => {
    const s = parseFilters(new URLSearchParams('from=2026-11-01T00:00:00Z&to=2026-11-02T00:00:00Z'))
    expect(s.time).toBe('custom')
    expect(timeWindow(s, '24h')).toEqual({ from: '2026-11-01T00:00:00Z', to: '2026-11-02T00:00:00Z' })
    expect(filterQueryString(s)).toBe('from=2026-11-01T00%3A00%3A00Z&to=2026-11-02T00%3A00%3A00Z')
  })

  it('score below accepts a bare value or scorer:value', () => {
    expect(parseScoreBelow('0.5')).toEqual({ value: 0.5 })
    expect(parseScoreBelow('Goal:0.3')).toEqual({ scorer: 'Goal', value: 0.3 })
    expect(parseScoreBelow('x')).toBeUndefined()
  })
})

describe('chips', () => {
  it('labels each filter in plain words and hides the default time range', () => {
    const chips = chipsOf({ agent: 'coder', status: 'error', has: 'approval', time: '24h', scoreBelow: { value: 0.5 } }, { defaultTime: '24h' })
    expect(chips.map((c) => `${c.label}: ${c.value}`)).toEqual(['Agent: coder', 'Status: Failed', 'Security: Needs approval', 'Score below: 0.5'])
    expect(chipsOf({ time: '7d' }, { defaultTime: '24h' })[0]).toMatchObject({ key: 'time', value: 'Last 7 days' })
  })

  it('removes a chip and its parameters', () => {
    expect(removeKey({ time: 'custom', from: 'a', to: 'b', agent: 'x' }, 'time')).toEqual({ agent: 'x' })
    expect(removeKey({ scoreBelow: { value: 1 }, costMin: 2 }, 'score')).toEqual({ costMin: 2 })
    expect(isEmptyFilter({ time: '24h' }, '24h')).toBe(true)
    expect(isEmptyFilter({ tool: 'Bash' }, '24h')).toBe(false)
  })
})

describe('filter text syntax', () => {
  it('turns key:value tokens into chips and keeps the rest as text', () => {
    expect(parseFilterText('agent:coder has:blocked status:failed lint errors')).toEqual({ agent: 'coder', has: 'block', status: 'error', q: 'lint errors' })
    expect(parseFilterText('score<0.5 cost>0.1 time:7d tool:"web search"')).toEqual({ scoreBelow: { value: 0.5 }, costMin: 0.1, time: '7d', tool: 'web search' })
  })

  it('leaves unknown values as text and clears text when only tokens are given', () => {
    expect(parseFilterText('status:maybe')).toEqual({ q: 'status:maybe' })
    expect(parseFilterText('has:approval', { q: 'old' })).toEqual({ has: 'approval' })
  })
})

describe('per-page parameters', () => {
  it('maps the chips to /api/v1/sessions parameters', () => {
    const now = Date.parse('2026-11-03T10:00:30Z')
    const p = sessionsParams({ agent: 'a', team: 't', status: 'ok', has: 'approval', scoreBelow: { scorer: 'S', value: 0.2 }, model: 'm', tool: 'x', q: 'hi' }, '24h', stableNow(now))
    expect(p).toEqual({
      agent: 'a',
      team_id: 't',
      status: 'ok',
      has_detection: 'require_approval',
      max_score: 'S:0.2',
      model: 'm',
      tool: 'x',
      q: 'hi',
      from: '2026-11-02T10:00:00.000Z',
    })
    expect(sessionsParams({ time: 'all' }, '24h')).toEqual({})
  })

  it('keeps relative ranges stable within a minute (stable query keys)', () => {
    const a = timeWindow({}, '24h', stableNow(Date.parse('2026-11-03T10:00:01Z')))
    const b = timeWindow({}, '24h', stableNow(Date.parse('2026-11-03T10:00:59Z')))
    expect(a).toEqual(b)
  })

  it('applies score-below without a scorer and cost on the client', () => {
    const s = { scores: { A: { mean: 0.9, count: 1 }, B: { mean: 0.2, count: 2 } }, cost_usd: 0.5 }
    expect(scoreBelowMatches(s, { value: 0.5 })).toBe(true)
    expect(scoreBelowMatches(s, { scorer: 'A', value: 0.5 })).toBe(false)
    expect(sessionMatchesClientFilters(s, { costMin: 1 })).toBe(false)
    expect(sessionMatchesClientFilters({ scores: {}, cost_usd: null }, { scoreBelow: { value: 0.5 } })).toBe(false)
  })
})
