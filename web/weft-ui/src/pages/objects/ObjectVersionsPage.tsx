// Every version of one asset (DESIGN.md §7.1 rule 1, §7.2 rule 3): a sentence
// ("support-tickets has 3 versions; the latest, version 3, was saved 5 min
// ago."), a quiet list of the versions, newest first, and a picker to compare
// two. Deleting one version can be undone for a few seconds; deleting every
// version asks first.
import { useMemo, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { ErrorBox, Loading } from '../../components/common'
import { ConfirmButton } from '../../components/objects/bits'
import { KIND_META, kindOf, rowsDigest, useObjectVersions, useTableCounts } from '../../components/objects/objectUtils'
import { useUndoDelete } from '../../components/settings/SettingsList'
import { GroupedList, ListRow } from '../../components/ui/GroupedList'
import { Icon } from '../../components/ui/Icons'
import { PageHeader } from '../../components/ui/PageHeader'
import { Sentence, type SentencePart } from '../../components/ui/Sentence'
import { useProjectId } from '../../hooks/useProject'
import { fmtBytes, fmtDate } from '../../lib/format'
import { joinClauses, plural, relativeTime } from '../../lib/summaries'
import { kindWords, versionsSentence, versionWords } from '../../lib/summaries/assets'
import { objectDiffUrl, objectUrl } from '../../lib/urls'
import './assets.css'

export function ObjectVersionsPage() {
  const projectId = useProjectId()
  const { objectId = '' } = useParams()
  const nav = useNavigate()
  const qc = useQueryClient()
  // Values too, for datasets' row counts (a few versions; the list is per asset).
  const versions = useObjectVersions(projectId, objectId, false)
  const { hidden, remove } = useUndoDelete()
  const vs = useMemo(() => (versions.data ?? []).filter((v) => !hidden.has(v.digest)), [versions.data, hidden])
  const latest = vs[0]
  const kind = latest ? kindOf(latest) : 'all'
  const listUrl = `/${projectId}/${kind === 'all' ? 'objects' : kind === 'ops' ? 'functions' : kind}`
  const counts = useTableCounts(projectId, vs.map((v) => rowsDigest(v.value)).filter(Boolean) as string[])
  const [a, setA] = useState('')
  const [b, setB] = useState('')
  const from = a || vs[1]?.digest || ''
  const to = b || vs[0]?.digest || ''
  // Who saved them, for the meta line.
  const savers = useMemo(() => [...new Set(vs.map((v) => v.user_id).filter(Boolean))] as string[], [vs])

  const sentence: SentencePart[] | null = versions.data
    ? versionsSentence(objectId, vs).map((p) => (typeof p === 'string' ? p : { text: p.text, to: objectUrl(projectId, objectId, 'latest'), title: 'Open the latest version' }))
    : null

  return (
    <div className="page-frame detail">
      <PageHeader
        variant="detail"
        back={{ to: listUrl, label: KIND_META[kind].title }}
        title={objectId}
        help="evaluations.md#datasets-models-prompts-and-scorers"
        helpTopic="Assets"
        sentence={sentence ? <Sentence parts={sentence} size="detail" /> : undefined}
        meta={latest ? `${kindWords(latest)[0].toUpperCase()}${kindWords(latest).slice(1)}${savers.length ? ` · saved by ${joinClauses(savers)}` : ''}` : undefined}
      />
      <ErrorBox error={versions.error} />
      {versions.isLoading ? (
        <Loading />
      ) : !vs.length ? (
        <div className="quiet-empty">
          <p>{objectId} was not found in this project.</p>
        </div>
      ) : (
        <>
          {vs.length > 1 && (
            <form
              className="diff-pickers"
              onSubmit={(e) => {
                e.preventDefault()
                const [x, y] = [vs.find((v) => v.digest === from), vs.find((v) => v.digest === to)]
                if (!x || !y) return
                const [lo, hi] = x.version <= y.version ? [x, y] : [y, x]
                nav(objectDiffUrl(projectId, objectId, lo.digest, hi.digest))
              }}
            >
              <label>
                Compare
                <select className="input" value={from} onChange={(e) => setA(e.target.value)}>
                  {vs.map((v) => (
                    <option key={v.digest} value={v.digest}>
                      {versionWords(v)}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                with
                <select className="input" value={to} onChange={(e) => setB(e.target.value)}>
                  {vs.map((v) => (
                    <option key={v.digest} value={v.digest}>
                      {versionWords(v)}
                    </option>
                  ))}
                </select>
              </label>
              <button type="submit" className="qbtn" disabled={from === to}>
                Compare
              </button>
            </form>
          )}
          <GroupedList title="Versions" count={vs.length}>
            {vs.map((v) => {
              const rd = rowsDigest(v.value)
              const rows = rd ? counts.data?.get(rd) : undefined
              const aliases = v.aliases.filter((x) => x !== 'latest')
              const facts = [rows != null ? plural(rows, 'row') : null, fmtBytes(v.size_bytes), v.user_id ? `saved by ${v.user_id}` : null].filter(Boolean).join(', ')
              const labels = [...aliases.map((x) => `alias ${x}`), ...(v.tags ?? []).map((t) => `#${t}`)].join(' · ')
              const n = v.version + 1
              return (
                <ListRow
                  key={v.digest}
                  title={`${versionWords(v)[0].toUpperCase()}${versionWords(v).slice(1)}${v.is_latest ? ' · latest' : ''}`}
                  summary={facts}
                  meta={labels || undefined}
                  time={relativeTime(v.created_at)}
                  timeTitle={`Saved ${fmtDate(v.created_at)}`}
                  to={objectUrl(projectId, objectId, v.digest)}
                  trailing={
                    vs.length > 1 ? (
                      <button
                        type="button"
                        className="qbtn sm row-action"
                        aria-label={`Delete version ${n}`}
                        title={`Delete version ${n}; the others are kept`}
                        onClick={() =>
                          remove(v.digest, `Deleted version ${n} of ${objectId}`, async () => {
                            await api.objectDelete(projectId, objectId, [v.digest])
                            await qc.invalidateQueries({ queryKey: ['obj-versions', projectId, objectId] })
                            await qc.invalidateQueries({ queryKey: ['objects', projectId] })
                          })
                        }
                      >
                        <Icon name="trash" size={14} />
                      </button>
                    ) : undefined
                  }
                />
              )
            })}
          </GroupedList>
          <p className="quiet-note">
            <ConfirmButton
              className="qbtn sm plain danger"
              title={`Delete every version of ${objectId}?`}
              body={`All ${vs.length} versions are deleted. Evaluations, monitors and spans that point at ${objectId} will no longer find it.`}
              confirmLabel="Delete all"
              onConfirm={async () => {
                await api.objectDelete(projectId, objectId)
                await qc.invalidateQueries({ queryKey: ['objects', projectId] })
                nav(listUrl)
              }}
            >
              Delete {objectId} and every version
            </ConfirmButton>
          </p>
        </>
      )}
    </div>
  )
}
