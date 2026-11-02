import { describe, expect, it } from 'vitest'
import { decodeIds, encodeIds, layoutColumns, moveBefore, moveStep, pinnedOffsets, togglePin } from './columnLayout'

describe('column layout', () => {
  it('orders pinned columns first, then the user order, then the rest', () => {
    const vis = ['status', 'name', 'latency', 'inputs.q']
    expect(layoutColumns(vis, [], []).map((c) => c.id)).toEqual(vis)
    expect(layoutColumns(vis, ['latency', 'name'], []).map((c) => c.id)).toEqual(['latency', 'name', 'status', 'inputs.q'])
    const pinned = layoutColumns(vis, ['latency', 'name'], ['inputs.q', 'name'])
    expect(pinned.map((c) => [c.id, c.pinned])).toEqual([
      ['name', true],
      ['inputs.q', true],
      ['latency', false],
      ['status', false],
    ])
    // Hidden columns in the saved order are skipped.
    expect(layoutColumns(['name'], ['latency', 'name'], ['latency']).map((c) => c.id)).toEqual(['name'])
  })
  it('moves columns', () => {
    expect(moveBefore(['a', 'b', 'c'], 'c', 'a')).toEqual(['c', 'a', 'b'])
    expect(moveBefore(['a', 'b', 'c'], 'a', null)).toEqual(['b', 'c', 'a'])
    expect(moveBefore(['a', 'b'], 'a', 'a')).toEqual(['a', 'b'])
    expect(moveStep(['a', 'b', 'c'], 'b', -1)).toEqual(['b', 'a', 'c'])
    expect(moveStep(['a', 'b', 'c'], 'c', 1)).toEqual(['a', 'b', 'c'])
    expect(togglePin(['a'], 'a')).toEqual([])
    expect(togglePin(['a'], 'b')).toEqual(['a', 'b'])
  })
  it('encodes ids with commas and computes sticky offsets', () => {
    const ids = ['inputs.a,b', 'name']
    expect(decodeIds(encodeIds(ids))).toEqual(ids)
    expect(decodeIds('name,name,,x')).toEqual(['name', 'x'])
    const slots = layoutColumns(['a', 'b', 'c'], [], ['a', 'b'])
    expect(pinnedOffsets(slots, { a: 100, b: 50, c: 10 }, 28)).toEqual({ a: 28, b: 128 })
  })
})
