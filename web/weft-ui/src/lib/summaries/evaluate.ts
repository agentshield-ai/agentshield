// Generated words for the Evaluate pages (DESIGN.md §7.1 rule 1, §7.2 rule
// 8): Evaluations, an evaluation run and its comparison, Scores,
// Leaderboards, Review queues and the Playground. Pure functions of the data
// the API returns, never a model, so they are exact: every number is one the
// API gave or one counted here from its rows. Table-driven tests:
// evaluate.test.ts.
import { joinClauses, plural, scorerWords, shortDuration } from '../summaries'

// ---------------------------------------------------------------- parts

/** What a clause of a sentence opens; the page turns it into a link or an action. */
export type EvalLink =
  | { kind: 'compare'; ids: string[]; baseline?: string }
  | { kind: 'runs'; status: 'error' | 'running' }
  | { kind: 'examples'; filter: ExampleFilter }
  | { kind: 'scores'; filter: 'low' }
  | { kind: 'queue'; id: string }

export type EvalPart = string | { text: string; link: EvalLink }

export type ExampleFilter = 'all' | 'failed' | 'regressed' | 'improved' | 'disagree'

/** The plain text of a sentence (tests, titles). */
export const partsText = (parts: EvalPart[]) => parts.map((p) => (typeof p === 'string' ? p : p.text)).join('')

const cap = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : s)

/** Joins clauses that may be links: "a", "a and b", "a, b, and c" (serial comma, as joinClauses). */
export function joinParts(xs: EvalPart[]): EvalPart[] {
  const out: EvalPart[] = []
  xs.forEach((x, i) => {
    if (i > 0) out.push(i === xs.length - 1 ? (xs.length > 2 ? ', and ' : ' and ') : ', ')
    out.push(x)
  })
  return out
}

// ---------------------------------------------------------------- metrics

export type MetricKind = 'fraction' | 'mean' | 'other'
export type Leaf = number | boolean

/** Flattens nested numeric and boolean leaves into dotted keys ("exact.true_fraction"); keys starting with "_" are skipped. */
export function flatten(v: unknown, prefix = '', out: Record<string, Leaf> = {}): Record<string, Leaf> {
  if (typeof v === 'number' || typeof v === 'boolean') {
    if (typeof v === 'boolean' || Number.isFinite(v)) out[prefix || 'value'] = v
    return out
  }
  if (v && typeof v === 'object' && !Array.isArray(v))
    for (const [k, x] of Object.entries(v as Record<string, unknown>)) if (!k.startsWith('_')) flatten(x, prefix ? `${prefix}.${k}` : k, out)
  return out
}

export function metricKind(key: string): MetricKind {
  return key.endsWith('.true_fraction') ? 'fraction' : key.endsWith('.mean') || key === 'mean' ? 'mean' : 'other'
}

/** True for figures that describe the run, not a score: latency, tokens, and true_count next to true_fraction. */
export function isRunFigure(key: string): boolean {
  return /(^|\.)(model_latency|latency|total_tokens|tokens)(\.|$)/.test(key) || key.endsWith('.true_count')
}

/** Default direction of a metric: true when lower values are better. */
export function lowerIsBetter(key: string): boolean {
  return /latency|loss|error|cost|tokens|distance|duration|time|wrong|toxic/i.test(key)
}

/** Names of the built-in scorers that read better spelled out. */
const METRIC_ALIASES: Record<string, string> = { exact: 'exact match' }

/** "exact.true_fraction" → "exact match"; "similarity.mean" → "similarity"; "judge.score.mean" → "judge score"; "model_latency.mean" → "model latency". */
export function metricWords(key: string): string {
  const parts = key.split('.').filter((p) => p !== 'true_fraction' && p !== 'mean')
  const w = parts.map(scorerWords).filter(Boolean).join(' ') || scorerWords(key)
  return METRIC_ALIASES[w] ?? w
}

/** A metric value as text: fractions as percentages ("80%"), other numbers with two decimals ("0.85"), latency in time units. */
export function metricValue(key: string, v: Leaf | null | undefined): string {
  if (v == null) return '—'
  if (typeof v === 'boolean') return v ? 'yes' : 'no'
  if (metricKind(key) === 'fraction') return `${trim(v * 100, v * 100 >= 10 || Number.isInteger(v * 100) ? 0 : 1)}%`
  if (/latency/.test(key)) return shortDuration(v * 1000)
  if (Number.isInteger(v)) return v.toLocaleString('en-US')
  if (Math.abs(v) >= 100) return trim(v, 1)
  return v.toFixed(2)
}

const trim = (x: number, digits: number) => String(Number(x.toFixed(digits)))

/** "exact 80%", "similarity 0.85". */
export const metricText = (key: string, v: Leaf | null | undefined) => `${metricWords(key)} ${metricValue(key, v)}`

export type ChangeFormat = 'both' | 'absolute' | 'percent' | 'ratio'

/** How a change against the baseline can be shown, in words (the compare page's "Show changes as" choice). */
export const CHANGE_FORMATS: { id: ChangeFormat; label: string }[] = [
  { id: 'both', label: 'difference and percent' },
  { id: 'absolute', label: 'difference' },
  { id: 'percent', label: 'percent' },
  { id: 'ratio', label: 'ratio' },
]

/**
 * A metric's change from the baseline, in the metric's own unit: "+75 pts" for fractions, "+8 ms (+58%)" for latency,
 * "+0.20 (+33%)" for other numbers; or only the difference, the percentage ("+58%") or the ratio ("1.58×"). "no change" when equal.
 */
export function changeText(key: string, base: number, v: number, format: ChangeFormat = 'both'): string {
  const d = v - base
  if (Math.abs(d) < 1e-12) return format === 'ratio' ? '1×' : 'no change'
  const sign = d > 0 ? '+' : '−'
  const a = Math.abs(d)
  const abs =
    metricKind(key) === 'fraction'
      ? `${sign}${trim(a * 100, a * 100 >= 10 ? 0 : 1)} pts`
      : /latency/.test(key)
        ? `${sign}${shortDuration(a * 1000)}`
        : `${sign}${Number.isInteger(a) ? a.toLocaleString('en-US') : a >= 100 ? trim(a, 1) : a.toFixed(2)}`
  const pct = base !== 0 ? `${sign}${trim((a / Math.abs(base)) * 100, (a / Math.abs(base)) * 100 >= 10 ? 0 : 1)}%` : null
  switch (format) {
    case 'absolute':
      return abs
    case 'percent':
      return pct ?? abs
    case 'ratio':
      return base !== 0 ? `${Number((v / base).toPrecision(3))}×` : abs
    default:
      return pct && metricKind(key) !== 'fraction' ? `${abs} (${pct})` : abs
  }
}

