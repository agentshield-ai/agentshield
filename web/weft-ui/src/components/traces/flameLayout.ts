// Flame graph and code-composition layouts for a trace (unit tested in flameLayout.test.ts).
import type { Call } from '../../api/types'
import type { TreeNode } from './callUtils'

export interface FlameRect {
  id: string
  call: Call
  depth: number
  /** Display row: concurrent calls at one depth get their own lanes. */
  row: number
  /** Start and end as fractions (0..1) of the trace span. */
  x0: number
  x1: number
  ms: number
}

export interface FlameLayout {
  rects: FlameRect[]
  depth: number
  rows: number
  t0: number
  spanMs: number
}

const endMs = (c: Call, now: number) => (c.ended_at ? new Date(c.ended_at).getTime() : now)

/**
 * Icicle layout: x is time relative to the earliest start, y is tree depth.
 * A child that runs past its parent is clipped to the parent so bars nest.
 */
export function flameLayout(roots: TreeNode[], now: number = Date.now()): FlameLayout {
  let min = Infinity
  let max = -Infinity
  const walkBounds = (ns: TreeNode[]) => {
    for (const n of ns) {
      min = Math.min(min, new Date(n.call.started_at).getTime())
      max = Math.max(max, endMs(n.call, now))
      walkBounds(n.children)
    }
  }
  walkBounds(roots)
  if (!Number.isFinite(min)) return { rects: [], depth: 0, rows: 0, t0: 0, spanMs: 0 }
  const span = Math.max(1, max - min)
  const rects: FlameRect[] = []
  let depth = 0
  const walk = (ns: TreeNode[], d: number, lo: number, hi: number) => {
    for (const n of ns) {
      const s = new Date(n.call.started_at).getTime()
      const e = endMs(n.call, now)
      const x0 = Math.min(Math.max((s - min) / span, lo), hi)
      const x1 = Math.max(Math.min((e - min) / span, hi), x0)
      rects.push({ id: n.call.id, call: n.call, depth: d, row: 0, x0, x1, ms: Math.max(0, e - s) })
      depth = Math.max(depth, d + 1)
      walk(n.children, d + 1, x0, x1)
    }
  }
  walk(roots, 0, 0, 1)
  // Lanes: calls that overlap in time at the same depth (parallel work)
  // stack into extra rows instead of drawing over each other.
  const lanesAt: number[] = []
  const laneOf = new Map<FlameRect, number>()
  for (let d = 0; d < depth; d++) {
    const ends: number[] = []
    for (const r of rects.filter((x) => x.depth === d).sort((a, b) => a.x0 - b.x0)) {
      let lane = ends.findIndex((end) => end <= r.x0 + 1e-9)
      if (lane < 0) lane = ends.push(0) - 1
      ends[lane] = r.x1
      laneOf.set(r, lane)
    }
    lanesAt[d] = Math.max(1, ends.length)
  }
  const base: number[] = []
  let rows = 0
  for (let d = 0; d < depth; d++) {
    base[d] = rows
    rows += lanesAt[d]
  }
  for (const r of rects) r.row = base[r.depth] + (laneOf.get(r) ?? 0)
  return { rects, depth, rows, t0: min, spanMs: span }
}

export interface ComposedOp {
  /** Path of op labels from the root, joined by " › ". */
  key: string
  label: string
  depth: number
  callIds: string[]
  count: number
  errors: number
  totalMs: number
  children: ComposedOp[]
}

/**
 * Code composition: the trace folded by op. Sibling calls of the same op
 * merge into one node (with a call count and total time), and the children
 * of every merged call are merged again one level down, so a loop that
 * calls `retrieve` then `llm` five times reads as two lines with ×5.
 */
export function composeOps(roots: TreeNode[], labelOf: (c: Call) => string, now: number = Date.now()): ComposedOp[] {
  const build = (nodes: TreeNode[], depth: number, prefix: string): ComposedOp[] => {
    const groups = new Map<string, TreeNode[]>()
    for (const n of nodes) {
      const l = labelOf(n.call)
      groups.set(l, [...(groups.get(l) ?? []), n])
    }
    const out: ComposedOp[] = []
    for (const [label, ns] of groups) {
      const key = prefix ? `${prefix} › ${label}` : label
      out.push({
        key,
        label,
        depth,
        callIds: ns.map((n) => n.call.id),
        count: ns.length,
        errors: ns.filter((n) => !!n.call.exception).length,
        totalMs: ns.reduce((s, n) => s + Math.max(0, endMs(n.call, now) - new Date(n.call.started_at).getTime()), 0),
        children: build(
          ns.flatMap((n) => n.children),
          depth + 1,
          key,
        ),
      })
    }
    return out
  }
  return build(roots, 0, '')
}

export function flattenComposed(ops: ComposedOp[], isOpen: (key: string) => boolean = () => true): ComposedOp[] {
  const out: ComposedOp[] = []
  const walk = (xs: ComposedOp[]) => {
    for (const o of xs) {
      out.push(o)
      if (isOpen(o.key)) walk(o.children)
    }
  }
  walk(ops)
  return out
}
