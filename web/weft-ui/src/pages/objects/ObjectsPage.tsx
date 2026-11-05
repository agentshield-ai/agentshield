// An asset list: Datasets, Models, Prompts, Scorers, Functions or All
// objects (DESIGN.md §7, §7.1 rule 1, §7.2 rule 3). One sentence says what
// there is and what changed last; then a grouped list, one row per asset:
// its name, one generated line ("50 rows, version 3, used by 2
// evaluations"), a quiet preview of its content, and when it last changed.
// Search, the kind filter and the sort live in the URL (?q=, ?class=, ?sort=).
// Deleting a row can be undone for a few seconds (§7.1 rule 11).
import { useMemo, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { WeftObject } from '../../api/types'
import { ErrorBox, Loading } from '../../components/common'
import { AssetEmpty } from '../../components/objects/AssetEmpty'
import { CreateDatasetModal } from '../../components/objects/CreateDatasetModal'
import { ModelFormModal, PromptFormModal, ScorerFormModal, type Created } from '../../components/objects/ObjectForms'
import { isServerScorer, KIND_META, objClass, rowsDigest, useObjects, useTableCounts, type ObjectPageKind } from '../../components/objects/objectUtils'
import { useUndoDelete } from '../../components/settings/SettingsList'
import { GroupedList, ListRow } from '../../components/ui/GroupedList'
import { Icon } from '../../components/ui/Icons'
import { PageHeader } from '../../components/ui/PageHeader'
import { SearchField } from '../../components/ui/SearchField'
import { Sentence, type SentencePart } from '../../components/ui/Sentence'
import { useProjectId } from '../../hooks/useProject'
import { fmtDate } from '../../lib/format'
import { relativeTime } from '../../lib/summaries'
import { assetFacts, assetPreview, assetsSentence, kindWords, usageCounts, type AssetPart } from '../../lib/summaries/assets'
import { leaderboardUrl, monitorUrl, objectUrl } from '../../lib/urls'
import './assets.css'

const CREATE_LABEL: Partial<Record<ObjectPageKind, string>> = {
  datasets: 'New dataset',
  prompts: 'New prompt',
  models: 'New model',
  scorers: 'New scorer',
}

const SORTS = [
  { id: 'recent', label: 'Recently changed' },
  { id: 'name', label: 'Name' },
  { id: 'versions', label: 'Most versions' },
] as const

/** Where a row goes: a leaderboard or monitor opens its own page, everything else its latest version. */
function primaryUrl(projectId: string, o: WeftObject) {
  const cls = objClass(o)
  if (cls === 'Leaderboard' || o.kind === 'leaderboard') return leaderboardUrl(projectId, o.name)
  if (cls === 'Monitor' || o.kind === 'monitor') return monitorUrl(projectId, o.name)
  return objectUrl(projectId, o.name, 'latest')
}

/** Group headings on All objects, in this order; other kinds follow by name. */
const GROUP_ORDER = ['dataset', 'model', 'prompt', 'scorer', 'evaluation', 'function', 'leaderboard', 'monitor', 'dashboard', 'automation']
const GROUP_TITLE: Record<string, string> = {
  dataset: 'Datasets',
  model: 'Models',
  prompt: 'Prompts',
  scorer: 'Scorers',
  evaluation: 'Evaluations',
  function: 'Functions',
  leaderboard: 'Leaderboards',
  monitor: 'Monitors',
  dashboard: 'Dashboards',
  automation: 'Automations',
  annotation_spec: 'Annotation fields',
  saved_view: 'Saved views',
  provider: 'Model providers',
  provider_model: 'Provider models',
  object: 'Other',
}

export function ObjectsPage({ kind }: { kind: ObjectPageKind }) {
  const projectId = useProjectId()
  const nav = useNavigate()
  const qc = useQueryClient()
  const meta = KIND_META[kind]
  const [params, setParams] = useSearchParams()
  const search = params.get('q') ?? ''
  const cls = params.get('class') ?? ''
  const sort = SORTS.find((s) => s.id === params.get('sort'))?.id ?? 'recent'
  const setParam = (k: string, v: string, empty = '') => {
    const n = new URLSearchParams(params)
    if (!v || v === empty) n.delete(k)
    else n.set(k, v)
    setParams(n, { replace: true })
  }
  const objs = useObjects(projectId, meta.filter)
  // Evaluations, to say which assets they use (the All page already has them).
  const needsEvals = kind === 'datasets' || kind === 'models' || kind === 'scorers'
  const evals = useObjects(projectId, { kinds: ['evaluation'] }, { enabled: needsEvals })
  const [creating, setCreating] = useState(false)
  const { hidden, remove } = useUndoDelete()

  const all = useMemo(() => (objs.data ?? []).filter((o) => !hidden.has(o.name)), [objs.data, hidden])
  const datasets = useMemo(() => all.filter((o) => o.kind === 'dataset'), [all])
  const counts = useTableCounts(projectId, datasets.map((o) => rowsDigest(o.value)).filter(Boolean) as string[])
  const rowsByName = useMemo(() => {
    const m = new Map<string, number>()
    if (!counts.data) return m
    for (const o of datasets) {
      const d = rowsDigest(o.value)
      if (d && counts.data.has(d)) m.set(o.name, counts.data.get(d)!)
    }
    return m
  }, [counts.data, datasets])
  const used = useMemo(() => usageCounts(kind === 'all' ? all.filter((o) => o.kind === 'evaluation') : (evals.data ?? [])), [kind, all, evals.data])

  const classes = useMemo(() => [...new Set(all.map((o) => kindWords(o)))].sort(), [all])
  const rows = useMemo(() => {
    const q = search.trim().toLowerCase()
    const xs = all.filter((o) => (!q || o.name.toLowerCase().includes(q) || kindWords(o).includes(q) || objClass(o).toLowerCase().includes(q)) && (!cls || kindWords(o) === cls))
    const key = (o: WeftObject): string | number => (sort === 'name' ? o.name.toLowerCase() : sort === 'versions' ? -(o.version_count ?? o.version + 1) : -Date.parse(o.created_at))
    return xs.slice().sort((a, b) => (key(a) < key(b) ? -1 : key(a) > key(b) ? 1 : 0))
  }, [all, search, cls, sort])

  const sentence: SentencePart[] | null = objs.data
    ? assetsSentence({ kind, objects: all, rows: rowsByName, serverScorer: (o) => isServerScorer(o as WeftObject) }).map((p: AssetPart) =>
        typeof p === 'string' ? p : { text: p.text, to: objectUrl(projectId, p.name, 'latest'), title: 'Open the latest version' },
      )
    : null

  const onCreated = (c: Created) => {
    setCreating(false)
    nav(objectUrl(projectId, c.name, c.digest))
  }
  const createLabel = CREATE_LABEL[kind]
  const createButton = createLabel ? (
    <button type="button" className="qbtn primary" onClick={() => setCreating(true)}>
      <Icon name="plus" size={14} /> {createLabel}
    </button>
  ) : null
  const empty = !!objs.data && (objs.data ?? []).length === 0

  const groups: [string, WeftObject[]][] = useMemo(() => {
    if (kind !== 'all' || sort !== 'recent') return [['', rows]]
    const by = new Map<string, WeftObject[]>()
    for (const o of rows) by.set(o.kind, [...(by.get(o.kind) ?? []), o])
    const order = (k: string) => {
      const i = GROUP_ORDER.indexOf(k)
      return i < 0 ? 100 : i
    }
    return [...by.entries()].sort((a, b) => order(a[0]) - order(b[0]) || a[0].localeCompare(b[0]))
  }, [kind, sort, rows])

  const row = (o: WeftObject) => {
    const preview = assetPreview(o)
    return (
      <ListRow
        key={o.name}
        title={o.name}
        summary={assetFacts(o, { rows: rowsByName.get(o.name), usedBy: used.get(o.name), serverScorer: o.kind === 'scorer' ? isServerScorer(o) : undefined })}
        meta={preview ? <span className="preview">{preview}</span> : undefined}
        time={relativeTime(o.created_at)}
        timeTitle={`Changed ${fmtDate(o.created_at)}`}
        to={primaryUrl(projectId, o)}
        trailing={
          <button
            type="button"
            className="qbtn sm row-action"
            aria-label={`Delete ${o.name}`}
            title={`Delete ${o.name} and its ${o.version_count ?? 1} ${(o.version_count ?? 1) === 1 ? 'version' : 'versions'}`}
            onClick={() =>
              remove(o.name, `Deleted ${o.name}`, async () => {
                await api.objectDelete(projectId, o.name)
                await qc.invalidateQueries({ queryKey: ['objects', projectId] })
              })
            }
          >
            <Icon name="trash" size={14} />
          </button>
        }
      />
    )
  }

  return (
    <div className="page-frame">
      <PageHeader
        title={meta.title}
        help="evaluations.md#datasets-models-prompts-and-scorers"
        helpTopic={meta.title}
        controls={
          empty ? undefined : (
            <span className="asset-controls">
              <SearchField label={`Search ${meta.title.toLowerCase()}`} value={search} onChange={(v) => setParam('q', v)} filterInput width={220} />
              {(kind === 'all' || classes.length > 1) && (
                <label>
                  <span className="sr-only">Show only one kind</span>
                  <select className="input" value={cls} onChange={(e) => setParam('class', e.target.value)}>
                    <option value="">Every kind</option>
                    {classes.map((c) => (
                      <option key={c} value={c}>
                        {c}
                      </option>
                    ))}
                  </select>
                </label>
              )}
              <label className="sort-pick">
                <span className="sr-only">Sort</span>
                <select className="input" value={sort} onChange={(e) => setParam('sort', e.target.value, 'recent')}>
                  {SORTS.map((s) => (
                    <option key={s.id} value={s.id}>
                      Sort: {s.label.toLowerCase()}
                    </option>
                  ))}
                </select>
              </label>
              {createButton}
            </span>
          )
        }
        sentence={empty ? undefined : sentence ? <Sentence parts={sentence} /> : undefined}
      />
      <ErrorBox error={objs.error} />
      {objs.isLoading ? (
        <Loading />
      ) : empty ? (
        <AssetEmpty kind={kind} projectId={projectId} action={createButton} />
      ) : rows.length === 0 ? (
        <div className="quiet-empty">
          <p>No {meta.title.toLowerCase()} match “{search || cls}”.</p>
          <button type="button" className="qbtn" onClick={() => setParams({}, { replace: true })}>
            Clear the search
          </button>
        </div>
      ) : (
        groups.map(([k, xs]) => (
          <GroupedList key={k || 'all'} title={k ? (GROUP_TITLE[k] ?? k) : undefined} count={k ? xs.length : undefined} label={k ? undefined : meta.title}>
            {xs.map(row)}
          </GroupedList>
        ))
      )}
      {creating && kind === 'datasets' && <CreateDatasetModal projectId={projectId} onClose={() => setCreating(false)} onCreated={onCreated} />}
      {creating && kind === 'prompts' && <PromptFormModal projectId={projectId} onClose={() => setCreating(false)} onCreated={onCreated} />}
      {creating && kind === 'models' && <ModelFormModal projectId={projectId} onClose={() => setCreating(false)} onCreated={onCreated} />}
      {creating && kind === 'scorers' && <ScorerFormModal projectId={projectId} onClose={() => setCreating(false)} onCreated={onCreated} />}
    </div>
  )
}
