// The raw span views and the asset pages on Weft's own API (docs/weft/API.md,
// DESIGN.md §10): spans, objects, tables, feedback and an evaluation are
// written through /api/v1, then each page that reads them must load without
// an error box or a console error.
import { expect, test, type Page } from '@playwright/test'
import { watchErrors } from './helpers'

const base = () => test.info().project.use.baseURL!
let P = ''
let root = ''
let runId = ''
const span = (k: string) => `0190e2e0-0000-7000-8000-${k.padStart(12, '0')}`

async function post(path: string, body: unknown) {
  const r = await fetch(base() + path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  if (!r.ok) throw new Error(`${path} ${r.status} ${await r.text()}`)
  return r.json()
}

async function save(name: string, value: unknown) {
  return (await post('/api/v1/objects/save', { project_id: P, name, value })) as { name: string; digest: string; ref: string }
}

test.beforeAll(async () => {
  P = `e2e${Date.now().toString(36)}/assets`
  const fn = await save('answer', { _type: 'Op', name: 'answer', code: 'def answer(question):\n    return "42"\n', language: 'python' })
  const t0 = Date.now() - 60_000
  const at = (ms: number) => new Date(t0 + ms).toISOString()
  root = span('1')
  await post('/api/v1/spans/batch', {
    project_id: P,
    items: [
      { span: { id: root, name: fn.ref, started_at: at(0), ended_at: at(900), inputs: { question: 'meaning of life?' }, output: '42', session_id: 'conv-1', turn_id: root } },
      {
        span: {
          id: span('2'),
          trace_id: root,
          parent_id: root,
          name: 'openai.chat.completions.create',
          started_at: at(100),
          ended_at: at(600),
          inputs: { model: 'mock/echo', messages: [{ role: 'user', content: 'meaning of life?' }] },
          output: { choices: [{ index: 0, message: { role: 'assistant', content: '42' }, finish_reason: 'stop' }] },
          summary: { usage: { 'gpt-4o-mini': { requests: 1, prompt_tokens: 12, completion_tokens: 3, total_tokens: 15 } } },
          session_id: 'conv-1',
        },
      },
      { span: { id: span('3'), trace_id: root, parent_id: root, name: 'lookup', started_at: at(650), ended_at: at(800), inputs: { q: 'x' }, error: 'KeyError: x' } },
    ],
  })
  await post('/api/v1/feedback/create', { project_id: P, ref: `weft:///${P}/span/${root}`, feedback_type: 'weft.reaction', payload: { emoji: '👍' } })
  const table = (await post('/api/v1/tables/create', { project_id: P, rows: [{ question: 'a', expected: 'a' }, { question: 'b', expected: 'c' }] })) as { ref: string }
  const ds = await save('qa', { _type: 'Dataset', _class_name: 'Dataset', _bases: ['Object', 'BaseModel'], name: 'qa', description: 'two rows', rows: table.ref })
  const model = await save('echo-model', {
    _type: 'LLMStructuredCompletionModel',
    _class_name: 'LLMStructuredCompletionModel',
    _bases: ['Model', 'Object', 'BaseModel'],
    name: 'echo-model',
    llm_model_id: 'mock/echo',
    default_params: { messages_template: [{ role: 'user', content: '{question}' }], temperature: 0 },
  })
  const scorer = await save('exact', { _type: 'ExactMatchScorer', _class_name: 'ExactMatchScorer', _bases: ['Scorer', 'Object', 'BaseModel'], name: 'exact', target_column: 'expected' })
  const ev = await save('qa-eval', { _type: 'Evaluation', _class_name: 'Evaluation', _bases: ['Object', 'BaseModel'], name: 'qa-eval', dataset: ds.ref, scorers: [scorer.ref], trials: 1 })
  await save('json-ok', { _type: 'JSONValidScorer', _class_name: 'JSONValidScorer', _bases: ['Scorer', 'Object', 'BaseModel'], name: 'json-ok' })
  await save('health', {
    _class_name: 'Dashboard',
    _bases: ['Object', 'BaseModel'],
    name: 'Health',
    time_range: 'all',
    filter: { names: ['answer'] },
    panels: [{ id: 'p1', title: 'Spans by name', chart: 'table', metrics: [{ agg: 'count', name: 'Spans' }], group_by: 'name', width: 3 }],
  })
  await save('greeting', { _type: 'StringPrompt', _class_name: 'StringPrompt', _bases: ['Prompt', 'Object', 'BaseModel'], name: 'greeting', content: 'Hello {name}' })
  await save('quality', {
    _type: 'AnnotationSpec',
    _class_name: 'AnnotationSpec',
    _bases: ['Object', 'BaseModel'],
    name: 'quality',
    field_schema: { type: 'integer', minimum: 1, maximum: 5 },
  })
  await save('lb', {
    _type: 'Leaderboard',
    _class_name: 'Leaderboard',
    _bases: ['Object', 'BaseModel'],
    name: 'lb',
    description: '',
    columns: [{ evaluation_object_ref: ev.ref, scorer_name: 'exact', summary_metric_path: 'true_fraction', should_minimize: false }],
  })
  await save('mon', { _type: 'Monitor', _class_name: 'Monitor', _bases: ['Object', 'BaseModel'], name: 'mon', active: false, sampling_rate: 1, names: ['answer'], scorers: [scorer.ref] })
  runId = ((await post('/api/v1/evaluations/run', { project_id: P, evaluation_ref: ev.ref, model_ref: model.ref })) as { id: string }).id
  for (let i = 0; i < 100; i++) {
    const st = await (await fetch(`${base()}/api/v1/evaluations/${runId}/status?project_id=${encodeURIComponent(P)}`)).json()
    if (st.code !== 'running') break
    await new Promise((r) => setTimeout(r, 200))
  }
})

async function loads(page: Page, path: string, text: string | RegExp) {
  await page.goto(path)
  await expect(page.getByText(text).filter({ visible: true }).first()).toBeVisible()
  await expect(page.locator('.error-box:not(.exception)')).toHaveCount(0)
}

test('the span page, its tabs, comparison and span sessions read the native API', async ({ page }) => {
  const errors = watchErrors(page)
  await loads(page, `/${P}/traces?time=all`, 'meaning of life?')
  await loads(page, `/${P}/spans/${root}`, 'meaning of life?')
  for (const tab of ['feedback', 'summary', 'attributes', 'code', 'use', 'raw'])
    await loads(page, `/${P}/spans/${root}?tab=${tab}`, tab === 'code' ? 'def answer' : tab === 'use' ? 'client.span(' : /answer/)
  await loads(page, `/${P}/spans/${span('3')}`, 'KeyError: x')
  await loads(page, `/${P}/compare-spans?ids=${root},${span('3')}`, 'KeyError: x')
  await loads(page, `/${P}/traces/sessions`, 'conv-1')
  await loads(page, `/${P}/traces/sessions/conv-1`, 'meaning of life?')
  // Earlier page paths still lead to the span pages.
  await page.goto(`/${P}/calls/${root}`)
  await expect(page).toHaveURL(new RegExp(`/${P}/spans/${root}$`))
  await page.goto(`/${P}/threads/conv-1`)
  await expect(page).toHaveURL(new RegExp(`/${P}/traces/sessions/conv-1$`))
  expect(errors).toEqual([])
})

test('asset pages read objects, tables and evaluations from the native API', async ({ page }) => {
  const errors = watchErrors(page)
  await loads(page, `/${P}/datasets`, 'qa')
  await loads(page, `/${P}/objects/qa/versions/latest`, 'two rows')
  await expect(page.locator('table').getByText('expected').first()).toBeVisible()
  await loads(page, `/${P}/functions`, 'answer')
  await loads(page, `/${P}/objects/answer/versions/latest`, 'def answer')
  await loads(page, `/${P}/prompts`, 'greeting')
  await loads(page, `/${P}/objects/greeting/versions/latest`, 'Hello {name}')
  await loads(page, `/${P}/models`, 'echo-model')
  await loads(page, `/${P}/scorers`, 'exact')
  await loads(page, `/${P}/objects`, 'qa-eval')
  await loads(page, `/${P}/objects/qa`, 'qa')
  await loads(page, `/${P}/evaluations`, 'qa-eval')
  await loads(page, `/${P}/evaluations/compare?ids=${runId}`, 'echo-model')
  await loads(page, `/${P}/evaluations/new`, '2 rows')
  await loads(page, `/${P}/leaderboards/lb`, 'echo-model')
  await loads(page, `/${P}/monitors/mon`, 'mon')
  await loads(page, `/${P}/playground?span=${span('2')}`, 'meaning of life?')
  await loads(page, `/${P}/settings?tab=costs`, /gpt-4o/)
  await loads(page, `/${P}/settings?tab=annotations`, 'quality')
  await loads(page, `/${P}/settings?tab=general`, /Version/i)
  expect(errors).toEqual([])
})

test('feedback, scores, renames, aliases, dashboards and the playground write through the native API', async ({ page }) => {
  const errors = watchErrors(page)
  // Reactions and notes (feedback/create, /query, /delete).
  await loads(page, `/${P}/spans/${span('2')}?tab=feedback`, 'Reactions')
  await page.getByPlaceholder('Add a note… (Ctrl+Enter to save)').fill('checked by e2e')
  await page.getByRole('button', { name: 'Add note' }).click()
  await expect(page.getByText('checked by e2e')).toBeVisible()
  await page.getByRole('button', { name: 'Delete note' }).click()
  await expect(page.getByText('checked by e2e')).toHaveCount(0)
  // A scorer run on demand (spans/score) lands as a score.
  await loads(page, `/${P}/spans/${span('2')}?tab=scores`, 'Run scorer')
  await page.getByRole('combobox', { name: 'Scorer' }).selectOption({ label: 'json-ok (JSONValidScorer)' })
  await page.getByRole('button', { name: 'Run scorer' }).click()
  await expect(page.getByText('Score added.')).toBeVisible()
  await expect(page.getByRole('cell', { name: 'json-ok' }).first()).toBeVisible()
  // Display name (spans/update).
  await page.getByTitle('Edit display name').click()
  await page.getByRole('textbox', { name: 'Display name' }).fill('renamed by e2e')
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByText('renamed by e2e').first()).toBeVisible()
  // A dashboard saved with earlier filter and group names still charts.
  await loads(page, `/${P}/dashboards/health`, 'Spans by name')
  await expect(page.locator('.error-box')).toHaveCount(0)
  // The playground streams a completion (completions/stream) and records a span.
  await loads(page, `/${P}/playground?span=${span('2')}`, 'meaning of life?')
  await page.getByRole('button', { name: /^Run/ }).first().click()
  const recorded = async () => {
    const res = await fetch(`${base()}/api/v1/spans/query`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ project_id: P, filter: { names: ['weft.completions.create'] }, limit: 0 }),
    })
    return ((await res.json()) as { total_count: number }).total_count
  }
  await expect.poll(recorded).toBeGreaterThan(0)
  await expect(page.locator('.error-box')).toHaveCount(0)
  expect(errors).toEqual([])
})
