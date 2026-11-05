// Generated words for the asset pages (Datasets, Models, Prompts, Scorers,
// Functions, All objects; one object's versions, one version, a comparison).
// DESIGN.md §7.1 rules 1 and 2, §7.2 rule 8: pure functions of what the API
// returned, so every figure is exact; plain words ("version 3", "50 rows",
// "used by 2 evaluations"), with "object", "ref" and "digest" left to the
// inspector and the raw view. Table-driven tests: assets.test.ts.
import type { Any, WeftObject } from '../../api/types'
import { fmtDuration, fmtRelative, versionLabel } from '../format'
import { joinClauses, plural, relativeTime } from '../summaries'
import { metricText, scoreMetrics } from './evaluate'

/** The list pages (mirrors components/objects/objectUtils ObjectPageKind). */
export type AssetKind = 'datasets' | 'models' | 'prompts' | 'scorers' | 'ops' | 'all'

/** A sentence part: text, or a name the page links to that asset. */
export type AssetPart = string | { text: string; name: string; version?: string }

export const ASSET_NOUN: Record<AssetKind, { one: string; many: string }> = {
  datasets: { one: 'dataset', many: 'datasets' },
  models: { one: 'model', many: 'models' },
  prompts: { one: 'prompt', many: 'prompts' },
  scorers: { one: 'scorer', many: 'scorers' },
  ops: { one: 'traced function', many: 'traced functions' },
  all: { one: 'asset', many: 'assets' },
}

type Obj = Pick<WeftObject, 'name' | 'kind' | 'class' | 'value' | 'version' | 'created_at' | 'aliases'> & { version_count?: number; user_id?: string | null; is_latest?: boolean }

/** "version 3" for API version 2 (format.ts versionLabel: people count from 1). */
export const versionWords = (o: Pick<WeftObject, 'version'>) => versionLabel(o.version)
/** The SDK's name for a version: "support-tickets:v2". Only for the inspector's "In code" row and code snippets. */
export const sdkName = (o: Pick<WeftObject, 'name' | 'version'>) => `${o.name}:v${o.version}`

function valueClass(v: Any): string | null {
  if (!v || typeof v !== 'object') return null
  return (v._class_name as string) ?? (v._type as string) ?? null
}

const CLASS_WORDS: Record<string, string> = {
  Dataset: 'dataset',
  StringPrompt: 'text prompt',
  MessagesPrompt: 'chat prompt',
  Prompt: 'prompt',
  LLMStructuredCompletionModel: 'model the server can run',
  Model: 'model',
  Evaluation: 'evaluation',
  LLMAsAJudgeScorer: 'LLM judge',
  ExactMatchScorer: 'exact-match scorer',
  ContainsScorer: 'contains scorer',
  RegexScorer: 'pattern scorer',
  JSONValidScorer: 'valid-JSON scorer',
  LevenshteinScorer: 'similarity scorer',
  Scorer: 'scorer',
  Leaderboard: 'leaderboard',
  Monitor: 'monitor',
  AnnotationSpec: 'annotation field',
  SavedView: 'saved view',
  Provider: 'model provider',
  ProviderModel: 'provider model',
  Dashboard: 'dashboard',
  Automation: 'automation',
}
const KIND_WORDS: Record<string, string> = {
  function: 'function',
  dataset: 'dataset',
  model: 'model',
  evaluation: 'evaluation',
  scorer: 'scorer',
  prompt: 'prompt',
  leaderboard: 'leaderboard',
  monitor: 'monitor',
  annotation_spec: 'annotation field',
  saved_view: 'saved view',
  provider: 'model provider',
  provider_model: 'provider model',
  dashboard: 'dashboard',
  automation: 'automation',
}

/** What an asset is, in plain words: "dataset", "chat prompt", "LLM judge", "function". */
export function kindWords(o: Pick<WeftObject, 'kind' | 'class' | 'value'>): string {
  if (o.kind === 'function') return 'function'
  const cls = o.class ?? valueClass(o.value)
  if (cls && CLASS_WORDS[cls]) return CLASS_WORDS[cls]
  if (cls && cls !== 'Object' && cls !== 'BaseModel')
    return cls
      .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
      .replace(/[_.-]+/g, ' ')
      .toLowerCase()
  return KIND_WORDS[o.kind] ?? 'saved value'
}

