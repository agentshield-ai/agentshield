// Generated words for the Agents and Teams pages (DESIGN.md §7.1 rule 1,
// §7.2 rule 8): the list sentences, one line per agent or team row, and the
// sentence at the top of one agent's or one team's page. Pure functions of
// what /api/v1/agents and /api/v1/teams return, never a model: every number
// is one the API gave or one counted here. Tests: agentsTeams.test.ts.
import type { AgentDetail, AgentSummary, GraphEdge, TeamDetail, TeamSummary } from '../../api/v1'
import type { FilterState, TimeRange } from '../filters'
import { fmtCost } from '../format'
import { countWord, durationWords, joinClauses, plural, RANGE_PHRASE, relativeTime, scorerWords } from '../summaries'

/** A part of a generated sentence: text, or a phrase that links somewhere. */
export type LinkedPart =
  | string
  | { text: string; agent: string }
  | { text: string; team: string }
  /** A link to the sessions this filter lists (laid over nothing: a fresh Sessions URL). */
  | { text: string; sessions: FilterState }

const cap = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : s)

/** "2 of its 7 runs", "its one run", "all 3 of its runs" (k of n). */
export function ofIts(k: number, n: number, one: string, many = `${one}s`): string {
  if (n === 1 && k === 1) return `its one ${one}`
  if (k === n) return `all ${n} of its ${many}`
  return `${k} of its ${n} ${n === 1 ? one : many}`
}
/** Low scores: a mean below this needs a look (the Sessions default). */
export const LOW_SCORE = 0.5

/** The lowest score below `low`, if any. */
function lowestScore(scores: Record<string, { mean: number; count: number }> | null | undefined, low = LOW_SCORE) {
  let best: { name: string; mean: number; count: number } | null = null
  for (const [name, v] of Object.entries(scores ?? {})) if (v.mean < low && (!best || v.mean < best.mean)) best = { name, ...v }
  return best
}

function scoreSentence(sc: { name: string; mean: number; count: number }): string {
  return sc.count > 1 ? `${cap(scorerWords(sc.name))} averaged ${sc.mean.toFixed(2)} over ${sc.count} results.` : `${cap(scorerWords(sc.name))} scored ${sc.mean.toFixed(2)}.`
}

/** "1 blocked and 5 needed approval", or "" when neither. */
function verdictWords(c: Partial<Record<string, number>> | null | undefined): string {
  const xs: string[] = []
  if (c?.block) xs.push(`${c.block} blocked`)
  if (c?.require_approval) xs.push(`${c.require_approval} needed approval`)
  return joinClauses(xs)
}

/** Names as a list, at most `max` of them, then "and N more". */
function nameList<T>(xs: T[], max: number, part: (x: T) => LinkedPart): LinkedPart[] {
  const shown = xs.slice(0, xs.length > max ? max - 1 : max)
  const rest = xs.length - shown.length
  const items: LinkedPart[][] = shown.map((x) => [part(x)])
  if (rest > 0) items.push([`${rest} more`])
  const out: LinkedPart[] = []
  items.forEach((it, i) => {
    if (i > 0) out.push(items.length === 2 ? ' and ' : i === items.length - 1 ? ', and ' : ', ')
    out.push(...it)
  })
  return out
}

// ---------------------------------------------------------------- agents

export interface AgentFlags {
  runs: number
  failedRuns: number
  /** Steps (tool calls, model calls) that ended with an error, beyond the failed runs. */
  failedSteps: number
  blocks: number
  approvals: number
  /** Tool calls with any non-allow verdict (block, approval or a logged alert). */
  flagged: number
  lowScore: { name: string; mean: number; count: number } | null
}

export function agentFlags(a: AgentSummary): AgentFlags {
  const failedRuns = a.invocation_error_count ?? 0
  const counts = a.detection_counts ?? {}
  const anyVerdict = (counts.block ?? 0) + (counts.require_approval ?? 0) + (counts.log ?? 0)
  return {
    runs: a.invocation_count,
    failedRuns,
    failedSteps: Math.max(0, a.error_count - failedRuns),
    blocks: counts.block ?? 0,
    approvals: counts.require_approval ?? 0,
    flagged: a.flagged_tool_calls ?? (anyVerdict > 0 ? anyVerdict : 0),
    lowScore: lowestScore(a.scores),
  }
}

