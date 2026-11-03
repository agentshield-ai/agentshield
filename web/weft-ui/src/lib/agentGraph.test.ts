import { describe, expect, it } from 'vitest'
import type { Graph } from '../api/v1'
import { agentColor, agentInitials, agentSlot } from './agentColor'
import { edgeTone, layers, layoutGraph, NODE_MAX_R, NODE_MIN_R, nodeRadius, nodeTone } from './agentGraph'

const node = (agent: string, calls: number, extra = {}) => ({ agent, calls, errors: 0, tokens: 0, cost_usd: null, detections: {}, ...extra })
const edge = (from: string, to: string, kind: 'delegate' | 'handoff' | 'return', count = 1, errors = 0) => ({
  from,
  to,
  kind,
  count,
  errors,
  error_rate: count ? errors / count : 0,
  p50_ms: kind === 'return' ? null : 1300,
  p95_ms: null,
  mean_score: null,
})

const G: Graph = {
  nodes: [node('orchestrator', 8), node('researcher', 6, { errors: 3 }), node('writer', 2), node('critic', 1, { detections: { block: 1 } })],
  edges: [
    edge('orchestrator', 'researcher', 'delegate', 6, 3),
    edge('researcher', 'orchestrator', 'return', 6),
    edge('orchestrator', 'writer', 'delegate', 2),
    edge('writer', 'critic', 'handoff', 1),
  ],
}

describe('layers', () => {
  it('starts from agents nobody hands off to and ignores returns', () => {
    expect(layers(G)).toEqual([['orchestrator'], ['researcher', 'writer'], ['critic']])
  })

  it('breaks a cycle at the agent that hands off most', () => {
    const g: Graph = { nodes: [node('a', 1), node('b', 1)], edges: [edge('a', 'b', 'handoff', 3), edge('b', 'a', 'handoff', 1)] }
    expect(layers(g)).toEqual([['a'], ['b']])
  })

  it('places agents seen only on edges, and isolated agents', () => {
    const g: Graph = { nodes: [node('solo', 1)], edges: [edge('x', 'y', 'delegate')] }
    expect(layers(g).flat().sort()).toEqual(['solo', 'x', 'y'])
  })
})

describe('layoutGraph', () => {
  const l = layoutGraph(G)
  it('sizes nodes by runs within bounds', () => {
    expect(nodeRadius(0, 8)).toBe(NODE_MIN_R)
    expect(nodeRadius(8, 8)).toBe(NODE_MAX_R)
    const r = Object.fromEntries(l.nodes.map((n) => [n.agent, n.r]))
    expect(r.orchestrator).toBeGreaterThan(r.writer)
  })

  it('keeps every node inside the view box with room for its label', () => {
    for (const n of l.nodes) {
      expect(n.x - n.r).toBeGreaterThanOrEqual(0)
      expect(n.x + n.r).toBeLessThanOrEqual(l.width)
      expect(n.y + n.r + 30).toBeLessThanOrEqual(l.height)
    }
    const ys = Object.fromEntries(l.nodes.map((n) => [n.agent, n.y]))
    expect(ys.orchestrator).toBeLessThan(ys.researcher)
    expect(ys.researcher).toBe(ys.writer)
    expect(ys.writer).toBeLessThan(ys.critic)
  })

  it('draws one path per edge, thicker for busier hand-offs, and bends returns the other way', () => {
    expect(l.edges).toHaveLength(4)
    const byKey = Object.fromEntries(l.edges.map((e) => [`${e.from}>${e.to}`, e]))
    expect(byKey['orchestrator>researcher'].width).toBeGreaterThan(byKey['orchestrator>writer'].width)
    expect(byKey['orchestrator>researcher'].path).toMatch(/^M[\d.-]+,[\d.-]+ Q[\d.-]+,[\d.-]+ [\d.-]+,[\d.-]+$/)
    expect(byKey['orchestrator>researcher'].path).not.toBe(byKey['researcher>orchestrator'].path)
  })

  it('colours by errors and detections', () => {
    expect(edgeTone(G.edges[0])).toBe('error')
    expect(edgeTone(G.edges[1])).toBe('muted')
    expect(edgeTone(G.edges[2])).toBe('ok')
    expect(nodeTone(G.nodes[1])).toBe('error')
    expect(nodeTone(G.nodes[3])).toBe('block')
    expect(nodeTone(G.nodes[0])).toBe('ok')
  })
})

describe('agent colours', () => {
  it('is stable per name and uses the palette tokens', () => {
    expect(agentColor('researcher')).toBe(agentColor('researcher'))
    expect(agentColor('researcher')).toMatch(/^var\(--agent-\d\)$/)
    expect(agentSlot(null)).toBe(-1)
    const slots = new Set(['orchestrator', 'researcher', 'writer', 'coder', 'reviewer', 'lead', 'critic', 'planner'].map(agentSlot))
    expect(slots.size).toBeGreaterThanOrEqual(5)
  })
  it('makes initials', () => {
    expect(agentInitials('researcher')).toBe('RE')
    expect(agentInitials('code-reviewer')).toBe('CR')
  })
})
