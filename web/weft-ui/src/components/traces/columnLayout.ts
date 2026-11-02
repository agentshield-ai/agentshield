// Column order and pinning for the traces table (unit tested in columnLayout.test.ts).
// Columns are identified by id: base column names ("name", "latency", …) and
// field paths ("inputs.question"). URL form: `order` and `pin`, comma
// separated, ids URI-encoded.

export interface ColumnSlot {
  id: string
  pinned: boolean
}

export function encodeIds(ids: string[]): string {
  return ids.map(encodeURIComponent).join(',')
}

export function decodeIds(s: string | null | undefined): string[] {
  if (!s) return []
  return [
    ...new Set(
      s
        .split(',')
        .filter(Boolean)
        .map((x) => {
          try {
            return decodeURIComponent(x)
          } catch {
            return x
          }
        }),
    ),
  ]
}

/**
 * Visible columns in display order: pinned columns first (in their relative
 * order), then the rest. `order` lists ids in the user's order; visible
 * columns missing from it keep their natural position after the ordered ones.
 */
export function layoutColumns(visible: string[], order: string[], pinned: string[]): ColumnSlot[] {
  const vis = new Set(visible)
  const ordered = [...order.filter((id) => vis.has(id)), ...visible.filter((id) => !order.includes(id))]
  const pin = new Set(pinned)
  return [...ordered.filter((id) => pin.has(id)), ...ordered.filter((id) => !pin.has(id))].map((id) => ({ id, pinned: pin.has(id) }))
}

/** Full order after moving `id` before `target` (or to the end when target is null). */
export function moveBefore(current: string[], id: string, target: string | null): string[] {
  if (id === target) return current
  const rest = current.filter((x) => x !== id)
  const i = target == null ? -1 : rest.indexOf(target)
  if (i < 0) return [...rest, id]
  return [...rest.slice(0, i), id, ...rest.slice(i)]
}

/** Full order after moving `id` one step left (-1) or right (+1) among `current`. */
export function moveStep(current: string[], id: string, dir: -1 | 1): string[] {
  const i = current.indexOf(id)
  const j = i + dir
  if (i < 0 || j < 0 || j >= current.length) return current
  const out = [...current]
  ;[out[i], out[j]] = [out[j], out[i]]
  return out
}

export function togglePin(pinned: string[], id: string): string[] {
  return pinned.includes(id) ? pinned.filter((x) => x !== id) : [...pinned, id]
}

/** Left offsets (px) for sticky pinned columns, after a fixed leading width. */
export function pinnedOffsets(slots: ColumnSlot[], widths: Record<string, number>, lead = 0): Record<string, number> {
  const out: Record<string, number> = {}
  let x = lead
  for (const s of slots) {
    if (!s.pinned) break
    out[s.id] = x
    x += widths[s.id] ?? 0
  }
  return out
}
