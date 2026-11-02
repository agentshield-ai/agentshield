import { describe, expect, it } from 'vitest'
import {
  decodeRatings,
  encodeRatings,
  parseSignalsVal,
  presetConfig,
  readSpanFilters,
  scoreTone,
  signalId,
  spanQueryParts,
  toSpec,
  trendBuckets,
  turnSignals,
  validateSignal,
  writeSpanFilters,
  EMPTY_SPAN_FILTERS,
} from './signalUtils'

describe('signal config', () => {
  it('parses stored configs leniently', () => {
    const v = parseSignalsVal({ model: 'gpt-4o-mini', signals: [{ id: 'nsfw', kind: 'tag', name: 'NSFW', description: 'x', preset: 'nsfw' }, { id: 'bad' }, null, { id: 'q', kind: 'rating', enabled: false }] })
    expect(v.model).toBe('gpt-4o-mini')
    expect(v.signals.map((s) => [s.id, s.enabled])).toEqual([
      ['nsfw', true],
      ['q', false],
    ])
    expect(parseSignalsVal(null)).toEqual({ signals: [], model: undefined, auto: true })
    const v2 = parseSignalsVal({ auto: false, signals: [{ id: 'a', kind: 'tag', sample_rate: 0.25 }, { id: 'b', kind: 'tag', sample_rate: 7 }] })
    expect(v2.auto).toBe(false)
    expect(v2.signals.map((x) => x.sample_rate)).toEqual([0.25, undefined])
  })
  it('builds run specs: presets by id, custom signals in full', () => {
    const p = presetConfig({ id: 'task_success', name: 'Task Success', kind: 'rating', description: 'd' })
    expect(toSpec(p)).toEqual({ preset: 'task_success', name: 'Task Success', description: 'd' })
    expect(toSpec({ id: 'refund', name: 'Refund', kind: 'tag', description: 'asks for a refund', enabled: true })).toEqual({ id: 'refund', name: 'Refund', kind: 'tag', description: 'asks for a refund' })
  })
  it('derives and validates ids', () => {
    expect(signalId('Asks for a Refund!')).toBe('asks_for_a_refund')
    expect(signalId('x'.repeat(50))).toHaveLength(36)
    expect(validateSignal({ id: 'refund', name: 'Refund', kind: 'tag', description: 'd' })).toBeNull()
    expect(validateSignal({ id: 'refund', name: 'Refund', kind: 'tag', description: 'd' }, ['refund'])).toMatch(/already exists/)
    expect(validateSignal({ id: '', name: '!!', kind: 'tag', description: 'd' })).toMatch(/id/)
    expect(validateSignal({ id: 'a', name: 'A', kind: 'rating', description: ' ' })).toMatch(/Describe/)
  })
})

describe('signal display', () => {
  it('tones scores', () => {
    expect(scoreTone(0.9)).toBe('good')
    expect(scoreTone(0.5)).toBe('mid')
    expect(scoreTone(0.1)).toBe('bad')
    expect(scoreTone(null)).toBe('none')
  })
  it('buckets ratings by mean and tags by matches', () => {
    const start = new Date('2026-01-01T00:00:00Z')
    const end = new Date('2026-01-01T04:00:00Z')
    const r = (h: number, score: number) => ({ kind: 'rating' as const, score, tags: [], turn_started_at: `2026-01-01T0${h}:30:00Z`, created_at: '' })
    const ratings = trendBuckets([r(0, 0.2), r(0, 0.6), r(3, 1)], start, end, 4)
    expect(ratings.map((p) => p.value)).toEqual([0.4, null, null, 1])
    expect(ratings.map((p) => p.count)).toEqual([2, 0, 0, 1])
    const t = (h: number, tags: string[]) => ({ kind: 'tag' as const, score: null, tags, turn_started_at: `2026-01-01T0${h}:10:00Z`, created_at: '' })
    expect(trendBuckets([t(1, ['nsfw']), t(1, []), t(9, ['x'])], start, end, 4).map((p) => p.value)).toEqual([0, 1, 0, 0])
  })
  it('collects ratings and de-duplicated tags with reasons from turn feedback', () => {
    const s = turnSignals([
      { scorer_ratings: { task_success: 1 }, scorer_tags: ['nsfw'], scorer_rating_reasons: { task_success: 'done' }, scorer_tag_confidences: { nsfw: 0.8 } },
      { scorer_ratings: {}, scorer_tags: ['nsfw', 'user_frustration'] },
    ])
    expect(s.ratings).toEqual([{ name: 'task_success', value: 1, confidence: undefined, reason: 'done' }])
    expect(s.tags.map((t) => [t.name, t.confidence])).toEqual([
      ['nsfw', 0.8],
      ['user_frustration', undefined],
    ])
  })
})

describe('spans table state', () => {
  it('round-trips rating filters through the URL', () => {
    const rs = [{ scorer_key: 'response.quality', op: 'gte' as const, value: 0.5 }]
    expect(decodeRatings(encodeRatings(rs))).toEqual(rs)
    expect(decodeRatings('a:ne:1,b:lt:x,c:lt:')).toEqual([])
  })
  it('reads and writes filter params', () => {
    const p = new URLSearchParams('op=chat&status=error&tags=nsfw,user_frustration&sentiment=frustrated&ratings=q:lt:0.4')
    const f = readSpanFilters(p)
    expect(f).toMatchObject({ op: 'chat', status: 'error', tags: ['nsfw', 'user_frustration'], sentiment: ['frustrated'] })
    expect(writeSpanFilters(f)).toMatchObject({ op: 'chat', status: 'error', tags: 'nsfw,user_frustration', ratings: 'q:lt:0.4', agent: null })
  })
  it('builds the spans query with signal and insight filters', () => {
    expect(spanQueryParts(EMPTY_SPAN_FILTERS)).toEqual({})
    const q = spanQueryParts({ ...EMPTY_SPAN_FILTERS, op: 'chat', status: 'error', tags: ['nsfw'], sentiment: ['frustrated'] }, 'travel')
    expect(q.query).toEqual({
      $expr: {
        $and: [
          { $eq: [{ $getField: 'agent_name' }, { $literal: 'travel' }] },
          { $eq: [{ $getField: 'operation_name' }, { $literal: 'chat' }] },
          { $eq: [{ $getField: 'is_error' }, { $literal: true }] },
        ],
      },
    })
    expect(q.signal_filters).toEqual({ tags: ['nsfw'] })
    expect(q.insight_filters).toEqual([{ field: 'intent_sentiment', values: ['frustrated'] }])
  })
})
