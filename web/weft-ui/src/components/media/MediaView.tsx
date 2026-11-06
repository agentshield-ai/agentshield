import { useEffect, useRef, useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, fileFetch, fileUrl } from '../../api/client'
import { fmtBytes } from '../../lib/format'
import { httpUrl, isSameOrigin, safeLinkHref } from '../../lib/safeUrl'
import { SafeMarkdown } from '../SafeMarkdown'
import { dataUrlBytes, dataUrlText, detectMedia, resolveContent, type MediaSpec } from './mediaUtils'
import { useInView, useResolvedUri } from './useResolvedUri'
import './media.css'


/** Text of a media value: inline, a data: URL, or the file content. */
function useMediaText(projectId: string, spec: MediaSpec, enabled: boolean) {
  return useQuery({
    queryKey: ['file-text', projectId, spec.digest ?? spec.dataUrl?.slice(0, 64) ?? spec.text?.slice(0, 64)],
    queryFn: async () => {
      if (spec.text != null) return spec.text
      if (spec.dataUrl) return dataUrlText(spec.dataUrl)
      return api.fileText(projectId, spec.digest!)
    },
    enabled,
    staleTime: Infinity,
  })
}

/**
 * A same-origin blob: URL for a PDF, which the browser will not render from
 * the sandboxed /api/v1/files/{digest} response. The blob is always
 * application/pdf, whatever mimetype was recorded: a blob of the recorded
 * type (text/html, image/svg+xml) would be a document in Weft's origin.
 */
function usePdfBlobUrl(projectId: string, spec: MediaSpec, enabled: boolean) {
  const [url, setUrl] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    if (!enabled) return
    let revoked = false
    let made: string | null = null
    ;(async () => {
      try {
        const bytes: BlobPart = spec.dataUrl ? dataUrlBytes(spec.dataUrl) : await (await fileFetch(projectId, spec.digest!)).arrayBuffer()
        made = URL.createObjectURL(new Blob([bytes], { type: 'application/pdf' }))
        if (!revoked) setUrl(made)
      } catch (e) {
        if (!revoked) setError(e instanceof Error ? e.message : String(e))
      }
    })()
    return () => {
      revoked = true
      if (made) URL.revokeObjectURL(made)
    }
  }, [enabled, projectId, spec.digest, spec.dataUrl])
  return { url, error }
}

/** The src of an inline image, audio or video (detectMedia checked the type), or of a download. */
function srcOf(projectId: string, spec: MediaSpec): string | undefined {
  if (spec.dataUrl) return spec.dataUrl
  if (spec.digest) return fileUrl(projectId, spec.digest)
  return undefined
}

