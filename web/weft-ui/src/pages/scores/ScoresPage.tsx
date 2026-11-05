// Scores: every scoped score (session, agent, hand-off, turn, span) with its
// scorer, value and reason. Deleting can be undone for a few seconds.
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { v1, type Score } from '../../api/v1'
import { useProjectId } from '../../hooks/useProject'
import { fmtDate, fmtRelative } from '../../lib/format'
import { agentUrl, sessionUrl } from '../../lib/urls'
import { ErrorBox, Loading } from '../../components/common'
import { GuidedEmpty } from '../../components/ui/EmptyState'
import { AgentChip, humanScorer, ScorePill } from '../../components/ui/bits'
import { Icon } from '../../components/ui/Icons'
import { HelpLink } from '../../components/ui/PageHeader'
import { undoable } from '../../components/ui/Toasts'

const SCOPES = [
  { id: '', label: 'Every scope' },
  { id: 'session', label: 'Sessions' },
  { id: 'agent', label: 'Agents' },
  { id: 'handoff', label: 'Hand-offs' },
  { id: 'turn', label: 'Turns' },
  { id: 'span', label: 'Steps' },
]

export function ScoresPage() {
  const pid = useProjectId()
  const qc = useQueryClient()
  const [scope, setScope] = useState('')
  const [scorer, setScorer] = useState('')
  const [hidden, setHidden] = useState<Set<string>>(new Set())
  const q = useQuery({
    queryKey: ['v1', pid, 'scores', 'all', scope, scorer],
    queryFn: () => v1.scores(pid, { scope: scope || undefined, scorer: scorer || undefined, limit: 500 }),
    placeholderData: keepPreviousData,
  })
  const all = useQuery({ queryKey: ['v1', pid, 'scores', 'names'], queryFn: () => v1.scores(pid, { limit: 1000 }) })
  const names = [...new Set((all.data?.items ?? []).map((s) => s.scorer))].sort()
  const rows = (q.data?.items ?? []).filter((s) => !hidden.has(s.id))
  const del = (x: Score) =>
    undoable({
      message: `Deleted ${humanScorer(x.scorer)} score`,
      apply: () => setHidden((h) => new Set(h).add(x.id)),
      revert: () =>
        setHidden((h) => {
          const n = new Set(h)
          n.delete(x.id)
          return n
        }),
      commit: async () => {
        await api.feedbackDelete(pid, x.id)
        await qc.invalidateQueries({ queryKey: ['v1', pid] })
      },
    })
  return (
    <div className="page wide">
      <header className="page-head">
        <div className="grow">
          <h1>Scores</h1>
          <p className="page-sub">How well sessions, agents and hand-offs did, and why.</p>
        </div>
        <HelpLink doc="evaluations.md#scores" topic="Scores" />
      </header>
      {all.data?.total_count === 0 ? (
        <GuidedEmpty projectId={pid} title="No scores yet" doc="evaluations.md#scores" showTest>
          Open a session and use its <strong>Scores</strong> tab to run the goal-completion, reasoning-consistency or hand-off-quality judges, or create a monitor that scores new
          sessions automatically.
        </GuidedEmpty>
      ) : (
        <>
          <div className="row wrap" style={{ gap: 10 }}>
            <label className="row small muted">
              Scope
              <select className="input" value={scope} onChange={(e) => setScope(e.target.value)}>
                {SCOPES.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.label}
                  </option>
                ))}
              </select>
            </label>
            <label className="row small muted">
              Scorer
              <select className="input" value={scorer} onChange={(e) => setScorer(e.target.value)}>
                <option value="">Every scorer</option>
                {names.map((n) => (
                  <option key={n} value={n}>
                    {humanScorer(n)}
                  </option>
                ))}
              </select>
            </label>
          </div>
          <ErrorBox error={q.error} />
          {q.isLoading ? (
            <Loading />
          ) : !rows.length ? (
            <p className="muted">No scores match.</p>
          ) : (
            <ul className="score-list">
              {rows.map((x) => (
                <li key={x.id} className="score-item">
                  <ScorePill value={x.value} scorer={x.scorer} />
                  <span className="small">
                    {x.scope === 'agent' && x.agent ? (
                      <>
                        <AgentChip name={x.agent} to={agentUrl(pid, x.agent)} size="sm" /> in{' '}
                      </>
                    ) : x.scope === 'handoff' ? (
                      'Hand-off in '
                    ) : x.scope === 'span' ? (
                      'Step in '
                    ) : (
                      'Session '
                    )}
                    {x.session_id ? (
                      <Link className="mono" to={sessionUrl(pid, x.session_id, { tab: 'scores' })}>
                        {x.session_id}
                      </Link>
                    ) : (
                      '—'
                    )}
                  </span>
                  <span className="grow score-reason">{x.reason || <span className="faint">no reason given</span>}</span>
                  <span className="faint small" title={fmtDate(x.created_at)}>
                    {fmtRelative(x.created_at)}
                  </span>
                  <button type="button" className="icon-btn" aria-label={`Delete ${humanScorer(x.scorer)} score`} onClick={() => del(x)}>
                    <Icon name="trash" size={13} />
                  </button>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </div>
  )
}
