// Which sidebar item a page belongs to, when its URL alone does not say.
// A dataset opens at /objects/<name>, so the URL points at "All objects",
// but its back link says "Datasets": PageHeader publishes its back link
// here and the sidebar highlights the item that link leads to.
import { useSyncExternalStore } from 'react'

let hint: string | null = null
const subs = new Set<() => void>()

/** Sets (or clears, with null) the path of the list the current page belongs to. */
export function setNavHint(to: string | null) {
  if (hint === to) return
  hint = to
  subs.forEach((f) => f())
}

export function useNavHint(): string | null {
  return useSyncExternalStore(
    (f) => {
      subs.add(f)
      return () => subs.delete(f)
    },
    () => hint,
  )
}

/** The first path segment after the project: navSection('/acme/bot/datasets?x=1', '/acme/bot') → 'datasets'. */
export function navSection(path: string, base: string): string | null {
  const p = path.split(/[?#]/)[0]
  if (!p.startsWith(base + '/')) return null
  return p.slice(base.length + 1).split('/')[0] || null
}
