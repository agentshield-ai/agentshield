import { useEffect, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { Any, ChatMessage } from '../../api/types'
import { CopyButton, ErrorBox, Modal } from '../common'
import { errMsg, templateVars } from './objectUtils'
import { Segmented } from '../ui/Segmented'
import { Icon } from '../ui/Icons'
import './bits.css'

/** Button that asks for confirmation in a modal before running an async action. */
export function ConfirmButton({
  children,
  title,
  body,
  confirmLabel = 'Delete',
  onConfirm,
  className = 'btn sm danger',
  disabled,
  ariaLabel,
  tooltip,
}: {
  children: ReactNode
  title: ReactNode
  body: ReactNode
  confirmLabel?: string
  onConfirm: () => Promise<unknown>
  className?: string
  disabled?: boolean
  /** The button's accessible name when its content is an icon. */
  ariaLabel?: string
  tooltip?: string
}) {
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  return (
    <>
      <button
        type="button"
        className={className}
        disabled={disabled}
        aria-label={ariaLabel}
        title={tooltip}
        onClick={(e) => {
          e.stopPropagation()
          setOpen(true)
        }}
      >
        {children}
      </button>
      {open && (
        <Modal
          title={title}
          onClose={() => setOpen(false)}
          footer={
            <>
              <button type="button" className="qbtn" onClick={() => setOpen(false)}>
                Cancel
              </button>
              <button
                type="button"
                className="qbtn danger-solid"
                disabled={busy}
                onClick={async () => {
                  setBusy(true)
                  setErr(null)
                  try {
                    await onConfirm()
                    setOpen(false)
                  } catch (e) {
                    setErr(e)
                  } finally {
                    setBusy(false)
                  }
                }}
              >
                {busy ? 'Working…' : confirmLabel}
              </button>
            </>
          }
        >
          <div>{body}</div>
          <ErrorBox error={err} />
        </Modal>
      )}
    </>
  )
}

/** Alias chips with remove buttons and an inline "add alias" input. */
export function AliasEditor({
  projectId,
  objectId,
  digest,
  aliases,
  compact,
}: {
  projectId: string
  objectId: string
  digest: string
  aliases: string[]
  compact?: boolean
}) {
  const qc = useQueryClient()
  const [adding, setAdding] = useState(false)
  const [val, setVal] = useState('')
  const [err, setErr] = useState<string | null>(null)
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ['obj-versions', projectId, objectId] })
    void qc.invalidateQueries({ queryKey: ['object', projectId, objectId] })
    void qc.invalidateQueries({ queryKey: ['objects', projectId] })
  }
  const add = async () => {
    const a = val.trim()
    if (!a) return setAdding(false)
    if (a === 'latest' || /^v\d+$/.test(a)) return setErr(`"${a}" is reserved`)
    try {
      await api.objectAliasSet(projectId, objectId, digest, a)
      setVal('')
      setAdding(false)
      setErr(null)
      refresh()
    } catch (e) {
      setErr(errMsg(e))
    }
  }
  return (
    <span className="name-list" onClick={(e) => e.stopPropagation()}>
      {aliases.map((a) => (
        <span key={a} className="name-item">
          {a}
          {a !== 'latest' && (
            <button
              type="button"
              className="name-x"
              aria-label={`Remove alias ${a}`}
              title={`Remove alias ${a}`}
              onClick={async () => {
                try {
                  await api.objectAliasRemove(projectId, objectId, a)
                  refresh()
                } catch (e) {
                  setErr(errMsg(e))
                }
              }}
            >
              ✕
            </button>
          )}
        </span>
      ))}
      {adding ? (
        <input
          className="input name-input"
          autoFocus
          value={val}
          aria-label="New alias"
          placeholder="alias, e.g. prod"
          onChange={(e) => setVal(e.target.value.replace(/[^\w.-]/g, ''))}
          onKeyDown={(e) => {
            if (e.key === 'Enter') void add()
            if (e.key === 'Escape') setAdding(false)
          }}
          onBlur={() => void add()}
        />
      ) : (
        <button type="button" className="name-add" onClick={() => setAdding(true)} title="Add an alias to this version">
          Add an alias
        </button>
      )}
      {err && !compact && <span className="small" style={{ color: 'var(--error)' }}>{err}</span>}
    </span>
  )
}

/** Highlight {placeholders} in template text. */
export function TemplateText({ text }: { text: string }) {
  const parts = text.split(/(\{[A-Za-z_][\w.]*\})/g)
  return (
    <>
      {parts.map((p, i) =>
        /^\{[A-Za-z_][\w.]*\}$/.test(p) ? (
          <span key={i} className="tvar">
            {p}
          </span>
        ) : (
          <span key={i}>{p}</span>
        ),
      )}
    </>
  )
}

