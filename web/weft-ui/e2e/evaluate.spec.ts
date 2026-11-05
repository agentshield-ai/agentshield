// The Evaluate pages with data (DESIGN.md §7, §7.1, §7.2): evaluations and
// their comparison, a leaderboard, review queues and one queue's review,
// the new-evaluation form and the playground. Seeds its own project through
// /api/v1 (two models evaluated on one dataset, a leaderboard, two queues),
// then checks every page in light and dark at 1440 and 390 px (no console
// errors, no horizontal overflow, no serious or critical axe violations,
// text-spacing stress at 390 px), the sentences, keyboard use and two tasks.
import { expect, test, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { Clicks, outDir, recordTask, watchErrors } from './helpers'

const base = () => test.info().project.use.baseURL!
let P = ''
let runs: string[] = []
let queue = ''

async function post(path: string, body: unknown) {
  const r = await fetch(base() + path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  if (!r.ok) throw new Error(`${path} ${r.status} ${await r.text()}`)
  return r.json()
}
const save = (name: string, value: unknown) => post('/api/v1/objects/save', { project_id: P, name, value }) as Promise<{ ref: string }>

test.beforeAll(async () => {
  P = `e2e${Date.now().toString(36)}/evaluate`
  const rows = [
    ['How do I reset my password?', 'How do I reset my password?'],
    ['Where is my invoice?', 'Where is my invoice?'],
    ['Cancel my subscription', 'Your subscription ends at the end of the period.'],
    ['Do you support SSO?', 'Do you support SSO?'],
  ].map(([question, expected]) => ({ question, expected }))
  const table = (await post('/api/v1/tables/create', { project_id: P, rows })) as { ref: string }
  const ds = await save('support-tickets', { _type: 'Dataset', _class_name: 'Dataset', _bases: ['Object', 'BaseModel'], name: 'support-tickets', rows: table.ref })
  const exact = await save('exact', { _type: 'ExactMatchScorer', _class_name: 'ExactMatchScorer', _bases: ['Scorer', 'Object', 'BaseModel'], name: 'exact', target_column: 'expected' })
  const sim = await save('similarity', { _type: 'LevenshteinScorer', _class_name: 'LevenshteinScorer', _bases: ['Scorer', 'Object', 'BaseModel'], name: 'similarity', target_column: 'expected' })
  const model = (name: string, tpl: string) =>
    save(name, {
      _type: 'LLMStructuredCompletionModel',
      _class_name: 'LLMStructuredCompletionModel',
      _bases: ['Model', 'Object', 'BaseModel'],
      name,
      llm_model_id: 'mock/echo',
      default_params: { messages_template: [{ role: 'user', content: tpl }], temperature: 0 },
    })
  const v1 = await model('support-v1', 'Answer: {question}')
  const v2 = await model('support-v2', '{question}')
  const ev = await save('support-eval', { _type: 'Evaluation', _class_name: 'Evaluation', _bases: ['Object', 'BaseModel'], name: 'support-eval', dataset: ds.ref, scorers: [exact.ref, sim.ref], trials: 1 })
  const run = async (m: string) => {
    const { id } = (await post('/api/v1/evaluations/run', { project_id: P, evaluation_ref: ev.ref, model_ref: m })) as { id: string }
    for (let i = 0; i < 100; i++) {
      const st = await (await fetch(`${base()}/api/v1/evaluations/${id}/status?project_id=${encodeURIComponent(P)}`)).json()
      if (st.code !== 'running') break
      await new Promise((r) => setTimeout(r, 150))
    }
    return id
  }
  runs = [await run(v1.ref), await run(v2.ref)]
  await save('support', {
    _type: 'Leaderboard',
    _class_name: 'Leaderboard',
    _bases: ['Object', 'BaseModel'],
    name: 'support',
    description: 'Support answers',
    columns: [
      { evaluation_object_ref: ev.ref, scorer_name: 'exact', summary_metric_path: 'true_fraction', should_minimize: false },
      { evaluation_object_ref: ev.ref, scorer_name: 'similarity', summary_metric_path: 'mean', should_minimize: false },
    ],
  })
  const quality = await save('quality', { _type: 'AnnotationSpec', _class_name: 'AnnotationSpec', _bases: ['Object', 'BaseModel'], name: 'quality', field_schema: { type: 'integer', minimum: 1, maximum: 5 } })
  const correct = await save('correct', { _type: 'AnnotationSpec', _class_name: 'AnnotationSpec', _bases: ['Object', 'BaseModel'], name: 'correct', field_schema: { type: 'boolean' } })
  queue = ((await post('/annotation_queues', { project_id: P, name: 'Weekly quality review', description: 'Check the answers', scorer_refs: [quality.ref, correct.ref] })) as { id: string }).id
  await post('/annotation_queues', { project_id: P, name: 'Nothing yet', description: '', scorer_refs: [] })
  const kids = (await post('/api/v1/spans/query', { project_id: P, filter: { parent_ids: [runs[1]], names: ['Evaluation.predict_and_score'] }, limit: 10 })) as { items: { id: string }[] }
  await post(`/annotation_queues/${queue}/items`, { project_id: P, span_ids: kids.items.map((s) => s.id), display_fields: [] })
})

const PAGES: [string, () => string][] = [
  ['ev-evaluations', () => `/${P}/evaluations`],
  ['ev-compare', () => `/${P}/evaluations/compare?ids=${runs.join(',')}`],
  ['ev-run', () => `/${P}/evaluations/compare?ids=${runs[1]}`],
  ['ev-new', () => `/${P}/evaluations/new`],
  ['ev-leaderboards', () => `/${P}/leaderboards`],
  ['ev-leaderboard', () => `/${P}/leaderboards/support`],
  ['ev-queues', () => `/${P}/review-queues`],
  ['ev-review', () => `/${P}/review-queues/${queue}`],
  ['ev-playground', () => `/${P}/playground`],
]

async function check(page: Page, name: string, tag: string) {
  await page.waitForLoadState('networkidle')
  await expect(page.locator('h1').first()).toBeVisible()
  await expect(page.locator('.spinner')).toHaveCount(0)
  await page.waitForTimeout(300)
  const overflow = await page.evaluate(() => {
    const main = document.querySelector('.main') as HTMLElement | null
    return { doc: document.documentElement.scrollWidth - window.innerWidth, main: main ? main.scrollWidth - main.clientWidth : 0 }
  })
  expect(overflow.doc, 'horizontal page overflow').toBeLessThanOrEqual(1)
  expect(overflow.main, 'horizontal overflow of the main area').toBeLessThanOrEqual(1)
  const axe = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
  const bad = axe.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical')
  expect(bad.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).slice(0, 3).join(', ')}`)).toEqual([])
  await page.addStyleTag({ content: 'html,body,#root,.app{height:auto!important} .main{overflow:visible!important} .topbar{position:static!important}' })
  await page.waitForTimeout(100)
  mkdirSync(join(outDir(), 'screenshots'), { recursive: true })
  await page.screenshot({ path: join(outDir(), 'screenshots', `${name}-${tag}.png`), fullPage: true })
}

for (const cfg of [
  { width: 1440, scheme: 'light' as const },
  { width: 1440, scheme: 'dark' as const },
  { width: 390, scheme: 'light' as const },
  { width: 390, scheme: 'dark' as const },
]) {
  test.describe(`evaluate ${cfg.width}px ${cfg.scheme}`, () => {
    test.use({ viewport: { width: cfg.width, height: 900 }, colorScheme: cfg.scheme })
    for (const [name, path] of PAGES) {
      test(name, async ({ page }) => {
        const errors = watchErrors(page)
        await page.goto(path())
        await check(page, name, `${cfg.width}-${cfg.scheme}`)
        expect(errors).toEqual([])
      })
    }
    test('ev-example-selected', async ({ page }) => {
      const errors = watchErrors(page)
      await page.goto(`/${P}/evaluations/compare?ids=${runs.join(',')}`)
      await page.locator('tr.ev-row').first().click()
      await expect(page.getByRole('complementary', { name: 'Selected example' })).toBeVisible()
      await check(page, 'ev-example-selected', `${cfg.width}-${cfg.scheme}`)
      expect(errors).toEqual([])
    })
  })
}

test.describe('evaluate 390px text-spacing stress', () => {
  test.use({ viewport: { width: 390, height: 900 }, colorScheme: 'light' })
  for (const [name, path] of PAGES) {
    test(name, async ({ page }) => {
      await page.goto(path())
      await page.waitForLoadState('networkidle')
      await expect(page.locator('h1').first()).toBeVisible()
      for (const css of [
        '*{line-height:1.5!important;letter-spacing:0.12em!important;word-spacing:0.16em!important} p{margin-bottom:2em!important}',
        '*{letter-spacing:3px!important}',
        '*{font-family:"FreeMono","DejaVu Sans Mono",monospace!important;letter-spacing:2px!important}',
      ]) {
        const tag = await page.addStyleTag({ content: css })
        await page.waitForTimeout(250)
        const o = await page.evaluate(() => {
          const main = document.querySelector('.main') as HTMLElement | null
          return { doc: document.documentElement.scrollWidth - window.innerWidth, main: main ? main.scrollWidth - main.clientWidth : 0 }
        })
        expect(o.doc, css).toBeLessThanOrEqual(1)
        expect(o.main, css).toBeLessThanOrEqual(1)
        await tag.evaluate((el) => el.remove())
      }
    })
  }
})

test('the Evaluations sentence says which evaluation got better, and its clause opens that comparison', async ({ page }, info) => {
  const errors = watchErrors(page)
  await page.goto(`/${P}/evaluations`)
  const sentence = page.locator('.ph-sentence')
  await expect(sentence).toContainText('2 runs of support-eval. Since its previous run, support-eval got better (exact 0% → 75%).')
  await expect(page.locator('.lrow').first()).toContainText('support-v2 scored exact 75%, up from 0% in the previous run (support-v1); 1 of 4 examples failed.')
  const c = new Clicks(page)
  await c.click(sentence.getByRole('link', { name: /support-eval got better/ }), 'clause “support-eval got better”')
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('support-eval: support-v1 vs support-v2')
  await expect(page.locator('.ph-sentence')).toContainText('support-v2 did better than support-v1 on 2 of 2 metrics. The biggest difference is exact: 75% against 0%.')
  // The example that got worse is one click further, and opens in the inspector.
  await c.click(page.locator('.ph-sentence').getByRole('button', { name: /got worse/ }), 'clause “… got worse”')
  await expect(page.getByRole('radio', { name: /^Worse/ })).toHaveAttribute('aria-checked', 'true')
  await expect(page.locator('tr.ev-row')).toHaveCount(1)
  await expect(page.locator('tr.ev-row')).toContainText('Cancel my subscription')
  recordTask('ev_which_examples_got_worse', c, 2, info)
  expect(errors).toEqual([])
})

test('a run’s examples: failing first, j/k select them, Escape returns to the runs', async ({ page }) => {
  await page.goto(`/${P}/evaluations/compare?ids=${runs[1]}`)
  await expect(page.locator('.ph-sentence')).toContainText('support-v2 scored exact 75% and similarity')
  await expect(page.locator('.ph-sentence')).toContainText('1 of 4 examples failed.')
  await expect(page.locator('.ph-sentence').getByRole('link', { name: 'Compare with the previous run' })).toBeVisible()
  await expect(page.locator('tr.ev-row').first()).toContainText('Cancel my subscription')
  await page.keyboard.press('j')
  await expect(page.getByRole('complementary', { name: 'Selected example' })).toContainText('Cancel my subscription')
  await expect(page).toHaveURL(/ex=/)
  await page.keyboard.press('j')
  await expect(page.locator('tr.ev-row[aria-selected="true"]')).toContainText('How do I reset my password?')
  await page.keyboard.press('Escape')
  await expect(page.getByRole('complementary', { name: 'Run details' })).toBeVisible()
})

test('the leaderboard says who leads, and deleting a leaderboard can be undone', async ({ page }) => {
  await page.goto(`/${P}/leaderboards/support`)
  await expect(page.locator('.ph-sentence')).toHaveText('support-v2 leads on all 2 metrics. 2 models ranked.')
  await expect(page.locator('table tbody tr').first()).toContainText('support-v2')
  await page.goto(`/${P}/leaderboards`)
  await expect(page.locator('.ph-sentence')).toHaveText('1 leaderboard. support-v2 leads it.')
  const del = page.getByRole('button', { name: 'Delete leaderboard support' })
  await del.click()
  const toast = page.locator('.toast').filter({ hasText: 'Deleted leaderboard “support”' })
  await expect(toast).toBeVisible()
  await toast.getByRole('button', { name: 'Undo' }).click()
  await page.reload()
  await expect(page.getByRole('button', { name: 'Delete leaderboard support' })).toHaveCount(1)
})

test('review queues say what waits; the review is keyboard-first', async ({ page }) => {
  await page.goto(`/${P}/review-queues`)
  await expect(page.locator('.ph-sentence')).toHaveText('4 items wait for review in Weekly quality review.')
  await page.locator('.ph-sentence').getByRole('link', { name: 'Weekly quality review' }).click()
  await expect(page.locator('.ph-sentence')).toHaveText('Item 1 of 4. 4 are left to review.')
  // 3 on the scale, y for "correct", then submit: the next item opens.
  await page.keyboard.press('3')
  await expect(page.getByRole('radiogroup', { name: 'quality' }).getByRole('radio', { name: '3' })).toHaveAttribute('aria-checked', 'true')
  await page.keyboard.press('y')
  await expect(page.getByRole('radiogroup', { name: 'correct' }).getByRole('radio', { name: 'Yes' })).toHaveAttribute('aria-checked', 'true')
  await page.keyboard.press('Control+Enter')
  await expect(page.locator('.ph-sentence')).toHaveText('Item 2 of 4. 3 are left to review; 1 done.')
  await page.keyboard.press('s')
  await expect(page.locator('.ph-sentence')).toHaveText('Item 3 of 4. 2 are left to review; 1 done, 1 skipped.')
  await page.keyboard.press('k')
  await expect(page.locator('.ph-sentence')).toContainText('Item 2 of 4.')
})
