import { Fragment, useMemo, useState } from 'react'
import type { Any } from '../../api/types'
import { preview } from '../../lib/format'

// ------------------------------------------------------------- line diff (LCS)

export interface DiffOp {
  type: 'ctx' | 'add' | 'del'
  a?: number // 1-based line in left
  b?: number // 1-based line in right
  text: string
}

export function lineDiff(a: string[], b: string[]): DiffOp[] {
  // Trim common prefix/suffix to keep the DP small.
  let start = 0
  while (start < a.length && start < b.length && a[start] === b[start]) start++
  let endA = a.length
  let endB = b.length
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) {
    endA--
    endB--
  }
  const ops: DiffOp[] = []
  for (let i = 0; i < start; i++) ops.push({ type: 'ctx', a: i + 1, b: i + 1, text: a[i] })
  const A = a.slice(start, endA)
  const B = b.slice(start, endB)
  const n = A.length
  const m = B.length
  if (n * m > 4_000_000) {
    A.forEach((t, i) => ops.push({ type: 'del', a: start + i + 1, text: t }))
    B.forEach((t, j) => ops.push({ type: 'add', b: start + j + 1, text: t }))
  } else {
    const dp: Uint32Array[] = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1))
    for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) dp[i][j] = A[i] === B[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1])
    let i = 0
    let j = 0
    while (i < n || j < m) {
      if (i < n && j < m && A[i] === B[j]) {
        ops.push({ type: 'ctx', a: start + i + 1, b: start + j + 1, text: A[i] })
        i++
        j++
      } else if (j < m && (i >= n || dp[i][j + 1] >= dp[i + 1][j])) {
        ops.push({ type: 'add', b: start + j + 1, text: B[j] })
        j++
      } else {
        ops.push({ type: 'del', a: start + i + 1, text: A[i] })
        i++
      }
    }
  }
  for (let k = 0; k < a.length - endA; k++) ops.push({ type: 'ctx', a: endA + k + 1, b: endB + k + 1, text: a[endA + k] })
  return ops
}

type Block = { kind: 'ops'; ops: DiffOp[] } | { kind: 'gap'; ops: DiffOp[]; id: number }

function collapse(ops: DiffOp[], context: number): Block[] {
  const blocks: Block[] = []
  let cur: DiffOp[] = []
  let i = 0
  let gapId = 0
  while (i < ops.length) {
    if (ops[i].type !== 'ctx') {
      cur.push(ops[i++])
      continue
    }
    let j = i
    while (j < ops.length && ops[j].type === 'ctx') j++
    const run = ops.slice(i, j)
    const head = i === 0 ? 0 : context
    const tail = j === ops.length ? 0 : context
    if (run.length > head + tail + 2) {
      cur.push(...run.slice(0, head))
      blocks.push({ kind: 'ops', ops: cur })
      blocks.push({ kind: 'gap', ops: run.slice(head, run.length - tail), id: gapId++ })
      cur = run.slice(run.length - tail)
    } else cur.push(...run)
    i = j
  }
  if (cur.length) blocks.push({ kind: 'ops', ops: cur })
  return blocks
}

function Line({ op, side }: { op: DiffOp | null; side?: 'a' | 'b' }) {
  if (!op)
    return (
      <div className="diff-line blank">
        <span className="ln" />
        <span className="tx" />
      </div>
    )
  const n = side === 'a' ? op.a : side === 'b' ? op.b : (op.b ?? op.a)
  return (
    <div className={`diff-line ${op.type}`}>
      <span className="ln">{n}</span>
      <span className="tx">{op.text}</span>
    </div>
  )
}

/** Pair consecutive del/add runs into side-by-side rows. */
function sideRows(ops: DiffOp[]): [DiffOp | null, DiffOp | null][] {
  const rows: [DiffOp | null, DiffOp | null][] = []
  let i = 0
  while (i < ops.length) {
    if (ops[i].type === 'ctx') {
      rows.push([ops[i], ops[i]])
      i++
      continue
    }
    const dels: DiffOp[] = []
    const adds: DiffOp[] = []
    while (i < ops.length && ops[i].type !== 'ctx') (ops[i].type === 'del' ? dels : adds).push(ops[i++])
    for (let k = 0; k < Math.max(dels.length, adds.length); k++) rows.push([dels[k] ?? null, adds[k] ?? null])
  }
  return rows
}