/** Code block with header (language label + copy button). */
export function CodeBlock({ code, language, title, maxHeight }: { code: string; language?: string; title?: ReactNode; maxHeight?: number }) {
  return (
    <div>
      <div className="code-head">
        <span className="grow truncate">{title}</span>
        {language && <span className="code-lang">{language === 'python' ? 'Python' : language}</span>}
        <CopyButton text={code} />
      </div>
      <pre className="code-block" style={maxHeight ? { maxHeight } : undefined}>
        <code>{code}</code>
      </pre>
    </div>
  )
}

/** Right-side drawer. */
export function Drawer({ title, onClose, children, actions }: { title: ReactNode; onClose: () => void; children: ReactNode; actions?: ReactNode }) {
  useEffect(() => {
    const h = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [onClose])
  return (
    <>
      <div className="drawer-backdrop" onClick={onClose} />
      <aside className="drawer" role="dialog" aria-modal="true">
        <div className="drawer-header">
          <h2 className="grow truncate">{title}</h2>
          {actions}
          <button type="button" className="modal-close" onClick={onClose} aria-label="Close">
            ✕
          </button>
        </div>
        <div className="drawer-body">{children}</div>
      </aside>
    </>
  )
}

export function Switch({ on, onChange, disabled, title, label }: { on: boolean; onChange: (v: boolean) => void; disabled?: boolean; title?: string; label?: string }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={on}
      aria-label={label}
      title={title}
      className={`switch ${on ? 'on' : ''}`}
      disabled={disabled}
      onClick={(e) => {
        e.stopPropagation()
        onChange(!on)
      }}
    />
  )
}

/** Small "Use this object" panel with SDK snippets. */
export function UsePanel({ refUri, name, version, kind }: { refUri: string; name: string; version: string; kind: 'dataset' | 'model' | 'prompt' | 'function' | 'other' }) {
  const [lang, setLang] = useState<'python' | 'typescript' | 'ref'>('python')
  const short = `${name}:${version}`
  const py =
    `import weft\n\nweft.init("${refUri.split('/').slice(3, 5).join('/')}")\n` +
    `${kind === 'function' ? 'fn' : 'obj'} = weft.get("${short}")\n` +
    (kind === 'dataset'
      ? `\nfor row in obj.rows:\n    print(row)\n`
      : kind === 'prompt'
        ? `\nmessages = obj.format(question="What is Weft?")\n`
        : kind === 'model'
          ? `\nresult = obj.predict(question="What is Weft?")\n`
          : kind === 'function'
            ? `\nresult = fn(...)  # traced\n`
            : '')
  const ts =
    `import * as weft from 'weft'\n\nawait weft.init('${refUri.split('/').slice(3, 5).join('/')}')\n` +
    `const obj = await weft.get('${short}')\n`
  return (
    <div className="col" style={{ gap: 10 }}>
      <Segmented
        label="Language"
        role="radio"
        size="sm"
        value={lang}
        onChange={setLang}
        options={[
          { id: 'python', label: 'Python' },
          { id: 'typescript', label: 'TypeScript' },
          { id: 'ref', label: 'Ref' },
        ]}
      />
      <div className="asset-code">
        {lang === 'python' && <CodeBlock code={py} language="python" />}
        {lang === 'typescript' && <CodeBlock code={ts} language="typescript" />}
        {lang === 'ref' && <CodeBlock code={refUri} language="ref" title="Pass this ref anywhere an asset is accepted" />}
      </div>
    </div>
  )
}

/** The middle of a long id cut out ("12b07323…71c7567c"); a ref keeps its name and the start of its digest ("…/support-eval:12b07323…"). */
export function shortId(text: string, keep = 8): string {
  const ref = /^weft:\/\/\/.*\/([^/]+):([0-9a-f]{16,})$/i.exec(text)
  if (ref) return `…/${ref[1]}:${ref[2].slice(0, 8)}…`
  return text.length > keep * 2 + 3 ? `${text.slice(0, keep)}…${text.slice(-keep)}` : text
}

/** A long id (a ref, a digest) on one line: the middle cut out, the full value in the tooltip and copied by the icon beside it. */
export function ShortCopy({ text, label = 'id' }: { text: string; label?: string }) {
  const [done, setDone] = useState(false)
  return (
    <span className={`short-copy ${done ? 'done' : ''}`}>
      <span className="short-copy-value" title={text}>
        {shortId(text)}
      </span>
      <button
        type="button"
        className="short-copy-btn"
        aria-label={`Copy the ${label} ${text}`}
        title={done ? 'Copied' : `Copy the full ${label}`}
        onClick={() => {
          void navigator.clipboard?.writeText(text).catch(() => undefined)
          setDone(true)
          setTimeout(() => setDone(false), 1200)
        }}
      >
        <Icon name={done ? 'ok' : 'copy'} size={13} />
      </button>
    </span>
  )
}
