// Table-driven tests of the generated summaries (DESIGN.md §7.2 rule 8).
// Fixtures: fixtures/e2e-sessions.json, the API's answers for the e2e seed
// (e2e/seed.ts) — the same sessions the browser tests and the mockups show.
import { describe, expect, it } from 'vitest'
import type { Detection, SessionAttention, SessionSummary, TimelineItem } from '../api/v1'
import fixture from './fixtures/e2e-sessions.json'
import {
  countWord,
  durationWords,
  joinClauses,
  medianCost,
  needsALook,
  offsetText,
  relativeTime,
  rowGlyph,
  scoreText,
  sessionMetaFigures,
  sessionMetaLine,
  sessionOutcome,
  sessionsSentence,
  sessionSummaryLine,
  shortDuration,
  type RowContext,
  type FilterClause,
  type SummaryPart,
} from './summaries'
import { ruleWords, sentenceCase } from './ruleWords'
import { buildTimeline } from './timeline'

const F = fixture as unknown as {
  sessions: Record<string, SessionSummary>
  timelines: Record<string, TimelineItem[]>
  detections: Record<string, Detection[]>
  list: { items: SessionSummary[]; total_count: number }
  attention: SessionAttention
}
const text = (parts: SummaryPart[]) => parts.map((p) => (typeof p === 'string' ? p : p.text)).join('')

function session(over: Partial<SessionSummary> = {}): SessionSummary {
  return {
    session_id: 's1',
    name: '',
    title: 'Do the thing',
    started_at: '2026-11-05T10:00:00Z',
    last_activity: '2026-11-05T10:00:04Z',
    duration_ms: 4000,
    agents: ['orchestrator', 'researcher'],
    team_id: 'trip-planners',
    team_name: 'Trip planners',
    turn_count: 1,
    trace_count: 1,
    span_count: 5,
    llm_call_count: 3,
    tool_call_count: 2,
    handoff_count: 1,
    reasoning_step_count: 2,
    detection_count: {},
    error_count: 0,
    input_tokens: 100,
    output_tokens: 50,
    reasoning_tokens: 0,
    cost_usd: 0.0062,
    status: 'ok',
    failed_step: false,
    models: [],
    tools: [],
    scores: {},
    ...over,
  }
}

function det(over: Partial<Detection> = {}): Detection {
  return {
    id: 'd1',
    event_id: 'e1',
    session_id: 's1',
    span_id: null,
    trace_id: null,
    link: 'tool_call_id',
    engine_span_id: null,
    tool_call_id: null,
    tool_name: 'Read',
    agent_name: 'coder',
    action: 'require_approval',
    mode: 'enforce',
    severity: 'medium',
    rules: [{ id: 'r1', title: 'Credential File Access Attempt', level: 'medium' }],
    source: 'engine',
    ruleset: null,
    event_source: null,
    overridden: false,
    overridden_at: null,
    replay_job_id: null,
    created_at: '2026-11-05T10:00:01Z',
    ...over,
  }
}

