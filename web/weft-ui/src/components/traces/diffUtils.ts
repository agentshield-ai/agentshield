// Value diffs for comparing calls against a baseline (unit tested).

export interface NumericDelta {
  delta: number
  /** Relative change in percent; null when the baseline is 0. */
  pct: number | null
}

export function asNumber(v: unknown): number | null {
  if (typeof v === 'number' && Number.isFinite(v)) return v
  if (typeof v === 'boolean') return v ? 1 : 0
  return null
}

/** Change from `base` to `v`, or null when either side is not a number. */
export function numericDelta(base: unknown, v: unknown): NumericDelta | null {
  const a = typeof base === 'boolean' ? null : asNumber(base)
  const b = typeof v === 'boolean' ? null : asNumber(v)
  if (a == null || b == null) return null
  const delta = b - a
  return { delta, pct: a === 0 ? (delta === 0 ? 0 : null) : (delta / Math.abs(a)) * 100 }
}

export function fmtDelta(d: NumericDelta, fmt: (n: number) => string = fmtPlain): string {
  if (d.delta === 0) return '±0'
  const sign = d.delta > 0 ? '+' : '−'
  const pct = d.pct == null ? '' : ` (${sign}${Math.abs(d.pct) >= 100 ? Math.round(Math.abs(d.pct)) : Math.abs(d.pct).toFixed(1)}%)`
  return `${sign}${fmt(Math.abs(d.delta))}${pct}`
}

/** How numeric changes against the baseline are shown in compare views. */
export type DeltaFormat = 'both' | 'absolute' | 'percent' | 'ratio'
export const DELTA_FORMATS: { id: DeltaFormat; label: string; title: string }[] = [
  { id: 'both', label: 'Δ + %', title: 'Absolute change and percentage' },
  { id: 'absolute', label: 'Δ', title: 'Absolute change' },
  { id: 'percent', label: '%', title: 'Percentage change' },
  { id: 'ratio', label: '×', title: 'Ratio to the baseline' },
]

export function parseDeltaFormat(s: string | null | undefined): DeltaFormat {
  return DELTA_FORMATS.some((f) => f.id === s) ? (s as DeltaFormat) : 'both'
}

function fmtPct(p: number): string {
  const a = Math.abs(p)
  return `${p > 0 ? '+' : p < 0 ? '−' : '±'}${a >= 100 ? Math.round(a) : a.toFixed(1)}%`
}

/**
 * Formats the change from `base` to `v`: absolute ("+50"), percentage
 * ("+50.0%"), ratio ("1.50×") or both ("+50 (+50.0%)"). Null when either
 * side is not a number; "—" when the percentage or ratio is undefined
 * because the baseline is 0.
 */
export function fmtDeltaAs(base: unknown, v: unknown, format: DeltaFormat, fmt: (n: number) => string = fmtPlain): string | null {
  const d = numericDelta(base, v)
  if (!d) return null
  switch (format) {
    case 'absolute':
      return d.delta === 0 ? '±0' : `${d.delta > 0 ? '+' : '−'}${fmt(Math.abs(d.delta))}`
    case 'percent':
      return d.pct == null ? '—' : fmtPct(d.pct)
    case 'ratio': {
      const a = base as number
      if (a === 0) return d.delta === 0 ? '1×' : '—'
      const r = (v as number) / a
      return `${Number(r.toPrecision(3)).toLocaleString()}×`
    }
  }
  return fmtDelta(d, fmt)
}

function fmtPlain(n: number): string {
  if (Number.isInteger(n)) return n.toLocaleString()
  return Number(n.toPrecision(4)).toLocaleString()
}

export type DiffOp = { op: 'same' | 'add' | 'del'; text: string }

const MAX_TOKENS = 3000

/** Split into words and the whitespace/punctuation between them, so joins reproduce the input. */
export function tokenize(s: string): string[] {
  return s.match(/\s+|[\p{L}\p{N}_]+|[^\s\p{L}\p{N}_]/gu) ?? []
}