/** The score metrics of a run summary, in a stable order: fractions and means first, then by key. */
export function scoreMetrics(summary: unknown): { key: string; value: number }[] {
  const flat = flatten(summary)
  return Object.entries(flat)
    .filter(([k, v]) => typeof v === 'number' && !isRunFigure(k))
    .map(([key, value]) => ({ key, value: value as number }))
    .sort((a, b) => rankKind(a.key) - rankKind(b.key) || a.key.localeCompare(b.key))
}

const rankKind = (k: string) => (metricKind(k) === 'other' ? 1 : 0)

/** The metric a run is summed up by: its first score metric (or the given key when the run has it). */
export function headline(summary: unknown, prefer?: string | null): { key: string; value: number } | null {
  const ms = scoreMetrics(summary)
  return (prefer ? ms.find((m) => m.key === prefer) : undefined) ?? ms[0] ?? null
}

/** Pass/fail counts of a run's boolean scorers, from its summary ({true_count, true_fraction}); `rows` fills in the total when every example failed. */
export function failureCounts(summary: unknown, rows?: number | null): { scorer: string; failed: number; total: number }[] {
  const flat = flatten(summary)
  const out: { scorer: string; failed: number; total: number }[] = []
  for (const [k, v] of Object.entries(flat)) {
    if (!k.endsWith('.true_count') || typeof v !== 'number') continue
    const base = k.slice(0, -'.true_count'.length)
    const frac = flat[`${base}.true_fraction`]
    let total: number | null = null
    if (typeof frac === 'number' && frac > 0) total = Math.round(v / frac)
    else if (rows != null && rows >= v) total = rows
    if (total == null) continue
    out.push({ scorer: base, failed: total - v, total })
  }
  return out.sort((a, b) => b.failed - a.failed || a.scorer.localeCompare(b.scorer))
}

export type Direction = 'better' | 'worse' | 'same'

export function direction(key: string, now: number, before: number, lower = lowerIsBetter(key)): Direction {
  if (Math.abs(now - before) < 1e-9) return 'same'
  return (lower ? now < before : now > before) ? 'better' : 'worse'
}

// ---------------------------------------------------------------- the Evaluations list

export interface RunInfo {
  id: string
  evaluation: string
  model: string
  status: 'success' | 'error' | 'running' | 'descendant_error' | string
  started_at: string
  ended_at?: string | null
  summary: unknown
  error?: string | null
  /** Examples in the dataset, when known. */
  rows?: number | null
}

/** The previous finished run of the same evaluation (runs newest first). */
export function previousRun(run: RunInfo, runs: RunInfo[]): RunInfo | null {
  const i = runs.findIndex((r) => r.id === run.id)
  for (let j = i + 1; j < runs.length; j++) if (runs[j].evaluation === run.evaluation && finished(runs[j])) return runs[j]
  return null
}

export const finished = (r: RunInfo) => r.status === 'success' || r.status === 'descendant_error'

/** The glyph of a run row: failed, running, worse than the previous run, or finished. */
export function runGlyph(run: RunInfo, prev: RunInfo | null): { kind: 'failed' | 'running' | 'warning' | 'ok'; label: string } {
  if (run.status === 'error') return { kind: 'failed', label: 'Failed' }
  if (run.status === 'running') return { kind: 'running', label: 'Running' }
  const h = headline(run.summary)
  const p = prev && h ? headline(prev.summary, h.key) : null
  if (h && p && p.key === h.key && direction(h.key, h.value, p.value) === 'worse') return { kind: 'warning', label: 'Worse than the previous run' }
  return { kind: 'ok', label: 'Finished' }
}

/**
 * The line under a run (the row's title is the model): "Scored exact match 80%, up from 0% in the previous run (support-v1); 2 of 10 examples failed."
 * The previous run is the one before it of the same evaluation.
 */
export function runLine(run: RunInfo, prev: RunInfo | null): string {
  if (run.status === 'running') return 'Still running.'
  if (run.status === 'error') return `Failed${run.error ? `: ${firstLine(run.error)}` : '.'}${run.error && !/[.!?]$/.test(firstLine(run.error)) ? '.' : ''}`
  const h = headline(run.summary)
  if (!h) return 'Finished with no scores.'
  let s = `Scored ${metricText(h.key, h.value)}`
  const p = prev ? headline(prev.summary, h.key) : null
  if (prev && p && p.key === h.key) {
    const d = direction(h.key, h.value, p.value)
    const was = metricValue(h.key, p.value)
    s += d === 'same' ? `, the same as the previous run` : `, ${h.value > p.value ? 'up' : 'down'} from ${was} in the previous run`
    if (prev.model !== run.model) s += ` (${prev.model})`
  }
  const f = failureCounts(run.summary, run.rows)
  if (f.length) {
    const w = f[0]
    s += `; ${w.failed === 0 ? `all ${w.total} examples passed` : `${w.failed} of ${plural(w.total, 'example')} failed`}${f.length > 1 && w.failed ? ` on ${metricWords(w.scorer)}` : ''}`
  }
  if (run.status === 'descendant_error') s += '; some examples raised errors'
  return s + '.'
}

const firstLine = (s: string) => s.split('\n')[0].trim().slice(0, 140)

/** A quiet meta line for a run row: "10 examples · took 1.2 s · $0.0012". */
export function runMeta(run: RunInfo, cost?: number | null): string {
  const xs: string[] = []
  if (run.rows != null) xs.push(plural(run.rows, 'example'))
  if (run.ended_at) xs.push(`took ${shortDuration(new Date(run.ended_at).getTime() - new Date(run.started_at).getTime())}`)
  if (cost) xs.push(`$${cost < 0.01 ? cost.toFixed(4) : cost.toFixed(2)}`)
  return xs.join(' · ')
}

export interface Change {
  evaluation: string
  key: string
  from: number
  to: number
  dir: Direction
  latest: RunInfo
  prev: RunInfo
}

/** Per evaluation: its latest finished run against the one before, on the latest run's headline metric. Runs newest first. */
export function latestChanges(runs: RunInfo[]): Change[] {
  const out: Change[] = []
  const seen = new Set<string>()
  for (const r of runs) {
    if (!finished(r) || seen.has(r.evaluation)) continue
    seen.add(r.evaluation)
    const prev = previousRun(r, runs)
    const h = headline(r.summary)
    const p = prev && h ? headline(prev.summary, h.key) : null
    if (!prev || !h || !p || p.key !== h.key) continue
    out.push({ evaluation: r.evaluation, key: h.key, from: p.value, to: h.value, dir: direction(h.key, h.value, p.value), latest: r, prev })
  }
  return out
}

/** "once", "twice", "3 times". */
export const times = (n: number) => (n === 1 ? 'once' : n === 2 ? 'twice' : `${n.toLocaleString('en-US')} times`)

/**
 * The Evaluations sentence. One evaluation: "support-eval ran twice. The latest run, with support-v2, scored 75% on exact match, up from 0%."
 * Several: "4 runs of 2 evaluations. Since its previous run, support-eval got better (exact match 0% → 80%). 1 run failed."
 * Clauses link to the comparison (or the run) they describe and to the failed or running runs.
 */
