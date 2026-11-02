// Inline leaderboard for compared evaluation runs (unit tested).
// Each selected metric is min-max normalised across runs (flipped when lower
// is better) and the run's score is the weighted mean of those values.

export interface LbMetric {
  key: string
  weight: number
  lowerBetter: boolean
}

export interface LbRow {
  id: string
  /** Weighted score in 0..1, null when no selected metric has a value. */
  score: number | null
  rank: number
  /** Normalised 0..1 per metric key (null when the run has no value). */
  parts: Record<string, number | null>
  missing: string[]
}

export function rankRuns(ids: string[], values: Record<string, Record<string, number | null | undefined>>, metrics: LbMetric[]): LbRow[] {
  const active = metrics.filter((m) => m.weight > 0)
  const rows: LbRow[] = ids.map((id) => ({ id, score: null, rank: 0, parts: {}, missing: [] }))
  const totalW = active.reduce((s, m) => s + m.weight, 0)
  for (const m of active) {
    const vals = ids.map((id) => values[m.key]?.[id]).filter((v): v is number => typeof v === 'number' && Number.isFinite(v))
    const lo = Math.min(...vals)
    const hi = Math.max(...vals)
    for (const r of rows) {
      const v = values[m.key]?.[r.id]
      if (typeof v !== 'number' || !Number.isFinite(v)) {
        r.parts[m.key] = null
        r.missing.push(m.key)
        continue
      }
      let n = hi === lo ? 1 : (v - lo) / (hi - lo)
      if (m.lowerBetter && hi !== lo) n = 1 - n
      r.parts[m.key] = n
    }
  }
  for (const r of rows) {
    if (!totalW || r.missing.length === active.length) continue
    // A missing metric counts as the worst value.
    // Rounded so float noise does not break ties.
    r.score = Math.round((active.reduce((s, m) => s + m.weight * (r.parts[m.key] ?? 0), 0) / totalW) * 1e9) / 1e9
  }
  const sorted = [...rows].sort((a, b) => (b.score ?? -1) - (a.score ?? -1) || ids.indexOf(a.id) - ids.indexOf(b.id))
  let rank = 0
  let prev: number | null | undefined
  sorted.forEach((r, i) => {
    if (r.score !== prev) rank = i + 1
    r.rank = r.score == null ? sorted.length : rank
    prev = r.score
  })
  return sorted
}

/** URL form: "key*weight" items joined by commas, keys URI-encoded; a leading "-" marks lower-is-better. */
export function encodeLb(metrics: LbMetric[]): string {
  return metrics.map((m) => `${m.lowerBetter ? '-' : ''}${encodeURIComponent(m.key)}*${m.weight}`).join(',')
}

export function decodeLb(s: string | null | undefined): LbMetric[] {
  if (!s) return []
  return s
    .split(',')
    .map((part) => {
      const lower = part.startsWith('-')
      const body = lower ? part.slice(1) : part
      const i = body.lastIndexOf('*')
      const key = decodeURIComponent(i < 0 ? body : body.slice(0, i))
      const w = i < 0 ? 1 : Number(body.slice(i + 1))
      return { key, weight: Number.isFinite(w) && w >= 0 ? w : 1, lowerBetter: lower }
    })
    .filter((m) => m.key)
}

export interface LeaderboardColumnSpec {
  evaluation_object_ref: string
  scorer_name: string
  summary_metric_path: string
  should_minimize: boolean
}

/** Splits a compare metric into the scorer name and the path inside its summary. */
export function metricColumn(m: { key: string; scorer?: string; path?: string }): { scorer_name: string; summary_metric_path: string } {
  if (m.scorer) return { scorer_name: m.scorer, summary_metric_path: m.path || m.key.slice(m.scorer.length + 1) }
  const [head, ...rest] = m.key.split('.')
  return { scorer_name: head, summary_metric_path: rest.join('.') }
}

/**
 * Leaderboard columns for a dynamic (compare view) leaderboard: one column
 * per weighted metric and distinct evaluation, so the saved Leaderboard
 * ranks every model evaluated with those evaluations, not just these runs.
 */
export function leaderboardColumns(
  evals: { evaluation_ref: string }[],
  selected: LbMetric[],
  metrics: { key: string; scorer?: string; path?: string }[],
): LeaderboardColumnSpec[] {
  const refs = [...new Set(evals.map((e) => e.evaluation_ref).filter(Boolean))]
  const out: LeaderboardColumnSpec[] = []
  for (const ref of refs)
    for (const s of selected) {
      if (s.weight <= 0) continue
      const m = metrics.find((x) => x.key === s.key) ?? { key: s.key }
      out.push({ evaluation_object_ref: ref, ...metricColumn(m), should_minimize: s.lowerBetter })
    }
  return out
}