describe('words', () => {
  it.each([
    [null, ''],
    [50, '50 milliseconds'],
    [1000, '1 second'],
    [1300, '1.3 seconds'],
    [4000, '4 seconds'],
    [9000, '9 seconds'],
    [30_000, '30 seconds'],
    [60_000, '1 minute'],
    [125_000, '2 minutes 5 seconds'],
    [3_600_000, '1 hour'],
    [3_900_000, '1 hour 5 minutes'],
  ])('durationWords(%s) = %s', (ms, want) => expect(durationWords(ms)).toBe(want))

  it.each([
    [150, '150 ms'],
    [2500, '2.5 s'],
    [9000, '9 s'],
    [42_000, '42 s'],
    [65_000, '1 min 5 s'],
    [7_380_000, '2 h 3 min'],
  ])('shortDuration(%s) = %s', (ms, want) => expect(shortDuration(ms)).toBe(want))

  it.each([
    [50, '0.05 s'],
    [1000, '1 s'],
    [1300, '1.3 s'],
    [5300, '5.3 s'],
    [9000, '9 s'],
  ])('offsetText(%s) = %s', (ms, want) => expect(offsetText(ms)).toBe(want))

  it.each([
    [10_000, 'just now'],
    [50 * 60_000, '50 min ago'],
    [2 * 3600_000, '2 h ago'],
    [3 * 86400_000, '3 days ago'],
  ])('relativeTime(-%s ms) = %s', (ago, want) => expect(relativeTime(new Date(1_000_000_000_000 - ago).toISOString(), 1_000_000_000_000)).toBe(want))

  it('joins clauses with a serial comma and counts in words', () => {
    expect(joinClauses(['a'])).toBe('a')
    expect(joinClauses(['a', 'b'])).toBe('a and b')
    expect(joinClauses(['a', 'b', 'c'])).toBe('a, b, and c')
    expect(countWord(1, 'step')).toBe('one step')
    expect(countWord(2, 'step')).toBe('2 steps')
    expect(scoreText('GoalCompletionJudge', { mean: 0.6, count: 1 })).toBe('goal completion 0.60')
  })

  it('names rules in plain words, and falls back to the title', () => {
    expect(ruleWords({ id: 'x', title: 'Remote Code Execution via Piped Script Download' }).noun).toBe('a script piped from the internet')
    expect(ruleWords({ id: 'x', title: 'Credential File Access Attempt' }).purpose).toBe('to read credential files')
    expect(ruleWords({ id: 'x', title: 'Reverse Shell Attempt' }).reason).toBe('This would let someone else control this machine remotely.')
    const unknown = ruleWords({ id: 'x', title: 'Unusual Tool Call Velocity in Session' }, 'Bash')
    expect(unknown).toEqual({ noun: 'a Bash call', purpose: 'to call Bash', reason: 'It matched the AgentShield rule “Unusual Tool Call Velocity in Session”.', known: false })
  })

  it.each([
    ['Remote Code Execution via Piped Script Download', 'Remote code execution via piped script download'],
    ['AWS Credential File Access', 'AWS credential file access'],
    ['MCP Tool Poisoning via GitHub Issue', 'MCP tool poisoning via GitHub issue'],
    ['already sentence case', 'already sentence case'],
    ['Read A File', 'Read a file'],
    ['', ''],
  ])('writes the rule title %j in sentence case', (title, want) => {
    expect(sentenceCase(title)).toBe(want)
  })
})

