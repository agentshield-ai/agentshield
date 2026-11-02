import { describe, expect, it } from 'vitest'
import { decodeLb, encodeLb, rankRuns } from './leaderboardUtils'

const values = {
  acc: { a: 0.9, b: 0.7, c: 0.8 },
  latency: { a: 3, b: 1, c: 2 },
  partial: { a: 1, b: null, c: 0 },
}

describe('inline leaderboard ranking', () => {
  it('ranks by one metric, respecting direction', () => {
    expect(rankRuns(['a', 'b', 'c'], values, [{ key: 'acc', weight: 1, lowerBetter: false }]).map((r) => r.id)).toEqual(['a', 'c', 'b'])
    expect(rankRuns(['a', 'b', 'c'], values, [{ key: 'latency', weight: 1, lowerBetter: true }]).map((r) => r.id)).toEqual(['b', 'c', 'a'])
  })
  it('combines weighted, normalised metrics', () => {
    // acc: a=1, c=.5, b=0; latency (lower better): b=1, c=.5, a=0
    const eq = rankRuns(['a', 'b', 'c'], values, [
      { key: 'acc', weight: 1, lowerBetter: false },
      { key: 'latency', weight: 1, lowerBetter: true },
    ])
    expect(eq.map((r) => r.score)).toEqual([0.5, 0.5, 0.5])
    expect(eq.every((r) => r.rank === 1)).toBe(true)
    const accHeavy = rankRuns(['a', 'b', 'c'], values, [
      { key: 'acc', weight: 3, lowerBetter: false },
      { key: 'latency', weight: 1, lowerBetter: true },
    ])
    expect(accHeavy[0]).toMatchObject({ id: 'a', rank: 1, score: 0.75 })
    expect(accHeavy.map((r) => r.rank)).toEqual([1, 2, 3])
  })
  it('treats a missing value as the worst and ignores zero weights', () => {
    const r = rankRuns(['a', 'b', 'c'], values, [
      { key: 'partial', weight: 1, lowerBetter: false },
      { key: 'acc', weight: 0, lowerBetter: false },
    ])
    expect(r.map((x) => x.id)).toEqual(['a', 'c', 'b'])
    expect(r.find((x) => x.id === 'b')!.score).toBeNull()
    expect(r.find((x) => x.id === 'b')!.missing).toEqual(['partial'])
  })
  it('gives everyone full marks when all values are equal', () => {
    const r = rankRuns(['a', 'b'], { m: { a: 2, b: 2 } }, [{ key: 'm', weight: 1, lowerBetter: true }])
    expect(r.map((x) => [x.score, x.rank])).toEqual([
      [1, 1],
      [1, 1],
    ])
  })
  it('round-trips the URL encoding', () => {
    const ms = [
      { key: 'exact.true_fraction', weight: 2, lowerBetter: false },
      { key: 'model_latency.mean', weight: 0.5, lowerBetter: true },
      { key: 'odd,key*x', weight: 1, lowerBetter: false },
    ]
    expect(decodeLb(encodeLb(ms))).toEqual(ms)
    expect(decodeLb('')).toEqual([])
    expect(decodeLb('a*nope')).toEqual([{ key: 'a', weight: 1, lowerBetter: false }])
  })
})
