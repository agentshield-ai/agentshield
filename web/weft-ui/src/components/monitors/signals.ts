import type { Any } from '../../api/types'

/**
 * Signals: ready-made monitor templates. Creating one publishes the scorer
 * object(s) and an active Monitor that runs them on new calls.
 */
export type SignalCategory = 'Quality' | 'Safety' | 'Reliability'

export interface SignalOptions {
  /** Judge model for LLM-based signals (e.g. gpt-4o-mini, mock/json). */
  model: string
  /** Span names to monitor; empty means all root spans. */
  opNames: string[]
  samplingRate: number
  /** latency signal threshold in seconds. */
  latencySeconds?: number
}

export interface SignalTemplate {
  id: string
  title: string
  description: string
  category: SignalCategory
  /** Which kind of model the signal needs. */
  model: 'judge' | 'moderation' | null
  /** Scorer name; feedback lands as weft.scorer.<scorer>. */
  scorer: string
  /** Score field to alert on, e.g. in an Automation's score_threshold trigger. */
  scorePath: string
  scorerVal: (o: SignalOptions) => Record<string, Any>
}

const BASES = ['Scorer', 'Object', 'BaseModel']
const cls = (c: string, fields: Record<string, Any>) => ({ _type: c, _class_name: c, _bases: BASES, ...fields })

const judge = (name: string, model: string, prompt: string) =>
  cls('LLMAsAJudgeScorer', { name, description: null, model, scoring_prompt: prompt, response_format: 'json_object' })

const rule = (name: string, r: string, threshold?: number) => cls('RuleScorer', { name, description: null, rule: r, ...(threshold != null ? { threshold } : {}) })

export const SIGNALS: SignalTemplate[] = [
  {
    id: 'hallucination',
    title: 'Hallucination',
    description: 'LLM judge flags outputs with claims the inputs do not support.',
    category: 'Quality',
    model: 'judge',
    scorer: 'hallucination',
    scorePath: 'hallucination_free',
    scorerVal: (o) => cls('HallucinationFreeScorer', { name: 'hallucination', description: null, model: o.model }),
  },
  {
    id: 'relevance',
    title: 'Relevance',
    description: 'LLM judge checks that the answer addresses what was asked.',
    category: 'Quality',
    model: 'judge',
    scorer: 'relevance',
    scorePath: 'is_relevant',
    scorerVal: (o) => cls('RelevanceScorer', { name: 'relevance', description: null, model: o.model }),
  },
  {
    id: 'task_completion',
    title: 'Task completion',
    description: 'LLM judge decides whether the agent finished the user’s task.',
    category: 'Quality',
    model: 'judge',
    scorer: 'task_completion',
    scorePath: 'completed',
    scorerVal: (o) =>
      judge(
        'task_completion',
        o.model,
        'You review an AI agent run.\n\nInputs: {inputs}\n\nFinal output: {output}\n\nDid the agent fully complete the task the user asked for? Reply with JSON {"completed": true|false, "reason": "..."}.',
      ),
  },
  {
    id: 'refusal',
    title: 'Refusal',
    description: 'LLM judge flags answers that decline or deflect the request.',
    category: 'Quality',
    model: 'judge',
    scorer: 'refusal',
    scorePath: 'refused',
    scorerVal: (o) =>
      judge(
        'refusal',
        o.model,
        'Inputs: {inputs}\n\nAssistant output: {output}\n\nDid the assistant refuse, decline or deflect the request instead of helping? Reply with JSON {"refused": true|false, "reason": "..."}.',
      ),
  },
  {
    id: 'user_frustration',
    title: 'User frustration',
    description: 'LLM judge spots users who are annoyed, repeating themselves or giving up.',
    category: 'Quality',
    model: 'judge',
    scorer: 'user_frustration',
    scorePath: 'frustrated',
    scorerVal: (o) =>
      judge(
        'user_frustration',
        o.model,
        'Read this conversation with an AI assistant.\n\nInputs: {inputs}\n\nAssistant output: {output}\n\nIs the user frustrated (annoyed, repeating themselves, complaining, giving up)? Reply with JSON {"frustrated": true|false, "score": <1-5, 5 = very frustrated>, "reason": "..."}.',
      ),
  },
  {
    id: 'safety',
    title: 'Safety / toxicity',
    description: 'OpenAI moderation API flags harmful or toxic output.',
    category: 'Safety',
    model: 'moderation',
    scorer: 'safety',
    scorePath: 'flagged',
    scorerVal: (o) =>
      // `model` is a Weft extension; the server defaults to omni-moderation-latest.
      cls('OpenAIModerationScorer', { name: 'safety', description: null, ...(o.model.startsWith('mock/') ? { model: 'mock/moderation' } : {}) }),
  },
  {
    id: 'pii',
    title: 'PII in output',
    description: 'Emails, phone numbers, SSNs, card numbers or IP addresses in the output.',
    category: 'Safety',
    model: null,
    scorer: 'pii',
    scorePath: 'flagged',
    scorerVal: () => rule('pii', 'pii_detected'),
  },
  {
    id: 'errors',
    title: 'Errors',
    description: 'Calls that raised an exception.',
    category: 'Reliability',
    model: null,
    scorer: 'error',
    scorePath: 'flagged',
    scorerVal: () => rule('error', 'exception'),
  },
  {
    id: 'tool_errors',
    title: 'Tool errors',
    description: 'A child call failed or a tool result reported an error.',
    category: 'Reliability',
    model: null,
    scorer: 'tool_error',
    scorePath: 'flagged',
    scorerVal: () => rule('tool_error', 'tool_error'),
  },
  {
    id: 'latency',
    title: 'Slow calls',
    description: 'Calls that took longer than a threshold.',
    category: 'Reliability',
    model: null,
    scorer: 'latency',
    scorePath: 'flagged',
    scorerVal: (o) => rule('latency', 'latency_gt', o.latencySeconds ?? 10),
  },
  {
    id: 'empty_output',
    title: 'Empty output',
    description: 'Calls that returned nothing.',
    category: 'Reliability',
    model: null,
    scorer: 'empty_output',
    scorePath: 'flagged',
    scorerVal: () => rule('empty_output', 'empty_output'),
  },
  {
    id: 'invalid_json',
    title: 'Invalid JSON',
    description: 'Outputs that should be JSON but do not parse.',
    category: 'Reliability',
    model: null,
    scorer: 'invalid_json',
    scorePath: 'flagged',
    scorerVal: () => rule('invalid_json', 'invalid_json'),
  },
]

