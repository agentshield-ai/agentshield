// Weft's native API (/api/v1, docs/weft/API.md "Native API" and "AgentShield
// detections"): sessions, agents, teams, hand-offs, reasoning, scores and
// AgentShield detections.
import { get, post } from './client'

/** `unknown`: a session known only from AgentShield verdicts (`verdicts_only`), whose outcome is not known. */
export type SessionStatus = 'running' | 'ok' | 'error' | 'unknown'
export type DetectionAction = 'allow' | 'log' | 'require_approval' | 'block'
export type ScoreSummary = Record<string, { mean: number; count: number }>

export interface List<T> {
  items: T[]
  total_count: number
}

export interface SessionSummary {
  session_id: string
  name: string
  title: string
  started_at: string
  last_activity: string
  /** Null for a verdicts-only session: its duration is unknown. */
  duration_ms: number | null
  /** Known only from AgentShield verdicts; no agent spans were recorded. */
  verdicts_only?: boolean
  agents: string[]
  team_id: string | null
  team_name: string | null
  turn_count: number
  trace_count: number
  span_count: number
  llm_call_count: number
  tool_call_count: number
  handoff_count: number
  reasoning_step_count: number
  detection_count: Partial<Record<DetectionAction, number>>
  error_count: number
  input_tokens: number
  output_tokens: number
  reasoning_tokens: number
  cost_usd: number | null
  status: SessionStatus
  /** Status ok, but a step failed (the status filter's failed_step): see filters.ts STATUS_HELP. */
  failed_step?: boolean
  models: string[]
  tools: string[]
  scores: ScoreSummary
  trace_ids?: string[]
}

export type TimelineType = 'turn' | 'message' | 'llm' | 'reasoning' | 'tool_call' | 'handoff' | 'error'

export interface TimelineItem {
  type: TimelineType
  span_id: string
  trace_id?: string
  agent_name: string | null
  started_at: string
  ended_at: string | null
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  payload: Record<string, any>
}

export interface GraphNode {
  agent: string
  calls: number
  /** Runs (invocations) that ended with an error; `errors` counts every failed span. */
  failed_calls?: number
  spans?: number
  errors: number
  tokens: number
  cost_usd: number | null
  tool_calls?: number
  detections: Partial<Record<DetectionAction, number>>
  sessions?: number
}

export interface GraphEdge {
  from: string
  to: string
  kind: 'delegate' | 'handoff' | 'return'
  count: number
  errors: number
  error_rate: number
  p50_ms: number | null
  p95_ms: number | null
  mean_score: number | null
  scores?: ScoreSummary
}

export interface Graph {
  nodes: GraphNode[]
  edges: GraphEdge[]
}

export interface ReasoningStep {
  id: string
  span_id: string
  session_id: string
  trace_id: string
  agent_name: string | null
  index: number
  span_index: number
  kind: 'thinking' | 'summary' | 'redacted'
  text: string
  tokens: number | null
  started_at: string
  next_span_id: string | null
  source: string
}

export interface AgentSummary {
  agent_name: string
  invocation_count: number
  span_count: number
  error_count: number
  invocation_error_count?: number
  first_seen: string | null
  last_seen: string | null
  total_input_tokens: number
  total_output_tokens: number
  total_cost_usd: number | null
  latency_ms_p50: number | null
  latency_ms_p95?: number | null
  llm_call_count: number
  tool_call_count: number
  models: string[] | null
  reasoning_tokens: number
  reasoning_step_count: number
  handoffs_in: number
  handoffs_out: number
  session_count: number
  tools: string[] | null
  teams: string[] | null
  detection_counts: Partial<Record<DetectionAction, number>>
  /** Tool calls with at least one non-allow engine verdict linked to them (API.md "Detection rate"). */
  flagged_tool_calls?: number
  /** flagged_tool_calls / tool_call_count, 0..1; null without recorded tool calls. */
  detection_rate: number | null
  scores: ScoreSummary
}

