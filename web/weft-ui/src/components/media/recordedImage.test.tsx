import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { RecordedImage } from './RecordedImage'
import { MediaView } from './MediaView'
import { SafeMarkdown } from '../SafeMarkdown'
import { ChatView } from '../traces/ChatView'

// U3/U5: images and links from recorded data. The test environment has no
// window.location, so the page is http://localhost/.
/** Renders with a query client; `texts` are the already-loaded texts of inline Html/Markdown values. */
const withQuery = (n: ReactNode, texts: string[] = []) => {
  const qc = new QueryClient({ defaultOptions: { queries: { enabled: false } } })
  for (const t of texts) qc.setQueryData(['file-text', 'a/b', t.slice(0, 64)], t)
  return renderToStaticMarkup(<QueryClientProvider client={qc}>{n}</QueryClientProvider>)
}
const FOREIGN = 'http://127.0.0.1:6869/leak?t=TOKEN'
const srcs = (html: string) => [...html.matchAll(/\s(?:src|href|data)="([^"]*)"/g)].map((m) => m[1])

describe('RecordedImage', () => {
  it('shows data:image and same-origin images inline', () => {
    expect(renderToStaticMarkup(<RecordedImage url="data:image/png;base64,AAAA" alt="a" />)).toMatch(/^<img src="data:image\/png;base64,AAAA" alt="a" loading="lazy" referrerPolicy="no-referrer"\/>$/)
    expect(renderToStaticMarkup(<RecordedImage url="/api/v1/files/d?project_id=a%2Fb" />)).toContain('<img src="http://localhost/api/v1/files/d?project_id=a%2Fb"')
  })
  it('does not fetch an image on another host until asked', () => {
    const html = renderToStaticMarkup(<RecordedImage url={FOREIGN} alt="chart" />)
    expect(html).not.toContain('<img')
    expect(srcs(html)).toEqual([])
    expect(html).toContain('<button type="button"')
    expect(html).toContain('>Image from 127.0.0.1:6869</button>')
  })
  it('shows other schemes as text', () => {
    for (const bad of ["javascript:alert('x')", 'data:text/html;base64,PHNjcmlwdD4=', 'vbscript:x', 'blob:http://localhost/x']) {
      const html = renderToStaticMarkup(<RecordedImage url={bad} />)
      expect(html, bad).not.toMatch(/<img|<a /)
      expect(html).toContain('media-unsafe-url')
    }
  })
})

describe('SafeMarkdown', () => {
  it('turns a remote markdown image into a button and keeps data images', () => {
    const html = renderToStaticMarkup(<SafeMarkdown>{`see ![chart](${FOREIGN}) and ![dot](data:image/gif;base64,R0lGOD)`}</SafeMarkdown>)
    expect(html).not.toContain('6869/leak"')
    expect(srcs(html)).toEqual(['data:image/gif;base64,R0lGOD'])
    expect(html).toContain('Image from')
  })
  it('lets only http(s) links through, opened without a referrer', () => {
    const html = renderToStaticMarkup(<SafeMarkdown>{'[ok](https://example.com/x) [bad](javascript:alert(1)) [d](data:text/html,x) <https://auto.example>'}</SafeMarkdown>)
    expect(srcs(html)).toEqual(['https://example.com/x', 'https://auto.example/'])
    expect(html).toContain('target="_blank" rel="noopener noreferrer"')
    expect(html).toContain('<span class="media-unsafe-link" title="Link not opened: javascript:alert(1)">bad</span>')
    expect(html).not.toMatch(/href="(javascript|data):/)
  })
  it('renders raw HTML as text', () => {
    const html = renderToStaticMarkup(<SafeMarkdown>{'<img src=x onerror=alert(1)><script>alert(1)</script>'}</SafeMarkdown>)
    expect(html).not.toMatch(/<img|<script/)
  })
})

describe('chat image parts', () => {
  const chat = (parts: unknown[]) => withQuery(<ChatView projectId="a/b" messages={[{ role: 'user', content: parts }]} />)
  it('never puts a javascript: URL in an href or src', () => {
    const html = chat([
      { type: 'image_url', image_url: { url: "javascript:alert('x')" } },
      { type: 'input_image', image_url: 'JaVaScRiPt:alert(1)' },
      { type: 'image', source: { type: 'url', url: 'javascript:alert(2)' } },
      { type: 'image', source: { type: 'base64', media_type: 'text/html', data: 'PHNjcmlwdD4=' } },
    ])
    expect(srcs(html)).toEqual([])
    expect(html).not.toMatch(/<img|<iframe|<object/)
    expect(html.match(/media-unsafe-url/g)).toHaveLength(4)
  })
  it('loads a remote image_url only on request and shows data images inline', () => {
    const html = chat([
      { type: 'image_url', image_url: { url: FOREIGN } },
      { type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'AAAA' } },
    ])
    expect(srcs(html)).toEqual(['data:image/png;base64,AAAA'])
    expect(html).toContain('Image from')
  })
})

describe('MediaView', () => {
  const evil = btoa('<script>alert(document.domain)</script>')
  it('renders a Pdf recorded as text/html as a download, not an object or frame', () => {
    const html = withQuery(<MediaView projectId="a/b" value={{ _type: 'Pdf', mimetype: 'text/html', data: evil, filename: 'evil.pdf' }} />)
    expect(html).not.toMatch(/<object|<iframe|<embed|<a /)
    expect(html).toContain('<button type="button" class="media-dl">Download</button>')
    expect(srcs(html)).toEqual([])
  })
  it('shows a PDF as loading until its application/pdf blob exists', () => {
    const html = withQuery(<MediaView projectId="a/b" value={{ _type: 'Pdf', mimetype: 'application/pdf', data: 'JVBERi0=' }} />)
    expect(html).toContain('Loading PDF')
    expect(html).not.toContain('data:application/pdf')
  })
  it('renders HTML only in a sandboxed srcdoc frame', () => {
    const h = '<script>alert(1)</script>'
    const html = withQuery(<MediaView projectId="a/b" value={{ _type: 'Html', html: h }} />, [h])
    expect(html).toMatch(/<iframe [^>]*sandbox=""[^>]*srcDoc=/)
  })
  it('asks before loading media from a URL on another host', () => {
    for (const v of [{ _type: 'Image', url: `${FOREIGN}.png`, mimetype: 'image/png' }, { type: 'uri', uri: 'https://cdn.example/clip.mp4', mime_type: 'video/mp4' }]) {
      const html = withQuery(<MediaView projectId="a/b" value={v} />)
      expect(html).not.toMatch(/<img|<video|<audio/)
      expect(srcs(html)).toEqual([])
      expect(html).toMatch(/(Image|Video) from/)
    }
  })
  it('renders a markdown value with remote images as buttons', () => {
    const md = `![x](${FOREIGN})`
    const html = withQuery(<MediaView projectId="a/b" value={{ _type: 'Markdown', markup: md }} />, [md])
    expect(html).not.toContain('<img')
    expect(html).toContain('Image from')
  })
})
