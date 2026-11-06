// The session Story (DESIGN.md §7 "Session page"): the timeline built by
// timeline.ts, retold as a conversation. Each entry is who acted and what
// it did: the user's request; an agent's reasoning directly above the action
// it led to (a tool call with its main argument, a hand-off); the other
// agent's work inside a hand-off, nested to any depth; model replies; the
// final answer. Model calls that only asked for a tool are folded into that
// tool call (they are its "model" in the inspector), and a reply repeated as
// the final answer or the hand-off's result is told once.
//
// Every timeline step belongs to exactly one entry (`owner`), so a deep link
// to any step (?step=<key>) selects the entry that tells it.
import type { BuiltTimeline, Step } from './timeline'

export interface Delegation {
  /** The hand-off step. */
  handoff: Step
  /** Its return of control, when recorded. */
  returned?: Step
  to: string
  /** What was asked (the hand-off's input). */
  ask: string
  /** What came back (the return's or the hand-off's output). */
  reply: string
  failed: boolean
  durationMs: number | null
  children: StoryEntry[]
}

interface Base {
  /** The entry's key: the key of the step it tells (used in ?step= and as the DOM id). */
  key: string
  step: Step
  agent: string | null
  /** Reasoning that led to this entry, in order. */
  thoughts: Step[]
}

export type StoryEntry =
  | { kind: 'turn'; key: string; turn: number }
  /** A user message, or (notice set) a message the harness sent as the user: a background task's notification. */
  | (Base & { kind: 'user'; text: string; notice?: string })
  | (Base & { kind: 'act'; model?: Step; delegation?: Delegation })
  | (Base & { kind: 'reply'; text: string })
  /** The turn's answer; at: the model call that wrote it, whose time it has. */
  | (Base & { kind: 'answer'; text: string; at?: Step })
  | (Base & { kind: 'thought' })
  | (Base & { kind: 'error' })

export type StepEntry = Exclude<StoryEntry, { kind: 'turn' }>

export interface Story {
  entries: StoryEntry[]
  /** Selectable entries in reading order (nested ones in place): what j/k walk through. */
  order: StepEntry[]
  byKey: Map<string, StepEntry>
  /** Any timeline step key → the key of the entry that tells it. */
  owner: Map<string, string>
}

const norm = (s: unknown) => String(s ?? '').replace(/\s+/g, ' ').trim()

/** The same text, allowing for previews cut at different lengths ("…"). */
export function sameText(a: unknown, b: unknown): boolean {
  const x = norm(a).replace(/…$/, '')
  const y = norm(b).replace(/…$/, '')
  if (!x || !y) return false
  if (x === y) return true
  const [short, long] = x.length < y.length ? [x, y] : [y, x]
  return short.length >= 200 && long.startsWith(short)
}

/** A background task's notification that a harness (Claude Code) sends as
 * a user message: its status and summary, or null for a real message. */
export function taskNotification(text: string): { status: string; summary: string } | null {
  if (!/^\s*<task-notification>/.test(text)) return null
  const tag = (name: string) => text.match(new RegExp(`<${name}>([\\s\\S]*?)</${name}>`))?.[1]?.trim() ?? ''
  return { status: tag('status') || 'finished', summary: tag('summary') }
}
const time = (s: string | null | undefined) => (s ? new Date(s).getTime() : NaN)

