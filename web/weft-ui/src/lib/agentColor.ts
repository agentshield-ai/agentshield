// A consistent colour per agent across every page (DESIGN.md §7.1 rule 9):
// a stable hash of the name picks one of the --agent-N tokens (weft.css),
// which have light and dark variants that keep text contrast.

export const AGENT_PALETTE_SIZE = 10

/** FNV-1a, so the same name maps to the same slot in every browser. */
export function hashName(name: string): number {
  let h = 0x811c9dc5
  for (let i = 0; i < name.length; i++) {
    h ^= name.charCodeAt(i)
    h = Math.imul(h, 0x01000193) >>> 0
  }
  return h >>> 0
}

export function agentSlot(name: string | null | undefined): number {
  if (!name) return -1
  return hashName(name) % AGENT_PALETTE_SIZE
}

export function agentColor(name: string | null | undefined): string {
  const i = agentSlot(name)
  return i < 0 ? 'var(--text-3)' : `var(--agent-${i})`
}

export function agentInitials(name: string): string {
  const parts = name.split(/[\s_\-.:/]+/).filter(Boolean)
  if (!parts.length) return '?'
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase()
  return (parts[0][0] + parts[1][0]).toUpperCase()
}