/** Save bytes as a file: an application/octet-stream blob, never opened by the browser. */
function saveBytes(bytes: BlobPart, filename: string) {
  const url = URL.createObjectURL(new Blob([bytes], { type: 'application/octet-stream' }))
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

/**
 * Download link. Inline data is saved as an application/octet-stream blob on
 * click; a stored file comes from /api/v1/files, which is sandboxed (CSP
 * sandbox, nosniff) and fetched with the download attribute.
 */
function DownloadLink({ projectId, spec }: { projectId: string; spec: MediaSpec }) {
  const name = spec.filename ?? `${spec.kind === 'file' ? 'download' : spec.kind}`
  if (spec.dataUrl) {
    const data = spec.dataUrl
    return (
      <button type="button" className="media-dl" onClick={() => saveBytes(dataUrlBytes(data), name)}>
        Download
      </button>
    )
  }
  if (!spec.digest) return null
  return (
    <a href={fileUrl(projectId, spec.digest)} download={name} className="media-dl" rel="noreferrer noopener">
      Download
    </a>
  )
}

function Caption({ projectId, spec }: { projectId: string; spec: MediaSpec }) {
  const label = spec.filename ?? spec.mimetype ?? spec.kind
  return (
    <div className="media-caption">
      <span className="truncate" title={label}>
        {label}
      </span>
      {spec.filename && spec.mimetype && <span className="faint">{spec.mimetype}</span>}
      {spec.size != null && <span className="faint">{fmtBytes(spec.size)}</span>}
      <DownloadLink projectId={projectId} spec={spec} />
    </div>
  )
}

function Rendered({ projectId, spec }: { projectId: string; spec: MediaSpec }) {
  const textKind = spec.kind === 'html' || spec.kind === 'markdown' || spec.kind === 'text'
  const text = useMediaText(projectId, spec, textKind)
  const blob = usePdfBlobUrl(projectId, spec, spec.kind === 'pdf')
  const src = srcOf(projectId, spec)
  switch (spec.kind) {
    case 'image':
      return <img src={src} alt={spec.filename ?? 'image'} className="media-img" loading="lazy" />
    case 'audio':
      return <audio controls src={src} preload="metadata" />
    case 'video':
      return <video controls src={src} className="media-video" preload="metadata" />
    case 'pdf':
      if (blob.error) return <div className="error-box">{blob.error}</div>
      if (!blob.url) return <div className="media-frame media-loading faint small">Loading PDF…</div>
      // An application/pdf blob in <object type="application/pdf">: the
      // browser's PDF viewer or nothing. Not a sandboxed iframe: Chrome
      // refuses to show PDFs in one (sandboxed frames get no PDF viewer).
      return (
        <object data={blob.url} type="application/pdf" className="media-frame" aria-label={spec.filename ?? 'PDF document'}>
          <a href={blob.url} target="_blank" rel="noreferrer noopener">
            Open PDF
          </a>
        </object>
      )
    case 'html':
      if (text.isLoading) return <div className="faint small">Loading…</div>
      // sandbox="" : no scripts, forms, popups or same-origin access.
      return <iframe title={spec.filename ?? 'HTML content'} sandbox="" srcDoc={text.data ?? ''} className="media-frame media-html" referrerPolicy="no-referrer" />
    case 'markdown':
      if (text.isLoading) return <div className="faint small">Loading…</div>
      return (
        <div className="markdown media-md">
          <SafeMarkdown>{text.data ?? ''}</SafeMarkdown>
        </div>
      )
    case 'text':
      if (text.isLoading) return <div className="faint small">Loading…</div>
      return <pre className="code-block media-text">{(text.data ?? '').slice(0, 20000)}</pre>
    default:
      return null
  }
}

/** Legacy Content (compat/legacyMedia.ts): read metadata.json for the mimetype, then render. */
function ClientContent({ projectId, spec }: { projectId: string; spec: MediaSpec }) {
  const meta = useQuery({
    queryKey: ['file-text', projectId, spec.metaDigest],
    queryFn: () => api.fileText(projectId, spec.metaDigest!),
    enabled: !!spec.metaDigest,
    staleTime: Infinity,
  })
  if (meta.isLoading) return <div className="faint small">Loading…</div>
  let parsed: unknown = {}
  try {
    parsed = meta.data ? JSON.parse(meta.data) : {}
  } catch {
    parsed = {}
  }
  return <MediaBlock projectId={projectId} spec={resolveContent(spec, parsed)} />
}

/**
 * A PDF by URL. One on this server is fetched into an application/pdf blob
 * and shown inline. One on another host is a link that opens in a new tab:
 * the page's Content-Security-Policy (internal/weft/webui) lets the UI fetch
 * and embed only from this server and blob: URLs, and embedding the URL
 * itself would show whatever document that host returns.
 */
function ExternalPdf({ url, spec }: { url: string; spec: MediaSpec }) {
  const u = httpUrl(url)
  const local = !!u && isSameOrigin(u)
  const [blobUrl, setBlobUrl] = useState<string | null>(null)
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    if (!local) return
    let revoked = false
    let made: string | null = null
    fetch(url)
      .then((r) => (r.ok ? r.blob() : Promise.reject(new Error(String(r.status)))))
      .then((b) => {
        made = URL.createObjectURL(new Blob([b], { type: 'application/pdf' }))
        if (!revoked) setBlobUrl(made)
      })
      .catch(() => !revoked && setFailed(true))
    return () => {
      revoked = true
      if (made) URL.revokeObjectURL(made)
    }
  }, [url, local])
  const href = safeLinkHref(url)
  if (local && !blobUrl && !failed) return <div className="media-frame media-loading faint small">Loading PDF…</div>
  if (!blobUrl)
    return (
      <div className="media-ref-fallback faint small">
        {href ? (
          <a href={href} target="_blank" rel="noreferrer noopener">
            Open PDF{spec.filename ? ` ${spec.filename}` : ''}
          </a>
        ) : (
          'PDF not available'
        )}
        {u && !local && <span>on {u.host}, opens in a new tab</span>}
      </div>
    )
  return (
    <object data={blobUrl} type="application/pdf" className="media-frame" aria-label={spec.filename ?? 'PDF document'}>
      <a href={blobUrl} target="_blank" rel="noreferrer noopener">
        Open PDF
      </a>
    </object>
  )
}