export function buildStory(tl: BuiltTimeline): Story {
  const steps = tl.steps
  const index = new Map(steps.map((s, i) => [s.key, i]))
  const used = new Set<string>()
  const owner = new Map<string, string>()
  const llmByCall = new Map<string, Step>()
  for (const s of steps)
    if (s.type === 'llm') for (const c of (s.item.payload?.tool_calls ?? []) as { id?: string }[]) if (c?.id && !llmByCall.has(c.id)) llmByCall.set(c.id, s)
  const turnSpans = new Set(steps.filter((s) => s.type === 'turn').map((s) => s.item.span_id))
  const toolCallIds = new Set(steps.filter((s) => s.type === 'tool_call').map((s) => s.item.payload?.tool_call_id).filter(Boolean))

  /** The non-return hand-off a tool call made: by the same agent, starting while the call ran. */
  const handoffOf = (i: number, end: number): number => {
    const s = steps[i]
    const t0 = time(s.item.started_at)
    const t1 = time(s.item.ended_at)
    for (let j = i + 1; j < end; j++) {
      const h = steps[j]
      if (h.type === 'turn') return -1
      if ((h.type === 'tool_call' || h.type === 'detection') && h.agent === s.agent && h.depth === s.depth) return -1
      if (h.type !== 'handoff' || h.item.payload?.kind === 'return' || used.has(h.key)) continue
      if ((h.item.payload?.from_agent ?? h.agent) !== s.agent) continue
      const at = time(h.item.started_at)
      if (Number.isFinite(t0) && Number.isFinite(at) && at < t0) continue
      if (Number.isFinite(t1) && Number.isFinite(at) && at > t1) return -1
      return j
    }
    return -1
  }

  /** Answer message key → the model call that wrote it. */
  const answeredBy = new Map<string, Step>()

  function scope(i0: number, i1: number, depth: number, ctx: { reply?: string; ownerKey?: string } = {}): StoryEntry[] {
    const out: StoryEntry[] = []
    let pending: Step[] = []
    let lastLlm: Step | undefined
    const claim = (key: string, xs: (Step | undefined)[]) => {
      for (const x of xs) if (x) {
        used.add(x.key)
        owner.set(x.key, key)
      }
    }
    const thoughtsFor = (...ss: Step[]): Step[] => {
      const led = ss.flatMap((s) => s.ledFrom.map((k) => tl.byKey.get(k)).filter((x): x is Step => !!x))
      const xs = [...pending, ...led].filter((x, i, a) => !used.has(x.key) && a.indexOf(x) === i).sort((a, b) => index.get(a.key)! - index.get(b.key)!)
      pending = []
      return xs
    }
    const flush = () => {
      if (!pending.length) return
      const first = pending[0]
      const e: StepEntry = { kind: 'thought', key: first.key, step: first, agent: first.agent, thoughts: pending }
      claim(e.key, pending)
      pending = []
      out.push(e)
    }
    const push = (e: StepEntry, extra: (Step | undefined)[] = []) => {
      claim(e.key, [e.step, ...e.thoughts, ...extra])
      out.push(e)
    }

    for (let i = i0; i < i1; i++) {
      const s = steps[i]
      if (used.has(s.key)) continue
      const p = s.item.payload ?? {}
      switch (s.type) {
        case 'turn':
          flush()
          used.add(s.key)
          if (s.turn > 1) out.push({ kind: 'turn', key: s.key, turn: s.turn })
          break
        case 'reasoning': {
          const target = s.ledTo ? tl.byKey.get(s.ledTo) : undefined
          const ti = target ? index.get(target.key)! : -1
          // Told with the action it led to, when that action comes later in this scope.
          if (target && !used.has(target.key) && ti > i && ti < i1) break
          pending.push(s)
          break
        }
        case 'message': {
          const text = String(p.text ?? '')
          if (p.role === 'user') {
            flush()
            const n = taskNotification(text)
            if (n) push({ kind: 'user', key: s.key, step: s, agent: s.agent, thoughts: [], text: n.summary || 'A background task finished.', notice: n.status })
            else push({ kind: 'user', key: s.key, step: s, agent: null, thoughts: [], text })
          } else push({ kind: 'answer', key: s.key, step: s, agent: s.agent, thoughts: thoughtsFor(s), text, at: answeredBy.get(s.key) })
          break
        }
        case 'llm': {
          lastLlm = s
          const calls = (p.tool_calls ?? []) as { id?: string; name?: string }[]
          const text = norm(p.output_preview)
          if (calls.some((c) => c.id && toolCallIds.has(c.id))) {
            used.add(s.key) // told with its tool call (owner set there)
            break
          }
          if (text) {
            const rest = steps.slice(i + 1, i1)
            const answer = rest.find((x) => x.type === 'message' && x.item.payload?.role !== 'user')
            const laterText = rest.some((x) => x.type === 'llm' && x.agent === s.agent && norm(x.item.payload?.output_preview))
            if (answer && sameText(answer.item.payload?.text, text)) {
              used.add(s.key)
              owner.set(s.key, answer.key)
              answeredBy.set(answer.key, s)
              break
            }
            if (ctx.reply && ctx.ownerKey && sameText(ctx.reply, text) && !laterText) {
              used.add(s.key)
              owner.set(s.key, ctx.ownerKey)
              break
            }
            // Reasoning of this same model call is recorded after it: it belongs above the reply.
            const own = steps.slice(i + 1, i1).filter((x) => x.type === 'reasoning' && x.item.span_id === s.item.span_id && !x.ledTo && !used.has(x.key))
            pending.push(...own)
            push({ kind: 'reply', key: s.key, step: s, agent: s.agent, thoughts: thoughtsFor(s), text: String(p.output_preview) })
          } else if (calls.length) {
            push({ kind: 'act', key: s.key, step: s, agent: s.agent, thoughts: thoughtsFor(s) })
          } else used.add(s.key) // an empty model call: nothing to tell
          break
        }
        case 'tool_call':
        case 'detection':
        case 'handoff': {
          if (s.type === 'handoff' && p.kind === 'return') {
            used.add(s.key)
            if (ctx.ownerKey) owner.set(s.key, ctx.ownerKey)
            break
          }
          const callId = p.tool_call_id as string | undefined
          const lastAsked = ((lastLlm?.item.payload?.tool_calls ?? []) as { id?: string }[]).some((c) => c.id && toolCallIds.has(c.id) && c.id !== callId)
          const model =
            s.type === 'tool_call' ? ((callId ? llmByCall.get(callId) : undefined) ?? (lastLlm?.agent === s.agent && !lastAsked ? lastLlm : undefined)) : undefined
          const j = s.type === 'handoff' ? i : handoffOf(i, i1)
          let delegation: Delegation | undefined
          let handoff: Step | undefined
          let returned: Step | undefined
          if (j >= 0) {
            handoff = steps[j]
            const hp = handoff.item.payload ?? {}
            let k = j + 1
            while (k < i1 && steps[k].type !== 'turn' && steps[k].depth > s.depth) k++
            returned = steps.slice(j + 1, k).find((x) => x.type === 'handoff' && x.item.payload?.kind === 'return')
            const reply = String(returned?.item.payload?.output_preview || hp.output_preview || '').trim()
            used.add(handoff.key)
            const thoughts = thoughtsFor(s, handoff)
            const children = scope(j + 1, k, s.depth + 1, { reply, ownerKey: s.key })
            delegation = {
              handoff,
              returned,
              to: String(hp.to_agent ?? ''),
              ask: String(hp.input_preview ?? ''),
              reply,
              failed: String(hp.status ?? '').toLowerCase() === 'error',
              durationMs: typeof hp.duration_ms === 'number' ? hp.duration_ms : handoff.durationMs,
              children,
            }
            push({ kind: 'act', key: s.key, step: s, agent: s.agent, thoughts, model, delegation }, [model, handoff, returned])
            i = k - 1
            break
          }
          push({ kind: 'act', key: s.key, step: s, agent: s.agent, thoughts: thoughtsFor(s), model }, [model])
          break
        }
        case 'error':
          // A failed turn is told by the outcome sentence; its cause is a step of its own.
          if (turnSpans.has(s.item.span_id)) break
          push({ kind: 'error', key: s.key, step: s, agent: s.agent, thoughts: thoughtsFor(s) })
          break
      }
    }
    flush()
    return out
  }

  const entries = scope(0, steps.length, 0)
  const order: StepEntry[] = []
  const walk = (xs: StoryEntry[]) => {
    for (const e of xs) {
      if (e.kind === 'turn') continue
      order.push(e)
      if (e.kind === 'act' && e.delegation) walk(e.delegation.children)
    }
  }
  walk(entries)
  const byKey = new Map(order.map((e) => [e.key, e]))
  // Steps told by no entry (a turn, an empty model call): the next entry in time order, else the last.
  const entryAt = order.map((e) => index.get(e.step.key) ?? 0)
  for (const s of steps) {
    if (owner.has(s.key)) continue
    const i = index.get(s.key)!
    let best = -1
    for (let k = 0; k < order.length; k++) if (entryAt[k] >= i && (best < 0 || entryAt[k] < entryAt[best])) best = k
    const e = best >= 0 ? order[best] : order[order.length - 1]
    if (e) owner.set(s.key, e.key)
  }
  return { entries, order, byKey, owner }
}

