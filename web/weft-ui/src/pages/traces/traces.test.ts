import { describe, expect, it } from 'vitest'
import { buildRequest, parseState, statusOp } from './TracesPage'

const ops = (url: string) => {
  const r = buildRequest(parseState(new URLSearchParams(url)))
  const e = r.query?.$expr as { $and?: unknown[] } | undefined
  return JSON.stringify(e?.$and ?? e ?? [])
}

describe('Traces filters', () => {
  it('status means the same as on Sessions: Failed is the trace itself, a failed step is descendant_error', () => {
    expect(statusOp('error')).toEqual({ $eq: [{ $getField: 'summary.weft.status' }, { $literal: 'error' }] })
    expect(ops('status=error')).toContain('{"$literal":"error"}')
    expect(ops('status=error')).not.toContain('descendant_error')
    expect(ops('status=failed_step')).toContain('{"$literal":"descendant_error"}')
    // The shared bar's "ok" is a span's success (no failure anywhere under it).
    expect(ops('status=ok')).toContain('{"$literal":"success"}')
    expect(ops('status=running')).toContain('{"$literal":"running"}')
    // "No agent data" has no span status: it applies through sessions.
    expect(ops('status=unknown')).not.toContain('summary.weft.status')
  })

  it('hides AgentShield replay evaluations unless asked', () => {
    expect(ops('')).toContain('summary.weft.replay')
    expect(ops('replay=1')).not.toContain('summary.weft.replay')
  })
})
