// Playground run history (unit tested in playground.test.ts). Runs in this
// browser are kept in localStorage per project; tracked runs come from the
// server as weft.completions.create / weft.images.generate calls.
import type { Any, Call } from '../../api/types'

export interface PlaygroundRun {
  id: string
  at: string
  mode: 'chat' | 'image'
  model: string
  /** The request inputs: chat completion inputs, or image inputs. */
  inputs: Record<string, Any>
  output: string
  callId?: string
  latencyMs?: number
  tokens?: number
  error?: string
  /** Image runs: how many images came back. */
  images?: number
  source: 'local' | 'server'
}

export const HISTORY_MAX = 50
const key = (projectId: string) => `weft.playground.history.${projectId}`

type Store = Pick<Storage, 'getItem' | 'setItem'>

function storage(): Store | null {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage
  } catch {
    return null
  }
}

export function loadRuns(projectId: string, s: Store | null = storage()): PlaygroundRun[] {
  try {
    const v = JSON.parse(s?.getItem(key(projectId)) ?? '[]')
    return Array.isArray(v) ? v.filter((r) => r && typeof r.id === 'string' && r.inputs && typeof r.inputs === 'object') : []
  } catch {
    return []
  }
}

export function saveRuns(projectId: string, runs: PlaygroundRun[], s: Store | null = storage()) {
  try {
    s?.setItem(key(projectId), JSON.stringify(runs.slice(0, HISTORY_MAX)))
  } catch {
    /* storage full or unavailable */
  }
}

/** Newest first, de-duplicated by id, capped; large image payloads are not stored. */
export function addRun(runs: PlaygroundRun[], run: PlaygroundRun, max = HISTORY_MAX): PlaygroundRun[] {
  const slim = { ...run, output: run.output.length > 4000 ? run.output.slice(0, 4000) + '…' : run.output }
  return [slim, ...runs.filter((r) => r.id !== run.id)].slice(0, max)
}

function lastUser(inputs: Record<string, Any>): string {
  if (typeof inputs.prompt === 'string') return inputs.prompt
  const msgs = Array.isArray(inputs.messages) ? inputs.messages : []
  for (let i = msgs.length - 1; i >= 0; i--) {
    const m = msgs[i]
    if (m?.role === 'user') return contentText(m.content)
  }
  return msgs.length ? contentText(msgs[msgs.length - 1]?.content) : ''
}

function contentText(c: Any): string {
  if (typeof c === 'string') return c
  if (Array.isArray(c)) return c.map((p) => (typeof p?.text === 'string' ? p.text : '')).join(' ')
  return c == null ? '' : JSON.stringify(c)
}

/** One-line description of what was asked. */
export function runPrompt(r: Pick<PlaygroundRun, 'inputs'>): string {
  return lastUser(r.inputs).replace(/\s+/g, ' ').trim()
}

/** A tracked playground call as a history run. */
export function callToRun(c: Call): PlaygroundRun | null {
  const inputs = (c.inputs ?? {}) as Record<string, Any>
  if (typeof inputs.model !== 'string') return null
  const image = c.op_name.includes('weft.images.generate')
  const out = (c.output ?? {}) as Any
  let output = ''
  let images: number | undefined
  if (image) {
    images = Array.isArray(out?.data) ? out.data.length : 0
    output = `${images} image${images === 1 ? '' : 's'}`
  } else {
    const msg = out?.choices?.[0]?.message
    output = msg ? contentText(msg.content) || (Array.isArray(msg.tool_calls) ? `→ ${msg.tool_calls.map((t: Any) => t?.function?.name).join(', ')}()` : '') : ''
  }
  const started = new Date(c.started_at).getTime()
  const ended = c.ended_at ? new Date(c.ended_at).getTime() : NaN
  let tokens = 0
  for (const u of Object.values(c.summary?.usage ?? {})) tokens += u.total_tokens ?? (u.prompt_tokens ?? 0) + (u.completion_tokens ?? 0)
  return {
    id: `call:${c.id}`,
    at: c.started_at,
    mode: image ? 'image' : 'chat',
    model: inputs.model,
    inputs,
    output: output || (out?.error ? String(out.error) : ''),
    callId: c.id,
    latencyMs: Number.isFinite(ended) ? ended - started : undefined,
    tokens: tokens || undefined,
    error: c.exception ?? undefined,
    images,
    source: 'server',
  }
}
