import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { isRef } from '../api/refs'
import { MediaView, isMedia } from './media/MediaView'
import { RefChip } from './RefChip'

// Generic renderer for call inputs/outputs and object values.
// - refs render as links
// - media ({_type: Image|Audio}, data: URLs, and strings that are exactly an
//   s3:// or gs:// object URI in a reference bucket) render inline
// - long strings render as text or markdown
// - objects/arrays render as a collapsible tree

// Media detection and rendering (Image, Audio, Video, PDF, Html, Markdown,
// Content, and client custom types, see media/clientTypes.ts) live in
// ./media; re-exported here for existing imports.
export { MediaView, isMedia }

function StringView({ s, markdown }: { s: string; markdown?: boolean }) {
  const [expanded, setExpanded] = useState(false)
  const long = s.length > 600
  const shown = long && !expanded ? s.slice(0, 600) + '…' : s
  return (
    <div>
      {markdown ? (
        <div className="markdown">
          <ReactMarkdown remarkPlugins={[remarkGfm]}>{shown}</ReactMarkdown>
        </div>
      ) : (
        <pre style={{ fontFamily: 'var(--font)', fontSize: 13 }}>{shown}</pre>
      )}
      {long && (
        <button className="btn ghost sm" onClick={() => setExpanded((e) => !e)}>
          {expanded ? 'Show less' : `Show all (${s.length.toLocaleString()} chars)`}
        </button>
      )}
    </div>
  )
}

function Scalar({ v }: { v: unknown }) {
  if (v === null || v === undefined) return <span className="json-null">null</span>
  if (typeof v === 'boolean') return <span className="json-bool">{String(v)}</span>
  if (typeof v === 'number') return <span className="json-number mono">{String(v)}</span>
  return <span className="json-string">{String(v)}</span>
}

function TreeNode({
  name,
  value,
  depth,
  projectId,
  markdown,
}: {
  name: string | null
  value: unknown
  depth: number
  projectId: string
  markdown?: boolean
}) {
  const isObj = value !== null && typeof value === 'object'
  const [open, setOpen] = useState(depth < 2)
  const label = name != null ? <span className="json-key mono">{name}: </span> : null

  if (isRef(value)) {
    return (
      <div style={{ paddingLeft: depth ? 14 : 0 }}>
        {label}
        <RefChip refStr={value} />
      </div>
    )
  }
  if (isMedia(value)) {
    return (
      <div style={{ paddingLeft: depth ? 14 : 0 }}>
        {label}
        <MediaView value={value} projectId={projectId} />
      </div>
    )
  }
  if (!isObj) {
    if (typeof value === 'string' && (value.length > 80 || value.includes('\n'))) {
      return (
        <div style={{ paddingLeft: depth ? 14 : 0 }}>
          {label}
          <div style={{ paddingLeft: 4 }}>
            <StringView s={value} markdown={markdown} />
          </div>
        </div>
      )
    }
    return (
      <div style={{ paddingLeft: depth ? 14 : 0 }}>
        {label}
        <Scalar v={value} />
      </div>
    )
  }
  const entries: [string, unknown][] = Array.isArray(value)
    ? value.map((v, i) => [String(i), v])
    : Object.entries(value as Record<string, unknown>).filter(([k]) => !k.startsWith('_') || depth > 0 || k === '_type')
  const summary = Array.isArray(value) ? `[${value.length}]` : `{${entries.length}}`
  return (
    <div style={{ paddingLeft: depth ? 14 : 0 }}>
      <span style={{ cursor: 'pointer' }} onClick={() => setOpen((o) => !o)}>
        <span className="faint mono" style={{ display: 'inline-block', width: 12 }}>
          {open ? '▾' : '▸'}
        </span>
        {label}
        {!open && <span className="faint mono">{summary}</span>}
      </span>
      {open &&
        (entries.length === 0 ? (
          <div style={{ paddingLeft: 14 }} className="faint mono">
            {Array.isArray(value) ? '[]' : '{}'}
          </div>
        ) : (
          entries.map(([k, v]) => <TreeNode key={k} name={k} value={v} depth={depth + 1} projectId={projectId} markdown={markdown} />)
        ))}
    </div>
  )
}

export function JsonBlock({ value }: { value: unknown }) {
  return (
    <pre className="code-block">
      <code>{JSON.stringify(value, null, 2)}</code>
    </pre>
  )
}

/**
 * Render any value. Top-level dicts render as key/value rows; everything
 * else as a tree. `mode` lets callers force raw JSON.
 */
export function ValueView({
  value,
  projectId,
  mode = 'tree',
  markdown = false,
}: {
  value: unknown
  projectId: string
  mode?: 'tree' | 'json'
  markdown?: boolean
}) {
  if (mode === 'json') return <JsonBlock value={value} />
  if (value === undefined) return <span className="faint">—</span>
  if (typeof value === 'string' && !isRef(value) && !isMedia(value)) return <StringView s={value} markdown={markdown} />
  if (value && typeof value === 'object' && !Array.isArray(value) && !isMedia(value)) {
    const entries = Object.entries(value as Record<string, unknown>)
    if (entries.length === 0) return <span className="faint mono">{'{}'}</span>
    return (
      <div className="kv">
        {entries.map(([k, v]) => (
          <div key={k} style={{ display: 'contents' }}>
            <div className="k">{k}</div>
            <div className="v">
              <TreeNode name={null} value={v} depth={0} projectId={projectId} markdown={markdown} />
            </div>
          </div>
        ))}
      </div>
    )
  }
  return <TreeNode name={null} value={value} depth={0} projectId={projectId} markdown={markdown} />
}
