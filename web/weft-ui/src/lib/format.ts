import type { Call, CallStatus } from '../api/types'

export function fmtDuration(ms: number | null | undefined): string {
  if (ms == null || Number.isNaN(ms)) return '—'
  if (ms < 1) return '<1ms'
  if (ms < 1000) return `${Math.round(ms)}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 2 : 1)}s`
  const m = Math.floor(ms / 60_000)
  const s = Math.round((ms % 60_000) / 1000)
  return `${m}m ${s}s`
}

export function fmtDate(iso: string | null | undefined): string {
  if (!iso) return '—'
  const d = new Date(iso)
  return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

export function fmtRelative(iso: string | null | undefined): string {
  if (!iso) return '—'
  const diff = Date.now() - new Date(iso).getTime()
  const s = Math.round(diff / 1000)
  if (s < 60) return `${s}s ago`
  const m = Math.round(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.round(m / 60)
  if (h < 48) return `${h}h ago`
  return `${Math.round(h / 24)}d ago`
}

export function fmtNumber(n: number | null | undefined, digits = 0): string {
  if (n == null || Number.isNaN(n)) return '—'
  return n.toLocaleString(undefined, { maximumFractionDigits: digits })
}

/** Money in US dollars, the one format used everywhere: two significant
 * digits below $1 ($0.0048, $0.12), cents from $1 ($1,234.50). Floating-point
 * noise never shows (0.0048000000000000004 gives $0.0048). */
export function fmtCost(usd: number | null | undefined): string {
  if (usd == null || !Number.isFinite(usd)) return '—'
  if (usd === 0) return '$0'
  const sign = usd < 0 ? '−' : ''
  const a = Math.abs(usd)
  if (a < 0.0001) return `${sign}<$0.0001`
  if (a < 1) {
    // Two significant digits; trailing zeros past the cents go (0.0040 → 0.004).
    const s = a.toFixed(Math.max(2, 1 - Math.floor(Math.log10(a))))
    const [i, f] = s.split('.')
    let frac = f
    while (frac.length > 2 && frac.endsWith('0')) frac = frac.slice(0, -1)
    return `${sign}$${i}.${frac}`
  }
  return `${sign}$${a.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`
}

export function fmtBytes(n: number | null | undefined): string {
  if (n == null) return '—'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = n
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${u[i]}`
}

export function fmtScore(v: unknown): string {
  if (v == null) return '—'
  if (typeof v === 'boolean') return v ? 'true' : 'false'
  if (typeof v === 'number') return Number.isInteger(v) ? String(v) : v.toFixed(3)
  return typeof v === 'string' ? v : JSON.stringify(v)
}

export function callStatus(c: Call): CallStatus {
  return c.summary?.weft?.status ?? (c.exception ? 'error' : c.ended_at ? 'success' : 'running')
}

export function callLatencyMs(c: Call): number | null {
  if (c.summary?.weft?.latency_ms != null) return c.summary.weft.latency_ms
  if (!c.ended_at) return null
  return new Date(c.ended_at).getTime() - new Date(c.started_at).getTime()
}

export function callTokens(c: Call): { prompt: number; completion: number; total: number } {
  let prompt = 0
  let completion = 0
  let total = 0
  for (const u of Object.values(c.summary?.usage ?? {})) {
    const p = u.prompt_tokens ?? u.input_tokens ?? 0
    const o = u.completion_tokens ?? u.output_tokens ?? 0
    prompt += p
    completion += o
    total += u.total_tokens ?? p + o
  }
  return { prompt, completion, total }
}

export function callCost(c: Call): number | null {
  const costs = c.summary?.weft?.costs
  if (!costs) return null
  let t = 0
  for (const v of Object.values(costs)) t += (v.prompt_tokens_total_cost ?? 0) + (v.completion_tokens_total_cost ?? 0)
  return t
}

/** Read a dotted path out of a nested value. */
export function getPath(obj: unknown, path: string): unknown {
  let cur: unknown = obj
  for (const part of path.split('.')) {
    if (cur == null || typeof cur !== 'object') return undefined
    cur = (cur as Record<string, unknown>)[part]
  }
  return cur
}

export function truncate(s: string, n: number): string {
  return s.length > n ? s.slice(0, n - 1) + '…' : s
}

/** One-line preview of an arbitrary value. */
export function preview(v: unknown, n = 120): string {
  if (v == null) return ''
  if (typeof v === 'string') return truncate(v.replace(/\s+/g, ' '), n)
  try {
    return truncate(JSON.stringify(v), n)
  } catch {
    return String(v)
  }
}
