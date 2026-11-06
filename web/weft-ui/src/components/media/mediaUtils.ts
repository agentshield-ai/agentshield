import { decodeLegacyMedia, isLegacyMedia } from '../../compat/legacyMedia'
// Detects media values in call inputs/outputs and object values (unit tested).
//
// Recognised shapes:
// - data: URLs (image, audio, video, pdf, html, markdown, text)
// - {_type: Image|Audio|Video|Pdf|Html|Markdown|File, file_digest, mime_type|mimetype}
// - {_type: "Content", mimetype, file_digest | data (base64), filename}  (Weft SDKs)
// - media stored by another product's client through the compatibility adapter, decoded
//   only by compat/legacyMedia.ts
// - a plain string that is exactly an s3:// or gs:// object URI (reference buckets, "bring your
//   own bucket"): the whole value must be the URI, so prose mentioning one stays text.
//   The kind comes from the file extension; other extensions render as a download link.
// - GenAI message parts: {type: "uri", uri, mime_type?, modality?} (the SDKs' attach_media(uri=…)),
//   {type: "blob", content (base64), mime_type}, Gemini {inlineData: {mimeType, data}} and
//   {fileData: {mimeType, fileUri}}.

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
  /** For legacy Content (compat/legacyMedia.ts): the metadata.json digest, read to learn the mimetype. */
  metaDigest?: string
  /** External storage (s3://, gs://, https://) resolved through /api/v1/files/resolve. */
  uri?: string
}

const FILE_URI_RE = /^weft-file:\/\/(?:[^/]+\/)*([A-Za-z0-9_-]{8,})$/
const EXTERNAL_URI_RE = /^(?:s3|gs|az|https?):\/\/\S+$/i

/** A media value's uri: a file digest for weft-file://, else an external uri. */
export function mediaUri(uri: string | undefined): { digest?: string; uri?: string } | null {
  if (!uri) return null
  const f = FILE_URI_RE.exec(uri.trim())
  if (f) return { digest: f[1] }
  if (EXTERNAL_URI_RE.test(uri.trim())) return { uri: uri.trim() }
  return null
}

// A whole value that is an object URI in a customer bucket: scheme, bucket
// name (S3/GCS naming rules), and a non-empty key that does not end in "/"
// (a "folder" is not an object). No whitespace anywhere.
const REFERENCE_URI_RE = /^(?:s3|gs):\/\/[a-z0-9][a-z0-9._-]{1,220}[a-z0-9]\/[^\s]*[^\s/]$/

/** True when `v` is exactly one s3:// or gs:// object URI (surrounding whitespace allowed). */
export function isReferenceUri(v: unknown): boolean {
  return typeof v === 'string' && v.length <= 2048 && REFERENCE_URI_RE.test(v.trim())
}

/**
 * Kinds rendered inline from a bucket URI: images, video, audio and PDF.
 * Everything else is a download link.
 */
const INLINE_REF_KINDS: ReadonlySet<MediaKind> = new Set<MediaKind>(['image', 'audio', 'video', 'pdf'])

/** The media spec of a reference URI, its kind from the extension (or a known mimetype). */
export function referenceUriSpec(uri: string, mimetype?: string): MediaSpec {
  const u = uri.trim()
  const filename = uriFilename(u)
  const mt = mimetype ?? mimeFromFilename(filename)
  const k = kindFromMime(mt)
  return { kind: INLINE_REF_KINDS.has(k) ? k : 'file', mimetype: mt, filename, uri: u }
}

