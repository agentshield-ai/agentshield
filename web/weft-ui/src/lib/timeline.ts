// Builds the session timeline the UI shows from /api/v1/sessions/{id}/timeline
// and /api/v1/detections: detections merged onto their tool calls (by span_id,
// then tool_call_id), each reasoning step linked to the action it led to
// (next_span_id), nesting by agent through hand-offs, and the gaps between
// steps.
import type { Detection, TimelineItem } from '../api/v1'

export type StepType = TimelineItem['type'] | 'detection'

export interface Step {
  /** Stable key, used in the URL (?step=) and as the DOM id. */
  key: string
  type: StepType
  item: TimelineItem
  agent: string | null
  /** Nesting level: 0 for the turn's agent, +1 per active hand-off. */
  depth: number
  turn: number
  /** Detections on this step (tool calls), or the detection itself for unlinked ones. */
  detections: Detection[]
  /** Reasoning: the key of the step it led to. */
  ledTo: string | null
  /** Actions: the reasoning steps that led to it. */
  ledFrom: string[]
  /** Idle time before this step started, in ms (0 when it overlaps the previous one). */
  gapMs: number
  durationMs: number | null
  problem: ProblemKind | null
  /** The error message of a failed step (an `error` item on the same span, folded in). */
  errorText?: string
}

export type ProblemKind = 'block' | 'approval' | 'error'

const t = (s: string | null | undefined) => (s ? new Date(s).getTime() : NaN)

export function stepKey(it: TimelineItem): string {
  const p = it.payload ?? {}
  switch (it.type) {
    case 'reasoning':
      return `reasoning:${p.step_id ?? `${it.span_id}#${p.index ?? 0}`}`
    case 'handoff':
      return `handoff:${p.handoff_id ?? it.span_id}`
    case 'message':
      return `message:${p.role ?? 'x'}:${it.span_id}`
    case 'tool_call':
      return `tool:${it.span_id}`
    default:
      return `${it.type}:${it.span_id}`
  }
}

function isErrorStatus(s: unknown): boolean {
  return typeof s === 'string' && s.toLowerCase() === 'error'
}

function problemOf(type: StepType, it: TimelineItem, dets: Detection[]): ProblemKind | null {
  if (dets.some((d) => d.action === 'block' && d.source === 'engine')) return 'block'
  if (dets.some((d) => d.action === 'require_approval' && d.source === 'engine')) return 'approval'
  if (type === 'error') return 'error'
  if ((type === 'tool_call' || type === 'llm' || type === 'handoff' || type === 'turn') && isErrorStatus(it.payload?.status)) return 'error'
  return null
}

export interface BuiltTimeline {
  steps: Step[]
  byKey: Map<string, Step>
  /** Detections that matched no step (shown as their own steps). */
  unlinked: Detection[]
  /** Steps that need attention, in order (blocks first, then approvals, then errors). */
  problems: Step[]
  agents: string[]
  turns: number
}

