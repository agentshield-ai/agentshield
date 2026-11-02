import { post } from './client'
import type { Any, Query, SortBy } from './types'

// Weave's Agents API (/agents/*). Shapes follow the weave server SDK, plus
// the Weft extensions documented in docs/weft/API.md.

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

export interface SpansQueryReq {
  project_id: string
  query?: Query
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

export interface SpanStatsReq {
  project_id: string
  start: string
  end?: string
  granularity?: number
  query?: Query
  group_by?: ValueRef[]
  group_limit?: number
  metrics?: StatsMetric[]
  timezone?: string
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
  weave_ref: string
  creator: string | null
  created_at: string | null
  scorer_ratings: Record<string, number>
  scorer_tags: string[]
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
  wb_user_id: string | null
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

export const agentsApi = {
  query: (body: { project_id: string; filters?: { agent_name?: string }; include_costs?: boolean; limit?: number; offset?: number; sort_by?: SortBy[] }) =>
    post<{ agents: AgentStats[]; total_count: number }>('/agents/query', body),
  versions: (body: { project_id: string; agent_name: string; include_costs?: boolean; sort_by?: SortBy[] }) =>
    post<{ versions: AgentStats[]; total_count: number }>('/agents/agent-versions/query', body),
  spans: (body: SpansQueryReq) => post<SpansQueryRes>('/agents/spans/query', body),
  stats: (body: SpanStatsReq) => post<SpanStatsRes>('/agents/spans/stats', body),
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
export const conversationUrl = (projectId: string, id: string, turn?: number) =>
  `/${projectId}/conversations/${encodeURIComponent(id)}${turn != null ? `#turn-${turn}` : ''}`
