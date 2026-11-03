import { describe, expect, it } from 'vitest'
import type { Detection, TimelineItem } from '../api/v1'
import { buildTimeline, findStep, moveFocus, reasoningFor, stepKey, stepTitle } from './timeline'

const at = (ms: number) => new Date(Date.UTC(2026, 10, 3, 9, 16, 0) + ms).toISOString()
const item = (type: TimelineItem['type'], span: string, agent: string, start: number, end: number | null, payload: Record<string, unknown>): TimelineItem => ({
  type,
  span_id: span,
  agent_name: agent,
  started_at: at(start),
  ended_at: end == null ? null : at(end),
  payload,
})

// The shape of /api/v1/sessions/{id}/timeline (docs/weft/API.md), with a failed tool call.
const ITEMS: TimelineItem[] = [
  item('turn', 'a1', 'orchestrator', 0, 4000, { input_preview: 'Find three facts about Oslo', status: 'OK', duration_ms: 4000 }),
  item('message', 'a1', 'orchestrator', 0, null, { role: 'user', text: 'Find three facts about Oslo' }),
  item('llm', 'a2', 'orchestrator', 50, 850, { model: 'claude-sonnet-4', tool_calls: [{ id: 'toolu_1', name: 'delegate_research', arguments: '{}' }] }),
  item('reasoning', 'a2', 'orchestrator', 50, null, { step_id: 'a2#0', kind: 'thinking', text: 'The researcher should gather them.', next_span_id: 'a3' }),
  item('tool_call', 'a3', 'orchestrator', 900, 2400, { tool_name: 'delegate_research', tool_call_id: 'toolu_1', arguments: 'Find facts', status: 'OK' }),
  item('handoff', 'a4', 'orchestrator', 1000, 2300, { handoff_id: 'h1', from_agent: 'orchestrator', to_agent: 'researcher', kind: 'delegate', status: 'ok' }),
  item('llm', 'a5', 'researcher', 1100, 1400, { model: 'gpt-4o' }),
  item('reasoning', 'a5', 'researcher', 1100, null, { step_id: 'a5#0', kind: 'thinking', text: 'I should search the web.', next_span_id: 'a6' }),
  item('tool_call', 'a6', 'researcher', 1450, 1850, { tool_name: 'web_search', tool_call_id: 'call_ws', arguments: 'Oslo facts', status: 'ERROR' }),
  item('error', 'a6', 'researcher', 1450, 1850, { message: 'upstream timeout' }),
  item('handoff', 'a4', 'researcher', 2300, 2300, { handoff_id: 'h2', from_agent: 'researcher', to_agent: 'orchestrator', kind: 'return' }),
  item('llm', 'a8', 'orchestrator', 4500, 5500, { model: 'claude-sonnet-4' }),
  item('message', 'a1', 'orchestrator', 5500, null, { role: 'assistant', text: 'Oslo is the capital.' }),
]

const det = (id: string, action: Detection['action'], over: Partial<Detection>): Detection => ({
  id,
  event_id: id,
  session_id: 's',
  span_id: null,
  trace_id: null,
  link: 'tool_call_id',
  engine_span_id: null,
  tool_call_id: null,
  tool_name: 'web_search',
  agent_name: 'researcher',
  action,
  mode: 'enforce',
  severity: 'high',
  rules: [{ id: 'r1', title: 'Rule one', level: 'high' }],
  source: 'engine',
  ruleset: null,
  event_source: null,
  overridden: false,
  overridden_at: null,
  replay_job_id: null,
  created_at: at(1500),
  ...over,
})