export function evaluationsSentence(runs: RunInfo[], filtered = false): EvalPart[] {
  if (!runs.length) return [filtered ? 'No runs match these filters.' : 'No evaluation has run yet.']
  const evals = new Set(runs.map((r) => r.evaluation))
  const parts: EvalPart[] = []
  if (evals.size === 1) {
    const name = [...evals][0]
    parts.push(filtered ? `${plural(runs.length, 'run')} of ${name} match.` : `${name} ran ${times(runs.length)}.`)
    const latest = runs.find(finished)
    if (latest) {
      const h = headline(latest.summary)
      const prev = previousRun(latest, runs)
      const p = prev && h ? headline(prev.summary, h.key) : null
      const which = latest === runs[0] ? 'The latest run' : 'The latest finished run'
      let text = `${which}, with ${latest.model}, `
      if (!h) text += 'finished with no scores'
      else {
        text += `scored ${metricValue(h.key, h.value)} on ${metricWords(h.key)}`
        if (prev && p && p.key === h.key) {
          const d = direction(h.key, h.value, p.value)
          text += d === 'same' ? ', the same as the run before' : `, ${h.value > p.value ? 'up' : 'down'} from ${metricValue(h.key, p.value)}`
        }
      }
      const link: EvalLink = prev && p && h && p.key === h.key ? { kind: 'compare', ids: [prev.id, latest.id], baseline: prev.id } : { kind: 'compare', ids: [latest.id] }
      parts.push(' ', { text, link }, '.')
    }
  } else {
    parts.push(`${plural(runs.length, 'run')} of ${evals.size} evaluations${filtered ? ' match' : ''}.`)
    const all = latestChanges(runs)
    const changes = all.filter((c) => c.dir !== 'same')
    const same = all.filter((c) => c.dir === 'same')
    if (changes.length) {
      parts.push(changes.length === 1 ? ' Since its previous run, ' : ' Since their previous runs, ')
      const clauses: EvalPart[] = changes.slice(0, 3).map((c) => ({
        text: `${c.evaluation} got ${c.dir} (${metricWords(c.key)} ${metricValue(c.key, c.from)} → ${metricValue(c.key, c.to)})`,
        link: { kind: 'compare', ids: [c.prev.id, c.latest.id], baseline: c.prev.id },
      }))
      if (changes.length > 3) clauses.push(`${plural(changes.length - 3, 'other evaluation')} changed`)
      parts.push(...joinParts(clauses))
      parts.push('.')
    } else if (same.length) {
      if (same.length === 1)
        parts.push(' ', { text: `${same[0].evaluation} scored the same as in its previous run`, link: { kind: 'compare', ids: [same[0].prev.id, same[0].latest.id], baseline: same[0].prev.id } }, '.')
      else parts.push(` ${plural(same.length, 'evaluation')} scored the same as in their previous runs.`)
    } else if (runs.some(finished)) {
      parts.push(' None has a previous run to compare with yet.')
    }
  }
  const failed = runs.filter((r) => r.status === 'error').length
  const running = runs.filter((r) => r.status === 'running').length
  const tail: EvalPart[] = []
  if (failed) tail.push({ text: `${plural(failed, 'run')} failed`, link: { kind: 'runs', status: 'error' } })
  if (running) tail.push({ text: `${running} ${running === 1 ? 'is' : 'are'} still running`, link: { kind: 'runs', status: 'running' } })
  if (tail.length) {
    const first = tail[0] as { text: string; link: EvalLink }
    tail[0] = { ...first, text: cap(first.text) }
    parts.push(' ', ...joinParts(tail), '.')
  }
  return parts
}

// ---------------------------------------------------------------- one run, or a comparison

export interface CmpRun {
  id: string
  label: string
  evaluation: string
  status: string
  summary: unknown
}

export interface CmpMetric {
  key: string
  values: Record<string, Leaf | null>
}

export interface CmpExample {
  /** Per run: the example's scores, averaged over trials (booleans stay booleans with one trial). */
  scores: Record<string, Record<string, Leaf>>
  /** Per run: true when a trial errored or a boolean score is false. */
  failed: Record<string, boolean>
}

/** An example's verdict against the baseline, over every other run and every score key. */
export function exampleChange(x: CmpExample, baseline: string, runIds: string[], lower: (k: string) => boolean): { regressed: boolean; improved: boolean } {
  let regressed = false
  let improved = false
  const b = x.scores[baseline] ?? {}
  for (const id of runIds) {
    if (id === baseline) continue
    const s = x.scores[id] ?? {}
    for (const k of Object.keys(s)) {
      if (isRunFigure(k)) continue
      const v = num(s[k])
      const w = num(b[k])
      if (v == null || w == null || Math.abs(v - w) < 1e-12) continue
      if (lower(k) ? v < w : v > w) improved = true
      else regressed = true
    }
  }
  return { regressed, improved }
}

const num = (v: Leaf | null | undefined) => (typeof v === 'number' ? v : typeof v === 'boolean' ? (v ? 1 : 0) : null)

/** True when a scores object has a false boolean leaf (an example a pass/fail scorer failed). */
export function hasFailingScore(scores: unknown): boolean {
  return Object.values(flatten(scores)).some((v) => v === false)
}

export interface CompareInput {
  runs: CmpRun[]
  baseline: string
  metrics: CmpMetric[]
  examples: CmpExample[]
  lower: (key: string) => boolean
}

/** The biggest difference between a run and the baseline, over score metrics: fractions by points, other numbers relative to the baseline. */
export function biggestDifference({ runs, baseline, metrics, lower }: Omit<CompareInput, 'examples'>): { key: string; run: string; from: number; to: number; dir: Direction } | null {
  let best: { key: string; run: string; from: number; to: number; dir: Direction; size: number } | null = null
  for (const m of metrics) {
    if (isRunFigure(m.key)) continue
    const b = num(m.values[baseline])
    if (b == null) continue
    for (const r of runs) {
      if (r.id === baseline) continue
      const v = num(m.values[r.id])
      if (v == null || Math.abs(v - b) < 1e-12) continue
      const size = metricKind(m.key) === 'fraction' || (Math.abs(b) <= 1 && Math.abs(v) <= 1) ? Math.abs(v - b) : Math.abs(v - b) / Math.max(Math.abs(b), 1e-9)
      if (!best || size > best.size) best = { key: m.key, run: r.id, from: b, to: v, dir: direction(m.key, v, b, lower(m.key)), size }
    }
  }
  if (!best) return null
  const { size: _size, ...rest } = best
  return rest
}

