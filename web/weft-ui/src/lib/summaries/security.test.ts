import { describe, expect, it } from 'vitest'
import type { Detection, ReplayJob, ShieldInsights } from '../../api/v1'
import {
  callWords,
  crossAgentSentences,
  detectionGlyph,
  detectionLine,
  detectionMeta,
  fatigueSentence,
  groupCounts,
  linkedSentence,
  overrideSentence,
  replayLine,
  replayMeta,
  ruleGroups,
  securitySentence,
  severityWords,
  type SecPart,
} from './security'

const text = (ps: SecPart[] | null) => (ps ?? []).map((p) => (typeof p === 'string' ? p : p.text)).join('')

const det = (over: Partial<Detection>): Detection =>
  ({
    id: Math.random().toString(36),
    event_id: 'e',
    session_id: 's1',
    session_title: 'Install the linter',
    span_id: 'sp',
    trace_id: null,
    link: 'tool_call_id',
    engine_span_id: null,
    tool_call_id: null,
    tool_name: 'Bash',
    agent_name: 'coder',
    action: 'block',
    mode: 'enforce',
    severity: 'critical',
    rules: [{ id: 'r1', title: 'Remote Code Execution via Piped Script Download', level: 'critical' }],
    source: 'engine',
    ruleset: null,
    event_source: null,
    overridden: false,
    overridden_at: null,
    replay_job_id: null,
    created_at: '2026-11-03T10:00:00Z',
    ...over,
  }) as Detection

describe('securitySentence', () => {
  const cases: [string, Parameters<typeof securitySentence>[0], string][] = [
    [
      'blocks, approvals and overrides',
      { stats: { total: 6, by_action: { block: 1, require_approval: 5 }, overridden: 3 }, time: '24h', filtered: false },
      'AgentShield blocked 1 tool call and asked for approval 5 times in the last 24 hours. Users overrode 3 of them.',
    ],
    ['only blocks', { stats: { total: 2, by_action: { block: 2 }, overridden: 0 }, time: '7d', filtered: false }, 'AgentShield blocked 2 tool calls and asked for no approval in the last 7 days.'],
    [
      'only approvals, one overridden',
      { stats: { total: 1, by_action: { require_approval: 1 }, overridden: 1 }, time: '24h', filtered: false },
      'AgentShield blocked nothing and asked for approval once in the last 24 hours. A user overrode it.',
    ],
    [
      'nothing stopped, some logged',
      { stats: { total: 3, by_action: { log: 3 }, overridden: 0 }, time: 'all', filtered: false },
      'AgentShield blocked nothing and asked for no approval. It logged 3 tool calls without stopping them.',
    ],
    [
      'filtered, logged as well',
      { stats: { total: 4, by_action: { block: 1, require_approval: 2, log: 1 }, overridden: 3 }, time: '30d', filtered: true },
      'AgentShield blocked 1 tool call and asked for approval twice in the last 30 days, among the tool calls these filters keep. It logged 1 more without stopping it. Users overrode all 3.',
    ],
    ['nothing at all', { stats: { total: 0, by_action: {}, overridden: 0 }, time: '24h', filtered: false }, 'AgentShield blocked nothing and asked for no approval in the last 24 hours.'],
  ]
  for (const [name, input, want] of cases)
    it(name, () => {
      expect(text(securitySentence(input))).toBe(want)
    })

  it('links the blocked and approval clauses to those detections', () => {
    const ps = securitySentence({ stats: { total: 6, by_action: { block: 1, require_approval: 5 }, overridden: 0 }, time: '24h', filtered: false })
    const links = ps.filter((p) => typeof p !== 'string') as Exclude<SecPart, string>[]
    expect(links.map((l) => l.filter)).toEqual([{ has: 'block' }, { has: 'approval' }])
  })
})

