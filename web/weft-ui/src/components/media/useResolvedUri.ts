import { useCallback, useEffect, useRef, useState, type RefObject } from 'react'
import { mediaApi } from '../../api/media'
import { UriResolver, type Resolved } from './uriResolver'

/** The app-wide resolver; the Buckets settings tab invalidates it after changes. */
export const uriResolver = new UriResolver((projectId, uris) => mediaApi.resolveUrls(projectId, uris))

/**
 * True once the element has come within `margin` of the viewport. Stays true
 * afterwards. Without IntersectionObserver (old browsers, tests) it is true
 * immediately.
 */
export function useInView<T extends Element>(ref: RefObject<T | null>, margin = '200px'): boolean {
  const [seen, setSeen] = useState(() => typeof IntersectionObserver === 'undefined')
  useEffect(() => {
    if (seen) return
    const el = ref.current
    if (!el) return
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) {
          setSeen(true)
          io.disconnect()
        }
      },
      { rootMargin: margin },
    )
    io.observe(el)
    return () => io.disconnect()
  }, [ref, margin, seen])
  return seen
}

/**
 * Resolve `uri` once `enabled` (visible). Re-resolves when the cached
 * presigned URL is about to expire while the component is mounted, and on
 * `retry()` (for example after the browser failed to load an expired URL).
 */
export function useResolvedUri(projectId: string, uri: string | undefined, enabled: boolean, resolver: UriResolver = uriResolver) {
  const [state, setState] = useState<Resolved | null>(() => (uri ? (resolver.peek(projectId, uri) ?? null) : null))
  const [tick, setTick] = useState(0)
  const alive = useRef(true)
  useEffect(() => {
    alive.current = true
    return () => {
      alive.current = false
    }
  }, [])
  useEffect(() => {
    if (!enabled || !uri) return
    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | null = null
    const cached = resolver.peek(projectId, uri)
    if (cached) setState(cached)
    resolver.resolve(projectId, uri).then((r) => {
      if (cancelled) return
      setState(r)
      const staleAt = resolver.staleAt(projectId, uri)
      if (staleAt != null) {
        // setTimeout is capped near 24.8 days; presigned URLs live at most 7.
        const wait = Math.min(Math.max(staleAt - Date.now(), 1000), 2 ** 31 - 1)
        timer = setTimeout(() => setTick((t) => t + 1), wait)
      }
    })
    return () => {
      cancelled = true
      if (timer) clearTimeout(timer)
    }
  }, [projectId, uri, enabled, tick, resolver])
  const retry = useCallback(() => {
    if (!uri) return
    resolver.invalidate(projectId, uri)
    if (alive.current) setTick((t) => t + 1)
  }, [projectId, uri, resolver])
  return { resolved: state, loading: enabled && !!uri && state == null, retry }
}
