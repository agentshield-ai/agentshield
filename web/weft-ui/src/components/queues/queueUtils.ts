import type { AnnotationQueueItem } from '../../api/queues'
import type { Any, Span, Feedback, WeftObject } from '../../api/types'
import { callToDatasetRow, getDotted } from '../traces/callUtils'

/** Name of an AnnotationSpec object (its `name` field, else the object name). */
export function specName(spec: Pick<WeftObject, 'name' | 'value'>): string {
  return String(spec.value?.name ?? spec.name)
}

export const annotationType = (name: string) => `weft.annotation.${name}`

const PENDING = new Set(['unstarted', 'in_progress'])

/** Index of the next item still to review after `from` (wrapping), or -1. */
export function nextPendingIndex(items: Pick<AnnotationQueueItem, 'annotation_state'>[], from: number): number {
  const n = items.length
  for (let step = 1; step <= n; step++) {
    const i = (((from + step) % n) + n) % n
    if (PENDING.has(items[i].annotation_state)) return i
  }
  return -1
}

export interface QueueProgress {
  total: number
  completed: number
  skipped: number
  remaining: number
  /** completed + skipped as a fraction of total (0..1). */
  done: number
}

export function progressCounts(items: Pick<AnnotationQueueItem, 'annotation_state'>[]): QueueProgress {
  let completed = 0
  let skipped = 0
  for (const it of items) {
    if (it.annotation_state === 'completed') completed++
    else if (it.annotation_state === 'skipped') skipped++
  }
  const total = items.length
  return { total, completed, skipped, remaining: total - completed - skipped, done: total ? (completed + skipped) / total : 0 }
}

/** Values of a call at the queue's display_fields paths (e.g. "inputs.question"). */
export function displayFieldValues(call: Span, fields: string[]): { path: string; value: unknown }[] {
  return fields.map((path) => ({ path, value: getDotted(call, path) }))
}

/**
 * Dataset rows for a queue export: each reviewed call's inputs and output,
 * plus one column per annotation spec holding the latest value given in this
 * queue, and `annotators` listing who annotated it. Calls without any
 * annotation are left out unless includeUnannotated is set.
 */
export function buildExportRows(
  items: Pick<AnnotationQueueItem, 'call_id'>[],
  calls: Map<string, Span>,
  feedback: Feedback[],
  specNames: string[],
  includeUnannotated = false,
): Record<string, Any>[] {
  const byCall = new Map<string, Feedback[]>()
  for (const f of feedback) {
    const m = /\/(?:span|call)\/([^/]+)$/.exec(f.ref)
    if (!m) continue
    const list = byCall.get(m[1]) ?? []
    list.push(f)
    byCall.set(m[1], list)
  }
  const rows: Record<string, Any>[] = []
  for (const it of items) {
    const call = calls.get(it.call_id)
    if (!call) continue
    const fbs = (byCall.get(it.call_id) ?? []).filter((f) => f.feedback_type.startsWith('weft.annotation.'))
    if (!fbs.length && !includeUnannotated) continue
    const row: Record<string, Any> = { ...callToDatasetRow(call, true) }
    const users = new Set<string>()
    for (const name of specNames) {
      const latest = fbs
        .filter((f) => f.feedback_type === annotationType(name))
        .sort((a, b) => (a.created_at < b.created_at ? 1 : a.created_at > b.created_at ? -1 : 0))[0]
      row[name] = latest ? (latest.payload?.value ?? null) : null
    }
    for (const f of fbs) users.add(f.creator ?? f.user_id ?? 'anonymous')
    row.annotators = [...users].sort()
    row.call_id = call.id
    rows.push(row)
  }
  return rows
}