describe('rule groups', () => {
  it('groups by the first rule, blocks first, with plain words and severity', () => {
    const gs = ruleGroups([
      det({ action: 'require_approval', rules: [{ id: 'r2', title: 'Credential File Access Attempt', level: 'medium' }], created_at: '2026-11-03T09:00:00Z' }),
      det({ action: 'require_approval', rules: [{ id: 'r2', title: 'Credential File Access Attempt', level: 'medium' }], created_at: '2026-11-03T11:00:00Z' }),
      det({}),
      det({ action: 'log', rules: [{ id: 'r3', title: 'Some Custom Rule', level: 'low' }] }),
    ])
    expect(gs.map((g) => [g.id, g.heading, g.count])).toEqual([
      ['r1', 'A script piped from the internet', 1],
      ['r2', 'A read of a credential file', 2],
      ['r3', 'Some Custom Rule', 1],
    ])
    expect(gs[0].reason).toBe('This downloads a script and runs it straight away, so whoever controls that address could run anything on this machine. Critical severity.')
    expect(gs[0].ruleTitle).toBe('Remote Code Execution via Piped Script Download')
    expect(gs[2].reason).toBe('Low severity.')
    expect(gs[2].ruleTitle).toBeNull()
    // Newest first inside a group.
    expect(gs[1].detections.map((d) => d.created_at)).toEqual(['2026-11-03T11:00:00Z', '2026-11-03T09:00:00Z'])
    expect(groupCounts(gs[1])).toBe('2 needed approval')
  })

  it('names a verdict without a rule', () => {
    expect(ruleGroups([det({ rules: [], severity: '' })])[0]).toMatchObject({ id: '', heading: 'No rule recorded', reason: 'No severity recorded.' })
  })

  const sev: [string, string][] = [
    ['critical', 'Critical'],
    ['HIGH', 'High'],
    ['informational', 'Informational'],
    ['', 'No severity'],
    ['none', 'No severity'],
  ]
  for (const [l, w] of sev) it(`severity ${l || '(empty)'}`, () => expect(severityWords(l)).toBe(w))
})

describe('detection rows', () => {
  const lines: [Partial<Detection>, string, string][] = [
    [{}, 'Blocked coder\'s Bash call.', 'blocked'],
    [{ action: 'block', overridden: true }, 'Blocked coder\'s Bash call. A user overrode it.', 'blocked'],
    [{ action: 'require_approval', tool_name: 'Read' }, 'Asked for approval before coder\'s Read call.', 'approval'],
    [{ action: 'require_approval', overridden: true, agent_name: null }, 'Asked for approval before a Bash call. A user approved it.', 'approval'],
    [{ action: 'log', tool_name: null }, 'Logged coder\'s tool call without stopping it.', 'warning'],
  ]
  for (const [over, line, glyph] of lines)
    it(line, () => {
      expect(detectionLine(det(over))).toBe(line)
      expect(detectionGlyph(det(over))).toBe(glyph)
    })
  it('says what else matched and whether the step is linked', () => {
    expect(detectionMeta(det({}))).toBe('')
    expect(
      detectionMeta(
        det({
          span_id: null,
          rules: [
            { id: 'a', title: 'A', level: 'high' },
            { id: 'b', title: 'B', level: 'low' },
            { id: 'c', title: '', level: 'low' },
          ],
        }),
      ),
    ).toBe('Also matched “B” and “c”. Not linked to a recorded tool call; opens the session.')
    expect(callWords({ agent_name: null, tool_name: null })).toBe('a tool call')
  })
  it('linked share', () => {
    expect(linkedSentence({ total: 4, linked: 3 })).toBe('75% of these verdicts are linked to the exact tool call; the rest stay on their session.')
    expect(linkedSentence({ total: 0, linked: 0 })).toBe('')
  })
})

