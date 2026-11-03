// Keyboard, URL-synced filters, saved views and undo (DESIGN.md §7.1 rules 5, 6, 10, 11).
import { expect, test } from '@playwright/test'
import { project, watchErrors } from './helpers'

test('`/` focuses the filter, `?` lists shortcuts, filter syntax becomes chips in the URL', async ({ page }) => {
  const errors = watchErrors(page)
  await page.goto(`/${project()}/sessions`)
  await expect(page.locator('.session-row').first()).toBeVisible()
  await page.keyboard.press('?')
  await expect(page.getByRole('dialog')).toContainText('Keyboard shortcuts')
  await page.keyboard.press('Escape')
  await page.keyboard.press('/')
  await expect(page.locator('[data-filter-input]')).toBeFocused()
  await page.keyboard.type('agent:coder has:approval')
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/agent=coder/)
  await expect(page).toHaveURL(/has=approval/)
  await expect(page.getByRole('button', { name: 'Remove filter Agent' })).toBeVisible()
  await expect(page.locator('.session-row')).toHaveCount(1)
  await expect(page.locator('.session-row')).toContainText('Find out why deploys fail on staging')
  // Reload keeps the filters: they live in the URL.
  await page.reload()
  await expect(page.getByRole('button', { name: 'Edit filter Security: Needs approval' })).toBeVisible()
  await page.getByRole('button', { name: 'Remove filter Agent' }).click()
  await expect(page).not.toHaveURL(/agent=coder/)
  expect(errors).toEqual([])
})

test('saved views can be saved, applied and deleted with undo', async ({ page }) => {
  await page.goto(`/${project()}/sessions?status=error`)
  await page.getByRole('button', { name: /Views/ }).click()
  await page.getByRole('button', { name: 'Save current filters as a view…' }).click()
  await page.getByRole('textbox', { name: 'View name' }).fill('Failures')
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(page).toHaveURL(/view=sessions-failures/)
  await page.goto(`/${project()}/sessions`)
  await page.getByRole('button', { name: /Views/ }).click()
  await page.getByRole('button', { name: 'Failures', exact: true }).click()
  await expect(page).toHaveURL(/status=error/)
  await page.getByRole('button', { name: /Failures/ }).click()
  await page.getByRole('button', { name: 'Delete view Failures' }).click()
  const toast = page.locator('.toast').filter({ hasText: 'Deleted view “Failures”' })
  await expect(toast).toBeVisible()
  await expect(page.getByRole('button', { name: 'Delete view Failures' })).toHaveCount(0)
  await toast.getByRole('button', { name: 'Undo' }).click()
  await expect(page.getByRole('button', { name: 'Delete view Failures' })).toBeVisible()
})

test('j/k move through the timeline and deep links survive reload', async ({ page }) => {
  await page.goto(`/${project()}/sessions/code-lint-1`)
  const steps = page.locator('.tl-list > li.tl-step')
  await expect(steps.first()).toBeVisible()
  // Three quick presses, without waiting for the URL in between, move three steps.
  await page.keyboard.press('j')
  await page.keyboard.press('j')
  await page.keyboard.press('j')
  const focused = page.locator('.tl-step[aria-current="step"]')
  await expect(focused).toHaveCount(1)
  await expect(steps.nth(2)).toHaveAttribute('aria-current', 'step')
  const key = new URL(page.url()).searchParams.get('step')
  expect(key).toBeTruthy()
  // Four quick presses back stop at the first step (a turn).
  for (let i = 0; i < 4; i++) await page.keyboard.press('k')
  await expect(steps.first()).toHaveAttribute('aria-current', 'step')
  await expect(steps.first()).toHaveClass(/tl-turn/)
  const turnKey = new URL(page.url()).searchParams.get('step')
  expect(turnKey).toMatch(/^turn:/)
  expect(turnKey).not.toBe(key)
  // A deep link to a turn is restored as the current step after a reload.
  await page.reload()
  await expect(page.locator('.tl-step[aria-current="step"]')).toHaveCount(1)
  await expect(steps.first()).toHaveAttribute('aria-current', 'step')
  // So is a deep link to a later step.
  await page.goto(`/${project()}/sessions/code-lint-1?step=${encodeURIComponent(key!)}`)
  await expect(steps.nth(2)).toHaveAttribute('aria-current', 'step')
  await expect(page.locator('.tl-step[aria-current="step"]')).toBeInViewport()
  await page.reload()
  await expect(steps.nth(2)).toHaveAttribute('aria-current', 'step')
  await expect(page.locator('.tl-step[aria-current="step"]')).toBeInViewport()
})

