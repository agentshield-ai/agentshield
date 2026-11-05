// Generated summaries (DESIGN.md §7.2 rule 8): the one-line summary of a
// session row, the Sessions page sentence, and a session's outcome
// sentence. Pure functions of the data the API returns, never a model, so
// they are exact: every number is one the API gave or one counted here from
// its rows, and the Sessions sentence uses the server's attention counts
// (GET /api/v1/sessions/attention), the same numbers the old attention cards
// showed. Table-driven tests: summaries.test.ts.
import type { Detection, SessionAttention, SessionSummary } from '../api/v1'
import { APPROVAL_HEAVY, filterFromSessionsParams, type FilterState, type TimeRange } from './filters'
import { fmtCost } from './format'
import { ruleWords } from './ruleWords'
import { oneLine, type BuiltTimeline, type Step } from './timeline'

// ---------------------------------------------------------------- words

/** "1 session", "3 sessions". */
export const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`

/** "one step" for 1, "2 steps" otherwise (the mockups' style for small counts in sentences). */
export const countWord = (n: number, one: string, many = `${one}s`) => (n === 1 ? `one ${one}` : `${n} ${many}`)

/** Joins clauses: "a", "a and b", "a, b, and c" (serial comma, as in the mockups). */
export function joinClauses(xs: string[]): string {
  if (xs.length <= 1) return xs.join('')
  if (xs.length === 2) return `${xs[0]} and ${xs[1]}`
  return `${xs.slice(0, -1).join(', ')}, and ${xs[xs.length - 1]}`
}

const cap = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : s)
const trimNum = (x: number, digits: number) => String(Number(x.toFixed(digits)))

/** A duration in words for sentences: "150 milliseconds", "1.3 seconds", "4 seconds", "2 minutes 5 seconds". */
export function durationWords(ms: number | null | undefined): string {
  if (ms == null || !Number.isFinite(ms) || ms < 0) return ''
  if (ms < 1000) return plural(Math.round(ms), 'millisecond')
  if (ms < 10_000) {
    const s = trimNum(ms / 1000, 1)
    return `${s} ${s === '1' ? 'second' : 'seconds'}`
  }
  if (ms < 60_000) return plural(Math.round(ms / 1000), 'second')
  const totalS = Math.round(ms / 1000)
  if (totalS < 3600) {
    const m = Math.floor(totalS / 60)
    const s = totalS % 60
    return s ? `${plural(m, 'minute')} ${plural(s, 'second')}` : plural(m, 'minute')
  }
  const totalM = Math.round(totalS / 60)
  const h = Math.floor(totalM / 60)
  const m = totalM % 60
  return m ? `${plural(h, 'hour')} ${plural(m, 'minute')}` : plural(h, 'hour')
}

/** A short duration for figures: "150 ms", "2.5 s", "9 s", "1 min 5 s", "2 h 3 min". */
export function shortDuration(ms: number | null | undefined): string {
  if (ms == null || !Number.isFinite(ms) || ms < 0) return '—'
  if (ms < 1000) return `${Math.round(ms)} ms`
  if (ms < 60_000) return `${trimNum(ms / 1000, ms < 10_000 ? 1 : 0)} s`
  const totalS = Math.round(ms / 1000)
  if (totalS < 3600) {
    const m = Math.floor(totalS / 60)
    const s = totalS % 60
    return s ? `${m} min ${s} s` : `${m} min`
  }
  const totalM = Math.round(totalS / 60)
  return totalM % 60 ? `${Math.floor(totalM / 60)} h ${totalM % 60} min` : `${Math.floor(totalM / 60)} h`
}

/** How far into a session something happened: "0.05 s", "1.3 s", "9 s", "1 min 5 s". */
export function offsetText(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) ms = 0
  if (ms < 1000) return `${trimNum(ms / 1000, 2)} s`
  return shortDuration(ms)
}

/** A relative time for list rows: "just now", "50 min ago", "2 h ago", "3 days ago". */
export function relativeTime(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return ''
  const diff = Math.max(0, now - new Date(iso).getTime())
  const m = Math.round(diff / 60_000)
  if (m < 1) return 'just now'
  if (m < 60) return `${m} min ago`
  const h = Math.round(m / 60)
  if (h < 48) return `${h} h ago`
  return plural(Math.round(h / 24), 'day') + ' ago'
}

/** "GoalCompletionJudge" → "goal completion"; "tool_safety" → "tool safety". */
export function scorerWords(name: string): string {
  return name
    .replace(/Judge$|Scorer$/, '')
    .replace(/[_.-]+/g, ' ')
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .trim()
    .toLowerCase()
}

/** A score as text (§7.2 rule 5): "goal completion 0.60". */
export function scoreText(name: string, v: { mean: number; count: number }): string {
  return `${scorerWords(name)} ${v.mean.toFixed(2)}`
}

/** True when the tool runs shell commands (its main argument is a command line). */
export function isCommandTool(tool: string | null | undefined): boolean {
  return /^(bash|sh|shell|zsh|exec|execute|execute_command|run_command|run_shell_command|terminal|command|cmd|powershell)$/i.test(tool ?? '')
}

// ---------------------------------------------------------------- one session

export interface RowContext {
  /** The server's cost spike threshold for the current range and filters (null: too few costs). */
  costThreshold?: number | null
  costFactor?: number
  /** The median known cost of the sessions in range, when every one of them is loaded. */
  medianCost?: number | null
  lowScore?: number
  approvalHeavy?: number
  /** This session's engine detections (block and require_approval), when loaded. */
  detections?: Detection[]
}

export interface SessionFlags {
  failed: boolean
  running: boolean
  unknown: boolean
  blocks: number
  approvals: number
  approvalHeavy: boolean
  /** Status ok, but a step failed that a block does not explain. */
  failedStep: boolean
  /** Failed steps not explained by blocks (error_count minus blocked tool calls, at least one when failed_step). */
  failedSteps: number
  lowScore: { name: string; mean: number; count: number } | null
  costSpike: boolean
}

export function sessionFlags(s: SessionSummary, ctx: RowContext = {}): SessionFlags {
  const blocks = s.detection_count?.block ?? 0
  const approvals = s.detection_count?.require_approval ?? 0
  const heavy = ctx.approvalHeavy ?? APPROVAL_HEAVY
  const low = ctx.lowScore ?? 0.5
  const status = String(s.status)
  const stepFailed = status === 'failed_step' || (status === 'ok' && (!!s.failed_step || s.error_count > 0))
  const unexplained = Math.max(0, s.error_count - blocks)
  let lowest: SessionFlags['lowScore'] = null
  for (const [name, v] of Object.entries(s.scores ?? {})) if (v.mean < low && (!lowest || v.mean < lowest.mean)) lowest = { name, ...v }
  return {
    failed: status === 'error',
    running: status === 'running',
    unknown: status === 'unknown' || !!s.verdicts_only,
    blocks,
    approvals,
    approvalHeavy: approvals >= heavy,
    failedStep: stepFailed && (unexplained > 0 || blocks === 0),
    failedSteps: stepFailed ? Math.max(1, unexplained) : 0,
    lowScore: lowest,
    costSpike: ctx.costThreshold != null && s.cost_usd != null && s.cost_usd >= ctx.costThreshold,
  }
}

/** The status-filter meaning of "went on after a failed step" (the server's failed_step), for clause membership. */
const serverFailedStep = (s: SessionSummary) => String(s.status) === 'failed_step' || (String(s.status) === 'ok' && !!s.failed_step)

/** Sessions that need a look: failed, blocked, approval-heavy, a failed step, a low score, or a cost spike. */
export function needsALook(s: SessionSummary, ctx: RowContext = {}): boolean {
  const f = sessionFlags(s, ctx)
  return f.failed || f.blocks > 0 || f.approvalHeavy || serverFailedStep(s) || !!f.lowScore || f.costSpike
}

export type RowGlyph = 'failed' | 'blocked' | 'approval' | 'warning' | 'cost' | 'ok' | 'running' | 'unknown'

/** The glyph of a session row, most serious first. */
export function rowGlyph(s: SessionSummary, ctx: RowContext = {}): { kind: RowGlyph; label: string } {
  const f = sessionFlags(s, ctx)
  if (f.failed) return { kind: 'failed', label: 'Failed' }
  if (f.blocks > 0) return { kind: 'blocked', label: 'Blocked by AgentShield' }
  if (f.approvalHeavy) return { kind: 'approval', label: 'Needs approval' }
  if (serverFailedStep(s)) return { kind: 'warning', label: 'Finished with a failed step' }
  if (f.lowScore) return { kind: 'warning', label: 'Low score' }
  if (f.costSpike) return { kind: 'cost', label: 'Cost spike' }
  if (f.running) return { kind: 'running', label: 'Running' }
  if (f.unknown) return { kind: 'unknown', label: 'Outcome unknown' }
  return { kind: 'ok', label: 'Finished' }
}

/** The session's title: its first request, else its name, else its id. */
export function sessionTitle(s: Pick<SessionSummary, 'title' | 'name' | 'session_id'>): string {
  return s.title?.trim() || s.name?.trim() || `Session ${s.session_id}`
}

/** A team worth naming (not one Weft made up from the agents). */
export function namedTeam(s: Pick<SessionSummary, 'team_id' | 'team_name'>): string | null {
  if (!s.team_id || s.team_id.startsWith('auto:')) return null
  return s.team_name || s.team_id
}

/** The row's meta line: "Code crew · coder, reviewer". */
export function sessionMetaLine(s: SessionSummary): string {
  const team = namedTeam(s)
  const agents = s.agents.length ? s.agents.join(', ') : s.verdicts_only ? 'no agent spans recorded' : 'no agents recorded'
  return team ? `${team} · ${agents}` : agents
}

/** Median of the known (positive) costs, as the server computes it for cost spikes; null with fewer than four. */
export function medianCost(sessions: Pick<SessionSummary, 'cost_usd'>[]): number | null {
  const xs = sessions.map((s) => s.cost_usd).filter((c): c is number => c != null && c > 0).sort((a, b) => a - b)
  if (xs.length < 4) return null
  const m = xs.length >> 1
  return xs.length % 2 ? xs[m] : (xs[m - 1] + xs[m]) / 2
}

function engineDets(ctx: RowContext, action: Detection['action']): Detection[] {
  return (ctx.detections ?? []).filter((d) => d.source !== 'replay' && d.action === action)
}

/** The most common rule among detections (ties: the first seen). */
function topRule(dets: Detection[]) {
  const counts = new Map<string, { n: number; d: Detection }>()
  for (const d of dets) {
    const r = d.rules[0]
    const k = r?.id ?? r?.title ?? ''
    const c = counts.get(k)
    if (c) c.n++
    else counts.set(k, { n: 1, d })
  }
  let best: { n: number; d: Detection } | undefined
  for (const c of counts.values()) if (!best || c.n > best.n) best = c
  return best ? { rule: best.d.rules[0], tool: best.d.tool_name, all: counts.size === 1 } : undefined
}

/**
 * The one line under a session row's title: what happened, in at most two
 * short sentences ("AgentShield blocked a script piped from the internet. The
 * session went on and finished."). Scores are mentioned when low, or when
 * nothing else is.
 */
export function sessionSummaryLine(s: SessionSummary, ctx: RowContext = {}): string {
  const f = sessionFlags(s, ctx)
  const dur = s.verdicts_only ? '' : durationWords(s.duration_ms)
  const out: string[] = []
  const scoreSentence = (sc: { name: string; mean: number; count: number }) =>
    sc.count > 1 ? `${cap(scorerWords(sc.name))} averaged ${sc.mean.toFixed(2)} over ${sc.count} results.` : `${cap(scorerWords(sc.name))} scored ${sc.mean.toFixed(2)}.`

  if (f.unknown) {
    out.push('Only AgentShield verdicts were recorded, so the outcome is unknown.')
    if (f.blocks) out.push(`AgentShield blocked ${countWord(f.blocks, 'tool call')}.`)
    else if (f.approvals) out.push(`AgentShield asked for approval ${countWord(f.approvals, 'time')}.`)
    return out.join(' ')
  }

  const blocked = engineDets(ctx, 'block')
  const asked = engineDets(ctx, 'require_approval')
  const blockWhat = () => {
    if (f.blocks !== 1) return `${f.blocks} tool calls`
    const top = topRule(blocked)
    return top ? ruleWords(top.rule, top.tool).noun : 'a tool call'
  }
  const approvalSentence = () => {
    const top = topRule(asked)
    const purpose = top && top.all ? ` ${ruleWords(top.rule, top.tool).purpose}` : ''
    let t = `Asked ${countWord(f.approvals, 'time')}${purpose}.`
    if (f.approvals === 1) t = `Asked once for approval${purpose}.`
    else if (!purpose) t = `Asked for approval ${f.approvals} times.`
    const overridden = asked.filter((d) => d.overridden).length
    if (overridden) t += f.approvals === 1 ? ' The user approved it.' : ` The user overrode ${overridden} of them.`
    return t
  }

  if (f.failed) {
    out.push(f.blocks ? `AgentShield blocked ${blockWhat()}, and the session failed${dur ? ` after ${dur}` : ''}.` : dur ? `Failed after ${dur}.` : 'Failed.')
    if (f.lowScore) out.push(scoreSentence(f.lowScore))
    else if (!f.blocks && f.approvalHeavy) out.push(approvalSentence())
    return out.join(' ')
  }
  if (f.running) {
    out.push(dur ? `Still running, ${dur} so far.` : 'Still running.')
    if (f.blocks) out.push(`AgentShield blocked ${blockWhat()}.`)
    return out.join(' ')
  }
  if (f.blocks) {
    out.push(`AgentShield blocked ${blockWhat()}.`)
    out.push(f.failedStep ? `The session went on and finished, but ${countWord(f.failedSteps, 'other step')} failed.` : 'The session went on and finished.')
    if (f.lowScore) out.push(scoreSentence(f.lowScore))
    return out.join(' ')
  }
  if (f.approvals) {
    out.push(approvalSentence())
    if (f.lowScore) out.push(scoreSentence(f.lowScore))
    return out.join(' ')
  }
  if (f.failedStep) {
    out.push(`Finished, but ${f.failedSteps === 1 ? 'one step' : `${f.failedSteps} steps`} failed on the way.`)
    if (f.lowScore) out.push(scoreSentence(f.lowScore))
    return out.join(' ')
  }
  if (f.costSpike && s.cost_usd != null) {
    const ratio = ctx.medianCost ? s.cost_usd / ctx.medianCost : null
    out.push(
      ratio && ratio >= 1.5
        ? `Finished. Cost ${fmtCost(s.cost_usd)}, about ${ratio < 10 ? Math.round(ratio) : Math.round(ratio / 5) * 5} times the median session.`
        : `Finished. Cost ${fmtCost(s.cost_usd)}, at least ${ctx.costFactor ?? 3} times the median session.`,
    )
    if (f.lowScore) out.push(scoreSentence(f.lowScore))
    return out.join(' ')
  }
  out.push(dur ? `Finished in ${dur}.` : 'Finished.')
  const scores = Object.entries(s.scores ?? {}).sort((a, b) => a[1].mean - b[1].mean)
  if (f.lowScore) out.push(scoreSentence(f.lowScore))
  else if (scores.length) out.push(scoreSentence({ name: scores[0][0], ...scores[0][1] }))
  return out.join(' ')
}

// ---------------------------------------------------------------- the Sessions sentence

export type ClauseId = 'failed' | 'blocked' | 'approval' | 'failed_step' | 'low_score' | 'cost'

/** A part of a generated sentence: text, or a clause that links to the sessions it counts. */
export type SummaryPart =
  | string
  | {
      text: string
      clause: ClauseId
      count: number
      /** The filter (laid over the page's own) that lists exactly these sessions. */
      filter: FilterState
      /** When the clause counts one session and it is loaded: that session, and where to open it. */
      session?: { id: string; step?: string; tab?: string }
      /** False when the filter lists more than the clause counts (see sessionsSentence). */
      exact: boolean
    }

const RANGE_PHRASE: Record<TimeRange, string> = {
  '1h': 'in the last hour',
  '24h': 'in the last 24 hours',
  '7d': 'in the last 7 days',
  '30d': 'in the last 30 days',
  all: '',
  custom: 'in this time range',
}

export interface SessionsSentenceInput {
  attention: SessionAttention
  /** The range the list shows (after any widening). */
  time: TimeRange
  /** Filters other than time are applied. */
  filtered: boolean
  /** The sessions loaded for the list (the same filters, newest first). */
  rows: SessionSummary[]
  /** The list's total (all matching sessions). */
  total: number
}

/**
 * The Sessions page sentence: how many sessions ran, then what needs a look,
 * each clause linking to the sessions it counts. Counts come from the
 * server's attention endpoint over every matching session. "Everything else
 * finished" is said only when every matching session is loaded and it is
 * true of them.
 */
export function sessionsSentence({ attention: a, time, filtered, rows, total }: SessionsSentenceInput): SummaryPart[] {
  const n = a.sessions
  const range = RANGE_PHRASE[time] ?? ''
  const verb = filtered ? (n === 1 ? 'matches these filters' : 'match these filters') : 'ran'
  if (n === 0) return [`No sessions ${filtered ? 'match these filters' : 'ran'}${range ? ` ${range}` : ''}.`]
  const head = `${plural(n, 'session')} ${verb}${range ? ` ${range}` : ''}.`

  const card = (id: SessionAttention['items'][number]['id']) => a.items.find((c) => c.id === id)
  const low = a.low_score_threshold ?? 0.5
  const heavy = a.approval_heavy_threshold || APPROVAL_HEAVY
  const ctx: RowContext = { costThreshold: a.cost_spike_threshold, lowScore: low, approvalHeavy: heavy }
  const security = card('security')
  const member: Record<ClauseId, (s: SessionSummary) => boolean> = {
    failed: (s) => String(s.status) === 'error',
    blocked: (s) => (s.detection_count?.block ?? 0) > 0,
    approval: (s) => (s.detection_count?.require_approval ?? 0) >= heavy,
    failed_step: serverFailedStep,
    low_score: (s) => !!sessionFlags(s, ctx).lowScore,
    cost: (s) => sessionFlags(s, ctx).costSpike,
  }
  const step: Partial<Record<ClauseId, { step?: string; tab?: string }>> = {
    failed: { step: 'error' },
    blocked: { step: 'problem' },
    approval: { step: 'problem' },
    failed_step: { step: 'error' },
    low_score: { tab: 'scores' },
  }
  const clause = (id: ClauseId, count: number, words: [string, string], filter: FilterState, exact = true): SummaryPart | null => {
    if (!count) return null
    const text = n === 1 ? words[0] : `${count} ${count === 1 ? words[0] : words[1]}`
    let session: { id: string; step?: string; tab?: string } | undefined
    if (count === 1) {
      const hits = rows.filter(member[id])
      if (hits.length === 1) session = { id: hits[0].session_id, ...step[id] }
    }
    return { text, clause: id, count, filter, session, exact }
  }
  const failed = card('failed')
  const severe = [
    clause('failed', failed?.count ?? 0, ['failed', 'failed'], filterFromSessionsParams(failed?.filter ?? { status: 'error' })),
    clause('blocked', a.blocked, ['was blocked by AgentShield', 'were blocked by AgentShield'], { has: 'block' }),
    // Approval-heavy sessions have no filter of their own: "blocked or approval-heavy" lists exactly
    // them when nothing was blocked, and also the blocked ones otherwise.
    clause('approval', a.approval_heavy, ['kept asking for approval', 'kept asking for approval'], filterFromSessionsParams(security?.filter ?? { has_detection: 'flagged' }), a.blocked === 0),
  ].filter(Boolean) as Exclude<SummaryPart, string>[]
  const step2 = card('failed_step')
  const lowCard = card('low_score')
  const costCard = card('cost')
  const mild = [
    clause('failed_step', step2?.count ?? 0, ['went on after a failed step', 'went on after a failed step'], filterFromSessionsParams(step2?.filter ?? { status: 'failed_step' })),
    clause('low_score', lowCard?.count ?? 0, [`scored below ${low}`, `scored below ${low}`], filterFromSessionsParams(lowCard?.filter ?? { score_below: String(low) })),
    a.cost_spike_threshold != null && costCard && Object.keys(costCard.filter).length
      ? clause('cost', costCard.count, [`cost at least ${a.cost_spike_factor} times the median session`, `cost at least ${a.cost_spike_factor} times the median session`], filterFromSessionsParams(costCard.filter))
      : null,
  ].filter(Boolean) as Exclude<SummaryPart, string>[]

  const parts: SummaryPart[] = [head]
  const sentence = (cl: Exclude<SummaryPart, string>[], lead?: string) => {
    if (!cl.length) return
    parts.push(' ')
    if (lead) parts.push(lead)
    cl.forEach((c, i) => {
      if (i > 0) parts.push(cl.length === 2 ? ' and ' : i === cl.length - 1 ? ', and ' : ', ')
      parts.push(c)
    })
    parts.push('.')
  }
  if (n === 1) {
    sentence([...severe, ...mild], 'It ')
  } else {
    sentence(severe)
    sentence(mild)
  }

  // The rest, when every matching session is loaded.
  if (rows.length === total && total === n) {
    const counted = (s: SessionSummary) => (Object.keys(member) as ClauseId[]).some((id) => member[id](s) && (id !== 'cost' || a.cost_spike_threshold != null))
    const rest = rows.filter((s) => !counted(s))
    if (rest.length) {
      const running = rest.filter((s) => String(s.status) === 'running').length
      const unknown = rest.filter((s) => String(s.status) === 'unknown' || s.verdicts_only).length
      const finished = rest.length - running - unknown
      const bits: string[] = []
      if (running) bits.push(`${running} ${running === 1 ? 'is' : 'are'} still running`)
      if (unknown) bits.push(`${unknown} ${unknown === 1 ? 'has' : 'have'} only AgentShield verdicts`)
      if (finished) bits.push(bits.length ? 'the rest finished' : severe.length + mild.length ? 'everything else finished' : n === 1 ? 'it finished' : 'all of them finished')
      if (bits.length) parts.push(' ', cap(joinClauses(bits)) + '.')
    }
  }
  return parts
}

// ---------------------------------------------------------------- one session's outcome

function errorLine(text: string | undefined): string {
  return oneLine((text ?? '').split('\n')[0], 120).replace(/[.\s]+$/, '')
}

/** "the coder's Bash call", "the researcher's web_search call". */
function whoseCall(s: Step): string {
  const p = s.item.payload ?? {}
  const who = s.agent ? `the ${s.agent}'s ` : 'a '
  if (s.type === 'llm') return `${who}call to ${p.model ?? 'the model'}`
  if (s.type === 'handoff') return `the hand-off from ${p.from_agent} to ${p.to_agent}`
  return `${who}${p.tool_name ?? s.detections[0]?.tool_name ?? 'tool'} call`
}

