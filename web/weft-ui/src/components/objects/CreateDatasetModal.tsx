import { useRef, useState } from 'react'
import { api } from '../../api/client'
import { tableRef } from '../../api/refs'
import type { Any } from '../../api/types'
import { ErrorBox, Modal } from '../common'
import { cellToText, columnsOf, parseRowsText, textToCell } from './csv'
import { NameField, usePublish, type Created } from './ObjectForms'
import { datasetVal, errMsg } from './objectUtils'

type Row = Record<string, Any>

/** Spreadsheet-like editor for a small set of rows. */
export function RowsGrid({ rows, columns, onChange }: { rows: Row[]; columns: string[]; onChange: (rows: Row[], columns: string[]) => void }) {
  const setCell = (ri: number, col: string, text: string) =>
    onChange(
      rows.map((r, i) => (i === ri ? { ...r, [col]: textToCell(text, r[col]) } : r)),
      columns,
    )
  const renameCol = (ci: number, name: string) => {
    const old = columns[ci]
    if (!name || name === old) return
    if (columns.includes(name)) return
    const cols = columns.map((c, i) => (i === ci ? name : c))
    onChange(
      rows.map((r) => {
        const o: Row = {}
        for (const c of cols) o[c] = c === name ? r[old] : r[c]
        return o
      }),
      cols,
    )
  }
  const removeCol = (ci: number) => {
    const col = columns[ci]
    onChange(
      rows.map((r) => {
        const o = { ...r }
        delete o[col]
        return o
      }),
      columns.filter((_, i) => i !== ci),
    )
  }
  const addCol = () => {
    let n = columns.length + 1
    while (columns.includes(`col${n}`)) n++
    onChange(
      rows.map((r) => ({ ...r, [`col${n}`]: '' })),
      [...columns, `col${n}`],
    )
  }
  return (
    <div className="col">
      <div className="table-wrap" style={{ maxHeight: 380 }}>
        <table className="table grid-edit">
          <thead>
            <tr>
              <th style={{ width: 36 }}>#</th>
              {columns.map((c, ci) => (
                <th key={c + ci}>
                  <div className="row" style={{ gap: 2 }}>
                    <input defaultValue={c} onBlur={(e) => renameCol(ci, e.target.value.trim())} aria-label={`Column ${ci + 1} name`} />
                    <button className="qbtn sm plain" onClick={() => removeCol(ci)} title="Remove column">
                      ✕
                    </button>
                  </div>
                </th>
              ))}
              <th style={{ width: 40 }}>
                <button className="qbtn sm plain" onClick={addCol} title="Add column">
                  +
                </button>
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r, ri) => (
              <tr key={ri}>
                <td className="faint small" style={{ padding: '7px 8px' }}>
                  {ri + 1}
                </td>
                {columns.map((c) => (
                  <td key={c}>
                    <textarea className="cell-input" rows={1} value={cellToText(r[c])} onChange={(e) => setCell(ri, c, e.target.value)} />
                  </td>
                ))}
                <td style={{ padding: '4px' }}>
                  <button className="qbtn sm plain danger" onClick={() => onChange(rows.filter((_, i) => i !== ri), columns)} title="Delete row">
                    ✕
                  </button>
                </td>
              </tr>
            ))}
            {rows.length === 0 && (
              <tr>
                <td colSpan={columns.length + 2} className="faint" style={{ padding: 16, textAlign: 'center' }}>
                  No rows yet
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      <div className="row">
        <button className="qbtn sm" onClick={() => onChange([...rows, Object.fromEntries(columns.map((c) => [c, '']))], columns)}>
          + Add row
        </button>
        <span className="hint">
          {rows.length} rows · {columns.length} columns. Non-string cells are edited as JSON.
        </span>
      </div>
    </div>
  )
}

