import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { api } from '../../api/client'
import { agentUrl, conversationUrl, signalsApi, type SignalKind, type SignalPreset, type SignalResult, type SignalRunRes } from '../../api/agents'
import { fmtDate, fmtNumber, fmtRelative, truncate } from '../../lib/format'
import { callUrl } from '../../lib/urls'
import { Empty, ErrorBox, Modal, Pager, Spinner } from '../common'
import { windowStart } from './agentUtils'
import { RangePicker, ScorePill, Sparkline, TOOLTIP_STYLE } from './bits'
import {
  fmtScore,
  parseSignalsVal,
  presetConfig,
  SIGNALS_OBJECT_ID,
  signalId,
  signalsVal,
  toSpec,
  trendBuckets,
  validateSignal,
  type SignalConfig,
} from './signalUtils'
import './agents.css'

const LIMIT = 50
const AXIS = { fontSize: 10, fill: 'var(--text-3)' }

type Update = (patch: Record<string, string | null>) => void

/** The project's signal configuration (the `agent-signals` object). */
export function useSignalConfig(projectId: string) {
  const qc = useQueryClient()
  const q = useQuery({
    queryKey: ['obj', projectId, SIGNALS_OBJECT_ID],
    queryFn: async () => {
      const r = await api.objsQuery({ project_id: projectId, filter: { object_ids: [SIGNALS_OBJECT_ID], latest_only: true }, limit: 1 })
      const o = r.objs.find((x) => !x.deleted_at)
      return o ? parseSignalsVal(o.val) : { signals: [] as SignalConfig[], model: undefined as string | undefined, auto: true }
    },
  })
  const save = useMutation({
    mutationFn: (next: { signals: SignalConfig[]; model?: string; auto?: boolean }) =>
      api.objCreate(projectId, SIGNALS_OBJECT_ID, signalsVal(next.signals, next.model, next.auto ?? q.data?.auto ?? true)),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['obj', projectId, SIGNALS_OBJECT_ID] }),
  })
  return { q, save, signals: q.data?.signals ?? [], model: q.data?.model, auto: q.data?.auto ?? true }
}

function NewSignalModal({ existing, presets, onAdd, onClose }: { existing: SignalConfig[]; presets: SignalPreset[]; onAdd: (s: SignalConfig[]) => void; onClose: () => void }) {
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [custom, setCustom] = useState({ name: '', kind: 'tag' as SignalKind, description: '' })
  const ids = existing.map((s) => s.id)
  const customCfg: SignalConfig = { id: signalId(custom.name), name: custom.name.trim(), kind: custom.kind, description: custom.description.trim(), enabled: true }
  const customErr = custom.name || custom.description ? validateSignal(customCfg, ids) : null
  const toggle = (id: string) =>
    setPicked((p) => {
      const n = new Set(p)
      if (n.has(id)) n.delete(id)
      else n.add(id)
      return n
    })
  const group = (kind: SignalKind) => (
    <div className="col" style={{ gap: 6 }}>
      {presets
        .filter((p) => p.kind === kind)
        .map((p) => {
          const have = ids.includes(p.id)
          return (
            <label key={p.id} className={`signal-preset ${picked.has(p.id) ? 'on' : ''} ${have ? 'have' : ''}`}>
              <input type="checkbox" disabled={have} checked={have || picked.has(p.id)} onChange={() => toggle(p.id)} />
              <span className="col" style={{ gap: 2 }}>
                <b>{p.name}</b>
                <span className="small muted">{p.description}</span>
                {have && <span className="small faint">Already added</span>}
              </span>
            </label>
          )
        })}
    </div>
  )
  const canAdd = picked.size > 0 || (custom.name.trim() && !customErr)
  return (
    <Modal
      title="Create signals"
      wide
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button
            className="btn primary"
            disabled={!canAdd}
            onClick={() => {
              const add = presets.filter((p) => picked.has(p.id)).map(presetConfig)
              if (custom.name.trim() && !customErr) add.push(customCfg)
              onAdd(add)
            }}
          >
            Add {picked.size + (custom.name.trim() && !customErr ? 1 : 0) || ''} signal{picked.size + (custom.name.trim() ? 1 : 0) === 1 ? '' : 's'}
          </button>
        </>
      }
    >
      <div className="col" style={{ gap: 14 }}>
        <span className="small muted">Signals score each turn with an LLM judge. Tags flag turns that match; ratings score turns from 0 (bad) to 1 (good). Results are stored as feedback on the turn, with confidence and a reason.</span>
        <div className="grid-2">
          <div className="col" style={{ gap: 6 }}>
            <h3>Tags</h3>
            {group('tag')}
          </div>
          <div className="col" style={{ gap: 6 }}>
            <h3>Ratings</h3>
            {group('rating')}
          </div>
        </div>
        <div className="col" style={{ gap: 6 }}>
          <h3>Custom signal</h3>
          <div className="row wrap">
            <input className="input" style={{ width: 220 }} placeholder="Name, e.g. Asks for refund" value={custom.name} onChange={(e) => setCustom({ ...custom, name: e.target.value })} aria-label="Signal name" />
            <div className="seg" role="group" aria-label="Signal type">
              {(['tag', 'rating'] as SignalKind[]).map((k) => (
                <button key={k} className={custom.kind === k ? 'on' : ''} aria-pressed={custom.kind === k} onClick={() => setCustom({ ...custom, kind: k })}>
                  {k === 'tag' ? 'Tag' : 'Rating'}
                </button>
              ))}
            </div>
            {custom.name && <span className="small faint mono">id: {customCfg.id || '—'}</span>}
          </div>
          <textarea
            className="input"
            rows={3}
            placeholder={custom.kind === 'tag' ? 'When should the judge apply this tag?' : 'What does 1 mean, and what does 0 mean?'}
            value={custom.description}
            onChange={(e) => setCustom({ ...custom, description: e.target.value })}
            aria-label="Signal description"
          />
          {customErr && <span className="small" style={{ color: 'var(--error)' }}>{customErr}</span>}
        </div>
      </div>
    </Modal>
  )
}

