import { describe, expect, it } from 'vitest'
import { dataUrlBytes, dataUrlText, detectMedia, kindFromMime, resolveContent } from './mediaUtils'

describe('media detection', () => {
  it('detects data URLs by mimetype', () => {
    expect(detectMedia('data:image/png;base64,AAAA')).toMatchObject({ kind: 'image', mimetype: 'image/png' })
    expect(detectMedia('data:application/pdf;base64,AAAA')?.kind).toBe('pdf')
    expect(detectMedia('data:text/html;charset=utf-8;base64,AAAA')?.kind).toBe('html')
    expect(detectMedia('data:application/octet-stream;base64,AAAA')).toBeNull()
    expect(detectMedia('just text')).toBeNull()
  })
  it('detects typed media with mime_type or mimetype', () => {
    expect(detectMedia({ _type: 'Image', file_digest: 'd', mime_type: 'image/png' })).toMatchObject({ kind: 'image', digest: 'd' })
    expect(detectMedia({ _type: 'Audio', file_digest: 'd', mime_type: 'audio/wav' })?.kind).toBe('audio')
    expect(detectMedia({ _type: 'Video', file_digest: 'd', mime_type: 'video/mp4', filename: 'a.mp4' })).toMatchObject({ kind: 'video', filename: 'a.mp4' })
    expect(detectMedia({ _type: 'PDF', file_digest: 'd', mime_type: 'application/pdf' })?.kind).toBe('pdf')
    expect(detectMedia({ _type: 'Html', file_digest: 'd', mimetype: 'text/html' })?.kind).toBe('html')
    expect(detectMedia({ _type: 'Markdown', file_digest: 'd', mime_type: 'text/markdown' })).toMatchObject({ kind: 'markdown', digest: 'd' })
    expect(detectMedia({ _type: 'Markdown', markup: '# hi' })).toMatchObject({ kind: 'markdown', text: '# hi' })
    expect(detectMedia({ _type: 'Image' })).toBeNull()
    expect(detectMedia({ _type: 'Dataset', rows: 'x' })).toBeNull()
  })
  it('detects Content by mimetype, with a digest or inline base64 data', () => {
    expect(detectMedia({ _type: 'Content', mimetype: 'application/pdf', filename: 'r.pdf', file_digest: 'd' })).toMatchObject({ kind: 'pdf', digest: 'd', filename: 'r.pdf' })
    expect(detectMedia({ _type: 'Content', mime_type: 'video/webm', file_digest: 'd' })?.kind).toBe('video')
    expect(detectMedia({ _type: 'Content', filename: 'notes.md', data: 'IyBoaQ==' })).toMatchObject({ kind: 'markdown', dataUrl: 'data:text/markdown;base64,IyBoaQ==' })
    expect(detectMedia({ _type: 'Content', mimetype: 'application/zip', file_digest: 'd' })?.kind).toBe('file')
  })
  it('maps mimetypes and decodes text', () => {
    expect(kindFromMime('text/plain; charset=utf-8')).toBe('text')
    expect(kindFromMime('application/json')).toBe('text')
    expect(kindFromMime(undefined)).toBe('file')
    expect(dataUrlText('data:text/plain;base64,aMOpbGxv')).toBe('héllo')
  })
})

// U1: a recorded mimetype must never turn into a same-origin document.
describe('untrusted mimetypes', () => {
  const html = btoa('<script>alert(document.domain)</script>')
  it('renders a PDF only as application/pdf; any other type becomes a download', () => {
    expect(detectMedia({ _type: 'Pdf', mimetype: 'text/html', data: html, filename: 'evil.pdf' })).toMatchObject({ kind: 'file', mimetype: 'text/html', filename: 'evil.pdf' })
    expect(detectMedia({ _type: 'PDF', mime_type: 'image/svg+xml', file_digest: 'd' })?.kind).toBe('file')
    expect(detectMedia({ _type: 'Pdf', mimetype: 'Application/PDF; x=1', file_digest: 'd' })?.kind).toBe('pdf')
    // No mimetype recorded: a PDF, and rendered as one.
    expect(detectMedia({ _type: 'Pdf', file_digest: 'd' })).toMatchObject({ kind: 'pdf', mimetype: 'application/pdf' })
    expect(detectMedia({ _type: 'Content', mimetype: 'application/pdf', data: 'JVBERi0=' })?.kind).toBe('pdf')
  })
  it('keeps images, audio and video only with a matching type', () => {
    expect(detectMedia({ _type: 'Image', mimetype: 'text/html', data: html })?.kind).toBe('file')
    expect(detectMedia({ _type: 'Audio', mime_type: 'application/xhtml+xml', file_digest: 'd' })?.kind).toBe('file')
    expect(detectMedia({ _type: 'Video', mime_type: 'image/png', file_digest: 'd' })?.kind).toBe('file')
    expect(detectMedia({ _type: 'Image', file_digest: 'd' })?.kind).toBe('image')
    // SVG is an image: it is only ever shown through <img>, where it runs no script.
    expect(detectMedia({ _type: 'Content', mimetype: 'image/svg+xml', data: 'PHN2Zy8+' })?.kind).toBe('image')
    expect(detectMedia({ type: 'blob', mime_type: 'text/html', content: html })?.kind).toBe('html')
  })
  it('renders HTML from its text in the sandboxed frame, never as a document', () => {
    expect(detectMedia({ _type: 'Content', mimetype: 'text/html', data: html })).toMatchObject({ kind: 'html' })
    expect(detectMedia(`data:text/html;base64,${html}`)?.kind).toBe('html')
    const legacy = { _type: 'CustomWeaveType', weave_type: { type: 'x.Content' }, files: { content: 'c', 'metadata.json': 'm' } }
    expect(resolveContent(detectMedia(legacy)!, { mimetype: 'text/html', filename: 'x.pdf' }).kind).toBe('html')
    const legacyImg = { _type: 'CustomWeaveType', weave_type: { type: 'PIL.Image.Image' }, files: { 'image.html': 'd' } }
    expect(detectMedia(legacyImg)?.kind).toBe('file')
  })
  it('decodes data URLs to bytes', () => {
    expect(Array.from(dataUrlBytes('data:application/pdf;base64,JVBE'))).toEqual([0x25, 0x50, 0x44])
    expect(new TextDecoder().decode(dataUrlBytes('data:text/plain,a%20b'))).toBe('a b')
  })
})
