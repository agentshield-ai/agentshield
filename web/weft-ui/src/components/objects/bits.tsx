import { useEffect, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { Any, ChatMessage } from '../../api/types'
import { CopyButton, ErrorBox, Modal } from '../common'
import { errMsg, templateVars } from './objectUtils'

/** Button that asks for confirmation in a modal before running an async action. */
export function ConfirmButton({
  children,
  title,
  body,
  confirmLabel = 'Delete',
  onConfirm,
  className = 'btn sm danger',
  disabled,
}: {
  children: ReactNode
  title: ReactNode
  body: ReactNode
  confirmLabel?: string
  onConfirm: () => Promise<unknown>
  className?: string
  disabled?: boolean
}) {
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  return (
    <>
      <button
        className={className}
        disabled={disabled}
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
              <button className="btn" onClick={() => setOpen(false)}>
                Cancel
              </button>
              <button
                className="btn primary"
                style={{ background: 'var(--error)', borderColor: 'var(--error)' }}
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
    void qc.invalidateQueries({ queryKey: ['obj', projectId, objectId] })
    void qc.invalidateQueries({ queryKey: ['objs', projectId] })
  }
  const add = async () => {
    const a = val.trim()
    if (!a) return setAdding(false)
    if (a === 'latest' || /^v\d+$/.test(a)) return setErr(`"${a}" is reserved`)
    try {
      await api.objAliasSet(projectId, objectId, digest, a)
      setVal('')
      setAdding(false)
      setErr(null)
      refresh()
    } catch (e) {
      setErr(errMsg(e))
    }
  }
  return (
    <span className="chips" onClick={(e) => e.stopPropagation()}>
      {aliases.map((a) => (
        <span key={a} className={`chip ${a === 'latest' ? '' : 'on'}`}>
          {a}
          {a !== 'latest' && (
            <button
              className="x"
              title={`Remove alias ${a}`}
              onClick={async () => {
                try {
                  await api.objAliasRemove(projectId, objectId, a)
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
          className="input"
          style={{ height: 22, width: 110, fontSize: 12 }}
          autoFocus
          value={val}
          placeholder="alias, e.g. prod"
          onChange={(e) => setVal(e.target.value.replace(/[^\w.-]/g, ''))}
          onKeyDown={(e) => {
            if (e.key === 'Enter') void add()
            if (e.key === 'Escape') setAdding(false)
          }}
          onBlur={() => void add()}
        />
      ) : (
        <button className="chip selectable" style={{ borderStyle: 'dashed' }} onClick={() => setAdding(true)} title="Add an alias to this version">
          + alias
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

function contentText(c: Any): string {
  if (typeof c === 'string') return c
  if (Array.isArray(c))
    return c
      .map((p) => (typeof p === 'string' ? p : p?.type === 'text' ? p.text : p?.type === 'image_url' ? '[image]' : JSON.stringify(p)))
      .join('\n')
  return JSON.stringify(c, null, 2)
}

/** Read-only list of chat messages with template variables highlighted. */
export function MessageList({ messages }: { messages: ChatMessage[] }) {
  if (!messages.length) return <div className="faint small">No messages</div>
  return (
    <div className="col">
      {messages.map((m, i) => (
        <div key={i} className="msg">
          <div className="msg-role">{m.role}</div>
          <div className="msg-body">
            <TemplateText text={contentText(m.content)} />
          </div>
        </div>
      ))}
    </div>
  )
}

export function TemplateVarChips({ texts }: { texts: string[] }) {
  const vars = templateVars(...texts)
  if (!vars.length) return <span className="faint small">none</span>
  return (
    <span className="chips">
      {vars.map((v) => (
        <span key={v} className="tvar">{`{${v}}`}</span>
      ))}
    </span>
  )
}

/** Code block with header (language label + copy button). */
export function CodeBlock({ code, language, title, maxHeight }: { code: string; language?: string; title?: ReactNode; maxHeight?: number }) {
  return (
    <div>
      <div className="code-head">
        <span className="grow truncate">{title}</span>
        {language && <span className="badge">{language}</span>}
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
          <button className="btn ghost icon" onClick={onClose} aria-label="Close">
            ✕
          </button>
        </div>
        <div className="drawer-body">{children}</div>
      </aside>
    </>
  )
}

export function Switch({ on, onChange, disabled, title }: { on: boolean; onChange: (v: boolean) => void; disabled?: boolean; title?: string }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={on}
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

export function Stat({ label, value, title }: { label: ReactNode; value: ReactNode; title?: string }) {
  return (
    <div className="stat" title={title}>
      <div className="label">{label}</div>
      <div className="value">{value}</div>
    </div>
  )
}

/** Small "Use this object" panel with SDK snippets. */
export function UsePanel({ refUri, name, version, kind }: { refUri: string; name: string; version: string; kind: 'dataset' | 'model' | 'prompt' | 'op' | 'other' }) {
  const [lang, setLang] = useState<'python' | 'typescript' | 'ref'>('python')
  const short = `${name}:${version}`
  const py =
    `import weft\n\nweft.init("${refUri.split('/').slice(3, 5).join('/')}")\n` +
    `${kind === 'op' ? 'fn' : 'obj'} = weft.ref("${short}").get()\n` +
    (kind === 'dataset'
      ? `\nfor row in obj.rows:\n    print(row)\n`
      : kind === 'prompt'
        ? `\nmessages = obj.format(question="What is Weft?")\n`
        : kind === 'model'
          ? `\nresult = obj.predict(question="What is Weft?")\n`
          : kind === 'op'
            ? `\nresult = fn(...)  # calls are traced\n`
            : '')
  const ts =
    `import * as weft from 'weft'\n\nawait weft.init('${refUri.split('/').slice(3, 5).join('/')}')\n` +
    `const obj = await weft.ref('${short}').get()\n`
  return (
    <div className="col">
      <div className="row">
        <div className="seg">
          <button className={lang === 'python' ? 'on' : ''} onClick={() => setLang('python')}>
            Python
          </button>
          <button className={lang === 'typescript' ? 'on' : ''} onClick={() => setLang('typescript')}>
            TypeScript
          </button>
          <button className={lang === 'ref' ? 'on' : ''} onClick={() => setLang('ref')}>
            Ref URI
          </button>
        </div>
      </div>
      {lang === 'python' && <CodeBlock code={py} language="python" />}
      {lang === 'typescript' && <CodeBlock code={ts} language="typescript" />}
      {lang === 'ref' && <CodeBlock code={refUri} language="ref" title="Pass this ref anywhere an object is accepted" />}
    </div>
  )
}
