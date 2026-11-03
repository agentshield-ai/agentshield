// Every main page in light and dark, at 1440 and 390 px: a full-page
// screenshot, no console errors, no horizontal page overflow, and no serious
// or critical axe-core violations (WCAG 2.1 A/AA).
import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { mkdirSync, writeFileSync, existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { outDir, project, watchErrors } from './helpers'

const blank = () => `${process.env.WEFT_E2E_EMPTY}-blank`

const PAGES: [string, () => string][] = [
  ['projects', () => '/'],
  ['sessions', () => `/${project()}/sessions`],
  ['session-timeline', () => `/${project()}/sessions/code-lint-1?step=problem`],
  ['session-graph', () => `/${project()}/sessions/trip-oslo?tab=graph`],
  ['session-reasoning', () => `/${project()}/sessions/trip-oslo?tab=reasoning`],
  ['session-security', () => `/${project()}/sessions/code-lint-1?tab=security`],
  ['session-scores', () => `/${project()}/sessions/trip-oslo?tab=scores`],
  ['session-raw', () => `/${project()}/sessions/trip-oslo?tab=raw`],
  ['agents', () => `/${project()}/agents`],
  ['agent', () => `/${project()}/agents/coder`],
  ['teams', () => `/${project()}/teams`],
  ['team', () => `/${project()}/teams/trip-planners`],
  ['security', () => `/${project()}/security`],
  ['traces', () => `/${project()}/traces`],
  ['dashboards', () => `/${project()}/dashboards`],
  ['monitors', () => `/${project()}/monitors`],
  ['evaluations', () => `/${project()}/evaluations`],
  ['scores', () => `/${project()}/scores`],
  ['leaderboards', () => `/${project()}/leaderboards`],
  ['review-queues', () => `/${project()}/review-queues`],
  ['datasets', () => `/${project()}/datasets`],
  ['objects', () => `/${project()}/objects`],
  ['automations', () => `/${project()}/automations`],
  ['settings', () => `/${project()}/settings`],
  ['help', () => '/help'],
  ['help-reference', () => '/help/API.md'],
  ['empty-project', () => `/${blank()}/sessions`],
  ['empty-agents', () => `/${blank()}/agents`],
  ['empty-teams', () => `/${blank()}/teams`],
  ['empty-security', () => `/${blank()}/security`],
  ['empty-traces', () => `/${blank()}/traces`],
  ['empty-review-queues', () => `/${blank()}/review-queues`],
]

const CONFIGS = [
  { width: 1440, scheme: 'light' as const },
  { width: 1440, scheme: 'dark' as const },
  { width: 390, scheme: 'light' as const },
  { width: 390, scheme: 'dark' as const },
]

function recordA11y(name: string, result: unknown) {
  const file = join(outDir(), 'a11y.json')
  mkdirSync(outDir(), { recursive: true })
  const all = existsSync(file) ? JSON.parse(readFileSync(file, 'utf8')) : {}
  all[name] = result
  writeFileSync(file, JSON.stringify(all, null, 2))
}

for (const cfg of CONFIGS) {
  test.describe(`${cfg.width}px ${cfg.scheme}`, () => {
    test.use({ viewport: { width: cfg.width, height: 900 }, colorScheme: cfg.scheme })
    for (const [name, path] of PAGES) {
      test(name, async ({ page }) => {
        const errors = watchErrors(page)
        await page.goto(path())
        await page.waitForLoadState('networkidle')
        await expect(page.locator('h1').first()).toBeVisible()
        await expect(page.locator('.spinner')).toHaveCount(0)
        await page.waitForTimeout(300)

        const overflow = await page.evaluate(() => {
          const main = document.querySelector('.main') as HTMLElement | null
          return {
            doc: document.documentElement.scrollWidth - window.innerWidth,
            main: main ? main.scrollWidth - main.clientWidth : 0,
          }
        })
        expect(overflow.doc, 'horizontal page overflow').toBeLessThanOrEqual(1)
        expect(overflow.main, 'horizontal overflow of the main area').toBeLessThanOrEqual(1)

        const axe = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
        const bad = axe.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical')
        recordA11y(`${name} ${cfg.width} ${cfg.scheme}`, {
          serious_or_critical: bad.map((v) => ({ id: v.id, impact: v.impact, nodes: v.nodes.slice(0, 3).map((n) => n.target.join(' ')) })),
          other: axe.violations.filter((v) => !bad.includes(v)).map((v) => `${v.id} (${v.impact})`),
          passes: axe.passes.length,
        })
        expect(
          bad.map(
            (v) =>
              `${v.id}: ${v.nodes
                .map((n) => n.target.join(' '))
                .slice(0, 3)
                .join(', ')}`,
          ),
        ).toEqual([])

        // Let the page grow to its full height for the screenshot.
        await page.addStyleTag({ content: 'html,body,#root,.app{height:auto!important} .main{overflow:visible!important} .topbar{position:static!important}' })
        await page.waitForTimeout(100)
        await page.screenshot({ path: join(outDir(), 'screenshots', `${name}-${cfg.width}-${cfg.scheme}.png`), fullPage: true })
        expect(errors).toEqual([])
      })
    }
  })
}
