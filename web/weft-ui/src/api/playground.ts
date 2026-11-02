// Playground history and image generation. See docs/weft/API.md
// ("Weave API parity additions" for /image/create).
import { api, post } from './client'
import type { Any, Call } from './types'

export interface ImageInputs {
  model: string
  prompt: string
  n?: number
  size?: string
  quality?: string
  response_format?: 'b64_json' | 'url'
}

export interface ImageResponse {
  created?: number
  data?: { b64_json?: string; url?: string; revised_prompt?: string }[]
  usage?: Any
  error?: string
}

export const PLAYGROUND_OPS = ['weft.completions.create', 'weft.images.generate']

export const playgroundApi = {
  imageCreate: (project_id: string, inputs: ImageInputs, track_llm_call = true) =>
    post<{ response: ImageResponse; weave_call_id?: string | null }>('/image/create', { project_id, inputs, track_llm_call }),
  /** Tracked playground calls (chat and image), newest first; judge and monitor calls are excluded. */
  history: (project_id: string, limit = 50, offset = 0): Promise<{ calls: Call[] }> =>
    api.callsQuery({
      project_id,
      filter: { op_names: PLAYGROUND_OPS },
      query: { $expr: { $not: [{ $eq: [{ $getField: 'attributes.weft.internal' }, { $literal: true }] }] } },
      sort_by: [{ field: 'started_at', direction: 'desc' }],
      limit,
      offset,
      include_costs: true,
    }),
}
