// Stored span filters (saved views, dashboards) in Weft's names.
import type { Any, SpansFilter } from './types'

// Filter keys of earlier versions, as saved views and dashboards may still
// hold them (the native API refuses unknown filter keys).
const OLD_KEYS: Record<string, keyof SpansFilter> = {
  op_names: 'names',
  call_ids: 'ids',
  thread_ids: 'session_ids',
}

const KEYS: (keyof SpansFilter)[] = [
  'names',
  'ids',
  'trace_ids',
  'parent_ids',
  'session_ids',
  'turn_ids',
  'user_ids',
  'run_ids',
  'input_refs',
  'output_refs',
  'trace_roots_only',
]

/** A span filter with Weft's keys; keys it does not know are dropped. */
export function normaliseSpansFilter(f: Any): SpansFilter {
  if (!f || typeof f !== 'object') return {}
  const out: Record<string, Any> = {}
  for (const [k, v] of Object.entries(f)) {
    const key = OLD_KEYS[k] ?? k
    if ((KEYS as string[]).includes(key) && v !== undefined && v !== null) out[key] = v
  }
  return out as SpansFilter
}
