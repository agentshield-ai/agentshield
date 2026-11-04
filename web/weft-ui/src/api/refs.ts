// Parsing and formatting of weft:/// refs. See docs/weft/API.md "Conventions".

export type RefKind = 'object' | 'function' | 'table' | 'span'

export interface ParsedRef {
  scheme: 'weft'
  entity: string
  project: string
  projectId: string
  /** The ref's kind; the earlier kind names `op` and `call` read as `function` and `span`. */
  kind: RefKind
  /** object/function name, table digest or span id */
  name: string
  /** version digest (or `latest`, `v<N>`, an alias) for object/function refs */
  digest: string | null
  /** extra path segments after the base ref, e.g. ['attr','rows','id','abc'] */
  extra: string[]
}

// Stored values (span inputs, object values, table rows) are never rewritten,
// so refs written with the earlier kind names op and call are read too.
const REF_RE = /^(weft):\/\/\/([^/]+)\/([^/]+)\/(object|function|table|span|op|call)\/(.+)$/

const KIND: Record<string, RefKind> = { object: 'object', function: 'function', op: 'function', table: 'table', span: 'span', call: 'span' }

export function isRef(v: unknown): v is string {
  return typeof v === 'string' && REF_RE.test(v)
}

export function parseRef(ref: string): ParsedRef | null {
  const m = REF_RE.exec(ref)
  if (!m) return null
  const [, scheme, entity, project, rawKind, rest] = m
  const kind = KIND[rawKind]
  const parts = rest.split('/')
  const head = decodeURIComponent(parts[0])
  const extra = parts.slice(1).map(decodeURIComponent)
  let name = head
  let digest: string | null = null
  if (kind === 'object' || kind === 'function') {
    const i = head.lastIndexOf(':')
    if (i >= 0) {
      name = head.slice(0, i)
      digest = head.slice(i + 1)
    }
  }
  return {
    scheme: scheme as 'weft',
    entity,
    project,
    projectId: `${entity}/${project}`,
    kind,
    name,
    digest,
    extra,
  }
}

export function objectRef(projectId: string, name: string, digest: string, kind: 'object' | 'function' = 'object') {
  return `weft:///${projectId}/${kind}/${encodeURIComponent(name)}:${digest}`
}

export function tableRef(projectId: string, digest: string) {
  return `weft:///${projectId}/table/${digest}`
}

export function spanRef(projectId: string, spanId: string) {
  return `weft:///${projectId}/span/${spanId}`
}

/** Short human label for a span name that may be a function ref ("weft:///e/p/function/foo:abc" -> "foo"). */
export function spanDisplayName(name: string): string {
  const p = parseRef(name)
  if (p) return p.name
  return name
}

/** Short label for any ref: "name:<digest prefix>" or "name". */
export function refLabel(ref: string): string {
  const p = parseRef(ref)
  if (!p) return ref
  if (p.kind === 'span') return `span ${p.name.slice(0, 8)}`
  if (p.kind === 'table') return `table ${p.name.slice(0, 8)}`
  const base = p.digest ? `${p.name}:${p.digest.slice(0, 6)}` : p.name
  return p.extra.length ? `${base}/${p.extra.join('/')}` : base
}
