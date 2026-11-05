// Table-driven tests of the Agents and Teams words (DESIGN.md §7.2 rule 8).
import { describe, expect, it } from 'vitest'
import type { AgentDetail, AgentSummary, GraphEdge, TeamDetail, TeamSummary } from '../../api/v1'
import {
  agentGlyph,
  agentMetaFigures,
  agentNeedsALook,
  agentOutcome,
  agentsSentence,
  analyticsSentence,
  agentSummaryLine,
  conversationSentence,
  handoffWords,
  ofIts,
  partsText,
  teamGlyph,
  teamMetaLine,
  teamName,
  teamNeedsALook,
  teamOutcome,
  teamsSentence,
  teamSummaryLine,
  worstHandoff,
  worstTool,
  type LinkedPart,
} from './agentsTeams'

function agent(over: Partial<AgentSummary> = {}): AgentSummary {
  return {
    agent_name: 'coder',
    invocation_count: 3,
    span_count: 22,
    error_count: 0,
    invocation_error_count: 0,
    first_seen: '2026-11-05T10:00:00Z',
    last_seen: '2026-11-05T11:00:00Z',
    total_input_tokens: 1800,
    total_output_tokens: 1080,
    total_cost_usd: 0.022,
    latency_ms_p50: 9000,
    llm_call_count: 9,
    tool_call_count: 10,
    models: ['claude-sonnet-4'],
    reasoning_tokens: 0,
    reasoning_step_count: 9,
    handoffs_in: 0,
    handoffs_out: 1,
    session_count: 3,
    tools: ['Bash', 'Read'],
    teams: ['code-crew'],
    detection_counts: {},
    flagged_tool_calls: 0,
    detection_rate: 0,
    scores: {},
    ...over,
  }
}

function detail(over: Partial<AgentDetail> = {}): AgentDetail {
  return { ...agent(), tool_counts: [], handoffs_from: [], handoffs_to: [], recent_sessions: [], ...over }
}

function team(over: Partial<TeamSummary> = {}): TeamSummary {
  return {
    team_id: 'trip-planners',
    name: 'Trip planners',
    auto: false,
    agents: ['orchestrator', 'researcher', 'writer'],
    session_count: 7,
    error_session_count: 0,
    handoff_count: 7,
    span_count: 50,
    input_tokens: 30000,
    output_tokens: 14160,
    cost_usd: 0.29,
    first_seen: '2026-11-05T05:00:00Z',
    last_activity: '2026-11-05T19:00:00Z',
    detection_count: {},
    ...over,
  }
}

const edge = (over: Partial<GraphEdge> = {}): GraphEdge => ({ from: 'orchestrator', to: 'researcher', kind: 'delegate', count: 5, errors: 0, error_rate: 0, p50_ms: 1300, p95_ms: null, mean_score: null, ...over })

function teamDetail(over: Partial<TeamDetail> = {}): TeamDetail {
  return { ...team(), agent_stats: [], graph: { nodes: [], edges: [] }, handoff_stats: [], recent_sessions: [], ...over }
}

/** Links as [text, target] pairs. */
const links = (parts: LinkedPart[]) =>
  parts.filter((p): p is Exclude<LinkedPart, string> => typeof p !== 'string').map((p) => [p.text, 'agent' in p ? p.agent : 'team' in p ? p.team : p.sessions])

describe('words', () => {
  it.each([
    [2, 7, 'run', '2 of its 7 runs'],
    [1, 1, 'run', 'its one run'],
    [3, 3, 'run', 'all 3 of its runs'],
    [1, 10, 'tool call', '1 of its 10 tool calls'],
  ])('ofIts(%i, %i, %s)', (k, n, one, want) => expect(ofIts(k, n, one)).toBe(want))
})

