// Agent analytics (every agent, or one with `agent`), in the look of the
// redesigned pages (DESIGN.md §7.2): one sentence with the totals over the
// time range (24 hours unless the URL says otherwise), then four views:
// Overview (quiet charts and plain tables), Steps (every recorded step,
// searchable and groupable), Ratings (turns a judge model rated or tagged) and
// Evaluations. The view, range and every filter live in the URL; earlier
// tab names (dashboard, agents) open the Overview.
import { useSearchParams } from 'react-router-dom'
import { useProjectId } from '../../hooks/useProject'
import { analyticsSentence } from '../../lib/summaries/agentsTeams'
import { agentsUrlFor, agentUrl } from '../../lib/urls'
import { ErrorBox } from '../common'
import { PageHeader } from '../ui/PageHeader'
import { Segmented, segmentedTabId } from '../ui/Segmented'
import { Sentence } from '../ui/Sentence'
import { AgentEvalsView } from './AgentEvals'
import { GroupTable, MessageSearch, QuietCharts, useAnalyticsTotals, VersionsTable } from './AnalyticsOverview'
import { SignalsView } from './SignalsView'
import { SpansTable } from './SpansTable'
import './agents.css'
import '../../pages/agents/agents-teams.css'

type View = 'overview' | 'spans' | 'signals' | 'evals'
const VIEWS: { id: View; label: string; title: string }[] = [
  { id: 'overview', label: 'Overview', title: 'Charts and tables over the time range' },
  { id: 'spans', label: 'Steps', title: 'Every recorded step: search, filter and group them' },
  { id: 'signals', label: 'Ratings', title: 'Turns rated or tagged by a judge model' },
  { id: 'evals', label: 'Evaluations', title: 'Evaluations that ran these agents' },
]
const LEGACY: Record<string, View> = { dashboard: 'overview', agents: 'overview' }

const RANGE_LABEL: Record<string, string> = { '1h': '1 hour', '6h': '6 hours', '24h': '24 hours', '7d': '7 days', '30d': '30 days', all: 'All time' }
const KNOWN = ['1h', '6h', '24h', '7d', '30d', 'all']

export function AnalyticsView({ agent }: { agent?: string }) {
  const projectId = useProjectId()
  const [params, setParams] = useSearchParams()
  const raw = params.get('tab') ?? ''
  const view: View = LEGACY[raw] ?? (VIEWS.some((v) => v.id === raw) ? (raw as View) : 'overview')
  const asked = params.get('range') ?? ''
  // "All time" exists only for Steps (evaluation links open them over all time).
  const range = KNOWN.includes(asked) && (asked !== 'all' || view === 'spans') ? asked : '24h'
  const options = ['24h', '7d', '30d', ...(['1h', '6h'].includes(range) ? [range] : []), ...(view === 'spans' ? ['all'] : [])].map((id) => ({ id, label: RANGE_LABEL[id] }))
  const totals = useAnalyticsTotals(projectId, range, agent)

  const update = (patch: Record<string, string | null>) =>
    setParams(
      (p) => {
        const n = new URLSearchParams(p)
        for (const [k, v] of Object.entries(patch)) (v == null || v === '' ? n.delete(k) : n.set(k, v))
        return n
      },
      { replace: true },
    )
  const switchView = (v: View, extra: Record<string, string> = {}) => {
    // Each view keeps its own filters; only the time range carries over.
    const n = new URLSearchParams(extra)
    if (v !== 'overview') n.set('tab', v)
    if (asked && asked !== '24h' && (asked !== 'all' || v === 'spans') && !n.has('range')) n.set('range', asked)
    setParams(n)
  }

  return (
    <div className="page-frame detail analytics-page">
      <PageHeader
        variant="detail"
        back={agent ? { to: agentUrl(projectId, agent), label: agent } : { to: agentsUrlFor(projectId), label: 'Agents' }}
        title={agent ? `Analytics of ${agent}` : 'Agent analytics'}
        help="agents.md"
        helpTopic="Agent analytics"
        sentence={
          totals.data ? (
            <Sentence size="detail" parts={[analyticsSentence(totals.data, range, agent)]} />
          ) : (
            <p className="sentence s-detail sk-sentence" aria-busy="true">
              <span className="sr-only">Counting…</span>
              <span className="sk sk-line" aria-hidden />
            </p>
          )
        }
      />
      <ErrorBox error={totals.error} />
      <div className="analytics-bar">
        <Segmented label="Views" role="tabs" panelId="analytics-view" className="segmented-views" options={VIEWS} value={view} onChange={(v) => switchView(v)} />
        <Segmented label="Time range" options={options} value={range} onChange={(r) => update({ range: r === '24h' ? null : r, offset: null })} />
      </div>
      <div role="tabpanel" id="analytics-view" aria-labelledby={segmentedTabId('analytics-view', view)} className="analytics-panel">
        {view === 'overview' && (
          <>
            <QuietCharts projectId={projectId} range={range} agent={agent} />
            {!agent && (
              <section className="at-section" aria-labelledby="an-agents-h">
                <h2 id="an-agents-h" className="at-h">
                  Agents
                </h2>
                <GroupTable projectId={projectId} range={range} by="agent_name" noun="Agent" caption="Agents, with their runs, failures, time and cost" />
              </section>
            )}
            <section className="at-section" aria-labelledby="an-tools-h">
              <h2 id="an-tools-h" className="at-h">
                Tools
              </h2>
              <GroupTable
                projectId={projectId}
                range={range}
                agent={agent}
                by="tool_name"
                noun="Tool"
                caption="Tools called, with failures, time and cost"
                onPick={(k) => switchView('spans', { tool: k })}
              />
            </section>
            <section className="at-section" aria-labelledby="an-models-h">
              <h2 id="an-models-h" className="at-h">
                Models
              </h2>
              <GroupTable
                projectId={projectId}
                range={range}
                agent={agent}
                by="request_model"
                noun="Model"
                caption="Models called, with failures, time and cost"
                onPick={(k) => switchView('spans', { model: k })}
              />
            </section>
            {agent && (
              <section className="at-section" aria-labelledby="an-versions-h">
                <h2 id="an-versions-h" className="at-h">
                  Versions
                </h2>
                <VersionsTable projectId={projectId} agent={agent} />
              </section>
            )}
            <section className="at-section" aria-labelledby="an-search-h">
              <h2 id="an-search-h" className="at-h">
                Search messages
              </h2>
              <MessageSearch projectId={projectId} agent={agent} />
            </section>
          </>
        )}
        {view === 'spans' && <SpansTable projectId={projectId} params={params} update={update} agent={agent} />}
        {view === 'signals' && <SignalsView projectId={projectId} params={params} update={update} agent={agent} />}
        {view === 'evals' && <AgentEvalsView projectId={projectId} agent={agent} />}
      </div>
    </div>
  )
}
