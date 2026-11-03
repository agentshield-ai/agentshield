import { describe, expect, it } from 'vitest'
import type { Detection, DetectionStats } from '../../api/v1'
import { detectionParams, statsFromDetections } from './SecurityPage'

const det = (over: Partial<Detection>): Detection =>
  ({
    id: Math.random().toString(36),
    event_id: 'e',
    session_id: 's1',
    span_id: 'sp',
    trace_id: null,
    link: 'tool_call_id',
    engine_span_id: null,
    tool_call_id: null,
    tool_name: 'Bash',
    agent_name: 'coder',
    action: 'block',
    mode: 'enforce',
    severity: 'critical',
    rules: [{ id: 'r1', title: 'Pipe to shell', level: 'critical' }],
    source: 'engine',
    ruleset: null,
    event_source: null,
    overridden: false,
    overridden_at: null,
    replay_job_id: null,
    created_at: '2026-11-03T10:00:00Z',
    ...over,
  }) as Detection

describe('Security page filters', () => {
  it('maps the security chip to actions and session chips to session_ids', () => {
    expect(detectionParams({ has: 'flagged', agent: 'coder' }, 'all')).toEqual({ source: 'engine', agent: 'coder', action: 'block,require_approval' })
    expect(detectionParams({ team: 't' }, 'all', ['a', 'b']).session_ids).toEqual(['a', 'b'])
  })

  it('counts the figures from the detections the text filter kept', () => {
    const server = { by_agent: [{ agent: 'coder', count: 9, by_action: {}, tool_calls: 4, detection_rate: 1 }] } as unknown as DetectionStats
    const st = statsFromDetections(
      [det({}), det({ action: 'require_approval', overridden: true, span_id: null, rules: [{ id: 'r2', title: 'Creds', level: 'medium' }] }), det({})],
      server,
    )
    expect(st.total).toBe(3)
    expect(st.by_action).toEqual({ block: 2, require_approval: 1 })
    expect(st.overridden).toBe(1)
    expect(st.linked).toBe(2)
    expect(st.by_rule.map((r) => [r.rule_id, r.count])).toEqual([
      ['r1', 2],
      ['r2', 1],
    ])
    expect(st.by_agent).toEqual([{ agent: 'coder', count: 3, by_action: {}, tool_calls: 4, detection_rate: 0.75 }])
    expect(statsFromDetections([]).total).toBe(0)
  })
})
