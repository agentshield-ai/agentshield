// Image helpers for the playground (unit tested in playground.test.ts).

/** An <img> src for one image of an /image/create response: a data URL for b64_json, else a safe http(s) or data URL. */
export function imageSrc(im: { b64_json?: string; url?: string } | null | undefined): string | null {
  if (!im) return null
  if (typeof im.b64_json === 'string' && /^[A-Za-z0-9+/=\s]+$/.test(im.b64_json)) {
    const b = im.b64_json.replace(/\s+/g, '')
    // PNG, JPEG, WebP and GIF magic numbers in base64.
    const mime = b.startsWith('/9j/') ? 'image/jpeg' : b.startsWith('UklGR') ? 'image/webp' : b.startsWith('R0lGOD') ? 'image/gif' : 'image/png'
    return `data:${mime};base64,${b}`
  }
  if (typeof im.url === 'string' && (/^https?:\/\//i.test(im.url) || /^data:image\/(png|jpe?g|webp|gif);base64,/i.test(im.url))) return im.url
  return null
}
