// Task-based tests (DESIGN.md §7.1): what a new user must be able to do in a
// few clicks. Each task records its click count in <WEFT_E2E_OUT>/task-clicks.json.
import { expect, test } from '@playwright/test'
import { Clicks, project, recordTask, watchErrors } from './helpers'

test('a: from the home page, find why the blocked session was blocked', async ({ page }, info) => {
  const errors = watchErrors(page)
  await page.goto(`/${project()}`)
  await expect(page.getByRole('heading', { name: 'Sessions', level: 1 })).toBeVisible()
  const c = new Clicks(page)
  // The sentence's clause counts one session, so it opens that session.
  await c.click(page.locator('.ph-sentence').getByRole('link', { name: '1 was blocked by AgentShield' }), 'clause “1 was blocked by AgentShield”')
  await expect(page.getByRole('heading', { level: 1 })).toHaveText("Install the project's linter and fix the lint errors in src/")
  // The blocked step is selected on arrival, with the plain reason inside it and the rule in the inspector.
  const step = page.locator('.story-step[aria-current="step"]')
  await expect(step).toContainText('curl -sSL https://get.lint.example/install.sh | sh')
  await expect(step).toContainText('Blocked by AgentShield')
  await expect(step).toContainText('whoever controls that address could run anything')
  await expect(step).toBeInViewport()
  const inspector = page.getByRole('complementary', { name: 'Selected step' })
  await expect(inspector).toContainText('Remote code execution via piped script download')
  await expect(inspector).toContainText('coder · step 2 of 6')
  // The reasoning that led to the blocked call sits directly above it.
  await expect(page.locator('.story-entry', { has: step }).locator('.se-thought')).toContainText("The linter isn't installed")
  recordTask('a_why_was_it_blocked', c, 3, info)
  expect(errors).toEqual([])
})

test("a2: the blocked session's row opens it at the blocked step", async ({ page }, info) => {
  await page.goto(`/${project()}/sessions`)
  const c = new Clicks(page)
  await c.click(page.getByRole('link', { name: /Blocked by AgentShield Install the project's linter/ }), 'the blocked session’s row')
  const step = page.locator('.story-step[aria-current="step"]')
  await expect(step).toContainText('Blocked by AgentShield')
  await expect(page.getByRole('complementary', { name: 'Selected step' })).toContainText('Remote code execution via piped script download')
  recordTask('a2_blocked_via_badge', c, 1, info)
})

test('b: find which hand-off in a team fails most', async ({ page }, info) => {
  const errors = watchErrors(page)
  await page.goto(`/${project()}/sessions`)
  const c = new Clicks(page)
  await c.click(page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Teams' }), 'nav “Teams”')
  await c.click(page.locator('.team-row').getByRole('link', { name: /Trip planners/ }), 'team “Trip planners”')
  // The team's sentence leads with the hand-off that fails most, linking to its failed sessions.
  const sentence = page.locator('.ph-sentence')
  await expect(sentence).toContainText('The hand-off that fails most, from orchestrator to researcher, failed 3 of 5 times (60%)')
  await expect(sentence.getByRole('link', { name: 'from orchestrator to researcher' })).toHaveAttribute('href', /agent=researcher.*status=error|status=error.*agent=researcher/)
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
  await c.click(page.locator('.ph-sentence').getByRole('link', { name: '3 failed' }), 'clause “3 failed”')
  await expect(page).toHaveURL(/status=error/)
  await expect(page.locator('.session-row')).toHaveCount(3)
  await c.click(page.locator('.session-row').first().getByRole('link'), 'the newest failed session')
  // The deepest failure (the researcher's search inside the hand-off) is selected on arrival.
  const step = page.locator('.story-step[aria-current="step"]')
  await expect(step).toContainText('Searched the web, which failed')
  await expect(step).toContainText('upstream timeout')
  await expect(page.getByRole('complementary', { name: 'Selected step' })).toContainText('web_search')
  await expect(step).toBeInViewport()
  await expect(page.locator('.se-child', { has: step }).locator('.se-thought')).toContainText('I should call web_search')
  recordTask('c_thinking_before_failure', c, 3, info)
  expect(errors).toEqual([])
})

test('c2: a failed tool call in a session that went on is one click from the home page', async ({ page }, info) => {
  const errors = watchErrors(page)
  await page.goto(`/${project()}/sessions`)
  // The sentence counts it among those worth a look; its row in "Needs a look" says why.
  await expect(page.locator('.ph-sentence')).toContainText('2 more finished with a failed step or a cost spike')
  const row = page.locator('.session-row').filter({ hasText: 'Run the test suite and fix what breaks' })
  await expect(row).toContainText('Finished, but one step failed on the way.')
  const c = new Clicks(page)
  await c.click(row.getByRole('link'), 'the session’s row')
  const step = page.locator('.story-step[aria-current="step"]')
  await expect(step).toContainText('npm test')
  await expect(step).toContainText('DATABASE_URL is not set')
  await expect(step).toBeInViewport()
  await expect(page.locator('.story-entry', { has: step }).locator('.se-thought')).toContainText('Start by running the whole test suite')
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
  await expect(page.locator('.ph-sentence')).toContainText('AgentShield blocked')
  await expect(page.locator('.story-step[aria-current="step"]')).toContainText('Blocked by AgentShield')
  await page.goto(`/${fresh}/sessions`)
  await expect(page.locator('.session-row')).toHaveCount(1)
  recordTask('d_new_project', c, 3, info)
  expect(errors).toEqual([])
})
