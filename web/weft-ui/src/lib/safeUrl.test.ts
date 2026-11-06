import { describe, expect, it } from 'vitest'
import { classifyImageUrl, httpUrl, isDataImageUrl, safeImageSrc, safeLinkHref } from './safeUrl'

const BASE = 'http://127.0.0.1:6345/acme/proj/spans/abc'

describe('URLs from recorded data', () => {
  it('lets only http(s) links through', () => {
    expect(safeLinkHref('https://example.com/a?b=1', BASE)).toBe('https://example.com/a?b=1')
    expect(safeLinkHref('  http://example.com  ', BASE)).toBe('http://example.com/')
    expect(safeLinkHref('/acme/proj/traces', BASE)).toBe('http://127.0.0.1:6345/acme/proj/traces')
    expect(safeLinkHref('//evil.example/x', BASE)).toBe('http://evil.example/x')
    for (const bad of [
      'javascript:alert(1)',
      'JavaScript:alert(1)',
      ' java\tscript:alert(1)',
      'java\nscript:alert(1)',
      '\u0001javascript:alert(1)',
      'vbscript:msgbox(1)',
      'data:text/html;base64,PHNjcmlwdD4=',
      'data:image/png;base64,AAAA',
      'blob:http://127.0.0.1:6345/0f0f',
      'file:///etc/passwd',
      'mailto:a@b.c',
      'ftp://x/y',
      '',
      '   ',
    ])
      expect(safeLinkHref(bad, BASE), bad).toBeNull()
    expect(safeLinkHref(undefined, BASE)).toBeNull()
    expect(safeLinkHref({ toString: () => 'https://x' }, BASE)).toBeNull()
  })
  it('lets http(s) and data:image/* reach an image src', () => {
    expect(safeImageSrc('data:image/png;base64,AAAA', BASE)).toBe('data:image/png;base64,AAAA')
    expect(safeImageSrc('data:image/svg+xml;utf8,<svg/>', BASE)).toBe('data:image/svg+xml;utf8,<svg/>')
    expect(safeImageSrc('https://cdn.example/a.png', BASE)).toBe('https://cdn.example/a.png')
    expect(safeImageSrc('javascript:alert(1)', BASE)).toBeNull()
    expect(safeImageSrc('data:text/html;base64,PHNjcmlwdD4=', BASE)).toBeNull()
    expect(safeImageSrc('data:,hello', BASE)).toBeNull()
    expect(isDataImageUrl('DATA:IMAGE/PNG;BASE64,AAAA')).toBe(true)
    expect(isDataImageUrl('data:image/png')).toBe(false)
    expect(httpUrl('https://x.example:8443/p', BASE)?.host).toBe('x.example:8443')
  })
  it('shows same-origin and data images inline, other hosts on request, other schemes as text', () => {
    expect(classifyImageUrl('data:image/gif;base64,R0lGOD', BASE)).toEqual({ kind: 'inline', src: 'data:image/gif;base64,R0lGOD' })
    expect(classifyImageUrl('/api/v1/files/abc?project_id=a%2Fb', BASE)).toEqual({ kind: 'inline', src: 'http://127.0.0.1:6345/api/v1/files/abc?project_id=a%2Fb' })
    expect(classifyImageUrl('http://127.0.0.1:6345/x.png', BASE)).toMatchObject({ kind: 'inline' })
    expect(classifyImageUrl('http://127.0.0.1:6869/leak?t=1', BASE)).toEqual({ kind: 'remote', src: 'http://127.0.0.1:6869/leak?t=1', host: '127.0.0.1:6869' })
    expect(classifyImageUrl('https://127.0.0.1:6345/x.png', BASE)).toMatchObject({ kind: 'remote' })
    expect(classifyImageUrl('javascript:alert(1)', BASE)).toEqual({ kind: 'unsafe', text: 'javascript:alert(1)' })
    expect(classifyImageUrl('data:text/html;base64,AAAA', BASE)).toMatchObject({ kind: 'unsafe' })
    expect(classifyImageUrl(42, BASE)).toEqual({ kind: 'unsafe', text: '' })
  })
})