/** The {placeholders} of a template, in order of first use. */
export function placeholders(...texts: (string | null | undefined)[]): string[] {
  const out: string[] = []
  for (const t of texts) {
    if (!t) continue
    for (const m of t.matchAll(/\{([A-Za-z_][\w.]*)\}/g)) if (!out.includes(m[1])) out.push(m[1])
  }
  return out
}

function promptTexts(v: Any): string[] {
  if (Array.isArray(v?.messages)) return v.messages.map((m: Any) => (typeof m?.content === 'string' ? m.content : ''))
  return typeof v?.content === 'string' ? [v.content] : []
}

/** "2 messages and 1 placeholder" for a prompt; "" when it is neither shape. */
export function promptShape(v: Any): string {
  const vars = placeholders(...promptTexts(v))
  const parts: string[] = []
  if (Array.isArray(v?.messages)) parts.push(plural(v.messages.length, 'message'))
  if (Array.isArray(v?.messages) || typeof v?.content === 'string') parts.push(vars.length ? plural(vars.length, 'placeholder') : 'no placeholders')
  return joinClauses(parts)
}

/** The model a judge or a completion model runs, as text ("" when it is a ref or unknown). */
function modelName(v: Any): string {
  if (typeof v?.llm_model_id === 'string') return v.llm_model_id
  if (typeof v?.model === 'string' && !v.model.startsWith('weft:///')) return v.model
  return ''
}

/** What one asset does, for the row's summary: "50 rows, version 3, used by 2 evaluations". */
export interface RowFacts {
  /** Row count of a dataset's table, when known. */
  rows?: number | null
  /** Evaluations whose dataset (or scorers, or model) refer to this asset. */
  usedBy?: number
  /** True when the server can run this scorer (monitors, server-side evaluations). */
  serverScorer?: boolean
}

export function assetFacts(o: Obj, f: RowFacts = {}): string {
  const v = o.value ?? {}
  const parts: string[] = []
  const cls = o.class ?? valueClass(v)
  if (o.kind === 'dataset' || cls === 'Dataset') {
    if (f.rows != null) parts.push(plural(f.rows, 'row'))
  } else if (o.kind === 'prompt') {
    const shape = promptShape(v)
    parts.push(shape ? `${kindWords(o)} with ${shape}` : kindWords(o))
  } else if (o.kind === 'model') {
    const m = modelName(v)
    const t = v?.default_params?.temperature
    parts.push(m ? `runs ${m}${typeof t === 'number' ? ` at temperature ${t}` : ''}` : kindWords(o))
  } else if (o.kind === 'scorer') {
    const m = cls === 'LLMAsAJudgeScorer' ? modelName(v) : ''
    parts.push(m ? `LLM judge using ${m}` : typeof v?.target_column === 'string' && v.target_column ? `${kindWords(o)} on ${v.target_column}` : kindWords(o))
    if (f.serverScorer === false) parts.push('runs only in the SDK')
  } else if (o.kind === 'evaluation') {
    const n = Array.isArray(v?.scorers) ? v.scorers.length : 0
    const ds = refName(v?.dataset)
    parts.push(`scores ${ds ?? 'each row'} with ${plural(n, 'scorer')}`)
  } else if (o.kind !== 'function' && kindWords(o) !== KIND_WORDS[o.kind]) {
    // The group heading already names the plain kind ("Leaderboards"); say only a more exact one.
    parts.push(kindWords(o))
  }
  parts.push(versionWords({ version: (o.version_count ?? o.version + 1) - 1 }))
  if (f.usedBy) parts.push(`used by ${plural(f.usedBy, 'evaluation')}`)
  const aliases = (o.aliases ?? []).filter((a) => a !== 'latest')
  if (aliases.length) parts.push(`${aliases.length === 1 ? 'alias' : 'aliases'} ${aliases.join(', ')}`)
  return cap(parts.join(', '))
}

/** The asset name a ref points at ("support-questions"), or null. */
function refName(ref: unknown): string | null {
  if (typeof ref !== 'string') return null
  const m = /^weft:\/\/\/[^/]+\/[^/]+\/(?:object|op|function)\/([^:/]+)/.exec(ref)
  return m ? decodeURIComponent(m[1]) : null
}