/** An agent needs a look when a run or step failed, AgentShield flagged a tool call, or a score is low. */
export function agentNeedsALook(a: AgentSummary): boolean {
  const f = agentFlags(a)
  return f.failedRuns > 0 || f.failedSteps > 0 || f.blocks > 0 || f.approvals > 0 || f.flagged > 0 || !!f.lowScore
}

export type AgentGlyph = 'failed' | 'blocked' | 'approval' | 'warning' | 'ok'

/** The glyph of an agent row, most serious first. */
export function agentGlyph(a: AgentSummary): { kind: AgentGlyph; label: string } {
  const f = agentFlags(a)
  if (f.failedRuns) return { kind: 'failed', label: 'Runs failed' }
  if (f.blocks) return { kind: 'blocked', label: 'Blocked by AgentShield' }
  if (f.approvals) return { kind: 'approval', label: 'Needed approval' }
  if (f.failedSteps) return { kind: 'warning', label: 'A step failed' }
  if (f.flagged) return { kind: 'warning', label: 'Flagged by AgentShield' }
  if (f.lowScore) return { kind: 'warning', label: 'Low score' }
  return { kind: 'ok', label: 'Fine' }
}

/** "Ran 7 times in 3 sessions." */
function ranLine(a: Pick<AgentSummary, 'invocation_count' | 'session_count'>): string {
  const where = a.session_count ? ` in ${countWord(a.session_count, 'session')}` : ''
  if (!a.invocation_count) return a.session_count ? `Ran${where}.` : 'No runs recorded.'
  return `Ran ${a.invocation_count === 1 ? 'once' : `${a.invocation_count} times`}${where}.`
}

/** "6 of its 10 tool calls were flagged, 1 blocked and 5 needed approval." */
function flaggedLine(a: AgentSummary, f: AgentFlags): string {
  const verdicts = verdictWords(a.detection_counts)
  if (a.flagged_tool_calls != null && a.tool_call_count > 0 && f.flagged > 0) {
    const k = Math.min(f.flagged, a.tool_call_count)
    return `${cap(ofIts(k, a.tool_call_count, 'tool call'))} ${k === 1 ? 'was' : 'were'} flagged${verdicts ? `, ${verdicts}` : ''}.`
  }
  if (f.blocks) return `AgentShield blocked ${countWord(f.blocks, 'tool call')}.`
  if (f.approvals) return `AgentShield asked for approval ${countWord(f.approvals, 'time')}.`
  if (f.flagged) return `AgentShield flagged ${countWord(f.flagged, 'tool call')}.`
  return ''
}

/**
 * The line under an agent row: how much it ran, then what went wrong
 * ("Ran 4 times in 3 sessions. 6 of its 10 tool calls were flagged, 1
 * blocked and 5 needed approval."), or that nothing did.
 */
export function agentSummaryLine(a: AgentSummary): string {
  const f = agentFlags(a)
  const out = [ranLine(a)]
  if (f.failedRuns) out.push(`${cap(ofIts(f.failedRuns, f.runs, 'run'))} failed.`)
  else if (f.failedSteps) out.push(`${cap(countWord(f.failedSteps, 'step'))} failed on the way.`)
  const fl = flaggedLine(a, f)
  if (fl) out.push(fl)
  if (f.lowScore) out.push(scoreSentence(f.lowScore))
  if (out.length === 1) out.push('Nothing failed or was flagged.')
  return out.join(' ')
}

/** The quiet meta line of an agent row: models · cost. */
export function agentMetaLine(a: AgentSummary): string {
  const bits: string[] = []
  if (a.models?.length) bits.push(a.models.join(', '))
  if (a.total_cost_usd != null) bits.push(`cost ${fmtCost(a.total_cost_usd)}`)
  return bits.join(' · ')
}

export interface AgentsSentenceInput {
  agents: AgentSummary[]
  time: TimeRange
  filtered: boolean
}

