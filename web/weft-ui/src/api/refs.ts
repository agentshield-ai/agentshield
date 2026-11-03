// Parsing and formatting of weft:/// refs. See docs/weft/API.md.

export type RefKind = 'object' | 'op' | 'table' | 'call'

export interface ParsedRef {
  scheme: 'weft'
  entity: string
  project: string
  projectId: string
  kind: RefKind
  /** object/op name, table digest or call id */
  name: string
  /** version digest for object/op refs */
  digest: string | null
  /** extra path segments after the base ref, e.g. ['attr','rows','id','abc'] */
  extra: string[]
}

const REF_RE = /^(weft):\/\/\/([^/]+)\/([^/]+)\/(object|op|table|call)\/(.+)$/

export function isRef(v: unknown): v is string {
  return typeof v === 'string' && REF_RE.test(v)
}

export function parseRef(ref: string): ParsedRef | null {
  const m = REF_RE.exec(ref)
  if (!m) return null
  const [, scheme, entity, project, kind, rest] = m
  const parts = rest.split('/')
  const head = decodeURIComponent(parts[0])
  const extra = parts.slice(1).map(decodeURIComponent)
  let name = head
  let digest: string | null = null
  if (kind === 'object' || kind === 'op') {
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
    kind: kind as RefKind,
    name,
    digest,
    extra,
  }
}

export function objectRef(projectId: string, objectId: string, digest: string, kind: 'object' | 'op' = 'object') {
  return `weft:///${projectId}/${kind}/${encodeURIComponent(objectId)}:${digest}`
}

export function tableRef(projectId: string, digest: string) {
  return `weft:///${projectId}/table/${digest}`
}

export function callRef(projectId: string, callId: string) {
  return `weft:///${projectId}/call/${callId}`
}

/** Short human label for an op name that may be a ref ("weft:///e/p/op/foo:abc" -> "foo"). */
export function opDisplayName(opName: string): string {
  const p = parseRef(opName)
  if (p) return p.name
  return opName
}

/** Short label for any ref: "name:v<digest prefix>" or "name". */
export function refLabel(ref: string): string {
  const p = parseRef(ref)
  if (!p) return ref
  if (p.kind === 'call') return `call ${p.name.slice(0, 8)}`
  if (p.kind === 'table') return `table ${p.name.slice(0, 8)}`
  const base = p.digest ? `${p.name}:${p.digest.slice(0, 6)}` : p.name
  return p.extra.length ? `${base}/${p.extra.join('/')}` : base
}
