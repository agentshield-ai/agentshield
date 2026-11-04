import { describe, expect, it } from 'vitest'
import { hasLinks, linksOf } from './linkedCalls'

describe('linked calls', () => {
  it('reads EvaluationLogger links in any accepted shape', () => {
    const l = linksOf({
      weft: {
        linked_spans: [{ span_id: 'abc123', trace_id: 't1' }, 'weft:///e/p/span/def456', 'ghi789xyz'],
        genai_span_ref: [{ trace_id: 't1', span_id: 'abc123' }, { span_id: 'zzz999' }],
        linked_conversation_ids: ['conv-1'],
      },
    })
    expect(l.calls).toEqual([
      { callId: 'abc123', traceId: 't1' },
      { callId: 'def456' },
      { callId: 'ghi789xyz' },
      { callId: 'zzz999', traceId: undefined },
    ])
    expect(l.conversations).toEqual(['conv-1'])
    expect(l.evalLink).toBeNull()
    expect(hasLinks(l)).toBe(true)
  })
  it('reads the deprecated keys of earlier SDK versions', () => {
    const l = linksOf({ weft: { linked_calls: [{ call_id: 'old123', trace_id: 't9' }, 'weft:///e/p/call/old456'] } })
    expect(l.calls).toEqual([{ callId: 'old123', traceId: 't9' }, { callId: 'old456' }])
    expect(linksOf({ 'weft.eval.run_id': 'r1', 'weft.eval.predict_and_score_call_id': 'p1' }).evalLink).toEqual({ runId: 'r1', predictionSpanId: 'p1', extra: {} })
    expect(linksOf({ weft: { eval: { predict_and_score_call_id: 'p3', trial_index: 0 } } }).evalLink).toEqual({ runId: undefined, predictionSpanId: 'p3', extra: { trial_index: '0' } })
  })
  it('reads weft.eval.* attributes flat, nested or from OTel', () => {
    expect(linksOf({ 'weft.eval.run_id': 'r1', 'weft.eval.prediction_span_id': 'p1', 'weft.eval.kind': 'agent' }).evalLink).toEqual({
      runId: 'r1',
      predictionSpanId: 'p1',
      extra: { kind: 'agent' },
    })
    expect(linksOf({ otel: { attributes: { 'weft.eval.run_id': 'r2' } } }).evalLink?.runId).toBe('r2')
    expect(linksOf({ weft: { eval: { prediction_span_id: 'p3', trial_index: 0 } } }).evalLink).toEqual({ runId: undefined, predictionSpanId: 'p3', extra: { trial_index: '0' } })
  })
  it('ignores unrelated attributes', () => {
    const l = linksOf({ weft: { internal: true, linked_spans: ['x', null, 3] }, env: 'prod' })
    expect(hasLinks(l)).toBe(false)
    expect(hasLinks(linksOf(null))).toBe(false)
  })
})