export function buildTimeline(items: TimelineItem[], detections: Detection[] = []): BuiltTimeline {
  // Detections by span and by tool call id; only the engine's verdicts and replays.
  const bySpan = new Map<string, Detection[]>()
  const byCall = new Map<string, Detection[]>()
  for (const d of detections) {
    if (d.span_id) bySpan.set(d.span_id, [...(bySpan.get(d.span_id) ?? []), d])
    else if (d.tool_call_id) byCall.set(d.tool_call_id, [...(byCall.get(d.tool_call_id) ?? []), d])
  }
  const used = new Set<string>()

  const steps: Step[] = []
  const seen = new Map<string, number>()
  let stack: string[] = []
  let turn = 0
  let lastEnd = NaN
  const agents: string[] = []

  for (const it of items) {
    let key = stepKey(it)
    const n = seen.get(key) ?? 0
    seen.set(key, n + 1)
    if (n > 0) key = `${key}~${n}`
    const agent = it.agent_name
    if (agent && !agents.includes(agent)) agents.push(agent)

    if (it.type === 'turn') {
      turn++
      stack = agent ? [agent] : []
    } else if (stack.length === 0 && agent) stack = [agent]

    let depth = agent ? stack.lastIndexOf(agent) : 0
    if (depth < 0) {
      // An agent we have not seen hand-off into: nest it under the current one.
      stack.push(agent!)
      depth = stack.length - 1
    }
    if (it.type === 'handoff') {
      const p = it.payload ?? {}
      if (p.kind === 'return') {
        const i = stack.lastIndexOf(p.from_agent ?? agent)
        if (i > 0) stack = stack.slice(0, i)
      } else if (p.to_agent) {
        const i = stack.lastIndexOf(p.from_agent ?? agent)
        stack = [...stack.slice(0, i >= 0 ? i + 1 : stack.length), p.to_agent]
      }
    }

    let dets: Detection[] = []
    if (it.type === 'tool_call') {
      dets = [...(bySpan.get(it.span_id) ?? [])]
      const callId = it.payload?.tool_call_id
      if (callId && byCall.has(callId)) dets.push(...byCall.get(callId)!)
      for (const d of dets) used.add(d.id)
    }

    const start = t(it.started_at)
    const end = t(it.ended_at)
    const gap = Number.isFinite(lastEnd) && Number.isFinite(start) ? Math.max(0, start - lastEnd) : 0
    // A turn spans everything inside it, so only its start counts.
    if (Number.isFinite(end) && it.type !== 'turn') lastEnd = Number.isFinite(lastEnd) ? Math.max(lastEnd, end) : end
    else if (Number.isFinite(start)) lastEnd = Number.isFinite(lastEnd) ? Math.max(lastEnd, start) : start
    const durationMs = typeof it.payload?.duration_ms === 'number' ? it.payload.duration_ms : Number.isFinite(end) && Number.isFinite(start) ? end - start : null

    steps.push({
      key,
      type: it.type,
      item: it,
      agent,
      depth: it.type === 'turn' ? 0 : Math.max(0, depth),
      turn: Math.max(1, turn),
      detections: dets,
      ledTo: null,
      ledFrom: [],
      gapMs: gap,
      durationMs,
      problem: problemOf(it.type, it, dets),
    })
  }

  // Detections that found no tool step become steps of their own, placed by time.
  const unlinked = detections.filter((d) => !used.has(d.id) && d.source === 'engine')
  for (const d of unlinked) {
    const at = t(d.created_at)
    let idx = steps.findIndex((s) => t(s.item.started_at) > at)
    if (idx < 0) idx = steps.length
    const prev = steps[idx - 1]
    const item: TimelineItem = { type: 'tool_call', span_id: d.span_id ?? '', agent_name: d.agent_name, started_at: d.created_at, ended_at: null, payload: {} }
    steps.splice(idx, 0, {
      key: `detection:${d.id}`,
      type: 'detection',
      item,
      agent: d.agent_name,
      depth: prev ? prev.depth : 0,
      turn: prev ? prev.turn : 1,
      detections: [d],
      ledTo: null,
      ledFrom: [],
      gapMs: 0,
      durationMs: null,
      problem: d.action === 'block' ? 'block' : d.action === 'require_approval' ? 'approval' : null,
    })
  }

  // An `error` item on the span of a tool call, model call or hand-off is that step's failure: fold it in.
  const owner = new Map<string, Step>()
  for (const s of steps) if ((s.type === 'tool_call' || s.type === 'llm' || s.type === 'handoff') && !owner.has(s.item.span_id)) owner.set(s.item.span_id, s)
  for (let i = steps.length - 1; i >= 0; i--) {
    const s = steps[i]
    if (s.type !== 'error') continue
    const o = owner.get(s.item.span_id)
    if (!o) continue
    o.errorText = String(s.item.payload?.message ?? 'Error')
    if (!o.problem) o.problem = 'error'
    steps.splice(i, 1)
  }

  const byKey = new Map(steps.map((s) => [s.key, s]))
  linkReasoning(steps)

  const rank: Record<ProblemKind, number> = { block: 0, approval: 1, error: 2 }
  // Blocks first, then approvals, then errors; among errors the deepest (the delegate's failure that
  // made its callers fail) comes first, since that is usually the cause.
  const order = new Map(steps.map((s, i) => [s.key, i]))
  const problems = steps
    .filter((s) => s.problem)
    .sort((a, b) => rank[a.problem!] - rank[b.problem!] || (a.problem === 'error' ? b.depth - a.depth : 0) || order.get(a.key)! - order.get(b.key)!)
  return { steps, byKey, unlinked, problems, agents, turns: Math.max(1, turn) }
}

