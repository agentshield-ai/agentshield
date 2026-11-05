// Generated words for the Security page (DESIGN.md §7, §7.2 rule 8): the
// sentence that answers "what did AgentShield stop or question, and why?",
// the groups of detections by rule with each row's line, the quiet
// sentences about approval fatigue, override escalation and correlated
// agents, and the line of each replay job. Pure functions of what the API
// returns; table-driven tests in security.test.ts.
import type { Detection, DetectionStats, ReplayJob, ShieldInsights } from '../../api/v1'
import type { FilterState, TimeRange } from '../filters'
import { ruleWords } from '../ruleWords'
import { joinClauses, plural } from '../summaries'

/** A part of a generated sentence: text, or a clause that links somewhere. */
export type SecPart =
  | string
  | {
      text: string
      /** A filter laid over the page's own (lists what the clause counts). */
      filter?: FilterState
      /** One session, opened at a step. */
      session?: { id: string; step?: string }
    }

const RANGE: Record<TimeRange, string> = {
  '1h': 'in the last hour',
  '24h': 'in the last 24 hours',
  '7d': 'in the last 7 days',
  '30d': 'in the last 30 days',
  all: '',
  custom: 'in this time range',
}

/** "in the last 24 hours" (empty for all time). */
export const rangePhrase = (t: TimeRange) => RANGE[t] ?? ''

const times = (n: number) => (n === 1 ? 'once' : n === 2 ? 'twice' : `${n} times`)
const cap = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : s)

export interface SecuritySentenceInput {
  stats: Pick<DetectionStats, 'total' | 'by_action' | 'overridden'>
  time: TimeRange
  /** Filters other than time are applied. */
  filtered: boolean
}

/**
 * The page's sentence: what AgentShield blocked and asked approval for,
 * then what it only logged and how many verdicts were overridden. The
 * blocked and approval clauses link to those detections.
 */
export function securitySentence({ stats, time, filtered }: SecuritySentenceInput): SecPart[] {
  const blocked = stats.by_action.block ?? 0
  const asked = stats.by_action.require_approval ?? 0
  const logged = stats.by_action.log ?? 0
  const range = rangePhrase(time)
  const tail = `${range ? ` ${range}` : ''}${filtered ? ', among the tool calls these filters keep' : ''}.`
  const parts: SecPart[] = ['AgentShield ']
  if (blocked && asked)
    parts.push({ text: `blocked ${plural(blocked, 'tool call')}`, filter: { has: 'block' } }, ' and ', { text: `asked for approval ${times(asked)}`, filter: { has: 'approval' } }, tail)
  else if (blocked) parts.push({ text: `blocked ${plural(blocked, 'tool call')}`, filter: { has: 'block' } }, ` and asked for no approval${tail}`)
  else if (asked) parts.push('blocked nothing and ', { text: `asked for approval ${times(asked)}`, filter: { has: 'approval' } }, tail)
  else parts.push(`blocked nothing and asked for no approval${tail}`)
  if (logged) parts.push(` It logged ${blocked || asked ? `${logged} more` : plural(logged, 'tool call')} without stopping ${logged === 1 ? 'it' : 'them'}.`)
  const stopped = blocked + asked
  const over = stats.overridden ?? 0
  if (over > 0) {
    if (!stopped) parts.push(` ${plural(over, 'verdict')} ${over === 1 ? 'was' : 'were'} overridden.`)
    else if (over === 1) parts.push(stopped === 1 ? ' A user overrode it.' : ' A user overrode one of them.')
    else parts.push(over >= stopped ? ` Users overrode all ${stopped}.` : ` Users overrode ${over} of them.`)
  }
  return parts
}

/** A severity in words: "Critical", "Medium", "No severity". */
export function severityWords(level: string | null | undefined): string {
  const l = (level ?? '').toLowerCase()
  if (!l || l === 'none') return 'No severity'
  if (l === 'informational' || l === 'info') return 'Informational'
  return cap(l)
}

/** "Critical severity.", "No severity recorded." */
export function severityPhrase(level: string | null | undefined): string {
  const w = severityWords(level)
  return w === 'No severity' ? 'No severity recorded.' : `${w} severity.`
}

const ACTION_RANK: Record<string, number> = { block: 3, require_approval: 2, log: 1, allow: 0 }

export interface RuleGroup {
  /** The rule's id ('' when the verdict recorded no rule). */
  id: string
  title: string
  level: string
  /** The heading: the rule in plain words ("A script piped from the internet"), or its title. */
  heading: string
  /** One sentence: why AgentShield stepped in, then the severity. */
  reason: string
  /** The rule's own title, when the heading is the plain words (shown quietly). */
  ruleTitle: string | null
  count: number
  by_action: Partial<Record<string, number>>
  /** Newest first. */
  detections: Detection[]
}

/**
 * Detections grouped by the rule that decided them (each verdict's first
 * rule, the most severe), the groups that blocked first, then those that
 * asked for approval, then by count.
 */
