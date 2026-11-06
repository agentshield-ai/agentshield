// URLs that come from recorded data (span inputs and outputs, saved objects,
// datasets, feedback) are written by whoever can send telemetry, so they are
// untrusted. Only these may reach an href or src (unit tested in safeUrl.test.ts):
// - links: http(s) URLs (relative ones resolve against the page);
// - images: http(s) URLs and data:image/* URLs (an <img> never runs script,
//   SVG included).
// Anything else (javascript:, data:text/html, vbscript:, blob:, file:, ...)
// renders as text. Remote images (another host) are not fetched until the
// viewer asks for them (components/media/RecordedImage.tsx), so opening a
// session never contacts a host named in its data.

/** data:image/<subtype>[;params][;base64],... */
const DATA_IMAGE_RE = /^data:image\/[a-z0-9.+-]+(?:;[a-z0-9=.+-]+)*,/i

function pageBase(): string {
  try {
    const href = globalThis.location?.href
    if (href) return href
  } catch {
    // no location (tests, workers)
  }
  return 'http://localhost/'
}

/** The parsed URL when `raw` is an http(s) URL, absolute or relative to `base` (the page); else null. */
export function httpUrl(raw: unknown, base: string = pageBase()): URL | null {
  if (typeof raw !== 'string') return null
  const s = raw.trim()
  if (!s) return null
  let u: URL
  try {
    u = new URL(s, base)
  } catch {
    return null
  }
  return u.protocol === 'http:' || u.protocol === 'https:' ? u : null
}

/** True for a data:image/* URL. */
export function isDataImageUrl(raw: unknown): raw is string {
  return typeof raw === 'string' && DATA_IMAGE_RE.test(raw.trim())
}

/**
 * The href for a link taken from recorded data: the normalised http(s) URL,
 * or null (render the link as text).
 */
export function safeLinkHref(raw: unknown, base: string = pageBase()): string | null {
  return httpUrl(raw, base)?.href ?? null
}

/** The src for an image taken from recorded data: a data:image/* or http(s) URL, or null. */
export function safeImageSrc(raw: unknown, base: string = pageBase()): string | null {
  if (isDataImageUrl(raw)) return raw.trim()
  return httpUrl(raw, base)?.href ?? null
}

/** True when `u` has the page's origin. */
export function isSameOrigin(u: URL, base: string = pageBase()): boolean {
  try {
    return u.origin === new URL(base).origin
  } catch {
    return false
  }
}

/** How an image URL from recorded data is shown. */
export type ImageSource =
  /** data:image/* or this server's own URL: shown inline. */
  | { kind: 'inline'; src: string }
  /** An http(s) URL on another host: shown only when the viewer asks. */
  | { kind: 'remote'; src: string; host: string }
  /** Any other scheme: the URL is shown as text, never used. */
  | { kind: 'unsafe'; text: string }

export function classifyImageUrl(raw: unknown, base: string = pageBase()): ImageSource {
  if (isDataImageUrl(raw)) return { kind: 'inline', src: raw.trim() }
  const u = httpUrl(raw, base)
  if (!u) return { kind: 'unsafe', text: typeof raw === 'string' ? raw : '' }
  if (isSameOrigin(u, base)) return { kind: 'inline', src: u.href }
  return { kind: 'remote', src: u.href, host: u.host }
}

/** A long URL shortened for display as text. */
export function shortUrl(s: string, max = 120): string {
  return s.length > max ? s.slice(0, max - 1) + '…' : s
}
