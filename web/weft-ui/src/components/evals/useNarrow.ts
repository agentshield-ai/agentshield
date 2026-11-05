import { useEffect, useState } from 'react'

/** Phone width: the Evaluate pages show their tables as blocks and lists below it (DESIGN.md §7.1, 390 px). */
export const NARROW = '(max-width: 640px)'

export function useNarrow(): boolean {
  const [narrow, setNarrow] = useState(() => typeof window !== 'undefined' && !!window.matchMedia?.(NARROW).matches)
  useEffect(() => {
    const m = window.matchMedia?.(NARROW)
    if (!m) return
    const h = () => setNarrow(m.matches)
    h()
    m.addEventListener('change', h)
    return () => m.removeEventListener('change', h)
  }, [])
  return narrow
}
