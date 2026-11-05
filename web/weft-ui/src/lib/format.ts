import type { Span, SpanStatus } from '../api/types'

/** A short duration, the one format for figures everywhere: "<1 ms",
 * "150 ms", "2.5 s", "9 s", "1 min 5 s", "2 h 3 min". */
export function fmtDuration(ms: number | null | undefined): string {
  if (ms == null || !Number.isFinite(ms) || ms < 0) return '—'
  if (ms < 1) return '<1 ms'
  if (ms < 1000) return `${Math.round(ms)} ms`
  const trim = (x: number, d: number) => String(Number(x.toFixed(d)))
  if (ms < 60_000) return `${trim(ms / 1000, ms < 10_000 ? 1 : 0)} s`
  const totalS = Math.round(ms / 1000)
  if (totalS < 3600) {
    const m = Math.floor(totalS / 60)
    const s = totalS % 60
    return s ? `${m} min ${s} s` : `${m} min`
  }
  const totalM = Math.round(totalS / 60)
  return totalM % 60 ? `${Math.floor(totalM / 60)} h ${totalM % 60} min` : `${Math.floor(totalM / 60)} h`
}

export function fmtDate(iso: string | null | undefined): string {
  if (!iso) return '—'
  const d = new Date(iso)
  return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

/** A relative time, the one format everywhere: "just now", "50 min ago",
 * "2 h ago", "1 day ago", "12 days ago". */
export function fmtRelative(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return '—'
  const diff = Math.max(0, now - new Date(iso).getTime())
  const m = Math.round(diff / 60_000)
  if (m < 1) return 'just now'
  if (m < 60) return `${m} min ago`
  const h = Math.round(m / 60)
  if (h < 24) return `${h} h ago`
  const d = Math.round(h / 24)
  return `${d} ${d === 1 ? 'day' : 'days'} ago`
}

/** A saved version as people read it. The API counts from 0 (`v0`); people
 * count from 1, so API version 2 is "version 3". Use this wherever a version
 * is shown in words; the exact ref (`name:v2`) appears only where code is
 * shown (the inspector's "In code" row). */
export function versionLabel(v: number | string | null | undefined): string {
  const n = typeof v === 'string' ? Number(v.replace(/^v/, '')) : v
  if (n == null || !Number.isFinite(n)) return ''
  return `version ${n + 1}`
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

export function callStatus(c: Span): SpanStatus {
  return c.summary?.weft?.status ?? (c.error ? 'error' : c.ended_at ? 'success' : 'running')
}

export function callLatencyMs(c: Span): number | null {
  if (c.summary?.weft?.latency_ms != null) return c.summary.weft.latency_ms
  if (!c.ended_at) return null
  return new Date(c.ended_at).getTime() - new Date(c.started_at).getTime()
}

export function callTokens(c: Span): { prompt: number; completion: number; total: number } {
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

export function callCost(c: Span): number | null {
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

/** "2 flagged of 5 tool calls" (the detection rate's numerator and denominator). */
export function flaggedOf(flagged: number | undefined, toolCalls: number): string {
  return `${flagged ?? 0} flagged of ${toolCalls} tool call${toolCalls === 1 ? '' : 's'}`
}
