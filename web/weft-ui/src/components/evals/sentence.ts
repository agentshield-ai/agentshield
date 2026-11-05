// Glue between the Evaluate pages and their generated words
// (lib/summaries/evaluate.ts): a run span as the summaries read it, and a
// sentence's clauses as links or actions.
import type { LeaderboardComputeRes, Span } from '../../api/types'
import type { SentencePart } from '../ui/Sentence'
import { callStatus } from '../../lib/format'
import { compareEvaluationsUrl } from '../../lib/urls'
import { evaluationsSentence, metricWords, type EvalLink, type EvalPart, type LbColumnLike, type LbRowLike, type RunInfo } from '../../lib/summaries/evaluate'
import { evalRunInfo } from './evalUtils'

/** An Evaluation.evaluate span as a RunInfo (rows: the dataset's size, when known). */
export function runInfoOf(c: Span, sizes?: Record<string, number | null>): RunInfo {
  const i = evalRunInfo(c)
  return {
    id: c.id,
    evaluation: i.evalName,
    model: i.modelName,
    status: callStatus(c),
    started_at: c.started_at,
    ended_at: c.ended_at,
    summary: c.output,
    error: c.error ?? null,
    rows: i.evalRef ? (sizes?.[i.evalRef] ?? null) : null,
  }
}

/** Sentence parts with each clause resolved by `resolve` into a link (`to`) or an action (`onClick`). */
export function toSentence(parts: EvalPart[], resolve: (l: EvalLink) => { to?: string; onClick?: () => void; title?: string } | null): SentencePart[] {
  return parts.map((p) => {
    if (typeof p === 'string') return p
    const r = resolve(p.link)
    return r ? { text: p.text, ...r } : p.text
  })
}

/** The Evaluations sentence with its clauses linked: changes to their comparison, failed or running runs to the status filter. */
export function evaluationSentenceParts(runs: RunInfo[], filtered: boolean, projectId: string, onStatus: (s: 'error' | 'running') => void): SentencePart[] {
  return toSentence(evaluationsSentence(runs, filtered), (l) => {
    if (l.kind === 'compare') return { to: `${compareEvaluationsUrl(projectId, l.ids)}${l.baseline ? `&baseline=${encodeURIComponent(l.baseline)}` : ''}`, title: 'Compare the two runs' }
    if (l.kind === 'runs') return { onClick: () => onStatus(l.status), title: 'Show only these runs' }
    return null
  })
}

/** A leaderboard's computed columns and rows as the summaries read them. */
export function lbShapes(data: LeaderboardComputeRes): { columns: LbColumnLike[]; rows: LbRowLike[] } {
  const many = new Set(data.columns.map((c) => c.evaluation_name)).size > 1
  return {
    columns: data.columns.map((c) => ({
      index: c.index,
      name: `${metricWords(`${c.scorer_name}.${c.summary_metric_path}`)}${many ? ` (${c.evaluation_name})` : ''}`,
      minimize: c.should_minimize,
    })),
    rows: data.rows.map((r) => ({
      model: r.model_name,
      values: Object.fromEntries(Object.entries(r.values).map(([k, v]) => [k, typeof v.value === 'number' ? v.value : typeof v.value === 'boolean' ? (v.value ? 1 : 0) : null])),
    })),
  }
}