export interface AgentDetail extends AgentSummary {
  tool_counts: { tool: string; count: number; errors: number }[]
  handoffs_from: { agent: string; kind: string; count: number }[]
  handoffs_to: { agent: string; kind: string; count: number }[]
  recent_sessions: { session_id: string; team_id: string | null; last_activity: string }[]
}

export interface TeamSummary {
  team_id: string
  name: string
  auto: boolean
  agents: string[]
  session_count: number
  error_session_count: number
  handoff_count: number
  span_count: number
  input_tokens: number
  output_tokens: number
  cost_usd: number | null
  first_seen: string | null
  last_activity: string | null
  detection_count: Partial<Record<DetectionAction, number>>
}

export interface TeamDetail extends TeamSummary {
  agent_stats: {
    agent: string
    calls: number
    failed_calls?: number
    errors: number
    tool_calls: number
    tokens: number
    cost_usd: number | null
    sessions: number
    handoffs_in: number
    handoffs_out: number
    detections: Partial<Record<DetectionAction, number>>
  }[]
  graph: Graph
  handoff_stats: GraphEdge[]
  recent_sessions: SessionSummary[]
}

export interface Handoff {
  id: string
  session_id: string
  trace_id: string
  source_span_id: string
  from_span_id: string
  to_span_id: string | null
  from_agent: string
  to_agent: string
  kind: string
  source: string
  started_at: string
  ended_at: string | null
  duration_ms: number | null
  status: 'running' | 'ok' | 'error'
  input_preview: string
  output_preview: string
  team_id: string | null
}

export interface Score {
  id: string
  scope: 'session' | 'agent' | 'handoff' | 'turn' | 'span'
  session_id?: string
  agent?: string
  handoff_id?: string
  turn_id?: string
  span_id?: string
  scorer: string
  value: number | null
  reason?: string
  model?: string
  output?: unknown
  ref?: string
  feedback_type?: string
  creator?: string | null
  created_at: string
}

export interface ScoreRunResult {
  scope: string
  session_id?: string
  agent?: string
  handoff_id?: string
  scorer: string
  value?: number | null
  reason?: string
  error?: string
  feedback_id?: string
}

export interface DetectionRule {
  id: string
  title: string
  level: string
  tags?: string[]
}

export interface Detection {
  id: string
  event_id: string
  session_id: string
  /** The session's name or first request; set on /api/v1/detections lists. */
  session_title?: string
  span_id: string | null
  trace_id: string | null
  link: 'tool_call_id' | 'nearest_time' | 'replay' | 'session' | 'none'
  engine_span_id: string | null
  tool_call_id: string | null
  tool_name: string | null
  agent_name: string | null
  action: DetectionAction
  mode: 'enforce' | 'audit' | 'shadow'
  severity: string
  rules: DetectionRule[]
  source: 'engine' | 'replay'
  ruleset: string | null
  event_source: string | null
  overridden: boolean
  overridden_at: string | null
  replay_job_id: string | null
  created_at: string
  ingested_at?: string
}

export interface DetectionStats {
  total: number
  by_action: Partial<Record<DetectionAction, number>>
  by_severity: Record<string, number>
  by_source: Record<string, number>
  overridden: number
  linked: number
  by_rule: { rule_id: string; title: string; level: string; count: number; by_action: Partial<Record<DetectionAction, number>> }[]
  by_agent: {
    agent: string
    /** Verdicts, including ones not linked to a recorded tool call. */
    count: number
    by_action: Partial<Record<DetectionAction, number>>
    tool_calls: number
    flagged_tool_calls?: number
    /** flagged_tool_calls / tool_calls, 0..1. */
    detection_rate: number | null
  }[]
  by_day: { day: string; count: number; by_action: Partial<Record<DetectionAction, number>>; by_severity: Record<string, number> }[]
}

