import type { Any, Span, Feedback } from '../../api/types'
import { spanDisplayName } from '../../api/refs'
import { callCost, callLatencyMs, callStatus, callTokens } from '../../lib/format'

export interface TreeNode {
  call: Span
  children: TreeNode[]
  depth: number
}

/** Build a forest from a flat list of calls, by parent_id. Children sorted by started_at. */
export function buildTree(calls: Span[]): TreeNode[] {
  const byId = new Map<string, TreeNode>()
  for (const c of calls) byId.set(c.id, { call: c, children: [], depth: 0 })
  const roots: TreeNode[] = []
  for (const n of byId.values()) {
    const p = n.call.parent_id ? byId.get(n.call.parent_id) : undefined
    if (p) p.children.push(n)
    else roots.push(n)
  }
  const byStart = (a: TreeNode, b: TreeNode) => a.call.started_at.localeCompare(b.call.started_at)
  const fix = (ns: TreeNode[], d: number) => {
    ns.sort(byStart)
    for (const n of ns) {
      n.depth = d
      fix(n.children, d + 1)
    }
  }
  fix(roots, 0)
  return roots
}

export function flattenTree(roots: TreeNode[], isOpen: (id: string) => boolean = () => true): TreeNode[] {
  const out: TreeNode[] = []
  const walk = (ns: TreeNode[]) => {
    for (const n of ns) {
      out.push(n)
      if (n.children.length && isOpen(n.call.id)) walk(n.children)
    }
  }
  walk(roots)
  return out
}

export function callLabel(c: Span): string {
  return c.display_name || c.summary?.weft?.display_name || spanDisplayName(c.name)
}

// ---------------------------------------------------------------- field discovery

const MAX_KEYS = 60

/** Discover dotted paths under `inputs.` / `output.` / `attributes.` from loaded calls (up to 2 levels). */
export function discoverFields(calls: Span[], roots: ('inputs' | 'output' | 'attributes')[] = ['inputs', 'output']): string[] {
  const out = new Set<string>()
  for (const root of roots) {
    for (const c of calls) {
      const v = (c as Any)[root]
      if (!v || typeof v !== 'object' || Array.isArray(v)) continue
      for (const [k, sub] of Object.entries(v)) {
        if (root === 'attributes' && k === 'otel') continue
        out.add(`${root}.${k}`)
        if (sub && typeof sub === 'object' && !Array.isArray(sub) && root !== 'attributes')
          for (const k2 of Object.keys(sub).slice(0, 20)) if (!k2.startsWith('_')) out.add(`${root}.${k}.${k2}`)
      }
      if (out.size > MAX_KEYS * roots.length) break
    }
  }
  return [...out].sort()
}

/** Feedback query paths discovered from feedback attached to calls. */
export function discoverFeedbackFields(calls: Span[]): string[] {
  const out = new Set<string>(['feedback.weft.reaction.emoji', 'feedback.weft.note.note'])
  for (const c of calls)
    for (const f of c.feedback ?? []) {
      if (f.feedback_type.startsWith('weft.scorer.')) {
        const o = f.payload?.output
        if (o && typeof o === 'object' && !Array.isArray(o)) for (const k of Object.keys(o)) out.add(`feedback.${f.feedback_type}.output.${k}`)
        else out.add(`feedback.${f.feedback_type}.output`)
      } else if (f.feedback_type.startsWith('weft.annotation.')) out.add(`feedback.${f.feedback_type}.value`)
    }
  return [...out].sort()
}

// ---------------------------------------------------------------- feedback summaries

export function reactionCounts(fb: Feedback[] | undefined): [string, number][] {
  const m = new Map<string, number>()
  for (const f of fb ?? []) if (f.feedback_type === 'weft.reaction' && f.payload?.emoji) m.set(f.payload.emoji, (m.get(f.payload.emoji) ?? 0) + 1)
  return [...m.entries()].sort((a, b) => b[1] - a[1])
}

// ---------------------------------------------------------------- export

export function download(filename: string, content: string, mime: string) {
  const blob = new Blob([content], { type: mime })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

function csvCell(v: unknown): string {
  if (v == null) return ''
  const s = typeof v === 'string' ? v : typeof v === 'number' || typeof v === 'boolean' ? String(v) : JSON.stringify(v)
  return /[",\n\r]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
}

export function callsToCsv(calls: Span[], extraPaths: string[] = []): string {
  const head = ['id', 'trace_id', 'parent_id', 'name', 'display_name', 'status', 'started_at', 'ended_at', 'latency_ms', 'total_tokens', 'cost_usd', 'error', 'inputs', 'output', ...extraPaths]
  const lines = [head.join(',')]
  for (const c of calls) {
    const row: unknown[] = [
      c.id,
      c.trace_id,
      c.parent_id,
      spanDisplayName(c.name),
      c.display_name,
      callStatus(c),
      c.started_at,
      c.ended_at,
      callLatencyMs(c),
      callTokens(c).total,
      callCost(c),
      c.error,
      c.inputs,
      c.output,
      ...extraPaths.map((p) => getDotted(c, p)),
    ]
    lines.push(row.map(csvCell).join(','))
  }
  return lines.join('\n') + '\n'
}

export function exportCalls(calls: Span[], format: 'json' | 'jsonl' | 'csv', extraPaths: string[] = []) {
  const stamp = new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')
  if (format === 'json') download(`calls-${stamp}.json`, JSON.stringify(calls, null, 2), 'application/json')
  else if (format === 'jsonl') download(`calls-${stamp}.jsonl`, calls.map((c) => JSON.stringify(c)).join('\n') + '\n', 'application/jsonl')
  else download(`calls-${stamp}.csv`, callsToCsv(calls, extraPaths), 'text/csv')
}

/** getPath variant that also tolerates keys containing dots at the leaf (e.g. attributes."gen_ai.system"). */
export function getDotted(obj: unknown, path: string): unknown {
  const parts = path.split('.')
  let cur: unknown = obj
  for (let i = 0; i < parts.length; i++) {
    if (cur == null || typeof cur !== 'object') return undefined
    const rec = cur as Record<string, unknown>
    if (parts[i] in rec) {
      cur = rec[parts[i]]
      continue
    }
    // try joining the remaining parts (keys containing dots)
    const rest = parts.slice(i).join('.')
    return rest in rec ? rec[rest] : undefined
  }
  return cur
}

/** Row for a dataset built from a call: its inputs (minus `self`) plus `output`. */
export function callToDatasetRow(c: Span, includeOutput: boolean): Record<string, Any> {
  const row: Record<string, Any> = {}
  const inputs = c.inputs && typeof c.inputs === 'object' && !Array.isArray(c.inputs) ? c.inputs : { input: c.inputs }
  for (const [k, v] of Object.entries(inputs)) if (k !== 'self') row[k] = v
  if (includeOutput) row.output = c.output ?? null
  return row
}

export function slugify(s: string): string {
  return s
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, '-')
    .replace(/^-+|-+$/g, '')
}
