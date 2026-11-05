// Which models the server can run in /api/v1/evaluations/run (unit tested in evals.test.ts).
import type { Any } from '../../api/types'

export type ServerModelKind = 'llm' | 'remote' | 'runtime'

/**
 * The server runs LLMStructuredCompletionModel models, models with a
 * `predict_url`, and models bound to a runtime (`runtime` = name or ref).
 */
export function serverModelKind(o: { value?: Any } | null | undefined): ServerModelKind | null {
  const v = o?.value
  if (!v || typeof v !== 'object') return null
  const cls = [v._class_name, ...(Array.isArray(v._bases) ? v._bases : [])]
  if (cls.includes('LLMStructuredCompletionModel')) return 'llm'
  if (typeof v.predict_url === 'string' && /^https?:\/\//i.test(v.predict_url)) return 'remote'
  if (typeof v.runtime === 'string' && v.runtime.trim()) return 'runtime'
  return null
}

/** One-line description of how a model runs. */
export function serverModelSummary(o: { value?: Any }): string {
  const v = o.value ?? {}
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

/** Offline test models, in words. */
const MOCK_WORDS: Record<string, string> = { 'mock/echo': 'an offline test model that repeats the prompt', 'mock/json': 'an offline test judge' }

/** How a model runs, in plain words for the model picker: "calls gpt-4o-mini", "an offline test model…", "calls your endpoint at …", "runs on runtime x". */
export function serverModelWords(o: { value?: Any }): string {
  const v = o.value ?? {}
  switch (serverModelKind(o)) {
    case 'llm': {
      const id = String(v.llm_model_id ?? '')
      return MOCK_WORDS[id] ?? (id ? `calls ${id}` : 'calls an AI provider')
    }
    case 'remote':
      return `calls your endpoint at ${v.predict_url}`
    case 'runtime':
      return `runs on the ${String(v.runtime).replace(/^weft:\/\/\/[^/]+\/[^/]+\/object\//, '').replace(/:.*$/, '')} runtime`
  }
  return 'cannot run on the server'
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