/** For each run but the baseline: how many score metrics it did better and worse on. */
export function metricWins({ runs, baseline, metrics, lower }: Omit<CompareInput, 'examples'>): { run: string; better: number; worse: number; total: number }[] {
  return runs
    .filter((r) => r.id !== baseline)
    .map((r) => {
      let better = 0
      let worse = 0
      let total = 0
      for (const m of metrics) {
        if (isRunFigure(m.key)) continue
        const b = num(m.values[baseline])
        const v = num(m.values[r.id])
        if (b == null || v == null) continue
        total++
        const d = direction(m.key, v, b, lower(m.key))
        if (d === 'better') better++
        else if (d === 'worse') worse++
      }
      return { run: r.id, better, worse, total }
    })
}

/**
 * The sentence of an evaluation run's page. One run: "support-v2 scored exact 80% and similarity 0.85 on support-eval. 2 of 10 examples failed."
 * Several: "support-v2 did better than support-v1 on 2 of 2 metrics. The biggest difference is exact: 80% against 0%. 8 examples got better and 1 got worse."
 */
export function compareSentence(input: CompareInput): EvalPart[] {
  const { runs, baseline, metrics, examples, lower } = input
  if (!runs.length) return ['None of these runs were found.']
  const label = (id: string) => runs.find((r) => r.id === id)?.label ?? id
  const parts: EvalPart[] = []
  if (runs.length === 1) {
    const r = runs[0]
    if (r.status === 'running') return [`${r.label} is still running on ${r.evaluation}.`]
    const ms = scoreMetrics(r.summary).slice(0, 3)
    parts.push(
      ms.length
        ? `${r.label} scored ${joinClauses(ms.map((m) => metricText(m.key, m.value)))} on ${r.evaluation}${r.status === 'error' ? ', then failed' : ''}.`
        : `${r.label} ${r.status === 'error' ? 'failed' : 'finished'} on ${r.evaluation} with no scores.`,
    )
    const failed = examples.filter((x) => x.failed[r.id]).length
    if (examples.length) {
      parts.push(' ')
      if (failed) parts.push({ text: `${failed} of ${plural(examples.length, 'example')} failed`, link: { kind: 'examples', filter: 'failed' } }, '.')
      else parts.push(examples.length === 1 ? 'The one example passed.' : `All ${examples.length} examples passed.`)
    }
    return parts
  }
  const wins = metricWins(input)
  const base = label(baseline)
  if (wins.length === 1) {
    const w = wins[0]
    const who = label(w.run)
    if (!w.total) parts.push(`${who} and ${base} share no score to compare.`)
    else if (w.better && !w.worse) parts.push(`${who} did better than ${base} on ${onMetrics(w.better, w.total)}.`)
    else if (w.worse && !w.better) parts.push(`${who} did worse than ${base} on ${onMetrics(w.worse, w.total)}.`)
    else if (!w.better && !w.worse) parts.push(`${who} scored the same as ${base} on ${onMetrics(w.total, w.total)}.`)
    else parts.push(`${who} did better than ${base} on ${plural(w.better, 'metric')} and worse on ${w.worse}.`)
  } else {
    const clauses = wins.map((w) => `${label(w.run)} on ${w.better} of ${w.total}`)
    parts.push(`Against ${base}, the other runs did better on: ${joinClauses(clauses)} ${wins.some((w) => w.total !== 1) ? 'metrics' : 'metric'}.`)
  }
  const big = biggestDifference(input)
  if (big) {
    const many = runs.length > 2
    parts.push(` The biggest difference is ${metricWords(big.key)}: ${metricValue(big.key, big.to)}${many ? ` for ${label(big.run)}` : ''} against ${metricValue(big.key, big.from)}${many ? ` for ${base}` : ''}.`)
  }
  let better = 0
  let worse = 0
  for (const x of examples) {
    const c = exampleChange(x, baseline, runs.map((r) => r.id), lower)
    if (c.improved) better++
    if (c.regressed) worse++
  }
  if (better || worse) {
    parts.push(' ')
    const xs: EvalPart[] = []
    if (worse) xs.push({ text: `${plural(worse, 'example')} got worse`, link: { kind: 'examples', filter: 'regressed' } })
    if (better) xs.push({ text: worse ? `${better} got better` : `${plural(better, 'example')} got better`, link: { kind: 'examples', filter: 'improved' } })
    parts.push(...joinParts(xs), '.')
  } else if (examples.length) parts.push(' No example changed.')
  return parts
}

// ---------------------------------------------------------------- scores

export const LOW_SCORE = 0.5

/** The range a scorer's numbers run over: 0–1, or a scale such as 1–5. */
export interface Scale {
  min: number
  max: number
}

/** One score as the sentences read it: the scorer's raw output when the API gives it, else its number. */
export interface ScoreItem {
  value: number | null
  output?: unknown
}

export interface ScoreLike extends ScoreItem {
  scorer: string
  session_id?: string
  span_id?: string
  scope: string
}

/**
 * How to read one scorer's results, from their shape: the yes/no field
 * every result has (counted first, a false one needs a look), the number
 * field, its range and the threshold below which a number needs a look
 * (the range's midpoint: 0.5 on 0–1, 3 on 1–5).
 */
export interface ScoreShape {
  boolKey: string | null
  numKey: string | null
  scale: Scale | null
  threshold: number | null
}

const BOOL_KEYS = ['correct', 'passed', 'pass', 'safe', 'success', 'ok', 'valid', 'match', 'value']
const NUM_KEYS = ['score', 'value', 'rating']

/** An output's yes/no and number fields ("correct", "score", "value" for a bare result); the number when there is no output. */
function leaves(x: ScoreItem): Record<string, Leaf> {
  if (x.output !== undefined && x.output !== null && typeof x.output !== 'string') {
    const f = flatten(x.output)
    if (Object.keys(f).length) return f
  }
  return x.value == null ? {} : { value: x.value }
}

/** A scale named in a scorer's own words: "Rate helpfulness 1-5", "from 0 to 10", "out of 10". */
export function scaleFromText(text: string | null | undefined): Scale | null {
  if (!text) return null
  const m = /\b(0|1)\s*(?:-|–|to)\s*(5|7|10|100)\b/.exec(text)
  if (m) return { min: Number(m[1]), max: Number(m[2]) }
  const o = /\bout of (5|7|10|100)\b/i.exec(text)
  return o ? { min: Number(o[1]) === 5 ? 1 : 0, max: Number(o[1]) } : null
}

/** The range numbers fall in, from the values seen: 0–1, else the smallest of 5, 10 or 100 that holds them; null when none does. */
export function inferScale(values: number[]): Scale | null {
  if (!values.length) return null
  const lo = Math.min(...values)
  const hi = Math.max(...values)
  if (lo >= 0 && hi <= 1) return { min: 0, max: 1 }
  if (lo < 0) return null
  for (const max of [5, 10, 100]) if (hi <= max) return { min: lo >= 1 && max === 5 ? 1 : 0, max }
  return null
}