function ActiveSignalsModal({ signals, onSave, onClose }: { signals: SignalConfig[]; onSave: (s: SignalConfig[]) => void; onClose: () => void }) {
  const [list, setList] = useState(signals)
  const [editing, setEditing] = useState<string | null>(null)
  return (
    <Modal
      title="Active signals"
      wide
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" onClick={() => onSave(list)}>
            Save
          </button>
        </>
      }
    >
      {list.length === 0 ? (
        <Empty>No signals yet.</Empty>
      ) : (
        <div className="col" style={{ gap: 8 }}>
          {list.map((s) => (
            <div key={s.id} className="signal-row">
              <div className="row">
                <label className="checkbox">
                  <input type="checkbox" checked={s.enabled} onChange={(e) => setList(list.map((x) => (x.id === s.id ? { ...x, enabled: e.target.checked } : x)))} aria-label={`Enable ${s.name}`} />
                </label>
                <span className={`badge ${s.kind === 'tag' ? 'accent' : ''}`}>{s.kind}</span>
                <b className="grow">{s.name}</b>
                <span className="mono small faint">{s.id}</span>
                {s.sample_rate != null && s.sample_rate < 1 && <span className="badge">{Math.round(s.sample_rate * 100)}%</span>}
                <button className="btn ghost sm" onClick={() => setEditing(editing === s.id ? null : s.id)}>
                  {editing === s.id ? 'Done' : 'Edit'}
                </button>
                <button className="btn ghost sm" aria-label={`Delete ${s.name}`} onClick={() => setList(list.filter((x) => x.id !== s.id))}>
                  Delete
                </button>
              </div>
              {editing === s.id ? (
                <>
                  <textarea className="input" rows={3} value={s.description} onChange={(e) => setList(list.map((x) => (x.id === s.id ? { ...x, description: e.target.value } : x)))} aria-label="Description" />
                  <label className="small muted row" style={{ gap: 6 }}>
                    Score
                    <input
                      className="input sm"
                      type="number"
                      min={0}
                      max={100}
                      step={5}
                      style={{ width: 70 }}
                      value={Math.round((s.sample_rate ?? 1) * 100)}
                      onChange={(e) => setList(list.map((x) => (x.id === s.id ? { ...x, sample_rate: Math.max(0, Math.min(100, Number(e.target.value) || 0)) / 100 } : x)))}
                      aria-label="Sample rate (percent)"
                    />
                    % of new turns automatically. Changing the description makes a new version that scores turns again.
                  </label>
                </>
              ) : (
                <div className="small muted">{s.description}</div>
              )}
            </div>
          ))}
        </div>
      )}
    </Modal>
  )
}