/** The fallback for a URI that cannot be shown: the URI as text and why. */
function UriFallback({ uri, reason, href: rawHref }: { uri: string; reason: string; href?: string }) {
  const href = safeLinkHref(rawHref)
  return (
    <div className="media media-ref-fallback">
      <span className="mono media-uri">{uri}</span>
      <span className="faint small">
        {reason}
        {href && (
          <>
            {' · '}
            <a href={href} target="_blank" rel="noreferrer noopener">
              open
            </a>
          </>
        )}
      </span>
    </div>
  )
}

const LOAD_FAILED = 'could not load: the object may be missing, or the bucket refuses this browser'

/**
 * Media stored outside Weft (s3://, gs://, https://). Images, audio, video
 * and PDFs are resolved lazily, once scrolled into view, through
 * /api/v1/files/resolve (presigned URLs are cached until they expire) and render
 * inline; other files show the URI with an on-demand download link. When the
 * URI cannot be resolved (bucket not registered, no access) the URI shows as
 * text with the reason.
 */
function ExternalMedia({ projectId, spec }: { projectId: string; spec: MediaSpec }) {
  const inline = spec.kind === 'image' || spec.kind === 'audio' || spec.kind === 'video' || spec.kind === 'pdf'
  if (!inline) return <ExternalFile projectId={projectId} spec={spec} />
  // An http(s) URL on another host is whatever the telemetry named: it is
  // fetched only when the viewer asks. Bucket URIs (s3://, gs://) resolve to
  // presigned URLs of a bucket registered for the project and load as before.
  const remote = httpUrl(spec.uri)
  if (remote && !isSameOrigin(remote)) return <RemoteMedia projectId={projectId} spec={spec} host={remote.host} />
  return <ExternalInline projectId={projectId} spec={spec} />
}

const KIND_LABEL: Partial<Record<MediaSpec['kind'], string>> = { image: 'Image', audio: 'Audio', video: 'Video', pdf: 'PDF' }

/** Media at an http(s) URL on another host: a button "Image from <host>" until clicked. */
function RemoteMedia({ projectId, spec, host }: { projectId: string; spec: MediaSpec; host: string }) {
  const [load, setLoad] = useState(false)
  if (load) return <ExternalInline projectId={projectId} spec={spec} />
  return (
    <div className="media media-ref-pending">
      <span className="mono media-uri faint">{spec.uri}</span>
      <span>
        <button type="button" className="btn ghost sm media-remote-image" onClick={() => setLoad(true)} title={`Load ${spec.uri}`}>
          {KIND_LABEL[spec.kind] ?? 'File'} from {host}
        </button>
      </span>
    </div>
  )
}

/**
 * Any other file in a bucket (a dataset path, a JSON file): the URI as text
 * and a download link resolved on demand, so values that merely name an
 * object do not each cost a presign request (or a refusal) on page load.
 */
