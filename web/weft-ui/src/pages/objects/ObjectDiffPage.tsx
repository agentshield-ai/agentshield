import { useMemo, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { Any, ObjSchema, TableRow } from '../../api/types'
import { Empty, ErrorBox, Loading, Section } from '../../components/common'
import { Stat } from '../../components/objects/bits'
import { columnsOf } from '../../components/objects/csv'
import { CellValue } from '../../components/objects/DatasetRowsView'
import { pathDiff, PathChangesTable, stableJson, TextDiff } from '../../components/objects/JsonDiff'
import { fetchAllRows, isDataset, KIND_META, kindOf, rowsDigest, useObjectVersions } from '../../components/objects/objectUtils'
import { useProjectId } from '../../hooks/useProject'
import { fmtRelative } from '../../lib/format'
import { objectUrl, objectVersionsUrl } from '../../lib/urls'

type View = 'changes' | 'split' | 'unified'

export function ObjectDiffPage() {
  const projectId = useProjectId()
  const { objectId = '' } = useParams()
  const [params, setParams] = useSearchParams()
  const a = params.get('a') ?? ''
  const b = params.get('b') ?? 'latest'
  const [view, setView] = useState<View>('split')
  const versions = useObjectVersions(projectId, objectId)
  const read = (d: string) => ({
    queryKey: ['obj', projectId, objectId, d],
    queryFn: () => api.objRead(projectId, objectId, d).then((r) => r.obj),
    enabled: !!d,
  })
  const qa = useQuery(read(a))
  const qb = useQuery(read(b))
  const setSide = (k: 'a' | 'b', d: string) => {
    const p = new URLSearchParams(params)
    p.set(k, d)
    setParams(p, { replace: true })
  }

  const changes = useMemo(() => (qa.data && qb.data ? pathDiff(qa.data.val, qb.data.val) : []), [qa.data, qb.data])
  const left = useMemo(() => (qa.data ? stableJson(qa.data.val) : ''), [qa.data])
  const right = useMemo(() => (qb.data ? stableJson(qb.data.val) : ''), [qb.data])
  const kind = qb.data ? kindOf(qb.data) : 'all'

  const picker = (side: 'a' | 'b', cur: ObjSchema | undefined) => (
    <select className="input" value={cur?.digest ?? (side === 'a' ? a : b)} onChange={(e) => setSide(side, e.target.value)} style={{ maxWidth: 240 }}>
      {!cur && <option value={side === 'a' ? a : b}>{side === 'a' ? a || 'choose…' : b}</option>}
      {(versions.data ?? []).map((v) => (
        <option key={v.digest} value={v.digest}>
          v{v.version_index} · {v.digest.slice(0, 8)} · {fmtRelative(v.created_at)}
        </option>
      ))}
    </select>
  )

  return (
    <div className="page">
      <div className="breadcrumbs">
        <Link to={`/${projectId}/${kind === 'all' ? 'objects' : kind}`}>{KIND_META[kind].title}</Link>
        <span>/</span>
        <Link to={objectVersionsUrl(projectId, objectId)}>{objectId}</Link>
        <span>/</span>
        <span>compare</span>
      </div>
      <div className="page-header">
        <h1>
          Compare <span className="mono">{objectId}</span>
        </h1>
        <span className="spacer" />
        <div className="row wrap">
          <span className="badge error">A</span>
          {picker('a', qa.data)}
          <button
            className="btn sm ghost"
            title="Swap"
            onClick={() => {
              const p = new URLSearchParams(params)
              p.set('a', qb.data?.digest ?? b)
              p.set('b', qa.data?.digest ?? a)
              setParams(p, { replace: true })
            }}
          >
            ⇄
          </button>
          <span className="badge success">B</span>
          {picker('b', qb.data)}
        </div>
      </div>
      <ErrorBox error={qa.error ?? qb.error ?? versions.error} />
      {!a ? (
        <div className="card">
          <Empty>Choose version A to compare.</Empty>
        </div>
      ) : qa.isLoading || qb.isLoading ? (
        <Loading />
      ) : qa.data && qb.data ? (
        <>
          <div className="row wrap small muted" style={{ gap: 16 }}>
            <span>
              <Link to={objectUrl(projectId, objectId, qa.data.digest)}>v{qa.data.version_index}</Link> → <Link to={objectUrl(projectId, objectId, qb.data.digest)}>v{qb.data.version_index}</Link>
            </span>
            <span style={{ color: 'var(--success)' }}>+{changes.filter((c) => c.kind === 'added').length} added</span>
            <span style={{ color: 'var(--error)' }}>−{changes.filter((c) => c.kind === 'removed').length} removed</span>
            <span style={{ color: 'var(--warn)' }}>~{changes.filter((c) => c.kind === 'changed').length} changed</span>
            <span className="grow" />
            <div className="seg">
              <button className={view === 'split' ? 'on' : ''} onClick={() => setView('split')}>
                Side by side
              </button>
              <button className={view === 'unified' ? 'on' : ''} onClick={() => setView('unified')}>
                Unified
              </button>
              <button className={view === 'changes' ? 'on' : ''} onClick={() => setView('changes')}>
                Changed paths
              </button>
            </div>
          </div>
          {qa.data.digest === qb.data.digest ? (
            <div className="card">
              <Empty>Both sides are the same version.</Empty>
            </div>
          ) : view === 'changes' ? (
            <PathChangesTable changes={changes} />
          ) : (
            <TextDiff left={left} right={right} mode={view} />
          )}
          {isDataset(qa.data) && isDataset(qb.data) && <DatasetRowDiff projectId={projectId} a={qa.data} b={qb.data} />}
        </>
      ) : null}
    </div>
  )
}

function DatasetRowDiff({ projectId, a, b }: { projectId: string; a: ObjSchema; b: ObjSchema }) {
  const da = rowsDigest(a.val)
  const db = rowsDigest(b.val)
  const [show, setShow] = useState<'added' | 'removed'>('added')
  const q = useQuery({
    queryKey: ['dataset-row-diff', projectId, da, db],
    enabled: !!da && !!db,
    queryFn: async () => {
      const [ra, rb] = await Promise.all([fetchAllRows(projectId, da!, 20_000), fetchAllRows(projectId, db!, 20_000)])
      const sa = new Set(ra.map((r) => r.digest))
      const sb = new Set(rb.map((r) => r.digest))
      return {
        countA: ra.length,
        countB: rb.length,
        added: rb.filter((r) => !sa.has(r.digest)),
        removed: ra.filter((r) => !sb.has(r.digest)),
        same: rb.filter((r) => sa.has(r.digest)).length,
      }
    },
  })
  return (
    <Section title="Row changes">
      <ErrorBox error={q.error} />
      {da === db ? (
        <div className="faint">Both versions point at the same rows table.</div>
      ) : q.isLoading ? (
        <Loading label="Comparing rows…" />
      ) : q.data ? (
        <div className="col" style={{ gap: 12 }}>
          <div className="stat-grid">
            <Stat label="Rows in A" value={q.data.countA.toLocaleString()} />
            <Stat label="Rows in B" value={q.data.countB.toLocaleString()} />
            <Stat label="Unchanged" value={q.data.same.toLocaleString()} />
            <Stat label="Added" value={<span style={{ color: 'var(--success)' }}>+{q.data.added.length}</span>} />
            <Stat label="Removed" value={<span style={{ color: 'var(--error)' }}>−{q.data.removed.length}</span>} />
          </div>
          <div className="seg" style={{ alignSelf: 'flex-start' }}>
            <button className={show === 'added' ? 'on' : ''} onClick={() => setShow('added')}>
              Added ({q.data.added.length})
            </button>
            <button className={show === 'removed' ? 'on' : ''} onClick={() => setShow('removed')}>
              Removed ({q.data.removed.length})
            </button>
          </div>
          <RowsTable projectId={projectId} rows={show === 'added' ? q.data.added : q.data.removed} tone={show} />
          <div className="hint">Rows are matched by content digest, so an edited row appears as one removed and one added row.</div>
        </div>
      ) : null}
    </Section>
  )
}

function RowsTable({ projectId, rows, tone }: { projectId: string; rows: TableRow[]; tone: 'added' | 'removed' }) {
  const shown = rows.slice(0, 200)
  const cols = columnsOf(shown.map((r) => r.val))
  if (!rows.length) return <div className="faint small">None.</div>
  const obj = (v: Any) => (v && typeof v === 'object' && !Array.isArray(v) ? v : { value: v })
  return (
    <div className="table-wrap" style={{ maxHeight: 480 }}>
      <table className="table">
        <thead>
          <tr>
            <th className="num">#</th>
            {cols.map((c) => (
              <th key={c}>{c}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {shown.map((r) => (
            <tr key={r.digest} style={{ background: tone === 'added' ? 'var(--success-bg)' : 'var(--error-bg)' }}>
              <td className="num faint small">{r.original_index}</td>
              {cols.map((c) => (
                <td key={c}>
                  <CellValue v={obj(r.val)[c]} projectId={projectId} />
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      {rows.length > 200 && <div className="hint" style={{ padding: 8 }}>Showing first 200 of {rows.length}.</div>}
    </div>
  )
}
