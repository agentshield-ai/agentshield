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
  await page.getByRole('button', { name: /^Filter/ }).click()
  await page.getByRole('button', { name: /Views/ }).click()
  await page.getByRole('button', { name: 'Save current filters as a view…' }).click()
  await page.getByRole('textbox', { name: 'View name' }).fill('Failures')
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(page).toHaveURL(/view=sessions-failures/)
  await page.goto(`/${project()}/sessions`)
  await page.getByRole('button', { name: /^Filter/ }).click()
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

test('j/k move through the story and deep links survive reload', async ({ page }) => {
  await page.goto(`/${project()}/sessions/code-lint-1`)
  const steps = page.locator('.story-step')
  await expect(steps.first()).toBeVisible()
  // On arrival the blocked step (the second) is selected.
  await expect(steps.nth(1)).toHaveAttribute('aria-current', 'step')
  // Three quick presses, without waiting for the URL in between, move three steps (into the hand-off and out).
  await page.keyboard.press('j')
  await page.keyboard.press('j')
  await page.keyboard.press('j')
  const focused = page.locator('.story-step[aria-current="step"]')
  await expect(focused).toHaveCount(1)
  await expect(steps.nth(4)).toHaveAttribute('aria-current', 'step')
  const key = new URL(page.url()).searchParams.get('step')
  expect(key).toBeTruthy()
  // Six quick presses back stop at the first step, the user's request.
  for (let i = 0; i < 6; i++) await page.keyboard.press('k')
  await expect(steps.first()).toHaveAttribute('aria-current', 'step')
  await expect(steps.first()).toHaveClass(/bubble user/)
  const userKey = new URL(page.url()).searchParams.get('step')
  expect(userKey).toMatch(/^message:user:/)
  expect(userKey).not.toBe(key)
  // A deep link to a step is restored as the selected step after a reload.
  await page.reload()
  await expect(page.locator('.story-step[aria-current="step"]')).toHaveCount(1)
  await expect(steps.first()).toHaveAttribute('aria-current', 'step')
  // So is a deep link to a later step, scrolled into view.
  await page.goto(`/${project()}/sessions/code-lint-1?step=${encodeURIComponent(key!)}`)
  await expect(steps.nth(4)).toHaveAttribute('aria-current', 'step')
  await expect(page.locator('.story-step[aria-current="step"]')).toBeInViewport()
  await page.reload()
  await expect(steps.nth(4)).toHaveAttribute('aria-current', 'step')
  await expect(page.locator('.story-step[aria-current="step"]')).toBeInViewport()
  // The old Timeline, Reasoning and Security tabs open the Story.
  for (const tab of ['timeline', 'reasoning', 'security']) {
    await page.goto(`/${project()}/sessions/code-lint-1?tab=${tab}`)
    await expect(page.getByRole('tab', { name: 'Story' })).toHaveAttribute('aria-selected', 'true')
    await expect(page).not.toHaveURL(/tab=/)
  }
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

test('team view draws agents and offers the same data as a table; the old graph URL opens it', async ({ page }) => {
  await page.goto(`/${project()}/sessions/code-lint-1?tab=graph`)
  await expect(page).toHaveURL(/tab=team/)
  await expect(page.getByRole('tab', { name: 'Team' })).toHaveAttribute('aria-selected', 'true')
  await expect(page.getByRole('group', { name: /Delegation graph: 2 agents/ })).toBeVisible()
  await page.getByRole('radio', { name: 'Table' }).click()
  await expect(page.locator('table').first()).toContainText('coder')
  await expect(page.locator('table.handoff-table')).toContainText('reviewer')
})

test('replay with current rules shows a diff', async ({ page }) => {
  const status = await (await page.request.get(`/api/v1/shield/status?project_id=${encodeURIComponent(project())}`)).json()
  test.skip(!status.replay_enabled, 'server started without --shield-rules-dir')
  await page.goto(`/${project()}/sessions/code-lint-1`)
  await page.getByRole('complementary', { name: 'Selected step' }).getByRole('button', { name: 'Replay with the current rules' }).click()
  await expect(page.getByText(/Checked \d+ of \d+ tool calls/)).toBeVisible({ timeout: 20_000 })
})

test('every clause of the Sessions sentence opens exactly the sessions it counts, with money shown plainly', async ({ page }) => {
  const errors = watchErrors(page)
  await page.goto(`/${project()}/sessions`)
  await expect(page.locator('.session-row').first()).toBeVisible()
  // No floating-point noise in any amount.
  await expect(page.locator('main, .main').first()).not.toContainText(/\$\d*\.\d{5,}/)
  const clauses = page.locator('.ph-sentence a')
  await expect(clauses.first()).toBeVisible()
  // At most two sentences: the serious ones as links, then how many more need a look.
  expect((await page.locator('.ph-sentence').innerText()).match(/[.!?](\s|$)/g)?.length).toBeLessThanOrEqual(2)
  const n = await clauses.count()
  expect(n).toBeGreaterThanOrEqual(4)
  for (let i = 0; i < n; i++) {
    await page.goto(`/${project()}/sessions`)
    const clause = page.locator('.ph-sentence a').nth(i)
    const text = await clause.innerText()
    // "N more …" points at the "Needs a look" group (checked below).
    if ((await clause.getAttribute('href'))?.endsWith('#needs-a-look')) continue
    const exact = (await clause.getAttribute('title')) !== 'Lists these sessions together with the blocked ones'
    const count = Number(/^\d+/.exec(text)?.[0])
    await clause.click()
    if (/\/sessions\/[^/?]+/.test(new URL(page.url()).pathname)) {
      // A clause that counts one session opens it.
      expect(count, text).toBe(1)
      await expect(page.locator('.story, .view-section').first(), text).toBeVisible()
    } else if (exact) await expect(page.locator('.session-row'), text).toHaveCount(count)
  }
  // "N more …" points at "Needs a look": the rows there that the first sentence does not name.
  await page.goto(`/${project()}/sessions`)
  const more = page.locator('.ph-sentence').getByRole('link', { name: /more finished with something worth a look/ })
  const count = Number(/^\d+/.exec(await more.innerText())?.[0])
  expect(count).toBeGreaterThan(0)
  await more.click()
  const look = page.getByRole('region', { name: /^Needs a look/ })
  await expect(look.getByRole('heading')).toBeInViewport()
  await expect(page.locator('#needs-a-look')).toBeFocused()
  const glyphs = await look.locator('.lrow-glyph [role="img"]').evaluateAll((xs) => xs.map((x) => x.getAttribute('aria-label')))
  expect(glyphs.filter((g) => !['Failed', 'Blocked by AgentShield', 'Needs approval'].includes(g ?? '')).length).toBe(count)
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
  await expect(page.locator('.team-row').getByRole('link', { name: /Code crew/ })).toBeVisible()
  await expect(page.locator('.team-row').getByRole('link', { name: /Trip planners/ })).toHaveCount(0)
  await expect(page.getByRole('note')).toContainText('showing teams with a session that matches')
  // Agents by a session status: the agents of sessions that went on after a failed step.
  await page.goto(`/${project()}/agents?status=failed_step`)
  await expect(page.locator('.agent-row')).toHaveCount(2)
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
  // Add filter and Views sit behind the Filter button, as on Sessions.
  await page.getByRole('button', { name: /^Filter/ }).click()
  await page.getByRole('button', { name: /Views/ }).click()
  await page.getByRole('button', { name: 'Save current filters as a view…' }).click()
  await page.getByRole('textbox', { name: 'View name' }).fill('Failed traces')
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(page).toHaveURL(/view=traces-failed-traces/)
  await page.goto(`/${project()}/traces`)
  await page.getByRole('button', { name: /^Filter/ }).click()
  await page.getByRole('button', { name: /Views/ }).click()
  await page.getByRole('button', { name: 'Failed traces', exact: true }).click()
  await expect(page).toHaveURL(/status=error/)
  await expect(page).toHaveURL(/cols=output/)
})

test('the status chip keeps the same runs on Sessions and Traces', async ({ page }) => {
  const errors = watchErrors(page)
  const P = process.env.WEFT_E2E_STATUS!
  // Sessions, by their title; traces, by their top-level span (first match wins).
  const sessionOf: [string, string][] = [
    ['Status check: a run that fails', 'failer'],
    ['Status check: a run that recovers', 'recoverer'],
    ['Status check: a helper that fails', 'delegator'],
    ['Status check: a run where nothing fails', 'finisher'],
    ['Status check: a server span that fails around an agent that does not', 'wrapfails'],
    ['Status check: an agent that fails inside a server span that does not', 'wrapholds'],
    ['Status check: a linked helper that fails in its own trace', 'caller'],
    ['Session st-verdicts', 'verdicts'],
    ['st-inflight', 'inflight'],
  ]
  const traceOf: [RegExp, string][] = [
    [/\/api\/v1\/evaluate/, 'engine(finisher)'],
    [/\bevaluate\b/, 'engine(verdicts)'],
    ...['failer', 'recoverer', 'delegator', 'finisher', 'wrapfails', 'wrapholds', 'caller', 'linkedhelper'].map((a): [RegExp, string] => [new RegExp(`\\b${a}\\b`), a]),
    [/\binflighter\b/, 'inflight'],
  ]
  // A run in progress over OTLP: its finished steps (one a tool call that
  // failed) have arrived, its top-level span has not (OTLP sends a span when
  // it ends). It is running on both pages, listed once on Traces.
  const now = Date.now()
  const ns = (ms: number) => String((now - 60_000 + ms) * 1e6)
  const kv = (key: string, value: string) => ({ key, value: { stringValue: value } })
  const inflight = (id: string, name: string, at: number, attrs: Record<string, string>, failed = false) => ({
    traceId: '1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f',
    spanId: id,
    parentSpanId: 'a0a0a0a0a0a0a0a0',
    name,
    kind: 1,
    startTimeUnixNano: ns(at),
    endTimeUnixNano: ns(at + 500),
    attributes: Object.entries({ 'gen_ai.conversation.id': 'st-inflight', 'gen_ai.agent.name': 'inflighter', ...attrs }).map(([k, v]) => kv(k, v)),
    ...(failed ? { status: { code: 2, message: 'exit status 1' } } : {}),
  })
  const otlp = await page.request.post('/otel/v1/traces', {
    headers: { project_id: P },
    data: {
      resourceSpans: [{ resource: { attributes: [kv('service.name', 'e2e')] }, scopeSpans: [{ scope: { name: 'e2e' }, spans: [
        inflight('b1b1b1b1b1b1b1b1', 'chat inflighter', 0, { 'gen_ai.operation.name': 'chat' }),
        inflight('b2b2b2b2b2b2b2b2', 'execute_tool Bash', 1000, { 'gen_ai.operation.name': 'execute_tool', 'gen_ai.tool.name': 'Bash' }, true),
        inflight('b3b3b3b3b3b3b3b3', 'execute_tool Read', 2000, { 'gen_ai.operation.name': 'execute_tool', 'gen_ai.tool.name': 'Read' }),
      ] }] }],
    },
  })
  expect(otlp.ok()).toBe(true)
  // A trace's status is read off its top-level span, agent span or not; a
  // session's combines its traces'. A helper linked to its caller that
  // failed is a failed step (its caller's own trace succeeded). The engine's
  // evaluate traces take their session's status: "No agent data" for a
  // session known only from them.
  const expected: Record<string, { sessions: string[]; traces: string[] }> = {
    error: { sessions: ['failer', 'wrapfails'], traces: ['failer', 'wrapfails'] },
    failed_step: { sessions: ['caller', 'delegator', 'recoverer', 'wrapholds'], traces: ['delegator', 'linkedhelper', 'recoverer', 'wrapholds'] },
    ok: { sessions: ['finisher'], traces: ['caller', 'engine(finisher)', 'finisher'] },
    unknown: { sessions: ['verdicts'], traces: ['engine(verdicts)'] },
    running: { sessions: ['inflight'], traces: ['inflight'] },
  }
  for (const [status, want] of Object.entries(expected)) {
    await page.goto(`/${P}/sessions?status=${status}&time=all`)
    const rows = page.locator('.session-row')
    await expect(rows).toHaveCount(want.sessions.length)
    const sessions = (await rows.allInnerTexts()).map((t) => sessionOf.find(([title]) => t.includes(title))?.[1]).sort()
    expect(sessions, `Sessions, status=${status}`).toEqual(want.sessions)
    await page.goto(`/${P}/traces?status=${status}&time=all`)
    const traces = page.locator('.traces-table tbody tr')
    await expect(traces).toHaveCount(want.traces.length)
    const roots = (await traces.allInnerTexts()).map((t) => traceOf.find(([re]) => re.test(t))?.[1]).sort()
    expect(roots, `Traces, status=${status}`).toEqual(want.traces)
  }
  // The chip says what it keeps, and Traces applies it to the traces themselves, not through sessions.
  for (const status of ['failed_step', 'unknown']) {
    await page.goto(`/${P}/traces?status=${status}&time=all`)
    await expect(page.locator('.traces-table tbody tr').first()).toBeVisible()
    await expect(page.getByRole('note')).toHaveCount(0)
  }
  await page.goto(`/${P}/traces?status=failed_step&time=all`)
  await expect(page.getByRole('button', { name: 'Edit filter Status: Went on after a failed step' })).toHaveAttribute('title', /a step in it failed/)
  await expect(page.getByRole('button', { name: 'Edit filter Status: Went on after a failed step' })).toHaveAttribute('title', /linked to its caller/)
  // A row shows its run status, the span's own in the tooltip: the linked
  // helper's span failed, its run is a failed step of its caller's.
  const helperDot = page.locator('.traces-table tbody tr').filter({ hasText: 'linkedhelper' }).locator('[data-run-status]')
  await expect(helperDot).toHaveAttribute('data-run-status', 'failed_step')
  await expect(helperDot).toHaveAttribute('title', /Went on after a failed step \(this span failed\)/)
  // The in-flight run is listed under its first step, marked as such.
  await page.goto(`/${P}/traces?status=running&time=all`)
  const inflightRow = page.locator('.traces-table tbody tr').filter({ hasText: 'inflighter' })
  await expect(inflightRow.locator('[data-run-status]')).toHaveAttribute('data-run-status', 'running')
  await expect(inflightRow).toContainText('top-level span not received yet')
  // The session row says a step failed.
  await page.goto(`/${P}/sessions?status=failed_step&time=all`)
  await expect(page.locator('.session-row').filter({ hasText: 'a linked helper that fails' }).getByRole('link', { name: /failed step/ })).toBeVisible()
  expect(errors).toEqual([])
})

test('Traces: text finds any span, field conditions live in Add filter', async ({ page }) => {
  const errors = watchErrors(page)
  // Free text matches a child span's error message, not only the root span.
  await page.goto(`/${project()}/traces?q=${encodeURIComponent('3 tests failed')}&time=all`)
  await expect(page.locator('.traces-table tbody tr')).toHaveCount(1)
  // One filter button: field conditions are an "Add filter" option.
  await page.goto(`/${project()}/traces?time=all`)
  await expect(page.getByRole('button', { name: '+ Filter' })).toHaveCount(0)
  await page.getByRole('button', { name: /^Filter/ }).click()
  await page.getByRole('button', { name: 'Add filter' }).click()
  await page.getByRole('tab', { name: 'Advanced: field condition' }).click()
  await page.getByRole('combobox', { name: 'Field', exact: true }).fill('name')
  await page.getByRole('combobox', { name: 'Operator' }).selectOption('contains')
  await page.getByRole('textbox', { name: 'Value' }).fill('orchestrator')
  await page.getByRole('button', { name: 'Apply' }).click()
  await expect(page.getByRole('button', { name: 'Remove filter Field: name contains orchestrator' })).toBeVisible()
  await expect(page).toHaveURL(/f=/)
  await expect(page.locator('.traces-table tbody tr').first()).toBeVisible()
  expect(errors).toEqual([])
})

test('Sessions text search finds tool errors; the projects page has help', async ({ page }) => {
  const errors = watchErrors(page)
  await page.goto(`/${project()}/sessions?q=${encodeURIComponent('3 tests failed')}&time=all`)
  await expect(page.locator('.session-row')).toHaveCount(1)
  await page.goto('/')
  await expect(page.getByRole('link', { name: 'Guide: Projects' })).toHaveAttribute('href', '/help/setup.md')
  expect(errors).toEqual([])
})

test('Agents and Teams say when they show only the first 500', async ({ page }) => {
  const errors = watchErrors(page)
  const P = process.env.WEFT_E2E_MANY!
  await page.goto(`/${P}/agents`)
  await expect(page.getByRole('status').filter({ hasText: 'Showing the first 500 of 501 agents' })).toBeVisible()
  await expect(page.locator('.agent-row')).toHaveCount(500)
  await page.goto(`/${P}/teams`)
  await expect(page.getByRole('status').filter({ hasText: 'Showing the first 500 of 501 teams' })).toBeVisible()
  // Below the cap there is no note.
  await page.goto(`/${project()}/agents?time=all`)
  await expect(page.locator('.agent-row').first()).toBeVisible()
  await expect(page.getByText(/Showing the first \d+ of/)).toHaveCount(0)
  expect(errors).toEqual([])
})
