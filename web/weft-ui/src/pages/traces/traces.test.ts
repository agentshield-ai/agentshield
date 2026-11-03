import { describe, expect, it } from 'vitest'
import { buildRequest, parseState, statusOp } from './TracesPage'

const ops = (url: string) => {
  const r = buildRequest(parseState(new URLSearchParams(url)))
  const e = r.query?.$expr as { $and?: unknown[] } | undefined
  return JSON.stringify(e?.$and ?? e ?? [])
}

describe('Traces filters', () => {
  it('status means the same as on Sessions: the run status the server computes for both pages', () => {
    expect(statusOp('error')).toEqual({ $eq: [{ $getField: 'summary.weft.run_status' }, { $literal: 'error' }] })
    for (const st of ['error', 'failed_step', 'ok', 'running', 'unknown']) {
      expect(ops(`status=${st}`)).toContain(`{"$getField":"summary.weft.run_status"},{"$literal":"${st}"}`)
      expect(ops(`status=${st}`)).not.toContain('summary.weft.status')
    }
    // Span status names from older links and saved views still work.
    expect(ops('status=success')).toContain('{"$literal":"ok"}')
    expect(ops('status=descendant_error')).toContain('{"$literal":"failed_step"}')
    expect(ops('status=bogus')).not.toContain('run_status')
  })

  it('hides AgentShield replay evaluations unless asked', () => {
    expect(ops('')).toContain('summary.weft.replay')
    expect(ops('replay=1')).not.toContain('summary.weft.replay')
  })
})