/** First id not in taken: base, base-2, base-3, … */
export function uniqueId(base: string, taken: Set<string>): string {
  if (!taken.has(base)) return base
  for (let i = 2; ; i++) if (!taken.has(`${base}-${i}`)) return `${base}-${i}`
}

export interface SignalPlan {
  scorer: { objectId: string; val: Record<string, Any> }
  monitorId: string
  monitorVal: (scorerRef: string) => Record<string, Any>
}

/** Objects to publish for a template. taken lists existing object ids. */
export function planSignal(t: SignalTemplate, o: SignalOptions, taken: Set<string>): SignalPlan {
  const scorerId = uniqueId(t.scorer, taken)
  const val = t.scorerVal(o)
  val.name = scorerId
  const monitorId = uniqueId(`signal-${t.id}`, new Set([...taken, scorerId]))
  return {
    scorer: { objectId: scorerId, val },
    monitorId,
    monitorVal: (scorerRef) => ({
      _type: 'Monitor',
      _class_name: 'Monitor',
      _bases: ['Object', 'BaseModel'],
      name: monitorId,
      description: `Signal: ${t.title}. ${t.description}`,
      active: true,
      sampling_rate: Math.min(1, Math.max(0, o.samplingRate)),
      op_names: o.opNames,
      scorers: [scorerRef],
    }),
  }
}

/** Publishes a plan; publish returns the created object's ref. */
export async function createSignal(plan: SignalPlan, publish: (objectId: string, val: Any) => Promise<{ ref: string }>) {
  const s = await publish(plan.scorer.objectId, plan.scorer.val)
  const m = await publish(plan.monitorId, plan.monitorVal(s.ref))
  return { scorerRef: s.ref, monitorRef: m.ref, monitorId: plan.monitorId }
}
