import { useEffect, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { useQuery } from '@tanstack/react-query'
import { api, fileUrl, getApiKey } from '../../api/client'
import { mediaApi } from '../../api/media'
import { fmtBytes } from '../../lib/format'
import { dataUrlText, detectMedia, resolveContent, type MediaSpec } from './mediaUtils'
import './media.css'

const API_BASE: string = (import.meta.env.VITE_WEFT_API as string | undefined) ?? ''

/** Text of a media value: inline, a data: URL, or the file content. */
function useMediaText(projectId: string, spec: MediaSpec, enabled: boolean) {
  return useQuery({
    queryKey: ['file-text', projectId, spec.digest ?? spec.dataUrl?.slice(0, 64) ?? spec.text?.slice(0, 64)],
    queryFn: async () => {
      if (spec.text != null) return spec.text
      if (spec.dataUrl) return dataUrlText(spec.dataUrl)
      return api.fileContentText(projectId, spec.digest!)
    },
    enabled,
    staleTime: Infinity,
  })
}

/**
 * A same-origin blob: URL with the right type. Used for PDFs, which the
 * browser will not render from the sandboxed /file/content response.
 */
function useBlobUrl(projectId: string, spec: MediaSpec, enabled: boolean) {
  const [url, setUrl] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    if (!enabled) return
    let revoked = false
    let made: string | null = null
    ;(async () => {
      try {
        let blob: Blob
        if (spec.dataUrl) blob = await (await fetch(spec.dataUrl)).blob()
        else {
          const headers: Record<string, string> = { 'Content-Type': 'application/json' }
          const key = getApiKey()
          if (key) headers.Authorization = `Bearer ${key}`
          const res = await fetch(API_BASE + '/file/content', { method: 'POST', headers, body: JSON.stringify({ project_id: projectId, digest: spec.digest }) })
          if (!res.ok) throw new Error(`file: ${res.status}`)
          blob = await res.blob()
        }
        blob = new Blob([blob], { type: spec.mimetype || 'application/pdf' })
        made = URL.createObjectURL(blob)
        if (!revoked) setUrl(made)
      } catch (e) {
        if (!revoked) setError(e instanceof Error ? e.message : String(e))
      }
    })()
    return () => {
      revoked = true
      if (made) URL.revokeObjectURL(made)
    }
  }, [enabled, projectId, spec.digest, spec.dataUrl, spec.mimetype])
  return { url, error }
}

function srcOf(projectId: string, spec: MediaSpec & { resolvedUrl?: string }): string | undefined {
  if (spec.resolvedUrl) return spec.resolvedUrl
  if (spec.dataUrl) return spec.dataUrl
  if (spec.digest) return fileUrl(projectId, spec.digest)
  return undefined
}

function Caption({ projectId, spec }: { projectId: string; spec: MediaSpec }) {
  const src = srcOf(projectId, spec)
  const label = spec.filename ?? spec.mimetype ?? spec.kind
  return (
    <div className="media-caption">
      <span className="truncate" title={label}>
        {label}
      </span>
      {spec.filename && spec.mimetype && <span className="faint">{spec.mimetype}</span>}
      {spec.size != null && <span className="faint">{fmtBytes(spec.size)}</span>}
      {src && (
        <a href={src} download={spec.filename ?? true} className="media-dl" target="_blank" rel="noreferrer noopener">
          Download
        </a>
      )}
    </div>
  )
}

function Rendered({ projectId, spec }: { projectId: string; spec: MediaSpec }) {
  const textKind = spec.kind === 'html' || spec.kind === 'markdown' || spec.kind === 'text'
  const text = useMediaText(projectId, spec, textKind)
  const blob = useBlobUrl(projectId, spec, spec.kind === 'pdf')
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
          <ReactMarkdown remarkPlugins={[remarkGfm]}>{text.data ?? ''}</ReactMarkdown>
        </div>
      )
    case 'text':
      if (text.isLoading) return <div className="faint small">Loading…</div>
      return <pre className="code-block media-text">{(text.data ?? '').slice(0, 20000)}</pre>
    default:
      return null
  }
}

/** Weave Content: read metadata.json for the mimetype, then render. */
function WeaveContent({ projectId, spec }: { projectId: string; spec: MediaSpec }) {
  const meta = useQuery({
    queryKey: ['file-text', projectId, spec.metaDigest],
    queryFn: () => api.fileContentText(projectId, spec.metaDigest!),
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
 * Media stored outside Weft (s3://, gs://, https://): resolve a download URL
 * with /files/resolve_url, render images, audio and video from it, and fall
 * back to a link for everything else or when the uri cannot be resolved.
 */
function ExternalMedia({ projectId, spec }: { projectId: string; spec: MediaSpec }) {
  const q = useQuery({
    queryKey: ['resolve-url', projectId, spec.uri],
    queryFn: () => mediaApi.resolveUrls(projectId, [spec.uri!]).then((r) => r.download_urls[0] ?? null),
    staleTime: 30 * 60_000,
    retry: false,
  })
  const direct = /^https?:\/\//i.test(spec.uri ?? '') ? spec.uri : undefined
  const url = q.data ?? (q.error ? direct : undefined)
  const label = spec.filename ?? spec.uri
  const playable = spec.kind === 'image' || spec.kind === 'audio' || spec.kind === 'video'
  if (q.isLoading) return <div className="faint small">Resolving {label}…</div>
  return (
    <div className={`media media-${spec.kind}`}>
      {url && playable ? <Rendered projectId={projectId} spec={{ ...spec, resolvedUrl: url } as MediaSpec} /> : null}
      <div className="media-caption">
        <span className="truncate mono" title={spec.uri}>
          {label}
        </span>
        {spec.mimetype && <span className="faint">{spec.mimetype}</span>}
        {url ? (
          <a href={url} className="media-dl" target="_blank" rel="noreferrer noopener">
            {playable ? 'Open' : 'Open file'}
          </a>
        ) : (
          <span className="faint small" title={q.error instanceof Error ? q.error.message : undefined}>
            not resolvable
          </span>
        )}
      </div>
    </div>
  )
}

function MediaBlock({ projectId, spec }: { projectId: string; spec: MediaSpec }) {
  if (spec.kind === 'content') return <WeaveContent projectId={projectId} spec={spec} />
  if (spec.uri && !spec.digest && !spec.dataUrl) return <ExternalMedia projectId={projectId} spec={spec} />
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
