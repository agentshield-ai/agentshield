import type {
  Any,
  Call,
  CallsQueryReq,
  CallsStatsRes,
  ChatCompletion,
  CompareEvaluationsRes,
  CompletionInputs,
  CostEntry,
  EvalStatus,
  Feedback,
  LeaderboardComputeRes,
  LLMModelInfo,
  ObjSchema,
  ObjsFilter,
  OpCount,
  Project,
  ProjectStats,
  Query,
  ServerInfo,
  SortBy,
  TableRow,
  TableUpdate,
  Thread,
  CallsFilter,
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

/** POST returning JSONL; calls onItem for each parsed line. */
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

export function fileUrl(projectId: string, digest: string): string {
  const q = new URLSearchParams({ project_id: projectId, digest })
  const key = getApiKey()
  if (key) q.set('api_key', key)
  return `${API_BASE}/file/content?${q.toString()}`
}

// ---------------------------------------------------------------- server
export const api = {
  serverInfo: () => get<ServerInfo>('/server_info'),
  authCheck: () => get<{ ok: boolean }>('/auth/check'),
  whoami: () => get<{ user_id: string; auth_required: boolean }>('/auth/whoami'),
  projects: () => get<{ projects: Project[] }>('/projects'),
  projectStats: (project_id: string) => post<ProjectStats>('/project/stats', { project_id }),

  // ---------------------------------------------------------------- calls
  callRead: (project_id: string, id: string, opts: { include_costs?: boolean; include_feedback?: boolean; include_storage_size?: boolean } = {}) =>
    post<{ call: Call | null }>('/call/read', { project_id, id, ...opts }),
  callsQuery: (req: CallsQueryReq) => post<{ calls: Call[] }>('/calls/query', req),
  callsStream: (req: CallsQueryReq, onItem: (c: Call) => void, signal?: AbortSignal) =>
    postStream<Call>('/calls/stream_query', req, onItem, signal),
  callsQueryStats: (req: { project_id: string; filter?: CallsFilter; query?: Query; search?: string }) =>
    post<{ count: number; total_storage_size_bytes: number }>('/calls/query_stats', req),
  callsDelete: (project_id: string, call_ids: string[]) => post<object>('/calls/delete', { project_id, call_ids }),
  callUpdate: (project_id: string, call_id: string, display_name: string) =>
    post<object>('/call/update', { project_id, call_id, display_name }),
  callsStats: (req: { project_id: string; filter?: CallsFilter; query?: Query; search?: string; start?: string; end?: string; bucket_seconds?: number }) =>
    post<CallsStatsRes>('/calls/stats', req),
  callsScore: (project_id: string, call_ids: string[], scorer_refs: string[]) =>
    post<{ results: { call_id: string; scorer: string; output: Any; scorer_call_id?: string; feedback_id?: string; error?: string }[] }>(
      '/calls/score',
      { project_id, call_ids, scorer_refs },
    ),
  opCounts: (project_id: string, trace_roots_only = false) =>
    post<{ ops: OpCount[] }>('/calls/op_counts', { project_id, trace_roots_only }),
  threadsQuery: (req: {
    project_id: string
    limit?: number
    offset?: number
    sort_by?: SortBy[]
    filter?: { after_datetime?: string; before_datetime?: string; thread_ids?: string[] }
  }) => post<{ threads: Thread[] }>('/threads/query', req),

  // ---------------------------------------------------------------- objects
  objCreate: (project_id: string, object_id: string, val: Any, builtin_object_class?: string) =>
    post<{ digest: string; object_id: string; version_index: number }>('/obj/create', {
      obj: { project_id, object_id, val, builtin_object_class },
    }),
  objRead: (project_id: string, object_id: string, digest = 'latest') =>
    post<{ obj: ObjSchema }>('/obj/read', { project_id, object_id, digest }),
  objsQuery: (req: {
    project_id: string
    filter?: ObjsFilter
    limit?: number
    offset?: number
    sort_by?: SortBy[]
    metadata_only?: boolean
    include_deleted?: boolean
  }) => post<{ objs: ObjSchema[] }>('/objs/query', req),
  objDelete: (project_id: string, object_id: string, digests?: string[]) =>
    post<{ num_deleted: number }>('/obj/delete', { project_id, object_id, digests }),
  objAliasSet: (project_id: string, object_id: string, digest: string, alias: string) =>
    post<object>('/obj/alias/set', { project_id, object_id, digest, alias }),
  objAliasRemove: (project_id: string, object_id: string, alias: string) =>
    post<object>('/obj/alias/remove', { project_id, object_id, alias }),

  // ---------------------------------------------------------------- tables
  tableCreate: (project_id: string, rows: Any[]) =>
    post<{ digest: string; row_digests: string[] }>('/table/create', { table: { project_id, rows } }),
  tableUpdate: (project_id: string, base_digest: string, updates: TableUpdate[]) =>
    post<{ digest: string; updated_row_digests: string[] }>('/table/update', { project_id, base_digest, updates }),
  tableQuery: (req: { project_id: string; digest: string; filter?: { row_digests?: string[] }; limit?: number; offset?: number; sort_by?: SortBy[] }) =>
    post<{ rows: TableRow[] }>('/table/query', req),
  tableQueryStats: (project_id: string, digest: string) => post<{ count: number }>('/table/query_stats', { project_id, digest }),

  // ---------------------------------------------------------------- refs & files
  refsReadBatch: (refs: string[]) => post<{ vals: Any[] }>('/refs/read_batch', { refs }),
  fileCreate: async (project_id: string, file: Blob, name = 'file') => {
    const fd = new FormData()
    fd.set('project_id', project_id)
    fd.set('file', file, name)
    const res = await fetch(API_BASE + '/file/create', { method: 'POST', headers: headers(false), body: fd })
    if (!res.ok) await raise(res)
    return (await res.json()) as { digest: string }
  },
  fileContentText: async (project_id: string, digest: string) => {
    const res = await fetch(API_BASE + '/file/content', { method: 'POST', headers: headers(), body: JSON.stringify({ project_id, digest }) })
    if (!res.ok) await raise(res)
    return res.text()
  },

  // ---------------------------------------------------------------- feedback
  feedbackCreate: (req: {
    project_id: string
    weave_ref: string
    feedback_type: string
    payload: Any
    creator?: string
    annotation_ref?: string
    runnable_ref?: string
    call_ref?: string
    trigger_ref?: string
  }) => post<{ id: string; created_at: string; wb_user_id: string; payload: Any }>('/feedback/create', req),
  feedbackQuery: (req: { project_id: string; query?: Query; sort_by?: SortBy[]; limit?: number; offset?: number }) =>
    post<{ result: Feedback[] }>('/feedback/query', req),
  feedbackPurge: (project_id: string, id: string) =>
    post<object>('/feedback/purge', { project_id, query: { $expr: { $eq: [{ $getField: 'id' }, { $literal: id }] } } }),
  feedbackReplace: (req: { feedback_id: string; project_id: string; weave_ref: string; feedback_type: string; payload: Any; annotation_ref?: string }) =>
    post<{ id: string }>('/feedback/replace', req),

  // ---------------------------------------------------------------- costs
  costCreate: (
    project_id: string,
    costs: Record<
      string,
      {
        prompt_token_cost: number
        completion_token_cost: number
        cache_read_input_token_cost?: number
        cache_creation_input_token_cost?: number
        effective_date?: string
        provider_id?: string
      }
    >,
  ) => post<{ ids: [string, string][] }>('/cost/create', { project_id, costs }),
  costQuery: (project_id: string, include_defaults = false) =>
    post<{ results: CostEntry[] }>('/cost/query', { project_id, include_defaults, limit: 1000 }),
  costPurge: (project_id: string, ids: string[]) => post<object>('/cost/purge', { project_id, ids }),

  // ---------------------------------------------------------------- llm
  llmModels: () => get<{ models: LLMModelInfo[] }>('/llm/models'),
  completionsCreate: (project_id: string, inputs: CompletionInputs, track_llm_call = true) =>
    post<{ response: ChatCompletion; weave_call_id?: string }>('/completions/create', { project_id, inputs, track_llm_call }),
  completionsStream: (project_id: string, inputs: CompletionInputs, onChunk: (chunk: Any) => void, signal?: AbortSignal) =>
    postStream<Any>('/completions/create_stream', { project_id, inputs, track_llm_call: true }, onChunk, signal),
  secretsList: () => get<{ secrets: { name: string; source: 'env' | 'db' }[] }>('/secrets/list'),
  secretsSet: (name: string, value: string) => post<object>('/secrets/set', { name, value }),
  secretsDelete: (name: string) => post<object>('/secrets/delete', { name }),

  // ---------------------------------------------------------------- evaluations
  evaluateModel: (project_id: string, evaluation_ref: string, model_ref: string) =>
    post<{ call_id: string }>('/evaluate_model', { project_id, evaluation_ref, model_ref }),
  evaluationStatus: (project_id: string, call_id: string) =>
    post<{ status: EvalStatus }>('/evaluation_status', { project_id, call_id }),
  evaluationsCompare: (project_id: string, evaluation_call_ids: string[]) =>
    post<CompareEvaluationsRes>('/evaluations/compare', { project_id, evaluation_call_ids }),
  leaderboardCompute: (project_id: string, object_id: string, digest?: string) =>
    post<LeaderboardComputeRes>('/leaderboard/compute', { project_id, object_id, digest }),
}

export type Api = typeof api