describe('agent rows', () => {
  it.each([
    ['fine', agent(), 'Ran 3 times in 3 sessions. Nothing failed or was flagged.', 'ok', false],
    ['runs failed', agent({ invocation_count: 7, session_count: 7, invocation_error_count: 3, error_count: 6 }), 'Ran 7 times in 7 sessions. 3 of its 7 runs failed.', 'failed', true],
    [
      'flagged, the example in the brief',
      agent({ error_count: 2, flagged_tool_calls: 6, detection_counts: { block: 1, require_approval: 5 } }),
      'Ran 3 times in 3 sessions. 2 steps failed on the way. 6 of its 10 tool calls were flagged, 1 blocked and 5 needed approval.',
      'blocked',
      true,
    ],
    ['only logged', agent({ flagged_tool_calls: 1, detection_counts: { log: 1 } }), 'Ran 3 times in 3 sessions. 1 of its 10 tool calls was flagged.', 'warning', true],
    ['one run, one session', agent({ invocation_count: 1, session_count: 1 }), 'Ran once in one session. Nothing failed or was flagged.', 'ok', false],
    ['a low score', agent({ scores: { GoalCompletionJudge: { mean: 0.3, count: 2 } } }), 'Ran 3 times in 3 sessions. Goal completion averaged 0.30 over 2 results.', 'warning', true],
    ['a good score is not trouble', agent({ scores: { GoalCompletionJudge: { mean: 0.9, count: 2 } } }), 'Ran 3 times in 3 sessions. Nothing failed or was flagged.', 'ok', false],
    ['no flag count from the server', agent({ flagged_tool_calls: undefined, detection_counts: { block: 2 } }), 'Ran 3 times in 3 sessions. AgentShield blocked 2 tool calls.', 'blocked', true],
  ] as [string, AgentSummary, string, string, boolean][])('%s', (_n, a, line, glyph, look) => {
    expect(agentSummaryLine(a)).toBe(line)
    expect(agentGlyph(a).kind).toBe(glyph)
    expect(agentNeedsALook(a)).toBe(look)
  })

  it('names the agents that need a look, linked', () => {
    const rows = [agent({ agent_name: 'orchestrator', invocation_error_count: 3 }), agent({ agent_name: 'writer' }), agent({ agent_name: 'coder', detection_counts: { block: 1 }, flagged_tool_calls: 1 })]
    const parts = agentsSentence({ agents: rows, time: '24h', filtered: false })
    expect(partsText(parts)).toBe('3 agents ran in the last 24 hours. 2 need a look: orchestrator and coder.')
    expect(links(parts)).toEqual([
      ['orchestrator', 'orchestrator'],
      ['coder', 'coder'],
    ])
  })

  it.each([
    ['nothing', [], '7d', false, 'No agents ran in the last 7 days.'],
    ['nothing matches', [], '24h', true, 'No agents match these filters in the last 24 hours.'],
    ['all fine', [agent(), agent({ agent_name: 'writer' })], '24h', false, '2 agents ran in the last 24 hours. Nothing they did failed or was flagged.'],
    ['one, fine', [agent()], 'all', true, '1 agent matches these filters. Nothing it did failed or was flagged.'],
    ['one in trouble', [agent({ invocation_error_count: 1 })], '24h', false, '1 agent ran in the last 24 hours. It needs a look.'],
    ['both in trouble', [agent({ invocation_error_count: 1 }), agent({ agent_name: 'b', invocation_error_count: 1 })], '24h', false, '2 agents ran in the last 24 hours. Both need a look: coder and b.'],
    [
      'many in trouble',
      ['a', 'b', 'c', 'd', 'e', 'f'].map((n) => agent({ agent_name: n, invocation_error_count: 1 })).concat([agent({ agent_name: 'g' })]),
      '24h',
      false,
      '7 agents ran in the last 24 hours. 6 need a look: a, b, c, and 3 more.',
    ],
  ] as [string, AgentSummary[], '24h' | '7d' | 'all', boolean, string][])('%s', (_n, agents, time, filtered, want) => {
    expect(partsText(agentsSentence({ agents, time, filtered }))).toBe(want)
  })
})

describe('one agent', () => {
  const tools = [
    { tool: 'Read', count: 5, errors: 0 },
    { tool: 'Bash', count: 4, errors: 2 },
  ]
  it.each([
    [
      'a tool failed, runs did not; flagged',
      detail({ error_count: 2, flagged_tool_calls: 6, detection_counts: { block: 1, require_approval: 5 }, tool_counts: tools }),
      'Ran 3 times in 3 sessions, usually taking 9 seconds. 2 of its 4 Bash calls failed, though none of its runs did. AgentShield flagged 6 of its 10 tool calls: 1 blocked and 5 needed approval.',
    ],
    [
      'runs failed, and where',
      detail({ invocation_count: 5, session_count: 5, invocation_error_count: 3, tool_counts: [{ tool: 'web_search', count: 5, errors: 3 }], tool_call_count: 5 }),
      'Ran 5 times in 5 sessions, usually taking 9 seconds. 3 of its 5 runs failed, and 3 of its 5 web_search calls failed. AgentShield flagged none of its tool calls.',
    ],
    ['clean', detail({ invocation_count: 1, session_count: 1, tool_call_count: 0 }), 'Ran once in one session. It did not fail.'],
    [
      'a low score',
      detail({ tool_call_count: 2, scores: { reasoning_consistency: { mean: 0.4, count: 1 } } }),
      'Ran 3 times in 3 sessions, usually taking 9 seconds. None of its runs failed. AgentShield flagged none of its tool calls. Reasoning consistency scored 0.40.',
    ],
  ] as [string, AgentDetail, string][])('%s', (_n, d, want) => expect(partsText(agentOutcome(d))).toBe(want))

  it('links each figure to the sessions behind it', () => {
    const d = detail({ invocation_error_count: 1, flagged_tool_calls: 1, detection_counts: { block: 1 }, tool_counts: tools })
    expect(links(agentOutcome(d))).toEqual([
      ['1 of its 3 runs failed', { agent: 'coder', status: 'error', time: 'all' }],
      ['2 of its 4 Bash calls failed', { agent: 'coder', tool: 'Bash', time: 'all' }],
      ['flagged 1 of its 10 tool calls', { agent: 'coder', has: 'flagged', time: 'all' }],
    ])
  })

  it('picks the tool that fails most', () => {
    expect(worstTool([{ tool: 'a', count: 10, errors: 2 }, { tool: 'b', count: 2, errors: 2 }, { tool: 'c', count: 5, errors: 1 }])?.tool).toBe('b')
    expect(worstTool(tools.slice(0, 1))).toBeNull()
  })

  it('writes the meta figures and hand-off words', () => {
    const now = new Date('2026-11-05T11:50:00Z').getTime()
    expect(agentMetaFigures(agent(), now)).toEqual(['claude-sonnet-4', '9 reasoning steps', 'cost $0.022', 'last seen 50 min ago'])
    expect(handoffWords({ kind: 'delegate', count: 5 })).toBe('Delegated 5 times.')
    expect(handoffWords({ kind: 'handoff', count: 1 })).toBe('Handed work on once.')
  })
})