export interface ShieldInsights {
  bucket: string
  approval_fatigue: {
    threshold: number
    series: { bucket: string; require_approval: number; overridden: number }[]
    by_session: { session_id: string; session_title?: string; agents: string[]; require_approval: number; approved: number; first_at: string; last_at: string; per_hour: number; fatigued: boolean }[]
    by_agent: { agent: string; require_approval: number; approved: number; sessions: number; approval_rate: number }[]
  }
  override_escalation: {
    threshold: number
    series: { bucket: string; overrides: number; overridden_blocks: number }[]
    by_session: {
      session_id: string
      /** The session's name or first request (absent when it has neither). */
      session_title?: string
      agents: string[]
      overrides: number
      overridden_blocks: number
      overridden_approvals: number
      rule_ids: string[]
      first_at: string
      last_at: string
      escalating: boolean
    }[]
  }
  cross_agent: {
    team_id: string
    agents: string[]
    sessions: number
    detections: number
    agents_with_detections: string[]
    shared_rules: { rule_id: string; title: string; level: string; agents: string[]; count: number }[]
    incidents: { start: string; end: string; agents: string[]; rule_ids: string[]; session_ids: string[]; detections: number; max_action: string }[]
    correlated: boolean
  }[]
}

export interface ReplayVerdict {
  action: DetectionAction
  severity: string
  rule_ids: string[]
  mode: string
  detection_id?: string
}

export interface ReplayChange {
  span_id: string
  trace_id: string
  session_id: string
  tool_call_id: string | null
  tool_name: string
  agent_name: string | null
  started_at: string
  before: ReplayVerdict | null
  after: ReplayVerdict | null
}

export interface ReplayJob {
  job_id: string
  status: 'queued' | 'running' | 'done' | 'failed'
  label: string
  ruleset: string
  engine: string
  rules_loaded?: number
  session_ids: string[] | null
  created_at: string
  started_at: string | null
  finished_at: string | null
  error: string | null
  summary?: {
    tool_calls: number
    evaluated: number
    errors: number
    detections: number
    blocked: number
    approvals_required: number
    with_engine_verdict: number
    newly_blocked: number
    no_longer_blocked: number
    changed: number
    unchanged: number
  }
  newly_blocked?: ReplayChange[]
  no_longer_blocked?: ReplayChange[]
  changed?: ReplayChange[]
  truncated?: boolean
  first_error?: string
  /** Hashed replay session keys ("sha256:<hex>") to the recorded session ids. */
  replay_sessions?: Record<string, string>
}

export interface ShieldStatus {
  replay_enabled: boolean
  replay_engine: 'in-process' | 'remote' | ''
  correlation_window_ms: number
}

/** GET /api/v1/sessions/attention: the attention cards, counted on the server over every matching session. */
export interface SessionAttention {
  items: {
    id: 'failed' | 'failed_step' | 'security' | 'low_score' | 'cost'
    count: number
    /** /api/v1/sessions parameters that, added to the request's, list exactly these sessions. */
    filter: Record<string, string>
    latest: { session_id: string; title: string } | null
  }[]
  sessions: number
  blocked: number
  approval_heavy: number
  lowest_score: number | null
  cost_spike_threshold: number | null
  approval_heavy_threshold: number
  low_score_threshold: number
  cost_spike_factor: number
}

type Params = Record<string, string | number | boolean | string[] | null | undefined>

/** Query string; an array becomes a repeated parameter (session_ids=a&session_ids=b). */
function qs(projectId: string, params: Params = {}): string {
  const u = new URLSearchParams({ project_id: projectId })
  for (const [k, v] of Object.entries(params)) {
    if (Array.isArray(v)) v.forEach((x) => u.append(k, x))
    else if (v != null && v !== '') u.set(k, String(v))
  }
  return u.toString()
}

const seg = (s: string) => encodeURIComponent(s)

