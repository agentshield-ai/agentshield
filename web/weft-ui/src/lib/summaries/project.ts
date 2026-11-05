// Generated words for the Project pages: Automations, Settings (every tab),
// Users & teams, and the Projects list. DESIGN.md §7.1 rule 1, §7.2 rule 8:
// pure functions of what the API returned, so every figure is exact.
// Table-driven tests: project.test.ts.
import type { AutomationAction, AutomationRun, AutomationTrigger } from '../../api/automations'
import { joinClauses, plural, relativeTime } from '../summaries'

const DAY = 86_400_000
const pct = (x: number) => `${Math.round(x * 1000) / 10}%`

// ---------------------------------------------------------------- automations

const OP_WORDS: Record<string, string> = { lt: 'below', lte: 'at or below', gt: 'above', gte: 'at or above', eq: 'equal to', ne: 'not equal to' }

/** A trigger in words: "polite scores below 0.5", "more than 10% of runs fail within 60 minutes". */
export function triggerWords(raw: AutomationTrigger & { op_names?: string[]; min_calls?: number }): string {
  switch (raw.type) {
    case 'score_threshold': {
      const s = `${raw.scorer || 'a scorer'}${raw.path ? ` (${raw.path})` : ''}`
      return `${s} scores ${OP_WORDS[raw.operator ?? ''] ?? raw.operator ?? '?'} ${raw.threshold ?? '?'}`
    }
    case 'error_rate': {
      const names = raw.names ?? raw.op_names
      const what = names?.length ? `runs of ${joinClauses(names)}` : 'runs'
      return `at least ${pct(raw.threshold ?? 0)} of ${what} fail within ${plural(raw.window_minutes ?? 60, 'minute')}`
    }
    case 'feedback_created':
      return raw.feedback_types?.length ? `feedback of type ${joinClauses(raw.feedback_types)} arrives` : 'any feedback arrives'
  }
  return 'its trigger fires'
}

/** Where an action delivers: "Slack (hooks.slack.com)", "a webhook at example.com", "a webhook whose address is in the secret HOOK_URL". */
export function actionWords(a: AutomationAction): string {
  const what = a.type === 'slack' ? 'Slack' : 'a webhook'
  if (a.url_secret) return `${what}, at the address in the secret ${a.url_secret}`
  let host = a.url ?? ''
  try {
    host = new URL(a.url ?? '').host
  } catch {
    /* keep the text */
  }
  return host ? (a.type === 'slack' ? `Slack (${host})` : `a webhook at ${host}`) : what
}

/** An automation's row summary: "When polite scores below 0.5, post to Slack (hooks.slack.com)." */
export function automationLine(v: { trigger?: AutomationTrigger; action?: AutomationAction }): string {
  const when = v.trigger ? triggerWords(v.trigger) : 'its trigger fires'
  const then = v.action ? `${v.action.type === 'slack' ? 'post to' : 'call'} ${actionWords(v.action)}` : 'do nothing'
  return `When ${when}, ${then}.`
}

/** The last real run of an automation, in words: "Last delivered 5 min ago." */
export function lastRunLine(run: Pick<AutomationRun, 'status' | 'created_at' | 'response_status'> | undefined, now = Date.now()): string {
  if (!run) return 'Has not fired yet.'
  return run.status === 'success'
    ? `Last delivered ${relativeTime(run.created_at, now)}.`
    : `Last delivery failed ${relativeTime(run.created_at, now)}${run.response_status ? ` (HTTP ${run.response_status})` : ''}.`
}

export interface AutomationsSentenceInput {
  automations: { name: string; enabled: boolean }[]
  runs: Pick<AutomationRun, 'status' | 'test' | 'created_at'>[]
  /** True when the runs list is the API's newest page only (older runs exist). */
  capped?: boolean
  now?: number
}

/** "2 automations, 1 on. In the last 24 hours they fired 3 times; 1 delivery failed." */
export function automationsSentence({ automations, runs, capped, now = Date.now() }: AutomationsSentenceInput): string {
  const n = automations.length
  if (!n) return 'No automations yet.'
  const on = automations.filter((a) => a.enabled).length
  let s = `${plural(n, 'automation')}, ${on === n ? (n === 1 ? 'on' : n === 2 ? 'both on' : 'all on') : on === 0 ? (n === 1 ? 'off' : 'all off') : `${on} on`}.`
  const day = runs.filter((r) => !r.test && now - Date.parse(r.created_at) <= DAY)
  // When the page of runs is full and every one is from today, the count is a lower bound.
  const atLeast = capped && day.length === runs.length && runs.length > 0
  const they = n === 1 ? 'it' : 'they'
  if (!day.length) return `${s} ${cap(they)} did not fire in the last 24 hours.`
  const failed = day.filter((r) => r.status !== 'success').length
  s += ` In the last 24 hours ${they} fired ${atLeast ? 'at least ' : ''}${day.length === 1 ? 'once' : plural(day.length, 'time')}`
  s += failed ? `; ${failed === day.length ? (failed === 1 ? 'the delivery failed' : 'every delivery failed') : `${plural(failed, 'delivery', 'deliveries')} failed`}.` : ', and every delivery went through.'
  return s
}