describe('teams', () => {
  it.each([
    ['fine', team(), 'Worked in 7 sessions. Its agents handed work on 7 times.', 'ok', false],
    ['failed', team({ error_session_count: 3 }), 'Worked in 7 sessions and 3 failed. Its agents handed work on 7 times.', 'failed', true],
    [
      'blocked and approvals',
      team({ session_count: 3, handoff_count: 1, detection_count: { block: 1, require_approval: 5 } }),
      'Worked in 3 sessions. Its agents handed work on once. AgentShield blocked one tool call and asked for approval 5 times.',
      'blocked',
      true,
    ],
    ['one failed session, no hand-offs', team({ session_count: 1, error_session_count: 1, handoff_count: 0 }), 'Worked in one session, which failed. No hand-offs.', 'failed', true],
  ] as [string, TeamSummary, string, string, boolean][])('%s', (_n, t, line, glyph, look) => {
    expect(teamSummaryLine(t)).toBe(line)
    expect(teamGlyph(t).kind).toBe(glyph)
    expect(teamNeedsALook(t)).toBe(look)
  })

  it('names automatic teams after their agents', () => {
    expect(teamName({ team_id: 'auto:coder+reviewer', name: '', auto: true })).toBe('coder + reviewer')
    expect(teamMetaLine(team({ auto: true, cost_usd: null }))).toBe('orchestrator, researcher, writer · grouped automatically')
  })

  it.each([
    ['none', [], '24h', false, 'No teams worked in the last 24 hours.'],
    ['all fine', [team()], '24h', false, '1 team worked in the last 24 hours. No session failed, and AgentShield neither blocked a tool call nor asked for approval.'],
    ['one in trouble', [team({ error_session_count: 3 }), team({ team_id: 'b', name: 'B' })], '7d', false, '2 teams worked in the last 7 days. Trip planners needs a look: 3 of its 7 sessions failed.'],
    ['one blocked', [team({ detection_count: { block: 1 } }), team({ team_id: 'b', name: 'B' })], '24h', true, '2 teams match these filters in the last 24 hours. Trip planners needs a look: AgentShield blocked one tool call.'],
    ['both', [team({ error_session_count: 3 }), team({ team_id: 'code-crew', name: 'Code crew', detection_count: { block: 1 } })], '24h', false, '2 teams worked in the last 24 hours. Both need a look: Trip planners and Code crew.'],
  ] as [string, TeamSummary[], '24h' | '7d', boolean, string][])('%s', (_n, teams, time, filtered, want) => {
    expect(partsText(teamsSentence({ teams, time, filtered }))).toBe(want)
  })
})

