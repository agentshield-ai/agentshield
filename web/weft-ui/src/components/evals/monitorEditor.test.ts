import { describe, expect, it } from 'vitest'
import { monitorScopeFields } from './MonitorEditor'

describe('monitor scopes', () => {
  it('stores the ticked scopes in a fixed order, and none for "where each scorer fits"', () => {
    expect(monitorScopeFields(['span', 'session'], 60)).toEqual({ scopes: ['session', 'span'], session_idle_seconds: 60 })
    expect(monitorScopeFields([], 120)).toEqual({ session_idle_seconds: 120 })
  })
  it('keeps the idle time within what the server accepts', () => {
    expect(monitorScopeFields([], 0).session_idle_seconds).toBe(60)
    expect(monitorScopeFields([], 1e9).session_idle_seconds).toBe(86400)
  })
})
