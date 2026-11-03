import { describe, expect, it } from 'vitest'
import { detectMedia, resolveContent } from '../components/media/mediaUtils'
import { isLegacyMedia } from './legacyMedia'

// Values stored by the other product's client through the compatibility
// adapter still render (docs/weft/DESIGN.md §8).
describe('legacy media encoding', () => {
  it('decodes media values tagged with the legacy encoding', () => {
    const img = { _type: 'CustomWeaveType', weave_type: { type: 'PIL.Image.Image' }, files: { 'image.jpg': 'd1' }, load_op: null }
    expect(detectMedia(img)).toMatchObject({ kind: 'image', digest: 'd1', mimetype: 'image/jpeg' })
    const wav = { _type: 'CustomWeaveType', weave_type: { type: 'wave.Wave_read' }, files: { 'audio.wav': 'd2', '_metadata.json': 'm' } }
    expect(detectMedia(wav)).toMatchObject({ kind: 'audio', digest: 'd2' })
    const video = { _type: 'CustomWeaveType', weave_type: { type: 'moviepy.video.io.VideoFileClip.VideoFileClip' }, files: { 'video.mp4': 'd3' } }
    expect(detectMedia(video)?.kind).toBe('video')
    const content = { _type: 'CustomWeaveType', weave_type: { type: 'weave.type_wrappers.Content.content.Content' }, files: { content: 'c', 'metadata.json': 'm' } }
    const spec = detectMedia(content)!
    expect(spec).toMatchObject({ kind: 'content', digest: 'c', metaDigest: 'm' })
    expect(resolveContent(spec, { mimetype: 'application/pdf', filename: 'x.pdf', size: 10 })).toMatchObject({ kind: 'pdf', filename: 'x.pdf', size: 10, digest: 'c' })
    expect(resolveContent(spec, {}).kind).toBe('file')
    const md = { _type: 'CustomWeaveType', weave_type: { type: 'rich.markdown.Markdown' }, val: { markup: '**b**' } }
    expect(detectMedia(md)).toMatchObject({ kind: 'markdown', text: '**b**' })
    expect(detectMedia({ _type: 'CustomWeaveType', weave_type: { type: 'Op' }, files: { 'obj.py': 'x' } })).toBeNull()
  })
  it('is consulted only for values carrying the legacy tag', () => {
    expect(isLegacyMedia({ _type: 'Image', file_digest: 'd' })).toBe(false)
    expect(isLegacyMedia({ _type: 'CustomWeaveType' })).toBe(true)
  })
})
