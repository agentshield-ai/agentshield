import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { isRef, tableRef } from '../../api/refs'
import type { Any, WeftObject, TableRow, TableUpdate } from '../../api/types'
import { Empty, ErrorBox, Loading, Pager, Spinner } from '../common'
import { RefChip } from '../RefChip'
import { ValueView } from '../ValueView'
import { Drawer } from './bits'
import { cellToText, columnsOf, rowsToCSV, textToCell } from './csv'
import { usePublish, type Created } from './ObjectForms'
import { datasetVal, downloadText, errMsg, fetchAllRows, rowsDigest } from './objectUtils'

type Row = Record<string, Any>
const LIMIT = 50

export function CellValue({ v, projectId }: { v: Any; projectId: string }) {
  if (v === undefined) return <span className="faint">—</span>
  if (v === null) return <span className="json-null mono">null</span>
  if (isRef(v)) return <RefChip refStr={v} />
  if (typeof v === 'boolean') return <span className="json-bool mono">{String(v)}</span>
  if (typeof v === 'number') return <span className="json-number mono">{v}</span>
  if (typeof v === 'string')
    return (
      <div className="cell-box" style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word' }} title={v.length > 200 ? v.slice(0, 2000) : undefined}>
        {v.length > 600 ? v.slice(0, 600) + '…' : v}
      </div>
    )
  return (
    <div className="cell-box">
      <ValueView value={v} projectId={projectId} />
    </div>
  )
}

const asObj = (v: Any): Row => (v && typeof v === 'object' && !Array.isArray(v) ? v : { value: v })

