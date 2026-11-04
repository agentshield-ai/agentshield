import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation } from '@tanstack/react-query'
import { playgroundApi, type ImageInputs, type ImageResponse } from '../../api/playground'
import type { LLMModelInfo } from '../../api/types'
import { fmtDuration } from '../../lib/format'
import { spanUrl } from '../../lib/urls'
import { ErrorBox } from '../common'
import { imageSrc } from './imageUtils'
import './playground.css'

export const IMAGE_SIZES = ['256x256', '512x512', '1024x1024', '1024x1536', '1536x1024']

/** Image generation through /api/v1/images; each run is a tracked weft.images.generate span. */
export function ImageMode({
  projectId,
  models,
  initial,
  onRun,
}: {
  projectId: string
  models: LLMModelInfo[]
  /** Inputs to load (from history); changes replace the form. */
  initial?: Partial<ImageInputs> & { nonce?: number }
  onRun: (inputs: ImageInputs, res: { response: ImageResponse; callId?: string; latencyMs: number; error?: string }) => void
}) {
  const imageModels = models.filter((m) => m.available && m.provider !== 'anthropic')
  const [model, setModel] = useState(initial?.model ?? '')
  const [prompt, setPrompt] = useState(initial?.prompt ?? '')
  const [size, setSize] = useState(initial?.size ?? '1024x1024')
  const [n, setN] = useState(initial?.n ?? 1)
  useEffect(() => {
    if (!initial) return
    if (initial.model) setModel(initial.model)
    if (initial.prompt != null) setPrompt(initial.prompt)
    if (initial.size) setSize(initial.size)
    if (initial.n) setN(initial.n)
  }, [initial])
  const chosen = model || imageModels.find((m) => /image|dall|gpt-image/i.test(m.id))?.id || imageModels.find((m) => m.provider === 'mock')?.id || imageModels[0]?.id || ''
  const [result, setResult] = useState<{ response: ImageResponse; callId?: string; latencyMs: number } | null>(null)
  const run = useMutation({
    mutationFn: async () => {
      const inputs: ImageInputs = { model: chosen, prompt, n, size }
      const t0 = performance.now()
      const r = await playgroundApi.images(projectId, inputs)
      const out = { response: r.response, callId: r.span_id ?? undefined, latencyMs: performance.now() - t0 }
      onRun(inputs, { ...out, error: r.response?.error })
      return out
    },
    onSuccess: setResult,
  })
  const images = result?.response?.data ?? []
  return (
    <div className="pg-image card">
      <div className="card-body col" style={{ gap: 10 }}>
        <div className="row wrap">
          <label className="small muted row" style={{ gap: 4 }}>
            Model
            <select className="input sm" value={chosen} onChange={(e) => setModel(e.target.value)} aria-label="Image model">
              {imageModels.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.label || m.id}
                </option>
              ))}
              {chosen && !imageModels.some((m) => m.id === chosen) && <option value={chosen}>{chosen}</option>}
            </select>
          </label>
          <label className="small muted row" style={{ gap: 4 }}>
            Size
            <select className="input sm" value={size} onChange={(e) => setSize(e.target.value)} aria-label="Image size">
              {IMAGE_SIZES.map((s) => (
                <option key={s}>{s}</option>
              ))}
            </select>
          </label>
          <label className="small muted row" style={{ gap: 4 }}>
            Images
            <input className="input sm" type="number" min={1} max={4} style={{ width: 56 }} value={n} onChange={(e) => setN(Math.max(1, Math.min(4, Number(e.target.value) || 1)))} aria-label="Number of images" />
          </label>
          <span className="grow" />
          <button className="btn primary sm" disabled={!prompt.trim() || !chosen || run.isPending} onClick={() => run.mutate()}>
            {run.isPending ? 'Generating…' : 'Generate'}
          </button>
        </div>
        <textarea
          className="input"
          rows={3}
          placeholder="Describe the image, e.g. A watercolour lighthouse at dawn"
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && prompt.trim()) {
              e.preventDefault()
              run.mutate()
            }
          }}
          aria-label="Image prompt"
        />
        <ErrorBox error={run.error} />
        {result?.response?.error && <div className="error-box">{result.response.error}</div>}
        {result && (
          <div className="row small muted">
            <span>
              {images.length} image{images.length === 1 ? '' : 's'} · {fmtDuration(result.latencyMs)}
            </span>
            <span className="grow" />
            {result.callId && <Link to={spanUrl(projectId, result.callId)}>Open weft.images.generate span ↗</Link>}
          </div>
        )}
        {images.length > 0 && (
          <div className="pg-image-grid">
            {images.map((im, i) => {
              const src = imageSrc(im)
              return (
                <figure key={i} className="pg-image-item">
                  {src ? <img src={src} alt={im.revised_prompt || prompt} loading="lazy" /> : <span className="faint small">No image data</span>}
                  {im.revised_prompt && <figcaption className="small muted">{im.revised_prompt}</figcaption>}
                  {src && (
                    <a className="small" href={src} download={`image-${i + 1}.png`}>
                      Download
                    </a>
                  )}
                </figure>
              )
            })}
          </div>
        )}
      </div>
    </div>
  )
}
