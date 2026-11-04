import { get, post } from './client'
import type { Any, Query, SortBy } from './types'

// The Agents API (/agents/*), including signals, insights and agent evals.
// Shapes are documented in docs/weft/API.md.

export type StatusCode = 'UNSET' | 'OK' | 'ERROR'

export interface AgentStats {
  agent_name: string
  agent_version?: string
  project_id: string
  error_count: number
  first_seen: string | null
  last_seen: string | null
  invocation_count: number
  span_count: number
  total_cost_usd: number | null
  total_duration_ms: number
  total_input_tokens: number
  total_output_tokens: number
  // Weft extensions
  version_count: number
  versions: string[] | null
  conversation_count: number
  invocation_error_count: number
  latency_ms_p50: number | null
  latency_ms_p95: number | null
  llm_call_count: number
  tool_call_count: number
  models: string[] | null
}

export interface ValueRef {
  key: string
  source?: 'field' | 'column' | 'derived' | 'custom_attrs_string' | 'custom_attrs_int' | 'custom_attrs_float' | 'custom_attrs_bool'
  alias?: string
}

export interface MeasureSpec {
  alias: string
  aggregation: 'sum' | 'avg' | 'min' | 'max' | 'count' | 'count_distinct' | 'count_true' | 'count_false'
  value?: ValueRef
  filter?: Query
}

export interface SpanMessage {
  role: string
  content: string
  finish_reason: string
}

export interface AgentSpan {
  span_id: string
  trace_id: string
  parent_span_id: string | null
  /** The op (call id / trace id) the span was emitted from, when it started its own trace inside one (weft.parent_call.*). */
  parent_call_id?: string | null
  parent_call_trace_id?: string | null
  operation_name: string | null
  span_name: string | null
  display_name: string
  agent_name: string | null
  agent_version: string | null
  conversation_id: string | null
  conversation_name: string | null
  tool_name: string | null
  tool_call_id: string | null
  tool_call_arguments: string | null
  tool_call_result: string | null
  request_model: string | null
  provider_name: string | null
  started_at: string | null
  ended_at: string | null
  duration_ms: number | null
  status_code: StatusCode
  status_message: string | null
  input_tokens: number | null
  output_tokens: number | null
  total_cost_usd: number | null
  input_messages: SpanMessage[]
  output_messages: SpanMessage[]
  custom_attrs_string: Record<string, string>
  custom_attrs_int: Record<string, number>
  custom_attrs_float: Record<string, number>
  custom_attrs_bool: Record<string, boolean>
  is_turn: boolean
  error_type?: string | null
  finish_reasons?: string[] | null
  span_kind?: string | null
  reasoning_tokens?: number | null
  cache_read_input_tokens?: number | null
  cache_creation_input_tokens?: number | null
  response_model?: string | null
}

export interface MessagePreview {
  role: 'user_message' | 'assistant_message'
  text: string
}

export interface SpanGroup {
  group_keys: Record<string, string | number | boolean | null>
  agent_names: string[]
  agent_versions: string[]
  conversation_count: number
  conversation_names: string[]
  error_count: number
  first_message: MessagePreview | null
  last_message: MessagePreview | null
  first_seen: string | null
  last_seen: string | null
  invocation_count: number
  /** Weft extension: turn spans in the group, including SDK turns without an agent invocation. */
  turn_count?: number
  metrics: Record<string, Any>
  provider_names: string[]
  request_models: string[]
  span_count: number
  total_cost_usd: number | null
  total_duration_ms: number
  total_input_tokens: number
  total_output_tokens: number
  total_reasoning_tokens: number
  latency_ms_p50: number | null
  latency_ms_p95: number | null
  trace_count: number
}

export interface SignalRatingFilter {
  op: 'gte' | 'gt' | 'lte' | 'lt' | 'eq'
  scorer_key: string
  value: number
}

export interface SignalFilters {
  ratings?: SignalRatingFilter[]
  tags?: string[]
}

export type InsightField = 'intent_category' | 'intent_sentiment' | 'failure_category' | 'failure_severity' | 'intent_topic_id' | 'failure_topic_id'

export interface InsightFilter {
  field: InsightField
  values: string[]
  exclude?: boolean
}

/** Weft extension: keep the spans of an evaluation run (or one of its predictions). */
export interface EvalSpanFilter {
  evaluation_run_id?: string
  /** The prediction's predict_and_score span. */
  prediction_span_id?: string
}