function RunPanel({
  projectId,
  agent,
  signals,
  model,
  auto,
  onModel,
  onAuto,
  onDone,
}: {
  projectId: string
  agent?: string
  signals: SignalConfig[]
  model?: string
  auto: boolean
  onModel: (m: string) => void
  onAuto: (on: boolean) => void
  onDone: () => void
}) {
  const models = useQuery({ queryKey: ['llm-models'], queryFn: api.llmModels, staleTime: 60_000 })
  const options = useMemo(() => {
    const ms = (models.data?.models ?? []).filter((m) => m.available)
    if (!ms.some((m) => m.id === 'mock/json')) ms.push({ id: 'mock/json', provider: 'mock', label: 'Mock JSON judge (offline)', available: true, max_tokens: 4096 })
    return ms
  }, [models.data])
  const chosen = model && options.some((m) => m.id === model) ? model : (options.find((m) => m.provider !== 'mock')?.id ?? options[0]?.id ?? '')
  const [limit, setLimit] = useState(20)
  const [rescore, setRescore] = useState(false)
  const [res, setRes] = useState<SignalRunRes | null>(null)
  const enabled = signals.filter((s) => s.enabled)
  const run = useMutation({
    mutationFn: () => signalsApi.run({ project_id: projectId, signals: enabled.map(toSpec), model: chosen, agent_name: agent, limit, rescore }),
    onSuccess: (r) => {
      setRes(r)
      // Remember the judge so new turns are scored with it too.
      if (model !== chosen) onModel(chosen)
      onDone()
    },
  })
  const errors = [...new Set((res?.results ?? []).filter((r) => r.error).map((r) => r.error!))]
  return (
    <div className="card">
      <div className="card-body col" style={{ gap: 8 }}>
        <div className="row wrap">
          <b className="grow">Score recent turns{agent ? ` of ${agent}` : ''}</b>
          <label className="small muted row" style={{ gap: 4 }}>
            Judge
            <select className="input sm" value={chosen} onChange={(e) => onModel(e.target.value)} aria-label="Judge model">
              {options.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.label || m.id}
                </option>
              ))}
            </select>
          </label>
          <label className="small muted row" style={{ gap: 4 }}>
            Turns
            <select className="input sm" value={limit} onChange={(e) => setLimit(Number(e.target.value))} aria-label="Number of turns">
              {[10, 20, 50, 100, 200].map((n) => (
                <option key={n} value={n}>
                  {n} most recent
                </option>
              ))}
            </select>
          </label>
          <label className="checkbox small" title="Score turns again even if this signal already scored them">
            <input type="checkbox" checked={rescore} onChange={(e) => setRescore(e.target.checked)} /> Re-score
          </label>
          <button className="btn primary sm" disabled={!enabled.length || !chosen || run.isPending} onClick={() => run.mutate()}>
            {run.isPending ? 'Scoring…' : `Run ${enabled.length} signal${enabled.length === 1 ? '' : 's'}`}
          </button>
        </div>
        <label className="checkbox small" title="Active signals score each agent turn when it finishes, with the judge chosen here">
          <input type="checkbox" checked={auto && !!model} disabled={!model} onChange={(e) => onAuto(e.target.checked)} /> Score new turns automatically
          {!model && <span className="faint"> (run once, or pick a judge, to enable)</span>}
        </label>
        <ErrorBox error={run.error} />
        {res && (
          <div className="small">
            Checked {fmtNumber(res.turns)} turn{res.turns === 1 ? '' : 's'}: <b>{fmtNumber(res.scored)}</b> new result{res.scored === 1 ? '' : 's'}
            {res.skipped > 0 && <> · skipped {fmtNumber(res.skipped)} already scored</>}
            {res.failed > 0 && (
              <span style={{ color: 'var(--error)' }}>
                {' '}
                · {fmtNumber(res.failed)} failed{errors.length ? `: ${truncate(errors[0], 160)}` : ''}
              </span>
            )}
          </div>
        )}
      </div>
    </div>
  )
}

/**
 * Signals: tag and rating results on turns (Weave's Agents → Signals tab),
 * with the active-signal drawer, preset/custom signal creation, a judge run,
 * a score volume timeline and per-signal trends.
 */
