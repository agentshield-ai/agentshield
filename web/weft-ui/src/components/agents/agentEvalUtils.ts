// Pure helpers for agent evals (unit tested in agents.test.ts).
import type { AgentEvalPrediction, AgentEvalRun } from '../../api/agents'
import type { Any } from '../../api/types'
import { flattenScores, fmtMetric } from '../evals/evalUtils'

/** Summary metrics worth a column: true_fraction over true_count, latency last. */
export function summaryMetrics(summary: Any): Record<string, number | boolean> {
  const flat = flattenScores(summary ?? {})
  const out: Record<string, number | boolean> = {}
  for (const [k, v] of Object.entries(flat)) {
    if (k.endsWith('.true_count') && `${k.slice(0, -'true_count'.length)}true_fraction` in flat) continue
    out[k] = v
  }
  return out
}

export function fmtScoreValue(k: string, v: unknown): string {
  if (typeof v === 'boolean') return v ? '✓' : '✗'
  if (k.endsWith('true_fraction') && typeof v === 'number') return `${(v * 100).toFixed(1)}%`
  return fmtMetric(v)
}

/** Trace id → the predictions whose spans include that trace. */
export function predictionsByTrace(runs: AgentEvalRun[] | undefined): Map<string, { run: AgentEvalRun; pred: AgentEvalPrediction }[]> {
  const out = new Map<string, { run: AgentEvalRun; pred: AgentEvalPrediction }[]>()
  for (const run of runs ?? [])
    for (const pred of run.predictions ?? [])
      for (const t of pred.trace_ids) {
        const xs = out.get(t) ?? []
        xs.push({ run, pred })
        out.set(t, xs)
      }
  return out
}
