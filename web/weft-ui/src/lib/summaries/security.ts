// Generated words for the Security page (DESIGN.md §7, §7.2 rule 8): the
// sentence that answers "what did AgentShield stop or question, and why?",
// the groups of detections by rule with each row's line, the quiet
// sentences about approval fatigue, override escalation and correlated
// agents, and the line of each replay job. Pure functions of what the API
// returns; table-driven tests in security.test.ts.
import type { Detection, DetectionStats, ReplayJob, ShieldInsights } from '../../api/v1'
import type { FilterState, TimeRange } from '../filters'
import { ruleWords } from '../ruleWords'
import { isCommandTool, joinClauses, plural } from '../summaries'
import { oneLine } from '../timeline'

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
  /**
   * Of the overridden verdicts, the approval requests a user allowed and the
   * blocks a user went past, when every detection is in hand (counted from
   * the list). Without them the sentence says only how many were let through.
   */
  split?: { approved: number; overrodeBlocks: number }
}

/** "the request", "all 5 requests", "3 of those requests". */
const approvedWhat = (approved: number, asked: number) => (approved >= asked ? (asked === 1 ? 'the request' : `all ${asked} requests`) : `${approved} of those requests`)

/**
 * The page's sentence: what AgentShield blocked and asked approval for,
 * then what it only logged, how many approval requests users approved and
 * how many blocks they overrode (an approval a user allowed is "approved";
 * "overrode" is only for going past a block). The blocked and approval
 * clauses link to those detections.
 */
export function securitySentence({ stats, time, filtered, split }: SecuritySentenceInput): SecPart[] {
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
  const over = stats.overridden ?? 0
  if (over <= 0) return parts
  // Without the split, an override with only approval requests (or only blocks) is still exact.
  const s = split ?? (!blocked ? { approved: over, overrodeBlocks: 0 } : !asked ? { approved: 0, overrodeBlocks: over } : null)
  if (!s) {
    parts.push(` Users approved or overrode ${over} of them.`)
    return parts
  }
  const a = Math.min(s.approved, asked)
  const b = Math.min(s.overrodeBlocks, blocked)
  const who = a + b === 1 ? 'A user' : 'Users'
  const blocksWhat = b >= blocked ? (blocked === 1 ? 'the block' : `all ${blocked} blocks`) : `${b} of the blocks`
  if (a && b) parts.push(` ${who} approved ${approvedWhat(a, asked)} and overrode ${b >= blocked ? blocksWhat : plural(b, 'block')}.`)
  else if (a) parts.push(` ${who} approved ${approvedWhat(a, asked)}.`)
  else if (b) parts.push(` ${who} overrode ${blocksWhat}.`)
  else if (!blocked && !asked) parts.push(` ${plural(over, 'verdict')} ${over === 1 ? 'was' : 'were'} overridden.`)
  return parts
}

/** The approval requests users approved and the blocks they overrode, among these detections. */
export function overrideSplit(dets: Pick<Detection, 'action' | 'overridden'>[]): { approved: number; overrodeBlocks: number } {
  let approved = 0
  let overrodeBlocks = 0
  for (const d of dets) {
    if (!d.overridden) continue
    if (d.action === 'require_approval') approved++
    else if (d.action === 'block') overrodeBlocks++
  }
  return { approved, overrodeBlocks }
}

/**
 * A rule title in sentence case: "Remote Code Execution via Piped Script
 * Download" → "Remote code execution via piped script download". Words with
 * more than one capital (SSH, GitHub, MCP) keep their case.
 */
