// Media values in the encoding of the official weave client, which only
// reaches Weft through the compatibility adapter (docs/weft/COMPATIBILITY.md).
// Values it stored keep that encoding, so the UI renders them:
//
//   {_type: "CustomWeaveType", weave_type: {type}, files: {...}, val?}
//   PIL.Image.Image → image.<ext>, wave.Wave_read / Audio → audio.<ext>, VideoFileClip → video.<ext>,
//   *.Content → content + metadata.json, rich Markdown → val.markup | markup.md
import { kindFromMime, mimeFromFilename, type MediaKind, type MediaSpec } from './mediaUtils'

/** The client's custom-type tag. */
export const CLIENT_CUSTOM_TYPE = 'CustomWeaveType'

/** A media spec for a client custom type, null for one that is not media,
 * undefined for any other value. */
export function detectClientType(o: Record<string, unknown>): MediaSpec | null | undefined {
  if (o._type !== CLIENT_CUSTOM_TYPE) return undefined
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