export function CreateDatasetModal({ projectId, onClose, onCreated }: { projectId: string; onClose: () => void; onCreated: (c: Created) => void }) {
  const publish = usePublish(projectId)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [mode, setMode] = useState<'upload' | 'paste' | 'grid'>('upload')
  const [paste, setPaste] = useState('')
  const [rows, setRows] = useState<Row[]>([])
  const [columns, setColumns] = useState<string[]>([])
  const [parseErr, setParseErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  const [fileName, setFileName] = useState<string | null>(null)
  const fileRef = useRef<HTMLInputElement>(null)

  const load = (text: string, hint?: 'csv' | 'json' | 'jsonl') => {
    try {
      const rs = parseRowsText(text, hint)
      setRows(rs)
      setColumns(columnsOf(rs))
      setParseErr(null)
    } catch (e) {
      setParseErr(errMsg(e))
    }
  }

  const onFile = async (f: File) => {
    setFileName(f.name)
    const ext = f.name.toLowerCase().split('.').pop()
    const hint = ext === 'csv' || ext === 'tsv' ? 'csv' : ext === 'jsonl' || ext === 'ndjson' ? 'jsonl' : ext === 'json' ? 'json' : undefined
    if (!name) setName(f.name.replace(/\.[^.]+$/, '').replace(/[^A-Za-z0-9_.-]/g, '-'))
    load(await f.text(), hint)
  }

  const startGrid = () => {
    setMode('grid')
    if (columns.length === 0) {
      setColumns(['question', 'expected'])
      setRows([{ question: '', expected: '' }])
    }
  }

  return (
    <Modal
      title="New dataset"
      wide
      onClose={onClose}
      footer={
        <>
          <span className="hint grow">{rows.length ? `${rows.length} rows will be uploaded` : ''}</span>
          <button type="button" className="qbtn" onClick={onClose}>
            Cancel
          </button>
          <button
            className="qbtn primary"
            disabled={!name || rows.length === 0 || busy}
            onClick={async () => {
              setBusy(true)
              setErr(null)
              try {
                const t = await api.tableCreate(projectId, rows)
                onCreated(await publish(name, datasetVal(name, tableRef(projectId, t.digest), description || null)))
              } catch (e) {
                setErr(e)
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? 'Publishing…' : 'Publish dataset'}
          </button>
        </>
      }
    >
      <div className="form-grid">
        <NameField value={name} onChange={setName} placeholder="qa-examples" />
        <label className="field">
          Description
          <input className="input" value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Optional" />
        </label>
      </div>
      <div className="seg">
        <button className={mode === 'upload' ? 'on' : ''} onClick={() => setMode('upload')}>
          Upload file
        </button>
        <button className={mode === 'paste' ? 'on' : ''} onClick={() => setMode('paste')}>
          Paste
        </button>
        <button className={mode === 'grid' ? 'on' : ''} onClick={startGrid}>
          Edit rows
        </button>
      </div>
      {mode === 'upload' && (
        <div
          className="empty"
          style={{ border: '1px dashed var(--border-strong)', borderRadius: 'var(--radius)', padding: 24, cursor: 'pointer' }}
          onClick={() => fileRef.current?.click()}
          onDragOver={(e) => e.preventDefault()}
          onDrop={(e) => {
            e.preventDefault()
            const f = e.dataTransfer.files[0]
            if (f) void onFile(f)
          }}
        >
          <div>{fileName ?? 'Drop a CSV, JSON or JSONL file here, or click to choose'}</div>
          <div className="hint">JSON must be an array of objects; CSV needs a header row.</div>
          <input
            ref={fileRef}
            type="file"
            accept=".csv,.json,.jsonl,.ndjson,text/csv,application/json"
            style={{ display: 'none' }}
            onChange={(e) => {
              const f = e.target.files?.[0]
              if (f) void onFile(f)
            }}
          />
        </div>
      )}
      {mode === 'paste' && (
        <label className="field">
          CSV, JSON array or JSONL
          <textarea
            className="input mono"
            rows={8}
            value={paste}
            placeholder={'question,expected\nWhat is 2+2?,4'}
            onChange={(e) => {
              setPaste(e.target.value)
              load(e.target.value)
            }}
          />
        </label>
      )}
      {parseErr && <ErrorBox error={parseErr} />}
      {(rows.length > 0 || mode === 'grid') && (
        <RowsGrid
          rows={rows}
          columns={columns}
          onChange={(r, c) => {
            setRows(r)
            setColumns(c)
          }}
        />
      )}
      <ErrorBox error={err} />
    </Modal>
  )
}
