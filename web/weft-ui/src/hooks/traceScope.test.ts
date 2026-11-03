import { describe, expect, it } from 'vitest'
import { traceConditions } from './useTraceScope'
import { legacyTraceView } from '../pages/traces/TracesPage'

describe('Traces filters', () => {
  it('matches agent, model and tool on any span of a trace when listing traces', () => {
    expect(traceConditions({ agent: 'researcher', tool: 'Bash' }, true, null)).toEqual([
      { $eq: [{ $getField: 'agent_name' }, { $literal: 'researcher' }] },
      { $eq: [{ $getField: 'tool_name' }, { $literal: 'Bash' }] },
    ])
    // Listing spans, those chips apply to each span instead.
    expect(traceConditions({ agent: 'researcher' }, false, null)).toEqual([])
  })

  it('keeps traces of matching sessions', () => {
    expect(traceConditions({}, true, ['s1', 's2'])).toEqual([{ $in: [{ $getField: 'conversation_id' }, [{ $literal: 's1' }, { $literal: 's2' }]] }])
  })

  it('reads views saved before Traces used the shared filter bar', () => {
    expect(legacyTraceView({ val: { view_type: 'traces', definition: { filter: { op_names: ['chat'] }, cols: ['inputs.q'], search: 'oslo' } } })).toEqual({
      op: 'chat',
      cols: 'inputs.q',
      q: 'oslo',
      time: 'all',
    })
    expect(legacyTraceView({ val: { view_type: 'traces', definition: { ui_params: { status: 'error' } } } })).toEqual({ status: 'error', time: 'all' })
    expect(legacyTraceView({ val: { view_type: 'filters.sessions' } })).toBeNull()
    expect(legacyTraceView({ val: { view_type: 'evaluations' } })).toBeNull()
  })
})
