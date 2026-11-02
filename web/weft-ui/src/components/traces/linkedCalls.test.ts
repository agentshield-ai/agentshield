import { describe, expect, it } from 'vitest'
import { hasLinks, linksOf } from './linkedCalls'

describe('linked calls', () => {
  it('reads EvaluationLogger links in any accepted shape', () => {
    const l = linksOf({
      weft: {
        linked_calls: [{ call_id: 'abc123', trace_id: 't1' }, 'weft:///e/p/call/def456', 'ghi789xyz'],
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
  it('reads weave.eval.* attributes flat, nested or from OTel', () => {
    expect(linksOf({ 'weave.eval.run_id': 'r1', 'weave.eval.predict_and_score_call_id': 'p1', 'weave.eval.kind': 'agent' }).evalLink).toEqual({
      runId: 'r1',
      predictAndScoreCallId: 'p1',
      extra: { kind: 'agent' },
    })
    expect(linksOf({ otel: { attributes: { 'weave.eval.run_id': 'r2' } } }).evalLink?.runId).toBe('r2')
    expect(linksOf({ weave: { eval: { predict_and_score_call_id: 'p3', trial_index: 0 } } }).evalLink).toEqual({ runId: undefined, predictAndScoreCallId: 'p3', extra: { trial_index: '0' } })
  })
  it('ignores unrelated attributes', () => {
    const l = linksOf({ weft: { internal: true, linked_calls: ['x', null, 3] }, env: 'prod' })
    expect(hasLinks(l)).toBe(false)
    expect(hasLinks(linksOf(null))).toBe(false)
  })
})