const cap = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : s)

const clip = (s: string, n: number) => {
  const one = s.replace(/\s+/g, ' ').trim()
  return one.length > n ? one.slice(0, n - 1).trimEnd() + '…' : one
}

/** A short readable line from the value itself: the description, a prompt's text, a function's signature. */
export function assetPreview(o: Pick<WeftObject, 'kind' | 'value' | 'name'>, n = 120): string {
  const v = o.value
  if (!v || typeof v !== 'object') return typeof v === 'string' ? clip(v, n) : ''
  if (typeof v.description === 'string' && v.description.trim()) return clip(v.description, n)
  if (o.kind === 'function') {
    if (typeof v.signature === 'string') return clip(`${v.name ?? o.name}${v.signature}`, n)
    if (typeof v.code === 'string') {
      const doc = /"""([\s\S]*?)"""/.exec(v.code)?.[1]
      if (doc?.trim()) return clip(doc, n)
      const first = v.code.split('\n').find((l: string) => l.trim())
      return first ? clip(first, n) : ''
    }
    return ''
  }
  if (typeof v.content === 'string') return clip(v.content, n)
  if (Array.isArray(v.messages) && v.messages.length) {
    const last = v.messages[v.messages.length - 1]
    return typeof last?.content === 'string' ? clip(last.content, n) : ''
  }
  if (typeof v.scoring_prompt === 'string') return clip(v.scoring_prompt, n)
  return ''
}

/** How many evaluations name each asset (by name) as their dataset, model or a scorer. */
export function usageCounts(evaluations: Pick<WeftObject, 'value'>[]): Map<string, number> {
  const out = new Map<string, number>()
  const nameOf = (ref: unknown) => {
    if (typeof ref !== 'string') return null
    const m = /^weft:\/\/\/[^/]+\/[^/]+\/(?:object|op|function)\/([^:/]+)/.exec(ref)
    return m ? decodeURIComponent(m[1]) : null
  }
  for (const e of evaluations) {
    const v = e.value ?? {}
    const names = new Set<string>()
    for (const r of [v.dataset, v.model, ...(Array.isArray(v.scorers) ? v.scorers : [])]) {
      const n = nameOf(r)
      if (n) names.add(n)
    }
    for (const n of names) out.set(n, (out.get(n) ?? 0) + 1)
  }
  return out
}

export interface ListSentenceInput {
  kind: AssetKind
  objects: Obj[]
  /** Row counts of datasets by name (datasets page); missing names: not loaded yet. */
  rows?: Map<string, number>
  /** True per scorer name when the server can run it (scorers page). */
  serverScorer?: (o: Obj) => boolean
  now?: number
}

/** The sentence under a list page's title, e.g. "2 datasets with 96 rows in all. The latest change was to support-tickets, 5 min ago." */
export function assetsSentence({ kind, objects, rows, serverScorer, now = Date.now() }: ListSentenceInput): AssetPart[] {
  const noun = ASSET_NOUN[kind]
  const n = objects.length
  if (n === 0) return [`No ${noun.many} yet.`]
  const head = plural(n, noun.one, noun.many)
  const out: AssetPart[] = []
  if (kind === 'datasets' && rows && objects.every((o) => rows.has(o.name))) {
    const total = objects.reduce((s, o) => s + (rows.get(o.name) ?? 0), 0)
    out.push(n === 1 ? `1 dataset with ${plural(total, 'row')}.` : `${head} with ${plural(total, 'row')} in all.`)
  } else if (kind === 'models') {
    const runnable = objects.filter((o) => o.class === 'LLMStructuredCompletionModel' || valueClass(o.value) === 'LLMStructuredCompletionModel').length
    out.push(`${head}; ${runnable === 0 ? 'the server can run none of them' : runnable === n ? (n === 1 ? 'the server can run it' : n === 2 ? 'the server can run both' : 'the server can run all of them') : `the server can run ${runnable}`}.`)
  } else if (kind === 'scorers' && serverScorer) {
    const sdk = objects.filter((o) => !serverScorer(o)).length
    out.push(
      `${head}; ${
        sdk === 0
          ? n === 1
            ? 'it can run on the server'
            : n === 2
              ? 'both can run on the server'
              : 'all can run on the server'
          : sdk === n
            ? n === 1
              ? 'it runs only in the SDK'
              : 'they run only in the SDK'
            : `${sdk} ${sdk === 1 ? 'runs' : 'run'} only in the SDK`
      }.`,
    )
  } else if (kind === 'all') {
    // One kind per group the page shows (its headings: Datasets, Scorers, …), not per class.
    const kinds = new Set(objects.map((o) => o.kind))
    out.push(`${head} of ${plural(kinds.size, 'kind')}.`)
  } else {
    out.push(`${head}.`)
  }
  const latest = objects.reduce((a, b) => (b.created_at > a.created_at ? b : a))
  out.push(' The latest change was to ', { text: latest.name, name: latest.name }, `, ${relativeTime(latest.created_at, now)}.`)
  return out
}