/** How to read a scorer's results (see ScoreShape); `scale` is the scorer's own when it states one. */
export function scoreShape(items: ScoreItem[], scale?: Scale | null): ScoreShape {
  const rows = items.map(leaves).filter((r) => Object.keys(r).length)
  const keys = [...new Set(rows.flatMap((r) => Object.keys(r)))]
  const isBool = (k: string) => rows.length > 0 && rows.every((r) => typeof r[k] === 'boolean')
  const boolKey = BOOL_KEYS.find((k) => keys.includes(k) && isBool(k)) ?? keys.sort().find(isBool) ?? null
  const nums = keys.filter((k) => k !== boolKey && rows.some((r) => typeof r[k] === 'number'))
  const numKey = NUM_KEYS.find((k) => nums.includes(k)) ?? nums.sort()[0] ?? null
  const values = numKey ? rows.map((r) => r[numKey]).filter((v): v is number => typeof v === 'number') : []
  const sc = numKey ? (scale ?? inferScale(values)) : null
  return { boolKey, numKey, scale: sc, threshold: sc ? (sc.min + sc.max) / 2 : null }
}

/** One result's yes/no and number, as its scorer's shape reads them. */
export function scoreParts(x: ScoreItem, shape: ScoreShape): { yes: boolean | null; num: number | null } {
  const r = leaves(x)
  const b = shape.boolKey ? r[shape.boolKey] : undefined
  const n = shape.numKey ? r[shape.numKey] : undefined
  return { yes: typeof b === 'boolean' ? b : null, num: typeof n === 'number' ? n : null }
}

/** Whether a result needs a look: its yes/no part says no, or (with no yes/no part) its number is below the threshold. */
export function needsLook(x: ScoreItem, shape: ScoreShape): boolean {
  const p = scoreParts(x, shape)
  if (shape.boolKey) return p.yes === false
  return p.num != null && shape.threshold != null && p.num < shape.threshold
}

/** What one scorer's results say, counted. */
export interface Judgement {
  scorer: string
  n: number
  shape: ScoreShape
  yes: number
  no: number
  mean: number | null
  /** Results with a number. */
  counted: number
  /** Results that need a look. */
  look: number
}

export function judge(scorer: string, items: ScoreItem[], shape: ScoreShape = scoreShape(items)): Judgement {
  const ps = items.map((x) => scoreParts(x, shape))
  const nums = ps.map((p) => p.num).filter((v): v is number => v != null)
  return {
    scorer,
    n: items.length,
    shape,
    yes: ps.filter((p) => p.yes === true).length,
    no: ps.filter((p) => p.yes === false).length,
    mean: nums.length ? nums.reduce((a, b) => a + b, 0) / nums.length : null,
    counted: nums.length,
    look: items.filter((x) => needsLook(x, shape)).length,
  }
}

/**
 * The words for a yes/no field: [said yes, said no after a yes, a yes, a no, said no alone]:
 * "said correct" / "did not" / "correct" / "not correct" / "did not say correct".
 */
export function boolWords(key: string): [string, string, string, string, string] {
  const k = key.toLowerCase().replace(/[\s-]+/g, '_')
  if (/^(pass|passed|ok|success|succeeded)$/.test(k)) return ['passed', 'failed', 'passed', 'failed', 'failed']
  if (/^(match|matches|matched|exact_match)$/.test(k)) return ['matched', 'did not', 'matched', 'no match', 'did not match']
  if (!k || k === 'value' || k === 'result') return ['were true', 'were false', 'true', 'false', 'were false']
  const w = k.replace(/^is_/, '').replace(/_/g, ' ')
  return [`said ${w}`, 'did not', w, `not ${w}`, `did not say ${w}`]
}

/** A number on its scale: "0.53", "2.52 out of 5". */
export function onScale(v: number, scale: Scale | null, exact = false): string {
  const t = Number.isInteger(v) ? String(v) : exact ? String(Number(v.toFixed(2))) : v.toFixed(2)
  return scale && scale.max !== 1 ? `${t} out of ${scale.max}` : t
}

const numWord = (key: string | null) => (!key || key === 'value' || key === 'score' ? 'score' : key.replace(/[_.]+/g, ' '))

/**
 * One result in words, its yes/no part first: "not correct, score 5 out of 5", "4 out of 5", "0.10".
 */
export function scoreResultText(x: ScoreItem, shape: ScoreShape): string {
  const p = scoreParts(x, shape)
  const b = shape.boolKey && p.yes != null ? boolWords(shape.boolKey)[p.yes ? 2 : 3] : ''
  const n = p.num != null ? onScale(p.num, shape.scale) : ''
  if (b && n) return `${b}, ${numWord(shape.numKey)} ${n}`
  return b || n || '—'
}

/** "3 are below 0.5", "one is below 3 out of 5". */
const belowText = (n: number, shape: ScoreShape) =>
  `${n === 1 ? 'one is' : `${n} are`} below ${onScale(shape.threshold as number, shape.scale, true)}`

/**
 * What the scorers' results say, after a sentence's count. The Scores page and a monitor both use it, so the two never disagree.
 * One scorer: "22 said correct; 3 did not. The score averages 2.52 out of 5." / "2 are below 0.5. Goal completion averages 0.53."
 * Several: "4 need a look. Goal completion averages 0.53; helpfulness judge: 9 of 10 said correct."
 * The part counting what needs a look links to those results.
 */
export function judgementParts(js: Judgement[]): EvalPart[] {
  const xs = js.filter((j) => j.n > 0)
  if (!xs.length) return []
  const lowLink = (text: string): EvalPart => ({ text, link: { kind: 'scores', filter: 'low' } })
  /** "1 was false", "2 were false". */
  const agree = (n: number, phrase: string) => (n === 1 ? phrase.replace(/^were /, 'was ') : phrase)
  if (xs.length === 1) {
    const j = xs[0]
    const parts: EvalPart[] = []
    if (j.shape.boolKey) {
      const [yes, no, , , noAlone] = boolWords(j.shape.boolKey)
      const all = (n: number) => (n === 1 ? 'It' : n === 2 ? 'Both' : `All ${n}`)
      if (!j.no) parts.push(`${j.yes === j.n ? all(j.n) : j.yes} ${agree(j.yes, yes)}.`)
      else if (!j.yes) parts.push(lowLink(`${j.no === j.n ? all(j.n) : j.no} ${agree(j.no, noAlone)}`), '.')
      else parts.push(`${j.yes} ${agree(j.yes, yes)}; `, lowLink(`${j.no} ${agree(j.no, no)}`), '.')
      if (j.mean != null && j.counted > 1) parts.push(` The ${numWord(j.shape.numKey)} averages ${onScale(j.mean, j.shape.scale)}.`)
      return parts
    }
    if (j.shape.threshold != null) {
      if (j.look) parts.push(lowLink(cap(belowText(j.look, j.shape))), '.')
      else parts.push(`None is below ${onScale(j.shape.threshold, j.shape.scale, true)}.`)
    }
    if (j.mean != null && j.counted > 1) parts.push(`${parts.length ? ' ' : ''}${cap(scorerWords(j.scorer))} averages ${onScale(j.mean, j.shape.scale)}.`)
    return parts
  }
  const look = xs.reduce((a, j) => a + j.look, 0)
  const parts: EvalPart[] = look ? [lowLink(`${look === 1 ? 'One needs' : `${look} need`} a look`), '.'] : ['None needs a look.']
  const each = [...xs]
    .sort((a, b) => b.look / b.n - a.look / a.n || a.scorer.localeCompare(b.scorer))
    .slice(0, 3)
    .map((j) => {
      if (j.shape.boolKey) return `${scorerWords(j.scorer)}: ${j.yes} of ${j.n} ${boolWords(j.shape.boolKey)[0]}`
      if (j.mean != null && j.counted > 1) return `${scorerWords(j.scorer)} averages ${onScale(j.mean, j.shape.scale)}`
      return ''
    })
    .filter(Boolean)
  if (each.length) parts.push(` ${cap(joinClauses(each))}.`)
  return parts
}

