import type {
  Any,
  ChatCompletion,
  CompareEvaluationsRes,
  CompletionInputs,
  EvalStatus,
  Feedback,
  LeaderboardComputeRes,
  ListRes,
  LLMModelInfo,
  ObjectKind,
  ObjectsFilter,
  PriceEntry,
  Project,
  ProjectStats,
  Query,
  SecretEntry,
  SecretSetReq,
  ServerInfo,
  SortBy,
  Span,
  SpanName,
  SpanSession,
  SpansFilter,
  SpansQueryReq,
  SpansStatsRes,
  TableRow,
  TableUpdate,
  WeftObject,
} from './types'

const API_BASE: string = (import.meta.env.VITE_WEFT_API as string | undefined) ?? ''
const KEY_STORAGE = 'weft.apiKey'

export function getApiKey(): string | null {
  try {
    return localStorage.getItem(KEY_STORAGE)
  } catch {
    return null
  }
}

export function setApiKey(key: string | null) {
  try {
    if (key) localStorage.setItem(KEY_STORAGE, key)
    else localStorage.removeItem(KEY_STORAGE)
  } catch {
    /* storage unavailable */
  }
}

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

function headers(json = true): Record<string, string> {
  const h: Record<string, string> = {}
  if (json) h['Content-Type'] = 'application/json'
  const key = getApiKey()
  if (key) h['Authorization'] = `Bearer ${key}`
  return h
}

async function raise(res: Response): Promise<never> {
  let msg = res.statusText
  try {
    const body = await res.json()
    msg = body.reason ?? body.detail ?? JSON.stringify(body)
  } catch {
    /* not JSON */
  }
  throw new ApiError(res.status, msg)
}

export async function post<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(API_BASE + path, { method: 'POST', headers: headers(), body: JSON.stringify(body) })
  if (!res.ok) await raise(res)
  return res.json() as Promise<T>
}

export async function get<T>(path: string): Promise<T> {
  const res = await fetch(API_BASE + path, { headers: headers(false) })
  if (!res.ok) await raise(res)
  return res.json() as Promise<T>
}

/** POST returning NDJSON; calls onItem for each parsed line. */
export async function postStream<T>(path: string, body: unknown, onItem: (item: T) => void, signal?: AbortSignal) {
  const res = await fetch(API_BASE + path, { method: 'POST', headers: headers(), body: JSON.stringify(body), signal })
  if (!res.ok) await raise(res)
  const reader = res.body!.getReader()
  const dec = new TextDecoder()
  let buf = ''
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buf += dec.decode(value, { stream: true })
    let nl: number
    while ((nl = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, nl).trim()
      buf = buf.slice(nl + 1)
      if (line) onItem(JSON.parse(line) as T)
    }
  }
  if (buf.trim()) onItem(JSON.parse(buf) as T)
}

const seg = encodeURIComponent
const pq = (projectId: string) => `project_id=${encodeURIComponent(projectId)}`

/** URL of a stored file's bytes (usable as <img src>: carries the API key when one is set). */
export function fileUrl(projectId: string, digest: string): string {
  const q = new URLSearchParams({ project_id: projectId })
  const key = getApiKey()
  if (key) q.set('api_key', key)
  return `${API_BASE}/api/v1/files/${seg(digest)}?${q.toString()}`
}

/** Fetches a stored file's bytes. */
export async function fileFetch(projectId: string, digest: string): Promise<Response> {
  const res = await fetch(`${API_BASE}/api/v1/files/${seg(digest)}?${pq(projectId)}`, { headers: headers(false) })
  if (!res.ok) await raise(res)
  return res
}

export type SpanScoreResult = { span_id: string; scorer: string; output: Any; scorer_span_id?: string; feedback_id?: string; error?: string }

export type PriceInput = {
  prompt_token_cost: number
  completion_token_cost: number
  cache_read_input_token_cost?: number
  cache_creation_input_token_cost?: number
  effective_date?: string
  provider_id?: string
}

