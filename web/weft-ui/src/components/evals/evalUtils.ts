import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { isRef, parseRef } from '../../api/refs'
import type { Any, Call, Operation } from '../../api/types'
import { and, contains, toQuery } from '../../lib/query'

export const EVAL_OP = 'Evaluation.evaluate'
export const PAS_OP = 'Evaluation.predict_and_score'

/** Flatten nested numeric / boolean leaves into dotted keys. */
export function flattenScores(v: Any, prefix = '', out: Record<string, number | boolean> = {}): Record<string, number | boolean> {
  if (typeof v === 'number' || typeof v === 'boolean') {
    out[prefix || 'value'] = v
    return out
  }
  if (v && typeof v === 'object' && !Array.isArray(v)) {
    for (const [k, x] of Object.entries(v)) {
      if (k.startsWith('_')) continue
      flattenScores(x, prefix ? `${prefix}.${k}` : k, out)
    }
  }
  return out
}

/** Flatten with string leaves too (for discovery of metric paths). */
export function flattenPaths(v: Any, prefix = ''): string[] {
  if (v && typeof v === 'object' && !Array.isArray(v))
    return Object.entries(v).flatMap(([k, x]) => (k.startsWith('_') ? [] : flattenPaths(x, prefix ? `${prefix}.${k}` : k)))
  return typeof v === 'number' || typeof v === 'boolean' ? [prefix] : []
}

export function sortMetricKeys(keys: string[]): string[] {
  const rank = (k: string) => (k.startsWith('model_latency') ? 2 : k.includes('latency') || k.includes('tokens') ? 1 : 0)
  return keys.slice().sort((a, b) => rank(a) - rank(b) || a.localeCompare(b))
}

/** Default direction for a metric: true when lower values are better. */
export function defaultLowerIsBetter(key: string): boolean {
  return /latency|loss|error|cost|tokens|distance|duration|time|wrong|toxic/i.test(key)
}

export function fmtMetric(v: unknown): string {
  if (v == null) return '—'
  if (typeof v === 'boolean') return v ? 'true' : 'false'
  if (typeof v === 'number') {
    if (Number.isInteger(v)) return v.toLocaleString()
    if (Math.abs(v) >= 100) return v.toFixed(1)
    return v.toFixed(3)
  }
  return String(v)
}

export const toNum = (v: unknown): number | null => (typeof v === 'number' ? v : typeof v === 'boolean' ? (v ? 1 : 0) : null)

export interface EvalRunInfo {
  evalRef: string | null
  evalName: string
  modelRef: string | null
  modelName: string
  summary: Any
}

export function evalRunInfo(c: Call): EvalRunInfo {
  const self = c.inputs?.self
  const model = c.inputs?.model
  const evalRef = typeof self === 'string' && isRef(self) ? self : null
  const modelRef = typeof model === 'string' && isRef(model) ? model : null
  return {
    evalRef,
    evalName: evalRef ? (parseRef(evalRef)?.name ?? evalRef) : (self?.name ?? 'evaluation'),
    modelRef,
    modelName: modelRef ? (parseRef(modelRef)?.name ?? modelRef) : (model?.name ?? (typeof model === 'string' ? model : 'model')),
    summary: c.output,
  }
}

/** Prefix that matches every version of an object ref: weft:///e/p/object/name: */
export const objectRefPrefix = (projectId: string, name: string) => `/${projectId}/object/${name}:`

export function useEvalRuns(
  projectId: string,
  opts: { evalName?: string; modelName?: string; limit?: number; enabled?: boolean; pollWhileRunning?: boolean } = {},
) {
  const q = and(
    opts.evalName ? contains('inputs.self', objectRefPrefix(projectId, opts.evalName), false) : null,
    opts.modelName ? contains('inputs.model', objectRefPrefix(projectId, opts.modelName), false) : null,
  )
  return useQuery({
    queryKey: ['eval-runs', projectId, opts.evalName ?? null, opts.modelName ?? null, opts.limit ?? 200],
    queryFn: () =>
      api
        .callsQuery({
          project_id: projectId,
          filter: { op_names: [EVAL_OP] },
          query: toQuery(q as Operation | null),
          sort_by: [{ field: 'started_at', direction: 'desc' }],
          limit: opts.limit ?? 200,
          include_costs: true,
        })
        .then((r) =>
          // Guard against substring over-matching (e.g. "qa:" inside "my-qa:").
          r.calls.filter((c) => {
            const i = evalRunInfo(c)
            return (!opts.evalName || i.evalName === opts.evalName) && (!opts.modelName || i.modelName === opts.modelName)
          }),
        ),
    enabled: opts.enabled ?? true,
    refetchInterval: (q) => (opts.pollWhileRunning && q.state.data?.some((c) => !c.ended_at) ? 3000 : false),
  })
}

/** Row counts of the datasets behind a set of evaluation refs, keyed by evaluation ref. */
export function useDatasetSizes(evalRefs: string[]) {
  const refs = [...new Set(evalRefs)].sort()
  return useQuery({
    queryKey: ['eval-dataset-sizes', refs],
    enabled: refs.length > 0,
    staleTime: 60_000,
    queryFn: async () => {
      const out: Record<string, number | null> = {}
      const evals = await api.refsReadBatch(refs)
      const dsRefs = evals.vals.map((v) => (typeof v?.dataset === 'string' ? v.dataset : null))
      const uniqDs = [...new Set(dsRefs.filter(Boolean) as string[])]
      const dsVals = uniqDs.length ? (await api.refsReadBatch(uniqDs)).vals : []
      const sizeByDs: Record<string, number | null> = {}
      await Promise.all(
        uniqDs.map(async (d, i) => {
          const rows = dsVals[i]?.rows
          const p = typeof rows === 'string' ? parseRef(rows) : null
          if (!p) return (sizeByDs[d] = Array.isArray(rows) ? rows.length : null)
          try {
            sizeByDs[d] = (await api.tableQueryStats(p.projectId, p.name)).count
          } catch {
            sizeByDs[d] = null
          }
        }),
      )
      refs.forEach((r, i) => (out[r] = dsRefs[i] ? (sizeByDs[dsRefs[i]!] ?? null) : null))
      return out
    },
  })
}

export const SERIES_COLORS = ['var(--c1)', 'var(--c2)', 'var(--c3)', 'var(--c4)', 'var(--c5)', 'var(--c6)']
export const seriesColor = (i: number) => SERIES_COLORS[i % SERIES_COLORS.length]