export function ruleGroups(dets: Detection[]): RuleGroup[] {
  const m = new Map<string, RuleGroup>()
  for (const d of dets) {
    const r = d.rules[0]
    const id = r?.id ?? ''
    let g = m.get(id)
    if (!g) {
      const w = ruleWords(r, d.tool_name)
      const title = r?.title || r?.id || ''
      const level = r?.level || d.severity || ''
      g = {
        id,
        title,
        level,
        heading: r ? (w.known ? cap(w.noun) : title) : 'No rule recorded',
        reason: `${r && w.known ? `${w.reason} ` : ''}${severityPhrase(level)}`,
        ruleTitle: r && w.known ? title : null,
        count: 0,
        by_action: {},
        detections: [],
      }
      m.set(id, g)
    }
    g.count++
    g.by_action[d.action] = (g.by_action[d.action] ?? 0) + 1
    g.detections.push(d)
  }
  const rank = (g: RuleGroup) => Math.max(...Object.keys(g.by_action).map((a) => ACTION_RANK[a] ?? 0))
  const groups = [...m.values()]
  for (const g of groups) g.detections.sort((a, b) => b.created_at.localeCompare(a.created_at))
  return groups.sort((a, b) => rank(b) - rank(a) || b.count - a.count || a.heading.localeCompare(b.heading))
}

/** A group's count in words: "2 blocked, 1 needed approval". */
export function groupCounts(g: Pick<RuleGroup, 'by_action'>): string {
  const xs: string[] = []
  if (g.by_action.block) xs.push(`${g.by_action.block} blocked`)
  if (g.by_action.require_approval) xs.push(`${g.by_action.require_approval} needed approval`)
  if (g.by_action.log) xs.push(`${g.by_action.log} logged`)
  if (g.by_action.allow) xs.push(`${g.by_action.allow} allowed`)
  return xs.join(', ')
}

/** The tool call a verdict is about: "coder's Bash call", "a Bash call", "a tool call". */
export function callWords(d: Pick<Detection, 'agent_name' | 'tool_name'>): string {
  const tool = d.tool_name ? `${d.tool_name} call` : 'tool call'
  return d.agent_name ? `${d.agent_name}'s ${tool}` : `a ${tool}`
}

/** The glyph of a detection row. */
export function detectionGlyph(d: Pick<Detection, 'action'>): 'blocked' | 'approval' | 'warning' | 'ok' {
  return d.action === 'block' ? 'blocked' : d.action === 'require_approval' ? 'approval' : d.action === 'log' ? 'warning' : 'ok'
}

/** A detection row's line: what AgentShield did, and whether a user overrode it. */
export function detectionLine(d: Pick<Detection, 'action' | 'agent_name' | 'tool_name' | 'overridden'>): string {
  const call = callWords(d)
  let s: string
  if (d.action === 'block') s = `Blocked ${call}.`
  else if (d.action === 'require_approval') s = `Asked for approval before ${call}.`
  else if (d.action === 'log') s = `Logged ${call} without stopping it.`
  else s = `Allowed ${call}.`
  if (d.overridden) s += d.action === 'require_approval' ? ' A user approved it.' : ' A user overrode it.'
  return s
}

/** The quiet line under a detection row: other rules it matched, and whether it is linked to its step. */
export function detectionMeta(d: Pick<Detection, 'rules' | 'span_id'>): string {
  const xs: string[] = []
  const others = d.rules.slice(1).map((r) => `“${r.title || r.id}”`)
  if (others.length) xs.push(`Also matched ${joinClauses(others)}.`)
  if (!d.span_id) xs.push('Not linked to a recorded tool call; opens the session.')
  return xs.join(' ')
}

type FatigueRow = ShieldInsights['approval_fatigue']['by_session'][number]
type OverrideRow = ShieldInsights['override_escalation']['by_session'][number]
const titleOf = (s: { session_id: string; session_title?: string }) => {
  const t = s.session_title?.trim()
  if (!t) return `session ${s.session_id}`
  return t.length > 70 ? t.slice(0, 69) + '…' : t
}

/** The sessions approval fatigue counts (those flagged, or with 3 or more requests). */
export const tiredSessions = (rows: FatigueRow[]) => rows.filter((s) => s.fatigued || s.require_approval >= 3)
/** The sessions with overrides. */
export const overridingSessions = (rows: OverrideRow[]) => rows.filter((s) => s.escalating || s.overrides > 0)

const MAX_NAMED = 5

/** Session links joined as clauses, each followed by its own words, with "and N more". */
function sessionList<T extends { session_id: string; session_title?: string }>(xs: T[], after: (x: T) => string): SecPart[] {
  const shown = xs.slice(0, MAX_NAMED)
  const items: SecPart[][] = shown.map((x) => [{ text: titleOf(x), session: { id: x.session_id, step: 'problem' } }, after(x)])
  if (xs.length > shown.length) items.push([`${xs.length - shown.length} more`])
  const out: SecPart[] = []
  items.forEach((it, i) => {
    if (i > 0) out.push(i === items.length - 1 ? (items.length > 2 ? ', and ' : ' and ') : ', ')
    out.push(...it)
  })
  return out
}

