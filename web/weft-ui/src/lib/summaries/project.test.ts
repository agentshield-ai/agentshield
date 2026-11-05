// Table-driven tests of the Project pages' generated words (DESIGN.md §7.2 rule 8).
import { describe, expect, it } from 'vitest'
import {
  actionWords,
  annotationsSentence,
  apiKeysSentence,
  auditSentence,
  automationLine,
  automationsSentence,
  bucketsSentence,
  costsSentence,
  generalSentence,
  lastRunLine,
  membersSentence,
  projectLine,
  projectsSentence,
  retentionSentence,
  samplingSentence,
  secretsSentence,
  sensitiveSentence,
  teamsSentence,
  triggerWords,
  usersSentence,
} from './project'

const NOW = Date.parse('2026-11-05T12:00:00Z')
const ago = (min: number) => new Date(NOW - min * 60_000).toISOString()
const DAYS = 24 * 60

describe('automations', () => {
  it.each([
    [{ type: 'score_threshold' as const, scorer: 'polite', path: '', operator: 'lt' as const, threshold: 0.5 }, 'polite scores below 0.5'],
    [{ type: 'score_threshold' as const, scorer: 'judge', path: 'correct', operator: 'gte' as const, threshold: 1 }, 'judge (correct) scores at or above 1'],
    [{ type: 'error_rate' as const, names: [], window_minutes: 60, threshold: 0.1 }, 'at least 10% of runs fail within 60 minutes'],
    [{ type: 'error_rate' as const, names: ['answer', 'plan'], window_minutes: 1, threshold: 0.255 }, 'at least 25.5% of runs of answer and plan fail within 1 minute'],
    [{ type: 'error_rate' as const, op_names: ['old'], window_minutes: 5, threshold: 0.5 }, 'at least 50% of runs of old fail within 5 minutes'],
    [{ type: 'feedback_created' as const }, 'any feedback arrives'],
    [{ type: 'feedback_created' as const, feedback_types: ['weft.note'] }, 'feedback of type weft.note arrives'],
  ])('triggerWords %#', (t, want) => expect(triggerWords(t)).toBe(want))

  it.each([
    [{ type: 'slack' as const, url: 'https://hooks.slack.com/services/x' }, 'Slack (hooks.slack.com)'],
    [{ type: 'webhook' as const, url: 'https://example.com/h' }, 'a webhook at example.com'],
    [{ type: 'webhook' as const, url_secret: 'HOOK_URL' }, 'a webhook, at the address in the secret HOOK_URL'],
    [{ type: 'webhook' as const, url: 'not a url' }, 'a webhook at not a url'],
  ])('actionWords %#', (a, want) => expect(actionWords(a)).toBe(want))

  it('automationLine', () => {
    expect(automationLine({ trigger: { type: 'score_threshold', scorer: 'polite', operator: 'lt', threshold: 0.5 }, action: { type: 'slack', url: 'https://hooks.slack.com/x' } })).toBe(
      'When polite scores below 0.5, post to Slack (hooks.slack.com).',
    )
    expect(automationLine({ trigger: { type: 'feedback_created' }, action: { type: 'webhook', url: 'https://example.com' } })).toBe('When any feedback arrives, call a webhook at example.com.')
  })

  it.each([
    [undefined, 'Has not fired yet.'],
    [{ status: 'success' as const, created_at: ago(5), response_status: 200 }, 'Last delivered 5 min ago.'],
    [{ status: 'error' as const, created_at: ago(120), response_status: 500 }, 'Last delivery failed 2 h ago (HTTP 500).'],
  ])('lastRunLine %#', (r, want) => expect(lastRunLine(r, NOW)).toBe(want))

  const run = (min: number, status: 'success' | 'error' = 'success', test = false) => ({ status, test, created_at: ago(min) })
  it.each([
    ['none', [], [], false, 'No automations yet.'],
    ['quiet', [{ name: 'a', enabled: true }], [run(2 * DAYS)], false, '1 automation is on. It has not fired in the last 24 hours.'],
    ['mixed', [{ name: 'a', enabled: true }, { name: 'b', enabled: false }], [run(5), run(10, 'error'), run(20), run(30, 'success', true)], false, '2 automations; 1 is on. In the last 24 hours they fired 3 times; 1 delivery failed.'],
    ['all ok', [{ name: 'a', enabled: true }, { name: 'b', enabled: true }], [run(5)], false, 'Both automations are on. In the last 24 hours they fired once, and every delivery went through.'],
    ['all failed, capped', [{ name: 'a', enabled: false }], [run(5, 'error'), run(6, 'error')], true, '1 automation is off. In the last 24 hours it fired at least 2 times; every delivery failed.'],
    ['three off', [{ name: 'a', enabled: false }, { name: 'b', enabled: false }, { name: 'c', enabled: false }], [], false, 'All 3 automations are off. They have not fired in the last 24 hours.'],
  ])('automationsSentence %s', (_, automations, runs, capped, want) => expect(automationsSentence({ automations, runs, capped, now: NOW })).toBe(want))
})

