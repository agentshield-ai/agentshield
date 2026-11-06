import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { automationsApi, type AutomationRun, type AutomationVal, type Operator, type TriggerType } from '../../api/automations'
import type { WeftObject } from '../../api/types'
import { settingsUrl } from '../../lib/urls'
import { ErrorBox, Modal } from '../common'
import { NameField, usePublish, type Created } from '../objects/ObjectForms'
import { errMsg, KIND_META, refOf, useObjects } from '../objects/objectUtils'
import { cleanAutomation, emptyAutomation, normaliseTrigger, OPERATORS, TRIGGER_LABELS, validateAutomation } from './automationUtils'
import { RunStatus } from './RunsTable'

const FEEDBACK_PRESETS = ['weft.reaction', 'weft.note', 'weft.annotation.*', 'weft.scorer.*']

const MESSAGE_HELP = 'Placeholders: {automation} {project_id} {summary} {span_id} {value} {threshold} {feedback_type} {error_rate} {total_spans} {error_spans}'

function toVal(initial?: WeftObject): AutomationVal {
  const base = emptyAutomation(initial?.name ?? '')
  if (!initial) return base
  const v = initial.value ?? {}
  return {
    ...base,
    ...v,
    name: initial.name,
    trigger: normaliseTrigger({ ...base.trigger, ...(v.trigger ?? {}) }),
    action: { ...base.action, ...(v.action ?? {}) },
  }
}