function ExternalFile({ projectId, spec }: { projectId: string; spec: MediaSpec }) {
  const uri = spec.uri!
  const [wanted, setWanted] = useState(false)
  const { resolved, loading } = useResolvedUri(projectId, uri, wanted)
  return (
    <div className="media media-ref-fallback">
      <span className="mono media-uri">{uri}</span>
      <span className="faint small row" style={{ gap: 8 }}>
        {spec.mimetype && <span>{spec.mimetype}</span>}
        {!wanted ? (
          <button className="btn ghost sm" onClick={() => setWanted(true)} title="Presign a download URL for this object">
            Get download link
          </button>
        ) : loading ? (
          <span>resolving…</span>
        ) : resolved?.ok && safeLinkHref(resolved.url) ? (
          <a href={safeLinkHref(resolved.url)!} target="_blank" rel="noreferrer noopener" download={spec.filename ?? true}>
            Download{spec.filename ? ` ${spec.filename}` : ''}
          </a>
        ) : (
          <span>not available: {resolved?.ok ? 'not an http(s) URL' : resolved?.reason}</span>
        )}
      </span>
    </div>
  )
}

function ExternalInline({ projectId, spec }: { projectId: string; spec: MediaSpec }) {
  const uri = spec.uri!
  const box = useRef<HTMLDivElement>(null)
  const visible = useInView(box)
  const { resolved, retry } = useResolvedUri(projectId, uri, visible)
  const [loadErrors, setLoadErrors] = useState(0)
  const onError = () => {
    // The first failure may be an expired presigned URL: resolve again once.
    if (loadErrors === 0) retry()
    setLoadErrors((n) => n + 1)
  }
  const label = spec.filename ?? uri
  if (!resolved)
    return (
      <div ref={box} className="media media-ref-pending">
        <span className="mono media-uri faint">{uri}</span>
        {visible && <span className="faint small">loading…</span>}
      </div>
    )
  if (!resolved.ok) return <UriFallback uri={uri} reason={`not rendered: ${resolved.reason}`} />
  const url = safeLinkHref(resolved.url)
  if (!url) return <UriFallback uri={uri} reason="not rendered: not an http(s) URL" />
  if (loadErrors > 1) return <UriFallback uri={uri} reason={LOAD_FAILED} href={url} />
  let body: ReactNode
  switch (spec.kind) {
    case 'image':
      body = <img src={url} alt={spec.filename ?? 'image'} className="media-img" onError={onError} referrerPolicy="no-referrer" />
      break
    case 'audio':
      body = <audio controls src={url} preload="metadata" onError={onError} />
      break
    case 'video':
      body = <video controls src={url} className="media-video" preload="metadata" onError={onError} />
      break
    default:
      body = <ExternalPdf url={url} spec={spec} />
  }
  return (
    <div className={`media media-${spec.kind}`}>
      {body}
      <div className="media-caption">
        <span className="truncate mono" title={uri}>
          {label}
        </span>
        {spec.mimetype && <span className="faint">{spec.mimetype}</span>}
        <a href={url} className="media-dl" target="_blank" rel="noreferrer noopener" title={uri}>
          Open
        </a>
      </div>
    </div>
  )
}

function MediaBlock({ projectId, spec }: { projectId: string; spec: MediaSpec }) {
  if (spec.kind === 'content') return <ClientContent projectId={projectId} spec={spec} />
  if (spec.uri && !spec.digest && !spec.dataUrl) return <ExternalMedia key={spec.uri} projectId={projectId} spec={spec} />
  const showCaption = spec.kind !== 'image' || !!spec.filename
  return (
    <div className={`media media-${spec.kind}`}>
      <Rendered projectId={projectId} spec={spec} />
      {showCaption && <Caption projectId={projectId} spec={spec} />}
    </div>
  )
}

export function MediaView({ value, projectId }: { value: unknown; projectId: string }) {
  const spec = detectMedia(value)
  if (!spec) return null
  return <MediaBlock projectId={projectId} spec={spec} />
}

export function isMedia(v: unknown): boolean {
  return detectMedia(v) != null
}