/** Ties each reasoning step to the tool call or hand-off it led to (next_span_id). */
export function linkReasoning(steps: Step[]): void {
  const actionBySpan = new Map<string, Step>()
  for (const s of steps) {
    if (s.type !== 'tool_call' && s.type !== 'handoff') continue
    if (s.type === 'handoff' && s.item.payload?.kind === 'return') continue
    const prev = actionBySpan.get(s.item.span_id)
    // Prefer the tool call over a hand-off on the same span.
    if (!prev || (prev.type === 'handoff' && s.type === 'tool_call')) actionBySpan.set(s.item.span_id, s)
  }
  for (const s of steps) {
    if (s.type !== 'reasoning') continue
    const next = s.item.payload?.next_span_id
    const target = next ? actionBySpan.get(next) : undefined
    if (target) {
      s.ledTo = target.key
      if (!target.ledFrom.includes(s.key)) target.ledFrom.push(s.key)
    }
  }
}

/** Finds a step from a deep link: a step key, or a bare span id (tool call first). */
export function findStep(tl: BuiltTimeline, ref: string | null): Step | undefined {
  if (!ref) return undefined
  const direct = tl.byKey.get(ref)
  if (direct) return direct
  const order: StepType[] = ['tool_call', 'handoff', 'llm', 'error', 'turn', 'reasoning', 'message']
  for (const type of order) {
    const s = tl.steps.find((x) => x.type === type && x.item.span_id === ref)
    if (s) return s
  }
  return undefined
}

/** The reasoning steps to expand when `key` is focused: those that led to it, or the step itself. */
export function reasoningFor(tl: BuiltTimeline, key: string): string[] {
  const s = tl.byKey.get(key)
  if (!s) return []
  if (s.type === 'reasoning') return [s.key]
  return s.ledFrom
}

export const STEP_LABEL: Record<StepType, string> = {
  turn: 'Turn',
  message: 'Message',
  llm: 'Model call',
  reasoning: 'Reasoning',
  tool_call: 'Tool call',
  handoff: 'Hand-off',
  error: 'Error',
  detection: 'Security verdict',
}

/** A short, readable one-line summary of a step (progressive disclosure: details sit behind "Details"). */
export function stepTitle(s: Step): string {
  const p = s.item.payload ?? {}
  switch (s.type) {
    case 'turn':
      return p.input_preview ? `Turn ${s.turn}: ${p.input_preview}` : `Turn ${s.turn}`
    case 'message':
      return p.role === 'user' ? 'User asked' : 'Final answer'
    case 'llm':
      return `${p.model ?? 'Model'} replied${Array.isArray(p.tool_calls) && p.tool_calls.length ? ` and asked for ${p.tool_calls.map((c: { name: string }) => c.name).join(', ')}` : ''}`
    case 'reasoning':
      return p.kind === 'redacted' ? 'Reasoning (redacted by the provider)' : p.kind === 'summary' ? 'Reasoning summary' : 'Reasoning'
    case 'tool_call':
      return `${p.tool_name ?? 'tool'}${p.arguments ? `(${oneLine(String(p.arguments), 80)})` : ''}`
    case 'handoff':
      return p.kind === 'return' ? `${p.from_agent} returned to ${p.to_agent}` : `${p.from_agent} handed off to ${p.to_agent}`
    case 'error':
      return p.message ? oneLine(String(p.message), 120) : 'Error'
    case 'detection':
      return `${s.detections[0]?.tool_name ?? 'Tool call'} (verdict without a recorded span)`
  }
}

export function oneLine(s: string, n: number): string {
  const x = s.replace(/\s+/g, ' ').trim()
  return x.length > n ? x.slice(0, n - 1) + '…' : x
}

/** Index of the next/previous navigable step for j/k. */
export function moveFocus(steps: Step[], current: string | null, dir: 1 | -1): string | null {
  if (!steps.length) return null
  const i = current ? steps.findIndex((s) => s.key === current) : -1
  if (i < 0) return dir === 1 ? steps[0].key : steps[steps.length - 1].key
  const j = Math.min(steps.length - 1, Math.max(0, i + dir))
  return steps[j].key
}