export function AutomationEditor({ projectId, initial, onClose, onSaved }: { projectId: string; initial?: WeftObject; onClose: () => void; onSaved: (c: Created) => void }) {
  const publish = usePublish(projectId)
  const [v, setV] = useState<AutomationVal>(() => toVal(initial))
  const [urlMode, setUrlMode] = useState<'url' | 'secret'>(v.action.url_secret ? 'secret' : 'url')
  const [headersText, setHeadersText] = useState(v.action.headers && Object.keys(v.action.headers).length ? JSON.stringify(v.action.headers, null, 2) : '')
  const [opsText, setOpsText] = useState((v.trigger.names ?? []).join(', '))
  const [typesText, setTypesText] = useState((v.trigger.feedback_types ?? []).join(', '))
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  const [testRun, setTestRun] = useState<AutomationRun | null>(null)

  const scorers = useObjects(projectId, KIND_META.scorers.filter)
  const monitors = useObjects(projectId, { kinds: ['monitor'] })
  const secrets = useQuery({ queryKey: ['secrets', projectId], queryFn: () => api.secretsList(projectId).then((r) => r.secrets) })
  const ops = useQuery({ queryKey: ['span-names', projectId], queryFn: () => api.spanNames(projectId).then((r) => r.items) })

  const setT = (p: Partial<AutomationVal['trigger']>) => setV((x) => ({ ...x, trigger: { ...x.trigger, ...p } }))
  const setA = (p: Partial<AutomationVal['action']>) => setV((x) => ({ ...x, action: { ...x.action, ...p } }))

  const headersErr = useMemo(() => {
    if (!headersText.trim()) return null
    try {
      const h = JSON.parse(headersText)
      return h && typeof h === 'object' && !Array.isArray(h) ? null : 'Headers must be a JSON object'
    } catch (e) {
      return errMsg(e)
    }
  }, [headersText])

  const final = (): AutomationVal => {
    const split = (s: string) =>
      s
        .split(',')
        .map((x) => x.trim())
        .filter(Boolean)
    const action = { ...v.action }
    if (urlMode === 'url') action.url_secret = ''
    else action.url = ''
    action.headers = v.action.type === 'webhook' && headersText.trim() && !headersErr ? JSON.parse(headersText) : {}
    return cleanAutomation({ ...v, action, trigger: { ...v.trigger, names: split(opsText), feedback_types: split(typesText) } })
  }
  const problem = validateAutomation(final()) ?? headersErr

  const run = async (fn: () => Promise<void>) => {
    setBusy(true)
    setErr(null)
    try {
      await fn()
    } catch (e) {
      setErr(e)
    } finally {
      setBusy(false)
    }
  }

  const t = v.trigger
  return (
    <Modal
      title={initial ? `Edit automation ${initial.name}` : 'New automation'}
      wide
      onClose={onClose}
      footer={
        <>
          <span className="hint grow">{problem ?? ''}</span>
          <button
            type="button"
            className="qbtn"
            disabled={!!problem || busy}
            title="Send a sample event to the action now"
            onClick={() => run(async () => setTestRun((await automationsApi.test(projectId, { val: final() })).run))}
          >
            Send test
          </button>
          <button type="button" className="qbtn" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="qbtn primary" disabled={!!problem || busy} onClick={() => run(async () => onSaved(await publish(v.name.trim(), final())))}>
            {busy ? 'Working…' : initial ? 'Save new version' : 'Create automation'}
          </button>
        </>
      }
    >
      <div className="form-grid">
        <NameField value={v.name} onChange={(name) => setV({ ...v, name })} locked={!!initial} placeholder="low-quality-alert" />
        <label className="field">
          Description
          <input className="input" value={v.description} onChange={(e) => setV({ ...v, description: e.target.value })} placeholder="Optional" />
        </label>
      </div>
      <label className="checkbox">
        <input type="checkbox" checked={v.enabled} onChange={(e) => setV({ ...v, enabled: e.target.checked })} /> Enabled
      </label>

      <h3 style={{ margin: '6px 0 0' }}>When</h3>
      <div className="seg" role="radiogroup" aria-label="Trigger" style={{ flex: 'none', alignSelf: 'flex-start' }}>
        {(Object.keys(TRIGGER_LABELS) as TriggerType[]).map((k) => (
          <button key={k} className={t.type === k ? 'on' : ''} onClick={() => setT({ type: k, threshold: k === 'error_rate' ? 0.1 : t.type === 'error_rate' ? 0.5 : t.threshold })}>
            {TRIGGER_LABELS[k]}
          </button>
        ))}
      </div>
      {t.type === 'score_threshold' && (
        <>
          <div className="form-grid">
            <label className="field">
              Scorer
              <input className="input mono" list="automation-scorers" value={t.scorer ?? ''} onChange={(e) => setT({ scorer: e.target.value })} placeholder="judge" />
              <datalist id="automation-scorers">
                {(scorers.data ?? []).map((o) => (
                  <option key={o.name} value={o.name} />
                ))}
              </datalist>
              <span className="hint">Scorer name (results written by monitors or Run scorer), or a full feedback type such as weft.annotation.quality.</span>
            </label>
            <label className="field">
              Score field
              <input className="input mono" value={t.path ?? ''} onChange={(e) => setT({ path: e.target.value })} placeholder="score (empty: the score itself)" />
              <span className="hint">Dotted path inside the score, e.g. score, flagged, hallucination_free. true counts as 1, false as 0.</span>
            </label>
          </div>
          <div className="form-grid">
            <label className="field">
              Comparison
              <select className="input" value={t.operator ?? 'lt'} onChange={(e) => setT({ operator: e.target.value as Operator })}>
                {OPERATORS.map((o) => (
                  <option key={o.id} value={o.id}>
                    {o.label} ({o.id})
                  </option>
                ))}
              </select>
            </label>
            <label className="field">
              Threshold
              <input className="input" type="number" step="any" value={t.threshold ?? ''} onChange={(e) => setT({ threshold: e.target.value === '' ? undefined : Number(e.target.value) })} />
            </label>
          </div>
          <label className="field">
            Only scores from monitor
            <select className="input" value={t.monitor_ref ?? ''} onChange={(e) => setT({ monitor_ref: e.target.value || null })}>
              <option value="">Any source</option>
              {(monitors.data ?? []).map((m) => (
                <option key={m.name} value={refOf(m)}>
                  {m.name}
                </option>
              ))}
            </select>
          </label>
        </>
      )}
      {t.type === 'error_rate' && (
        <>
          <label className="field">
            Span names
            <input className="input mono" list="automation-ops" value={opsText} onChange={(e) => setOpsText(e.target.value)} placeholder="Comma-separated; empty means all root spans" />
            <datalist id="automation-ops">
              {(ops.data ?? []).map((o) => (
                <option key={o.base_name} value={o.base_name} />
              ))}
            </datalist>
          </label>
          <div className="grid-3">
            <label className="field">
              Error rate at least (%)
              <input
                className="input"
                type="number"
                min={0}
                max={100}
                step="any"
                value={t.threshold != null ? Math.round(t.threshold * 10000) / 100 : ''}
                onChange={(e) => setT({ threshold: e.target.value === '' ? undefined : Number(e.target.value) / 100 })}
              />
            </label>
            <label className="field">
              Window (minutes)
              <input className="input" type="number" min={1} value={t.window_minutes ?? 60} onChange={(e) => setT({ window_minutes: Number(e.target.value) })} />
            </label>
            <label className="field">
              Minimum spans
              <input className="input" type="number" min={1} value={t.min_spans ?? 1} onChange={(e) => setT({ min_spans: Number(e.target.value) })} />
            </label>
          </div>
          <span className="hint">Checked every minute in the background (and on demand with “Check now”).</span>
        </>
      )}
      {t.type === 'feedback_created' && (
        <label className="field">
          Feedback types
          <input className="input mono" value={typesText} onChange={(e) => setTypesText(e.target.value)} placeholder="Comma-separated; empty means any feedback" />
          <span className="chips" style={{ marginTop: 4 }}>
            {FEEDBACK_PRESETS.map((p) => (
              <button key={p} type="button" className="chip selectable mono" onClick={() => setTypesText(typesText.trim() ? `${typesText}, ${p}` : p)}>
                + {p}
              </button>
            ))}
          </span>
          <span className="hint">Exact types, or a prefix ending in * (weft.reaction matches every emoji reaction).</span>
        </label>
      )}

      <h3 style={{ margin: '6px 0 0' }}>Then</h3>
      <div className="row" style={{ gap: 12, flex: 'none' }}>
        <div className="seg" role="radiogroup" aria-label="Action">
          <button className={v.action.type === 'slack' ? 'on' : ''} onClick={() => setA({ type: 'slack' })}>
            Slack message
          </button>
          <button className={v.action.type === 'webhook' ? 'on' : ''} onClick={() => setA({ type: 'webhook' })}>
            Webhook (JSON POST)
          </button>
        </div>
        <div className="seg" role="radiogroup" aria-label="URL source">
          <button className={urlMode === 'url' ? 'on' : ''} onClick={() => setUrlMode('url')}>
            URL
          </button>
          <button className={urlMode === 'secret' ? 'on' : ''} onClick={() => setUrlMode('secret')}>
            From secret
          </button>
        </div>
      </div>
      {urlMode === 'url' ? (
        <label className="field">
          {v.action.type === 'slack' ? 'Slack incoming webhook URL' : 'Webhook URL'}
          <input
            className="input mono"
            value={v.action.url ?? ''}
            onChange={(e) => setA({ url: e.target.value })}
            placeholder={v.action.type === 'slack' ? 'https://hooks.slack.com/services/…' : 'https://example.com/hooks/weft'}
          />
          <span className="hint">Stored in the object in plain text. Use a secret for URLs that embed a token.</span>
        </label>
      ) : (
        <label className="field">
          Secret holding the URL
          <input className="input mono" list="automation-secrets" value={v.action.url_secret ?? ''} onChange={(e) => setA({ url_secret: e.target.value })} placeholder="SLACK_WEBHOOK_URL" />
          <datalist id="automation-secrets">
            {[...new Set((secrets.data ?? []).filter((s) => s.kind !== 'bucket').map((s) => s.name))].map((n) => (
              <option key={n} value={n} />
            ))}
          </datalist>
          <span className="hint">
            Manage secrets in <Link to={settingsUrl(projectId, 'secrets')}>Settings → Secrets</Link>.
          </span>
        </label>
      )}
      <label className="field">
        Message (optional)
        <textarea className="input mono" rows={2} value={v.action.message ?? ''} onChange={(e) => setA({ message: e.target.value })} placeholder="Weft automation {automation} fired in {project_id}: {summary}" />
        <span className="hint">{MESSAGE_HELP}</span>
      </label>
      {v.action.type === 'webhook' && (
        <label className="field">
          Extra headers (JSON, optional)
          <textarea className="input mono" rows={2} value={headersText} onChange={(e) => setHeadersText(e.target.value)} placeholder='{"X-Api-Key": "…"}' />
          {headersErr && <span className="hint" style={{ color: 'var(--error)' }}>{headersErr}</span>}
        </label>
      )}
      <label className="field" style={{ maxWidth: 260 }}>
        Cooldown (seconds)
        <input
          className="input"
          type="number"
          min={0}
          value={v.cooldown_seconds ?? ''}
          onChange={(e) => setV({ ...v, cooldown_seconds: e.target.value === '' ? null : Number(e.target.value) })}
          placeholder={t.type === 'error_rate' ? 'window length' : '300'}
        />
        <span className="hint">Minimum time between two alerts from this automation.</span>
      </label>
      {testRun && (
        <div className={testRun.status === 'success' ? 'success-box' : 'warn-box'}>
          <RunStatus run={testRun} /> Test {testRun.status === 'success' ? 'delivered' : 'failed'}
          {testRun.response_status ? ` (HTTP ${testRun.response_status})` : ''}
          {testRun.error ? `: ${testRun.error}` : ''}
        </div>
      )}
      <ErrorBox error={err} />
    </Modal>
  )
}
