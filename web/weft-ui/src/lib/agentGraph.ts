// Layout for the delegation graph (DESIGN.md §7): agents as nodes sized by
// invocations, hand-offs as edges. Top-down layers from the agents nobody
// hands off to, so the graph reads like an org chart and fits narrow screens.
import type { Graph, GraphEdge, GraphNode } from '../api/v1'

export interface LaidNode extends GraphNode {
  x: number
  y: number
  r: number
  layer: number
}

export interface LaidEdge extends GraphEdge {
  path: string
  labelX: number
  labelY: number
  /** Stroke width, scaled by count. */
  width: number
}

export interface GraphLayout {
  nodes: LaidNode[]
  edges: LaidEdge[]
  width: number
  height: number
}

export const NODE_MIN_R = 20
export const NODE_MAX_R = 36
const LAYER_GAP = 170
const NODE_GAP = 220
const PAD = 70
/** Room under the last layer for the node's name and figures. */
const LABEL_ROOM = 50

/** Layers by breadth-first search over non-return edges from the root agents. */
export function layers(graph: Graph): string[][] {
  const names = graph.nodes.map((n) => n.agent)
  for (const e of graph.edges) for (const a of [e.from, e.to]) if (!names.includes(a)) names.push(a)
  const forward = graph.edges.filter((e) => e.kind !== 'return' && e.from !== e.to)
  const incoming = new Map<string, number>(names.map((n) => [n, 0]))
  for (const e of forward) incoming.set(e.to, (incoming.get(e.to) ?? 0) + 1)
  let roots = names.filter((n) => (incoming.get(n) ?? 0) === 0)
  if (!roots.length && names.length) {
    // A cycle: start from the agent that hands off the most.
    const out = new Map<string, number>()
    for (const e of forward) out.set(e.from, (out.get(e.from) ?? 0) + e.count)
    roots = [names.slice().sort((a, b) => (out.get(b) ?? 0) - (out.get(a) ?? 0))[0]]
  }
  const layerOf = new Map<string, number>()
  const queue = roots.map((r) => [r, 0] as [string, number])
  for (const [r] of queue) layerOf.set(r, 0)
  while (queue.length) {
    const [n, l] = queue.shift()!
    for (const e of forward)
      if (e.from === n && !layerOf.has(e.to)) {
        layerOf.set(e.to, l + 1)
        queue.push([e.to, l + 1])
      }
  }
  for (const n of names) if (!layerOf.has(n)) layerOf.set(n, 0)
  const max = Math.max(0, ...layerOf.values())
  const out: string[][] = Array.from({ length: max + 1 }, () => [])
  for (const n of names) out[layerOf.get(n)!].push(n)
  return out.filter((l) => l.length)
}

export function nodeRadius(calls: number, maxCalls: number): number {
  if (maxCalls <= 0) return NODE_MIN_R
  return NODE_MIN_R + (NODE_MAX_R - NODE_MIN_R) * Math.sqrt(Math.max(0, calls) / maxCalls)
}

export function layoutGraph(graph: Graph): GraphLayout {
  const ls = layers(graph)
  const byName = new Map(graph.nodes.map((n) => [n.agent, n]))
  const maxCalls = Math.max(0, ...graph.nodes.map((n) => n.calls))
  const widest = Math.max(1, ...ls.map((l) => l.length))
  const width = Math.max(320, PAD * 2 + (widest - 1) * NODE_GAP)
  const height = PAD + (ls.length - 1) * LAYER_GAP + NODE_MAX_R + LABEL_ROOM
  const nodes: LaidNode[] = []
  ls.forEach((layer, li) => {
    const span = (layer.length - 1) * NODE_GAP
    layer.forEach((name, i) => {
      const base = byName.get(name) ?? { agent: name, calls: 0, errors: 0, tokens: 0, cost_usd: null, detections: {} }
      nodes.push({ ...base, x: width / 2 - span / 2 + i * NODE_GAP, y: PAD + li * LAYER_GAP, r: nodeRadius(base.calls, maxCalls), layer: li })
    })
  })
  const pos = new Map(nodes.map((n) => [n.agent, n]))
  const maxCount = Math.max(1, ...graph.edges.map((e) => e.count))
  const edges: LaidEdge[] = graph.edges
    .filter((e) => pos.has(e.from) && pos.has(e.to) && e.from !== e.to)
    .map((e) => {
      const a = pos.get(e.from)!
      const b = pos.get(e.to)!
      // Forward and return edges between the same pair bend to opposite sides.
      const bend = e.kind === 'return' ? -1 : 1
      const dx = b.x - a.x
      const dy = b.y - a.y
      const len = Math.hypot(dx, dy) || 1
      const nx = -dy / len
      const ny = dx / len
      const curve = 28 * bend
      const mx = (a.x + b.x) / 2 + nx * curve
      const my = (a.y + b.y) / 2 + ny * curve
      // Start and end on the node rims, aimed at the control point.
      const sa = Math.atan2(my - a.y, mx - a.x)
      const ea = Math.atan2(my - b.y, mx - b.x)
      const sx = a.x + Math.cos(sa) * a.r
      const sy = a.y + Math.sin(sa) * a.r
      const ex = b.x + Math.cos(ea) * (b.r + 6)
      const ey = b.y + Math.sin(ea) * (b.r + 6)
      return {
        ...e,
        path: `M${r1(sx)},${r1(sy)} Q${r1(mx)},${r1(my)} ${r1(ex)},${r1(ey)}`,
        labelX: r1((sx + 2 * mx + ex) / 4),
        labelY: r1((sy + 2 * my + ey) / 4),
        width: e.kind === 'return' ? 1.25 : 1.5 + 3 * (e.count / maxCount),
      }
    })
  return { nodes, edges, width, height }
}

const r1 = (n: number) => Math.round(n * 10) / 10

/** Status of an edge for colouring: errors win over detections. */
export function edgeTone(e: GraphEdge): 'error' | 'ok' | 'muted' {
  if (e.kind === 'return') return 'muted'
  return e.errors > 0 ? 'error' : 'ok'
}

export function nodeTone(n: GraphNode): 'block' | 'approval' | 'error' | 'ok' {
  if ((n.detections?.block ?? 0) > 0) return 'block'
  if (n.errors > 0) return 'error'
  if ((n.detections?.require_approval ?? 0) > 0) return 'approval'
  return 'ok'
}
