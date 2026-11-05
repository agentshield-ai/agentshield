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

/** "exact.true_fraction" → "exact"; "similarity.mean" → "similarity"; "judge.score.mean" → "judge score"; "model_latency.mean" → "model latency". */
export function metricWords(key: string): string {
  const parts = key.split('.').filter((p) => p !== 'true_fraction' && p !== 'mean')
  return parts.map(scorerWords).filter(Boolean).join(' ') || scorerWords(key)
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
 * The line under a run: "support-v2 scored exact 80%, up from 0% in the previous run (support-v1); 2 of 10 examples failed."
 * The previous run is the one before it of the same evaluation.
 */
export function runLine(run: RunInfo, prev: RunInfo | null): string {
  if (run.status === 'running') return `${run.model} is running.`
  if (run.status === 'error') return `${run.model} failed${run.error ? `: ${firstLine(run.error)}` : '.'}${run.error && !/[.!?]$/.test(firstLine(run.error)) ? '.' : ''}`
  const h = headline(run.summary)
  if (!h) return `${run.model} finished with no scores.`
  let s = `${run.model} scored ${metricText(h.key, h.value)}`
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

/**
 * The Evaluations sentence: "4 runs of 2 evaluations. Since its previous run, support-eval got better (exact 0% → 80%). 1 run failed."
 * Clauses link to the comparison they describe and to the failed or running runs.
 */
export function evaluationsSentence(runs: RunInfo[], filtered = false): EvalPart[] {
  if (!runs.length) return [filtered ? 'No runs match these filters.' : 'No evaluation has run yet.']
  const evals = new Set(runs.map((r) => r.evaluation))
  const parts: EvalPart[] = [
    evals.size === 1 ? `${plural(runs.length, 'run')} of ${[...evals][0]}${filtered ? ' match' : ''}.` : `${plural(runs.length, 'run')} of ${evals.size} evaluations${filtered ? ' match' : ''}.`,
  ]
  const changes = latestChanges(runs).filter((c) => c.dir !== 'same')
  const same = latestChanges(runs).filter((c) => c.dir === 'same')
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
    else if (w.better && !w.worse) parts.push(`${who} did better than ${base} on ${w.better} of ${plural(w.total, 'metric')}.`)
    else if (w.worse && !w.better) parts.push(`${who} did worse than ${base} on ${w.worse} of ${plural(w.total, 'metric')}.`)
    else if (!w.better && !w.worse) parts.push(`${who} scored the same as ${base} on ${w.total === 1 ? 'the one metric' : `all ${w.total} metrics`}.`)
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

export interface ScoreLike {
  scorer: string
  value: number | null
  session_id?: string
  scope: string
}

/** The Scores sentence: "12 scores from 2 scorers on 5 sessions. 3 are below 0.5. Goal completion averages 0.53." */
export function scoresSentence(scores: ScoreLike[], filtered = false): EvalPart[] {
  if (!scores.length) return [filtered ? 'No scores match these filters.' : 'Nothing has been scored yet.']
  const scorers = [...new Set(scores.map((s) => s.scorer))]
  const sessions = new Set(scores.map((s) => s.session_id).filter(Boolean))
  const parts: EvalPart[] = [
    `${plural(scores.length, 'score')} from ${plural(scorers.length, 'scorer')}${sessions.size ? ` on ${plural(sessions.size, 'session')}` : ''}${filtered ? ' match' : ''}.`,
  ]
  const low = scores.filter((s) => s.value != null && s.value < LOW_SCORE)
  if (low.length) {
    const lowSessions = new Set(low.map((s) => s.session_id).filter(Boolean))
    parts.push(' ', {
      text: `${low.length === 1 ? 'One is' : `${low.length} are`} below ${LOW_SCORE}`,
      link: { kind: 'scores', filter: 'low' },
    })
    parts.push(lowSessions.size > 1 ? `, in ${lowSessions.size} sessions.` : '.')
  } else parts.push(` None is below ${LOW_SCORE}.`)
  const means = scorers
    .map((n) => {
      const vs = scores.filter((s) => s.scorer === n && s.value != null).map((s) => s.value as number)
      return { n, mean: vs.length ? vs.reduce((a, b) => a + b, 0) / vs.length : null, count: vs.length }
    })
    .filter((m) => m.mean != null && m.count > 1)
    .sort((a, b) => (a.mean as number) - (b.mean as number))
    .slice(0, 2)
  if (means.length) parts.push(` ${cap(joinClauses(means.map((m) => `${scorerWords(m.n)} averages ${(m.mean as number).toFixed(2)}`)))}.`)
  return parts
}

/** A score row's title: "Goal completion 0.10". */
export const scoreTitle = (s: Pick<ScoreLike, 'scorer' | 'value'>) => `${cap(scorerWords(s.scorer))} ${s.value == null ? '—' : s.value.toFixed(2)}`

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
  let s = `${top} leads on ${n === columns.length ? (n === 1 ? 'the one metric' : `all ${n} metrics`) : `${n} of ${plural(columns.length, 'metric')}`}`
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
  return `${top} leads on ${n} of ${plural(columns.length, 'metric')}, among ${plural(rows.length, 'model')}.`
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
