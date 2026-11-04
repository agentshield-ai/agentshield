import { describe, expect, it } from 'vitest'
import type { Span } from '../../api/types'
import { buildTree } from './callUtils'
import { composeOps, flameLayout, flattenComposed } from './flameLayout'

const T0 = Date.parse('2026-01-01T00:00:00Z')
function call(id: string, parent: string | null, op: string, startMs: number, endMs: number | null, err = false): Span {
  return {
    id,
    parent_id: parent,
    trace_id: 't',
    project_id: 'e/p',
    name: `weft:///e/p/function/${op}:abc`,
    started_at: new Date(T0 + startMs).toISOString(),
    ended_at: endMs == null ? null : new Date(T0 + endMs).toISOString(),
    error: err ? 'boom' : null,
    inputs: {},
    output: null,
    attributes: {},
    summary: {},
  } as unknown as Span
}

const calls = [
  call('r', null, 'agent', 0, 1000),
  call('a', 'r', 'retrieve', 0, 100),
  call('b', 'r', 'llm', 100, 400),
  call('c', 'r', 'retrieve', 400, 500),
  call('d', 'r', 'llm', 500, 1200, true), // runs past its parent: clipped
  call('e', 'b', 'tool', 150, 250),
]

describe('flame layout', () => {
  it('positions bars by time and depth', () => {
    const l = flameLayout(buildTree(calls))
    expect(l.spanMs).toBe(1200)
    expect(l.depth).toBe(3)
    const by = Object.fromEntries(l.rects.map((r) => [r.id, r]))
    expect(by.r.x0).toBe(0)
    expect(by.b.depth).toBe(1)
    expect(by.e.depth).toBe(2)
    expect(by.e.x0).toBeCloseTo(150 / 1200)
    // Child clipped to its parent's end.
    expect(by.d.x1).toBeCloseTo(1000 / 1200)
    expect(by.d.ms).toBe(700)
    // No overlaps here: one row per depth.
    expect(l.rows).toBe(3)
    expect(by.e.row).toBe(2)
  })
  it('stacks concurrent calls into lanes', () => {
    const par = [call('r', null, 'agent', 0, 100), call('a', 'r', 'x', 0, 60), call('b', 'r', 'x', 10, 70), call('c', 'r', 'x', 60, 90), call('k', 'b', 'y', 20, 30)]
    const l = flameLayout(buildTree(par))
    const by = Object.fromEntries(l.rects.map((r) => [r.id, r.row]))
    expect(by).toEqual({ r: 0, a: 1, b: 2, c: 1, k: 3 })
    expect(l.rows).toBe(4)
  })
  it('handles empty traces', () => {
    expect(flameLayout([]).rects).toEqual([])
  })
})

describe('code composition', () => {
  it('merges repeated sibling ops and their children', () => {
    const label = (c: Span) => c.name.split('/function/')[1].split(':')[0]
    const ops = composeOps(buildTree(calls), label, T0 + 5000)
    expect(ops).toHaveLength(1)
    const kids = ops[0].children
    expect(kids.map((k) => [k.label, k.count, k.errors, k.totalMs])).toEqual([
      ['retrieve', 2, 0, 200],
      ['llm', 2, 1, 1000],
    ])
    expect(kids[1].children.map((k) => [k.key, k.count])).toEqual([['agent › llm › tool', 1]])
    expect(flattenComposed(ops).map((o) => o.label)).toEqual(['agent', 'retrieve', 'llm', 'tool'])
    expect(flattenComposed(ops, (k) => k !== 'agent').map((o) => o.label)).toEqual(['agent'])
  })
})
