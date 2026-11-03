import { expect, type Locator, type Page, type TestInfo } from '@playwright/test'
import { mkdirSync, readFileSync, writeFileSync, existsSync } from 'node:fs'
import { dirname, join } from 'node:path'

export const project = () => process.env.WEFT_E2E_PROJECT!
export const outDir = () => process.env.WEFT_E2E_OUT ?? 'e2e-results'

/** Counts the clicks a task takes; every navigation step goes through it. */
export class Clicks {
  n = 0
  path: string[] = []
  constructor(private page: Page) {}
  async click(target: Locator, label: string) {
    this.n++
    this.path.push(label)
    await target.click()
  }
}

/** Fails the test on console errors or uncaught exceptions. */
export function watchErrors(page: Page): string[] {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`))
  page.on('console', (m) => {
    if (m.type() === 'error') errors.push(`console: ${m.text()}`)
  })
  return errors
}

export function recordTask(name: string, clicks: Clicks, limit: number, info: TestInfo) {
  const file = join(outDir(), 'task-clicks.json')
  mkdirSync(dirname(file), { recursive: true })
  const all = existsSync(file) ? JSON.parse(readFileSync(file, 'utf8')) : {}
  all[name] = { clicks: clicks.n, limit, path: clicks.path }
  writeFileSync(file, JSON.stringify(all, null, 2))
  info.annotations.push({ type: 'clicks', description: `${clicks.n} (limit ${limit}): ${clicks.path.join(' → ')}` })
  expect(clicks.n, `${name}: ${clicks.path.join(' → ')}`).toBeLessThanOrEqual(limit)
}