describe('session rows', () => {
  const cases: [string, SessionSummary, RowContext, string, string, boolean][] = [
    ['finished, with a score', session({ scores: { GoalCompletionJudge: { mean: 0.9, count: 1 } } }), {}, 'Finished in 4 seconds. Goal completion scored 0.90.', 'ok', false],
    ['finished, nothing else', session(), {}, 'Finished in 4 seconds.', 'ok', false],
    ['failed, low score', session({ status: 'error', scores: { GoalCompletionJudge: { mean: 0.1, count: 1 } } }), {}, 'Failed after 4 seconds. Goal completion scored 0.10.', 'failed', true],
    ['failed', session({ status: 'error' }), {}, 'Failed after 4 seconds.', 'failed', true],
    [
      'blocked, rule known',
      session({ detection_count: { block: 1 }, error_count: 1, failed_step: true, duration_ms: 9000 }),
      { detections: [det({ action: 'block', tool_name: 'Bash', rules: [{ id: 'r', title: 'Remote Code Execution via Piped Script Download', level: 'critical' }] })] },
      'AgentShield blocked a script piped from the internet. The session went on and finished.',
      'blocked',
      true,
    ],
    ['blocked, detections not loaded yet', session({ detection_count: { block: 1 }, error_count: 1, failed_step: true }), {}, 'AgentShield blocked a tool call. The session went on and finished.', 'blocked', true],
    [
      'blocked, and another step failed',
      session({ detection_count: { block: 1 }, error_count: 2, failed_step: true }),
      {},
      'AgentShield blocked a tool call. The session went on and finished, but one other step failed.',
      'blocked',
      true,
    ],
    ['blocked twice, then failed', session({ status: 'error', detection_count: { block: 2 } }), {}, 'AgentShield blocked 2 tool calls, and the session failed after 4 seconds.', 'failed', true],
    [
      'approval-heavy with overrides',
      session({ detection_count: { require_approval: 5 }, duration_ms: 30_000 }),
      { detections: [0, 1, 2, 3, 4].map((i) => det({ id: `d${i}`, overridden: i < 3 })) },
      'Asked 5 times to read credential files. You approved 3 of them.',
      'approval',
      true,
    ],
    ['approval-heavy, detections not loaded', session({ detection_count: { require_approval: 5 } }), {}, 'Asked for approval 5 times.', 'approval', true],
    ['one approval, approved', session({ detection_count: { require_approval: 1 } }), { detections: [det({ overridden: true })] }, 'Asked once for approval to read credential files. You approved it.', 'ok', false],
    ['went on after a failed step', session({ failed_step: true, error_count: 1 }), {}, 'Finished, but one step failed on the way.', 'warning', true],
    ['went on after 2 failed steps', session({ failed_step: true, error_count: 2 }), {}, 'Finished, but 2 steps failed on the way.', 'warning', true],
    ['cost spike, median known', session({ cost_usd: 0.25 }), { costThreshold: 0.019, medianCost: 0.0062 }, 'Finished. Cost $0.25, about 40 times the median session.', 'cost', true],
    ['cost spike, median unknown', session({ cost_usd: 0.25 }), { costThreshold: 0.019, costFactor: 3 }, 'Finished. Cost $0.25, at least 3 times the median session.', 'cost', true],
    ['below the cost threshold', session({ cost_usd: 0.018 }), { costThreshold: 0.019 }, 'Finished in 4 seconds.', 'ok', false],
    ['running', session({ status: 'running', duration_ms: 125_000 }), {}, 'Still running, 2 minutes 5 seconds so far.', 'running', false],
    [
      'verdicts only',
      session({ status: 'unknown', verdicts_only: true, duration_ms: null, detection_count: { block: 1 }, agents: [] }),
      {},
      'Only AgentShield verdicts were recorded, so the outcome is unknown. AgentShield blocked one tool call.',
      'blocked',
      true,
    ],
    ['several results averaged', session({ scores: { ToolSafetyScorer: { mean: 0.75, count: 3 } } }), {}, 'Finished in 4 seconds. Tool safety averaged 0.75 over 3 results.', 'ok', false],
  ]
  it.each(cases)('%s', (_name, s, ctx, line, glyph, look) => {
    expect(sessionSummaryLine(s, ctx)).toBe(line)
    expect(rowGlyph(s, ctx).kind).toBe(glyph)
    expect(needsALook(s, ctx)).toBe(look)
  })

  it('writes the meta line: team, then agents; no made-up teams', () => {
    expect(sessionMetaLine(session())).toBe('Trip planners · orchestrator, researcher')
    expect(sessionMetaLine(session({ team_id: 'auto:abc', team_name: 'orchestrator + researcher' }))).toBe('orchestrator, researcher')
    expect(sessionMetaLine(session({ team_id: null, team_name: null, agents: [], verdicts_only: true }))).toBe('no agent spans recorded')
  })

  it('takes the median of known costs, as the server does', () => {
    expect(medianCost([{ cost_usd: 1 }, { cost_usd: 2 }, { cost_usd: null }])).toBeNull()
    expect(medianCost([1, 2, 3, 4].map((c) => ({ cost_usd: c })))).toBe(2.5)
    expect(medianCost([5, 1, 0, 3, 2, 4].map((c) => ({ cost_usd: c })))).toBe(3)
  })

  it('summarises the e2e seed the way the mockup does', () => {
    const rows = F.list.items
    const ctx: RowContext = { costThreshold: F.attention.cost_spike_threshold, medianCost: medianCost(rows) }
    const byId = Object.fromEntries(rows.map((s) => [s.session_id, s]))
    expect(sessionSummaryLine(byId['code-lint-1'], { ...ctx, detections: F.detections['code-lint-1'] })).toBe(
      'AgentShield blocked a script piped from the internet. The session went on and finished.',
    )
    expect(sessionSummaryLine(byId['code-deploy-1'], { ...ctx, detections: F.detections['code-deploy-1'] })).toBe(
      'Asked 5 times to read credential files. You approved 3 of them.',
    )
    expect(sessionSummaryLine(byId['trip-lisbon'], ctx)).toBe('Failed after 4 seconds. Goal completion scored 0.10.')
    expect(sessionSummaryLine(byId['code-test-1'], ctx)).toBe('Finished, but one step failed on the way.')
    expect(sessionSummaryLine(byId['trip-oslo-2'], ctx)).toBe('Finished. Cost $0.25, about 40 times the median session.')
    expect(sessionSummaryLine(byId['trip-oslo'], ctx)).toBe('Finished in 4 seconds. Goal completion scored 0.90.')
    expect(rows.filter((s) => needsALook(s, ctx)).map((s) => s.session_id)).toEqual([
      'code-lint-1',
      'trip-lisbon',
      'code-deploy-1',
      'trip-kyoto',
      'code-test-1',
      'trip-oslo-2',
      'trip-bergen',
    ])
  })
})

