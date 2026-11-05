// PageHeader: the top of every page (DESIGN.md §7.1 rules 1 and 12, §7.2).
// A title, the sentence that answers the page's question, one quiet line of
// figures, and a small "?" that opens the page's guide. No explanation
// panels: a page that needs one is redesigned.
//
// Two shapes, as in the approved mockups:
//   - list pages ("list"): a 30 px title with the page's controls on the
//     right (segmented time range, search), the sentence below at 21 px;
//   - detail pages ("detail"): a back link, a 28 px title, the outcome
//     sentence at 19 px and the meta line, at most 820 px wide.
//
//   <PageHeader title="Sessions" help="sessions.md" controls={<>…</>} sentence={<Sentence parts={…} />} />
//   <PageHeader variant="detail" back={{ to: sessionsUrl(pid), label: 'Sessions' }}
//     title={title} sentence={outcome} meta="Code crew · coder and reviewer · …" help="sessions.md#read-a-session" />
import { useEffect, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { setNavHint } from './navHint'

/** The in-app path of a guide: helpUrl('sessions.md#keyboard') → /help/sessions.md#keyboard. */
export function helpUrl(doc: string): string {
  return '/help/' + doc
}

/** The small "?" next to a page title: a link to the page's guide. */
export function HelpLink({ doc, topic }: { doc: string; topic?: string }) {
  return (
    <Link className="help-q" to={helpUrl(doc)} aria-label={topic ? `Guide: ${topic}` : 'Guide to this page'} title="Open the guide to this page">
      ?
    </Link>
  )
}

export function PageHeader({
  title,
  sentence,
  meta,
  help,
  helpTopic,
  controls,
  back,
  variant = 'list',
  className,
}: {
  title: ReactNode
  /** The sentence that answers the page's question (usually a <Sentence>). */
  sentence?: ReactNode
  /** One quiet line of figures. */
  meta?: ReactNode
  /** The guide this page's "?" opens, e.g. "sessions.md". */
  help?: string
  /** What the guide is about, for the "?" link's accessible name (defaults to the title when it is text). */
  helpTopic?: string
  /** Controls on the title's line (list pages): a segmented control, a search field, a Filter button. */
  controls?: ReactNode
  /** A link back to the list a detail page belongs to. */
  back?: { to: string; label: string }
  variant?: 'list' | 'detail'
  className?: string
}) {
  const topic = helpTopic ?? (typeof title === 'string' ? title : undefined)
  // The sidebar highlights the list this page's back link leads to (a dataset's page, at /objects/…, belongs to Datasets).
  const backTo = back?.to
  useEffect(() => {
    if (!backTo) return
    setNavHint(backTo)
    return () => setNavHint(null)
  }, [backTo])
  const heading = (
    <div className="ph-title-line">
      <h1 className="ph-title">{title}</h1>
      {help && <HelpLink doc={help} topic={topic} />}
    </div>
  )
  if (variant === 'detail')
    return (
      <header className={`ph detail ${className ?? ''}`}>
        {back && (
          <Link className="ph-back" to={back.to}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
              <path d="M15 5l-7 7 7 7" />
            </svg>
            {back.label}
          </Link>
        )}
        <div className="ph-block">
          {heading}
          {sentence && <div className="ph-sentence detail">{sentence}</div>}
          {meta && <p className="ph-meta">{meta}</p>}
        </div>
        {controls && <div className="ph-controls">{controls}</div>}
      </header>
    )
  return (
    <header className={`ph list ${className ?? ''}`}>
      <div className="ph-top">
        {heading}
        {controls && <div className="ph-controls">{controls}</div>}
      </div>
      {sentence && <div className="ph-sentence">{sentence}</div>}
      {meta && <p className="ph-meta">{meta}</p>}
    </header>
  )
}
