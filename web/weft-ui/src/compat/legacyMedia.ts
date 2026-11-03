// COMPATIBILITY ONLY: media in the encoding of the other product's client
// (docs/weft/DESIGN.md §8). That client writes to Weft only through the
// compatibility adapter (--weave-compat, docs/weft/COMPATIBILITY.md), and the
// values it stored keep their encoding, so the UI still has to render them.
// Nothing in Weft writes this encoding: the Weft SDKs use {_type: "Image" |
// "Audio" | "Content" | …} (components/media/mediaUtils.ts). mediaUtils calls
// in here only for a value tagged with LEGACY_MEDIA_TYPE.
//
//   {_type: LEGACY_MEDIA_TYPE, weave_type: {type}, files: {...}, val?}
//   PIL.Image.Image → image.<ext>, wave.Wave_read / Audio → audio.<ext>, VideoFileClip → video.<ext>,
//   *.Content → content + metadata.json, rich Markdown → val.markup | markup.md
import { kindFromMime, mimeFromFilename, type MediaKind, type MediaSpec } from '../components/media/mediaUtils'

/** The tag of that encoding; a value without it never reaches this module. */
export const LEGACY_MEDIA_TYPE = 'CustomWeaveType'

/** True when `o` is a media value in the legacy client encoding. */
export function isLegacyMedia(o: Record<string, unknown>): boolean {
  return o._type === LEGACY_MEDIA_TYPE
}

/** The media spec of a legacy-encoded value, or null when it is not media (an op, a model, …). */
export function decodeLegacyMedia(o: Record<string, unknown>): MediaSpec | null {
  return detectCustomType(o)
}

function detectCustomType(o: Record<string, unknown>): MediaSpec | null {
  const wt = (o.weave_type as { type?: string } | undefined)?.type ?? ''
  const files = (o.files && typeof o.files === 'object' ? o.files : {}) as Record<string, string>
  const val = (o.val && typeof o.val === 'object' ? o.val : {}) as Record<string, unknown>
  const names = Object.keys(files)
  const pick = (prefix: string) => names.find((n) => n.startsWith(prefix))
  if (wt.endsWith('Content.content.Content') || wt.endsWith('.Content')) {
    if (!files.content) return null
    return { kind: 'content', digest: files.content, metaDigest: files['metadata.json'] }
  }
  if (/markdown/i.test(wt)) {
    if (typeof val.markup === 'string') return { kind: 'markdown', text: val.markup }
    if (files['markup.md']) return { kind: 'markdown', digest: files['markup.md'], mimetype: 'text/markdown' }
    return null
  }
  const byPrefix: [RegExp, string, MediaKind][] = [
    [/^PIL\.|Image/, 'image.', 'image'],
    [/wave\.Wave_read|Audio/, 'audio.', 'audio'],
    [/moviepy|Video/, 'video.', 'video'],
  ]
  for (const [re, prefix, kind] of byPrefix) {
    if (!re.test(wt)) continue
    const f = pick(prefix)
    if (f) return { kind, digest: files[f], filename: f, mimetype: mimeFromFilename(f) }
  }
  // Any other single-file custom type with a recognisable extension.
  const media = names.filter((n) => !n.endsWith('.json'))
  if (media.length === 1) {
    const mt = mimeFromFilename(media[0])
    if (mt) return { kind: kindFromMime(mt), digest: files[media[0]], filename: media[0], mimetype: mt }
  }
  return null
}