// Weft's own API (docs/weft/API.md): every route is under /api/v1, lists
// return {items, total_count} and streams NDJSON.
export const api = {
  server: () => get<ServerInfo>('/api/v1/server'),
  authCheck: () => get<{ ok: boolean }>('/auth/check'),
  whoami: () => get<{ user_id: string; auth_required: boolean }>('/auth/whoami'),
  projects: () => get<{ projects: Project[] }>('/projects'),
  projectStats: (project_id: string) => get<ProjectStats>(`/api/v1/projects/stats?${pq(project_id)}`),

  // ---------------------------------------------------------------- spans
  spanGet: (
    project_id: string,
    id: string,
    opts: { include_costs?: boolean; include_feedback?: boolean; include_storage_size?: boolean } = {},
  ) => {
    const q = new URLSearchParams({ project_id })
    for (const [k, v] of Object.entries(opts)) if (v) q.set(k, 'true')
    return get<Span>(`/api/v1/spans/${seg(id)}?${q.toString()}`)
  },
  /** One span, or null when there is none (404). */
  spanFind: async (project_id: string, id: string, opts: { include_costs?: boolean; include_feedback?: boolean; include_storage_size?: boolean } = {}) => {
    try {
      return await api.spanGet(project_id, id, opts)
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return null
      throw e
    }
  },
  spansQuery: (req: SpansQueryReq) => post<ListRes<Span>>('/api/v1/spans/query', req),
  spansStream: (req: SpansQueryReq, onItem: (s: Span) => void, signal?: AbortSignal) => postStream<Span>('/api/v1/spans/stream', req, onItem, signal),
  /** The number of matching spans (a query with limit 0). */
  spansCount: (req: { project_id: string; filter?: SpansFilter; query?: Query; search?: string }) =>
    post<ListRes<Span>>('/api/v1/spans/query', { ...req, limit: 0 }).then((r) => r.total_count),
  spansDelete: (project_id: string, ids: string[]) => post<object>('/api/v1/spans/delete', { project_id, ids }),
  spanUpdate: (project_id: string, id: string, display_name: string) => post<object>('/api/v1/spans/update', { project_id, id, display_name }),
  spansStats: (req: { project_id: string; filter?: SpansFilter; query?: Query; search?: string; start?: string; end?: string; bucket_seconds?: number }) =>
    post<SpansStatsRes>('/api/v1/spans/stats', req),
  spansScore: (project_id: string, ids: string[], scorer_refs: string[]) =>
    post<{ items: SpanScoreResult[] }>('/api/v1/spans/score', { project_id, ids, scorer_refs }),
  spanNames: (project_id: string, trace_roots_only = false) => post<ListRes<SpanName>>('/api/v1/spans/names', { project_id, trace_roots_only }),
  spanSessions: (req: {
    project_id: string
    limit?: number
    offset?: number
    sort_by?: SortBy[]
    filter?: { started_after?: string; started_before?: string; session_ids?: string[] }
  }) => post<ListRes<SpanSession>>('/api/v1/spans/sessions', req),

  // ---------------------------------------------------------------- objects
  objectSave: (project_id: string, name: string, value: Any, cls?: string) =>
    post<{ name: string; digest: string; version: number; kind: ObjectKind; ref: string }>('/api/v1/objects/save', {
      project_id,
      name,
      value,
      ...(cls ? { class: cls } : {}),
    }),
  /** One version: a digest, `v<N>`, `latest` or an alias. */
  objectGet: (project_id: string, name: string, version = 'latest') =>
    get<WeftObject>(
      version === 'latest' ? `/api/v1/objects/${seg(name)}?${pq(project_id)}` : `/api/v1/objects/${seg(name)}/versions/${seg(version)}?${pq(project_id)}`,
    ),
  objectsQuery: (req: {
    project_id: string
    filter?: ObjectsFilter
    limit?: number
    offset?: number
    sort_by?: SortBy[]
    metadata_only?: boolean
    include_deleted?: boolean
  }) => post<ListRes<WeftObject>>('/api/v1/objects/query', req),
  objectDelete: (project_id: string, name: string, digests?: string[]) =>
    post<{ num_deleted: number }>('/api/v1/objects/delete', { project_id, name, digests }),
  objectAliasSet: (project_id: string, name: string, version: string, alias: string) =>
    post<object>(`/api/v1/objects/${seg(name)}/aliases`, { project_id, version, aliases: [alias] }),
  objectAliasRemove: (project_id: string, name: string, alias: string) =>
    post<object>(`/api/v1/objects/${seg(name)}/aliases/remove`, { project_id, aliases: [alias] }),
  objectTagsAdd: (project_id: string, name: string, version: string, tags: string[]) =>
    post<object>(`/api/v1/objects/${seg(name)}/versions/${seg(version)}/tags`, { project_id, tags }),
  objectTagsRemove: (project_id: string, name: string, version: string, tags: string[]) =>
    post<object>(`/api/v1/objects/${seg(name)}/versions/${seg(version)}/tags/remove`, { project_id, tags }),
  /** Distinct tags on live versions. */
  objectTags: (project_id: string) => post<ListRes<string>>('/api/v1/objects/tags', { project_id }),
  /** Distinct aliases (`latest` is not listed). */
  objectAliases: (project_id: string) => post<ListRes<string>>('/api/v1/objects/aliases', { project_id }),

  // ---------------------------------------------------------------- tables
  tableCreate: (project_id: string, rows: Any[]) =>
    post<{ digest: string; row_digests: string[]; ref: string }>('/api/v1/tables/create', { project_id, rows }),
  tableUpdate: (project_id: string, digest: string, updates: TableUpdate[]) =>
    post<{ digest: string; updated_row_digests: string[]; ref: string }>('/api/v1/tables/update', { project_id, digest, updates }),
  tableQuery: (req: { project_id: string; digest: string; filter?: { row_digests?: string[] }; limit?: number; offset?: number; sort_by?: SortBy[] }) =>
    post<ListRes<TableRow>>('/api/v1/tables/query', req),
  /** Row count of one table. */
  tableCount: (project_id: string, digest: string) =>
    post<ListRes<{ digest: string; count: number }>>('/api/v1/tables/stats', { project_id, digest }).then((r) => r.items[0]?.count ?? 0),

  // ---------------------------------------------------------------- refs & files
  /** Values aligned with refs (null for unknown refs). */
  refsRead: (refs: string[]) => post<{ items: Any[] }>('/api/v1/refs/read', { refs }),
  fileUpload: async (project_id: string, file: Blob, name = 'file') => {
    const fd = new FormData()
    fd.set('project_id', project_id)
    fd.set('file', file, name)
    const res = await fetch(API_BASE + '/api/v1/files/upload', { method: 'POST', headers: headers(false), body: fd })
    if (!res.ok) await raise(res)
    return (await res.json()) as { digest: string; size_bytes: number }
  },
  fileText: async (project_id: string, digest: string) => (await fileFetch(project_id, digest)).text(),

  // ---------------------------------------------------------------- feedback
  feedbackCreate: (req: {
    project_id: string
    ref: string
    feedback_type: string
    payload: Any
    creator?: string
    annotation_ref?: string
    runnable_ref?: string
    span_ref?: string
    trigger_ref?: string
  }) => post<{ id: string; created_at: string; user_id: string; payload: Any }>('/api/v1/feedback/create', req),
  feedbackQuery: (req: { project_id: string; query?: Query; sort_by?: SortBy[]; limit?: number; offset?: number }) =>
    post<ListRes<Feedback>>('/api/v1/feedback/query', req),
  feedbackDelete: (project_id: string, id: string) => post<object>('/api/v1/feedback/delete', { project_id, ids: [id] }),
  feedbackReplace: (req: { feedback_id: string; project_id: string; ref: string; feedback_type: string; payload: Any; annotation_ref?: string }) =>
    post<{ id: string }>('/api/v1/feedback/replace', req),

  // ---------------------------------------------------------------- prices
  pricesCreate: (project_id: string, prices: Record<string, PriceInput>) =>
    post<{ items: { id: string; llm_id: string }[] }>('/api/v1/prices/create', { project_id, prices }),
  pricesQuery: (project_id: string, include_defaults = false) =>
    post<ListRes<PriceEntry>>('/api/v1/prices/query', { project_id, include_defaults, limit: 10000 }),
  pricesDelete: (project_id: string, ids: string[]) => post<object>('/api/v1/prices/delete', { project_id, ids }),

  // ---------------------------------------------------------------- llm
  llmModels: () => get<{ models: LLMModelInfo[] }>('/llm/models'),
  completions: (project_id: string, inputs: CompletionInputs, track = true) =>
    post<{ response: ChatCompletion; span_id?: string | null }>('/api/v1/completions', { project_id, inputs, track }),
  completionsStream: (project_id: string, inputs: CompletionInputs, onChunk: (chunk: Any) => void, signal?: AbortSignal) =>
    postStream<Any>('/api/v1/completions/stream', { project_id, inputs, track: true }, onChunk, signal),
  /** Secrets the project can use (members), or every secret (server admins, without a project). */
  secretsList: (projectId?: string) =>
    get<{ secrets: SecretEntry[] }>(projectId ? `/api/v1/secrets/list?project_id=${encodeURIComponent(projectId)}` : '/secrets/list'),
  secretsSet: (req: SecretSetReq) => post<object>('/api/v1/secrets/set', req),
  secretsDelete: (req: Omit<SecretSetReq, 'value' | 'allowed_hosts' | 'kind'>) => post<object>('/api/v1/secrets/delete', req),

  // ---------------------------------------------------------------- evaluations
  /** Starts a server-side evaluation run; returns the run's span id. */
  evaluationRun: (project_id: string, evaluation_ref: string, model_ref: string) =>
    post<{ id: string }>('/api/v1/evaluations/run', { project_id, evaluation_ref, model_ref }),
  evaluationStatus: (project_id: string, id: string) => get<EvalStatus>(`/api/v1/evaluations/${seg(id)}/status?${pq(project_id)}`),
  evaluationsCompare: (project_id: string, ids: string[]) => post<CompareEvaluationsRes>('/api/v1/evaluations/compare', { project_id, ids }),
  leaderboardCompute: (project_id: string, name: string, version?: string) =>
    post<LeaderboardComputeRes>('/api/v1/leaderboards/compute', { project_id, name, ...(version ? { version } : {}) }),
}

export type Api = typeof api