export function DatasetRowsView({ projectId, obj, onPublished }: { projectId: string; obj: WeftObject; onPublished: (c: Created) => void }) {
  const digest = rowsDigest(obj.value)
  const publish = usePublish(projectId)
  const [offset, setOffset] = useState(0)
  const [sort, setSort] = useState<{ col: string; dir: 'asc' | 'desc' } | null>(null)
  const [editing, setEditing] = useState(false)
  const [edits, setEdits] = useState<Map<number, Row>>(new Map())
  const [deleted, setDeleted] = useState<Set<number>>(new Set())
  const [appended, setAppended] = useState<Row[]>([])
  const [saving, setSaving] = useState(false)
  const [saveErr, setSaveErr] = useState<unknown>(null)
  const [open, setOpen] = useState<TableRow | null>(null)
  const [downloading, setDownloading] = useState(false)

  const count = useQuery({
    queryKey: ['table-count', projectId, digest],
    queryFn: () => api.tableCount(projectId, digest!),
    enabled: !!digest,
  })
  const page = useQuery({
    queryKey: ['table-rows', projectId, digest, offset, sort],
    queryFn: () =>
      api
        .tableQuery({
          project_id: projectId,
          digest: digest!,
          limit: LIMIT,
          offset,
          sort_by: sort ? [{ field: `val.${sort.col}`, direction: sort.dir }] : undefined,
        })
        .then((r) => r.items),
    enabled: !!digest,
    placeholderData: keepPreviousData,
  })

  const columns = useMemo(() => {
    const vals = (page.data ?? []).map((r) => edits.get(r.index) ?? r.value)
    return columnsOf([...vals, ...appended])
  }, [page.data, edits, appended])

  const dirty = edits.size + deleted.size + appended.length
  const reset = () => {
    setEdits(new Map())
    setDeleted(new Set())
    setAppended([])
    setSaveErr(null)
  }

  if (!digest) return <ErrorBox error={`This dataset has no rows table (rows = ${JSON.stringify(obj.value?.rows)})`} />

  const download = async (fmt: 'json' | 'csv' | 'jsonl') => {
    setDownloading(true)
    try {
      const rows = (await fetchAllRows(projectId, digest)).map((r) => r.value)
      const name = `${obj.name}-v${obj.version}`
      if (fmt === 'csv') downloadText(`${name}.csv`, rowsToCSV(rows), 'text/csv')
      else if (fmt === 'jsonl') downloadText(`${name}.jsonl`, rows.map((r) => JSON.stringify(r)).join('\n') + '\n', 'application/x-ndjson')
      else downloadText(`${name}.json`, JSON.stringify(rows, null, 2))
    } catch (e) {
      alert(errMsg(e))
    } finally {
      setDownloading(false)
    }
  }

  const save = async () => {
    setSaving(true)
    setSaveErr(null)
    try {
      const updates: TableUpdate[] = []
      for (const [index, row] of [...edits.entries()].sort((a, b) => a[0] - b[0])) {
        if (!deleted.has(index)) updates.push({ replace: { index, row } })
      }
      for (const index of [...deleted].sort((a, b) => b - a)) updates.push({ pop: { index } })
      for (const row of appended) updates.push({ append: { row } })
      const t = await api.tableUpdate(projectId, digest, updates)
      const c = await publish(obj.name, datasetVal(obj.name, tableRef(projectId, t.digest), null, obj.value))
      reset()
      setEditing(false)
      onPublished(c)
    } catch (e) {
      setSaveErr(e)
    } finally {
      setSaving(false)
    }
  }

  const setCell = (r: TableRow, col: string, text: string) => {
    const base = asObj(edits.get(r.index) ?? r.value)
    const next = new Map(edits)
    next.set(r.index, { ...base, [col]: textToCell(text, base[col]) })
    setEdits(next)
  }

  const total = count.data ?? null
  return (
    <div className="col" style={{ gap: 10 }}>
      <div className="toolbar">
        <span className="muted small">
          {total != null ? `${total.toLocaleString()} rows` : <Spinner />} · {columns.length} columns
        </span>
        {page.isFetching && <Spinner />}
        <span className="grow" />
        {editing ? (
          <>
            <span className="hint">{dirty ? `${edits.size} edited · ${deleted.size} deleted · ${appended.length} added` : 'Click a cell to edit'}</span>
            <button className="btn sm" onClick={() => setAppended([...appended, Object.fromEntries(columns.map((c) => [c, '']))])}>
              + Add row
            </button>
            <button
              className="btn sm"
              onClick={() => {
                reset()
                setEditing(false)
              }}
            >
              Cancel
            </button>
            <button className="btn sm primary" disabled={!dirty || saving} onClick={save}>
              {saving ? 'Publishing…' : 'Publish new version'}
            </button>
          </>
        ) : (
          <>
            <select
              className="input"
              style={{ height: 26, fontSize: 12 }}
              value=""
              disabled={downloading}
              onChange={(e) => e.target.value && void download(e.target.value as 'json' | 'csv' | 'jsonl')}
            >
              <option value="">{downloading ? 'Downloading…' : 'Download…'}</option>
              <option value="json">JSON</option>
              <option value="jsonl">JSONL</option>
              <option value="csv">CSV</option>
            </select>
            <button className="btn sm" onClick={() => setEditing(true)}>
              Edit rows
            </button>
          </>
        )}
      </div>
      <ErrorBox error={page.error ?? count.error ?? saveErr} />
      {page.isLoading ? (
        <Loading />
      ) : (page.data ?? []).length === 0 && appended.length === 0 ? (
        <div className="card">
          <Empty>This dataset has no rows.</Empty>
        </div>
      ) : (
        <div className="table-wrap" style={{ maxHeight: '70vh' }}>
          <table className={`table ${editing ? 'grid-edit' : ''}`}>
            <thead>
              <tr>
                <th className="num" style={{ width: 44 }}>
                  #
                </th>
                {columns.map((c) => (
                  <th
                    key={c}
                    className="sortable"
                    onClick={() => {
                      if (editing) return
                      setOffset(0)
                      setSort((s) => (s?.col !== c ? { col: c, dir: 'asc' } : s.dir === 'asc' ? { col: c, dir: 'desc' } : null))
                    }}
                  >
                    {c} {sort?.col === c ? (sort.dir === 'asc' ? '▲' : '▼') : ''}
                  </th>
                ))}
                {editing && <th style={{ width: 40 }} />}
              </tr>
            </thead>
            <tbody>
              {(page.data ?? []).map((r) => {
                const isDel = deleted.has(r.index)
                const edited = edits.get(r.index)
                const val = asObj(edited ?? r.value)
                return (
                  <tr
                    key={r.digest + r.index}
                    className={`${isDel ? 'row-deleted' : edited ? 'row-edited' : ''} ${editing ? '' : 'clickable'}`}
                    onClick={() => !editing && setOpen(r)}
                  >
                    <td className="num faint small" style={editing ? { padding: '7px 8px' } : undefined}>
                      {r.index}
                    </td>
                    {columns.map((c) =>
                      editing ? (
                        <td key={c}>
                          <textarea className="cell-input" rows={1} disabled={isDel} value={cellToText(val[c])} onChange={(e) => setCell(r, c, e.target.value)} />
                        </td>
                      ) : (
                        <td key={c}>
                          <CellValue v={val[c]} projectId={projectId} />
                        </td>
                      ),
                    )}
                    {editing && (
                      <td style={{ padding: 4 }}>
                        <button
                          className="btn ghost sm icon"
                          title={isDel ? 'Restore row' : 'Delete row'}
                          onClick={() => {
                            const n = new Set(deleted)
                            if (isDel) n.delete(r.index)
                            else n.add(r.index)
                            setDeleted(n)
                          }}
                        >
                          {isDel ? '↺' : '✕'}
                        </button>
                      </td>
                    )}
                  </tr>
                )
              })}
              {appended.map((row, i) => (
                <tr key={`new-${i}`} className="row-new">
                  <td className="num faint small" style={{ padding: '7px 8px' }}>
                    new
                  </td>
                  {columns.map((c) => (
                    <td key={c}>
                      <textarea
                        className="cell-input"
                        rows={1}
                        value={cellToText(row[c])}
                        onChange={(e) => setAppended(appended.map((r, j) => (j === i ? { ...r, [c]: textToCell(e.target.value, r[c]) } : r)))}
                      />
                    </td>
                  ))}
                  <td style={{ padding: 4 }}>
                    <button className="btn ghost sm icon" title="Remove new row" onClick={() => setAppended(appended.filter((_, j) => j !== i))}>
                      ✕
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {total != null && total > LIMIT && <Pager offset={offset} limit={LIMIT} total={total} onChange={setOffset} />}
      {open && (
        <Drawer title={`Row ${open.index}`} onClose={() => setOpen(null)}>
          <div className="hint mono">digest {open.digest}</div>
          <ValueView value={open.value} projectId={projectId} />
        </Drawer>
      )}
    </div>
  )
}
