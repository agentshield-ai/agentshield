// Minimal CSV / JSON / JSONL parsing and CSV serialisation for dataset import/export.
import type { Any } from '../../api/types'

/** RFC 4180-ish CSV parser: quoted fields, escaped quotes, CRLF. */
export function parseCSV(text: string): string[][] {
  const rows: string[][] = []
  let row: string[] = []
  let field = ''
  let inQuotes = false
  let i = 0
  if (text.charCodeAt(0) === 0xfeff) i = 1
  for (; i < text.length; i++) {
    const c = text[i]
    if (inQuotes) {
      if (c === '"') {
        if (text[i + 1] === '"') {
          field += '"'
          i++
        } else inQuotes = false
      } else field += c
      continue
    }
    if (c === '"') inQuotes = true
    else if (c === ',') {
      row.push(field)
      field = ''
    } else if (c === '\n' || c === '\r') {
      if (c === '\r' && text[i + 1] === '\n') i++
      row.push(field)
      rows.push(row)
      row = []
      field = ''
    } else field += c
  }
  if (field !== '' || row.length) {
    row.push(field)
    rows.push(row)
  }
  return rows.filter((r) => !(r.length === 1 && r[0] === ''))
}

/** Convert CSV text into an array of row objects keyed by header. */
export function csvToRows(text: string): Record<string, Any>[] {
  const [header, ...body] = parseCSV(text)
  if (!header) return []
  return body.map((r) => Object.fromEntries(header.map((h, i) => [h || `col${i + 1}`, autoType(r[i] ?? '')])))
}

/** Keep CSV strings, but turn obvious JSON objects/arrays back into values. */
function autoType(s: string): Any {
  const t = s.trim()
  if ((t.startsWith('{') && t.endsWith('}')) || (t.startsWith('[') && t.endsWith(']'))) {
    try {
      return JSON.parse(t)
    } catch {
      /* keep string */
    }
  }
  return s
}

export function parseRowsText(text: string, hint?: 'csv' | 'json' | 'jsonl'): Record<string, Any>[] {
  const t = text.trim()
  if (!t) return []
  const fmt = hint ?? (t.startsWith('[') ? 'json' : t.startsWith('{') ? 'jsonl' : 'csv')
  if (fmt === 'json') {
    const v = JSON.parse(t)
    if (!Array.isArray(v)) {
      if (v && typeof v === 'object' && Array.isArray(v.rows)) return v.rows.map(wrap)
      throw new Error('JSON must be an array of rows')
    }
    return v.map(wrap)
  }
  if (fmt === 'jsonl') {
    return t
      .split(/\r?\n/)
      .filter((l) => l.trim())
      .map((l, i) => {
        try {
          return wrap(JSON.parse(l))
        } catch {
          throw new Error(`Line ${i + 1} is not valid JSON`)
        }
      })
  }
  return csvToRows(text)
}

function wrap(v: Any): Record<string, Any> {
  return v && typeof v === 'object' && !Array.isArray(v) ? v : { value: v }
}

function csvCell(v: Any): string {
  if (v == null) return ''
  const s = typeof v === 'string' ? v : JSON.stringify(v)
  return /[",\r\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
}

export function rowsToCSV(rows: Any[]): string {
  const cols = columnsOf(rows)
  const lines = [cols.map(csvCell).join(',')]
  for (const r of rows) {
    const o = r && typeof r === 'object' && !Array.isArray(r) ? r : { value: r }
    lines.push(cols.map((c) => csvCell(o[c])).join(','))
  }
  return lines.join('\n') + '\n'
}

/** Union of keys across rows in first-seen order. Non-object rows get a "value" column. */
export function columnsOf(rows: Any[]): string[] {
  const seen = new Set<string>()
  for (const r of rows) {
    if (r && typeof r === 'object' && !Array.isArray(r)) Object.keys(r).forEach((k) => seen.add(k))
    else seen.add('value')
  }
  return [...seen]
}

/** Text shown when editing a cell; JSON for non-strings. */
export function cellToText(v: Any): string {
  if (v === undefined) return ''
  return typeof v === 'string' ? v : JSON.stringify(v)
}

/** Parse edited cell text back. Strings stay strings unless the original was a non-string. */
export function textToCell(text: string, original: Any): Any {
  if (typeof original === 'string' || original === undefined) return text
  try {
    return JSON.parse(text)
  } catch {
    return text
  }
}