describe('buildTimeline', () => {
  const dets = [
    det('d-span', 'block', { span_id: 'a6' }),
    det('d-call', 'require_approval', { tool_call_id: 'toolu_1', tool_name: 'delegate_research', agent_name: 'orchestrator' }),
    det('d-orphan', 'log', { tool_call_id: 'nowhere', link: 'session', created_at: at(3000) }),
  ]
  const tl = buildTimeline(ITEMS, dets)

  it('merges detections onto tool calls by span id, then by tool call id', () => {
    expect(tl.byKey.get('tool:a6')!.detections.map((d) => d.id)).toEqual(['d-span'])
    expect(tl.byKey.get('tool:a3')!.detections.map((d) => d.id)).toEqual(['d-call'])
  })

  it('keeps unlinked verdicts as their own steps, placed by time', () => {
    expect(tl.unlinked.map((d) => d.id)).toEqual(['d-orphan'])
    const keys = tl.steps.map((s) => s.key)
    expect(keys.indexOf('detection:d-orphan')).toBeGreaterThan(keys.indexOf('handoff:h2'))
    expect(keys.indexOf('detection:d-orphan')).toBeLessThan(keys.indexOf('llm:a8'))
  })

  it('ties each reasoning step to the action it led to', () => {
    expect(tl.byKey.get('reasoning:a2#0')!.ledTo).toBe('tool:a3')
    expect(tl.byKey.get('reasoning:a5#0')!.ledTo).toBe('tool:a6')
    expect(tl.byKey.get('tool:a6')!.ledFrom).toEqual(['reasoning:a5#0'])
    expect(reasoningFor(tl, 'tool:a6')).toEqual(['reasoning:a5#0'])
    expect(reasoningFor(tl, 'reasoning:a2#0')).toEqual(['reasoning:a2#0'])
  })

  it('prefers the tool call over a hand-off on the same span', () => {
    const items = [
      item('reasoning', 'x1', 'a', 0, null, { step_id: 'x1#0', next_span_id: 'h' }),
      item('handoff', 'h', 'a', 10, 20, { handoff_id: 'hh', from_agent: 'a', to_agent: 'b', kind: 'handoff' }),
      item('tool_call', 'h', 'a', 10, 20, { tool_name: 'transfer_to_agent' }),
    ]
    expect(buildTimeline(items).byKey.get('reasoning:x1#0')!.ledTo).toBe('tool:h')
  })

  it('nests the delegate one level deeper until it returns', () => {
    const depth = (k: string) => tl.byKey.get(k)!.depth
    expect(depth('llm:a2')).toBe(0)
    expect(depth('handoff:h1')).toBe(0)
    expect(depth('llm:a5')).toBe(1)
    expect(depth('tool:a6')).toBe(1)
    expect(depth('handoff:h2')).toBe(1)
    expect(depth('llm:a8')).toBe(0)
    expect(tl.agents).toEqual(['orchestrator', 'researcher'])
  })

  it('folds an error item into the failed step and ranks problems', () => {
    expect(tl.steps.some((s) => s.type === 'error')).toBe(false)
    expect(tl.byKey.get('tool:a6')!.errorText).toBe('upstream timeout')
    expect(tl.problems.map((p) => [p.key, p.problem])).toEqual([
      ['tool:a6', 'block'],
      ['tool:a3', 'approval'],
    ])
  })

  it('measures idle gaps between steps', () => {
    expect(tl.byKey.get('llm:a8')!.gapMs).toBe(2100)
    expect(tl.byKey.get('llm:a5')!.gapMs).toBe(0)
  })

  it('gives every step a unique key', () => {
    const keys = tl.steps.map((s) => s.key)
    expect(new Set(keys).size).toBe(keys.length)
    expect(keys).toContain('message:user:a1')
    expect(keys).toContain('message:assistant:a1')
    expect(stepKey(ITEMS[0])).toBe('turn:a1')
  })
})

describe('deep links and navigation', () => {
  const tl = buildTimeline(ITEMS, [])
  it('finds a step by key or by bare span id (tool call first)', () => {
    expect(findStep(tl, 'handoff:h1')!.key).toBe('handoff:h1')
    expect(findStep(tl, 'a6')!.key).toBe('tool:a6')
    expect(findStep(tl, 'a2')!.key).toBe('llm:a2')
    expect(findStep(tl, 'missing')).toBeUndefined()
  })

  it('moves with j/k and stops at the ends', () => {
    expect(moveFocus(tl.steps, null, 1)).toBe(tl.steps[0].key)
    expect(moveFocus(tl.steps, tl.steps[0].key, -1)).toBe(tl.steps[0].key)
    expect(moveFocus(tl.steps, tl.steps[1].key, 1)).toBe(tl.steps[2].key)
    const last = tl.steps[tl.steps.length - 1].key
    expect(moveFocus(tl.steps, last, 1)).toBe(last)
  })

  it('summarises steps in plain words', () => {
    expect(stepTitle(tl.byKey.get('tool:a6')!)).toBe('web_search(Oslo facts)')
    expect(stepTitle(tl.byKey.get('handoff:h1')!)).toBe('orchestrator handed off to researcher')
    expect(stepTitle(tl.byKey.get('handoff:h2')!)).toBe('researcher returned to orchestrator')
    expect(stepTitle(tl.byKey.get('llm:a2')!)).toBe('claude-sonnet-4 replied and asked for delegate_research')
  })
})

describe('problem order', () => {
  it('puts the deepest failure first among errors', () => {
    const items = [
      item('turn', 't', 'o', 0, 100, {}),
      item('tool_call', 'd', 'o', 1, 90, { tool_name: 'delegate', status: 'ERROR' }),
      item('handoff', 'h', 'o', 2, 80, { handoff_id: 'h', from_agent: 'o', to_agent: 'r', kind: 'delegate', status: 'error' }),
      item('tool_call', 'w', 'r', 3, 70, { tool_name: 'web_search', status: 'ERROR' }),
    ]
    expect(buildTimeline(items).problems.map((p) => p.key)).toEqual(['tool:w', 'tool:d', 'handoff:h'])
  })
})