const cap = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : s)

// ---------------------------------------------------------------- settings

export interface StatsInput {
  span_count: number
  trace_count: number
  object_count: number
  feedback_count: number
  storage_size_bytes: number
}

/** "agents/demo holds 3 traces (12 spans), 24 saved assets and 4 pieces of feedback, 1.2 MB in all." */
export function generalSentence(projectId: string, s: StatsInput, bytes: (n: number) => string): string {
  if (!s.span_count && !s.object_count && !s.feedback_count) return `${projectId} is empty: nothing has been sent to it yet.`
  const parts = [`${plural(s.trace_count, 'trace')} (${plural(s.span_count, 'span')})`, plural(s.object_count, 'saved asset'), s.feedback_count ? `${s.feedback_count} ${s.feedback_count === 1 ? 'piece' : 'pieces'} of feedback` : 'no feedback']
  return `${projectId} holds ${joinClauses(parts)}, ${bytes(s.storage_size_bytes)} in all.`
}

/** "12 model prices: 1 set for this project, 11 built in." */
export function costsSentence(prices: { pricing_level: string }[]): string {
  if (!prices.length) return 'No model prices: costs are not computed.'
  const own = prices.filter((p) => p.pricing_level === 'project').length
  const built = prices.length - own
  if (!own) return `${plural(prices.length, 'model price')}, all built in. Add a price to set your own.`
  return `${plural(prices.length, 'model price')}: ${own} set for this project${built ? `, ${built} built in` : ''}.`
}

/** "2 of 6 provider keys are set, 1 from the server's environment. 1 custom provider." */
export function secretsSentence(names: string[], set: { name: string; source: string }[], providers: number): string {
  const byName = new Map(set.map((x) => [x.name, x]))
  const isSet = names.filter((n) => byName.has(n))
  const env = isSet.filter((n) => byName.get(n)!.source === 'env').length
  let s = `${isSet.length} of ${plural(names.length, 'provider key')} ${isSet.length === 1 ? 'is' : 'are'} set${env ? `, ${env} from the server's environment` : ''}.`
  s += providers ? ` ${plural(providers, 'custom provider')}.` : ''
  return s
}

/** "2 annotation fields: quality and correct." */
export function annotationsSentence(names: string[]): string {
  if (!names.length) return 'No annotation fields yet: reviewers can react and write notes, but not score.'
  return `${plural(names.length, 'annotation field')}: ${joinClauses(names)}.`
}

/** "Spans are kept forever." / "Spans older than 30 days are deleted every hour." */
export function retentionSentence(days: number | null | undefined): string {
  if (!days) return 'Spans and their feedback are kept forever.'
  return `Spans older than ${plural(days, 'day')} are deleted every hour, with their feedback. Datasets and other saved assets are kept.`
}

/** "Every trace is kept." / "40% of traces are kept." */
export function samplingSentence(rate: number, dryRun: boolean): string {
  if (rate >= 1) return 'Every trace is kept.'
  if (dryRun) return `Every trace is kept, and the ${pct(1 - rate)} that sampling would drop are marked: a dry run.`
  return rate <= 0 ? 'No trace is kept: everything sent to this project is dropped.' : `${pct(rate)} of traces are kept; the rest are dropped as they arrive.`
}

/** "Sensitive data is stored as sent." / "Sensitive data is replaced with placeholders before it is stored, by 8 of 9 detectors and 1 custom pattern." */
export function sensitiveSentence(policy: string, enabled: number, total: number, patterns: number): string {
  if (policy === 'off') return 'Sensitive data is stored as sent.'
  const by = [`${enabled === total ? `all ${total}` : `${enabled} of ${total}`} detectors`]
  if (patterns) by.push(plural(patterns, 'custom pattern'))
  return `Sensitive data is replaced with placeholders before it is stored, by ${joinClauses(by)}.`
}

