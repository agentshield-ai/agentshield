// The range each scorer says its numbers run over, read from the stored
// scorer object (an explicit scale or min/max, else its prompt's "1-5").
// Shared by Scores and a monitor's page, so both read a scorer the same way.
import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { scaleFromText, type Scale } from '../../lib/summaries/evaluate'

/** A scorer object's own scale: `scale: {min, max}`, `min_score`/`max_score`, or one named in its prompt or description. */
export function scorerScale(v: unknown): Scale | null {
  if (!v || typeof v !== 'object') return null
  const o = v as Record<string, unknown>
  const sc = o.scale as Record<string, unknown> | undefined
  if (sc && typeof sc.min === 'number' && typeof sc.max === 'number' && sc.max > sc.min) return { min: sc.min, max: sc.max }
  if (typeof o.min_score === 'number' && typeof o.max_score === 'number' && o.max_score > o.min_score) return { min: o.min_score, max: o.max_score }
  for (const k of ['scoring_prompt', 'prompt', 'system_prompt', 'description']) {
    const s = typeof o[k] === 'string' ? scaleFromText(o[k] as string) : null
    if (s) return s
  }
  return null
}

/** Each named scorer's own scale (scorers with no stored object, or that state none, are left out). One query, no 404s. */
export function useScorerScales(projectId: string, names: string[]): Record<string, Scale | null> {
  const sorted = [...new Set(names)].sort().slice(0, 50)
  const q = useQuery({
    queryKey: ['objects', projectId, 'scorer-scales', sorted],
    queryFn: () => api.objectsQuery({ project_id: projectId, filter: { names: sorted, latest_only: true }, limit: sorted.length }),
    enabled: sorted.length > 0,
    staleTime: 60_000,
  })
  return useMemo(() => {
    const out: Record<string, Scale | null> = {}
    for (const o of q.data?.items ?? []) {
      const s = scorerScale(o.value)
      if (s) out[o.name] = s
    }
    return out
  }, [q.data])
}
