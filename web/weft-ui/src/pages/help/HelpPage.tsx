// The guides in docs/weft, served by this server so they match the running
// version and work offline (DESIGN.md §7.1 rule 12).
import { useEffect, useMemo, type ReactNode } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { helpApi } from '../../api/help'
import { ErrorBox, Loading } from '../../components/common'
import { guideLink, slugger } from '../../lib/helpLinks'

function textOf(children: ReactNode): string {
  if (typeof children === 'string' || typeof children === 'number') return String(children)
  if (Array.isArray(children)) return children.map(textOf).join('')
  if (children && typeof children === 'object' && 'props' in children) return textOf((children as { props: { children?: ReactNode } }).props.children)
  return ''
}

export function HelpPage() {
  const { doc = 'README.md' } = useParams()
  const loc = useLocation()
  const navigate = useNavigate()
  const list = useQuery({ queryKey: ['help'], queryFn: helpApi.list, staleTime: Infinity })
  const page = useQuery({ queryKey: ['help', doc], queryFn: () => helpApi.read(doc), staleTime: Infinity })

  useEffect(() => {
    if (!page.data) return
    const id = decodeURIComponent(loc.hash.slice(1))
    if (id) document.getElementById(id)?.scrollIntoView()
    else window.scrollTo(0, 0)
  }, [page.data, loc.hash])

  const components = useMemo<Components>(() => {
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
    }
    // A fresh slugger per document keeps repeated headings numbered from 0.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [page.data])

  return (
    <div className="help-page">
      <nav className="help-nav" aria-label="Guides">
        <button type="button" className="btn ghost sm" onClick={() => (window.history.length > 1 ? navigate(-1) : navigate('/'))}>
          ← Back
        </button>
        <h2 className="small muted">Guides</h2>
        <ul>
          {(list.data?.items ?? []).map((g) => (
            <li key={g.name}>
              <Link to={`/help/${g.name}`} aria-current={g.name === doc ? 'page' : undefined} className={g.name === doc ? 'active' : undefined}>
                {g.title}
              </Link>
            </li>
          ))}
        </ul>
      </nav>
      <article className="help-doc markdown">
        {page.isLoading ? <Loading /> : page.error ? <ErrorBox error={page.error} /> : <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>{page.data?.markdown ?? ''}</ReactMarkdown>}
      </article>
    </div>
  )
}
