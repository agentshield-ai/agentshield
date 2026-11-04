import { parseRef } from '../api/refs'

export function splitProject(projectId: string): [string, string] {
  const i = projectId.indexOf('/')
  return i < 0 ? ['default', projectId] : [projectId.slice(0, i), projectId.slice(i + 1)]
}

export const projectBase = (projectId: string) => `/${projectId}`
export const tracesUrl = (projectId: string, params?: Record<string, string>) =>
  `/${projectId}/traces${params ? '?' + new URLSearchParams(params).toString() : ''}`
export const spanUrl = (projectId: string, spanId: string, tab?: string) =>
  `/${projectId}/spans/${encodeURIComponent(spanId)}${tab ? `?tab=${tab}` : ''}`
/** Spans grouped by session id (the raw view under Sessions). */
export const spanSessionsUrl = (projectId: string) => `/${projectId}/traces/sessions`
export const spanSessionUrl = (projectId: string, sessionId: string) => `/${projectId}/traces/sessions/${encodeURIComponent(sessionId)}`
export const playgroundUrl = (projectId: string, spanId?: string) =>
  `/${projectId}/playground${spanId ? `?span=${encodeURIComponent(spanId)}` : ''}`
export const compareSpansUrl = (projectId: string, ids: string[]) => `/${projectId}/compare-spans?ids=${ids.join(',')}`
export const evaluationsUrl = (projectId: string) => `/${projectId}/evaluations`
export const compareEvaluationsUrl = (projectId: string, runIds: string[]) =>
  `/${projectId}/evaluations/compare?ids=${runIds.join(',')}`
export const newEvaluationUrl = (projectId: string) => `/${projectId}/evaluations/new`
export const objectVersionsUrl = (projectId: string, objectId: string) => `/${projectId}/objects/${encodeURIComponent(objectId)}`
export const objectUrl = (projectId: string, objectId: string, digest = 'latest') =>
  `/${projectId}/objects/${encodeURIComponent(objectId)}/versions/${encodeURIComponent(digest)}`
export const objectDiffUrl = (projectId: string, objectId: string, a: string, b: string) =>
  `/${projectId}/objects/${encodeURIComponent(objectId)}/compare?a=${encodeURIComponent(a)}&b=${encodeURIComponent(b)}`
export const leaderboardUrl = (projectId: string, objectId: string) => `/${projectId}/leaderboards/${encodeURIComponent(objectId)}`
export const monitorUrl = (projectId: string, objectId: string) => `/${projectId}/monitors/${encodeURIComponent(objectId)}`
export const settingsUrl = (projectId: string, tab?: string) => `/${projectId}/settings${tab ? `?tab=${tab}` : ''}`

/** Map any ref to the UI page that shows it. Returns null when there is no page. */
export function refUrl(ref: string): string | null {
  const p = parseRef(ref)
  if (!p) return null
  switch (p.kind) {
    case 'span':
      return spanUrl(p.projectId, p.name)
    case 'object':
    case 'function':
      return objectUrl(p.projectId, p.name, p.digest ?? 'latest')
    case 'table':
      return null
  }
}

// ---------------------------------------------------------------- agent-first pages
export const sessionsUrl = (projectId: string, params?: string) => `/${projectId}/sessions${params ? `?${params}` : ''}`
export const sessionUrl = (projectId: string, sessionId: string, opts: { tab?: string; step?: string } = {}) => {
  const q = new URLSearchParams()
  if (opts.tab && opts.tab !== 'timeline') q.set('tab', opts.tab)
  if (opts.step) q.set('step', opts.step)
  const s = q.toString()
  return `/${projectId}/sessions/${encodeURIComponent(sessionId)}${s ? `?${s}` : ''}`
}
export const agentUrl = (projectId: string, name: string) => `/${projectId}/agents/${encodeURIComponent(name)}`
export const agentAnalyticsUrl = (projectId: string, name?: string) =>
  name ? `/${projectId}/agents/${encodeURIComponent(name)}/analytics` : `/${projectId}/agent-analytics`
export const teamsUrl = (projectId: string) => `/${projectId}/teams`
export const teamUrl = (projectId: string, teamId: string) => `/${projectId}/teams/${encodeURIComponent(teamId)}`
export const securityUrl = (projectId: string, params?: string) => `/${projectId}/security${params ? `?${params}` : ''}`
/** Raw spans of one trace in the Traces explorer. */
export const traceSpansUrl = (projectId: string, traceId: string) =>
  `/${projectId}/traces?roots=0&f=${encodeURIComponent(JSON.stringify([['trace_id', 'eq', traceId]]))}`
export const agentsUrlFor = (projectId: string) => `/${projectId}/agents`