/** The entry to select for a step key (or the entry key itself). */
export function entryFor(story: Story, key: string | null | undefined): StepEntry | undefined {
  if (!key) return undefined
  return story.byKey.get(key) ?? story.byKey.get(story.owner.get(key) ?? '')
}

/** The entry j/k moves to from `current`. */
export function moveEntry(story: Story, current: string | null, dir: 1 | -1): string | null {
  const xs = story.order
  if (!xs.length) return null
  const i = current ? xs.findIndex((e) => e.key === current) : -1
  if (i < 0) return dir === 1 ? xs[0].key : xs[xs.length - 1].key
  return xs[Math.min(xs.length - 1, Math.max(0, i + dir))].key
}

/** Fields that hold a tool call's main argument, most telling first:
 * Bash's command, Read/Edit/Write's file path, Grep's pattern, a fetch's URL,
 * a search query, a sub-agent's prompt. */
const MAIN_ARG_FIELDS = ['command', 'cmd', 'file_path', 'notebook_path', 'pattern', 'url', 'query', 'q', 'path', 'prompt', 'description']

/** The main argument of a tool call as one string. Arguments arrive as an
 * object or as JSON text (Claude Code sends JSON text); either way the field
 * a person recognises is shown on its own ("ls", "src/app.ts"), and anything
 * else is shown as compact JSON. */
export function mainArgumentText(a: unknown): string {
  if (a == null || a === '') return ''
  let v: unknown = a
  if (typeof a === 'string') {
    const t = a.trim()
    if (!t.startsWith('{')) return a
    try {
      v = JSON.parse(t)
    } catch {
      return a
    }
  }
  if (v && typeof v === 'object' && !Array.isArray(v)) {
    const o = v as Record<string, unknown>
    for (const k of MAIN_ARG_FIELDS) {
      const x = o[k]
      if (typeof x === 'string' && x.trim()) return x
    }
  }
  return typeof a === 'string' ? a : JSON.stringify(a)
}