/** "3 sessions", "18 steps", "3 sessions and 18 steps": what the scores are on. */
function scoredSubjects(scores: ScoreLike[]): string {
  const sessions = new Set(scores.filter((s) => s.scope !== 'span').map((s) => s.session_id).filter(Boolean))
  const steps = new Set(scores.filter((s) => s.scope === 'span').map((s) => s.span_id).filter(Boolean))
  const xs = [sessions.size ? plural(sessions.size, 'session') : '', steps.size ? plural(steps.size, 'step') : ''].filter(Boolean)
  return xs.length ? ` on ${xs.join(' and ')}` : ''
}

/** Each scorer's shape, read from all of its scores (pass every score, not a filtered few, so a filter does not change the threshold). */
export function scoreShapes(scores: (ScoreItem & { scorer: string })[], scales: Record<string, Scale | null | undefined> = {}): Record<string, ScoreShape> {
  const by = new Map<string, ScoreItem[]>()
  for (const s of scores) by.set(s.scorer, [...(by.get(s.scorer) ?? []), s])
  return Object.fromEntries([...by.entries()].map(([n, xs]) => [n, scoreShape(xs, scales[n])]))
}

/** Each scorer's judgement of the given scores, read with `shapes` (else each scorer's own shape from these scores). */
export function judgements(scores: (ScoreItem & { scorer: string })[], shapes: Record<string, ScoreShape> = {}): Judgement[] {
  const by = new Map<string, ScoreItem[]>()
  for (const s of scores) by.set(s.scorer, [...(by.get(s.scorer) ?? []), s])
  return [...by.entries()].map(([n, xs]) => judge(n, xs, shapes[n] ?? scoreShape(xs)))
}

/**
 * The Scores sentence: "25 scores from 1 scorer on 25 steps. 22 said correct; 3 did not. The score averages 2.52 out of 5."
 * Several scorers: "12 scores from 2 scorers on 5 sessions and 18 steps. 4 need a look. Goal completion averages 0.53; …"
 */
export function scoresSentence(scores: ScoreLike[], filtered = false, shapes: Record<string, ScoreShape> = {}): EvalPart[] {
  if (!scores.length) return [filtered ? 'No scores match these filters.' : 'Nothing has been scored yet.']
  const scorers = [...new Set(scores.map((s) => s.scorer))]
  const parts: EvalPart[] = [`${plural(scores.length, 'score')} from ${plural(scorers.length, 'scorer')}${scoredSubjects(scores)}${filtered ? ' match' : ''}.`]
  const rest = judgementParts(judgements(scores, { ...scoreShapes(scores), ...shapes }))
  if (rest.length) parts.push(' ', ...rest)
  return parts
}

/** A score row's title: "Goal completion 0.10", "Helpfulness judge: not correct, score 5 out of 5" (with its scorer's shape). */
export const scoreTitle = (s: Pick<ScoreLike, 'scorer' | 'value' | 'output'>, shape?: ScoreShape) => {
  if (shape && (shape.boolKey || shape.scale)) return `${cap(scorerWords(s.scorer))}${shape.boolKey ? ':' : ''} ${scoreResultText(s, shape)}`
  return `${cap(scorerWords(s.scorer))} ${s.value == null ? '—' : Number.isInteger(s.value) ? String(s.value) : s.value.toFixed(2)}`
}

/**
 * The number a scorer's output stands for, as the server reads it (store.ScoreValue): a number, a boolean as 1 or 0, or in an object the
 * first of value, score, safe, passed, pass, correct, else its first boolean or number key in name order. Null for text.
 */
export function scoreValue(v: unknown): number | null {
  if (typeof v === 'boolean') return v ? 1 : 0
  if (typeof v === 'number') return Number.isFinite(v) ? v : null
  if (v && typeof v === 'object' && !Array.isArray(v)) {
    const o = v as Record<string, unknown>
    for (const k of ['value', 'score', 'safe', 'passed', 'pass', 'correct']) {
      if (k in o) {
        const r = scoreValue(o[k])
        if (r != null) return r
      }
    }
    for (const k of Object.keys(o).sort()) {
      const x = o[k]
      if (typeof x === 'boolean' || (typeof x === 'number' && Number.isFinite(x))) return scoreValue(x)
    }
  }
  return null
}

/** What a score is about: "The session", "Agent coder", "A hand-off", "A turn", "A step". */
export function scoreSubject(s: { scope: string; agent?: string }): string {
  switch (s.scope) {
    case 'session':
      return 'The session'
    case 'agent':
      return s.agent ? `Agent ${s.agent}` : 'An agent'
    case 'handoff':
      return 'A hand-off'
    case 'turn':
      return 'A turn'
    case 'span':
      return 'A step'
    default:
      return cap(s.scope)
  }
}

// ---------------------------------------------------------------- leaderboards

export interface LbColumnLike {
  index: number
  name: string
  minimize: boolean
}

export interface LbRowLike {
  model: string
  values: Record<string, number | null>
}

/** The models that lead each column (ties lead together). */
export function columnLeaders(columns: LbColumnLike[], rows: LbRowLike[]): Record<string, string[]> {
  const out: Record<string, string[]> = {}
  for (const c of columns) {
    const k = String(c.index)
    const vals = rows.map((r) => r.values[k]).filter((v): v is number => v != null)
    if (!vals.length) {
      out[k] = []
      continue
    }
    const best = c.minimize ? Math.min(...vals) : Math.max(...vals)
    out[k] = rows.filter((r) => r.values[k] != null && Math.abs((r.values[k] as number) - best) < 1e-12).map((r) => r.model)
  }
  return out
}

