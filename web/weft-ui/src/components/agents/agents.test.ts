import { describe, expect, it } from 'vitest'
import { eqQuery } from '../../api/agents'
import { errorRate, fmtPct, kindClass, opCategory, parseMaybeJSON } from './agentUtils'

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