export function sentenceCase(title: string): string {
  const words = title.trim().split(/(\s+)/)
  let first = true
  return words
    .map((w) => {
      if (!w.trim()) return w
      const caps = (w.match(/[A-Z]/g) ?? []).length
      const out = caps > 1 ? w : first ? w[0].toUpperCase() + w.slice(1) : w.toLowerCase()
      first = false
      return out
    })
    .join('')
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
        heading: r ? (w.known ? cap(w.noun) : (r.title ? sentenceCase(r.title) : title)) : 'No rule recorded',
        reason: `${r && w.known ? `${w.reason} ` : ''}${severityPhrase(level)}`,
        ruleTitle: r && w.known ? (r.title ? sentenceCase(r.title) : title) : null,
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

/** A group's count by action, only when it mixes actions (one kind is the rows' glyph and the heading's count). */
export function groupMix(g: Pick<RuleGroup, 'by_action'>): string {
  return Object.values(g.by_action).filter((n) => n).length > 1 ? groupCounts(g) : ''
}

/** The tool call a verdict is about: "coder's Bash call", "a Bash call", "a tool call". */
export function callWords(d: Pick<Detection, 'agent_name' | 'tool_name'>): string {
  const tool = d.tool_name ? `${d.tool_name} call` : 'tool call'
  return d.agent_name ? `${d.agent_name}'s ${tool}` : `a ${tool}`
}

/** What a tool call did, in three forms: "read ~/.ssh/config" / "reading …" / "read …". */
export interface ToolTarget {
  /** After "to": "run “ls”". */
  inf: string
  /** After "from": "running “ls”". */
  ger: string
  /** After a subject: "ran “ls”". */
  past: string
}

const ARG_ATTRS = ['gen_ai.tool.call.arguments', 'gen_ai.tool.input', 'tool_arguments', 'ai.toolCall.args', 'ai.toolCall.input', 'tool_input']

const parseMaybe = (v: unknown): unknown => {
  if (typeof v !== 'string' || !/^\s*[[{]/.test(v)) return v
  try {
    return JSON.parse(v)
  } catch {
    return v
  }
}

/**
 * A tool call's arguments from its span: the arguments attribute when set,
 * else the inputs (unwrapping an `arguments` / `args` / `input` object).
 * The same places the server's replay reads.
 */
export function spanToolArgs(span: { inputs?: Record<string, unknown> | null; attributes?: Record<string, unknown> | null }): unknown {
  for (const k of ARG_ATTRS) {
    const v = span.attributes?.[k]
    if (v != null && v !== '') return parseMaybe(v)
  }
  const inputs = Object.fromEntries(Object.entries(span.inputs ?? {}).filter(([k]) => k !== 'self'))
  for (const k of ['arguments', 'args', 'params', 'tool_input', 'input']) {
    if (k in inputs && Object.keys(inputs).length === 1) return parseMaybe(inputs[k])
  }
  return Object.keys(inputs).length ? inputs : null
}

const str = (v: unknown) => (typeof v === 'string' && v.trim() ? v.trim() : '')
const pick = (args: unknown, keys: string[]) => {
  if (typeof args === 'string') return args.trim()
  if (!args || typeof args !== 'object') return ''
  for (const k of keys) {
    const v = str((args as Record<string, unknown>)[k])
    if (v) return v
  }
  return ''
}

const verb = (inf: string, ger: string, past: string, obj: string): ToolTarget => ({ inf: `${inf} ${obj}`, ger: `${ger} ${obj}`, past: `${past} ${obj}` })

/**
 * What a tool call did, named from its arguments: "read ~/.ssh/config",
 * "ran “curl … | sh”", "fetched https://…". Null when the tool or its
 * arguments say nothing a person would recognise.
 */
export function toolTarget(tool: string | null | undefined, args: unknown): ToolTarget | null {
  const t = (tool ?? '').toLowerCase()
  const path = () => oneLine(pick(args, ['file_path', 'path', 'filename', 'file', 'notebook_path', 'target']), 60)
  if (isCommandTool(tool)) {
    const c = oneLine(pick(args, ['command', 'cmd', 'script', 'code']), 60)
    return c ? verb('run', 'running', 'ran', `“${c}”`) : null
  }
  if (/^(read|read_file|readfile|view|cat|open_file)$/.test(t)) return path() ? verb('read', 'reading', 'read', path()) : null
  if (/^(write|write_file|writefile|create_file|createfile)$/.test(t)) return path() ? verb('write', 'writing', 'wrote', path()) : null
  if (/^(edit|multiedit|edit_file|str_replace|str_replace_editor|apply_patch|notebookedit)$/.test(t)) return path() ? verb('edit', 'editing', 'edited', path()) : null
  if (/^(webfetch|web_fetch|fetch|http_request|http_get|browse|open_url)$/.test(t)) {
    const u = oneLine(pick(args, ['url', 'uri', 'href']), 60)
    return u ? verb('fetch', 'fetching', 'fetched', u) : null
  }
  if (/^(websearch|web_search|search|google_search)$/.test(t)) {
    const q = oneLine(pick(args, ['query', 'q', 'search']), 60)
    return q ? verb('search the web for', 'searching the web for', 'searched the web for', `“${q}”`) : null
  }
  if (/^(glob|grep|find|ls|list_files)$/.test(t)) {
    const q = oneLine(pick(args, ['pattern', 'query', 'path']), 60)
    return q ? verb('search for', 'searching for', 'searched for', `“${q}”`) : null
  }
  return null
}

/** The glyph of a detection row. */
export function detectionGlyph(d: Pick<Detection, 'action'>): 'blocked' | 'approval' | 'warning' | 'ok' {
  return d.action === 'block' ? 'blocked' : d.action === 'require_approval' ? 'approval' : d.action === 'log' ? 'warning' : 'ok'
}

/**
 * A detection row's line: what AgentShield did to what, and whether a user
 * approved the request or overrode the block. With the call's target:
 * "Asked before coder read ~/.ssh/config."; without: "Asked for approval
 * before coder's Read call."
 */
export function detectionLine(d: Pick<Detection, 'action' | 'agent_name' | 'tool_name' | 'overridden'>, target?: ToolTarget | null): string {
  let s: string
  if (target) {
    const who = d.agent_name || 'the agent'
    if (d.action === 'block') s = `Blocked ${who} from ${target.ger}.`
    else if (d.action === 'require_approval') s = `Asked before ${who} ${target.past}.`
    else if (d.action === 'log') s = `Logged ${who} ${target.ger} without stopping it.`
    else s = `Allowed ${who} to ${target.inf}.`
  } else {
    const call = callWords(d)
    if (d.action === 'block') s = `Blocked ${call}.`
    else if (d.action === 'require_approval') s = `Asked for approval before ${call}.`
    else if (d.action === 'log') s = `Logged ${call} without stopping it.`
    else s = `Allowed ${call}.`
  }
  if (d.overridden) s += d.action === 'require_approval' ? ' A user approved it.' : ' A user overrode it.'
  return s
}

/** The quiet line under a detection row: other rules it matched, and whether it is linked to its step. */
export function detectionMeta(d: Pick<Detection, 'rules' | 'span_id'>): string {
  const xs: string[] = []
  const others = d.rules.slice(1).map((r) => `“${r.title ? sentenceCase(r.title) : r.id}”`)
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
/** The sessions where a user overrode a block (an approved request is not an override). */
export const overridingSessions = (rows: OverrideRow[]) => rows.filter((s) => s.overridden_blocks > 0)

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

/**
 * Where the approval requests piled up, in one sentence, or null when no
 * session asked often. The counts are the header sentence's; this says only
 * which sessions and why that matters. `asked` is the page's total.
 */
export function fatigueSentence(rows: FatigueRow[], threshold: number, asked?: number): SecPart[] | null {
  const xs = tiredSessions(rows).sort((a, b) => b.require_approval - a.require_approval)
  if (!xs.length) return null
  const parts: SecPart[] = []
  if (xs.length === 1) {
    const s = xs[0]
    parts.push(s.require_approval === asked ? 'Every approval request came from one session, ' : `${s.require_approval} of the approval requests came from one session, `)
    parts.push({ text: titleOf(s), session: { id: s.session_id, step: 'problem' } }, '.')
  } else {
    parts.push(`${xs.length} sessions asked again and again: `, ...sessionList(xs, (s) => ` (${times(s.require_approval)})`), '.')
  }
  if (xs.some((s) => s.fatigued)) parts.push(` At ${threshold} or more requests in a session, people tend to approve without reading.`)
  return parts
}

/** Where users overrode blocks, in one sentence, or null when nobody did. */
export function overrideSentence(rows: OverrideRow[]): SecPart[] | null {
  const xs = overridingSessions(rows).sort((a, b) => b.overridden_blocks - a.overridden_blocks)
  if (!xs.length) return null
  const parts: SecPart[] = []
  if (xs.length === 1) {
    const s = xs[0]
    const n = s.overridden_blocks
    parts.push(n === 1 ? 'The block a user overrode was in ' : `The ${n} blocks users overrode were in `, { text: titleOf(s), session: { id: s.session_id, step: 'problem' } }, '.')
  } else {
    parts.push(`Users overrode blocks in ${xs.length} sessions: `, ...sessionList(xs, (s) => ` (${times(s.overridden_blocks)})`), '.')
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
      const rules = c.shared_rules.map((r) => `“${r.title ? sentenceCase(r.title) : r.rule_id}”`)
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
  if (!s) return 'Finished; open it to see what would change.'
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
