// Code snippets that reproduce the current traces query (unit tested).
import type { CallsFilter, Query, SortBy } from '../../api/types'

/** Render a JSON value as a Python literal (True/False/None, repr-style strings). */
export function toPython(v: unknown, indent = 0): string {
  const pad = (n: number) => '    '.repeat(n)
  if (v === null || v === undefined) return 'None'
  if (v === true) return 'True'
  if (v === false) return 'False'
  if (typeof v === 'number') return Number.isFinite(v) ? String(v) : 'None'
  if (typeof v === 'string') return JSON.stringify(v)
  if (Array.isArray(v)) {
    if (!v.length) return '[]'
    const inner = v.map((x) => toPython(x, indent + 1))
    const flat = `[${inner.join(', ')}]`
    if (flat.length <= 80 && !flat.includes('\n')) return flat
    return `[\n${inner.map((x) => pad(indent + 1) + x).join(',\n')},\n${pad(indent)}]`
  }
  const entries = Object.entries(v as Record<string, unknown>).filter(([, x]) => x !== undefined)
  if (!entries.length) return '{}'
  const inner = entries.map(([k, x]) => `${JSON.stringify(k)}: ${toPython(x, indent + 1)}`)
  const flat = `{${inner.join(', ')}}`
  if (flat.length <= 80 && !flat.includes('\n')) return flat
  return `{\n${inner.map((x) => pad(indent + 1) + x).join(',\n')},\n${pad(indent)}}`
}

export interface QuerySpec {
  projectId: string
  filter?: CallsFilter
  query?: Query
  search?: string
  sortBy?: SortBy[]
}

function cleanFilter(f?: CallsFilter): CallsFilter | undefined {
  if (!f) return undefined
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(f)) {
    if (v === undefined || v === false || (Array.isArray(v) && !v.length)) continue
    out[k] = v
  }
  return Object.keys(out).length ? (out as CallsFilter) : undefined
}

function isDefaultSort(s?: SortBy[]) {
  return !s?.length || (s.length === 1 && s[0].field === 'started_at' && s[0].direction === 'desc')
}

export function pythonSnippet(q: QuerySpec, origin: string): string {
  const args: string[] = []
  const filter = cleanFilter(q.filter)
  if (filter) args.push(`filter=${toPython(filter, 1)}`)
  if (q.query) args.push(`query=${toPython(q.query, 1)}`)
  if (!isDefaultSort(q.sortBy)) args.push(`sort_by=${toPython(q.sortBy, 1)}`)
  if (q.search) args.push(`search=${toPython(q.search)}`)
  args.push('include_costs=True')
  return `import weft

client = weft.init(${JSON.stringify(q.projectId)}, server_url=${JSON.stringify(origin)})
calls = client.get_calls(
${args.map((a) => `    ${a},`).join('\n')}
)
for call in calls:
    print(call.id, call.op_name, call.output)`
}

/** JSON body of /calls/export for this query. */
export function exportBody(q: QuerySpec, format: 'csv' | 'jsonl' | 'json', extra: Record<string, unknown> = {}): Record<string, unknown> {
  const body: Record<string, unknown> = { project_id: q.projectId }
  const filter = cleanFilter(q.filter)
  if (filter) body.filter = filter
  if (q.query) body.query = q.query
  if (q.search) body.search = q.search
  if (!isDefaultSort(q.sortBy)) body.sort_by = q.sortBy
  body.format = format
  return { ...body, ...extra }
}

/** Shell-quote for single-quoted strings. */
export function shQuote(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`
}

export function curlSnippet(q: QuerySpec, origin: string, format: 'csv' | 'jsonl' | 'json', auth: boolean): string {
  const body = JSON.stringify(exportBody(q, format), null, 2)
  const out = `calls.${format}`
  return `curl -sS -X POST ${origin}/calls/export \\
  -H 'Content-Type: application/json'${auth ? ` \\\n  -H "Authorization: Bearer $WEFT_API_KEY"` : ''} \\
  -d ${shQuote(body)} \\
  -o ${out}`
}
