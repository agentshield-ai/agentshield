import { describe, expect, it } from 'vitest'
import { eqQuery, evalSpansUrl, type AgentEvalPrediction, type AgentEvalRun } from '../../api/agents'
import { errorRate, fmtPct, kindClass, opCategory, parseMaybeJSON } from './agentUtils'
import { fmtScoreValue, predictionsByTrace, summaryMetrics } from './agentEvalUtils'
import { activeFilterCount, EMPTY_SPAN_FILTERS, readSpanFilters, spanQueryParts, writeSpanFilters } from './signalUtils'

describe('agent helpers', () => {
  it('maps operations to display categories', () => {
    expect(opCategory('invoke_agent')).toBe('agent')
    expect(opCategory('chat')).toBe('llm')
    expect(opCategory('execute_tool')).toBe('tool')
    expect(opCategory('chain')).toBe('other')
    expect(kindClass('chat', 'ERROR')).toBe('kind-error')
    expect(kindClass('execute_tool', 'OK')).toBe('kind-tool')
  })
  it('builds equality queries', () => {
    expect(eqQuery({})).toBeUndefined()
    expect(eqQuery({ agent_name: 'a', skip: '' })).toEqual({ $expr: { $eq: [{ $getField: 'agent_name' }, { $literal: 'a' }] } })
    expect(eqQuery({ a: 1, b: true })).toEqual({
      $expr: { $and: [{ $eq: [{ $getField: 'a' }, { $literal: 1 }] }, { $eq: [{ $getField: 'b' }, { $literal: true }] }] },
    })
  })
  it('formats rates and JSON', () => {
    expect(errorRate(1, 4)).toBe(0.25)
    expect(errorRate(0, 0)).toBeNull()
    expect(fmtPct(0.25)).toBe('25%')
    expect(fmtPct(0.05)).toBe('5.0%')
    expect(parseMaybeJSON('{"a":1}')).toEqual({ a: 1 })
    expect(parseMaybeJSON('plain')).toBe('plain')
  })
})

describe('agent evals', () => {
  it('links to the Spans view filtered to a run', () => {
    expect(evalSpansUrl('e/p', 'run-1')).toBe('/e/p/agent-analytics?tab=spans&eval=run-1&range=all')
    expect(evalSpansUrl('e/p', 'run-1', { pred: 'pas-1', conversations: true })).toBe('/e/p/agent-analytics?tab=spans&eval=run-1&range=all&pred=pas-1&group=conversation_id')
    expect(evalSpansUrl('e/p', 'run-1', { agent: 'a b' })).toBe('/e/p/agents/a%20b/analytics?tab=spans&eval=run-1&range=all')
  })
  it('round-trips the evaluation filter through the URL and into the query', () => {
    const f = readSpanFilters(new URLSearchParams('eval=run-1&pred=pas-2&op=chat'))
    expect(f.evalRun).toBe('run-1')
    expect(f.evalPred).toBe('pas-2')
    expect(writeSpanFilters(f)).toMatchObject({ eval: 'run-1', pred: 'pas-2', op: 'chat' })
    expect(spanQueryParts(f)).toMatchObject({ evaluation_run_id: 'run-1', prediction_span_id: 'pas-2' })
    expect(activeFilterCount(f)).toBe(2)
    expect(spanQueryParts(EMPTY_SPAN_FILTERS).evaluation_run_id).toBeUndefined()
  })
  it('picks summary metrics and formats scores', () => {
    expect(summaryMetrics({ ok: { true_count: 2, true_fraction: 0.5 }, judge: { mean: 4 }, model_latency: { mean: 0.2 } })).toEqual({
      'ok.true_fraction': 0.5,
      'judge.mean': 4,
      'model_latency.mean': 0.2,
    })
    expect(fmtScoreValue('ok.true_fraction', 0.5)).toBe('50.0%')
    expect(fmtScoreValue('resolved', true)).toBe('✓')
  })
  it('indexes predictions by trace', () => {
    const pred = (id: string, traces: string[]) => ({ prediction_span_id: id, trace_ids: traces }) as AgentEvalPrediction
    const runs = [{ evaluation_run_id: 'r1', predictions: [pred('p1', ['t1', 't2'])] }, { evaluation_run_id: 'r2', predictions: [pred('p2', ['t1'])] }] as AgentEvalRun[]
    const m = predictionsByTrace(runs)
    expect(m.get('t1')?.map((x) => x.pred.prediction_span_id)).toEqual(['p1', 'p2'])
    expect(m.get('t2')?.length).toBe(1)
    expect(predictionsByTrace(undefined).size).toBe(0)
  })
})