describe('patterns', () => {
  const f = (over: Partial<ShieldInsights['approval_fatigue']['by_session'][number]>) => ({
    session_id: 'd1',
    session_title: 'Find out why deploys fail',
    agents: ['coder'],
    require_approval: 5,
    approved: 3,
    first_at: '',
    last_at: '',
    per_hour: 1,
    fatigued: true,
    ...over,
  })
  it('approval fatigue', () => {
    expect(fatigueSentence([], 5)).toBeNull()
    expect(fatigueSentence([f({ require_approval: 2, fatigued: false })], 5)).toBeNull()
    expect(text(fatigueSentence([f({})], 5))).toBe(
      'One session asked for approval 5 times, and 3 were approved: Find out why deploys fail. At 5 or more requests in a session, people tend to approve without reading.',
    )
    expect(text(fatigueSentence([f({ fatigued: false, require_approval: 3, approved: 0 }), f({ session_id: 'd2', session_title: 'Other', require_approval: 7 })], 5))).toBe(
      '2 sessions asked for approval again and again: Other (7 times) and Find out why deploys fail (3 times). At 5 or more requests in a session, people tend to approve without reading.',
    )
    const link = fatigueSentence([f({})], 5)!.find((p) => typeof p !== 'string') as Exclude<SecPart, string>
    expect(link.session).toEqual({ id: 'd1', step: 'problem' })
  })
  const o = (over: Partial<ShieldInsights['override_escalation']['by_session'][number]>) => ({
    session_id: 'd1',
    session_title: 'Deploys',
    agents: [],
    overrides: 3,
    overridden_blocks: 0,
    overridden_approvals: 3,
    rule_ids: [],
    first_at: '',
    last_at: '',
    escalating: true,
    ...over,
  })
  it('override escalation', () => {
    expect(overrideSentence([])).toBeNull()
    expect(text(overrideSentence([o({})]))).toBe('A user overrode AgentShield 3 times (3 approval requests) in Deploys. Overrides that keep coming like this are a sign of escalation.')
    expect(text(overrideSentence([o({ escalating: false, overrides: 1, overridden_blocks: 1, overridden_approvals: 0 })]))).toBe('A user overrode AgentShield once (1 block) in Deploys.')
    expect(text(overrideSentence([o({}), o({ session_id: 'x', session_title: '', overrides: 2, escalating: true })]))).toBe(
      'Users overrode AgentShield in 2 sessions: Deploys (3 times) and session x (twice). In both they keep coming, a sign of escalation.',
    )
  })
  it('correlated agents', () => {
    expect(
      crossAgentSentences([
        { team_id: 't', agents: [], sessions: 1, detections: 2, agents_with_detections: ['coder', 'reviewer'], shared_rules: [{ rule_id: 'r', title: 'Exfil', level: 'high', agents: [], count: 2 }], incidents: [{ start: '', end: '', agents: [], rule_ids: [], session_ids: [], detections: 2, max_action: 'block' }], correlated: true },
        { team_id: 'u', agents: [], sessions: 1, detections: 1, agents_with_detections: [], shared_rules: [], incidents: [], correlated: false },
      ]),
    ).toEqual(['The same rule fired for coder and reviewer close together in time, once: “Exfil”.'])
  })
})

describe('replay jobs', () => {
  const job = (over: Partial<ReplayJob>): ReplayJob => ({ job_id: 'j', status: 'done', label: 'ui', ruleset: 'rules:abc', engine: 'in-process', session_ids: null, created_at: '', started_at: null, finished_at: null, error: null, ...over })
  const sm = { tool_calls: 24, evaluated: 24, errors: 0, detections: 0, blocked: 0, approvals_required: 0, with_engine_verdict: 0, newly_blocked: 13, no_longer_blocked: 0, changed: 1, unchanged: 10 }
  const cases: [Partial<ReplayJob>, string][] = [
    [{ status: 'queued' }, 'Waiting to start.'],
    [{ status: 'running' }, 'Replaying the recorded tool calls…'],
    [{ status: 'failed', error: 'engine down\nstack' }, 'Failed: engine down'],
    [{ summary: sm }, 'Checked 24 tool calls: 13 newly blocked and 1 changed otherwise.'],
    [{ summary: { ...sm, evaluated: 20, newly_blocked: 0, changed: 0 } }, 'Checked 20 of 24 tool calls; nothing would change.'],
    [{}, 'Finished; open it to see what would change.'],
  ]
  for (const [over, want] of cases) it(want, () => expect(replayLine(job(over))).toBe(want))
  it('meta', () => {
    expect(replayMeta(job({ rules_loaded: 69, session_ids: ['a', 'b'] }))).toBe('rules:abc (69 rules) · 2 sessions')
  })
})
