import { describe, expect, it } from 'vitest'
import { buildRequest, parseState, statusOp } from './TracesPage'

const ops = (url: string) => {
  const r = buildRequest(parseState(new URLSearchParams(url)))
  const e = r.query?.$expr as { $and?: unknown[] } | undefined
  return JSON.stringify(e?.$and ?? e ?? [])
}

describe('Traces filters', () => {
  it('Failed means an error anywhere in the trace (the span or one under it)', () => {
    expect(statusOp('error')).toEqual({ $in: [{ $getField: 'summary.weft.status' }, [{ $literal: 'error' }, { $literal: 'descendant_error' }]] })
    expect(statusOp('running')).toEqual({ $eq: [{ $getField: 'summary.weft.status' }, { $literal: 'running' }] })
    expect(ops('status=error')).toContain('descendant_error')
    // The shared bar's "ok" is a span's success.
    expect(ops('status=ok')).toContain('"success"')
  })

  it('hides AgentShield replay evaluations unless asked', () => {
    expect(ops('')).toContain('summary.weft.replay')
    expect(ops('replay=1')).not.toContain('summary.weft.replay')
  })
})