test('deleting a score can be undone', async ({ page }) => {
  await page.goto(`/${project()}/sessions/trip-oslo?tab=scores`)
  const del = page.getByRole('button', { name: 'Delete Goal Completion score' })
  await expect(del).toHaveCount(1)
  await del.click()
  const toast = page.locator('.toast').filter({ hasText: 'Deleted Goal Completion score' })
  await expect(toast).toBeVisible()
  await expect(del).toHaveCount(0)
  await toast.getByRole('button', { name: 'Undo' }).click()
  await expect(del).toHaveCount(1)
  // Nothing was deleted on the server.
  await page.reload()
  await expect(page.getByRole('button', { name: 'Delete Goal Completion score' })).toHaveCount(1)
})

test('the default range widens when the last 24 hours are empty, and explains why', async ({ page }) => {
  await page.goto(`/${process.env.WEFT_E2E_OLDER}/sessions`)
  await expect(page.getByRole('status').filter({ hasText: 'because nothing happened in the last 24 hours' })).toBeVisible()
  await expect(page.locator('.session-row')).toHaveCount(1)
})

test('graph tab draws agents and offers the same data as a table', async ({ page }) => {
  await page.goto(`/${project()}/sessions/code-lint-1?tab=graph`)
  await expect(page.getByRole('group', { name: /Delegation graph: 2 agents/ })).toBeVisible()
  await page.getByRole('button', { name: 'Table' }).click()
  await expect(page.locator('table').first()).toContainText('coder')
  await expect(page.locator('table.handoff-table')).toContainText('reviewer')
})

test('replay with current rules shows a diff', async ({ page }) => {
  const status = await (await page.request.get(`/api/v1/shield/status?project_id=${encodeURIComponent(project())}`)).json()
  test.skip(!status.replay_enabled, 'server started without --shield-rules-dir')
  await page.goto(`/${project()}/sessions/code-lint-1?tab=security`)
  await page.getByRole('button', { name: /Replay with current rules/ }).click()
  await expect(page.getByText(/Checked \d+ of \d+ tool calls/)).toBeVisible({ timeout: 20_000 })
})

test('every attention card opens exactly the sessions it counts, with money shown plainly', async ({ page }) => {
  const errors = watchErrors(page)
  await page.goto(`/${project()}/sessions`)
  await expect(page.locator('.session-row').first()).toBeVisible()
  // No floating-point noise in any amount.
  await expect(page.locator('main, .main').first()).not.toContainText(/\$\d*\.\d{5,}/)
  const cards = page.locator('a.attn-link')
  const n = await cards.count()
  expect(n).toBeGreaterThanOrEqual(3)
  for (let i = 0; i < n; i++) {
    await page.goto(`/${project()}/sessions`)
    const card = page.locator('a.attn-link').nth(i)
    const title = await card.locator('xpath=..').locator('.attn-title').innerText()
    const count = Number(await card.locator('.attn-count').innerText())
    await card.click()
    await expect(page.locator('.session-row'), title).toHaveCount(count)
  }
  expect(errors).toEqual([])
})

test('every page applies every filter, and says when it goes through sessions', async ({ page }) => {
  const errors = watchErrors(page)
  // Traces by team: only the code crew's traces (3 sessions, one trace each).
  await page.goto(`/${project()}/traces?team=code-crew`)
  await expect(page.getByRole('button', { name: 'Remove filter Team' })).toBeVisible()
  await expect(page.getByRole('note')).toContainText('showing traces that belong to a session that matches')
  await expect(page.locator('.traces-table tbody tr')).toHaveCount(3)
  // Traces by agent: traces where any span is the researcher's, though no root span is.
  await page.goto(`/${project()}/traces?agent=researcher`)
  await expect(page.locator('.traces-table tbody tr')).toHaveCount(5)
  // Teams by tool: the trip planners never ran Bash.
  await page.goto(`/${project()}/teams?tool=Bash`)
  await expect(page.getByRole('list', { name: 'Teams' }).getByRole('link', { name: /Code crew/ })).toBeVisible()
  await expect(page.getByRole('list', { name: 'Teams' }).getByRole('link', { name: /Trip planners/ })).toHaveCount(0)
  await expect(page.getByRole('note')).toContainText('showing teams with a session that matches')
  // Agents by a session status: the agents of sessions that went on after a failed step.
  await page.goto(`/${project()}/agents?status=failed_step`)
  await expect(page.locator('table.agents-table tbody tr')).toHaveCount(2)
  // Security by team: the trip planners have no detections.
  await page.goto(`/${project()}/security?team=trip-planners`)
  await expect(page.getByRole('button', { name: 'Remove filter Team' })).toBeVisible()
  await expect(page.getByText('Nothing matches these filters.')).toBeVisible()
  await page.goto(`/${project()}/security?team=code-crew&has=block`)
  await expect(page.locator('.det-row')).toHaveCount(1)
  expect(errors).toEqual([])
})