describe('one team', () => {
  it('leads with the hand-off that fails most, linking to its failed sessions', () => {
    const d = teamDetail({ error_session_count: 3, handoff_stats: [edge({ to: 'writer', count: 2 }), edge({ errors: 3, error_rate: 0.6 })] })
    const parts = teamOutcome(d)
    expect(partsText(parts)).toBe(
      'Trip planners worked in 7 sessions and 3 failed, each time at the hand-off from orchestrator to researcher, which failed 3 of 5 times, usually after 1.3 seconds.',
    )
    expect(links(parts)).toEqual([
      ['3 failed', { team: 'trip-planners', status: 'error', time: 'all' }],
      ['the hand-off from orchestrator to researcher', { team: 'trip-planners', agent: 'researcher', status: 'error', time: 'all' }],
    ])
  })

  it('says "each time at the hand-off" only when that route accounts for every failed session', () => {
    // More failed sessions than failed hand-offs: some failed elsewhere.
    const more = teamDetail({ error_session_count: 4, handoff_stats: [edge({ errors: 3, error_rate: 0.6 })] })
    expect(partsText(teamOutcome(more))).toBe(
      'Trip planners worked in 7 sessions and 4 failed. The hand-off that fails most, from orchestrator to researcher, failed 3 of 5 times, usually after 1.3 seconds.',
    )
    expect(links(teamOutcome(more))[1]).toEqual(['from orchestrator to researcher', { team: 'trip-planners', agent: 'researcher', status: 'error', time: 'all' }])
    // Another route failed too.
    const two = teamDetail({ error_session_count: 3, handoff_stats: [edge({ errors: 3, error_rate: 0.6 }), edge({ to: 'writer', count: 2, errors: 1, error_rate: 0.5 })] })
    expect(partsText(teamOutcome(two))).toMatch(/^Trip planners worked in 7 sessions and 3 failed\. The hand-off that fails most/)
    // One failed session, at the one route that failed.
    const one = teamDetail({ error_session_count: 1, handoff_stats: [edge({ errors: 1, error_rate: 0.2 })] })
    expect(partsText(teamOutcome(one))).toBe('Trip planners worked in 7 sessions and 1 failed, at the hand-off from orchestrator to researcher, which failed 1 of 5 times, usually after 1.3 seconds.')
  })

  it.each([
    ['no failures', teamDetail({ handoff_stats: [edge(), edge({ to: 'writer', count: 2 })] }), 'Trip planners worked in 7 sessions. None of its 7 hand-offs failed.'],
    ['one hand-off', teamDetail({ session_count: 1, handoff_stats: [edge({ count: 1 })] }), 'Trip planners worked in one session. Its one hand-off did not fail.'],
    ['no hand-offs', teamDetail({ detection_count: { block: 1 } }), 'Trip planners worked in 7 sessions. Its agents did not hand work to each other. AgentShield blocked one tool call.'],
    ['returns are not hand-offs', teamDetail({ handoff_stats: [edge({ kind: 'return', errors: 2, error_rate: 1 })] }), 'Trip planners worked in 7 sessions. Its agents did not hand work to each other.'],
  ] as [string, TeamDetail, string][])('%s', (_n, d, want) => expect(partsText(teamOutcome(d))).toBe(want))

  it('ranks failing routes by rate, then failures', () => {
    expect(worstHandoff([edge({ to: 'a', errors: 1, error_rate: 0.5 }), edge({ to: 'b', errors: 3, error_rate: 0.5 }), edge({ to: 'c', errors: 1, error_rate: 0.1 })])?.to).toBe('b')
    expect(worstHandoff([edge()])).toBeNull()
  })
})

describe('analytics', () => {
  it.each([
    [{ agents: 5, sessions: 10, invocations: 18, failed: 6, tokens: 47360, cost: 0.31 }, '24h', undefined, 'In the last 24 hours, 5 agents ran 18 times in 10 sessions. 6 of the runs failed. They used 47,360 tokens, costing $0.31.'],
    [{ agents: 1, sessions: 0, invocations: 1, failed: 0, tokens: 0, cost: 0 }, 'all', undefined, '1 agent ran once. None of the runs failed.'],
    [{ agents: 0, sessions: 0, invocations: 0, failed: 0, tokens: 0, cost: 0 }, '6h', undefined, 'In the last 6 hours, nothing was recorded.'],
    [{ agents: 1, sessions: 3, invocations: 3, failed: 0, tokens: 2880, cost: 0.022 }, '24h', 'coder', 'In the last 24 hours, coder ran 3 times in 3 sessions. None of its runs failed. It used 2,880 tokens, costing $0.022.'],
    [{ agents: 1, sessions: 2, invocations: 2, failed: 2, tokens: 0, cost: 0 }, '7d', 'researcher', 'In the last 7 days, researcher ran twice in 2 sessions. Both of its runs failed.'],
    [{ agents: 0, sessions: 0, invocations: 0, failed: 0, tokens: 0, cost: 0 }, '24h', 'coder', 'In the last 24 hours, nothing was recorded for coder.'],
  ])('writes the totals %#', (t, range, agent, want) => expect(analyticsSentence(t, range, agent)).toBe(want))
  it('writes a turn view', () => {
    expect(conversationSentence({ total_turns: 1, agent_names: ['coder', 'reviewer'], span_count: 8, error_count: 1 })).toBe('One turn with coder and reviewer; one step ended with an error.')
    expect(conversationSentence({ total_turns: 3, agent_names: [], span_count: 1, error_count: 0 })).toBe('3 turns. Nothing failed.')
  })
})
