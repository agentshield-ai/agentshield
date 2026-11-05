// Runs shown on an asset's page (DESIGN.md §7.2 rules 3 and 5): grouped
// lists, not tables. An evaluation run is a row with a status glyph, the
// model (or evaluation) it ran, its scores as text ("exact match 75%,
// similarity 0.79"), a quiet meta line and "565 ms · 2 h ago"; a checkbox
// beside it picks runs to compare. A traced function's run is a row with
// the question it was asked as the title, its answer (or how it failed) and
// the same time column. Words come from lib/summaries/assets.ts.
import type { Span, SpanStatus } from '../../api/types'
import type { AgentEvalRun } from '../../api/agents'
import { callCost, callLatencyMs, callStatus, callTokens, fmtCost, fmtDate } from '../../lib/format'
import { plural } from '../../lib/summaries'
import { runAnswer, runScores, runTitle, runWhen } from '../../lib/summaries/assets'
import { compareEvaluationsUrl, spanUrl } from '../../lib/urls'
import { GroupedList, ListRow } from '../ui/GroupedList'
import { StatusGlyph, type GlyphKind } from '../ui/StatusGlyph'
import { evalRunInfo, useDatasetSizes } from './evalUtils'

const GLYPH: Record<SpanStatus, { kind: GlyphKind; label: string }> = {
  success: { kind: 'ok', label: 'Finished' },
  error: { kind: 'failed', label: 'Failed' },
  running: { kind: 'running', label: 'Running' },
  descendant_error: { kind: 'warning', label: 'Finished; some steps failed' },
}
const glyphOf = (c: Span) => GLYPH[callStatus(c)] ?? GLYPH.success

/** Evaluation runs as a list. `by` names what each row is titled with: the model it ran, or the evaluation. */
export function EvalRunsList({
  projectId,
  calls,
  by,
  label,
  selected,
  onSelect,
  agentRuns,
}: {
  projectId: string
  calls: Span[]
  by: 'model' | 'evaluation'
  /** The list's accessible name (the page shows the heading). */
  label: string
  selected?: string[]
  onSelect?: (ids: string[]) => void
  /** Agent activity per run (Weft agent evals), said in the meta line. */
  agentRuns?: Map<string, AgentEvalRun>
}) {
  const infos = calls.map((c) => ({ c, i: evalRunInfo(c) }))
  const sizes = useDatasetSizes(infos.map(({ i }) => i.evalRef).filter(Boolean) as string[])
  const sel = selected ?? []
  const toggle = (id: string) => onSelect?.(sel.includes(id) ? sel.filter((x) => x !== id) : [...sel, id])
  return (
    <GroupedList label={label} className="asset-runs">
      {infos.map(({ c, i }) => {
        const g = glyphOf(c)
        const name = by === 'model' ? i.modelName : i.evalName
        const rows = i.evalRef ? sizes.data?.[i.evalRef] : null
        const tokens = callTokens(c).total
        const cost = callCost(c)
        const agents = agentRuns?.get(c.id)
        const meta = [
          rows != null ? plural(rows, 'example') : null,
          tokens ? `${tokens.toLocaleString('en-US')} tokens` : null,
          cost ? fmtCost(cost) : null,
          agents ? plural(agents.conversation_count, 'agent session') : null,
        ]
          .filter(Boolean)
          .join(' · ')
        return (
          <ListRow
            key={c.id}
            className={sel.includes(c.id) ? 'selected' : undefined}
            glyph={<StatusGlyph kind={g.kind} label={g.label} size={18} />}
            title={name}
            summary={callStatus(c) === 'error' ? runAnswer(null, c.error ?? 'the run raised an error') : runScores(c.output)}
            meta={meta || undefined}
            time={runWhen(callLatencyMs(c), c.started_at)}
            timeTitle={`Started ${fmtDate(c.started_at)}`}
            to={compareEvaluationsUrl(projectId, [c.id])}
            ariaLabel={`${name}, ${g.label.toLowerCase()}, ${runWhen(callLatencyMs(c), c.started_at)}`}
            trailing={
              onSelect ? (
                <label className="asset-pick" title="Select to compare">
                  <input type="checkbox" checked={sel.includes(c.id)} onChange={() => toggle(c.id)} aria-label={`Select the ${name} run (${runWhen(null, c.started_at)}) to compare`} />
                </label>
              ) : undefined
            }
          />
        )
      })}
    </GroupedList>
  )
}

/** Runs of a traced function or a scorer: the question as the title, the answer under it, "3.4 s · 2 h ago". */
export function SpanRunsList({ projectId, calls, label }: { projectId: string; calls: Span[]; label: string }) {
  return (
    <GroupedList label={label} className="asset-runs">
      {calls.map((c) => {
        const g = glyphOf(c)
        const when = runWhen(callLatencyMs(c), c.started_at)
        return (
          <ListRow
            key={c.id}
            glyph={<StatusGlyph kind={g.kind} label={g.label} size={18} />}
            title={runTitle(c.inputs, c.display_name || 'Run')}
            summary={runAnswer(c.output, c.error)}
            time={when}
            timeTitle={`Started ${fmtDate(c.started_at)}`}
            to={spanUrl(projectId, c.id)}
          />
        )
      })}
    </GroupedList>
  )
}
