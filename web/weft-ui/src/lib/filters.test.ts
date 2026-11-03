import { describe, expect, it } from 'vitest'
import {
  chipsOf,
  FILTER_KEYS,
  FILTER_SUPPORT,
  isFlagged,
  sessionScope,
  sessionScopeNote,
  type FilterPage,
  filterQueryString,
  isEmptyFilter,
  parseFilters,
  parseFilterText,
  parseScoreBelow,
  removeKey,
  scoreBelowMatches,
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
    const p = sessionsParams(
      { agent: 'a', team: 't', status: 'ok', has: 'approval', scoreBelow: { scorer: 'S', value: 0.2 }, model: 'm', tool: 'x', q: 'hi' },
      '24h',
      stableNow(now),
    )
    expect(p).toEqual({
      agent: 'a',
      team_id: 't',
      status: 'ok',
      has_detection: 'require_approval',
      score_below: 'S:0.2',
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

  it('sends score-below and cost to the server (no browser-side filtering, no page cap)', () => {
    expect(sessionsParams({ scoreBelow: { value: 0.5 }, costMin: 1 }, 'all')).toEqual({ score_below: '0.5', cost_min: '1' })
    const s = { scores: { A: { mean: 0.9, count: 1 }, B: { mean: 0.2, count: 2 } }, cost_usd: 0.5 }
    expect(scoreBelowMatches(s, { value: 0.5 })).toBe(true)
    expect(scoreBelowMatches(s, { scorer: 'A', value: 0.5 })).toBe(false)
  })
})

describe('one chip set on every page', () => {
  const pages: FilterPage[] = ['sessions', 'agents', 'teams', 'security', 'traces']

  it('every page applies every chip, on its rows or through sessions', () => {
    for (const p of pages) for (const k of FILTER_KEYS) expect(['page', 'sessions'], `${p} ${k}`).toContain(FILTER_SUPPORT[p][k])
  })

  it('sends the chips a page cannot read off its rows through sessions', () => {
    const all = parseFilters(new URLSearchParams('agent=coder&team=code-crew&status=error&has=block&score_below=0.5&model=m&tool=Bash&cost_min=0.1&q=x&time=7d'))
    expect(sessionScope('sessions', all)).toEqual({})
    expect(sessionScope('agents', all)).toEqual({ costMin: 0.1 })
    expect(sessionScope('teams', all)).toEqual({ status: 'error', has: 'block', scoreBelow: { value: 0.5 }, model: 'm', tool: 'Bash', costMin: 0.1 })
    expect(sessionScope('security', all)).toEqual({ team: 'code-crew', status: 'error', scoreBelow: { value: 0.5 }, model: 'm', costMin: 0.1 })
    expect(sessionScope('traces', all)).toEqual({ team: 'code-crew', has: 'block', scoreBelow: { value: 0.5 }, costMin: 0.1 })
    // Session-only values go through sessions even where the chip is otherwise the page's own.
    expect(sessionScope('traces', { status: 'failed_step' })).toEqual({ status: 'failed_step' })
    expect(sessionScope('security', { has: 'flagged' })).toEqual({ has: 'flagged' })
    expect(sessionScope('agents', { status: 'running' })).toEqual({ status: 'running' })
    expect(sessionScope('agents', { has: 'flagged' })).toEqual({})
  })

  it('says how session chips apply, so none looks ignored', () => {
    expect(sessionScopeNote('teams', { tool: 'Bash' })).toBe('Tool: showing teams with a session that matches this filter.')
    expect(sessionScopeNote('traces', { team: 't', costMin: 1 })).toBe('Team, Cost at least: showing traces that belong to a session that matches these filters.')
    expect(sessionScopeNote('sessions', { team: 't' })).toBeNull()
  })

  it('knows blocked-or-approval-heavy and went-on-after-a-failed-step', () => {
    expect(parseFilterText('has:flagged status:failed_step')).toEqual({ has: 'flagged', status: 'failed_step' })
    expect(sessionsParams({ has: 'flagged', status: 'failed_step' }, 'all')).toEqual({ has_detection: 'flagged', status: 'failed_step' })
    expect(isFlagged({ require_approval: 3 })).toBe(true)
    expect(isFlagged({ require_approval: 2, allow: 9 })).toBe(false)
    expect(chipsOf({ costMin: 0.0048 })[0].value).toBe('$0.0048')
  })
})
