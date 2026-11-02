import type { StatusCode } from '../../api/agents'

export type SpanCategory = 'agent' | 'llm' | 'tool' | 'user' | 'other'

/** Display category for a normalised operation name. */
export function opCategory(op: string | null | undefined): SpanCategory {
  switch (op) {
    case 'invoke_agent':
    case 'create_agent':
      return 'agent'
    case 'chat':
    case 'text_completion':
    case 'generate_content':
    case 'embeddings':
      return 'llm'
    case 'execute_tool':
    case 'retrieval':
      return 'tool'
  }
  return 'other'
}

export const CATEGORY_LABEL: Record<SpanCategory, string> = {
  agent: 'Agent',
  llm: 'LLM',
  tool: 'Tool',
  user: 'User',
  other: 'Other',
}

export function kindClass(op: string | null | undefined, status?: StatusCode | null): string {
  if (status === 'ERROR') return 'kind-error'
  return `kind-${opCategory(op)}`
}

/** Pretty-print a JSON string when it parses. */
export function parseMaybeJSON(s: string | null | undefined): unknown {
  if (s == null) return null
  const t = s.trim()
  if (t.startsWith('{') || t.startsWith('[') || t.startsWith('"')) {
    try {
      return JSON.parse(t)
    } catch {
      return s
    }
  }
  return s
}

export function errorRate(errors: number, total: number): number | null {
  return total > 0 ? errors / total : null
}

export function fmtPct(v: number | null | undefined): string {
  if (v == null) return '—'
  if (v === 0) return '0%'
  if (v < 0.001) return '<0.1%'
  return `${(v * 100).toFixed(v < 0.1 ? 1 : 0)}%`
}

export function compact(n: number | null | undefined): string {
  if (n == null) return '—'
  return Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 }).format(n)
}

/** ISO start of a look-back window; undefined for "all" (no lower bound). */
export function windowStartOrAll(range: string): string | undefined {
  return range === 'all' ? undefined : windowStart(range)
}

/** ISO start of a look-back window. */
export function windowStart(range: string): string {
  const ms: Record<string, number> = { '1h': 3_600_000, '6h': 6 * 3_600_000, '24h': 86_400_000, '7d': 7 * 86_400_000, '30d': 30 * 86_400_000 }
  return new Date(Date.now() - (ms[range] ?? ms['7d'])).toISOString()
}

export const RANGES = ['1h', '6h', '24h', '7d', '30d'] as const
export type Range = (typeof RANGES)[number]

export function granularityFor(range: string): number {
  switch (range) {
    case '1h':
      return 60
    case '6h':
      return 600
    case '24h':
      return 1800
    case '30d':
      return 86_400
  }
  return 3 * 3600
}