test('Traces defaults to the last 24 hours, widens when empty, and saves views', async ({ page }) => {
  await page.goto(`/${process.env.WEFT_E2E_OLDER}/traces`)
  await expect(page.getByRole('status').filter({ hasText: 'because nothing happened in the last 24 hours' })).toBeVisible()
  await expect(page.locator('.traces-table tbody tr')).toHaveCount(1)
  await page.goto(`/${project()}/traces?status=error&cols=output`)
  await page.getByRole('button', { name: /Views/ }).click()
  await page.getByRole('button', { name: 'Save current filters as a view…' }).click()
  await page.getByRole('textbox', { name: 'View name' }).fill('Failed traces')
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(page).toHaveURL(/view=traces-failed-traces/)
  await page.goto(`/${project()}/traces`)
  await page.getByRole('button', { name: /Views/ }).click()
  await page.getByRole('button', { name: 'Failed traces', exact: true }).click()
  await expect(page).toHaveURL(/status=error/)
  await expect(page).toHaveURL(/cols=output/)
})

test('Traces: Failed means an error anywhere, text finds any span, field conditions live in Add filter', async ({ page }) => {
  const errors = watchErrors(page)
  // Every session with a failed step or a failure has a trace with an error somewhere in it.
  const api = async (q: string) =>
    (await (await page.request.get(`/api/v1/sessions?project_id=${encodeURIComponent(project())}&${q}`)).json()).total_count as number
  const withErrors = (await api('status=error')) + (await api('status=failed_step'))
  expect(withErrors).toBeGreaterThan(0)
  await page.goto(`/${project()}/traces?status=error&time=all`)
  await expect(page.locator('.traces-table tbody tr').first()).toBeVisible()
  expect(await page.locator('.traces-table tbody tr').count()).toBeGreaterThanOrEqual(withErrors)
  // Free text matches a child span's error message, not only the root span.
  await page.goto(`/${project()}/traces?q=${encodeURIComponent('3 tests failed')}&time=all`)
  await expect(page.locator('.traces-table tbody tr')).toHaveCount(1)
  // One filter button: field conditions are an "Add filter" option.
  await page.goto(`/${project()}/traces?time=all`)
  await expect(page.getByRole('button', { name: '+ Filter' })).toHaveCount(0)
  await page.getByRole('button', { name: 'Add filter' }).click()
  await page.getByRole('tab', { name: 'Advanced: field condition' }).click()
  await page.getByRole('combobox', { name: 'Field', exact: true }).fill('op_name')
  await page.getByRole('combobox', { name: 'Operator' }).selectOption('contains')
  await page.getByRole('textbox', { name: 'Value' }).fill('orchestrator')
  await page.getByRole('button', { name: 'Apply' }).click()
  await expect(page.getByRole('button', { name: 'Remove filter Field: op_name contains orchestrator' })).toBeVisible()
  await expect(page).toHaveURL(/f=/)
  await expect(page.locator('.traces-table tbody tr').first()).toBeVisible()
  expect(errors).toEqual([])
})

test('Sessions text search finds tool errors; the projects page has help', async ({ page }) => {
  const errors = watchErrors(page)
  await page.goto(`/${project()}/sessions?q=${encodeURIComponent('3 tests failed')}&time=all`)
  await expect(page.locator('.session-row')).toHaveCount(1)
  await page.goto('/')
  await expect(page.getByRole('complementary', { name: 'About Projects' })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Read the guide' })).toHaveAttribute('href', '/help/setup.md')
  expect(errors).toEqual([])
})
