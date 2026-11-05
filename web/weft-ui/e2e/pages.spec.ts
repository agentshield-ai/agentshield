// Every main page in light and dark, at 1440 and 390 px: a full-page
// screenshot, no console errors, no horizontal page overflow, and no serious
// or critical axe-core violations (WCAG 2.1 A/AA). The long-* pages show a
// project whose names are long and unbroken (e2e/seed.ts longNames). Then
// every page at 390 px under text-spacing stress (WCAG 1.4.12's minimum,
// wider letter and word spacing, a wide monospace fallback font): still no
// horizontal overflow.
import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { mkdirSync, writeFileSync, existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { outDir, project, watchErrors } from './helpers'
import { LONG_AGENT, LONG_SESSION } from './seed'

const blank = () => `${process.env.WEFT_E2E_EMPTY}-blank`
const long = () => process.env.WEFT_E2E_LONG!

const PAGES: [string, () => string][] = [
  ['projects', () => '/'],
  ['sessions', () => `/${project()}/sessions`],
  ['session-story', () => `/${project()}/sessions/code-lint-1`],
  ['session-failed', () => `/${project()}/sessions/trip-lisbon`],
  ['session-approvals', () => `/${project()}/sessions/code-deploy-1`],
  ['session-team', () => `/${project()}/sessions/trip-oslo?tab=team`],
  ['session-scores', () => `/${project()}/sessions/trip-oslo?tab=scores`],
  ['session-raw', () => `/${project()}/sessions/trip-oslo?tab=raw`],
  ['agents', () => `/${project()}/agents`],
  ['agent', () => `/${project()}/agents/coder`],
  ['agent-analytics', () => `/${project()}/agents/coder/analytics`],
  ['agents-analytics', () => `/${project()}/agent-analytics`],
  ['agents-analytics-steps', () => `/${project()}/agent-analytics?tab=spans`],
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
  ['long-sessions', () => `/${long()}/sessions`],
  ['long-session', () => `/${long()}/sessions/${LONG_SESSION}`],
  ['long-session-team', () => `/${long()}/sessions/${LONG_SESSION}?tab=team`],
  ['long-session-raw', () => `/${long()}/sessions/${LONG_SESSION}?tab=raw`],
  ['long-agents', () => `/${long()}/agents`],
  ['long-agent', () => `/${long()}/agents/${encodeURIComponent(LONG_AGENT)}`],
  ['long-teams', () => `/${long()}/teams`],
  ['long-security', () => `/${long()}/security?time=30d`],
  ['long-traces', () => `/${long()}/traces`],
]

// The text-spacing stress check covers every page above. The first style is
// WCAG 1.4.12's minimum (line height 1.5, paragraph spacing 2em, letter
// spacing 0.12em, word spacing 0.16em); the others go further.
const STRESS_PAGES = PAGES.map(([name]) => name)
const SPACING_STRESS: [string, string][] = [
  ['wcag-1.4.12', '*{line-height:1.5!important;letter-spacing:0.12em!important;word-spacing:0.16em!important} p{margin-bottom:2em!important}'],
  ['letter-3px', '*{letter-spacing:3px!important}'],
  ['letter-0.2em-word-0.3em', '*{letter-spacing:0.2em!important;word-spacing:0.3em!important}'],
  ['wide-mono', '*{font-family:"FreeMono","DejaVu Sans Mono",monospace!important;letter-spacing:2px!important}'],
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

test.describe('390px text-spacing stress', () => {
  test.use({ viewport: { width: 390, height: 900 }, colorScheme: 'light' })
  for (const name of STRESS_PAGES) {
    const path = PAGES.find(([n]) => n === name)![1]
    test(name, async ({ page }) => {
      await page.goto(path())
      await page.waitForLoadState('networkidle')
      await expect(page.locator('h1').first()).toBeVisible()
      await expect(page.locator('.spinner')).toHaveCount(0)
      for (const [label, css] of SPACING_STRESS) {
        const tag = await page.addStyleTag({ content: css })
        await page.waitForTimeout(300)
        const overflow = await page.evaluate(() => {
          const main = document.querySelector('.main') as HTMLElement | null
          // The elements that stick out, to say what overflows.
          const right = main ? main.getBoundingClientRect().right : window.innerWidth
          const offenders: string[] = []
          const clipped = (el: HTMLElement) => {
            for (let p = el.parentElement; p && p !== main; p = p.parentElement) {
              if (getComputedStyle(p).overflowX !== 'visible') return true
            }
            return false
          }
          for (const el of Array.from(document.querySelectorAll<HTMLElement>('.main *'))) {
            const r = el.getBoundingClientRect()
            if (r.width > 0 && r.right > right + 1 && offenders.length < 5 && !clipped(el)) {
              offenders.push(`${el.tagName.toLowerCase()}.${String(el.className).split(' ').join('.')} +${Math.round(r.right - right)}px "${(el.textContent ?? '').slice(0, 40)}"`)
            }
          }
          return {
            doc: document.documentElement.scrollWidth - window.innerWidth,
            main: main ? main.scrollWidth - main.clientWidth : 0,
            offenders: offenders.join('; '),
          }
        })
        expect(overflow.doc, `${label}: horizontal page overflow (${overflow.offenders})`).toBeLessThanOrEqual(1)
        expect(overflow.main, `${label}: horizontal overflow of the main area (${overflow.offenders})`).toBeLessThanOrEqual(1)
        await tag.evaluate((el) => el.remove())
      }
    })
  }
})
