// Types mirror docs/weft/API.md. Keep the two in sync.

export type Json = null | boolean | number | string | Json[] | { [k: string]: Json }
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type Any = any

export type CallStatus = 'success' | 'error' | 'running' | 'descendant_error'

export interface UsageEntry {
  requests?: number
  prompt_tokens?: number
  completion_tokens?: number
  total_tokens?: number
  input_tokens?: number
  output_tokens?: number
  [k: string]: Any
}

export interface LLMCost {
  requests?: number
  prompt_tokens?: number
  completion_tokens?: number
  total_tokens?: number
  prompt_token_cost?: number
  completion_token_cost?: number
  /** All prompt tokens, cache reads and writes included; the call's cost is this plus completion_tokens_total_cost. */
  prompt_tokens_total_cost?: number
  completion_tokens_total_cost?: number
  cache_read_input_tokens?: number
  cache_creation_input_tokens?: number
  cache_read_input_token_cost?: number
  cache_creation_input_token_cost?: number
  /** Part of prompt_tokens_total_cost spent on cache reads (do not add it again). */
  cache_read_input_tokens_total_cost?: number
  /** Part of prompt_tokens_total_cost spent on cache writes (do not add it again). */
  cache_creation_input_tokens_total_cost?: number
  prompt_token_cost_unit?: string
  completion_token_cost_unit?: string
  effective_date?: string
  provider_id?: string
  pricing_level?: 'default' | 'project'
}

export interface CallSummary {
  usage?: Record<string, UsageEntry>
  status_counts?: { success?: number; error?: number }
  /** Computed on read. */
  weft?: {
    status: CallStatus
    latency_ms: number | null
    trace_name: string
    display_name: string | null
    costs?: Record<string, LLMCost>
  }
  [k: string]: Any
}

export interface Feedback {
  id: string
  project_id: string
  ref: string
  feedback_type: string
  payload: Any
  creator: string | null
  created_at: string
  wb_user_id: string | null
  annotation_ref?: string | null
  runnable_ref?: string | null
  call_ref?: string | null
  trigger_ref?: string | null
  /** Annotation queue the feedback was given in. */
  queue_id?: string | null
}

export interface Call {
  id: string
  project_id: string
  op_name: string
  display_name: string | null
  trace_id: string
  parent_id: string | null
  thread_id: string | null
  turn_id: string | null
  started_at: string
  ended_at: string | null
  exception: string | null
  attributes: Record<string, Any>
  inputs: Record<string, Any>
  output: Any
  summary: CallSummary
  wb_user_id: string | null
  wb_run_id: string | null
  deleted_at: string | null
  feedback?: Feedback[]
  storage_size_bytes?: number
}

export type Operand =
  | { $literal: Any }
  | { $getField: string }
  | { $convert: { input: Operand; to: 'double' | 'int' | 'string' | 'bool' | 'exists' } }
  | Operation

export type Operation =
  | { $and: Operation[] }
  | { $or: Operation[] }
  | { $not: [Operation] }
  | { $eq: [Operand, Operand] }
  | { $gt: [Operand, Operand] }
  | { $gte: [Operand, Operand] }
  | { $lt: [Operand, Operand] }
  | { $lte: [Operand, Operand] }
  | { $in: [Operand, Operand[]] }
  | { $contains: { input: Operand; substr: Operand; case_insensitive?: boolean } }

export interface Query {
  $expr: Operation
}

export interface SortBy {
  field: string
  direction: 'asc' | 'desc'
}

export interface CallsFilter {
  op_names?: string[]
  input_refs?: string[]
  output_refs?: string[]
  parent_ids?: string[]
  trace_ids?: string[]
  call_ids?: string[]
  trace_roots_only?: boolean
  wb_user_ids?: string[]
  wb_run_ids?: string[]
  thread_ids?: string[]
  turn_ids?: string[]
}

export interface CallsQueryReq {
  project_id: string
  filter?: CallsFilter
  query?: Query
  search?: string
  sort_by?: SortBy[]
  limit?: number
  offset?: number
  columns?: string[]
  expand_columns?: string[]
  include_costs?: boolean
  include_feedback?: boolean
  include_storage_size?: boolean
  include_deleted?: boolean
}

export interface CallsStatsBucket {
  start: string
  count: number
  error_count: number
  latency_ms_avg: number | null
  latency_ms_p50: number | null
  latency_ms_p95: number | null
  latency_ms_p99: number | null
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  cost_usd: number
}

export interface CallsStatsRes {
  bucket_seconds: number
  buckets: CallsStatsBucket[]
  totals: {
    count: number
    error_count: number
    total_tokens: number
    cost_usd: number
    latency_ms_p50: number | null
    latency_ms_p95: number | null
  }
}