/** The Agents page sentence: how many agents ran, then which need a look, each named and linked. */
export function agentsSentence({ agents, time, filtered }: AgentsSentenceInput): LinkedPart[] {
  const n = agents.length
  const range = RANGE_PHRASE[time] ?? ''
  const where = range ? ` ${range}` : ''
  if (!n) return [`No agents ${filtered ? 'match these filters' : 'ran'}${where}.`]
  const head = `${plural(n, 'agent')} ${filtered ? (n === 1 ? 'matches these filters' : 'match these filters') : 'ran'}${where}.`
  const look = agents.filter(agentNeedsALook)
  if (!look.length) return [head, ` ${n === 1 ? 'Nothing it did' : 'Nothing they did'} failed or was flagged.`]
  if (n === 1) return [head, ' It ', { text: 'needs a look', agent: look[0].agent_name }, '.']
  return [head, ` ${look.length === n ? (n === 2 ? 'Both' : `All ${n}`) : look.length} need${look.length === 1 && look.length !== n ? 's' : ''} a look: `, ...nameList(look, 4, (a) => ({ text: a.agent_name, agent: a.agent_name })), '.']
}

/** The tool whose calls failed most (most failures, then highest share). */
export function worstTool(tools: AgentDetail['tool_counts']): AgentDetail['tool_counts'][number] | null {
  let best: AgentDetail['tool_counts'][number] | null = null
  for (const t of tools) if (t.errors > 0 && (!best || t.errors > best.errors || (t.errors === best.errors && t.errors / t.count > best.errors / best.count))) best = t
  return best
}

/**
 * One agent's page sentence: how much it ran, whether its runs failed and
 * where (the tool whose calls failed most), what AgentShield flagged, and a
 * low score. Links open the sessions behind each figure.
 */
export function agentOutcome(d: AgentDetail): LinkedPart[] {
  const f = agentFlags(d)
  const agent = d.agent_name
  const out: LinkedPart[] = []
  const usual = d.latency_ms_p50 != null && d.invocation_count > 1 ? `, usually taking ${durationWords(d.latency_ms_p50)}` : ''
  out.push(ranLine(d).replace(/\.$/, `${usual}.`))
  const tool = worstTool(d.tool_counts ?? [])
  const toolPart = (t: NonNullable<typeof tool>, start: boolean): LinkedPart => {
    const words = `${ofIts(t.errors, t.count, `${t.tool} call`)} failed`
    return { text: start ? cap(words) : words, sessions: { agent, tool: t.tool, time: 'all' } }
  }
  if (f.failedRuns) {
    out.push(' ', { text: `${cap(ofIts(f.failedRuns, f.runs, 'run'))} failed`, sessions: { agent, status: 'error', time: 'all' } })
    if (tool) out.push(', and ', toolPart(tool, false))
    out.push('.')
  } else if (tool) {
    out.push(' ', toolPart(tool, true), `, though ${f.runs === 1 ? 'its run did not' : 'none of its runs did'}.`)
  } else if (f.failedSteps) {
    out.push(` ${cap(countWord(f.failedSteps, 'step'))} failed on the way, though ${f.runs === 1 ? 'its run did not' : 'none of its runs did'}.`)
  } else if (f.runs) {
    out.push(` ${f.runs === 1 ? 'It did not fail' : 'None of its runs failed'}.`)
  }
  if (f.flagged && d.tool_call_count > 0 && d.flagged_tool_calls != null) {
    const verdicts = verdictWords(d.detection_counts)
    out.push(' AgentShield ', { text: `flagged ${ofIts(Math.min(f.flagged, d.tool_call_count), d.tool_call_count, 'tool call')}`, sessions: { agent, has: 'flagged', time: 'all' } }, verdicts ? `: ${verdicts}.` : '.')
  } else if (f.blocks || f.approvals) {
    out.push(' ', { text: `AgentShield ${joinClauses([f.blocks ? `blocked ${countWord(f.blocks, 'tool call')}` : '', f.approvals ? `asked for approval ${countWord(f.approvals, 'time')}` : ''].filter(Boolean))}`, sessions: { agent, has: 'flagged', time: 'all' } }, '.')
  } else if (d.tool_call_count > 0) {
    out.push(` AgentShield flagged none of its tool calls.`)
  }
  if (f.lowScore) out.push(' ', scoreSentence(f.lowScore))
  return out
}