/** The sentence on an asset's versions page: "support-tickets has 3 versions; the latest, version 3, was saved 5 min ago." */
export function versionsSentence(name: string, versions: Obj[], now = Date.now()): AssetPart[] {
  if (!versions.length) return [`${name} was not found in this project.`]
  const latest = versions.reduce((a, b) => (b.version > a.version ? b : a))
  const out: AssetPart[] = []
  if (versions.length === 1) out.push(`${name} has one version, saved ${relativeTime(latest.created_at, now)}.`)
  else out.push(`${name} has ${versions.length} versions; the latest, `, { text: versionWords(latest), name, version: 'latest' }, `, was saved ${relativeTime(latest.created_at, now)}.`)
  const aliased = versions
    .slice()
    .sort((a, b) => a.version - b.version)
    .flatMap((v) => (v.aliases ?? []).filter((a) => a !== 'latest').map((a) => `“${a}” points to ${versionWords(v)}`))
  if (aliased.length) out.push(` ${joinClauses(aliased)}.`)
  return out
}

export interface VersionSentenceInput {
  o: Obj
  /** Every version's number, to say whether a newer one exists. */
  latestVersion?: number | null
  rows?: number | null
  /** Row count of the version before, when there is one. */
  prevRows?: number | null
  now?: number
}

/** The sentence on one version: "Version 3 of support-tickets, saved 2 days ago. It has 50 rows, 4 more than version 2." */
export function versionSentence({ o, latestVersion, rows, prevRows, now = Date.now() }: VersionSentenceInput): AssetPart[] {
  const v = o.value ?? {}
  const out: AssetPart[] = [`${cap(versionWords(o))} of ${o.name}, saved ${relativeTime(o.created_at, now)}${o.user_id ? ` by ${o.user_id}` : ''}.`]
  if (latestVersion != null && latestVersion > o.version) out.push(' A newer version, ', { text: versionWords({ version: latestVersion }), name: o.name, version: 'latest' }, ', exists.')
  if (rows != null) {
    let s = ` It has ${plural(rows, 'row')}`
    if (prevRows != null && o.version > 0) {
      const d = rows - prevRows
      const prev = versionWords({ version: o.version - 1 })
      s += d === 0 ? `, as many as ${prev}` : `, ${Math.abs(d)} ${d > 0 ? 'more' : 'fewer'} than ${prev}`
    }
    out.push(s + '.')
  } else if (o.kind === 'prompt') {
    const shape = promptShape(v)
    const vars = placeholders(...promptTexts(v))
    if (shape) out.push(` It has ${shape}${vars.length ? `: ${vars.map((x) => `{${x}}`).join(', ')}` : ''}.`)
  } else if (o.kind === 'model') {
    const m = modelName(v)
    if (m) out.push(` It runs ${m}.`)
  } else if (o.kind === 'scorer') {
    const m = modelName(v)
    out.push(` It is ${article(kindWords(o))}${m ? ` using ${m}` : ''}.`)
  } else if (o.kind === 'evaluation') {
    const n = Array.isArray(v.scorers) ? v.scorers.length : 0
    out.push(` It scores each row with ${plural(n, 'scorer')}${typeof v.trials === 'number' && v.trials > 1 ? `, ${v.trials} times` : ''}.`)
  }
  return out
}