describe('settings sentences', () => {
  it('general', () => {
    expect(generalSentence('e/p', { span_count: 12, trace_count: 3, object_count: 1, feedback_count: 4, storage_size_bytes: 2048 }, () => '2 KB')).toBe(
      'e/p holds 3 traces (12 spans), 1 saved asset, and 4 pieces of feedback, 2 KB in all.',
    )
    expect(generalSentence('e/p', { span_count: 2, trace_count: 1, object_count: 0, feedback_count: 0, storage_size_bytes: 1 }, () => '1 B')).toBe('e/p holds 1 trace (2 spans), 0 saved assets, and no feedback, 1 B in all.')
    expect(generalSentence('e/p', { span_count: 0, trace_count: 0, object_count: 0, feedback_count: 0, storage_size_bytes: 0 }, () => '0 B')).toBe('e/p is empty: nothing has been sent to it yet.')
  })
  it.each([
    [[], 'No model prices: costs are not computed.'],
    [[{ pricing_level: 'default' }, { pricing_level: 'default' }], '2 model prices, all built in. Add a price to set your own.'],
    [[{ pricing_level: 'project' }, { pricing_level: 'default' }, { pricing_level: 'default' }], '3 model prices: 1 set for this project, 2 built in.'],
    [[{ pricing_level: 'project' }], '1 model price: 1 set for this project.'],
  ])('costs %#', (ps, want) => expect(costsSentence(ps)).toBe(want))
  it.each([
    [['A', 'B', 'C'], [{ name: 'A', source: 'env' }, { name: 'B', source: 'db' }], 1, '2 of 3 provider keys are set, 1 from the server\'s environment. 1 custom provider.'],
    [['A', 'B'], [{ name: 'B', source: 'db' }], 0, '1 of 2 provider keys is set.'],
  ])('secrets %#', (names, set, prov, want) => expect(secretsSentence(names, set, prov)).toBe(want))
  it.each([
    [[], 'No annotation fields yet: reviewers can react and write notes, but not score.'],
    [['quality', 'correct'], '2 annotation fields: quality and correct.'],
  ])('annotations %#', (n, want) => expect(annotationsSentence(n)).toBe(want))
  it.each([
    [null, 'Spans and their feedback are kept forever.'],
    [30, 'Spans older than 30 days are deleted every hour, with their feedback. Datasets and other saved assets are kept.'],
  ])('retention %#', (d, want) => expect(retentionSentence(d)).toBe(want))
  it.each([
    [1, false, 'Every trace is kept.'],
    [0.4, false, '40% of traces are kept; the rest are dropped as they arrive.'],
    [0.4, true, 'Every trace is kept, and the 60% that sampling would drop are marked: a dry run.'],
    [0, false, 'No trace is kept: everything sent to this project is dropped.'],
  ])('sampling %#', (r, dry, want) => expect(samplingSentence(r, dry)).toBe(want))
  it.each([
    ['off', 9, 9, 0, 'Sensitive data is stored as sent.'],
    ['pii-v1', 9, 9, 0, 'Sensitive data is replaced with placeholders before it is stored, by all 9 detectors.'],
    ['pii-v1', 8, 9, 1, 'Sensitive data is replaced with placeholders before it is stored, by 8 of 9 detectors and 1 custom pattern.'],
  ])('sensitive %#', (p, e, t, n, want) => expect(sensitiveSentence(p, e, t, n)).toBe(want))

  const key = (o: Partial<{ revoked_at: string | null; last_used_at: string | null; created_at: string; admin: boolean }> = {}) => ({ revoked_at: null, last_used_at: ago(5), created_at: ago(200 * DAYS), admin: false, ...o })
  it.each([
    [[], false, 'No API keys: the server is open to anyone who can reach it.'],
    [[key({ revoked_at: ago(1) })], true, "No managed API keys; the server uses the keys it was started with."],
    [[key({ admin: true }), key({ last_used_at: ago(100 * DAYS) }), key()], true, '3 API keys; 1 is admin and 1 has not been used in 90 days.'],
    [[key({ last_used_at: null, created_at: ago(5) })], true, '1 API key; 1 has never been used.'],
    [[key({ admin: true }), key({ admin: true })], true, '2 API keys; all are admin.'],
  ])('apiKeys %#', (ks, auth, want) => expect(apiKeysSentence(ks, auth, NOW)).toBe(want))

  it.each([
    [[], 'Nobody has a role on e/p yet; server admins and the server\'s own keys still have access.'],
    [[{ role: 'admin', disabled: false }], '1 person has a role on e/p: 1 admin.'],
    [[{ role: 'admin', disabled: false }, { role: 'member', disabled: false }, { role: 'member', disabled: false }, { role: 'viewer', disabled: false }, { role: 'viewer', disabled: true }], '4 people have roles on e/p: 1 admin, 2 members, and 1 viewer.'],
  ])('members %#', (ms, want) => expect(membersSentence('e/p', ms)).toBe(want))

  it.each([
    [0, 90, true, 'No entries match these filters. Entries are kept for 90 days.'],
    [0, -1, false, 'No entries yet. Entries are kept forever.'],
    [124, 90, false, '124 entries recorded. Entries are kept for 90 days.'],
    [1, 0, true, '1 entry matches these filters. Entries are kept forever.'],
  ])('audit %#', (n, d, f, want) => expect(auditSentence(n, d, f)).toBe(want))

  it.each([
    [[], 'No storage buckets are registered, so media that lives in S3 or Cloud Storage shows as a link.'],
    [[{ scope: 'project' }, { scope: 'entity', entity: 'acme' }], '2 buckets are registered, 1 for this project and 1 for its team; media stored there shows inline.'],
  ])('buckets %#', (bs, want) => expect(bucketsSentence(bs)).toBe(want))
})

describe('users, teams, projects', () => {
  it.each([
    [[], 'No users yet.'],
    [[{ admin: true, disabled_at: null }, { admin: false, disabled_at: ago(1) }, { admin: false, disabled_at: null }], '3 users; 1 is a server admin and 1 is disabled.'],
  ])('users %#', (us, want) => expect(usersSentence(us)).toBe(want))
  it('teams', () => expect(teamsSentence([{ members: [1, 2] }, { members: [3] }])).toBe('2 teams with 3 members in all.'))
  it('projects', () => {
    expect(projectsSentence([], NOW).lead).toMatch(/^No projects yet/)
    const s = projectsSentence(
      [
        { project_id: 'a/b', last_activity: ago(60) },
        { project_id: 'c/d', last_activity: ago(5) },
        { project_id: 'e/f', last_activity: null },
      ],
      NOW,
    )
    expect(s.lead + s.latest + s.tail).toBe('3 projects. The most recently active is c/d, 5 min ago.')
    expect(projectLine({ trace_count: 1, object_count: 24 })).toBe('1 trace, 24 saved assets')
  })
})