describe('the Sessions sentence', () => {
  it('names the serious ones as linked clauses, then how many more need a look', () => {
    const rows = F.list.items
    const parts = sessionsSentence({ attention: F.attention, time: '24h', filtered: false, rows, total: F.list.total_count })
    expect(text(parts)).toBe(
      '10 sessions ran in the last 24 hours. 3 failed, 1 was blocked by AgentShield and 1 kept asking for approval. 2 more finished with a failed step or a cost spike.',
    )
    const links = parts.filter((p): p is Exclude<SummaryPart, string> => typeof p !== 'string')
    expect(links.map((l) => [l.clause, l.count, 'session' in l ? (l.session?.id ?? null) : null, 'exact' in l ? l.exact : null])).toEqual([
      ['failed', 3, null, true],
      ['blocked', 1, 'code-lint-1', true],
      ['approval', 1, 'code-deploy-1', false],
      ['more', 2, null, null],
    ])
    const [failed, blocked] = links as FilterClause[]
    expect(failed.filter).toEqual({ status: 'error' })
    expect(blocked.filter).toEqual({ has: 'block' })
    expect(blocked.session).toEqual({ id: 'code-lint-1', step: 'problem' })
    // The "more" clause counts exactly the rows of "Needs a look" that the first sentence does not name.
    const ctx: RowContext = { costThreshold: F.attention.cost_spike_threshold, lowScore: 0.5, approvalHeavy: 3 }
    const named = new Set(['code-lint-1', 'code-deploy-1', ...rows.filter((s) => String(s.status) === 'error').map((s) => s.session_id)])
    expect(rows.filter((s) => needsALook(s, ctx) && !named.has(s.session_id)).map((s) => s.session_id)).toEqual(['code-test-1', 'trip-oslo-2'])
    expect(links[3]).toMatchObject({ atLeast: false })
  })

  const att = (over: Partial<SessionAttention> = {}, counts: Partial<Record<string, number>> = {}): SessionAttention => ({
    items: (['failed', 'failed_step', 'security', 'low_score', 'cost'] as const).map((id) => ({
      id,
      count: counts[id] ?? 0,
      filter: (id === 'failed' ? { status: 'error' } : id === 'failed_step' ? { status: 'failed_step' } : id === 'security' ? { has_detection: 'flagged' } : id === 'low_score' ? { score_below: '0.5' } : {}) as Record<string, string>,
      latest: null,
    })),
    sessions: 0,
    blocked: 0,
    approval_heavy: 0,
    lowest_score: null,
    cost_spike_threshold: null,
    approval_heavy_threshold: 3,
    low_score_threshold: 0.5,
    cost_spike_factor: 3,
    ...over,
  })
  const lowScored = (over: Partial<SessionSummary> = {}) => session({ scores: { G: { mean: 0.2, count: 1 } }, ...over })

  it.each([
    ['nothing', att(), '24h', false, [], undefined, 'No sessions ran in the last 24 hours.'],
    ['nothing matches', att(), '7d', true, [], undefined, 'No sessions match these filters in the last 7 days.'],
    ['one that finished', att({ sessions: 1 }), '24h', false, [session()], undefined, '1 session ran in the last 24 hours. It finished.'],
    ['one still running', att({ sessions: 1 }), '24h', false, [session({ status: 'running' })], undefined, '1 session ran in the last 24 hours. It is still running.'],
    ['one that was blocked', att({ sessions: 1, blocked: 1 }, { security: 1 }), '24h', false, [session({ detection_count: { block: 1 } })], undefined, '1 session ran in the last 24 hours. It was blocked by AgentShield.'],
    [
      'one that failed and scored low: the serious one is named, the row says the rest',
      att({ sessions: 1 }, { failed: 1, low_score: 1 }),
      'all',
      false,
      [lowScored({ status: 'error' })],
      undefined,
      '1 session ran. It failed.',
    ],
    ['one that scored low', att({ sessions: 1 }, { low_score: 1 }), '24h', false, [lowScored()], undefined, '1 session ran in the last 24 hours. It finished with a low score.'],
    ['all finished', att({ sessions: 3 }), '30d', false, [session(), session(), session()], undefined, '3 sessions ran in the last 30 days. All of them finished.'],
    [
      'some still running: no "the rest finished"',
      att({ sessions: 3 }, { failed: 1 }),
      '24h',
      true,
      [session({ status: 'error' }), session({ status: 'running' }), session()],
      undefined,
      '3 sessions match these filters in the last 24 hours. 1 failed. 1 is still running.',
    ],
    [
      'nothing serious, some worth a look',
      att({ sessions: 4 }, { low_score: 1, failed_step: 1 }),
      '24h',
      false,
      [lowScored(), session({ failed_step: true, error_count: 1 }), session(), session()],
      undefined,
      '4 sessions ran in the last 24 hours. 2 finished with a failed step or a low score.',
    ],
    [
      'one more, still running, needs a look',
      att({ sessions: 3 }, { failed: 1, low_score: 1 }),
      '24h',
      false,
      [session({ status: 'error' }), lowScored({ status: 'running' }), session({ status: 'running' })],
      undefined,
      '3 sessions ran in the last 24 hours. 1 failed. 1 more needs a look and 1 is still running.',
    ],
    [
      'not every row loaded: a lower bound, and no claim about the rest',
      att({ sessions: 200 }, { failed: 2, low_score: 9 }),
      '24h',
      false,
      [session({ status: 'error' }), lowScored(), lowScored(), session({ status: 'running' })],
      200,
      '200 sessions ran in the last 24 hours. 2 failed. At least 2 more finished with a low score.',
    ],
    ['not every row loaded, nothing else loaded needs a look', att({ sessions: 200 }, { failed: 2 }), '24h', false, [session({ status: 'error' })], 200, '200 sessions ran in the last 24 hours. 2 failed.'],
    [
      'two kinds of serious clause',
      att({ sessions: 5, blocked: 2, approval_heavy: 0 }, { failed: 1, security: 2 }),
      '1h',
      false,
      [],
      5,
      '5 sessions ran in the last hour. 1 failed and 2 were blocked by AgentShield.',
    ],
  ] as [string, SessionAttention, '24h' | '7d' | '30d' | 'all' | '1h', boolean, SessionSummary[], number | undefined, string][])('%s', (_n, a, time, filtered, rows, total, want) => {
    expect(text(sessionsSentence({ attention: a, time, filtered, rows, total: total ?? (rows.length || a.sessions) }))).toBe(want)
  })

  it('is at most three short sentences', () => {
    const parts = sessionsSentence({ attention: F.attention, time: '24h', filtered: false, rows: F.list.items, total: F.list.total_count })
    expect(text(parts).match(/[.!?](\s|$)/g)?.length).toBeLessThanOrEqual(3)
  })

  it('names every kind the other ones have, most serious first', () => {
    const rows = [session({ cost_usd: 9 }), session({ scores: { G: { mean: 0.2, count: 1 } } }), session({ failed_step: true, error_count: 1 }), session()]
    const a = att({ sessions: 4, cost_spike_threshold: 5 })
    expect(text(sessionsSentence({ attention: a, time: '24h', filtered: false, rows, total: 4 }))).toBe(
      '4 sessions ran in the last 24 hours. 3 finished with a failed step, a low score or a cost spike.',
    )
  })

  it('links approval-heavy sessions exactly when nothing was blocked', () => {
    const parts = sessionsSentence({ attention: att({ sessions: 4, approval_heavy: 2 }, { security: 2 }), time: '24h', filtered: false, rows: [], total: 4 })
    const c = parts.find((p) => typeof p !== 'string' && p.clause === 'approval') as FilterClause
    expect(c.exact).toBe(true)
    expect(c.filter).toEqual({ has: 'flagged' })
  })
})

