import { describe, expect, it } from 'vitest'
import { detectMedia, mediaUri } from './mediaUtils'

describe('media stored by reference', () => {
  it('maps weft-file uris to digests and keeps external uris', () => {
    expect(mediaUri('weft-file://9CkzWUDje42we1970o46ApU4')).toEqual({ digest: '9CkzWUDje42we1970o46ApU4' })
    expect(mediaUri('weft-file://e/p/abcdef0123')).toEqual({ digest: 'abcdef0123' })
    expect(mediaUri('s3://bucket/e/p/img.png')).toEqual({ uri: 's3://bucket/e/p/img.png' })
    expect(mediaUri('https://cdn.example.com/a.mp4?sig=1')).toEqual({ uri: 'https://cdn.example.com/a.mp4?sig=1' })
    expect(mediaUri('javascript:alert(1)')).toBeNull()
    expect(mediaUri(undefined)).toBeNull()
  })
  it('detects Content and typed media with a uri', () => {
    expect(detectMedia({ _type: 'Content', uri: 's3://b/k/photo.jpg' })).toMatchObject({ kind: 'image', mimetype: 'image/jpeg', filename: 'photo.jpg', uri: 's3://b/k/photo.jpg' })
    expect(detectMedia({ _type: 'Audio', uri: 'https://x/clip', mime_type: 'audio/wav' })).toMatchObject({ kind: 'audio', uri: 'https://x/clip' })
    expect(detectMedia({ _type: 'PDF', uri: 'gs://b/report.pdf', filename: 'r.pdf' })).toMatchObject({ kind: 'pdf', filename: 'r.pdf' })
    expect(detectMedia({ _type: 'Image', uri: 'weft-file://abcdef012345', mimetype: 'image/png' })).toMatchObject({ kind: 'image', digest: 'abcdef012345' })
    expect(detectMedia({ _type: 'Content', uri: 'ftp://x/y' })).toBeNull()
  })
})
