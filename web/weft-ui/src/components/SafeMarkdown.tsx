import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { safeLinkHref } from '../lib/safeUrl'
import { RecordedImage } from './media/RecordedImage'

// Markdown from recorded data (messages, Markdown media, prose fields). No raw
// HTML (no rehype-raw). Links reach an href only when they are http(s) and open in a
// new tab without a Referer; other links render as their text. Images go
// through RecordedImage: inline for data:image and this server's URLs, a
// click-to-load button for other hosts, text for anything else.

/** react-markdown's default transform blanks unsafe URLs; the components below decide instead and show them as text. */
const keepUrl = (url: string) => url

const COMPONENTS: Components = {
  a: ({ href, title, children }) => {
    const safe = safeLinkHref(href)
    if (!safe)
      return (
        <span className="media-unsafe-link" title={href ? `Link not opened: ${href}` : undefined}>
          {children}
        </span>
      )
    return (
      <a href={safe} title={title} target="_blank" rel="noopener noreferrer">
        {children}
      </a>
    )
  },
  img: ({ src, alt }) => <RecordedImage url={src} alt={alt} className="markdown-img" />,
}

export function SafeMarkdown({ children }: { children: string }) {
  return (
    <ReactMarkdown remarkPlugins={[remarkGfm]} urlTransform={keepUrl} components={COMPONENTS}>
      {children}
    </ReactMarkdown>
  )
}
