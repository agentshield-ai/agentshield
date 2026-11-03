import { describe, expect, it } from 'vitest'
import { dataUrlText, detectMedia, kindFromMime, resolveContent } from './mediaUtils'

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
