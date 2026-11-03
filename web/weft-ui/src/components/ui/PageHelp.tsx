// "What is this page?" (DESIGN.md §7.1 rule 12): a short, dismissible
// explanation with a link to the guide in docs/weft/.
import { useState, type ReactNode } from 'react'
import { Icon } from './Icons'

export const DOCS_BASE = 'https://github.com/agentshield-ai/agentshield/blob/main/docs/weft/'

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

export function docsUrl(doc: string): string {
  return DOCS_BASE + doc
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
        <a className="help-link" href={docsUrl(doc)} target="_blank" rel="noreferrer">
          Read the guide <Icon name="external" size={12} />
        </a>
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