export function TextDiff({ left, right, mode, context = 3 }: { left: string; right: string; mode: 'unified' | 'split'; context?: number }) {
  const ops = useMemo(() => lineDiff(left.split('\n'), right.split('\n')), [left, right])
  const blocks = useMemo(() => collapse(ops, context), [ops, context])
  const [opened, setOpened] = useState<Set<number>>(new Set())
  const changed = ops.some((o) => o.type !== 'ctx')
  if (!changed) return <div className="faint small">No differences.</div>
  return (
    <div className="code-block" style={{ padding: 0, maxHeight: 'none' }}>
      {blocks.map((b, bi) => {
        if (b.kind === 'gap' && !opened.has(b.id))
          return (
            <div key={bi} className="diff-gap" onClick={() => setOpened(new Set([...opened, b.id]))}>
              ⋯ {b.ops.length} unchanged lines
            </div>
          )
        if (mode === 'unified') return b.ops.map((op, i) => <Line key={`${bi}-${i}`} op={op} />)
        return (
          <div key={bi} style={{ display: 'grid', gridTemplateColumns: 'minmax(0,1fr) minmax(0,1fr)' }}>
            {sideRows(b.ops).map(([l, r], i) => (
              <Fragment key={i}>
                <div style={{ borderRight: '1px solid var(--border)', minWidth: 0 }}>
                  <Line op={l && l.type === 'add' ? null : l} side="a" />
                </div>
                <div style={{ minWidth: 0 }}>
                  <Line op={r && r.type === 'del' ? null : r} side="b" />
                </div>
              </Fragment>
            ))}
          </div>
        )
      })}
    </div>
  )
}

// ------------------------------------------------------------- path diff

export interface PathChange {
  path: string
  kind: 'added' | 'removed' | 'changed'
  a?: Any
  b?: Any
}

function leaves(v: Any, prefix: string, out: Map<string, Any>) {
  if (v && typeof v === 'object') {
    const entries = Array.isArray(v) ? v.map((x, i) => [String(i), x] as const) : Object.entries(v)
    if (entries.length === 0) out.set(prefix || '(root)', v)
    for (const [k, x] of entries) leaves(x, prefix ? `${prefix}.${k}` : k, out)
  } else out.set(prefix || '(root)', v)
}

export function pathDiff(a: Any, b: Any): PathChange[] {
  const la = new Map<string, Any>()
  const lb = new Map<string, Any>()
  leaves(a, '', la)
  leaves(b, '', lb)
  const out: PathChange[] = []
  for (const [p, x] of la) {
    if (!lb.has(p)) out.push({ path: p, kind: 'removed', a: x })
    else if (JSON.stringify(x) !== JSON.stringify(lb.get(p))) out.push({ path: p, kind: 'changed', a: x, b: lb.get(p) })
  }
  for (const [p, x] of lb) if (!la.has(p)) out.push({ path: p, kind: 'added', b: x })
  return out.sort((x, y) => x.path.localeCompare(y.path, undefined, { numeric: true }))
}

export function PathChangesTable({ changes }: { changes: PathChange[] }) {
  if (!changes.length) return <div className="faint small">No differences.</div>
  return (
    <div className="table-wrap">
      <table className="table">
        <thead>
          <tr>
            <th>Path</th>
            <th>Change</th>
            <th>Before</th>
            <th>After</th>
          </tr>
        </thead>
        <tbody>
          {changes.map((c) => (
            <tr key={c.path}>
              <td className="mono small">{c.path}</td>
              <td>
                <span className={`badge ${c.kind === 'added' ? 'success' : c.kind === 'removed' ? 'error' : 'warn'}`}>{c.kind}</span>
              </td>
              <td className="mono small" style={{ color: c.kind === 'added' ? 'var(--text-3)' : 'var(--error)' }}>
                <span className="cell-preview" title={JSON.stringify(c.a)}>
                  {c.kind === 'added' ? '—' : preview(JSON.stringify(c.a), 140)}
                </span>
              </td>
              <td className="mono small" style={{ color: c.kind === 'removed' ? 'var(--text-3)' : 'var(--success)' }}>
                <span className="cell-preview" title={JSON.stringify(c.b)}>
                  {c.kind === 'removed' ? '—' : preview(JSON.stringify(c.b), 140)}
                </span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/** Stable pretty JSON with sorted keys so the line diff is meaningful. */
export function stableJson(v: Any): string {
  const sort = (x: Any): Any => {
    if (Array.isArray(x)) return x.map(sort)
    if (x && typeof x === 'object') return Object.fromEntries(Object.keys(x).sort().map((k) => [k, sort(x[k])]))
    return x
  }
  return JSON.stringify(sort(v), null, 2) ?? 'null'
}