/** "support-v2 leads on 2 of 3 metrics; support-v1 leads on similarity (tone-check)." */
export function leaderboardSentence(columns: LbColumnLike[], rows: LbRowLike[]): string {
  if (!rows.length) return columns.length ? 'No model has a finished run of these evaluations yet.' : 'This leaderboard has no metrics yet.'
  const leaders = columnLeaders(columns, rows)
  const counts = new Map<string, number>()
  for (const ms of Object.values(leaders)) for (const m of ms) counts.set(m, (counts.get(m) ?? 0) + 1)
  if (rows.length === 1) return `Only ${rows[0].model} has finished runs for ${columns.length === 1 ? 'this metric' : 'these metrics'}.`
  const ranked = [...counts.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
  if (!ranked.length) return `${plural(rows.length, 'model')} ranked; none has a value yet.`
  const [top, n] = ranked[0]
  let s = `${top} leads on ${onMetrics(n, columns.length)}`
  const others = columns.filter((c) => !leaders[String(c.index)].includes(top) && leaders[String(c.index)].length)
  if (others.length) {
    const byModel = new Map<string, string[]>()
    for (const c of others) {
      const m = leaders[String(c.index)][0]
      byModel.set(m, [...(byModel.get(m) ?? []), c.name])
    }
    const xs = [...byModel.entries()].slice(0, 2).map(([m, cs]) => `${m} leads on ${joinClauses(cs)}`)
    s += `; ${xs.join('; ')}`
  }
  return `${s}. ${plural(rows.length, 'model')} ranked.`
}

/** A leaderboards list row line: "support-v2 leads on 2 of 3 metrics." */
export function leaderboardLine(columns: LbColumnLike[], rows: LbRowLike[]): string {
  if (!columns.length) return 'No metrics yet.'
  if (!rows.length) return `${plural(columns.length, 'metric')}; no finished runs yet.`
  const leaders = columnLeaders(columns, rows)
  const counts = new Map<string, number>()
  for (const ms of Object.values(leaders)) for (const m of ms) counts.set(m, (counts.get(m) ?? 0) + 1)
  const ranked = [...counts.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
  if (!ranked.length) return `${plural(columns.length, 'metric')}; no values yet.`
  const [top, n] = ranked[0]
  return `${top} leads on ${onMetrics(n, columns.length)}, among ${plural(rows.length, 'model')}.`
}

/** "the one metric", "both metrics", "all 3 metrics", "2 of 3 metrics". */
export function onMetrics(n: number, total: number): string {
  if (n !== total) return `${n} of ${plural(total, 'metric')}`
  return total === 1 ? 'the one metric' : total === 2 ? 'both metrics' : `all ${total} metrics`
}

// ---------------------------------------------------------------- review queues

export interface QueueLike {
  id: string
  name: string
  total: number
  done: number
}

/** "6 items wait for review in 2 queues; Weekly quality review has 4 of them." */
export function queuesSentence(queues: QueueLike[]): EvalPart[] {
  if (!queues.length) return ['No review queues yet.']
  const left = queues.map((q) => ({ ...q, left: Math.max(0, q.total - q.done) }))
  const waiting = left.filter((q) => q.left > 0)
  const total = waiting.reduce((a, q) => a + q.left, 0)
  if (!total) {
    const items = queues.reduce((a, q) => a + q.total, 0)
    return [items ? `Every item in ${queues.length === 1 ? 'the queue' : `the ${queues.length} queues`} has been reviewed.` : `${queues.length === 1 ? 'The queue is' : `The ${queues.length} queues are`} empty.`]
  }
  const parts: EvalPart[] = [`${plural(total, 'item')} ${total === 1 ? 'waits' : 'wait'} for review`]
  if (waiting.length === 1) {
    parts.push(' in ', { text: waiting[0].name, link: { kind: 'queue', id: waiting[0].id } }, '.')
    return parts
  }
  parts.push(` in ${waiting.length} queues`)
  const top = [...waiting].sort((a, b) => b.left - a.left || a.name.localeCompare(b.name))[0]
  parts.push('; ', { text: top.name, link: { kind: 'queue', id: top.id } }, ` has ${top.left === total ? 'all' : top.left} of them.`)
  return parts
}

export interface FieldLike {
  name: string
  schema?: { type?: string; enum?: unknown[] } | null
}

/**
 * What a queue asks of its reviewers, from its fields' types: "asks reviewers to rate quality and say whether it is correct".
 * Fields whose type is not known yet are named plainly ("asks reviewers about tone").
 */
export function queueAsks(fields: FieldLike[]): string {
  if (!fields.length) return 'asks for nothing: reviewers mark each item done or skip it'
  const words = (f: FieldLike) => scorerWords(f.name)
  const by = (pred: (f: FieldLike) => boolean) => fields.filter(pred).map(words)
  const known = (f: FieldLike) => !!f.schema && (!!f.schema.type || Array.isArray(f.schema.enum))
  const choice = by((f) => known(f) && Array.isArray(f.schema!.enum))
  const rate = by((f) => known(f) && !Array.isArray(f.schema!.enum) && (f.schema!.type === 'integer' || f.schema!.type === 'number'))
  const yesNo = by((f) => known(f) && !Array.isArray(f.schema!.enum) && f.schema!.type === 'boolean').map((w) => w.replace(/^is\s+/, ''))
  const write = by((f) => known(f) && !Array.isArray(f.schema!.enum) && !['integer', 'number', 'boolean'].includes(f.schema!.type ?? ''))
  const unknown = by((f) => !known(f))
  const clauses = [
    rate.length ? `rate ${joinClauses(rate)}` : '',
    yesNo.length ? `say whether it is ${joinClauses(yesNo)}` : '',
    choice.length ? `choose ${joinClauses(choice.map((w) => `the ${w}`))}` : '',
    write.length ? `add ${joinClauses(write)}` : '',
  ].filter(Boolean)
  if (!clauses.length) return `asks reviewers about ${joinClauses(unknown)}`
  return `asks reviewers to ${joinClauses(clauses)}${unknown.length ? `, and about ${joinClauses(unknown)}` : ''}`
}

/** A queue row's line: "4 of 6 items left to review." / "All 6 items reviewed." / "Empty: add spans from Traces." */
export function queueLine(q: { total: number; done: number }): string {
  if (!q.total) return 'Empty. Add steps from Traces or a step’s page.'
  const left = Math.max(0, q.total - q.done)
  if (!left) return `All ${plural(q.total, 'item')} reviewed.`
  return `${left} of ${plural(q.total, 'item')} left to review.`
}

/** The review page's sentence: "Item 3 of 6. 4 are left to review; 2 done, 1 skipped." */
export function reviewSentence(p: { position: number | null; total: number; completed: number; skipped: number }): string {
  if (!p.total) return 'This queue is empty.'
  const left = p.total - p.completed - p.skipped
  if (!left) return `You have reviewed every item: ${p.completed} done${p.skipped ? `, ${p.skipped} skipped` : ''}.`
  const done = [p.completed ? `${p.completed} done` : '', p.skipped ? `${p.skipped} skipped` : ''].filter(Boolean)
  return `${p.position != null ? `Item ${p.position} of ${p.total}. ` : ''}${left === 1 ? 'One is' : `${left} are`} left to review${done.length ? `; ${done.join(', ')}` : ''}.`
}

// ---------------------------------------------------------------- one review item

/** A value as plain text: strings as they are, yes/no, numbers, lists joined, objects as "key: value" lines. */
export function plainText(v: unknown, depth = 0): string {
  if (v == null) return '—'
  if (typeof v === 'string') return v
  if (typeof v === 'boolean') return v ? 'yes' : 'no'
  if (typeof v === 'number') return Number.isInteger(v) ? v.toLocaleString('en-US') : String(Number(v.toFixed(4)))
  if (Array.isArray(v)) return v.every((x) => x == null || typeof x !== 'object') ? v.map((x) => plainText(x)).join(', ') : JSON.stringify(v)
  if (typeof v === 'object') {
    if (depth > 0) return JSON.stringify(v)
    return Object.entries(v as Record<string, unknown>)
      .filter(([k]) => !k.startsWith('_'))
      .map(([k, x]) => `${cap(scorerWords(k))}: ${plainText(x, depth + 1)}`)
      .join('\n')
  }
  return String(v)
}

/** Dataset columns that hold the answer a person expects. */
const EXPECTED = /^(expected|expected_output|expected_answer|target|reference|ground_truth|gold|label|ideal|correct_answer)$/i

export interface ReviewField {
  label: string
  text: string
}

export interface ReviewView {
  /** What the model was asked: the dataset row's fields, or the step's inputs ("Question: …"). */
  asked: ReviewField[]
  /** What it answered. */
  answer: ReviewField | null
  /** The answer the dataset expects. */
  expected: ReviewField[]
  /** The scores already given, as text: "exact match pass · similarity 1". */
  scores: string | null
  /** The model that answered, when the step names one. */
  model: string | null
  /** Seconds the model took, when the step records it. */
  modelLatency: number | null
}

/**
 * What a review item shows first (DESIGN.md §7.2: the thing to judge, not the raw step): the question and the answer, the expected answer and
 * the scores as text. `inputs` are the step's inputs with refs to dataset rows already read (an object in place of the ref string); `names`
 * gives the readable name of any ref left (the model).
 */
export function reviewView(inputs: Record<string, unknown> | null | undefined, output: unknown, names: Record<string, string> = {}): ReviewView {
  const asked: ReviewField[] = []
  const expected: ReviewField[] = []
  let model: string | null = null
  const add = (k: string, v: unknown) => (EXPECTED.test(k) ? expected : asked).push({ label: cap(scorerWords(k)) || k, text: plainText(v) })
  for (const [k, v] of Object.entries(inputs ?? {})) {
    if (k === 'self') continue
    if (k === 'model' && typeof v === 'string' && v.startsWith('weft:///')) {
      model = names[v] ?? null
      continue
    }
    if (typeof v === 'string' && v.startsWith('weft:///')) continue
    if ((k === 'example' || k === 'row' || k === 'inputs') && v && typeof v === 'object' && !Array.isArray(v)) {
      for (const [kk, vv] of Object.entries(v as Record<string, unknown>)) if (!kk.startsWith('_')) add(kk, vv)
    } else add(k, v)
  }
  let answer: ReviewField | null = null
  let scores: string | null = null
  let modelLatency: number | null = null
  const o = output as Record<string, unknown> | null
  if (o && typeof o === 'object' && !Array.isArray(o) && 'output' in o && ('scores' in o || 'model_latency' in o)) {
    answer = { label: 'Answer', text: plainText(o.output) }
    const flat = flatten(o.scores ?? {})
    const xs = Object.entries(flat).map(([k, v]) => `${metricWords(k)} ${typeof v === 'boolean' ? (v ? 'passed' : 'failed') : metricValue(k, v)}`)
    scores = xs.length ? xs.join(' · ') : null
    modelLatency = typeof o.model_latency === 'number' ? o.model_latency : null
  } else if (output !== undefined && output !== null) answer = { label: 'Answer', text: plainText(output) }
  return { asked, answer, expected, scores, model, modelLatency }
}

// ---------------------------------------------------------------- playground

export interface PgResult {
  model: string
  running?: boolean
  error?: string | null
  latencyMs?: number | null
  tokens?: { prompt?: number; completion?: number; total?: number } | null
}

/** "Try a prompt on gpt-4o-mini." / "gpt-4o-mini answered in 1.2 s with 34 tokens." */
export function playgroundSentence(results: PgResult[]): string {
  const ran = results.filter((r) => r.running || r.error || r.latencyMs != null)
  if (!ran.length) return results.length > 1 ? `Try one prompt on ${joinClauses(results.map((r) => r.model))} side by side.` : `Try a prompt on ${results[0]?.model || 'a model'}.`
  return ran
    .map((r) => {
      if (r.running) return `${r.model} is answering…`
      if (r.error && r.error !== 'Stopped') return `${r.model} failed: ${firstLine(r.error)}${/[.!?]$/.test(firstLine(r.error)) ? '' : '.'}`
      if (r.error === 'Stopped') return `${r.model} was stopped.`
      const t = r.tokens?.total
      return `${r.model} answered in ${shortDuration(r.latencyMs)}${t != null ? ` with ${plural(t, 'token')}` : ''}.`
    })
    .join(' ')
}

/** "34 tokens (20 in, 14 out)". */
export function tokensText(u: { prompt_tokens?: number; completion_tokens?: number; total_tokens?: number } | null | undefined): string {
  if (!u || u.total_tokens == null) return ''
  const inOut = u.prompt_tokens != null || u.completion_tokens != null ? ` (${u.prompt_tokens ?? 0} in, ${u.completion_tokens ?? 0} out)` : ''
  return `${plural(u.total_tokens, 'token')}${inOut}`
}

/** The Leaderboards sentence: "2 leaderboards. support-v2 leads both." / "… leads 2 of 3." */
export function leaderboardsSentence(items: { name: string; leader: string | null }[]): string {
  if (!items.length) return 'No leaderboards yet.'
  const head = `${plural(items.length, 'leaderboard')}.`
  const counts = new Map<string, number>()
  for (const x of items) if (x.leader) counts.set(x.leader, (counts.get(x.leader) ?? 0) + 1)
  const ranked = [...counts.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
  if (!ranked.length) return `${head} None has a finished run to rank yet.`
  const [top, n] = ranked[0]
  if (items.length === 1) return `${head} ${top} leads it.`
  return `${head} ${top} leads ${n === items.length ? (n === 2 ? 'both' : 'all of them') : `${n} of them`}.`
}

/** The model that leads most columns of a leaderboard (null when none has a value). */
export function leaderOf(columns: LbColumnLike[], rows: LbRowLike[]): string | null {
  const counts = new Map<string, number>()
  for (const ms of Object.values(columnLeaders(columns, rows))) for (const m of ms) counts.set(m, (counts.get(m) ?? 0) + 1)
  return [...counts.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))[0]?.[0] ?? null
}