export interface OpCount {
  op_name: string
  op_base_name: string
  count: number
  error_count: number
  last_called: string
}

export interface Thread {
  thread_id: string
  turn_count: number
  call_count: number
  start_time: string
  last_updated: string
  first_turn_id: string | null
  last_turn_id: string | null
  p50_turn_duration_ms: number | null
  p99_turn_duration_ms: number | null
  total_tokens: number
  cost_usd: number
}

export interface Project {
  project_id: string
  entity: string
  project: string
  call_count: number
  trace_count: number
  object_count: number
  last_activity: string | null
}

export interface ProjectStats {
  call_count: number
  trace_count: number
  object_count: number
  table_count: number
  file_count: number
  feedback_count: number
  storage_size_bytes: number
}

export interface ObjSchema {
  project_id: string
  object_id: string
  created_at: string
  deleted_at: string | null
  digest: string
  version_index: number
  is_latest: boolean
  kind: 'object' | 'op'
  base_object_class: string | null
  leaf_object_class: string | null
  val: Any
  wb_user_id: string | null
  aliases: string[]
  /** Version tags (/objs/{id}/versions/{digest}/tags). */
  tags?: string[]
  size_bytes: number
  version_count?: number
}

export interface ObjsFilter {
  base_object_classes?: string[]
  leaf_object_classes?: string[]
  object_ids?: string[]
  is_op?: boolean
  latest_only?: boolean
}

export interface TableRow {
  digest: string
  val: Any
  original_index: number
}

export type TableUpdate =
  | { append: { row: Any } }
  | { pop: { index: number } }
  | { insert: { index: number; row: Any } }
  | { replace: { index: number; row: Any } }

export interface CostEntry {
  id: string
  llm_id: string
  prompt_token_cost: number
  completion_token_cost: number
  /** USD per cache-read token; null bills cache reads at prompt_token_cost. */
  cache_read_input_token_cost: number | null
  /** USD per cache-write token; null bills cache writes at prompt_token_cost. */
  cache_creation_input_token_cost: number | null
  prompt_token_cost_unit: string
  completion_token_cost_unit: string
  effective_date: string
  provider_id: string
  pricing_level: 'default' | 'project'
}

export interface LLMModelInfo {
  id: string
  provider: string
  label: string
  available: boolean
  max_tokens: number
}

export interface ChatMessage {
  role: 'system' | 'user' | 'assistant' | 'tool' | string
  content: Any
  name?: string
  tool_calls?: Any[]
  tool_call_id?: string
}

export interface CompletionInputs {
  model: string
  messages: ChatMessage[]
  temperature?: number
  max_tokens?: number
  top_p?: number
  frequency_penalty?: number
  presence_penalty?: number
  stop?: string[]
  n?: number
  response_format?: Any
  tools?: Any[]
}

export interface ChatCompletion {
  id?: string
  model?: string
  choices?: { index: number; message: ChatMessage; finish_reason: string | null }[]
  usage?: { prompt_tokens: number; completion_tokens: number; total_tokens: number }
  error?: string
}

export interface EvalStatus {
  code: 'running' | 'complete' | 'failed' | 'unknown'
  completed_rows: number
  total_rows: number
  error?: string
}

export interface CompareEvaluation {
  call_id: string
  display_name: string | null
  evaluation_ref: string
  evaluation_name: string
  model_ref: string
  model_name: string
  started_at: string
  ended_at: string | null
  status: CallStatus
  summary: Any
  dataset_ref: string | null
  scorers: string[]
  total_tokens: number
  cost_usd: number
  row_count: number
}

export interface CompareMetric {
  key: string
  scorer: string
  path: string
  kind: 'fraction' | 'mean' | 'other'
  values: Record<string, number | boolean | null>
}

export interface CompareRowResult {
  call_id: string
  output: Any
  scores: Record<string, Any>
  model_latency: number | null
  total_tokens: number
  trial: number
}

export interface CompareRow {
  row_key: string
  inputs: Any
  results: Record<string, CompareRowResult[]>
}

export interface CompareEvaluationsRes {
  evaluations: CompareEvaluation[]
  metrics: CompareMetric[]
  rows: CompareRow[]
}

export interface LeaderboardColumn {
  index: number
  evaluation_object_ref: string
  evaluation_name: string
  scorer_name: string
  summary_metric_path: string
  should_minimize: boolean
}

export interface LeaderboardComputeRes {
  leaderboard: ObjSchema
  columns: LeaderboardColumn[]
  rows: {
    model_ref: string
    model_name: string
    values: Record<string, { value: number | boolean | null; evaluation_call_id: string; started_at: string }>
  }[]
}

export interface ServerInfo {
  version: string
  auth_required: boolean
  otel_endpoint: string
}
