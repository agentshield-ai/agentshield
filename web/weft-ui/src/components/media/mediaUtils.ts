// Detects media values in call inputs/outputs and object values (unit tested).
//
// Recognised shapes:
// - data: URLs (image, audio, video, pdf, html, markdown, text)
// - {_type: Image|Audio|Video|Pdf|Html|Markdown|File, file_digest, mime_type|mimetype}
// - {_type: "Content", mimetype, file_digest | data (base64), filename}  (Weft SDKs)
// - {_type: "CustomWeaveType", weave_type: {type}, files: {...}, val?}  (official weave client):
//   PIL.Image.Image → image.<ext>, wave.Wave_read / Audio → audio.<ext>, VideoFileClip → video.<ext>,
//   weave.type_wrappers.Content.content.Content → content + metadata.json, rich Markdown → val.markup | markup.md

export type MediaKind = 'image' | 'audio' | 'video' | 'pdf' | 'html' | 'markdown' | 'text' | 'file' | 'content'

export interface MediaSpec {
  kind: MediaKind
  mimetype?: string
  filename?: string
  size?: number
  /** One of: */
  digest?: string
  dataUrl?: string
  text?: string
  /** For weave Content: the metadata.json digest, read to learn the mimetype. */
  metaDigest?: string
}

const EXT_MIME: Record<string, string> = {
  png: 'image/png',
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  gif: 'image/gif',
  webp: 'image/webp',
  bmp: 'image/bmp',
  svg: 'image/svg+xml',
  tif: 'image/tiff',
  tiff: 'image/tiff',
  wav: 'audio/wav',
  mp3: 'audio/mpeg',
  ogg: 'audio/ogg',
  flac: 'audio/flac',
  m4a: 'audio/mp4',
  mp4: 'video/mp4',
  webm: 'video/webm',
  mov: 'video/quicktime',
  gif_video: 'image/gif',
  pdf: 'application/pdf',
  html: 'text/html',
  htm: 'text/html',
  md: 'text/markdown',
  markdown: 'text/markdown',
  txt: 'text/plain',
  json: 'application/json',
  csv: 'text/csv',
}

export function mimeFromFilename(name?: string): string | undefined {
  if (!name) return undefined
  const i = name.lastIndexOf('.')
  if (i < 0) return undefined
  return EXT_MIME[name.slice(i + 1).toLowerCase()]
}

export function kindFromMime(mime?: string): MediaKind {
  const m = (mime ?? '').toLowerCase().split(';')[0].trim()
  if (m.startsWith('image/')) return 'image'
  if (m.startsWith('audio/')) return 'audio'
  if (m.startsWith('video/')) return 'video'
  if (m === 'application/pdf') return 'pdf'
  if (m === 'text/html' || m === 'application/xhtml+xml') return 'html'
  if (m === 'text/markdown' || m === 'text/x-markdown') return 'markdown'
  if (m.startsWith('text/') || m === 'application/json' || m === 'application/xml') return 'text'
  return 'file'
}

const DATA_URL_RE = /^data:([\w.+-]+\/[\w.+-]+)(;[\w=.-]+)*;base64,/

const TYPE_KIND: Record<string, MediaKind> = {
  Image: 'image',
  Audio: 'audio',
  Video: 'video',
  Pdf: 'pdf',
  PDF: 'pdf',
  Html: 'html',
  HTML: 'html',
  Markdown: 'markdown',
  File: 'file',
}

function str(v: unknown): string | undefined {
  return typeof v === 'string' && v ? v : undefined
}

/** Returns how to render `v`, or null when it is not media. */
export function detectMedia(v: unknown): MediaSpec | null {
  if (typeof v === 'string') {
    const m = DATA_URL_RE.exec(v)
    if (!m) return null
    const kind = kindFromMime(m[1])
    // Plain data: text is rarely meant as media; only render known media kinds.
    if (kind === 'file') return null
    return { kind, mimetype: m[1], dataUrl: v }
  }
  if (!v || typeof v !== 'object' || Array.isArray(v)) return null
  const o = v as Record<string, unknown>
  const type = str(o._type)
  if (type === 'CustomWeaveType') return detectWeaveCustom(o)
  if (!type) return null
  const mime = str(o.mimetype) ?? str(o.mime_type) ?? str(o.content_type)
  const filename = str(o.filename) ?? str(o.name)
  const size = typeof o.size === 'number' ? o.size : undefined
  const digest = str(o.file_digest) ?? str(o.digest)
  if (type === 'Content') {
    const mt = mime ?? mimeFromFilename(filename) ?? 'application/octet-stream'
    const kind = kindFromMime(mt)
    if (digest) return { kind, mimetype: mt, filename, size, digest }
    const data = str(o.data)
    if (data) return { kind, mimetype: mt, filename, size, dataUrl: `data:${mt};base64,${data}` }
    return null
  }
  const known = TYPE_KIND[type]
  if (!known) return null
  // Inline text for Markdown/Html values.
  const inline = str(o.markup) ?? str(o.content) ?? str(o.html) ?? str(o.text)
  if ((known === 'markdown' || known === 'html') && inline && !digest) return { kind: known, text: inline, mimetype: mime, filename }
  if (digest) {
    const mt = mime ?? mimeFromFilename(filename)
    const kind = known === 'file' ? kindFromMime(mt) : known
    return { kind, mimetype: mt, filename, size, digest }
  }
  const data = str(o.data)
  if (data && mime) return { kind: known === 'file' ? kindFromMime(mime) : known, mimetype: mime, filename, size, dataUrl: data.startsWith('data:') ? data : `data:${mime};base64,${data}` }
  return null
}

function detectWeaveCustom(o: Record<string, unknown>): MediaSpec | null {
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

/** Resolve a weave Content spec once its metadata.json has been read. */
export function resolveContent(spec: MediaSpec, metadata: unknown): MediaSpec {
  const m = (metadata && typeof metadata === 'object' ? metadata : {}) as Record<string, unknown>
  const mt = str(m.mimetype) ?? mimeFromFilename(str(m.filename)) ?? 'application/octet-stream'
  return { ...spec, kind: kindFromMime(mt), mimetype: mt, filename: str(m.filename), size: typeof m.size === 'number' ? m.size : undefined }
}

/** Decode a base64 data URL into text (UTF-8). */
export function dataUrlText(url: string): string {
  const i = url.indexOf(',')
  const b64 = url.slice(i + 1)
  const bin = atob(b64)
  const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0))
  return new TextDecoder().decode(bytes)
}

export function isMediaValue(v: unknown): boolean {
  return detectMedia(v) != null
}