/** One agent's quiet meta line: models · reasoning steps · cost · last seen. */
export function agentMetaFigures(d: AgentSummary, now = Date.now()): string[] {
  const bits: string[] = []
  if (d.models?.length) bits.push(d.models.join(', '))
  if (d.reasoning_step_count) bits.push(plural(d.reasoning_step_count, 'reasoning step'))
  if (d.total_cost_usd != null) bits.push(`cost ${fmtCost(d.total_cost_usd)}`)
  if (d.last_seen) bits.push(`last seen ${relativeTime(d.last_seen, now)}`)
  return bits
}

/** A hand-off route under its heading ("Who hands work to it"): "Delegated 5 times.", "Handed work on once." */
export function handoffWords(h: { kind: string; count: number }): string {
  const times = h.count === 1 ? 'once' : `${h.count} times`
  return h.kind === 'delegate' ? `Delegated ${times}.` : `Handed work on ${times}.`
}

// ---------------------------------------------------------------- teams

/** A team's display name: its name, or the agents an automatic team was made from. */
export function teamName(t: Pick<TeamSummary, 'team_id' | 'name' | 'auto'>): string {
  if (t.name) return t.name
  return t.team_id.startsWith('auto:') ? t.team_id.slice(5).replace(/\+/g, ' + ') : t.team_id
}

export function teamNeedsALook(t: TeamSummary): boolean {
  return t.error_session_count > 0 || (t.detection_count?.block ?? 0) > 0 || (t.detection_count?.require_approval ?? 0) > 0
}

export function teamGlyph(t: TeamSummary): { kind: 'failed' | 'blocked' | 'approval' | 'ok'; label: string } {
  if (t.error_session_count) return { kind: 'failed', label: 'Sessions failed' }
  if (t.detection_count?.block) return { kind: 'blocked', label: 'Blocked by AgentShield' }
  if (t.detection_count?.require_approval) return { kind: 'approval', label: 'Needed approval' }
  return { kind: 'ok', label: 'Fine' }
}

/** "AgentShield blocked 1 tool call and asked for approval 5 times." or "". */
function teamSecurity(c: Partial<Record<string, number>> | null | undefined): string {
  const xs: string[] = []
  if (c?.block) xs.push(`blocked ${countWord(c.block, 'tool call')}`)
  if (c?.require_approval) xs.push(`asked for approval ${countWord(c.require_approval, 'time')}`)
  return xs.length ? `AgentShield ${joinClauses(xs)}.` : ''
}

/** "Worked in 7 sessions and 3 failed." */
function teamSessions(t: TeamSummary): string {
  const n = t.session_count
  if (!n) return 'No sessions recorded.'
  if (n === 1) return t.error_session_count ? 'Worked in one session, which failed.' : 'Worked in one session.'
  return `Worked in ${n} sessions${t.error_session_count ? ` and ${t.error_session_count} failed` : ''}.`
}

/** The line under a team row: its sessions and failures, hand-offs, and what AgentShield did. */
export function teamSummaryLine(t: TeamSummary): string {
  const out = [teamSessions(t)]
  out.push(t.handoff_count ? `Its agents handed work on ${t.handoff_count === 1 ? 'once' : `${t.handoff_count} times`}.` : 'No hand-offs.')
  const sec = teamSecurity(t.detection_count)
  if (sec) out.push(sec)
  return out.join(' ')
}

/** The meta line of a team row: its agents, and whether Weft grouped it. */
export function teamMetaLine(t: TeamSummary): string {
  const bits = [t.agents.join(', ') || 'no agents recorded']
  if (t.auto) bits.push('grouped automatically')
  if (t.cost_usd != null) bits.push(`cost ${fmtCost(t.cost_usd)}`)
  return bits.join(' · ')
}

export interface TeamsSentenceInput {
  teams: TeamSummary[]
  time: TimeRange
  filtered: boolean
}

