// Group D's own check over a populated assets project (demo/assets, seeded by seed.mjs):
// axe AA (serious/critical), no console errors, no horizontal overflow, light/dark, 1440/390,
// and the 390 px text-spacing stress.
import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

const P = 'demo/assets'
const PAGES: [string, string][] = [
  ['datasets', `/${P}/datasets`],
  ['models', `/${P}/models`],
  ['prompts', `/${P}/prompts`],
  ['scorers', `/${P}/scorers`],
  ['functions', `/${P}/functions`],
  ['all', `/${P}/objects`],
  ['dataset', `/${P}/objects/support-tickets/versions/latest`],
  ['dataset-use', `/${P}/objects/support-tickets/versions/latest?tab=use`],
  ['dataset-raw', `/${P}/objects/support-tickets/versions/latest?tab=raw`],
  ['prompt', `/${P}/objects/triage/versions/latest`],
  ['function', `/${P}/objects/route_ticket/versions/latest`],
  ['model', `/${P}/objects/triage-model/versions/latest`],
  ['judge', `/${P}/objects/polite/versions/latest`],
  ['evaluation', `/${P}/objects/triage-eval/versions/latest`],
  ['versions', `/${P}/objects/support-tickets`],
  ['diff', `/${P}/objects/support-tickets/compare`],
  ['diff-split', `/${P}/objects/triage/compare?view=split`],
  ['automations', `/${P}/automations`],
  ...['general', 'integrations', 'costs', 'annotations', 'secrets', 'buckets', 'apikeys', 'members', 'audit', 'retention', 'sampling', 'sensitive', 'delete'].map(
    (t): [string, string] => [`settings-${t}`, `/${P}/settings?tab=${t}`],
  ),
  ['admin', '/admin'],
  ['admin-teams', '/admin?tab=teams'],
  ['projects', '/'],
  ['help', '/help/sessions.md'],
]
const CONFIGS = [
  { width: 1440, scheme: 'light' as const },
  { width: 1440, scheme: 'dark' as const },
  { width: 390, scheme: 'light' as const },
  { width: 390, scheme: 'dark' as const },
]
for (const cfg of CONFIGS)
  test.describe(`${cfg.width} ${cfg.scheme}`, () => {
    test.use({ viewport: { width: cfg.width, height: 900 }, colorScheme: cfg.scheme })
    for (const [name, path] of PAGES)
      test(name, async ({ page }) => {
        const errors: string[] = []
        page.on('pageerror', (e) => errors.push(e.message))
        page.on('console', (m) => m.type() === 'error' && errors.push(m.text()))
        await page.goto(path)
        await page.waitForLoadState('networkidle')
        await expect(page.locator('h1').first()).toBeVisible()
        await expect(page.locator('.spinner')).toHaveCount(0)
        await page.waitForTimeout(300)
        const ov = await page.evaluate(() => {
          const main = document.querySelector('.main') as HTMLElement | null
          return Math.max(document.documentElement.scrollWidth - window.innerWidth, main ? main.scrollWidth - main.clientWidth : 0)
        })
        expect(ov, 'overflow').toBeLessThanOrEqual(1)
        const axe = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
        const bad = axe.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical')
        expect(bad.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).slice(0, 3).join(', ')}`)).toEqual([])
        if (cfg.width === 390 && cfg.scheme === 'light') {
          for (const css of [
            '*{line-height:1.5!important;letter-spacing:0.12em!important;word-spacing:0.16em!important} p{margin-bottom:2em!important}',
            '*{letter-spacing:3px!important}',
            '*{font-family:"FreeMono","DejaVu Sans Mono",monospace!important;letter-spacing:2px!important}',
          ]) {
            const tag = await page.addStyleTag({ content: css })
            await page.waitForTimeout(200)
            const o = await page.evaluate(() => {
              const main = document.querySelector('.main') as HTMLElement | null
              return Math.max(document.documentElement.scrollWidth - window.innerWidth, main ? main.scrollWidth - main.clientWidth : 0)
            })
            expect(o, `stress overflow ${css}`).toBeLessThanOrEqual(1)
            await tag.evaluate((el) => el.remove())
          }
        }
        expect(errors).toEqual([])
      })
  })
