// Custom runtimes (the SDKs' register_custom_runtime). See docs/weft/API.md "Custom runtimes".
import { get } from './client'

export interface Runtime {
  name: string
  base_url: string
  api_key_secret?: string | null
  headers?: Record<string, string> | null
  runtime_ids: { id: string; max_tokens?: number; playground_id?: string }[]
  kind?: 'openai' | 'predict'
  ref?: string
}

export const runtimesApi = {
  list: (projectId: string) => get<{ items: Runtime[]; total_count: number }>(`/api/v1/runtimes?project_id=${encodeURIComponent(projectId)}`),
}
