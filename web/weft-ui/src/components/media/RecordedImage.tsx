import { useState, type CSSProperties } from 'react'
import { classifyImageUrl, shortUrl } from '../../lib/safeUrl'

// An image whose URL comes from recorded data (a markdown image, an
// image_url message part). data:image/* and this server's own URLs show
// inline; an http(s) URL on another host is a button, "Image from <host>",
// and is fetched only when the viewer clicks it, so opening a session never
// contacts a host named in its data. Any other scheme (javascript:, data:text/html, ...)
// shows as text. Unit tested in recordedImage.test.tsx.

export function RecordedImage({ url, alt, className, style }: { url: unknown; alt?: string; className?: string; style?: CSSProperties }) {
  const [load, setLoad] = useState(false)
  const src = classifyImageUrl(url)
  if (src.kind === 'unsafe')
    return (
      <span className="media-unsafe-url mono" title="Not shown: only http(s) and data:image URLs are rendered">
        {alt ? `${alt}: ` : ''}
        {shortUrl(src.text) || '(empty image URL)'}
      </span>
    )
  if (src.kind === 'inline') return <img src={src.src} alt={alt ?? ''} className={className} style={style} loading="lazy" referrerPolicy="no-referrer" />
  if (!load)
    return (
      <button
        type="button"
        className="btn ghost sm media-remote-image"
        onClick={() => setLoad(true)}
        title={`${alt ? `${alt}: ` : ''}load ${shortUrl(src.src, 200)}`}
      >
        Image from {src.host}
      </button>
    )
  return (
    <a href={src.src} target="_blank" rel="noopener noreferrer" title={shortUrl(src.src, 200)}>
      <img src={src.src} alt={alt ?? ''} className={className} style={style} referrerPolicy="no-referrer" />
    </a>
  )
}
