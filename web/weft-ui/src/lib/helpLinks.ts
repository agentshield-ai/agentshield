// Heading anchors and link targets for the in-app guides (pages/help).

/** A heading's anchor, as the guides' cross-references spell it: lower case,
 * punctuation dropped, spaces as hyphens; repeats get -1, -2, ... */
export function slugger(): (text: string) => string {
  const seen = new Map<string, number>()
  return (text: string) => {
    const base = text
      .toLowerCase()
      .trim()
      .replace(/[^\p{L}\p{N}\s_-]/gu, '')
      .replace(/\s/g, '-')
    const n = seen.get(base)
    seen.set(base, (n ?? -1) + 1)
    return n === undefined ? base : `${base}-${n + 1}`
  }
}

export type GuideLink = { kind: 'guide'; to: string } | { kind: 'anchor'; hash: string } | { kind: 'external'; href: string } | { kind: 'text' }

/** Where a link inside a guide goes: another guide, an anchor on this page,
 * an external URL, or nowhere (repository files, which are not served). */
export function guideLink(href: string | undefined): GuideLink {
  if (!href) return { kind: 'text' }
  if (href.startsWith('#')) return { kind: 'anchor', hash: href.slice(1) }
  if (/^(https?:|mailto:)/i.test(href)) return { kind: 'external', href }
  const m = /^(?:\.\/)?([A-Za-z0-9_-]+\.md)(#.*)?$/.exec(href)
  if (m) return { kind: 'guide', to: `/help/${m[1]}${m[2] ?? ''}` }
  return { kind: 'text' }
}
