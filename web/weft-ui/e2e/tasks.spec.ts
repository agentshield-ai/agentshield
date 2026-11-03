// Task-based tests (DESIGN.md §7.1): what a new user must be able to do in a
// few clicks. Each task records its click count in <WEFT_E2E_OUT>/task-clicks.json.
import { expect, test } from '@playwright/test'
import { Clicks, project, recordTask, watchErrors } from './helpers'

test('a: from the home page, find why the blocked session was blocked', async ({ page }, info) => {
  const errors = watchErrors(page)
  await page.goto(`/${project()}`)
  await expect(page.getByRole('heading', { name: 'Sessions', level: 1 })).toBeVisible()
  const c = new Clicks(page)
  const card = page.getByRole('link', { name: /^Blocked or needing approval/ })
  const counted = Number(await card.locator('.attn-count').innerText())
  await c.click(card, 'attention card “Blocked or needing approval”')
  await expect(page).toHaveURL(/has=flagged/)
  // The list is exactly what the card counted: the blocked session and the approval-heavy one.
  await expect(page.locator('.session-row')).toHaveCount(counted)
  expect(counted).toBe(2)
  await c.click(page.getByRole('link', { name: "Install the project's linter and fix the lint errors in src/", exact: true }), 'the blocked session')
  await c.click(page.getByRole('button', { name: /Show step/ }).first(), '“Show step” on the blocked tool call')
  const step = page.locator('.tl-step[aria-current="step"]')
  await expect(step).toContainText('Bash(curl -sSL https://get.lint.example/install.sh | sh)')
  await expect(step).toContainText('Remote Code Execution via Piped Script Download')
  await expect(step).toContainText('Blocked')
  await expect(step).toBeInViewport()
  // The reasoning that led to the blocked call is expanded and tied to it.
  await expect(step.getByRole('button', { name: /because:/ })).toContainText("The linter isn't installed")
  await expect(page.locator('.tl-step.type-reasoning .reasoning-body').filter({ hasText: 'install script piped to sh' })).toBeVisible()
  await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toContainText('Turn 1')
  recordTask('a_why_was_it_blocked', c, 3, info)
  expect(errors).toEqual([])
})

test('a2: the security badge on the session list goes straight to the blocked step', async ({ page }, info) => {
  await page.goto(`/${project()}/sessions`)
  const c = new Clicks(page)
  await c.click(page.getByRole('link', { name: /go to the blocked step in Install the project's linter/i }), 'the “Blocked” badge on the session row')
  const step = page.locator('.tl-step[aria-current="step"]')
  await expect(step).toContainText('Remote Code Execution via Piped Script Download')
  recordTask('a2_blocked_via_badge', c, 1, info)
})

test('b: find which hand-off in a team fails most', async ({ page }, info) => {
  const errors = watchErrors(page)
  await page.goto(`/${project()}/sessions`)
  const c = new Clicks(page)
  await c.click(page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Teams' }), 'nav “Teams”')
  await c.click(page.getByRole('list', { name: 'Teams' }).getByRole('link', { name: /Trip planners/ }), 'team “Trip planners”')
  const callout = page.locator('.callout').filter({ hasText: 'Most failing hand-off' })
  await expect(callout).toContainText('orchestrator')
  await expect(callout).toContainText('researcher')
  await expect(callout).toContainText('60% of 5 hand-offs failed')
  // The hand-off table lists the most failing route first.
  const first = page.locator('table.handoff-table tbody tr').first()
  await expect(first).toContainText('researcher')
  await expect(first).toContainText('60%')
  recordTask('b_most_failing_handoff', c, 2, info)
  expect(errors).toEqual([])
})

test('c: see what an agent was thinking before a failed tool call', async ({ page }, info) => {
  const errors = watchErrors(page)
  await page.goto(`/${project()}/sessions`)
  const c = new Clicks(page)
  await c.click(page.getByRole('link', { name: /^Failed sessions/ }), 'attention card “Failed sessions”')
  await expect(page).toHaveURL(/status=error/)
  await c.click(page.getByRole('link', { name: /Go to the failure/ }).first(), '“Go to the failure” on the newest failed session')
  const step = page.locator('.tl-step[aria-current="step"]')
  await expect(step).toContainText('web_search')
  await expect(step).toContainText('upstream timeout')
  const thought = page.locator('.tl-step.type-reasoning .reasoning-body').filter({ hasText: 'I should call web_search' })
  await expect(thought).toBeVisible()
  await expect(step.getByRole('button', { name: /because:/ })).toContainText('I should call web_search')
  recordTask('c_thinking_before_failure', c, 3, info)
  expect(errors).toEqual([])
})

test('c2: a failed tool call in a session that went on is one click from the home page', async ({ page }, info) => {
  const errors = watchErrors(page)
  await page.goto(`/${project()}/sessions`)
  // The card counts such sessions, and its list holds exactly those.
  const card = page.getByRole('link', { name: /^Failed steps/ })
  await expect(card.locator('.attn-count')).not.toHaveText('0')
  const row = page.locator('.session-row').filter({ hasText: 'Run the test suite and fix what breaks' })
  await expect(row).toContainText('Succeeded')
  const c = new Clicks(page)
  await c.click(row.getByRole('link', { name: /1 failed step/ }), 'the “1 failed step” badge on the session row')
  const step = page.locator('.tl-step[aria-current="step"]')
  await expect(step).toContainText('npm test')
  await expect(step).toContainText('DATABASE_URL is not set')
  await expect(step).toBeInViewport()
  await expect(step.getByRole('button', { name: /because:/ })).toContainText('Start by running the whole test suite')
  recordTask('c2_failed_step_in_a_session_that_went_on', c, 1, info)
  expect(errors).toEqual([])
})

test('d: set up a new project from the empty state', async ({ page }, info) => {
  const errors = watchErrors(page)
  const fresh = process.env.WEFT_E2E_EMPTY!
  await page.goto('/')
  const c = new Clicks(page)
  await page.getByRole('textbox', { name: 'New project name' }).fill(fresh)
  await c.click(page.getByRole('button', { name: 'Open project' }), '“Open project”')
  await expect(page.getByRole('heading', { name: 'No sessions yet' })).toBeVisible()
  // The snippets name this server and project.
  await expect(page.locator('.snippet pre')).toContainText(`weft.init("${fresh}"`)
  // Optional reading, not counted as a step of the task: the coding-agent plugin snippet.
  await page.getByRole('tab', { name: 'Claude Code' }).click()
  await expect(page.locator('.snippet pre')).toContainText(`--project ${fresh}`)
  await c.click(page.getByRole('button', { name: 'Send a test session' }), '“Send a test session”')
  await expect(page).toHaveURL(/\/sessions\/demo-/)
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Add a lint step to CI')
  await expect(page.locator('.callout').first()).toContainText('AgentShield blocked a tool call')
  await page.goto(`/${fresh}/sessions`)
  await expect(page.locator('.session-row')).toHaveCount(1)
  recordTask('d_new_project', c, 3, info)
  expect(errors).toEqual([])
})
