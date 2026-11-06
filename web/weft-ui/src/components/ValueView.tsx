import { useState } from 'react'
import { isRef } from '../api/refs'
import { MediaView, isMedia } from './media/MediaView'
import { RefChip } from './RefChip'
import { SafeMarkdown } from './SafeMarkdown'
import './valueview.css'

// Generic renderer for span inputs/outputs and saved values, readable first
// (DESIGN.md §7.1 rule 4, §7.2):
// - a dict at the top shows as a definition list (field, value);
// - a list of rows (objects with the same few plain fields) as a plain table;
// - text as prose (markdown when asked), code fields as code, long text
//   collapsed with "Show all";
// - refs as links, media inline ({_type: Image|Audio}, data: URLs, and
//   strings that are exactly an s3:// or gs:// object URI in a reference
//   bucket);
// - anything else nested as a collapsible tree. Raw JSON is JsonBlock.

// Media detection and rendering (Image, Audio, Video, PDF, Html, Markdown,
// Content, and the legacy encoding in compat/legacyMedia.ts) live in
// ./media; re-exported here for existing imports.
export { MediaView, isMedia }

const LONG = 600
const CODE_KEYS = /^(code|source|sql|query|script|patch|diff|stack|traceback)$/i

function StringView({ s, markdown, code }: { s: string; markdown?: boolean; code?: boolean }) {
  const [expanded, setExpanded] = useState(false)
  const long = s.length > LONG
  const shown = long && !expanded ? s.slice(0, LONG) + '…' : s
  return (
    <div>
      {code ? (
        <pre className="vv-code">{shown}</pre>
      ) : markdown ? (
        <div className="markdown">
          <SafeMarkdown>{shown}</SafeMarkdown>
        </div>
      ) : (
        <p className="vv-text">{shown}</p>
      )}
      {long && (
        <button type="button" className="vv-more" onClick={() => setExpanded((e) => !e)}>
          {expanded ? 'Show less' : `Show all (${s.length.toLocaleString()} characters)`}
        </button>
      )}
    </div>
  )
}

/** A plain value in near-black text: values are never syntax-coloured (strings, numbers and booleans read alike). */
function Scalar({ v }: { v: unknown }) {
  if (v === null || v === undefined) return <span className="vv-null">null</span>
  if (typeof v === 'number') return <span className="vv-value vv-num">{String(v)}</span>
  return <span className="vv-value">{String(v)}</span>
}

const isPlain = (v: unknown) => v === null || ['string', 'number', 'boolean'].includes(typeof v)

/** The columns of a list of rows, or null when it is not one: 1–200 objects whose values are mostly plain, at most 8 columns. */
export function rowColumns(value: unknown): string[] | null {
  if (!Array.isArray(value) || value.length === 0 || value.length > 200) return null
  if (!value.every((r) => r && typeof r === 'object' && !Array.isArray(r) && !isMedia(r))) return null
  const cols: string[] = []
  for (const r of value as Record<string, unknown>[]) for (const k of Object.keys(r)) if (!cols.includes(k)) cols.push(k)
  if (cols.length === 0 || cols.length > 8) return null
  const cells = (value as Record<string, unknown>[]).flatMap((r) => cols.map((c) => r[c]))
  const plain = cells.filter((c) => c === undefined || isPlain(c) || isRef(c)).length
  return plain / cells.length >= 0.8 ? cols : null
}