/** The Teams page sentence: how many teams worked, then which need a look, named and linked. */
export function teamsSentence({ teams, time, filtered }: TeamsSentenceInput): LinkedPart[] {
  const n = teams.length
  const range = RANGE_PHRASE[time] ?? ''
  const where = range ? ` ${range}` : ''
  if (!n) return [`No teams ${filtered ? 'match these filters' : 'worked'}${where}.`]
  const head = `${plural(n, 'team')} ${filtered ? (n === 1 ? 'matches these filters' : 'match these filters') : 'worked'}${where}.`
  const look = teams.filter(teamNeedsALook)
  if (!look.length) return [head, ' No session failed, and AgentShield neither blocked a tool call nor asked for approval.']
  if (look.length === 1) {
    const t = look[0]
    const why = t.error_session_count
      ? `${ofIts(t.error_session_count, t.session_count, 'session')} failed`
      : teamSecurity(t.detection_count).replace(/\.$/, '')
    return [head, ' ', { text: teamName(t), team: t.team_id }, ` needs a look: ${why}.`]
  }
  return [head, ` ${look.length === n ? (n === 2 ? 'Both' : `All ${n}`) : look.length} need a look: `, ...nameList(look, 4, (t) => ({ text: teamName(t), team: t.team_id })), '.']
}

/** The hand-off route that fails most: highest failure rate, then most failures; null when none failed. */
export function worstHandoff(edges: GraphEdge[]): GraphEdge | null {
  return [...edges].filter((e) => e.kind !== 'return' && e.errors > 0).sort((a, b) => b.error_rate - a.error_rate || b.errors - a.errors || b.count - a.count)[0] ?? null
}

/**
 * One team's page sentence, leading with the hand-off that fails most
 * (DESIGN.md §7): "Trip planners worked in 7 sessions and 3 failed, each
 * time at the hand-off from orchestrator to researcher, which failed 3 of 5
 * times, usually after 1.3 seconds." "Each time at the hand-off" is said only
 * when that route's failures account for every failed session and no other
 * route failed; otherwise: "… and 3 failed. The hand-off that fails most,
 * from orchestrator to researcher, failed 3 of 5 times, …".
 */
export function teamOutcome(d: TeamDetail): LinkedPart[] {
  const out: LinkedPart[] = []
  const name = teamName(d)
  const n = d.session_count
  const failed = d.error_session_count
  const failedLink = (text: string): LinkedPart => ({ text, sessions: { team: d.team_id, status: 'error', time: 'all' } })
  const worst = worstHandoff(d.handoff_stats ?? [])
  const forward = (d.handoff_stats ?? []).filter((e) => e.kind !== 'return')
  const failedHandoffs = forward.reduce((s, e) => s + e.errors, 0)
  const route = (w: GraphEdge): LinkedPart => ({ text: `the hand-off from ${w.from} to ${w.to}`, sessions: { team: d.team_id, agent: w.to, status: 'error', time: 'all' } })
  const after = (w: GraphEdge) => (w.p50_ms != null ? `, usually after ${durationWords(w.p50_ms)}` : '')
  const times = (w: GraphEdge) => `${w.errors} of ${w.count} ${w.count === 1 ? 'time' : 'times'}`
  // Every failed session failed at this one route: its failures are all the failures, one per failed session.
  const eachTime = !!worst && failed > 0 && worst.errors === failed && failedHandoffs === worst.errors

  if (!n) out.push(`${name} has no sessions recorded.`)
  else if (n === 1) {
    if (!failed) out.push(`${name} worked in one session.`)
    else if (eachTime) out.push(`${name} worked in one session, which `, failedLink('failed'), ' at ', route(worst!), `${after(worst!)}.`)
    else out.push(`${name} worked in one session, which `, failedLink('failed'), '.')
  } else {
    out.push(`${name} worked in ${n} sessions`)
    if (failed) out.push(' and ', failedLink(`${failed} failed`))
    if (eachTime) out.push(failed === 1 ? ', at ' : ', each time at ', route(worst!), `, which failed ${times(worst!)}${after(worst!)}.`)
    else out.push('.')
  }
  if (worst && !eachTime) {
    const r = route(worst) as { text: string; sessions: FilterState }
    out.push(' The hand-off that fails most, ', { ...r, text: r.text.replace(/^the hand-off /, '') }, `, failed ${times(worst)}${after(worst)}.`)
  } else if (!worst && forward.length) {
    const total = forward.reduce((s, e) => s + e.count, 0)
    out.push(total === 1 ? ' Its one hand-off did not fail.' : ` None of its ${total} hand-offs failed.`)
  } else if (!worst && n) {
    out.push(' Its agents did not hand work to each other.')
  }
  const sec = teamSecurity(d.detection_count)
  if (sec) out.push(' ', sec)
  return out
}

