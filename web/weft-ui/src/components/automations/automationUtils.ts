import type { AutomationAction, AutomationTrigger, AutomationVal, Operator } from '../../api/automations'

export const OPERATORS: { id: Operator; label: string }[] = [
  { id: 'lt', label: '<' },
  { id: 'lte', label: '≤' },
  { id: 'gt', label: '>' },
  { id: 'gte', label: '≥' },
  { id: 'eq', label: '=' },
  { id: 'ne', label: '≠' },
]

export const TRIGGER_LABELS: Record<AutomationTrigger['type'], string> = {
  score_threshold: 'Score crosses a threshold',
  error_rate: 'Error rate over a window',
  feedback_created: 'Feedback is created',
}

export function emptyAutomation(name = ''): AutomationVal {
  return {
    _type: 'Automation',
    _class_name: 'Automation',
    _bases: ['Object', 'BaseModel'],
    name,
    description: '',
    enabled: true,
    trigger: { type: 'score_threshold', scorer: '', path: '', operator: 'lt', threshold: 0.5 },
    action: { type: 'slack', url: '', url_secret: '', headers: {}, message: '' },
    cooldown_seconds: 300,
  }
}

const isHttpUrl = (s: string) => /^https?:\/\/[^\s/]+/i.test(s.trim())

/** Client-side validation mirroring the server's; null when valid. */
export function validateAutomation(v: AutomationVal): string | null {
  if (!v.name.trim()) return 'Enter a name'
  const t = v.trigger
  switch (t.type) {
    case 'score_threshold':
      if (!t.scorer?.trim()) return 'Choose the scorer to watch'
      if (t.threshold == null || Number.isNaN(t.threshold)) return 'Enter a threshold'
      if (!OPERATORS.some((o) => o.id === t.operator)) return 'Choose a comparison'
      break
    case 'error_rate':
      if (t.threshold == null || t.threshold < 0 || t.threshold > 1) return 'Error-rate threshold must be between 0% and 100%'
      if (!t.window_minutes || t.window_minutes <= 0) return 'Window must be at least one minute'
      break
    case 'feedback_created':
      break
    default:
      return 'Choose a trigger'
  }
  const a = v.action
  if (!a.url?.trim() && !a.url_secret?.trim()) return 'Enter a webhook URL or the name of a secret holding it'
  if (a.url?.trim() && !isHttpUrl(a.url)) return 'The URL must start with http:// or https://'
  return null
}

const opLabel = (op?: string) => OPERATORS.find((o) => o.id === op)?.label ?? op ?? '?'
const pct = (x?: number) => `${Math.round((x ?? 0) * 1000) / 10}%`

export function describeTrigger(t: AutomationTrigger): string {
  switch (t.type) {
    case 'score_threshold': {
      const s = `${t.scorer || '?'}${t.path ? '.' + t.path : ''}`
      return `${s} ${opLabel(t.operator)} ${t.threshold ?? '?'}`
    }
    case 'error_rate': {
      const ops = t.op_names?.length ? t.op_names.join(', ') : 'root calls'
      return `error rate ≥ ${pct(t.threshold)} over ${t.window_minutes ?? 60} min (${ops})`
    }
    case 'feedback_created':
      return t.feedback_types?.length ? `feedback ${t.feedback_types.join(', ')}` : 'any feedback'
  }
  return '?'
}

export function describeAction(a: AutomationAction): string {
  const where = a.url_secret ? `secret ${a.url_secret}` : (() => {
    try {
      return new URL(a.url ?? '').host
    } catch {
      return a.url ?? ''
    }
  })()
  return `${a.type === 'slack' ? 'Slack' : 'Webhook'} → ${where}`
}

/** Removes fields that do not apply to the chosen trigger before saving. */
export function cleanAutomation(v: AutomationVal): AutomationVal {
  const t = v.trigger
  let trigger: AutomationTrigger
  switch (t.type) {
    case 'score_threshold':
      trigger = { type: t.type, scorer: t.scorer?.trim(), path: t.path?.trim() ?? '', operator: t.operator, threshold: Number(t.threshold) }
      if (t.monitor_ref) trigger.monitor_ref = t.monitor_ref
      break
    case 'error_rate':
      trigger = { type: t.type, op_names: (t.op_names ?? []).filter(Boolean), window_minutes: Number(t.window_minutes ?? 60), threshold: Number(t.threshold), min_calls: Number(t.min_calls ?? 1) }
      break
    default:
      trigger = { type: 'feedback_created', feedback_types: (t.feedback_types ?? []).map((s) => s.trim()).filter(Boolean) }
  }
  const a = v.action
  const action: AutomationAction = { type: a.type }
  if (a.url_secret?.trim()) action.url_secret = a.url_secret.trim()
  else action.url = a.url?.trim()
  if (a.headers && Object.keys(a.headers).length) action.headers = a.headers
  if (a.message?.trim()) action.message = a.message
  return { ...v, name: v.name.trim(), trigger, action }
}