const article = (w: string) => (/^[aeiou]/i.test(w) && !/^(one|user)/i.test(w) ? `an ${w}` : /^LLM/.test(w) ? `an ${w}` : `a ${w}`)

export interface DiffSentenceInput {
  a: Pick<WeftObject, 'version'>
  b: Pick<WeftObject, 'version'>
  same: boolean
  changed: number
  added: number
  removed: number
  /** Datasets: the row comparison, when done. */
  rows?: { a: number; b: number; added: number; removed: number } | null
}

/** "From version 2 to version 3, 1 value changed. The rows went from 46 to 50: 4 added, none removed." */
export function diffSentence({ a, b, same, changed, added, removed, rows }: DiffSentenceInput): string {
  if (same) return `Both sides are ${versionWords(b)}, so there is nothing to compare.`
  const clauses: string[] = []
  if (changed) clauses.push(`${plural(changed, 'value')} changed`)
  if (added) clauses.push(`${added} ${added === 1 ? 'was' : 'were'} added`)
  if (removed) clauses.push(`${removed} ${removed === 1 ? 'was' : 'were'} removed`)
  let s = `From ${versionWords(a)} to ${versionWords(b)}, ${clauses.length ? joinClauses(clauses) : 'no value changed'}.`
  if (rows) {
    const what = (n: number, verb: string) => (n === 0 ? `none ${verb}` : `${n} ${verb}`)
    s +=
      rows.added === 0 && rows.removed === 0
        ? ` The ${plural(rows.b, 'row')} are the same.`
        : ` The rows went from ${rows.a} to ${rows.b}: ${what(rows.added, 'added')}, ${what(rows.removed, 'removed')}.`
  }
  return s
}

// ---------------------------------------------------------------- runs on an asset's page

const QUESTION_KEYS = ['question', 'query', 'input', 'prompt', 'text', 'message', 'task', 'q']
const ANSWER_KEYS = ['answer', 'output', 'response', 'result', 'text', 'content', 'completion', 'message']
const isRefText = (x: unknown) => typeof x === 'string' && x.startsWith('weft:///')

function pickText(v: unknown, keys: string[]): string | null {
  if (typeof v === 'string') return v.trim() ? v : null
  if (!v || typeof v !== 'object' || Array.isArray(v)) return null
  const o = v as Record<string, unknown>
  for (const k of keys) if (typeof o[k] === 'string' && (o[k] as string).trim() && !isRefText(o[k])) return o[k] as string
  for (const [k, x] of Object.entries(o)) if (k !== 'self' && typeof x === 'string' && x.trim() && !isRefText(x)) return x
  return null
}

/** The title of a run row: the question it was asked ("Where is my order #4512?"), else its name. */
export function runTitle(inputs: unknown, fallback: string, n = 140): string {
  const t = pickText(inputs, QUESTION_KEYS)
  return t ? clip(t, n) : fallback
}

/** What the run answered, or how it failed: "Your order shipped yesterday.", "Failed: RateLimitError: 429 Too Many Requests". */
export function runAnswer(output: unknown, error?: string | null, n = 160): string {
  if (error) return clip(`Failed: ${error.split('\n')[0]}`, n)
  if (output == null) return 'No answer recorded.'
  const t = pickText(output, ANSWER_KEYS)
  if (t) return clip(t, n)
  if (typeof output === 'number' || typeof output === 'boolean') return String(output)
  try {
    return clip(JSON.stringify(output), n)
  } catch {
    return ''
  }
}

/** The time column of a run row: "3.4 s · 2 h ago" (no duration while it runs). */
export function runWhen(latencyMs: number | null | undefined, startedAt: string, now = Date.now()): string {
  return latencyMs == null ? fmtRelative(startedAt, now) : `${fmtDuration(latencyMs)} · ${fmtRelative(startedAt, now)}`
}

/** An evaluation run's scores as text: "exact match 0%, helpfulness judge score 2.75, mentions answer 100%". */
export function runScores(summary: unknown, max = 4): string {
  const ms = scoreMetrics(summary)
  if (!ms.length) return 'No scores.'
  const shown = ms.slice(0, max).map((m) => metricText(m.key, m.value))
  return cap(joinClauses(ms.length > max ? [...shown, `${ms.length - max} more`] : shown))
}