/** "ran “npm i -D eslint”" or "called web_search". */
function didWhat(s: Step): string {
  const p = s.item.payload ?? {}
  const arg = typeof p.arguments === 'string' ? p.arguments.trim() : ''
  if (isCommandTool(p.tool_name) && arg) return `ran “${oneLine(arg, 60)}”`
  return `called ${p.tool_name ?? 'a tool'}`
}

const okStatus = (s: Step) => !s.problem && String(s.item.payload?.status ?? 'OK').toUpperCase() !== 'ERROR'

/** What the agent did after a problem step: the hand-off it made, and the tool call that then worked. */
function recovery(tl: BuiltTimeline, p: Step): string {
  const i = tl.steps.indexOf(p)
  if (i < 0 || !p.agent) return ''
  const after = tl.steps.slice(i + 1)
  const t = (x: string | null | undefined) => (x ? new Date(x).getTime() : NaN)
  // A tool call that handed work to another agent (delegate_*) is the hand-off, not the fix.
  const delegates = (s: Step) =>
    after.some((h) => {
      if (h.type !== 'handoff' || h.item.payload?.kind === 'return' || (h.item.payload?.from_agent ?? h.agent) !== s.agent) return false
      const at = t(h.item.started_at)
      return at >= t(s.item.started_at) && at <= t(s.item.ended_at)
    })
  const fixed = after.find((s) => s.type === 'tool_call' && s.agent === p.agent && okStatus(s) && !delegates(s))
  const asked = after.find(
    (s) => s.type === 'handoff' && s.item.payload?.kind !== 'return' && s.item.payload?.from_agent === p.agent && (!fixed || tl.steps.indexOf(s) < tl.steps.indexOf(fixed)),
  )
  const to = asked?.item.payload?.to_agent
  if (fixed) return asked && to ? `After asking the ${to}, the ${p.agent} ${didWhat(fixed)}, which worked.` : `The ${p.agent} then ${didWhat(fixed)}, which worked.`
  if (asked && to) return `The ${p.agent} then asked the ${to} for help.`
  return ''
}

