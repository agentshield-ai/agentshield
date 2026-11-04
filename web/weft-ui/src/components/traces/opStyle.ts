// Op kind and colour set by the SDKs (attributes.weft.span_kind / .color).
// Colours are limited to hex values and a few names mapped to theme tokens.
import type { Span } from '../../api/types'

const NAMED: Record<string, string> = {
  red: 'var(--error)',
  orange: 'var(--c3)',
  yellow: 'var(--warn)',
  green: 'var(--c5)',
  teal: 'var(--c2)',
  blue: 'var(--c6)',
  indigo: 'var(--c1)',
  purple: 'var(--c4)',
  magenta: 'var(--c4)',
}

export function safeColor(c: unknown): string | undefined {
  if (typeof c !== 'string') return undefined
  const v = c.trim().toLowerCase()
  if (/^#([0-9a-f]{3}|[0-9a-f]{6})$/.test(v)) return v
  return NAMED[v]
}

export function opStyle(c: Pick<Span, 'attributes'>): { kind?: string; color?: string } {
  const a = (c.attributes ?? {}) as Record<string, Record<string, unknown> | undefined>
  const kind = a.weft?.span_kind
  return { kind: typeof kind === 'string' && kind ? kind.slice(0, 24) : undefined, color: safeColor(a.weft?.color) }
}