function RowsTable({ rows, cols, projectId }: { rows: Record<string, unknown>[]; cols: string[]; projectId: string }) {
  return (
    <div className="vv-table-box" tabIndex={0} role="region" aria-label={`Table of ${rows.length} rows`}>
      <table className="vv-table">
        <thead>
          <tr>
            {cols.map((c) => (
              <th key={c} scope="col">
                {c}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={i}>
              {cols.map((c) => (
                <td key={c}>{r[c] === undefined ? <span className="faint">—</span> : <TreeNode name={null} value={r[c]} depth={1} projectId={projectId} flat />}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function TreeNode({
  name,
  value,
  depth,
  projectId,
  markdown,
  flat,
}: {
  name: string | null
  value: unknown
  depth: number
  projectId: string
  markdown?: boolean
  /** Inside a table cell or a field: no indent. */
  flat?: boolean
}) {
  const isObj = value !== null && typeof value === 'object'
  const [open, setOpen] = useState(depth < 2)
  const label = name != null ? <span className="vv-key">{name}: </span> : null
  const pad = flat || !depth ? 0 : 14

  if (isRef(value)) {
    return (
      <div style={{ paddingLeft: pad }}>
        {label}
        <RefChip refStr={value} />
      </div>
    )
  }
  if (isMedia(value)) {
    return (
      <div style={{ paddingLeft: pad }}>
        {label}
        <MediaView value={value} projectId={projectId} />
      </div>
    )
  }
  if (!isObj) {
    if (typeof value === 'string' && (value.length > 80 || value.includes('\n'))) {
      return (
        <div style={{ paddingLeft: pad }}>
          {label}
          <StringView s={value} markdown={markdown} code={name != null && CODE_KEYS.test(name)} />
        </div>
      )
    }
    return (
      <div style={{ paddingLeft: pad }}>
        {label}
        <Scalar v={value} />
      </div>
    )
  }
  const cols = rowColumns(value)
  if (cols)
    return (
      <div style={{ paddingLeft: pad }}>
        {label}
        <RowsTable rows={value as Record<string, unknown>[]} cols={cols} projectId={projectId} />
      </div>
    )
  const entries: [string, unknown][] = Array.isArray(value)
    ? value.map((v, i) => [String(i), v])
    : Object.entries(value as Record<string, unknown>).filter(([k]) => !k.startsWith('_') || depth > 0 || k === '_type')
  const summary = Array.isArray(value) ? `${value.length} ${value.length === 1 ? 'item' : 'items'}` : `${entries.length} ${entries.length === 1 ? 'field' : 'fields'}`
  return (
    <div style={{ paddingLeft: pad }}>
      <button type="button" className="vv-tree-toggle" aria-expanded={open} aria-label={label ? undefined : `${open ? 'Collapse' : 'Expand'} ${summary}`} onClick={() => setOpen((o) => !o)}>
        <span className="faint mono" aria-hidden="true" style={{ display: 'inline-block', width: 12 }}>
          {open ? '▾' : '▸'}
        </span>
        {label}
        {!open && <span className="faint">{summary}</span>}
      </button>
      {open &&
        (entries.length === 0 ? (
          <div style={{ paddingLeft: 14 }} className="faint">
            {Array.isArray(value) ? 'empty list' : 'no fields'}
          </div>
        ) : (
          entries.map(([k, v]) => <TreeNode key={k} name={k} value={v} depth={depth + 1} projectId={projectId} markdown={markdown} />)
        ))}
    </div>
  )
}

export function JsonBlock({ value }: { value: unknown }) {
  return (
    <pre className="code-block" tabIndex={0}>
      <code>{JSON.stringify(value, null, 2)}</code>
    </pre>
  )
}

/** A field name for people: "llm_model_id" → "llm model id"; "_type" stays. */
const fieldLabel = (k: string) => (k.startsWith('_') ? k : k.replace(/[_]+/g, ' '))

/**
 * Render any value. A dict at the top renders as field/value pairs, a list
 * of rows as a table, everything else as text or a tree. `mode` forces raw
 * JSON.
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
    if (entries.length === 0) return <span className="faint">No fields</span>
    return (
      <dl className="vv-fields">
        {entries.map(([k, v]) => (
          <div key={k} style={{ display: 'contents' }}>
            <dt title={k}>{fieldLabel(k)}</dt>
            <dd>
              {typeof v === 'string' && CODE_KEYS.test(k) && !isRef(v) ? (
                <StringView s={v} code />
              ) : (
                <TreeNode name={null} value={v} depth={0} projectId={projectId} markdown={markdown} flat />
              )}
            </dd>
          </div>
        ))}
      </dl>
    )
  }
  return <TreeNode name={null} value={value} depth={0} projectId={projectId} markdown={markdown} />
}