/** One team's quiet meta line: agents · hand-offs · tokens · cost · last active. */
export function teamMetaFigures(d: TeamDetail, now = Date.now()): string[] {
  const bits: string[] = []
  bits.push(d.agents.length ? joinClauses(d.agents).replace(/, and /, ' and ') : 'no agents recorded')
  if (d.auto) bits.push('grouped automatically from the agents in each session')
  if (d.input_tokens + d.output_tokens) bits.push(`${(d.input_tokens + d.output_tokens).toLocaleString('en-US')} tokens`)
  if (d.cost_usd != null) bits.push(`cost ${fmtCost(d.cost_usd)}`)
  if (d.last_activity) bits.push(`last active ${relativeTime(d.last_activity, now)}`)
  return bits
}

/** The plain text of linked parts (tests, titles). */
export function partsText(parts: LinkedPart[]): string {
  return parts.map((p) => (typeof p === 'string' ? p : p.text)).join('')
}

// ---------------------------------------------------------------- analytics

const num = (n: number) => n.toLocaleString('en-US')

/** "It used 44,160 tokens, costing $0.29." */
function usage(tokens: number, cost: number | null, who = 'It'): string {
  if (!tokens && cost == null) return ''
  const t = tokens ? `${who} used ${plural(tokens, 'token').replace(/^\d+/, num(tokens))}` : ''
  if (cost == null || !cost) return t ? `${t}.` : ''
  return t ? `${t}, costing ${fmtCost(cost)}.` : `${who} cost ${fmtCost(cost)}.`
}

/** Totals over a time window, of every agent or of one (the analytics pages). */
export interface AgentsTotals {
  agents: number
  sessions: number
  invocations: number
  failed: number
  tokens: number
  cost: number
}

/**
 * The analytics sentence, of every agent ("In the last 24 hours, 5 agents
 * ran 18 times in 10 sessions. 6 of the runs failed. They used 47,360
 * tokens, costing $0.31.") or of one ("In the last 24 hours, coder ran 3
 * times in 3 sessions. None of its runs failed. It used …").
 */
export function analyticsSentence(t: AgentsTotals, time: TimeRange | string, agent?: string): string {
  const range = time === '1h' ? 'in the last hour' : time === '6h' ? 'in the last 6 hours' : (RANGE_PHRASE[time as TimeRange] ?? '')
  const lead = range ? `${cap(range)}, ` : ''
  if (!t.invocations && !t.tokens && !(agent ? false : t.agents)) return `${lead ? `${lead}n` : 'N'}othing was recorded${agent ? ` for ${agent}` : ''}.`
  const who = agent ?? (t.agents ? plural(t.agents, 'agent').replace(/^\d+/, num(t.agents)) : 'agents')
  const ran = t.invocations ? ` ran ${t.invocations === 1 ? 'once' : `${num(t.invocations)} times`}` : ' ran'
  const where = t.sessions ? ` in ${plural(t.sessions, 'session').replace(/^\d+/, num(t.sessions))}` : ''
  const out = [`${lead}${who}${ran}${where}.`]
  const its = agent ? 'its runs' : 'the runs'
  const all = t.failed === 2 ? 'Both' : `All ${num(t.failed)}`
  if (t.invocations) out.push(t.failed ? `${t.failed === t.invocations && t.failed > 1 ? all : num(t.failed)} of ${its} failed.` : `None of ${its} failed.`)
  const u = usage(t.tokens, t.cost, agent || t.agents === 1 ? 'It' : 'They')
  if (u) out.push(u)
  return out.join(' ').replace(/^./, (c) => c.toUpperCase())
}

/** The turn view's sentence: "12 turns with coder and reviewer; 2 steps ended with an error." */
export function conversationSentence(c: { total_turns: number; agent_names: string[]; span_count: number; error_count: number }): string {
  const agents = c.agent_names.length ? ` with ${joinClauses(c.agent_names).replace(/, and /, ' and ')}` : ''
  const head = `${cap(countWord(c.total_turns, 'turn'))}${agents}`
  return c.error_count ? `${head}; ${c.error_count === 1 ? 'one step' : `${num(c.error_count)} steps`} ended with an error.` : `${head}. Nothing failed.`
}