export function SignalsView({ projectId, params, update, agent }: { projectId: string; params: URLSearchParams; update: Update; agent?: string }) {
  const qc = useQueryClient()
  const range = params.get('range') ?? '7d'
  const since = useMemo(() => windowStart(range), [range])
  const signalF = params.get('signal') ?? ''
  const kindF = (params.get('kind') ?? '') as '' | SignalKind
  const agentF = agent ?? params.get('agent') ?? ''
  const minF = params.get('min') ?? ''
  const maxF = params.get('max') ?? ''
  const offset = Math.max(0, Number(params.get('offset') ?? 0) || 0)
  const [modal, setModal] = useState<null | 'new' | 'active'>(null)
  const cfg = useSignalConfig(projectId)
  const presets = useQuery({ queryKey: ['signal-presets'], queryFn: signalsApi.presets, staleTime: Infinity })
  const filters = {
    project_id: projectId,
    started_after: since,
    agent_name: agentF || undefined,
    signal: signalF || undefined,
    kind: kindF || undefined,
    min_score: minF !== '' ? Number(minF) : undefined,
    max_score: maxF !== '' ? Number(maxF) : undefined,
  }
  const page = useQuery({
    queryKey: ['signal-results', filters, offset],
    queryFn: () => signalsApi.query({ ...filters, limit: LIMIT, offset }),
    placeholderData: keepPreviousData,
  })
  // All results in the window (unpaged) for trends and the volume timeline.
  const all = useQuery({
    queryKey: ['signal-results-all', projectId, since, agentF],
    queryFn: () => signalsApi.query({ project_id: projectId, started_after: since, agent_name: agentF || undefined, limit: 5000 }),
  })
  const allResults = all.data?.results ?? []
  const trends = useMemo(() => {
    const m = new Map<string, SignalResult[]>()
    for (const r of allResults) m.set(`${r.kind}:${r.signal}`, [...(m.get(`${r.kind}:${r.signal}`) ?? []), r])
    const out = new Map<string, ReturnType<typeof trendBuckets>>()
    for (const [k, rs] of m) out.set(k, trendBuckets(rs, new Date(since), new Date(), 16))
    return out
  }, [allResults, since])
  const filteredForTimeline = allResults.filter((r) => (!signalF || r.signal === signalF) && (!kindF || r.kind === kindF))
  const timeline = useMemo(() => {
    const rs = trendBuckets(filteredForTimeline.filter((r) => r.kind === 'rating'), new Date(since), new Date(), 30)
    const ts = trendBuckets(filteredForTimeline.filter((r) => r.kind === 'tag'), new Date(since), new Date(), 30)
    return rs.map((p, i) => ({ t: p.t, ratings: p.count, tags: ts[i].value ?? 0 }))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [all.data, signalF, kindF, since])
  const summary = all.data?.summary ?? []
  const signalNames = [...new Set(summary.map((s) => s.signal))].sort()
  const agentNames = [...new Set(allResults.map((r) => r.agent_name).filter((a): a is string => !!a))].sort()
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ['signal-results'] })
    void qc.invalidateQueries({ queryKey: ['signal-results-all'] })
    void qc.invalidateQueries({ queryKey: ['agent-span-signals'] })
    void qc.invalidateQueries({ queryKey: ['agents-dash-signals'] })
  }
  const nActive = cfg.signals.filter((s) => s.enabled).length
  const results = page.data?.results ?? []
  const setFilter = (patch: Record<string, string | null>) => update({ ...patch, offset: null })

  return (
    <div className="col" style={{ gap: 12 }}>
      <div className="toolbar row wrap">
        <button className="btn sm" onClick={() => setModal('active')} disabled={cfg.q.isLoading}>
          {nActive} active signal{nActive === 1 ? '' : 's'}
        </button>
        <button className="btn sm primary" onClick={() => setModal('new')}>
          + New signal
        </button>
        <span className="grow" />
        <RangePicker value={range} onChange={(r) => setFilter({ range: r === '7d' ? null : r })} />
      </div>
      <ErrorBox error={cfg.q.error ?? cfg.save.error} />
      {cfg.signals.length > 0 ? (
        <RunPanel
          projectId={projectId}
          agent={agent}
          signals={cfg.signals}
          model={cfg.model}
          auto={cfg.auto}
          onModel={(m) => cfg.save.mutate({ signals: cfg.signals, model: m })}
          onAuto={(on) => cfg.save.mutate({ signals: cfg.signals, model: cfg.model, auto: on })}
          onDone={refresh}
        />
      ) : (
        !cfg.q.isLoading && (
          <div className="info-box">
            No signals are set up yet. Add presets such as <b>Frustrated user</b>, <b>Task success</b> or <b>Response quality</b> with <b>+ New signal</b>, then score recent turns with a judge model.
          </div>
        )
      )}
      {summary.length > 0 && (
        <div className="signal-cards">
          {summary.map((s) => {
            const key = `${s.kind}:${s.signal}`
            const on = signalF === s.signal && (!kindF || kindF === s.kind)
            return (
              <button key={key} className={`signal-card ${on ? 'on' : ''}`} onClick={() => setFilter(on ? { signal: null, kind: null } : { signal: s.signal, kind: s.kind })} aria-pressed={on}>
                <div className="row small">
                  <span className={`badge ${s.kind === 'tag' ? 'accent' : ''}`}>{s.kind}</span>
                  <span className="truncate grow" style={{ textAlign: 'left' }}>
                    {s.signal_name ?? s.signal}
                  </span>
                </div>
                <div className="row" style={{ alignItems: 'flex-end' }}>
                  <div className="col grow" style={{ gap: 0, alignItems: 'flex-start' }}>
                    <span className="signal-card-value">{s.kind === 'rating' ? fmtScore(s.avg) : fmtNumber(s.matched)}</span>
                    <span className="small faint">{s.kind === 'rating' ? `avg of ${fmtNumber(s.count)}` : `turns tagged`}</span>
                  </div>
                  <Sparkline points={trends.get(key) ?? []} kind={s.kind} />
                </div>
              </button>
            )
          })}
        </div>
      )}
      <div className="row wrap toolbar">
        <label className="small muted row" style={{ gap: 4 }}>
          Scorer
          <select className="input sm" value={signalF} onChange={(e) => setFilter({ signal: e.target.value || null })} aria-label="Scorer">
            <option value="">All</option>
            {signalNames.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
        </label>
        <label className="small muted row" style={{ gap: 4 }}>
          Type
          <select className="input sm" value={kindF} onChange={(e) => setFilter({ kind: e.target.value || null })} aria-label="Type">
            <option value="">All</option>
            <option value="rating">Ratings</option>
            <option value="tag">Tags</option>
          </select>
        </label>
        {!agent && (
          <label className="small muted row" style={{ gap: 4 }}>
            Agent
            <select className="input sm" value={agentF} onChange={(e) => setFilter({ agent: e.target.value || null })} aria-label="Agent">
              <option value="">All</option>
              {[...new Set([...agentNames, ...(agentF ? [agentF] : [])])].map((a) => (
                <option key={a} value={a}>
                  {a}
                </option>
              ))}
            </select>
          </label>
        )}
        <label className="small muted row" style={{ gap: 4 }}>
          Score
          <input className="input sm" type="number" min={0} max={1} step={0.1} style={{ width: 64 }} placeholder="min" value={minF} onChange={(e) => setFilter({ min: e.target.value || null })} aria-label="Minimum score" />
          –
          <input className="input sm" type="number" min={0} max={1} step={0.1} style={{ width: 64 }} placeholder="max" value={maxF} onChange={(e) => setFilter({ max: e.target.value || null })} aria-label="Maximum score" />
        </label>
        <span className="grow" />
        {page.isFetching && <Spinner />}
        <span className="small muted">{fmtNumber(page.data?.total_count ?? 0)} results</span>
      </div>
      {filteredForTimeline.length > 0 && (
        <div className="chart-card" aria-label="Score volume timeline">
          <div className="row small muted">
            <span className="grow">Score volume</span>
            <span>
              <span className="legend-swatch" style={{ background: 'var(--c2)' }} /> ratings evaluated <span className="legend-swatch" style={{ background: 'var(--c4)', marginLeft: 8 }} /> tags matched
            </span>
          </div>
          <div style={{ height: 90 }}>
            <ResponsiveContainer width="100%" height="100%">
              <BarChart data={timeline} margin={{ top: 4, right: 4, bottom: 0, left: 0 }}>
                <CartesianGrid vertical={false} stroke="var(--border)" />
                <XAxis dataKey="t" tick={AXIS} minTickGap={40} axisLine={false} tickLine={false} tickFormatter={(iso: string) => new Date(iso).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit' })} />
                <YAxis tick={AXIS} allowDecimals={false} width={28} axisLine={false} tickLine={false} />
                <Tooltip contentStyle={TOOLTIP_STYLE} labelFormatter={(iso) => new Date(String(iso)).toLocaleString()} cursor={{ fill: 'var(--panel-2)' }} />
                <Bar dataKey="ratings" name="Ratings" stackId="s" fill="var(--c2)" isAnimationActive={false} />
                <Bar dataKey="tags" name="Tags matched" stackId="s" fill="var(--c4)" isAnimationActive={false} />
              </BarChart>
            </ResponsiveContainer>
          </div>
        </div>
      )}
      <ErrorBox error={page.error} />
      {page.isLoading ? (
        <Spinner label="Loading signal results…" />
      ) : results.length === 0 ? (
        <Empty>{summary.length ? 'No results match these filters.' : 'No signal results in this period. Tag signals only list turns they matched.'}</Empty>
      ) : (
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>Type</th>
                <th>Scorer</th>
                <th>Last message</th>
                {!agent && <th>Agent</th>}
                <th>Score</th>
                <th>Reason</th>
                <th>Trend</th>
                <th>When</th>
              </tr>
            </thead>
            <tbody>
              {results.map((r) => (
                <tr key={`${r.feedback_id}-${r.kind}-${r.signal}`}>
                  <td>
                    <span className={`badge ${r.kind === 'tag' ? 'accent' : ''}`}>{r.kind}</span>
                  </td>
                  <td className="small" title={r.signal}>{r.signal_name && r.signal === r.signal_id ? r.signal_name : <span className="mono">{r.signal}</span>}</td>
                  <td style={{ maxWidth: 320 }}>
                    {r.conversation_id ? (
                      <Link to={conversationUrl(projectId, r.conversation_id)} className="small mono">
                        {truncate(r.conversation_name || r.conversation_id, 40)}
                      </Link>
                    ) : (
                      <Link to={callUrl(projectId, r.span_id)} className="small mono">
                        turn {r.span_id.slice(0, 8)}
                      </Link>
                    )}
                    {r.turn_status === 'ERROR' && (
                      <span className="badge error" style={{ marginLeft: 6 }}>
                        error
                      </span>
                    )}
                    {r.last_message && (
                      <div className="small muted truncate" title={r.last_message.text}>
                        <span className="faint">{r.last_message.role === 'user_message' ? 'user: ' : 'assistant: '}</span>
                        {r.last_message.text}
                      </div>
                    )}
                  </td>
                  {!agent && (
                    <td className="small">
                      {r.agent_name ? <Link to={agentUrl(projectId, r.agent_name)}>{r.agent_name}</Link> : <span className="faint">—</span>}
                      {r.agent_version && <span className="faint mono"> {r.agent_version}</span>}
                    </td>
                  )}
                  <td>
                    {r.kind === 'rating' ? (
                      <ScorePill value={r.score} confidence={r.confidence} reason={r.reason} />
                    ) : (
                      <div className="chips">
                        {r.tags.map((t) => (
                          <span key={t} className="chip tag-chip">
                            {t}
                          </span>
                        ))}
                      </div>
                    )}
                    {r.confidence != null && <div className="small faint">conf. {Math.round(r.confidence * 100)}%</div>}
                  </td>
                  <td className="small muted" style={{ maxWidth: 280 }}>
                    <div className="clamp-2" title={r.reason ?? undefined}>
                      {r.reason ?? '—'}
                    </div>
                  </td>
                  <td>
                    <Sparkline points={trends.get(`${r.kind}:${r.signal}`) ?? []} kind={r.kind} />
                  </td>
                  <td className="small" title={`Turn ${fmtDate(r.turn_started_at)} · scored ${fmtDate(r.created_at)}`} style={{ whiteSpace: 'nowrap' }}>
                    {fmtRelative(r.turn_started_at ?? r.created_at)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {(page.data?.total_count ?? 0) > LIMIT && (
        <div className="row" style={{ justifyContent: 'flex-end' }}>
          <Pager offset={offset} limit={LIMIT} total={page.data?.total_count ?? null} onChange={(o) => update({ offset: o ? String(o) : null })} />
        </div>
      )}
      {modal === 'new' && (
        <NewSignalModal
          existing={cfg.signals}
          presets={presets.data?.presets ?? []}
          onClose={() => setModal(null)}
          onAdd={(add) => cfg.save.mutate({ signals: [...cfg.signals, ...add], model: cfg.model }, { onSuccess: () => setModal(null) })}
        />
      )}
      {modal === 'active' && (
        <ActiveSignalsModal signals={cfg.signals} onClose={() => setModal(null)} onSave={(list) => cfg.save.mutate({ signals: list, model: cfg.model }, { onSuccess: () => setModal(null) })} />
      )}
    </div>
  )
}
