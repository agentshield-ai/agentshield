// Links between evaluation predictions and existing spans (unit tested in linkedCalls.test.ts).
//
// - EvaluationLogger predictions linked to existing traces carry
//   attributes.weft.linked_spans ([{span_id, trace_id?}] or ids / span refs),
//   attributes.weft.genai_span_ref ([{trace_id, span_id}]) and
//   attributes.weft.linked_conversation_ids.
// - Agent spans started inside a prediction carry weft.eval.* attributes
//   (run_id, prediction_span_id, ...), flat, nested or as OTel attributes.
// - Spans recorded by earlier SDK versions use the deprecated keys
//   weft.linked_calls ({call_id} items) and weft.eval.predict_and_score_call_id
//   (docs/weft/API.md, "Span attributes"); both are read.

export interface LinkedCall {
  callId: string
  traceId?: string
}

export interface EvalLink {
  runId?: string
  /** The prediction's predict_and_score span. */
  predictionSpanId?: string
  extra: Record<string, string>
}

export interface Links {
  calls: LinkedCall[]
  conversations: string[]
  evalLink: EvalLink | null
}

type Obj = Record<string, unknown>
const asObj = (v: unknown): Obj | undefined => (v && typeof v === 'object' && !Array.isArray(v) ? (v as Obj) : undefined)
const s = (v: unknown) => (typeof v === 'string' && v ? v : undefined)

/** A span id from an id, a weft:/// span ref, or {span_id|call_id|id}. */
function callOf(v: unknown): LinkedCall | null {
  if (typeof v === 'string') {
    const m = /^weft:\/\/\/[^/]+\/[^/]+\/(?:span|call)\/([^/?#]+)/.exec(v)
    if (m) return { callId: m[1] }
    return /^[\w-]{6,}$/.test(v) ? { callId: v } : null
  }
  const o = asObj(v)
  if (!o) return null
  const id = s(o.span_id) ?? s(o.call_id) ?? s(o.id)
  return id ? { callId: id, traceId: s(o.trace_id) } : null
}

export function linksOf(attributes: unknown): Links {
  const a = asObj(attributes) ?? {}
  const weft = asObj(a.weft) ?? {}
  const calls: LinkedCall[] = []
  const seen = new Set<string>()
  const add = (c: LinkedCall | null) => {
    if (!c) return
    const prev = calls.find((x) => x.callId === c.callId)
    if (prev) {
      prev.traceId ??= c.traceId
      return
    }
    if (seen.has(c.callId)) return
    seen.add(c.callId)
    calls.push(c)
  }
  for (const key of ['linked_spans', 'linked_calls', 'genai_span_ref']) {
    const v = weft[key]
    for (const x of Array.isArray(v) ? v : v != null ? [v] : []) add(callOf(x))
  }
  const conversations = (Array.isArray(weft.linked_conversation_ids) ? weft.linked_conversation_ids : []).filter((x): x is string => typeof x === 'string' && !!x)

  // weft.eval.*: flat keys, nested attributes.weft.eval, or OTel span attributes.
  const ev: Record<string, string> = {}
  const collect = (o: Obj | undefined, prefix: string) => {
    for (const [k, v] of Object.entries(o ?? {})) if (k.startsWith(prefix) && (typeof v === 'string' || typeof v === 'number')) ev[k.slice(prefix.length)] = String(v)
  }
  collect(a, 'weft.eval.')
  collect(asObj(asObj(a.otel)?.attributes), 'weft.eval.')
  for (const [k, v] of Object.entries(asObj(weft.eval) ?? {})) if (typeof v === 'string' || typeof v === 'number') ev[k] = String(v)
  let evalLink: EvalLink | null = null
  if (Object.keys(ev).length) {
    const { run_id, prediction_span_id, predict_and_score_call_id, ...extra } = ev
    evalLink = { runId: run_id, predictionSpanId: prediction_span_id ?? predict_and_score_call_id, extra }
  }
  return { calls, conversations, evalLink }
}

export const hasLinks = (l: Links) => l.calls.length > 0 || l.conversations.length > 0 || l.evalLink != null
