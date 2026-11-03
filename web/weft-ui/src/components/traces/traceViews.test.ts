import { describe, expect, it } from 'vitest'
import { diffStrings, fmtDelta, leafDiffs, numericDelta, similarity, tokenize } from './diffUtils'
import { buildOpGraph, layoutOpGraph, NODE_H, nextCallFor, opPath, type GraphCall } from './graphLayout'

describe('numeric diff', () => {
  it('computes delta and percentage against the baseline', () => {
    expect(numericDelta(100, 150)).toEqual({ delta: 50, pct: 50 })
    expect(numericDelta(200, 50)).toEqual({ delta: -150, pct: -75 })
    expect(numericDelta(-4, -2)).toEqual({ delta: 2, pct: 50 })
    expect(numericDelta(0, 3)).toEqual({ delta: 3, pct: null })
    expect(numericDelta(0, 0)).toEqual({ delta: 0, pct: 0 })
    expect(numericDelta('1', 2)).toBeNull()
    expect(numericDelta(true, false)).toBeNull()
  })
  it('formats deltas with sign and percent', () => {
    expect(fmtDelta({ delta: 50, pct: 50 })).toBe('+50 (+50.0%)')
    expect(fmtDelta({ delta: -1.5, pct: -300 })).toBe('−1.5 (−300%)')
    expect(fmtDelta({ delta: 3, pct: null })).toBe('+3')
    expect(fmtDelta({ delta: 0, pct: 0 })).toBe('±0')
    expect(fmtDelta({ delta: 1200, pct: 20 }, (n) => `${n / 1000}s`)).toBe('+1.2s (+20.0%)')
  })
})

describe('string diff', () => {
  it('tokenizes losslessly', () => {
    const s = 'Hello, wörld!  42 times\nok'
    expect(tokenize(s).join('')).toBe(s)
  })
  it('produces word-level insertions and deletions', () => {
    const ops = diffStrings('the quick brown fox', 'the slow brown fox jumps')
    expect(ops).toEqual([
      { op: 'same', text: 'the ' },
      { op: 'del', text: 'quick' },
      { op: 'add', text: 'slow' },
      { op: 'same', text: ' brown fox' },
      { op: 'add', text: ' jumps' },
    ])
    // Applying the ops reproduces both sides.
    expect(ops.filter((o) => o.op !== 'add').map((o) => o.text).join('')).toBe('the quick brown fox')
    expect(ops.filter((o) => o.op !== 'del').map((o) => o.text).join('')).toBe('the slow brown fox jumps')
  })
  it('handles identical, empty and very different strings', () => {
    expect(diffStrings('same', 'same')).toEqual([{ op: 'same', text: 'same' }])
    expect(diffStrings('', 'new')).toEqual([{ op: 'add', text: 'new' }])
    expect(similarity(diffStrings('abc def', 'abc def'))).toBe(1)
    expect(similarity(diffStrings('aaa', 'bbb'))).toBe(0)
    const big = Array.from({ length: 5000 }, (_, i) => `w${i}`).join(' ')
    const ops = diffStrings(big, big.replace('w2500', 'X'))
    expect(ops.find((o) => o.op === 'del')?.text).toBe('w2500')
  })
})

describe('leaf diff', () => {
  it('reports changed, added and removed leaves with numeric deltas', () => {
    const d = leafDiffs({ a: 1, b: { c: 'x', d: [1, 2] }, gone: true }, { a: 3, b: { c: 'x', d: [1, 5] }, extra: null })
    const by = Object.fromEntries(d.map((l) => [l.path, l]))
    expect(by['a']).toMatchObject({ status: 'changed', numeric: { delta: 2, pct: 200 } })
    expect(by['b.c'].status).toBe('same')
    expect(by['b.d.1']).toMatchObject({ status: 'changed', base: 2, value: 5 })
    expect(by['gone'].status).toBe('removed')
    expect(by['extra'].status).toBe('added')
  })
})

// root(agent) → plan(llm), search(tool) ×2 → llm, llm → (none); agent → agent (recursion)
const trace: GraphCall[] = [
  { id: 'r', parent_id: null, op: 'agent', started_at: '1', latency_ms: 100 },
  { id: 'a', parent_id: 'r', op: 'llm', started_at: '2', latency_ms: 10 },
  { id: 'b', parent_id: 'r', op: 'search', started_at: '3', latency_ms: 20, error: true },
  { id: 'c', parent_id: 'r', op: 'search', started_at: '4', latency_ms: 30 },
  { id: 'd', parent_id: 'c', op: 'llm', started_at: '5', latency_ms: 5 },
  { id: 'e', parent_id: 'r', op: 'agent', started_at: '6', latency_ms: 40 },
]

