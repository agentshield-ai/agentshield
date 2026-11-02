import { useRef, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { dashboardsApi } from '../../api/dashboards'
import { fmtBytes, fmtNumber } from '../../lib/format'
import { ErrorBox, Modal, Tabs } from '../common'
import { CodeSnippet, serverOrigin } from './GettingStarted'
import { curlSnippet, exportBody, pythonSnippet, type QuerySpec } from './codeSnippets'
import '../dashboards/dashboards.css'

type Format = 'csv' | 'jsonl' | 'json'

/** Server-side export of every call matching the current filters. */
export function ServerExportModal({ spec, columns, onClose }: { spec: QuerySpec; columns: string[]; onClose: () => void }) {
  const [format, setFormat] = useState<Format>('csv')
  const [flatten, setFlatten] = useState(true)
  const [feedback, setFeedback] = useState(false)
  const [onlyCols, setOnlyCols] = useState(false)
  const [bytes, setBytes] = useState(0)
  const abort = useRef<AbortController | null>(null)
  const count = useQuery({
    queryKey: ['calls', spec.projectId, 'count', { filter: spec.filter, query: spec.query, search: spec.search }],
    queryFn: () => api.callsQueryStats({ project_id: spec.projectId, filter: spec.filter, query: spec.query, search: spec.search }),
  })
  const baseCols = ['id', 'op_name', 'status', 'started_at', 'latency_ms', 'total_tokens', 'cost_usd']
  const run = useMutation({
    mutationFn: async () => {
      abort.current = new AbortController()
      setBytes(0)
      const body = exportBody(spec, format, {
        flatten: format === 'csv' ? flatten : flatten || undefined,
        include_feedback: feedback || undefined,
        columns: onlyCols && columns.length ? [...baseCols, ...columns] : undefined,
      })
      return dashboardsApi.exportCalls(body as never, setBytes, abort.current.signal)
    },
  })
  const n = count.data?.count
  return (
    <Modal
      title="Export all matching calls"
      onClose={() => {
        abort.current?.abort()
        onClose()
      }}
      footer={
        <>
          {run.isPending && <span className="small muted">Downloading… {fmtBytes(bytes)}</span>}
          {run.isSuccess && <span className="small muted">Done · {fmtBytes(run.data)}</span>}
          <span className="grow" />
          <button
            className="btn"
            onClick={() => {
              abort.current?.abort()
              onClose()
            }}
          >
            {run.isSuccess ? 'Close' : 'Cancel'}
          </button>
          <button className="btn primary" disabled={run.isPending || n === 0} onClick={() => run.mutate()}>
            Download{n != null ? ` ${fmtNumber(n)} row${n === 1 ? '' : 's'}` : ''}
          </button>
        </>
      }
    >
      <div className="col" style={{ gap: 12 }}>
        <div className="small muted">
          The server streams every call that matches the current filters, search and sort, not only the loaded page. Large exports may take a while.
        </div>
        <div className="field dash-field">
          Format
          <div className="seg" role="group" aria-label="Format">
            {(['csv', 'jsonl', 'json'] as Format[]).map((f) => (
              <button key={f} className={format === f ? 'on' : ''} aria-pressed={format === f} onClick={() => setFormat(f)}>
                {f.toUpperCase()}
              </button>
            ))}
          </div>
        </div>
        <label className="checkbox">
          <input type="checkbox" checked={flatten} onChange={(e) => setFlatten(e.target.checked)} />
          {format === 'csv' ? 'Flatten nested values into dotted columns (inputs.question, output.answer, …)' : 'Flatten each call into dotted keys'}
        </label>
        <label className="checkbox">
          <input type="checkbox" checked={feedback} onChange={(e) => setFeedback(e.target.checked)} /> Include feedback
        </label>
        {columns.length > 0 && (
          <label className="checkbox">
            <input type="checkbox" checked={onlyCols} onChange={(e) => setOnlyCols(e.target.checked)} /> Only the table's columns ({[...baseCols, ...columns].length})
          </label>
        )}
        <ErrorBox error={run.error && !(run.error instanceof DOMException) ? run.error : null} />
      </div>
    </Modal>
  )
}

/** "Copy as code": the current filter as a Python SDK query and an export cURL. */
export function CopyAsCodeModal({ spec, authRequired, onClose }: { spec: QuerySpec; authRequired: boolean; onClose: () => void }) {
  const [tab, setTab] = useState<'python' | 'curl'>('python')
  const origin = serverOrigin()
  return (
    <Modal title="Query these calls from code" onClose={onClose} wide>
      <div className="col" style={{ gap: 10 }}>
        <Tabs
          tabs={[
            { id: 'python', label: 'Python SDK' },
            { id: 'curl', label: 'cURL (export)' },
          ]}
          value={tab}
          onChange={setTab}
        />
        {tab === 'python' ? (
          <>
            <div className="small muted">Iterates over every call matching the current filters. Requires the Weft Python SDK{authRequired ? ' and WEFT_API_KEY' : ''}.</div>
            <CodeSnippet code={pythonSnippet(spec, origin)} lang="python" />
          </>
        ) : (
          <>
            <div className="small muted">Downloads all matching calls as CSV. Change format to jsonl or json for full call objects.</div>
            <CodeSnippet code={curlSnippet(spec, origin, 'csv', authRequired)} lang="bash" />
          </>
        )}
      </div>
    </Modal>
  )
}
