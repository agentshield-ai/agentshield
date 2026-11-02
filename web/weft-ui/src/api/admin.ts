// Administration API: managed API keys, project settings (retention TTL,
// ingest sampling, sensitive-data redaction), project delete, object tags
// and aliases, and dataset lineage. See docs/weft/API.md "Administration".
import { ApiError, get, getApiKey, post } from './client'

const API_BASE: string = (import.meta.env?.VITE_WEFT_API as string | undefined) ?? ''

async function send<T>(method: 'PUT' | 'DELETE', path: string, body?: unknown): Promise<T> {
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

const q = (projectId: string) => `project_id=${encodeURIComponent(projectId)}`
const seg = encodeURIComponent

// ---------------------------------------------------------------- API keys

export interface ManagedApiKey {
  id: string
  name: string
  user_id: string
  prefix: string
  all_projects: boolean
  projects: string[]
  read_only: boolean
  admin: boolean
  created_by: string | null
  created_at: string
  last_used_at: string | null
  revoked_at: string | null
  user_bound?: boolean
}

export interface ApiKeyCreate {
  name: string
  user_id?: string
  projects?: string[]
  read_only?: boolean
  admin?: boolean
}

export interface Principal {
  auth_required: boolean
  /** true for user-bound keys and SSO sessions: access comes from the user's roles */
  rbac?: boolean
  auth_method?: string
  admin: boolean
  read_only: boolean
  projects: string[] | null
  key_id: string | null
  key_name: string | null
  user_id: string
}

// ---------------------------------------------------------------- settings

export interface SamplingSettings {
  sample_rate: number
  dry_run: boolean
}

export interface RedactionEntity {
  name: string
  placeholder: string
  description: string
}

export interface RedactionPattern {
  name: string
  pattern: string
}

export interface SensitiveDataSettings {
  policy: 'off' | 'pii-v1'
  entities: string[]
  custom_patterns: RedactionPattern[]
  available_entities?: RedactionEntity[]
}

// ---------------------------------------------------------------- lineage

export interface DatasetSourceLink {
  id: string
  dataset_object_id: string
  dataset_digest: string
  row_digest: string
  source_kind: 'call' | 'span' | 'conversation'
  source_id: string
  source_trace_id: string
  source_display_name: string
  source_started_at: string
  link_metadata: Record<string, unknown> | null
  added_by: string | null
  created_at: string
  updated_at: string
  deleted_at: string | null
}

export const adminApi = {
  principal: () => get<Principal>('/auth/principal'),
  apiKeys: (includeRevoked = false) =>
    get<{ keys: ManagedApiKey[]; static_key_count: number }>(`/api_keys/list${includeRevoked ? '?include_revoked=true' : ''}`),
  apiKeyCreate: (req: ApiKeyCreate) => post<{ key: ManagedApiKey; secret: string }>('/api_keys/create', req),
  apiKeyRevoke: (id: string) => post<{ key: ManagedApiKey }>('/api_keys/revoke', { id }),

  ttlRead: (project_id: string) => post<{ retention_days: number | null }>('/project/ttl_settings/read', { project_id }),
  ttlUpdate: (project_id: string, retention_days: number | null) =>
    post<{ retention_days: number | null }>('/project/ttl_settings/update', { project_id, retention_days }),
  ttlPurge: (project_id: string) =>
    post<{ purged: { calls: number; feedback: number }; retention_days: number | null }>('/project/ttl_settings/purge', { project_id }),

  samplingRead: (project_id: string) => post<SamplingSettings>('/project/ingest_sampling_settings/read', { project_id }),
  samplingUpdate: (project_id: string, s: SamplingSettings) => post<SamplingSettings>('/project/ingest_sampling_settings/update', { project_id, ...s }),

  sensitiveRead: (project_id: string) => get<SensitiveDataSettings>(`/project/sensitive_data_settings?${q(project_id)}`),
  sensitiveUpdate: (project_id: string, s: SensitiveDataSettings) =>
    post<SensitiveDataSettings>('/project/sensitive_data_settings/update', {
      project_id,
      policy: s.policy,
      entities: s.entities,
      custom_patterns: s.custom_patterns,
    }),
  sensitivePreview: (s: SensitiveDataSettings, text: string) =>
    post<{ text: string }>('/project/sensitive_data_settings/preview', { policy: s.policy, entities: s.entities, custom_patterns: s.custom_patterns, text }),

  projectDelete: (project_id: string) => post<{ project_id: string; deleted: Record<string, number> }>('/project/delete', { project_id, confirm: project_id }),

  tags: (project_id: string) => get<{ tags: string[] }>(`/tags?${q(project_id)}`),
  aliases: (project_id: string) => get<{ aliases: string[] }>(`/aliases?${q(project_id)}`),
  tagsAdd: (project_id: string, objectId: string, digest: string, tags: string[]) =>
    send<Record<string, never>>('PUT', `/objs/${seg(objectId)}/versions/${seg(digest)}/tags`, { project_id, tags }),
  tagsRemove: (project_id: string, objectId: string, digest: string, tags: string[]) =>
    post<Record<string, never>>(`/objs/${seg(objectId)}/versions/${seg(digest)}/tags/remove`, { project_id, tags }),

  datasetSources: (project_id: string, dataset_object_id: string, row_digests?: string[]) =>
    post<{ links: DatasetSourceLink[] }>('/dataset_sources/query', { project_id, dataset_object_id, row_digests, limit: 1000 }),
  datasetSourcesDelete: (project_id: string, link_ids: string[]) =>
    post<{ entries: { link_id: string; deleted: boolean }[] }>('/dataset_sources/link_delete', { project_id, link_ids }),
}
