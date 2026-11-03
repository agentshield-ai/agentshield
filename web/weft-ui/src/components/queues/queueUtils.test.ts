import { describe, expect, it } from 'vitest'
import type { Call, Feedback } from '../../api/types'
import { cleanAutomation, describeTrigger, emptyAutomation, validateAutomation } from '../automations/automationUtils'
import { createSignal, planSignal, SIGNALS, uniqueId } from '../monitors/signals'
import { buildExportRows, displayFieldValues, nextPendingIndex, progressCounts } from './queueUtils'

const call = (id: string, inputs: Record<string, unknown>, output: unknown) => ({ id, inputs, output, op_name: 'answer' }) as unknown as Call
const fb = (callId: string, type: string, value: unknown, at: string, user = 'alice'): Feedback => ({
  id: `${callId}-${type}-${at}`,
  project_id: 'e/p',
  ref: `weft:///e/p/call/${callId}`,
  feedback_type: type,
  payload: { value },
  creator: null,
  created_at: at,
  user_id: user,
})

describe('queue navigation', () => {
  const items = [{ annotation_state: 'completed' }, { annotation_state: 'unstarted' }, { annotation_state: 'skipped' }, { annotation_state: 'in_progress' }] as const
  it('finds the next pending item, wrapping', () => {
    expect(nextPendingIndex([...items], 0)).toBe(1)
    expect(nextPendingIndex([...items], 1)).toBe(3)
    expect(nextPendingIndex([...items], 3)).toBe(1)
    expect(nextPendingIndex([{ annotation_state: 'completed' }], 0)).toBe(-1)
    expect(nextPendingIndex([], 0)).toBe(-1)
  })
  it('counts progress', () => {
    expect(progressCounts([...items])).toEqual({ total: 4, completed: 1, skipped: 1, remaining: 2, done: 0.5 })
    expect(progressCounts([]).done).toBe(0)
  })
  it('reads display fields', () => {
    expect(displayFieldValues(call('c', { q: 'hi' }, 'out'), ['inputs.q', 'output', 'missing'])).toEqual([
      { path: 'inputs.q', value: 'hi' },
      { path: 'output', value: 'out' },
      { path: 'missing', value: undefined },
    ])
  })
})

describe('queue export', () => {
  const calls = new Map([
    ['c1', call('c1', { question: 'a?', self: 'ref' }, 'A')],
    ['c2', call('c2', { question: 'b?' }, 'B')],
  ])
  const feedback = [
    fb('c1', 'weft.annotation.quality', 2, '2025-01-01T00:00:00Z'),
    fb('c1', 'weft.annotation.quality', 5, '2025-01-02T00:00:00Z', 'bob'),
    fb('c1', 'weft.annotation.correct', true, '2025-01-01T00:00:00Z'),
    fb('c1', 'weft.reaction', null, '2025-01-01T00:00:00Z', 'carol'),
  ]
  it('keeps annotated calls with the latest value per spec', () => {
    const rows = buildExportRows([{ call_id: 'c1' }, { call_id: 'c2' }, { call_id: 'gone' }], calls, feedback, ['quality', 'correct', 'tone'])
    expect(rows).toEqual([{ question: 'a?', output: 'A', quality: 5, correct: true, tone: null, annotators: ['alice', 'bob'], call_id: 'c1' }])
  })
  it('can include unannotated calls', () => {
    const rows = buildExportRows([{ call_id: 'c1' }, { call_id: 'c2' }], calls, feedback, ['quality'], true)
    expect(rows.map((r) => r.call_id)).toEqual(['c1', 'c2'])
    expect(rows[1].quality).toBeNull()
  })
})

describe('automations', () => {
  it('validates required fields', () => {
    const a = emptyAutomation('alert')
    expect(validateAutomation(a)).toBe('Choose the scorer to watch')
    a.trigger.scorer = 'judge'
    expect(validateAutomation(a)).toMatch(/webhook URL/)
    a.action.url = 'ftp://x'
    expect(validateAutomation(a)).toMatch(/http/)
    a.action.url = 'https://hooks.slack.com/services/x'
    expect(validateAutomation(a)).toBeNull()
    expect(validateAutomation({ ...a, trigger: { type: 'error_rate', threshold: 2, window_minutes: 5 } })).toMatch(/between/)
  })
  it('describes triggers', () => {
    expect(describeTrigger({ type: 'score_threshold', scorer: 'judge', path: 'score', operator: 'lt', threshold: 3 })).toBe('judge.score < 3')
    expect(describeTrigger({ type: 'error_rate', threshold: 0.25, window_minutes: 15, op_names: ['agent'] })).toBe('error rate ≥ 25% over 15 min (agent)')
    expect(describeTrigger({ type: 'feedback_created', feedback_types: [] })).toBe('any feedback')
  })
  it('drops fields of other trigger types and prefers the secret', () => {
    const a = emptyAutomation('x')
    a.trigger = { type: 'feedback_created', feedback_types: [' weft.reaction ', ''], scorer: 'old', threshold: 3 }
    a.action = { type: 'webhook', url: 'https://x.test', url_secret: 'HOOK', headers: {}, message: '' }
    const c = cleanAutomation(a)
    expect(c.trigger).toEqual({ type: 'feedback_created', feedback_types: ['weft.reaction'] })
    expect(c.action).toEqual({ type: 'webhook', url_secret: 'HOOK' })
  })
})

describe('signals', () => {
  const opts = { model: 'mock/json', opNames: ['agent'], samplingRate: 0.5, latencySeconds: 3 }
  it('has unique ids and scorer names', () => {
    expect(new Set(SIGNALS.map((s) => s.id)).size).toBe(SIGNALS.length)
    expect(new Set(SIGNALS.map((s) => s.scorer)).size).toBe(SIGNALS.length)
  })
  it('avoids taken object ids', () => {
    expect(uniqueId('pii', new Set(['pii', 'pii-2']))).toBe('pii-3')
    const plan = planSignal(SIGNALS.find((s) => s.id === 'latency')!, opts, new Set(['latency', 'signal-latency']))
    expect(plan.scorer.objectId).toBe('latency-2')
    expect(plan.scorer.val).toMatchObject({ _class_name: 'RuleScorer', name: 'latency-2', rule: 'latency_gt', threshold: 3 })
    expect(plan.monitorId).toBe('signal-latency-2')
  })
  it('uses the mock moderation model offline', () => {
    const plan = planSignal(SIGNALS.find((s) => s.id === 'safety')!, opts, new Set())
    expect(plan.scorer.val.model).toBe('mock/moderation')
    expect(planSignal(SIGNALS.find((s) => s.id === 'safety')!, { ...opts, model: 'gpt-4o' }, new Set()).scorer.val.model).toBeUndefined()
  })
  it('publishes the scorer, then a monitor that references it', async () => {
    const published: [string, Record<string, unknown>][] = []
    const plan = planSignal(SIGNALS[0], opts, new Set())
    const res = await createSignal(plan, async (id, val) => {
      published.push([id, val])
      return { ref: `weft:///e/p/object/${id}:d${published.length}` }
    })
    expect(published.map((p) => p[0])).toEqual(['hallucination', 'signal-hallucination'])
    expect(published[1][1]).toMatchObject({ _class_name: 'Monitor', active: true, sampling_rate: 0.5, op_names: ['agent'], scorers: ['weft:///e/p/object/hallucination:d1'] })
    expect(res.monitorId).toBe('signal-hallucination')
  })
})