/** "3 API keys; 1 has not been used in 90 days." */
export function apiKeysSentence(keys: { revoked_at: string | null; last_used_at: string | null; created_at: string; admin: boolean }[], authRequired: boolean, now = Date.now()): string {
  const live = keys.filter((k) => !k.revoked_at)
  if (!live.length) return authRequired ? 'No managed API keys; the server uses the keys it was started with.' : 'No API keys: the server is open to anyone who can reach it.'
  const stale = live.filter((k) => now - Date.parse(k.last_used_at ?? k.created_at) > 90 * DAY).length
  const never = live.filter((k) => !k.last_used_at).length
  const admins = live.filter((k) => k.admin).length
  const clauses: string[] = []
  if (admins) clauses.push(`${admins === live.length && live.length > 1 ? 'all are' : `${admins} ${admins === 1 ? 'is' : 'are'}`} admin`)
  if (stale) clauses.push(`${stale} ${stale === 1 ? 'has' : 'have'} not been used in 90 days`)
  else if (never) clauses.push(`${never} ${never === 1 ? 'has' : 'have'} never been used`)
  return `${plural(live.length, 'API key')}${clauses.length ? `; ${joinClauses(clauses)}` : ''}.`
}

/** "4 people have roles on agents/demo: 1 admin, 2 members and 1 viewer." */
export function membersSentence(projectId: string, members: { role: string; disabled: boolean }[]): string {
  const live = members.filter((m) => !m.disabled)
  if (!live.length) return `Nobody has a role on ${projectId} yet; server admins and the server's own keys still have access.`
  const by = (r: string) => live.filter((m) => m.role === r).length
  const parts = (
    [
      ['admin', by('admin')],
      ['member', by('member')],
      ['viewer', by('viewer')],
    ] as [string, number][]
  )
    .filter(([, n]) => n)
    .map(([r, n]) => plural(n, r))
  const who = live.length === 1 ? '1 person has a role' : `${live.length} people have roles`
  return `${who} on ${projectId}: ${joinClauses(parts)}.`
}

/** "124 entries match; entries are kept for 90 days." */
export function auditSentence(total: number, retentionDays: number, filtered: boolean): string {
  const head = total === 0 ? (filtered ? 'No entries match these filters' : 'No entries yet') : `${plural(total, 'entry', 'entries')} ${filtered ? (total === 1 ? 'matches these filters' : 'match these filters') : 'recorded'}`
  return `${head}. Entries are kept ${retentionDays > 0 ? `for ${plural(retentionDays, 'day')}` : 'forever'}.`
}

/** "No buckets are registered." / "2 buckets are registered: 1 for this project and 1 for team acme." */
export function bucketsSentence(buckets: { scope: string; entity?: string }[]): string {
  if (!buckets.length) return 'No storage buckets are registered, so media that lives in S3 or Cloud Storage shows as a link.'
  const own = buckets.filter((b) => b.scope === 'project').length
  const team = buckets.length - own
  const parts: string[] = []
  if (own) parts.push(`${own} for this project`)
  if (team) parts.push(`${team} for its team`)
  return `${plural(buckets.length, 'bucket')} ${buckets.length === 1 ? 'is' : 'are'} registered, ${joinClauses(parts)}; media stored there shows inline.`
}

// ---------------------------------------------------------------- users, teams, projects

/** "5 users; 1 is a server admin and 1 is disabled." */
export function usersSentence(users: { admin: boolean; disabled_at: string | null }[]): string {
  if (!users.length) return 'No users yet.'
  const admins = users.filter((u) => u.admin && !u.disabled_at).length
  const off = users.filter((u) => u.disabled_at).length
  const clauses: string[] = []
  if (admins) clauses.push(`${admins} ${admins === 1 ? 'is a server admin' : 'are server admins'}`)
  if (off) clauses.push(`${off} ${off === 1 ? 'is' : 'are'} disabled`)
  return `${plural(users.length, 'user')}${clauses.length ? `; ${joinClauses(clauses)}` : ''}.`
}

/** "3 teams with 7 members in all." */
export function teamsSentence(teams: { members: unknown[] }[]): string {
  if (!teams.length) return 'No teams yet.'
  const m = teams.reduce((s, t) => s + t.members.length, 0)
  return `${plural(teams.length, 'team')} with ${plural(m, 'member')} in all.`
}

/** "4 projects. The most recently active is agents/demo, 5 min ago." */
export function projectsSentence(projects: { project_id: string; last_activity: string | null }[], now = Date.now()): { lead: string; latest: string | null; tail: string } {
  if (!projects.length) return { lead: 'No projects yet. A project is created when the first trace or asset is written to it.', latest: null, tail: '' }
  const active = projects.filter((p) => p.last_activity)
  if (!active.length) return { lead: `${plural(projects.length, 'project')}, none active yet.`, latest: null, tail: '' }
  const latest = active.reduce((a, b) => ((b.last_activity ?? '') > (a.last_activity ?? '') ? b : a))
  return { lead: `${plural(projects.length, 'project')}. The most recently active is `, latest: latest.project_id, tail: `, ${relativeTime(latest.last_activity, now)}.` }
}

/** A project row's line: "3 traces, 24 saved assets". */
export function projectLine(p: { trace_count: number; object_count: number }): string {
  return `${plural(p.trace_count, 'trace')}, ${plural(p.object_count, 'saved asset')}`
}
