// Annotation queues (Weave /annotation_queues API).
import { ApiError, getApiKey, post, postStream } from './client'

export type AnnotationState = 'unstarted' | 'in_progress' | 'completed' | 'skipped'

export interface AnnotationQueue {
  id: string
  project_id: string
  name: string
  description: string
  scorer_refs: string[]
  created_at: string
  created_by: string
  updated_at: string
  deleted_at: string | null
}

export interface AnnotationQueueItem {
  id: string
  project_id: string
  queue_id: string
  call_id: string
  call_op_name: string
  call_trace_id: string
  call_started_at: string
  call_ended_at: string | null
  display_fields: string[]
  added_by: string | null
  annotation_state: AnnotationState
  annotator_user_id: string | null
  created_at: string
  created_by: string
  updated_at: string
  deleted_at: string | null
  position_in_queue?: number
}

export interface AnnotationQueueStat {
  queue_id: string
  total_items: number
  completed_items: number
}

const API_BASE: string = (import.meta.env?.VITE_WEFT_API as string | undefined) ?? ''

/** fetch for the PUT/GET/DELETE queue endpoints. */
async function send<T>(method: 'GET' | 'PUT' | 'DELETE', path: string, body?: unknown): Promise<T> {
  const h: Record<string, string> = { 'Content-Type': 'application/json' }
  const key = getApiKey()
  if (key) h['Authorization'] = `Bearer ${key}`
  const res = await fetch(API_BASE + path, { method, headers: h, body: body === undefined ? undefined : JSON.stringify(body) })
  if (!res.ok) {
    let msg = res.statusText
    try {
      const b = await res.json()
      msg = b.reason ?? b.detail ?? JSON.stringify(b)
    } catch {
      /* not JSON */
    }
    throw new ApiError(res.status, msg)
  }
  return res.json() as Promise<T>
}

const q = (id: string) => `/annotation_queues/${encodeURIComponent(id)}`

export const queuesApi = {
  create: (project_id: string, name: string, description: string, scorer_refs: string[]) =>
    post<{ id: string }>('/annotation_queues', { project_id, name, description, scorer_refs }),
  query: async (project_id: string, name?: string): Promise<AnnotationQueue[]> => {
    const out: AnnotationQueue[] = []
    await postStream<AnnotationQueue>('/annotation_queues/query', { project_id, name: name || undefined, sort_by: [{ field: 'created_at', direction: 'desc' }] }, (x) =>
      out.push(x),
    )
    return out
  },
  stats: (project_id: string, queue_ids: string[]) => post<{ stats: AnnotationQueueStat[] }>('/annotation_queues/stats', { project_id, queue_ids }),
  read: (project_id: string, id: string) => send<{ queue: AnnotationQueue }>('GET', `${q(id)}?project_id=${encodeURIComponent(project_id)}`),
  update: (project_id: string, id: string, patch: { name?: string; description?: string; scorer_refs?: string[] }) =>
    send<{ queue: AnnotationQueue }>('PUT', q(id), { project_id, ...patch }),
  remove: (project_id: string, id: string) => send<{ queue: AnnotationQueue }>('DELETE', `${q(id)}?project_id=${encodeURIComponent(project_id)}`),
  addItems: (project_id: string, id: string, call_ids: string[], display_fields: string[] = []) =>
    post<{ added_count: number; duplicates: number }>(`${q(id)}/items`, { project_id, call_ids, display_fields }),
  items: (
    project_id: string,
    id: string,
    opts: { filter?: { annotation_states?: AnnotationState[]; call_id?: string; id?: string }; limit?: number; offset?: number; include_position?: boolean } = {},
  ) => post<{ items: AnnotationQueueItem[] }>(`${q(id)}/items/query`, { project_id, include_position: true, ...opts }),
  progress: (project_id: string, id: string, item_id: string, annotation_state: AnnotationState) =>
    post<{ item: AnnotationQueueItem }>(`${q(id)}/items/${encodeURIComponent(item_id)}/progress`, { project_id, queue_id: id, annotation_state }),
  /** Annotation feedback written from a queue carries its queue_id. */
  annotate: (req: { project_id: string; weave_ref: string; feedback_type: string; payload: unknown; annotation_ref?: string; queue_id: string; replace_id?: string }) => {
    const { replace_id, ...body } = req
    return replace_id
      ? post<{ id: string; wb_user_id?: string }>('/feedback/replace', { ...body, feedback_id: replace_id })
      : post<{ id: string; wb_user_id?: string }>('/feedback/create', body)
  },
}