describe('trace graph', () => {
  it('builds op nodes and counted edges', () => {
    const { nodes, edges } = buildOpGraph(trace)
    const n = Object.fromEntries(nodes.map((x) => [x.id, x]))
    expect(Object.keys(n).sort()).toEqual(['agent', 'llm', 'search'])
    expect(n.search).toMatchObject({ count: 2, errors: 1, totalMs: 50, layer: 1, callIds: ['b', 'c'] })
    expect(n.llm.layer).toBe(1) // first seen at depth 1 even though also called at depth 2
    const e = Object.fromEntries(edges.map((x) => [`${x.from}>${x.to}`, x]))
    expect(e['agent>search'].count).toBe(2)
    expect(e['search>llm'].count).toBe(1)
    expect(e['agent>agent'].self).toBe(true)
  })
  it('lays out layers left to right without overlaps', () => {
    const g = layoutOpGraph(trace)
    const n = Object.fromEntries(g.nodes.map((x) => [x.id, x]))
    expect(n.agent.x).toBeLessThan(n.llm.x)
    expect(n.llm.x).toBe(n.search.x)
    expect(Math.abs(n.llm.y - n.search.y)).toBeGreaterThanOrEqual(NODE_H)
    for (const node of g.nodes) {
      expect(node.x + node.w).toBeLessThanOrEqual(g.width)
      expect(node.y + node.h).toBeLessThanOrEqual(g.height)
    }
    const back = g.edges.find((e) => e.from === 'search' && e.to === 'llm')!
    expect(back.back).toBe(true) // same layer → drawn as an arc
    expect(g.edges.find((e) => e.from === 'agent' && e.to === 'llm')!.path).toMatch(/^M [\d.]+ [\d.]+ C /)
  })
  it('orders a layer by its parents (barycenter)', () => {
    const calls: GraphCall[] = [
      { id: 'r', parent_id: null, op: 'root', started_at: '0' },
      { id: 'x', parent_id: 'r', op: 'X', started_at: '1' },
      { id: 'y', parent_id: 'r', op: 'Y', started_at: '2' },
      { id: 'yc', parent_id: 'y', op: 'fromY', started_at: '3' },
      { id: 'xc', parent_id: 'x', op: 'fromX', started_at: '4' },
    ]
    const g = layoutOpGraph(calls)
    const n = Object.fromEntries(g.nodes.map((x) => [x.id, x]))
    expect(n.fromX.y).toBeLessThan(n.fromY.y)
  })
  it('finds the op path and cycles through calls of a node', () => {
    expect(opPath(trace, 'd')).toEqual(['agent', 'search', 'llm'])
    expect(nextCallFor({ callIds: ['b', 'c'] }, 'x')).toBe('b')
    expect(nextCallFor({ callIds: ['b', 'c'] }, 'b')).toBe('c')
    expect(nextCallFor({ callIds: ['b', 'c'] }, 'c')).toBe('b')
  })
  it('survives parent cycles in bad data', () => {
    const g = layoutOpGraph([
      { id: 'a', parent_id: 'b', op: 'A' },
      { id: 'b', parent_id: 'a', op: 'B' },
    ])
    expect(g.nodes).toHaveLength(2)
  })
})

describe('op kind and colour', () => {
  it('keeps the first kind/colour per op node', async () => {
    const { nodes } = buildOpGraph([
      { id: 'r', parent_id: null, op: 'agent', kind: 'agent', color: '#e8590c' },
      { id: 't', parent_id: 'r', op: 'search', kind: 'tool' },
    ])
    expect(nodes.find((n) => n.id === 'agent')).toMatchObject({ kind: 'agent', color: '#e8590c' })
    const { opStyle, safeColor } = await import('./opStyle')
    expect(opStyle({ attributes: { weft: { span_kind: 'llm', color: 'blue' } } })).toEqual({ kind: 'llm', color: 'var(--c6)' })
    expect(safeColor('red; background:url(x)')).toBeUndefined()
    expect(safeColor('#ABC')).toBe('#abc')
  })
})
