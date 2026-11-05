// Two versions of one asset side by side (DESIGN.md §7.1 rule 1, §7.2): the
// sentence says what changed ("From version 2 to version 3, 1 value changed.
// The rows went from 46 to 50: 4 added, none removed."); then the changed
// fields, the whole value side by side or as one column, and for datasets
// the rows added and removed. ?a= and ?b= name the versions (digests).
import { useMemo, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { Any, TableRow, WeftObject } from '../../api/types'
import { ErrorBox, Loading } from '../../components/common'
import { columnsOf } from '../../components/objects/csv'
import { CellValue } from '../../components/objects/DatasetRowsView'
import { pathDiff, PathChangesTable, stableJson, TextDiff } from '../../components/objects/JsonDiff'
import { fetchAllRows, isDataset, rowsDigest, useObjectVersions } from '../../components/objects/objectUtils'
import { PageHeader } from '../../components/ui/PageHeader'
import { PlainTable } from '../../components/ui/PlainTable'
import { Segmented, segmentedTabId } from '../../components/ui/Segmented'
import { Sentence } from '../../components/ui/Sentence'
import { useProjectId } from '../../hooks/useProject'
import { relativeTime } from '../../lib/summaries'
import { diffSentence, versionWords } from '../../lib/summaries/assets'
import { objectUrl, objectVersionsUrl } from '../../lib/urls'
import './assets.css'

type View = 'changes' | 'split' | 'unified'
const VIEWS: { id: View; label: string; title: string }[] = [
  { id: 'changes', label: 'Changed fields', title: 'Only the fields that differ' },
  { id: 'split', label: 'Side by side', title: 'The whole value, both versions' },
  { id: 'unified', label: 'One column', title: 'The whole value, with lines removed and added' },
]

const cap = (s: string) => s[0].toUpperCase() + s.slice(1)

export function ObjectDiffPage() {
  const projectId = useProjectId()
  const { objectId = '' } = useParams()
  const [params, setParams] = useSearchParams()
  const b = params.get('b') ?? 'latest'
  const versions = useObjectVersions(projectId, objectId)
  // Without ?a=, compare with the version before ?b= (or the one before the latest).
  const vs = versions.data ?? []
  const bVersion = vs.find((v) => v.digest === b) ?? (b === 'latest' ? vs[0] : undefined)
  const a = params.get('a') || (bVersion ? (vs.find((v) => v.version === bVersion.version - 1)?.digest ?? '') : '')
  const rawView = params.get('view')
  const view: View = VIEWS.some((v) => v.id === rawView) ? (rawView as View) : 'changes'
  const read = (d: string) => ({
    queryKey: ['object', projectId, objectId, d],
    queryFn: () => api.objectGet(projectId, objectId, d),
    enabled: !!d,
  })
  const qa = useQuery(read(a))
  const qb = useQuery(read(b))
  const set = (k: string, v: string | null) => {
    const p = new URLSearchParams(params)
    if (v) p.set(k, v)
    else p.delete(k)
    setParams(p, { replace: true })
  }

  const changes = useMemo(() => (qa.data && qb.data ? pathDiff(qa.data.value, qb.data.value) : []), [qa.data, qb.data])
  const left = useMemo(() => (qa.data ? stableJson(qa.data.value) : ''), [qa.data])
  const right = useMemo(() => (qb.data ? stableJson(qb.data.value) : ''), [qb.data])
  const datasets = !!qa.data && !!qb.data && isDataset(qa.data) && isDataset(qb.data)
  const rows = useRowDiff(projectId, datasets ? qa.data! : null, datasets ? qb.data! : null)

  const picker = (side: 'a' | 'b', cur: WeftObject | undefined, label: string) => (
    <label>
      {label}
      <select className="input" value={cur?.digest ?? (side === 'a' ? a : b)} onChange={(e) => set(side, e.target.value)}>
        {!cur && <option value={side === 'a' ? a : b}>{side === 'a' ? (a ? 'this version' : 'choose…') : b}</option>}
        {(versions.data ?? []).map((v) => (
          <option key={v.digest} value={v.digest}>
            {cap(versionWords(v))} · {relativeTime(v.created_at)}
          </option>
        ))}
      </select>
    </label>
  )

  const ready = qa.data && qb.data
  const same = !!ready && qa.data!.digest === qb.data!.digest
  const sentence = ready
    ? diffSentence({
        a: qa.data!,
        b: qb.data!,
        same,
        changed: changes.filter((c) => c.kind === 'changed').length,
        added: changes.filter((c) => c.kind === 'added').length,
        removed: changes.filter((c) => c.kind === 'removed').length,
        rows: rows.data && !rows.data.sameTable ? { a: rows.data.countA, b: rows.data.countB, added: rows.data.added.length, removed: rows.data.removed.length } : null,
      })
    : null

  return (
    <div className="page-frame detail">
      <PageHeader
        variant="detail"
        back={{ to: objectVersionsUrl(projectId, objectId), label: `Versions of ${objectId}` }}
        title={`Compare versions of ${objectId}`}
        help="evaluations.md#datasets-models-prompts-and-scorers"
        helpTopic="Assets"
        sentence={sentence ? <Sentence parts={[sentence]} size="detail" /> : undefined}
        meta={
          ready ? (
            <>
              <Link to={objectUrl(projectId, objectId, qa.data!.digest)}>Open {versionWords(qa.data!)}</Link>
              {' · '}
              <Link to={objectUrl(projectId, objectId, qb.data!.digest)}>Open {versionWords(qb.data!)}</Link>
            </>
          ) : undefined
        }
      />
      <div className="diff-pickers">
        {picker('a', qa.data, 'From')}
        <button
          type="button"
          className="qbtn sm"
          aria-label="Swap the two versions"
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
        {picker('b', qb.data, 'to')}
      </div>
      <ErrorBox error={qa.error ?? qb.error ?? versions.error} />
      {!a ? (
        <div className="quiet-empty">
          <p>Choose the version to compare from.</p>
        </div>
      ) : qa.isLoading || qb.isLoading ? (
        <Loading />
      ) : ready && !same ? (
        <>
          <Segmented label="How to show the changes" role="tabs" panelId="diff-view" value={view} onChange={(v) => set('view', v === 'changes' ? null : v)} options={VIEWS} />
          <div role="tabpanel" id="diff-view" aria-labelledby={segmentedTabId('diff-view', view)} style={{ minWidth: 0 }}>
            {view === 'changes' ? <PathChangesTable changes={changes} /> : <TextDiff left={left} right={right} mode={view} />}
          </div>
          {datasets && <RowChanges projectId={projectId} q={rows} />}
        </>
      ) : null}
    </div>
  )
}

function useRowDiff(projectId: string, a: WeftObject | null, b: WeftObject | null) {
  const da = a ? rowsDigest(a.value) : null
  const db = b ? rowsDigest(b.value) : null
  return useQuery({
    queryKey: ['dataset-row-diff', projectId, da, db],
    enabled: !!da && !!db,
    queryFn: async () => {
      if (da === db) return { sameTable: true, countA: 0, countB: 0, added: [] as TableRow[], removed: [] as TableRow[] }
      const [ra, rb] = await Promise.all([fetchAllRows(projectId, da!, 20_000), fetchAllRows(projectId, db!, 20_000)])
      const sa = new Set(ra.map((r) => r.digest))
      const sb = new Set(rb.map((r) => r.digest))
      return { sameTable: false, countA: ra.length, countB: rb.length, added: rb.filter((r) => !sa.has(r.digest)), removed: ra.filter((r) => !sb.has(r.digest)) }
    },
  })
}

function RowChanges({ projectId, q }: { projectId: string; q: ReturnType<typeof useRowDiff> }) {
  const [show, setShow] = useState<'added' | 'removed'>('added')
  if (q.isLoading) return <Loading label="Comparing rows…" />
  if (q.error) return <ErrorBox error={q.error} />
  if (!q.data) return null
  if (q.data.sameTable) return <p className="quiet-note">Both versions have the same rows.</p>
  return (
    <section className="asset-section">
      <div className="asset-section-head">
        <h2>Rows</h2>
        <Segmented
          label="Which rows"
          role="radio"
          size="sm"
          value={show}
          onChange={setShow}
          options={[
            { id: 'added', label: `Added ${q.data.added.length}` },
            { id: 'removed', label: `Removed ${q.data.removed.length}` },
          ]}
        />
      </div>
      <RowsTable projectId={projectId} rows={show === 'added' ? q.data.added : q.data.removed} what={show} />
      <p className="quiet-note">Rows are matched by their content, so an edited row shows as one removed and one added.</p>
    </section>
  )
}

function RowsTable({ projectId, rows, what }: { projectId: string; rows: TableRow[]; what: 'added' | 'removed' }) {
  const shown = rows.slice(0, 200)
  const cols = columnsOf(shown.map((r) => r.value))
  if (!rows.length) return <p className="quiet-note">No rows were {what}.</p>
  const obj = (v: Any) => (v && typeof v === 'object' && !Array.isArray(v) ? v : { value: v })
  return (
    <>
      <PlainTable caption={`Rows ${what}`} className="rows-table">
        <thead>
          <tr>
            <th scope="col" className="num">
              Row
            </th>
            {cols.map((c) => (
              <th key={c} scope="col">
                {c}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {shown.map((r) => (
            <tr key={r.digest}>
              <td className="num">{r.index + 1}</td>
              {cols.map((c) => (
                <td key={c}>
                  <CellValue v={obj(r.value)[c]} projectId={projectId} />
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </PlainTable>
      {rows.length > 200 && <p className="quiet-note">Showing the first 200 of {rows.length}.</p>}
    </>
  )
}
