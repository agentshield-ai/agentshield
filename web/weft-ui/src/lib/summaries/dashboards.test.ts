import { describe, expect, it } from 'vitest'
import { dashboardLine, dashboardSentence, dashboardsSentence, everyWords, monitorLine, monitorSentence, monitorsSentence, monitorTarget, rangeWords } from './dashboards'

const label = (id: string) => ({ '7d': 'Last 7 days', '24h': 'Last 24 hours' })[id] ?? ''

describe('dashboards', () => {
  it('range and refresh words', () => {
    expect(rangeWords('7d', label)).toBe('the last 7 days')
    expect(rangeWords('all', label)).toBe('all time')
    expect(rangeWords(undefined, label)).toBe('all time')
    expect(rangeWords('custom:a/b', label)).toBe('a custom range')
    expect(everyWords(30)).toBe('every 30 seconds')
    expect(everyWords(60)).toBe('every minute')
    expect(everyWords(300)).toBe('every 5 minutes')
  })
  const list: [{ charts: number }[], string][] = [
    [[], 'No dashboards yet.'],
    [[{ charts: 4 }], 'One dashboard, with 4 charts.'],
    [[{ charts: 4 }, { charts: 0 }], '2 dashboards, with 4 charts between them.'],
  ]
  for (const [xs, want] of list) it(want, () => expect(dashboardsSentence(xs)).toBe(want))
  const lines: [Parameters<typeof dashboardLine>[0], string][] = [
    [{ titles: [], range: 'the last 24 hours' }, 'No charts yet. Over the last 24 hours.'],
    [{ titles: ['Spans', 'Errors'], range: 'all time' }, '2 charts: Spans and Errors. Over all time.'],
    [{ titles: ['A', 'B', 'C', 'D'], range: 'the last 7 days' }, '4 charts: A, B, C, and 1 more. Over the last 7 days.'],
  ]
  for (const [i, want] of lines) it(want, () => expect(dashboardLine(i)).toBe(want))
  const one: [Parameters<typeof dashboardSentence>[0], string][] = [
    [{ charts: 4, names: [], roots: true, range: 'the last 7 days', refreshSeconds: 0 }, '4 charts of every top-level span over the last 7 days.'],
    [{ charts: 1, names: ['answer'], roots: true, range: 'all time', refreshSeconds: 30 }, '1 chart of top-level spans named “answer” over all time, refreshed every 30 seconds.'],
    [{ charts: 2, names: ['a', 'b'], roots: false, search: 'oslo', range: 'the last 7 days', refreshSeconds: 60 }, '2 charts of spans named “a” or “b” matching “oslo” over the last 7 days, refreshed every minute.'],
    [{ charts: 0, names: [], roots: false, range: 'all time', refreshSeconds: 0 }, 'No charts yet. Charts here would cover every span over all time.'],
  ]
  for (const [i, want] of one) it(want, () => expect(dashboardSentence(i)).toBe(want))
})

describe('monitors', () => {
  const now = Date.parse('2026-11-05T12:00:00Z')
  const m = { scorers: ['GoalCompletionJudge'], scopes: ['session' as const], names: [], rate: 1, active: true }
  const targets: [Parameters<typeof monitorTarget>[0], string][] = [
    [m, 'goal completion on every finished session'],
    [{ ...m, scorers: ['relevance', 'tool_safety'], scopes: [], names: ['answer'], rate: 0.2 }, 'relevance and tool safety on 20% of “answer” spans'],
    [{ ...m, scopes: ['agent', 'handoff'] }, 'goal completion on every agent run and every hand-off'],
    [{ ...m, scorers: [], scopes: [], names: ['a', 'b', 'c'] }, 'nothing yet on every “a”, “b”, or “c” span'],
    [{ ...m, scopes: [] }, 'goal completion on every top-level span'],
  ]
  for (const [i, want] of targets) it(want, () => expect(monitorTarget(i)).toBe(want))
  const lines: [Parameters<typeof monitorLine>[0], Parameters<typeof monitorLine>[1], string][] = [
    [m, { lastAt: '2026-11-05T11:55:00Z', today: 12 }, 'Scores goal completion on every finished session; last scored 5 min ago, 12 scores in the last 24 hours.'],
    [m, { lastAt: '2026-11-05T11:55:00Z', today: 500, capped: true }, 'Scores goal completion on every finished session; last scored 5 min ago, at least 500 scores in the last 24 hours.'],
    [m, { lastAt: '2026-11-02T12:00:00Z', today: 0 }, 'Scores goal completion on every finished session; last scored 3 days ago.'],
    [m, { lastAt: null, today: 0 }, 'Scores goal completion on every finished session; nothing scored yet.'],
    [{ ...m, active: false }, { lastAt: null, today: 0 }, 'Paused; scored goal completion on every finished session; it never scored anything.'],
    [m, null, 'Scores goal completion on every finished session.'],
  ]
  for (const [i, a, want] of lines) it(want, () => expect(monitorLine(i, a, now)).toBe(want))
  const list: [Parameters<typeof monitorsSentence>[0], string][] = [
    [{ total: 0, active: 0, today: null }, 'No monitors yet.'],
    [{ total: 1, active: 1, today: 3 }, 'One monitor, active. It recorded 3 scores in the last 24 hours.'],
    [{ total: 3, active: 2, today: 0 }, '3 monitors, 2 active. They recorded no scores in the last 24 hours.'],
    [{ total: 2, active: 2, today: null }, '2 monitors, all active.'],
    [{ total: 2, active: 0, today: 1000, capped: true }, '2 monitors, all paused. They recorded at least 1000 scores in the last 24 hours.'],
  ]
  for (const [i, want] of list) it(want, () => expect(monitorsSentence(i)).toBe(want))
  it('one monitor', () => {
    expect(monitorSentence(m, { total: 34, lastAt: '2026-11-05T11:55:00Z', averages: [{ scorer: 'GoalCompletionJudge', text: 'averages 0.82' }] }, now)).toBe(
      'Scores goal completion on every finished session. 34 scores so far, the last 5 min ago; goal completion averages 0.82.',
    )
    expect(monitorSentence({ ...m, active: false }, { total: 0, averages: [] }, now)).toBe('Paused. It scores goal completion on every finished session. Nothing scored yet; activate it to start.')
    expect(monitorSentence(m, { total: 1000, capped: true, averages: [] }, now)).toBe('Scores goal completion on every finished session. At least 1000 scores so far.')
  })
})
