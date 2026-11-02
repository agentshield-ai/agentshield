// Which models the server can run in /evaluate_model (unit tested in evals.test.ts).
import type { Any } from '../../api/types'

export type ServerModelKind = 'llm' | 'remote' | 'runtime'

/**
 * The server runs LLMStructuredCompletionModel models, models with a
 * `predict_url`, and models bound to a runtime (`runtime` = name or ref).
 */
export function serverModelKind(o: { val?: Any } | null | undefined): ServerModelKind | null {
  const v = o?.val
  if (!v || typeof v !== 'object') return null
  const cls = [v._class_name, ...(Array.isArray(v._bases) ? v._bases : [])]
  if (cls.includes('LLMStructuredCompletionModel')) return 'llm'
  if (typeof v.predict_url === 'string' && /^https?:\/\//i.test(v.predict_url)) return 'remote'
  if (typeof v.runtime === 'string' && v.runtime.trim()) return 'runtime'
  return null
}

/** One-line description of how a model runs. */
export function serverModelSummary(o: { val?: Any }): string {
  const v = o.val ?? {}
  switch (serverModelKind(o)) {
    case 'llm':
      return String(v.llm_model_id ?? 'LLM')
    case 'remote':
      return `POST ${v.predict_url}`
    case 'runtime':
      return `runtime ${String(v.runtime).replace(/^weft:\/\/\/[^/]+\/[^/]+\/object\//, '')}${v.runtime_id ? ` / ${v.runtime_id}` : ''}`
  }
  return 'not runnable on the server'
}

/** Validates a predict URL for a remote model. */
export function predictUrlError(u: string): string | null {
  if (!u.trim()) return 'Enter the URL the server should POST rows to.'
  try {
    const url = new URL(u.trim())
    if (url.protocol !== 'http:' && url.protocol !== 'https:') return 'Use an http or https URL.'
  } catch {
    return 'That is not a valid URL.'
  }
  return null
}

/** `{column}` placeholders in a prompt template. */
export function promptColumns(t: string): string[] {
  return [...new Set([...t.matchAll(/\{([A-Za-z_][\w.]*)\}/g)].map((m) => m[1]))]
}
