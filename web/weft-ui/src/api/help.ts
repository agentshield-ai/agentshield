// The guides in docs/weft, embedded in the server (GET /api/v1/help).
import { get } from './client'

export interface Guide {
  name: string
  title: string
}

export interface GuidePage extends Guide {
  markdown: string
}

export const helpApi = {
  list: () => get<{ items: Guide[] }>('/api/v1/help'),
  read: (name: string) => get<GuidePage>(`/api/v1/help/${encodeURIComponent(name)}`),
}