export interface OutcomeInput {
  summary: SessionSummary
  tl: BuiltTimeline
  /** The session's detections (engine and replay; replays are ignored). */
  detections?: Detection[]
}

/** The session page's outcome sentence: how it ended, what AgentShield did, what failed and what happened next. */
export function sessionOutcome({ summary: s, tl, detections = [] }: OutcomeInput): string {
  const engine = detections.filter((d) => d.source !== 'replay')
  const blockedSteps = tl.steps.filter((x) => x.problem === 'block')
  const blocks = blockedSteps.length || (s.detection_count?.block ?? 0)
  const approvals = engine.filter((d) => d.action === 'require_approval')
  const approvalCount = approvals.length || (s.detection_count?.require_approval ?? 0)
  const errors = tl.problems.filter((p) => p.problem === 'error')
  const dur = durationWords(s.duration_ms)
  const status = String(s.status)
  const out: string[] = []

  if (status === 'unknown' || s.verdicts_only) {
    out.push('Only AgentShield verdicts were recorded for this session, so its outcome is unknown.')
    if (blocks) out.push(`AgentShield blocked ${countWord(blocks, 'tool call')}.`)
    else if (approvalCount) out.push(`AgentShield asked for approval ${countWord(approvalCount, 'time')}.`)
    return out.join(' ')
  }
  if (status === 'running') {
    out.push(dur ? `Still running, ${dur} so far.` : 'Still running.')
    if (blocks) out.push(`AgentShield has blocked ${countWord(blocks, 'step')} so far.`)
    return out.join(' ')
  }
  if (status === 'error') {
    out.push(blocks ? `Failed${dur ? ` after ${dur}` : ''}, after AgentShield blocked ${countWord(blocks, 'step')}.` : dur ? `Failed after ${dur}.` : 'Failed.')
    const cause = errors[0]
    if (cause) {
      const msg = errorLine(cause.errorText ?? cause.item.payload?.message)
      const onTurn = cause.type === 'turn' || (cause.type === 'error' && tl.steps.some((t) => t.type === 'turn' && t.item.span_id === cause.item.span_id))
      if (onTurn) out.push(msg ? `It ended with an error: ${msg}.` : '')
      else if (cause.type === 'error') out.push(`${cap(String(cause.item.payload?.name || 'a step'))} failed${msg ? `: ${msg}` : ''}.`)
      else out.push(`${cap(whoseCall(cause))} failed${msg ? `: ${msg}` : ''}.`)
    }
    return out.filter(Boolean).join(' ')
  }
  // Finished (ok, or went on after a failed step).
  if (blocks) {
    out.push(`Finished, after AgentShield blocked ${countWord(blocks, 'step')}.`)
    const first = blockedSteps[0]
    const r = first ? recovery(tl, first) : ''
    if (r) out.push(r)
    const other = errors.filter((e) => e.problem === 'error')
    if (other.length) out.push(`${cap(countWord(other.length, 'other step'))} failed on the way.`)
    return out.join(' ')
  }
  if (approvalCount) {
    out.push(`Finished, after asking for approval ${countWord(approvalCount, 'time')}.`)
    const overridden = approvals.filter((d) => d.overridden).length
    if (overridden) out.push(approvalCount === 1 ? 'The user approved it.' : `The user overrode ${overridden} of them.`)
    if (errors.length) out.push(`${cap(countWord(errors.length, 'step'))} failed on the way.`)
    return out.join(' ')
  }
  if (errors.length) {
    const first = errors[0]
    const msg = errorLine(first.errorText ?? first.item.payload?.message)
    out.push(`Finished, but ${whoseCall(first)} failed on the way${msg ? ` (${msg})` : ''}.`)
    const r = recovery(tl, first)
    if (r) out.push(r)
    if (errors.length > 1) out.push(`${cap(countWord(errors.length - 1, 'other step'))} failed too.`)
    return out.join(' ')
  }
  out.push(dur ? `Finished in ${dur}.` : 'Finished.')
  return out.join(' ')
}

/** The session page's quiet meta line: team · agents · started · took · cost · scores. */
export function sessionMetaFigures(s: SessionSummary, now = Date.now()): string {
  const bits: string[] = []
  const team = namedTeam(s)
  if (team) bits.push(team)
  if (s.agents.length) bits.push(joinClauses(s.agents).replace(/, and /, ' and '))
  bits.push(`started ${relativeTime(s.started_at, now)}`)
  if (s.duration_ms != null && !s.verdicts_only) bits.push(`took ${shortDuration(s.duration_ms)}`)
  if (s.cost_usd != null) bits.push(`cost ${fmtCost(s.cost_usd)}`)
  for (const [k, v] of Object.entries(s.scores ?? {})) bits.push(scoreText(k, v))
  return bits.join(' · ')
}