export const v1 = {
  sessions: (pid: string, p: Params = {}) => get<List<SessionSummary>>(`/api/v1/sessions?${qs(pid, p)}`),
  sessionsAttention: (pid: string, p: Params = {}) => get<SessionAttention>(`/api/v1/sessions/attention?${qs(pid, p)}`),
  session: (pid: string, id: string) => get<SessionSummary>(`/api/v1/sessions/${seg(id)}?${qs(pid)}`),
  timeline: (pid: string, id: string, p: Params = {}) => get<List<TimelineItem> & { session_id: string }>(`/api/v1/sessions/${seg(id)}/timeline?${qs(pid, { limit: 1000, ...p })}`),
  graph: (pid: string, id: string) => get<Graph>(`/api/v1/sessions/${seg(id)}/graph?${qs(pid)}`),
  reasoning: (pid: string, id: string, p: Params = {}) => get<List<ReasoningStep>>(`/api/v1/sessions/${seg(id)}/reasoning?${qs(pid, p)}`),
  agents: (pid: string, p: Params = {}) => get<List<AgentSummary>>(`/api/v1/agents?${qs(pid, p)}`),
  agent: (pid: string, name: string) => get<AgentDetail>(`/api/v1/agents/${seg(name)}?${qs(pid)}`),
  teams: (pid: string, p: Params = {}) => get<List<TeamSummary>>(`/api/v1/teams?${qs(pid, p)}`),
  team: (pid: string, id: string) => get<TeamDetail>(`/api/v1/teams/${seg(id)}?${qs(pid)}`),
  handoffs: (pid: string, p: Params = {}) => get<List<Handoff>>(`/api/v1/handoffs?${qs(pid, p)}`),
  scores: (pid: string, p: Params = {}) => get<List<Score>>(`/api/v1/scores?${qs(pid, p)}`),
  runScores: (body: {
    project_id: string
    scope: 'session' | 'agent' | 'handoff'
    scorers: string[]
    session_ids?: string[]
    agent?: string
    handoff_ids?: string[]
    model?: string
  }) => post<List<ScoreRunResult>>('/api/v1/scores/run', body),
  detections: (pid: string, p: Params = {}) => get<List<Detection>>(`/api/v1/detections?${qs(pid, p)}`),
  detectionStats: (pid: string, p: Params = {}) => get<DetectionStats>(`/api/v1/detections/stats?${qs(pid, p)}`),
  insights: (pid: string, p: Params = {}) => get<ShieldInsights>(`/api/v1/shield/insights?${qs(pid, p)}`),
  shieldStatus: (pid: string) => get<ShieldStatus>(`/api/v1/shield/status?${qs(pid)}`),
  replayStart: (body: { project_id: string; session_ids?: string[]; filter?: Record<string, unknown>; label?: string }) =>
    post<{ job_id: string; status: string }>('/api/v1/shield/replay', body),
  replayJobs: (pid: string, p: Params = {}) => get<List<ReplayJob>>(`/api/v1/shield/replay?${qs(pid, p)}`),
  replayJob: (pid: string, id: string) => get<ReplayJob>(`/api/v1/shield/replay/${seg(id)}?${qs(pid)}`),
  demoSession: (project_id: string) => post<{ session_id: string; project_id: string }>('/api/v1/demo/session', { project_id }),
}

/** The scorers /api/v1/scores/run knows at each scope (API.md "Scores"). */
export const SCORERS_BY_SCOPE: Record<'session' | 'agent' | 'handoff', { name: string; label: string; help: string }[]> = {
  session: [
    { name: 'GoalCompletionJudge', label: 'Goal completion', help: "An LLM judge: did the session achieve the user's goal? 0 to 1." },
    { name: 'ReasoningConsistencyJudge', label: 'Reasoning consistency', help: 'An LLM judge: did the actions follow from the stated reasoning? 0 to 1.' },
    { name: 'ToolSafetyScorer', label: 'Tool safety', help: 'Re-checks every tool call with the AgentShield rules: 1 when nothing would be blocked.' },
  ],
  agent: [
    { name: 'ReasoningConsistencyJudge', label: 'Reasoning consistency', help: "An LLM judge per agent: did the agent's actions follow from its reasoning?" },
    { name: 'ToolSafetyScorer', label: 'Tool safety', help: "Re-checks the agent's tool calls with the AgentShield rules." },
  ],
  handoff: [{ name: 'HandoffQualityJudge', label: 'Hand-off quality', help: 'An LLM judge per hand-off: was the delegation appropriate, and was the result used?' }],
}
