// Types mirror docs/weft/API.md. Keep the two in sync.

export type Json = null | boolean | number | string | Json[] | { [k: string]: Json }
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type Any = any

export type SpanStatus = 'success' | 'error' | 'running' | 'descendant_error'

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
  /** All prompt tokens, cache reads and writes included; the span's cost is this plus completion_tokens_total_cost. */
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

export interface SpanSummary {
  usage?: Record<string, UsageEntry>
  status_counts?: { success?: number; error?: number }
  /** Computed on read. */
  weft?: {
    status: SpanStatus
    latency_ms: number | null
    trace_name: string
    display_name: string | null
    costs?: Record<string, LLMCost>
    /** The status the shared status filter reads (only with include_run_status). */
    run_status?: 'ok' | 'error' | 'failed_step' | 'running' | 'unknown'
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
  user_id: string | null
  annotation_ref?: string | null
  runnable_ref?: string | null
  /** The scorer's own span, for scorer results. */
  span_ref?: string | null
  trigger_ref?: string | null
  /** Annotation queue the feedback was given in. */
  queue_id?: string | null
}

/** One traced function or model call (docs/weft/API.md "Spans"). */
export interface Span {
  id: string
  project_id: string
  /** A function ref, or a plain span name (OTLP). */
  name: string
  display_name: string | null
  trace_id: string
  parent_id: string | null
  /** The conversation the span belongs to. */
  session_id: string | null
  turn_id: string | null
  started_at: string
  ended_at: string | null
  /** The error text when the span failed. */
  error: string | null
  attributes: Record<string, Any>
  inputs: Record<string, Any>
  output: Any
  summary: SpanSummary
  user_id: string | null
  run_id: string | null
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

export interface SpansFilter {
  /** Span names: a function ref, a ref with a :* wildcard, or a base name. */
  names?: string[]
  input_refs?: string[]
  output_refs?: string[]
  parent_ids?: string[]
  trace_ids?: string[]
  ids?: string[]
  trace_roots_only?: boolean
  user_ids?: string[]
  run_ids?: string[]
  session_ids?: string[]
  turn_ids?: string[]
}

export interface SpansQueryReq {
  project_id: string
  filter?: SpansFilter
  query?: Query
  search?: string
  sort_by?: SortBy[]
  /** Default 100, at most 10,000; 0 returns only total_count. */
  limit?: number
  offset?: number
  columns?: string[]
  expand_columns?: string[]
  include_costs?: boolean
  include_feedback?: boolean
  include_storage_size?: boolean
  include_deleted?: boolean
  /** Adds summary.weft.run_status to every span. */
  include_run_status?: boolean
}

/** A native list response. */
export interface ListRes<T> {
  items: T[]
  total_count: number
}

export interface SpansStatsBucket {
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

export interface SpansStatsRes {
  bucket_seconds: number
  buckets: SpansStatsBucket[]
  totals: {
    count: number
    error_count: number
    total_tokens: number
    cost_usd: number
    latency_ms_p50: number | null
    latency_ms_p95: number | null
  }
}

/** Span counts by name (/api/v1/spans/names). */
export interface SpanName {
  name: string
  base_name: string
  count: number
  error_count: number
  last_started_at: string
}

/** Turn statistics of one session id (/api/v1/spans/sessions). */
export interface SpanSession {
  session_id: string
  turn_count: number
  span_count: number
  started_at: string
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
  span_count: number
  trace_count: number
  object_count: number
  last_activity: string | null
}

export interface ProjectStats {
  span_count: number
  trace_count: number
  object_count: number
  table_count: number
  file_count: number
  feedback_count: number
  storage_size_bytes: number
}

/** What an object is: function for a versioned function, else the kind of its base class. */
export type ObjectKind =
  | 'function'
  | 'dataset'
  | 'model'
  | 'evaluation'
  | 'scorer'
  | 'prompt'
  | 'leaderboard'
  | 'monitor'
  | 'annotation_spec'
  | 'saved_view'
  | 'provider'
  | 'provider_model'
  | 'dashboard'
  | 'automation'
  | 'object'

/** One version of a versioned value (docs/weft/API.md "Objects"). */
export interface WeftObject {
  project_id: string
  name: string
  digest: string
  version: number
  kind: ObjectKind
  /** The most specific class Weft knows the value by; null for functions and plain values. */
  class: string | null
  /** Omitted with metadata_only. */
  value: Any
  ref: string
  is_latest: boolean
  aliases: string[]
  tags: string[]
  user_id: string | null
  created_at: string
  deleted_at: string | null
  size_bytes: number
  /** Only with filter.latest_only. */
  version_count?: number
}

export interface ObjectsFilter {
  names?: string[]
  kinds?: ObjectKind[]
  classes?: string[]
  latest_only?: boolean
}

export interface TableRow {
  digest: string
  index: number
  value: Any
}

export type TableUpdate =
  | { append: { row: Any } }
  | { pop: { index: number } }
  | { insert: { index: number; row: Any } }
  | { replace: { index: number; row: Any } }

export interface PriceEntry {
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

/** Where a stored secret applies (docs/weft/API.md, "Secrets"). */
export type SecretScope = 'server' | 'team' | 'project'

/** A stored or environment secret, never its value (GET /api/v1/secrets/list). */
export interface SecretEntry {
  name: string
  source: 'env' | 'db'
  scope: SecretScope
  /** The team for team secrets, the project for project secrets, "" for server secrets. */
  scope_id: string
  kind: 'secret' | 'bucket'
  /** Hosts the secret may be sent to ("host", "host:port", "*.domain"); empty: any. */
  allowed_hosts: string[]
  created_by?: string | null
  updated_at?: string
}

/** POST /api/v1/secrets/set (and /delete without value and allowed_hosts). */
export interface SecretSetReq {
  name: string
  value?: string
  scope: SecretScope
  project_id?: string
  team?: string
  allowed_hosts?: string[]
  kind?: 'secret' | 'bucket'
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
  /** The run's Evaluation.evaluate span. */
  id: string
  display_name: string | null
  evaluation_ref: string
  evaluation_name: string
  model_ref: string
  model_name: string
  started_at: string
  ended_at: string | null
  status: SpanStatus
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
  /** The predict_and_score span. */
  span_id: string
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
  leaderboard: WeftObject
  columns: LeaderboardColumn[]
  rows: {
    model_ref: string
    model_name: string
    values: Record<string, { value: number | boolean | null; evaluation_run_id: string; started_at: string }>
  }[]
}

export interface ServerInfo {
  name: string
  version: string
  api_version: string
  auth_required: boolean
  otel_endpoint: string
  features: string[]
  limits: Record<string, number>
}
