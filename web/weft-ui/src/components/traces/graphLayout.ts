// Op-level call graph of a trace and a simple layered layout (unit tested).
// Nodes are ops (by display name); an edge A → B means a call of A made a
// call of B. Layers follow the depth at which an op is first called, laid
// out left to right; nodes inside a layer are ordered by their parents'
// positions (one barycenter sweep) to reduce crossings.

export interface GraphCall {
  id: string
  parent_id: string | null
  op: string
  error?: boolean
  latency_ms?: number | null
  started_at?: string
}

export interface OpNode {
  id: string
  label: string
  callIds: string[]
  count: number
  errors: number
  totalMs: number
  layer: number
  order: number
  x: number
  y: number
  w: number
  h: number
}

export interface OpEdge {
  from: string
  to: string
  count: number
  /** Points into the same or an earlier layer (recursion or a cycle). */
  back: boolean
  self: boolean
  path: string
  labelX: number
  labelY: number
}

export interface OpGraph {
  nodes: OpNode[]
  edges: OpEdge[]
  width: number
  height: number
}

export const NODE_H = 44
const COL_GAP = 56
const ROW_GAP = 14
const PAD = 12

export function nodeWidth(label: string): number {
  return Math.min(220, Math.max(112, Math.round(label.length * 7 + 28)))
}

/** Build op nodes and edges from a trace's calls. */
export function buildOpGraph(calls: GraphCall[]): { nodes: OpNode[]; edges: Omit<OpEdge, 'path' | 'labelX' | 'labelY' | 'back'>[] } {
  const byId = new Map(calls.map((c) => [c.id, c]))
  const sorted = [...calls].sort((a, b) => (a.started_at ?? '').localeCompare(b.started_at ?? ''))
  const depthCache = new Map<string, number>()
  const depth = (c: GraphCall): number => {
    const hit = depthCache.get(c.id)
    if (hit != null) return hit
    // Walk up iteratively; guard against parent cycles in bad data.
    let d = 0
    let cur: GraphCall | undefined = c
    const seen = new Set<string>()
    while (cur?.parent_id && byId.has(cur.parent_id) && !seen.has(cur.id)) {
      seen.add(cur.id)
      cur = byId.get(cur.parent_id)
      d++
    }
    depthCache.set(c.id, d)
    return d
  }
  const nodes = new Map<string, OpNode>()
  for (const c of sorted) {
    let n = nodes.get(c.op)
    if (!n) {
      n = { id: c.op, label: c.op, callIds: [], count: 0, errors: 0, totalMs: 0, layer: depth(c), order: nodes.size, x: 0, y: 0, w: nodeWidth(c.op), h: NODE_H }
      nodes.set(c.op, n)
    }
    n.layer = Math.min(n.layer, depth(c))
    n.callIds.push(c.id)
    n.count++
    if (c.error) n.errors++
    n.totalMs += c.latency_ms ?? 0
  }
  const edgeMap = new Map<string, { from: string; to: string; count: number; self: boolean }>()
  for (const c of sorted) {
    const p = c.parent_id ? byId.get(c.parent_id) : undefined
    if (!p) continue
    const key = `${p.op}\u0000${c.op}`
    const e = edgeMap.get(key) ?? { from: p.op, to: c.op, count: 0, self: p.op === c.op }
    e.count++
    edgeMap.set(key, e)
  }
  return { nodes: [...nodes.values()], edges: [...edgeMap.values()] }
}

/** Assign x/y to nodes and SVG paths to edges. */
export function layoutOpGraph(calls: GraphCall[]): OpGraph {
  const { nodes, edges } = buildOpGraph(calls)
  const layers: OpNode[][] = []
  for (const n of nodes) (layers[n.layer] ??= []).push(n)
  const compact = layers.filter(Boolean)
  compact.forEach((l, i) => l.forEach((n) => (n.layer = i)))
  const byId = new Map(nodes.map((n) => [n.id, n]))
  // Barycenter ordering: sort each layer by the mean order of its parents.
  for (let li = 1; li < compact.length; li++) {
    const bary = new Map<string, number>()
    for (const n of compact[li]) {
      const ps = edges.filter((e) => e.to === n.id && !e.self).map((e) => byId.get(e.from)!).filter((p) => p.layer < li)
      bary.set(n.id, ps.length ? ps.reduce((s, p) => s + p.order, 0) / ps.length : n.order)
    }
    compact[li].sort((a, b) => bary.get(a.id)! - bary.get(b.id)! || a.order - b.order)
    compact[li].forEach((n, i) => (n.order = i))
  }
  compact[0]?.forEach((n, i) => (n.order = i))
  // Positions: each layer is a column; columns are as wide as their widest node.
  let x = PAD
  let height = 0
  for (const layer of compact) {
    const colW = Math.max(...layer.map((n) => n.w))
    layer.forEach((n, i) => {
      n.x = x
      n.y = PAD + i * (NODE_H + ROW_GAP)
      n.w = colW
    })
    height = Math.max(height, PAD + layer.length * (NODE_H + ROW_GAP) - ROW_GAP + PAD)
    x += colW + COL_GAP
  }
  const width = Math.max(x - COL_GAP + PAD, 2 * PAD)
  const out: OpEdge[] = edges.map((e) => {
    const a = byId.get(e.from)!
    const b = byId.get(e.to)!
    if (e.self) {
      // Small loop above the node's right end.
      const sx = a.x + a.w - 26
      const sy = a.y
      return { ...e, back: true, path: `M ${sx} ${sy} C ${sx - 6} ${sy - 22}, ${sx + 26} ${sy - 22}, ${sx + 18} ${sy}`, labelX: sx + 8, labelY: sy - 14 }
    }
    if (b.layer > a.layer) {
      const x1 = a.x + a.w
      const y1 = a.y + a.h / 2
      const x2 = b.x
      const y2 = b.y + b.h / 2
      const mx = (x1 + x2) / 2
      return { ...e, back: false, path: `M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`, labelX: mx, labelY: (y1 + y2) / 2 - 4 }
    }
    // Back edge (to the same or an earlier layer): arc below both nodes.
    const x1 = a.x + a.w / 2
    const y1 = a.y + a.h
    const x2 = b.x + b.w / 2
    const y2 = b.y + b.h
    const dip = Math.max(y1, y2) + 24
    return { ...e, back: true, path: `M ${x1} ${y1} C ${x1} ${dip}, ${x2} ${dip}, ${x2} ${y2}`, labelX: (x1 + x2) / 2, labelY: dip - 4 }
  })
  const maxDip = out.some((e) => e.back && !e.self) ? 30 : 0
  return { nodes, edges: out, width, height: height + maxDip }
}

/** Ops on the path from the trace root to `callId`, in order. */
export function opPath(calls: GraphCall[], callId: string): string[] {
  const byId = new Map(calls.map((c) => [c.id, c]))
  const out: string[] = []
  const seen = new Set<string>()
  let cur = byId.get(callId)
  while (cur && !seen.has(cur.id)) {
    seen.add(cur.id)
    out.unshift(cur.op)
    cur = cur.parent_id ? byId.get(cur.parent_id) : undefined
  }
  return out
}

/** Which call to select when an op node is clicked: cycle through its calls. */
export function nextCallFor(node: Pick<OpNode, 'callIds'>, selectedId: string): string {
  const i = node.callIds.indexOf(selectedId)
  return node.callIds[(i + 1) % node.callIds.length]
}