function uriFilename(uri: string): string | undefined {
  const path = uri.split(/[?#]/)[0]
  const name = path.slice(path.lastIndexOf('/') + 1)
  if (!name || !name.includes('.')) return undefined
  try {
    return decodeURIComponent(name)
  } catch {
    return name
  }
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
  const m = normMime(mime)
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

/** A mimetype without parameters, lowercased. */
export function normMime(mime?: string): string {
  return (mime ?? '').toLowerCase().split(';')[0].trim()
}

/**
 * The spec as it may be rendered. The recorded mimetype is untrusted (anyone
 * who can send telemetry chooses it), so a kind that renders inline must agree
 * with it: a PDF is only ever application/pdf (rendered from an application/pdf
 * blob whatever was recorded), images, audio and video need an image/, audio/
 * or video/ type. Anything else becomes a download ('file', saved as
 * application/octet-stream), so a recorded mimetype can never produce a
 * same-origin document. Html, Markdown and text are rendered from their text
 * (a sandboxed srcdoc iframe, markdown, <pre>) and stay as they are.
 */
export function safeSpec(spec: MediaSpec | null): MediaSpec | null {
  if (!spec) return null
  const mt = normMime(spec.mimetype)
  switch (spec.kind) {
    case 'pdf':
      if (!mt) return { ...spec, mimetype: 'application/pdf' }
      return mt === 'application/pdf' ? spec : { ...spec, kind: 'file' }
    case 'image':
    case 'audio':
    case 'video':
      return !mt || mt.startsWith(`${spec.kind}/`) ? spec : { ...spec, kind: 'file' }
    default:
      return spec
  }
}

/** Returns how to render `v`, or null when it is not media. */
export function detectMedia(v: unknown): MediaSpec | null {
  return safeSpec(detectMediaValue(v))
}

function detectMediaValue(v: unknown): MediaSpec | null {
  if (typeof v === 'string') {
    if (isReferenceUri(v)) return referenceUriSpec(v)
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
  if (isLegacyMedia(o)) return decodeLegacyMedia(o)
  if (!type) return detectPart(o)
  const mime = str(o.mimetype) ?? str(o.mime_type) ?? str(o.content_type)
  const filename = str(o.filename) ?? str(o.name)
  const size = typeof o.size === 'number' ? o.size : undefined
  const ref = mediaUri(str(o.uri) ?? str(o.url))
  const digest = str(o.file_digest) ?? str(o.digest) ?? ref?.digest
  const uri = digest ? undefined : ref?.uri
  if (type === 'Content') {
    const mt = mime ?? mimeFromFilename(filename) ?? (uri ? mimeFromFilename(uriFilename(uri)) : undefined) ?? 'application/octet-stream'
    const kind = kindFromMime(mt)
    if (digest) return { kind, mimetype: mt, filename, size, digest }
    if (uri) return { kind, mimetype: mt, filename: filename ?? uriFilename(uri), size, uri }
    const data = str(o.data)
    if (data) return { kind, mimetype: mt, filename, size, dataUrl: `data:${mt};base64,${data}` }
    return null
  }
  const known = TYPE_KIND[type]
  if (!known) return null
  // Inline text for Markdown/Html values.
  const inline = str(o.markup) ?? str(o.content) ?? str(o.html) ?? str(o.text)
  if ((known === 'markdown' || known === 'html') && inline && !digest) return { kind: known, text: inline, mimetype: mime, filename }
  if (digest || uri) {
    const mt = mime ?? mimeFromFilename(filename) ?? (uri ? mimeFromFilename(uriFilename(uri)) : undefined)
    const kind = known === 'file' ? kindFromMime(mt) : known
    return digest ? { kind, mimetype: mt, filename, size, digest } : { kind, mimetype: mt, filename: filename ?? uriFilename(uri!), size, uri }
  }
  const data = str(o.data)
  if (data && mime) return { kind: known === 'file' ? kindFromMime(mime) : known, mimetype: mime, filename, size, dataUrl: data.startsWith('data:') ? data : `data:${mime};base64,${data}` }
  return null
}

const MODALITY_KIND: Record<string, MediaKind> = { image: 'image', audio: 'audio', video: 'video' }

/** GenAI / Gemini message parts that carry media without a `_type`. */
function detectPart(o: Record<string, unknown>): MediaSpec | null {
  const t = str(o.type)
  if (t === 'uri') {
    const raw = str(o.uri) ?? str(o.url)
    const ref = mediaUri(raw)
    if (!ref) return null
    const mt = str(o.mime_type) ?? str(o.mimetype)
    const modal = MODALITY_KIND[str(o.modality)?.toLowerCase() ?? '']
    if (ref.digest) {
      const kind = mt ? kindFromMime(mt) : (modal ?? 'file')
      return { kind, mimetype: mt, digest: ref.digest }
    }
    const spec = referenceUriSpec(ref.uri!, mt)
    // An explicit modality wins over an unknown extension.
    if (spec.kind === 'file' && modal) return { ...spec, kind: modal }
    return spec
  }
  if (t === 'blob') {
    const data = str(o.content) ?? str(o.data)
    const mt = str(o.mime_type) ?? str(o.mimetype)
    if (!data || !mt) return null
    const kind = kindFromMime(mt)
    if (kind === 'file') return null
    return { kind, mimetype: mt, dataUrl: data.startsWith('data:') ? data : `data:${mt};base64,${data}` }
  }
  if (t) return null
  const inline = o.inlineData && typeof o.inlineData === 'object' ? (o.inlineData as Record<string, unknown>) : null
  if (inline) {
    const mt = str(inline.mimeType) ?? str(inline.mime_type)
    const data = str(inline.data)
    if (!mt || !data) return null
    const kind = kindFromMime(mt)
    return kind === 'file' ? null : { kind, mimetype: mt, dataUrl: `data:${mt};base64,${data}` }
  }
  const fd = o.fileData && typeof o.fileData === 'object' ? (o.fileData as Record<string, unknown>) : null
  if (fd) {
    const ref = mediaUri(str(fd.fileUri) ?? str(fd.file_uri))
    if (!ref?.uri) return null
    return referenceUriSpec(ref.uri, str(fd.mimeType) ?? str(fd.mime_type))
  }
  return null
}

/** Resolve a legacy Content spec (compat/legacyMedia.ts) once its metadata.json has been read. */
export function resolveContent(spec: MediaSpec, metadata: unknown): MediaSpec {
  const m = (metadata && typeof metadata === 'object' ? metadata : {}) as Record<string, unknown>
  const mt = str(m.mimetype) ?? mimeFromFilename(str(m.filename)) ?? 'application/octet-stream'
  return safeSpec({ ...spec, kind: kindFromMime(mt), mimetype: mt, filename: str(m.filename), size: typeof m.size === 'number' ? m.size : undefined })!
}

/** The bytes of a data URL (base64 or percent-encoded). */
export function dataUrlBytes(url: string): Uint8Array<ArrayBuffer> {
  const i = url.indexOf(',')
  const head = url.slice(0, Math.max(i, 0))
  const body = url.slice(i + 1)
  if (/;base64$/i.test(head)) {
    const bin = atob(body.replace(/\s+/g, ''))
    return Uint8Array.from(bin, (c) => c.charCodeAt(0))
  }
  let text = body
  try {
    text = decodeURIComponent(body)
  } catch {
    // keep as is
  }
  return new TextEncoder().encode(text) as Uint8Array<ArrayBuffer>
}

/** Decode a data URL into text (UTF-8). */
export function dataUrlText(url: string): string {
  return new TextDecoder().decode(dataUrlBytes(url))
}

export function isMediaValue(v: unknown): boolean {
  return detectMedia(v) != null
}
