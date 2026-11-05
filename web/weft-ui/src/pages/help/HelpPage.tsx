// The guides in docs/weft, served by this server so they match the running
// version and work offline (DESIGN.md §7.1 rule 12). A readable article: a
// column of about 70 characters in Inter with quiet headings, the guides on
// the left (a menu on a phone) and "On this page" beside it (folded above the
// article on a narrower screen), built from the article's own headings.
import { useEffect, useState, type ReactNode } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { helpApi } from '../../api/help'
import { ErrorBox, Loading } from '../../components/common'
import { guideLink, slugger } from '../../lib/helpLinks'
import './help.css'

function textOf(children: ReactNode): string {
  if (typeof children === 'string' || typeof children === 'number') return String(children)
  if (Array.isArray(children)) return children.map(textOf).join('')
  if (children && typeof children === 'object' && 'props' in children) return textOf((children as { props: { children?: ReactNode } }).props.children)
  return ''
}

/** The guide /help opens with. */
export const FIRST_GUIDE = 'setup.md'

interface TocEntry {
  id: string
  text: string
  level: 2 | 3
}

export function HelpPage() {
  const { doc = FIRST_GUIDE } = useParams()
  const loc = useLocation()
  const navigate = useNavigate()
  const list = useQuery({ queryKey: ['help'], queryFn: helpApi.list, staleTime: Infinity })
  const page = useQuery({ queryKey: ['help', doc], queryFn: () => helpApi.read(doc), staleTime: Infinity })
  const items = list.data?.items ?? []
  const groups: [string, typeof items][] = [
    ['Guides', items.filter((g) => g.kind !== 'reference')],
    ['Reference', items.filter((g) => g.kind === 'reference')],
  ]
  const [toc, setToc] = useState<TocEntry[]>([])

  useEffect(() => {
    if (!page.data) return
    // The table of contents is read from the rendered headings, so its anchors are exactly theirs.
    const hs = Array.from(document.querySelectorAll<HTMLElement>('.help-doc h2, .help-doc h3'))
    setToc(hs.map((h) => ({ id: h.id, text: h.textContent ?? '', level: h.tagName === 'H2' ? 2 : 3 })))
    const id = decodeURIComponent(loc.hash.slice(1))
    if (id) document.getElementById(id)?.scrollIntoView()
    else window.scrollTo(0, 0)
  }, [page.data, loc.hash])

  // A fresh slugger on every render: anchors restart from 0 each time, so they stay the same across renders.
  const components = ((): Components => {
    const slug = slugger()
    const heading =
      (Tag: 'h1' | 'h2' | 'h3' | 'h4') =>
      ({ children }: { children?: ReactNode }) => (
        <Tag id={slug(textOf(children))} tabIndex={-1}>
          {children}
        </Tag>
      )
    return {
      h1: heading('h1'),
      h2: heading('h2'),
      h3: heading('h3'),
      h4: heading('h4'),
      a: ({ href, children }) => {
        const l = guideLink(href)
        switch (l.kind) {
          case 'guide':
            return <Link to={l.to}>{children}</Link>
          case 'anchor':
            return <Link to={{ hash: l.hash }}>{children}</Link>
          case 'external':
            return (
              <a href={l.href} target="_blank" rel="noreferrer">
                {children}
              </a>
            )
          default:
            return <span title={href}>{children}</span>
        }
      },
      table: ({ children }) => (
        <div className="help-table" tabIndex={0} role="region" aria-label="Table" style={{ overflowX: 'auto', maxWidth: '100%' }}>
          <table>{children}</table>
        </div>
      ),
    }
  })()

  // The title line is rendered on its own so "On this page" can follow it on a narrow screen.
  const md = page.data?.markdown ?? ''
  const m = /^\s*(# [^\n]*)\n/.exec(md)
  const title = m ? m[1] : ''
  const body = m ? md.slice(m[0].length) : md

  // Only the main sections, and their subsections when there are few.
  const shownToc = toc.filter((t) => t.level === 2).length >= 4 ? toc.filter((t) => t.level === 2) : toc
  const tocList = (
    <ul>
      {shownToc.map((t) => (
        <li key={t.id} className={t.level === 3 ? 'sub' : undefined}>
          <Link to={{ hash: t.id }}>{t.text}</Link>
        </li>
      ))}
    </ul>
  )

  return (
    <div className="help2">
      <nav className="help2-nav" aria-label="Guides">
        <button type="button" className="help2-back" onClick={() => (window.history.length > 1 ? navigate(-1) : navigate('/'))}>
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
            <path d="M15 5l-7 7 7 7" />
          </svg>
          Back
        </button>
        <label className="help2-pick">
          <span className="sr-only">Guide</span>
          <select className="input" value={doc} onChange={(e) => navigate(`/help/${e.target.value}`)}>
            {groups.map(([label, gs]) =>
              gs.length ? (
                <optgroup key={label} label={label}>
                  {gs.map((g) => (
                    <option key={g.name} value={g.name}>
                      {g.title}
                    </option>
                  ))}
                </optgroup>
              ) : null,
            )}
          </select>
        </label>
        <div className="help2-nav-groups">
          {groups.map(([label, gs]) =>
            gs.length ? (
              <section key={label} aria-label={label} style={{ marginBottom: 16 }}>
                <h2>{label}</h2>
                <ul>
                  {gs.map((g) => (
                    <li key={g.name}>
                      <Link to={`/help/${g.name}`} aria-current={g.name === doc ? 'page' : undefined}>
                        {g.title}
                      </Link>
                    </li>
                  ))}
                </ul>
              </section>
            ) : null,
          )}
        </div>
      </nav>
      <article className="help-doc markdown">
        {page.isLoading ? (
          <Loading />
        ) : page.error ? (
          <ErrorBox error={page.error} />
        ) : (
          <>
            {title && (
              <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
                {title}
              </ReactMarkdown>
            )}
            {shownToc.length > 2 && (
              <details className="help2-toc-inline">
                <summary>On this page</summary>
                {tocList}
              </details>
            )}
            <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
              {body}
            </ReactMarkdown>
          </>
        )}
      </article>
      {shownToc.length > 2 && (
        <nav className="help2-toc" aria-label="On this page">
          <h2>On this page</h2>
          {tocList}
        </nav>
      )}
    </div>
  )
}