describe('session outcome', () => {
  const outcome = (id: string) => {
    const dets = F.detections[id].filter((d) => d.source === 'engine')
    const tl = buildTimeline(F.timelines[id], dets)
    return sessionOutcome({ summary: F.sessions[id], tl, detections: dets })
  }
  it.each([
    ['code-lint-1', 'Finished, after AgentShield blocked one step. After asking the reviewer, the coder ran “npm i -D eslint”, which worked.'],
    ['trip-lisbon', "Failed after 4 seconds. The researcher's web_search call failed: upstream timeout after 400 ms (HTTP 504)."],
    ['code-test-1', "Finished, but the coder's Bash call failed on the way (exit status 1: 3 tests failed (DATABASE_URL is not set)). The coder then ran “DATABASE_URL=sqlite::memory: npm test”, which worked."],
    ['code-deploy-1', 'Finished, after asking for approval 5 times. You approved 3 of them.'],
    ['trip-oslo', 'Finished in 4 seconds.'],
  ])('%s', (id, want) => expect(outcome(id)).toBe(want))

  it('writes the meta line of figures', () => {
    const s = F.sessions['code-lint-1']
    const now = new Date(s.started_at).getTime() + 50 * 60_000
    expect(sessionMetaFigures(s, now)).toBe('Code crew · coder and reviewer · started 50 min ago · took 9 s · cost $0.0072 · goal completion 0.60')
  })
})