/** Approval fatigue in one or two sentences, or null when no session asked often. */
export function fatigueSentence(rows: FatigueRow[], threshold: number): SecPart[] | null {
  const xs = tiredSessions(rows).sort((a, b) => b.require_approval - a.require_approval)
  if (!xs.length) return null
  const parts: SecPart[] = []
  if (xs.length === 1) {
    const s = xs[0]
    parts.push(`One session asked for approval ${times(s.require_approval)}`)
    if (s.approved) parts.push(`, and ${s.approved} ${s.approved === 1 ? 'was' : 'were'} approved`)
    parts.push(': ', { text: titleOf(s), session: { id: s.session_id, step: 'problem' } }, '.')
  } else {
    parts.push(`${xs.length} sessions asked for approval again and again: `, ...sessionList(xs, (s) => ` (${times(s.require_approval)})`), '.')
  }
  if (xs.some((s) => s.fatigued)) parts.push(` At ${threshold} or more requests in a session, people tend to approve without reading.`)
  return parts
}

/** Override escalation in one or two sentences, or null when nobody overrode a verdict. */
export function overrideSentence(rows: OverrideRow[]): SecPart[] | null {
  const xs = overridingSessions(rows).sort((a, b) => b.overrides - a.overrides)
  if (!xs.length) return null
  const what = (s: OverrideRow) =>
    joinClauses([s.overridden_blocks ? plural(s.overridden_blocks, 'block') : '', s.overridden_approvals ? plural(s.overridden_approvals, 'approval request') : ''].filter(Boolean))
  const parts: SecPart[] = []
  if (xs.length === 1) {
    const s = xs[0]
    const w = what(s)
    parts.push(`A user overrode AgentShield ${times(s.overrides)}${w ? ` (${w})` : ''} in `, { text: titleOf(s), session: { id: s.session_id, step: 'problem' } }, '.')
  } else {
    parts.push(`Users overrode AgentShield in ${xs.length} sessions: `, ...sessionList(xs, (s) => ` (${times(s.overrides)})`), '.')
  }
  const esc = xs.filter((s) => s.escalating).length
  if (esc) parts.push(xs.length === 1 ? ' Overrides that keep coming like this are a sign of escalation.' : ` In ${esc === xs.length ? (esc === 2 ? 'both' : `all ${esc}`) : esc} they keep coming, a sign of escalation.`)
  return parts
}

/** One sentence per team where the same rules fired for several agents close together. */
export function crossAgentSentences(rows: ShieldInsights['cross_agent']): string[] {
  return rows
    .filter((c) => c.correlated)
    .map((c) => {
      const rules = c.shared_rules.map((r) => `“${r.title || r.rule_id}”`)
      return `The same ${rules.length === 1 ? 'rule' : 'rules'} fired for ${joinClauses(c.agents_with_detections)} close together in time, ${times(c.incidents.length)}${rules.length ? `: ${joinClauses(rules)}` : ''}.`
    })
}

/** A replay job's glyph. */
export function replayGlyph(j: Pick<ReplayJob, 'status'>): 'running' | 'failed' | 'ok' {
  return j.status === 'failed' ? 'failed' : j.status === 'done' ? 'ok' : 'running'
}

/** A replay job's line: what the current rules would change. */
export function replayLine(j: Pick<ReplayJob, 'status' | 'summary' | 'error'>): string {
  if (j.status === 'queued') return 'Waiting to start.'
  if (j.status === 'running') return 'Replaying the recorded tool calls…'
  if (j.status === 'failed') return `Failed${j.error ? `: ${j.error.split('\n')[0]}` : '.'}`
  const s = j.summary
  if (!s) return 'Finished.'
  const head = `Checked ${s.evaluated === s.tool_calls ? plural(s.tool_calls, 'tool call') : `${s.evaluated} of ${plural(s.tool_calls, 'tool call')}`}`
  const changes = [s.newly_blocked ? `${s.newly_blocked} newly blocked` : '', s.no_longer_blocked ? `${s.no_longer_blocked} no longer blocked` : '', s.changed ? `${s.changed} changed otherwise` : ''].filter(
    Boolean,
  )
  return changes.length ? `${head}: ${joinClauses(changes)}.` : `${head}; nothing would change.`
}

/** A replay job's quiet meta line: the rules it used. */
export function replayMeta(j: Pick<ReplayJob, 'ruleset' | 'rules_loaded' | 'session_ids'>): string {
  const xs = [j.ruleset ? `${j.ruleset}${j.rules_loaded != null ? ` (${plural(j.rules_loaded, 'rule')})` : ''}` : '']
  if (j.session_ids?.length) xs.push(plural(j.session_ids.length, 'session'))
  return xs.filter(Boolean).join(' · ')
}

/** "92% of verdicts are linked to the exact tool call." */
export function linkedSentence(st: Pick<DetectionStats, 'total' | 'linked'>): string {
  if (!st.total) return ''
  const pct = Math.round((st.linked / st.total) * 100)
  return `${pct}% of these verdicts are linked to the exact tool call; the rest stay on their session.`
}