/** Word-level diff from `a` (baseline) to `b` using an LCS table. */
export function diffStrings(a: string, b: string): DiffOp[] {
  if (a === b) return a ? [{ op: 'same', text: a }] : []
  const x = tokenize(a)
  const y = tokenize(b)
  // Trim the common prefix and suffix to keep the table small.
  let pre = 0
  while (pre < x.length && pre < y.length && x[pre] === y[pre]) pre++
  let suf = 0
  while (suf < x.length - pre && suf < y.length - pre && x[x.length - 1 - suf] === y[y.length - 1 - suf]) suf++
  const xs = x.slice(pre, x.length - suf)
  const ys = y.slice(pre, y.length - suf)
  const out: DiffOp[] = []
  const push = (op: DiffOp['op'], text: string) => {
    if (!text) return
    const last = out[out.length - 1]
    if (last && last.op === op) last.text += text
    else out.push({ op, text })
  }
  push('same', x.slice(0, pre).join(''))
  if (xs.length * ys.length > MAX_TOKENS * MAX_TOKENS / 4 || xs.length > MAX_TOKENS || ys.length > MAX_TOKENS) {
    push('del', xs.join(''))
    push('add', ys.join(''))
  } else {
    const n = xs.length
    const m = ys.length
    const L: Uint32Array[] = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1))
    for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) L[i][j] = xs[i] === ys[j] ? L[i + 1][j + 1] + 1 : Math.max(L[i + 1][j], L[i][j + 1])
    let i = 0
    let j = 0
    while (i < n && j < m) {
      if (xs[i] === ys[j]) {
        push('same', xs[i])
        i++
        j++
      } else if (L[i + 1][j] >= L[i][j + 1]) push('del', xs[i++])
      else push('add', ys[j++])
    }
    while (i < n) push('del', xs[i++])
    while (j < m) push('add', ys[j++])
  }
  push('same', x.slice(x.length - suf).join(''))
  return out
}

/** Share of the text that is unchanged (0..1), for summarising string diffs. */
export function similarity(ops: DiffOp[]): number {
  let same = 0
  let total = 0
  for (const o of ops) {
    if (o.op === 'same') same += o.text.length * 2
    total += o.text.length * (o.op === 'same' ? 2 : 1)
  }
  return total ? same / total : 1
}

export type LeafStatus = 'same' | 'changed' | 'added' | 'removed'

export interface LeafDiff {
  path: string
  base: unknown
  value: unknown
  status: LeafStatus
  numeric: NumericDelta | null
}

/** Flatten to dotted leaves (arrays by index), stopping at `maxDepth`. */
export function flattenLeaves(v: unknown, prefix = '', out: Map<string, unknown> = new Map(), depth = 0, maxDepth = 6): Map<string, unknown> {
  if (v && typeof v === 'object' && depth < maxDepth) {
    const entries = Array.isArray(v) ? v.map((x, i) => [String(i), x] as const) : Object.entries(v as Record<string, unknown>)
    if (!entries.length) out.set(prefix, v)
    for (const [k, x] of entries) flattenLeaves(x, prefix ? `${prefix}.${k}` : k, out, depth + 1, maxDepth)
    return out
  }
  out.set(prefix, v)
  return out
}

const same = (a: unknown, b: unknown) => a === b || JSON.stringify(a) === JSON.stringify(b)

/** Leaf-by-leaf comparison of `value` against `base`. */
export function leafDiffs(base: unknown, value: unknown): LeafDiff[] {
  const a = flattenLeaves(base)
  const b = flattenLeaves(value)
  const keys = [...new Set([...a.keys(), ...b.keys()])]
  return keys.map((path) => {
    const hasA = a.has(path)
    const hasB = b.has(path)
    const av = a.get(path)
    const bv = b.get(path)
    const status: LeafStatus = !hasA ? 'added' : !hasB ? 'removed' : same(av, bv) ? 'same' : 'changed'
    return { path, base: av, value: bv, status, numeric: status === 'changed' ? numericDelta(av, bv) : null }
  })
}