export interface SpansQueryReq extends EvalSpanFilter {
  project_id: string
  query?: Query
  signal_filters?: SignalFilters
  insight_filters?: InsightFilter[]
  group_by?: ValueRef[]
  measures?: MeasureSpec[]
  sort_by?: SortBy[]
  limit?: number
  offset?: number
  include_costs?: boolean
  include_details?: boolean
  started_after?: string
  started_before?: string
}

export interface SpansQueryRes {
  spans: AgentSpan[]
  groups: SpanGroup[]
  total_count: number
}

export interface StatsMetric {
  alias: string
  value: ValueRef
  value_type: 'number' | 'boolean' | 'string' | 'datetime'
  aggregations?: MeasureSpec['aggregation'][]
  percentiles?: number[]
}

export interface SpanStatsReq extends EvalSpanFilter {
  project_id: string
  start: string
  end?: string
  granularity?: number
  query?: Query
  group_by?: ValueRef[]
  group_limit?: number
  metrics?: StatsMetric[]
  timezone?: string
  signal_filters?: SignalFilters
  insight_filters?: InsightFilter[]
}

export interface SpanStatsRes {
  bucket_type: 'time' | 'number'
  granularity: number | null
  start: string
  end: string
  timezone: string
  columns: { name: string; role: 'time' | 'bucket' | 'group' | 'metric'; value_type: string; metric: string | null; aggregation: string | null }[]
  rows: Record<string, Any>[]
}

export interface ChatFeedback {
  id: string
  feedback_type: string
  payload: Record<string, Any>
  ref: string
  creator: string | null
  created_at: string | null
  scorer_ratings: Record<string, number>
  scorer_tags: string[]
  scorer_rating_confidences?: Record<string, number>
  scorer_rating_reasons?: Record<string, string>
  scorer_tag_confidences?: Record<string, number>
  scorer_tag_reasons?: Record<string, string>
}

export interface ChatMessage {
  type: 'user_message' | 'assistant_message' | 'tool_call' | 'agent_handoff' | 'agent_start' | 'context_compacted'
  span_id: string | null
  started_at: string | null
  status_code: StatusCode | null
  status_message: string | null
  error_type: string | null
  agent_name: string | null
  agent_version: string | null
  feedback: ChatFeedback[] | null
  operation_name?: string
  span_name?: string
  media?: Any[]
  user_message: { text: string; content_refs: string[] } | null
  assistant_message: {
    text: string
    model: string | null
    duration_ms: number | null
    input_tokens: number | null
    output_tokens: number | null
    reasoning_tokens: number | null
    reasoning_content: string | null
    total_cost_usd: number | null
    status: StatusCode | null
    requested_tools?: string[]
  } | null
  tool_call: {
    tool_name: string | null
    tool_arguments: string | null
    tool_result: string | null
    duration_ms: number | null
    status: StatusCode | null
    tool_call_id?: string | null
  } | null
  agent_start: { model: string | null; status: StatusCode | null; system_instructions: string | null; tool_definitions: string | null } | null
}

export interface TraceChat {
  trace_id: string
  root_span_id: string
  root_span_name: string | null
  agent_name: string | null
  agent_version: string | null
  provider: string | null
  started_at: string | null
  ended_at: string | null
  status_code: StatusCode | null
  total_duration_ms: number | null
  total_input_tokens: number
  total_output_tokens: number
  total_reasoning_tokens: number
  total_cost_usd: number | null
  span_count: number
  llm_call_count: number
  tool_call_count: number
  user_id: string | null
  feedback: ChatFeedback[] | null
  messages: ChatMessage[]
}

export interface ConversationChat {
  conversation_id: string
  conversation_name: string | null
  agent_names: string[]
  started_at: string | null
  last_activity: string | null
  total_turns: number
  total_cost_usd: number | null
  total_input_tokens: number
  total_output_tokens: number
  span_count: number
  error_count: number
  has_more: boolean
  limit: number
  offset: number
  turns: TraceChat[]
}

export interface ConversationSpan {
  span_id: string
  trace_id: string
  operation_name: string
  status: StatusCode
  duration_ms: number
  span_name: string
  started_at: string
}

export interface ConversationSpans {
  conversation_id: string
  spans: ConversationSpan[]
  spans_feedback: { feedback_type: string; ratings: { name: string; value: number }[]; tags: string[]; trace_id: string | null }[]
}

