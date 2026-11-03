// "What is this page?" (DESIGN.md §7.1 rule 12): a short, dismissible
// explanation with a link to the guide in docs/weft/, which the server
// embeds and the UI shows at /help/<guide>.
import { useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { Icon } from './Icons'

const key = (id: string) => `weft.help.${id}`

function readDismissed(id: string): boolean {
  try {
    return localStorage.getItem(key(id)) === 'dismissed'
  } catch {
    return false
  }
}

function writeDismissed(id: string, v: boolean) {
  try {
    if (v) localStorage.setItem(key(id), 'dismissed')
    else localStorage.removeItem(key(id))
  } catch {
    /* storage unavailable: the panel just shows again next time */
  }
}

/** The in-app path of a guide, e.g. docsUrl('sessions.md#keyboard') → /help/sessions.md#keyboard. */
export function docsUrl(doc: string): string {
  return '/help/' + doc
}

/** The panel, plus a header button that brings it back once dismissed. */
export function usePageHelp(id: string, title: string, body: ReactNode, doc: string) {
  const [open, setOpen] = useState(() => !readDismissed(id))
  const panel = open ? (
    <aside className="help-panel" aria-label={`About ${title}`}>
      <Icon name="help" size={18} />
      <div className="grow">
        <div className="help-title">What is this page?</div>
        <div className="help-body">{body}</div>
        <Link className="help-link" to={docsUrl(doc)}>
          Read the guide
        </Link>
      </div>
      <button
        type="button"
        className="icon-btn"
        aria-label="Dismiss this help"
        onClick={() => {
          writeDismissed(id, true)
          setOpen(false)
        }}
      >
        <Icon name="close" size={14} />
      </button>
    </aside>
  ) : null
  const button = open ? null : (
    <button
      type="button"
      className="btn ghost sm"
      onClick={() => {
        writeDismissed(id, false)
        setOpen(true)
      }}
    >
      <Icon name="help" size={14} /> What is this page?
    </button>
  )
  return { panel, button }
}
