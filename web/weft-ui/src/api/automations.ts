// Automations: Automation objects (stored with /api/v1/objects/save) and their runs.
import { post } from './client'
import type { Any } from './types'

export type TriggerType = 'score_threshold' | 'error_rate' | 'feedback_created'
export type Operator = 'gt' | 'gte' | 'lt' | 'lte' | 'eq' | 'ne'

export interface AutomationTrigger {
  type: TriggerType
  scorer?: string
  monitor_ref?: string | null
  path?: string
  operator?: Operator
  threshold?: number
  op_names?: string[]
  window_minutes?: number
  min_calls?: number
  feedback_types?: string[]
}

export interface AutomationAction {
  type: 'webhook' | 'slack'
  url?: string
  url_secret?: string
  headers?: Record<string, string>
  message?: string
}

export interface AutomationVal {
  _type: 'Automation'
  _class_name: 'Automation'
  _bases: string[]
  name: string
  description: string
  enabled: boolean
  trigger: AutomationTrigger
  action: AutomationAction
  cooldown_seconds?: number | null
}

export interface AutomationRun {
  id: string
  project_id: string
  automation_id: string
  automation_ref: string
  trigger_type: string
  action_type: string
  status: 'success' | 'error'
  test: boolean
  event: Any
  response_status: number | null
  response_body: string | null
  error: string | null
  duration_ms: number | null
  created_at: string
}

export const automationsApi = {
  runs: (project_id: string, automation_id?: string, limit = 100) =>
    post<{ runs: AutomationRun[] }>('/automations/runs/query', { project_id, automation_id: automation_id || undefined, limit }),
  test: (project_id: string, req: { automation_id?: string; val?: AutomationVal }) =>
    post<{ run: AutomationRun }>('/automations/test', { project_id, ...req }),
  check: (project_id: string) => post<{ runs: AutomationRun[] }>('/automations/check', { project_id }),
  validate: (val: AutomationVal) => post<{ valid: boolean }>('/automations/validate', { val }),
}
