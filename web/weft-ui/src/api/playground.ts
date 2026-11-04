// Playground history and image generation. See docs/weft/API.md
// ("Model calls and playground").
import { api, post } from './client'
import type { Any, ListRes, Span } from './types'

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
  images: (project_id: string, inputs: ImageInputs, track = true) =>
    post<{ response: ImageResponse; span_id?: string | null }>('/api/v1/images', { project_id, inputs, track }),
  /** Tracked playground spans (chat and image), newest first; judge and monitor spans are excluded. */
  history: (project_id: string, limit = 50, offset = 0): Promise<ListRes<Span>> =>
    api.spansQuery({
      project_id,
      filter: { names: PLAYGROUND_OPS },
      query: { $expr: { $not: [{ $eq: [{ $getField: 'attributes.weft.internal' }, { $literal: true }] }] } },
      sort_by: [{ field: 'started_at', direction: 'desc' }],
      limit,
      offset,
      include_costs: true,
    }),
}