export interface SearchResult {
  agent_name: string
  conversation_id: string
  conversation_name: string
  last_activity: string
  matched_messages: { content_digest: string; content_preview: string; role: string; span_id: string; started_at: string; trace_id: string }[]
}

// ---------------------------------------------------------------- agent evals (Weft extensions)

/** A prediction (its predict_and_score span) of an evaluation run with agent spans. */
export interface AgentEvalPrediction {
  prediction_span_id: string
  status: string
  example: Any
  output: Any
  scores: Any
  model_latency: number | null
  kind?: string
  row_digest?: string
  example_id?: string
  trial_index?: number | null
  conversation_ids: string[]
  trace_ids: string[]
  span_count: number
}

/** An evaluation run with the agent activity linked to it. */
export interface AgentEvalRun {
  evaluation_run_id: string
  display_name: string | null
  evaluation_ref: string | null
  evaluation_name: string
  model_ref: string | null
  model_name: string
  started_at: string | null
  ended_at: string | null
  status: string
  summary: Any
  prediction_count: number
  span_count: number
  error_count: number
  turn_count: number
  trace_count: number
  conversation_count: number
  conversation_ids: string[]
  agent_names: string[]
  first_seen: string | null
  last_seen: string | null
  total_input_tokens: number
  total_output_tokens: number
  total_cost_usd: number | null
  predictions?: AgentEvalPrediction[]
}

export interface AgentEvalsQueryReq {
  project_id: string
  agent_name?: string
  conversation_id?: string
  trace_ids?: string[]
  evaluation_run_ids?: string[]
  include_costs?: boolean
  include_predictions?: boolean
  limit?: number
  offset?: number
}

export type AgentEvalConversation = SpanGroup & { conversation_id: string; predictions: AgentEvalPrediction[] }

export interface AgentEvalConversationsRes {
  conversations: AgentEvalConversation[]
  total_count: number
  span_count: number
  other_span_count: number
}

export const agentEvalsApi = {
  query: (body: AgentEvalsQueryReq) => post<{ evaluations: AgentEvalRun[]; total_count: number }>('/agents/evals/query', body),
  conversations: (body: EvalSpanFilter & { project_id: string; agent_name?: string; include_costs?: boolean; limit?: number; offset?: number; sort_by?: SortBy[] }) =>
    post<AgentEvalConversationsRes>('/agents/evals/conversations', body),
}

// ---------------------------------------------------------------- signals (Weft extensions)

export type SignalKind = 'tag' | 'rating'

export interface SignalPreset {
  id: string
  name: string
  kind: SignalKind
  description: string
}

/** A signal to run: a preset id, or a custom definition. */
export interface SignalSpec {
  id?: string
  preset?: string
  name?: string
  kind?: SignalKind
  description?: string
}

export interface SignalRunReq {
  project_id: string
  signals: SignalSpec[]
  model: string
  agent_name?: string
  conversation_id?: string
  trace_ids?: string[]
  started_after?: string
  started_before?: string
  limit?: number
  rescore?: boolean
}

export interface SignalRunRes {
  turns: number
  scored: number
  skipped: number
  failed: number
  results: { span_id: string; trace_id: string; signal: string; kind: SignalKind; score?: number; matched?: boolean; confidence?: number; reason?: string; feedback_id?: string; error?: string }[]
}

export interface SignalResult {
  feedback_id: string
  feedback_type: string
  kind: SignalKind
  signal: string
  signal_id: string | null
  signal_name: string | null
  score: number | null
  tags: string[]
  confidence: number | null
  reason: string | null
  creator: string | null
  created_at: string
  span_id: string
  trace_id: string
  conversation_id: string | null
  conversation_name: string | null
  agent_name: string | null
  agent_version: string | null
  turn_started_at: string | null
  turn_status: StatusCode
  last_message: MessagePreview | null
  scorer_span_id: string | null
}

export interface SignalSummary {
  signal: string
  signal_name: string | null
  kind: SignalKind
  count: number
  matched: number
  avg: number | null
  tag_counts?: Record<string, number>
}

export interface SignalsQueryReq {
  project_id: string
  agent_name?: string
  conversation_id?: string
  trace_ids?: string[]
  signal?: string
  kind?: SignalKind
  tag?: string
  min_score?: number
  max_score?: number
  matched_only?: boolean
  started_after?: string
  started_before?: string
  limit?: number
  offset?: number
}

