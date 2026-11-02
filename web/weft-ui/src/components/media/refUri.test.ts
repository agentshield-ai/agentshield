import { describe, expect, it } from 'vitest'
import { detectMedia, isReferenceUri, referenceUriSpec } from './mediaUtils'

describe('bucket URIs as plain strings (reference buckets)', () => {
  it('detects whole-value s3:// and gs:// object URIs and takes the kind from the extension', () => {
    expect(detectMedia('s3://cust-media/frames/000123.png')).toEqual({
      kind: 'image',
      mimetype: 'image/png',
      filename: '000123.png',
      uri: 's3://cust-media/frames/000123.png',
    })
    expect(detectMedia('gs://bucket-1/a/b/clip.MP4')).toMatchObject({ kind: 'video', mimetype: 'video/mp4', uri: 'gs://bucket-1/a/b/clip.MP4' })
    expect(detectMedia('s3://b.example/x/voice.mp3')?.kind).toBe('audio')
    expect(detectMedia('s3://my_bucket/report.pdf')?.kind).toBe('pdf')
    expect(detectMedia('gs://bkt/photo.jpeg')?.kind).toBe('image')
    expect(detectMedia('s3://bkt/anim.gif')?.kind).toBe('image')
    expect(detectMedia('s3://bkt/movie.mov')?.kind).toBe('video')
    expect(detectMedia('s3://bkt/movie.webm')?.kind).toBe('video')
    // Surrounding whitespace is tolerated; the URI is trimmed.
    expect(detectMedia('  s3://bkt/k/a.png\n')).toMatchObject({ uri: 's3://bkt/k/a.png' })
    expect(detectMedia('s3://bkt/k/a%20b.png')).toMatchObject({ filename: 'a b.png' })
  })
  it('shows other or missing extensions as a download link (kind file)', () => {
    expect(detectMedia('s3://bkt/data/train.jsonl')).toMatchObject({ kind: 'file', uri: 's3://bkt/data/train.jsonl' })
    expect(detectMedia('s3://bkt/notes.md')?.kind).toBe('file')
    expect(detectMedia('gs://bkt/objects/0001')).toMatchObject({ kind: 'file', filename: undefined })
    expect(referenceUriSpec('s3://bkt/blob', 'image/png').kind).toBe('image')
  })
  it('does not treat prose, prefixes, other schemes or malformed URIs as media', () => {
    expect(detectMedia('see s3://bkt/a.png for the frame')).toBeNull()
    expect(detectMedia('s3://bkt/a.png and s3://bkt/b.png')).toBeNull()
    expect(detectMedia('s3://bkt/frames/')).toBeNull() // a prefix, not an object
    expect(detectMedia('s3://bkt')).toBeNull()
    expect(detectMedia('s3://bkt/')).toBeNull()
    expect(detectMedia('s3://Bad_Bucket/a.png')).toBeNull() // uppercase bucket names are invalid
    expect(detectMedia('s3://-bkt/a.png')).toBeNull()
    expect(detectMedia('az://container/a.png')).toBeNull()
    expect(detectMedia('ftp://host/a.png')).toBeNull()
    // Weave renders bucket URIs only; https strings stay text (links).
    expect(detectMedia('https://example.com/cat.png')).toBeNull()
    expect(detectMedia('S3://bkt/a.png')).toBeNull()
    expect(isReferenceUri(42)).toBe(false)
    expect(isReferenceUri('s3://bkt/' + 'a'.repeat(3000) + '.png')).toBe(false)
  })
})

describe('message parts carrying media', () => {
  it('detects GenAI uri parts (attach_media(uri=…))', () => {
    expect(detectMedia({ type: 'uri', uri: 's3://cust-media/frames/000125.png', mime_type: 'image/png', modality: 'image' })).toMatchObject({
      kind: 'image',
      uri: 's3://cust-media/frames/000125.png',
    })
    // Modality decides when the extension says nothing.
    expect(detectMedia({ type: 'uri', uri: 'gs://bkt/obj', modality: 'video' })).toMatchObject({ kind: 'video', uri: 'gs://bkt/obj' })
    expect(detectMedia({ type: 'uri', uri: 'https://cdn.example.com/a.wav' })).toMatchObject({ kind: 'audio', uri: 'https://cdn.example.com/a.wav' })
    expect(detectMedia({ type: 'uri', uri: 'weft-file://abcdefgh1234', mime_type: 'image/png' })).toMatchObject({ kind: 'image', digest: 'abcdefgh1234' })
    expect(detectMedia({ type: 'uri', uri: 'javascript:alert(1)' })).toBeNull()
    expect(detectMedia({ type: 'uri' })).toBeNull()
  })
  it('detects blob parts and Gemini inlineData / fileData', () => {
    expect(detectMedia({ type: 'blob', mime_type: 'image/png', content: 'AAAA' })).toMatchObject({ kind: 'image', dataUrl: 'data:image/png;base64,AAAA' })
    expect(detectMedia({ type: 'blob', mime_type: 'application/zip', content: 'AAAA' })).toBeNull()
    expect(detectMedia({ inlineData: { mimeType: 'audio/wav', data: 'UklG' } })).toMatchObject({ kind: 'audio' })
    expect(detectMedia({ fileData: { mimeType: 'video/mp4', fileUri: 'gs://bkt/v/clip' } })).toMatchObject({ kind: 'video', uri: 'gs://bkt/v/clip' })
  })
  it('leaves other typed parts alone', () => {
    expect(detectMedia({ type: 'text', text: 's3://bkt/a.png' })).toBeNull()
    expect(detectMedia({ type: 'file', file_id: 'file-abc' })).toBeNull()
    expect(detectMedia({ uri: 's3://bkt/a.png' })).toBeNull()
  })
})