export interface SignalsQueryRes {
  results: SignalResult[]
  summary: SignalSummary[]
  total_count: number
}

/** One item of the feedback batch schema (scorer and span fields included). */
export interface AgentFeedbackItem {
  project_id: string
  ref: string
  feedback_type: string
  payload: Record<string, Any>
  scorer_ratings?: Record<string, number>
  scorer_rating_confidences?: Record<string, number>
  scorer_rating_reasons?: Record<string, string>
  scorer_tags?: string[]
  scorer_tag_confidences?: Record<string, number>
  scorer_tag_reasons?: Record<string, string>
}

export const signalsApi = {
  presets: () => get<{ presets: SignalPreset[] }>('/agents/signals/presets'),
  run: (body: SignalRunReq) => post<SignalRunRes>('/agents/signals/run', body),
  query: (body: SignalsQueryReq) => post<SignalsQueryRes>('/agents/signals/query', body),
  feedback: (batch: AgentFeedbackItem[]) => post<{ res: { id: string }[] }>('/agents/feedback/batch/create', { batch }),
}

export const agentsApi = {
  query: (body: { project_id: string; filters?: { agent_name?: string }; include_costs?: boolean; limit?: number; offset?: number; sort_by?: SortBy[] }) =>
    post<{ agents: AgentStats[]; total_count: number }>('/agents/query', body),
  versions: (body: { project_id: string; agent_name: string; include_costs?: boolean; sort_by?: SortBy[] }) =>
    post<{ versions: AgentStats[]; total_count: number }>('/agents/agent-versions/query', body),
  spans: (body: SpansQueryReq) => post<SpansQueryRes>('/agents/spans/query', body),
  stats: (body: SpanStatsReq) => post<SpanStatsRes>('/agents/spans/stats', body),
  customAttrs: (body: { project_id: string; started_after?: string; limit?: number }) =>
    post<{ attributes: { key: string; source: NonNullable<ValueRef['source']>; value_type: string; span_count: number }[]; has_more: boolean }>(
      '/agents/spans/custom-attrs/schema',
      body,
    ),
  conversationChat: (body: { project_id: string; conversation_id: string; include_feedback?: boolean; include_model_tool_calls?: boolean; limit?: number; offset?: number }) =>
    post<ConversationChat>('/agents/conversations/chat', body),
  conversationSpans: (body: { project_id: string; conversation_ids: string[] }) =>
    post<{ conversations: ConversationSpans[] }>('/agents/conversations/spans', body),
  traceChat: (body: { project_id: string; trace_id: string; include_feedback?: boolean; include_model_tool_calls?: boolean }) =>
    post<TraceChat>('/agents/traces/chat', body),
  search: (body: { project_id: string; query: string; agent_name?: string; limit?: number; offset?: number; roles?: string[] }) =>
    post<{ results: SearchResult[]; total_conversations: number }>('/agents/search', body),
}

/** {$expr: {$and: [$eq field literal...]}} for simple equality filters. */
export function eqQuery(fields: Record<string, string | boolean | number | null | undefined>): Query | undefined {
  const parts = Object.entries(fields)
    .filter(([, v]) => v !== undefined && v !== null && v !== '')
    .map(([k, v]) => ({ $eq: [{ $getField: k }, { $literal: v }] }))
  if (!parts.length) return undefined
  return { $expr: parts.length === 1 ? parts[0] : { $and: parts } } as Query
}

export const agentUrl = (projectId: string, name: string) => `/${projectId}/agents/${encodeURIComponent(name)}`
export const agentsUrl = (projectId: string) => `/${projectId}/agents`
/**
 * The Agents Spans tab filtered to an evaluation run (or one prediction), over
 * all time; `conversations` groups the spans by conversation. With `agent`,
 * the agent's own Spans tab.
 */
export function evalSpansUrl(projectId: string, runId: string, opts: { pred?: string; conversations?: boolean; agent?: string } = {}): string {
  const p = new URLSearchParams({ tab: 'spans', eval: runId, range: 'all' })
  if (opts.pred) p.set('pred', opts.pred)
  if (opts.conversations) p.set('group', 'conversation_id')
  return `${opts.agent ? agentUrl(projectId, opts.agent) : agentsUrl(projectId)}?${p}`
}
export const conversationUrl = (projectId: string, id: string, turn?: number) =>
  `/${projectId}/conversations/${encodeURIComponent(id)}${turn != null ? `#turn-${turn}` : ''}`
