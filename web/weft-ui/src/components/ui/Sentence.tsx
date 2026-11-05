// Sentence: generated text whose clauses link to what they count (DESIGN.md
// §7.1 rule 1, §7.2 rule 8). The words come from a tested function in
// src/lib/summaries.ts; this only renders them. Links are accent-coloured
// with a faint underline, so they never rely on colour alone.
//
//   <Sentence parts={['10 sessions ran. ', { text: '3 failed', to: '/p/sessions?status=error' }, '.']} />
import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'

export type SentencePart = string | { text: string; to?: string; title?: string; onClick?: () => void }

export function Sentence({ parts, size = 'outcome', as = 'p', className }: { parts: SentencePart[]; size?: 'outcome' | 'detail' | 'body'; as?: 'p' | 'span'; className?: string }) {
  const Tag = as
  const nodes: ReactNode[] = parts.map((p, i) => {
    if (typeof p === 'string') return p
    if (p.to)
      return (
        <Link key={i} to={p.to} title={p.title} className="sentence-link">
          {p.text}
        </Link>
      )
    if (p.onClick)
      return (
        <button key={i} type="button" className="sentence-link linkish" title={p.title} onClick={p.onClick}>
          {p.text}
        </button>
      )
    return p.text
  })
  return <Tag className={`sentence s-${size} ${className ?? ''}`}>{nodes}</Tag>
}

/** The plain text of a sentence (for tests, titles and aria-labels). */
export function sentenceText(parts: SentencePart[]): string {
  return parts.map((p) => (typeof p === 'string' ? p : p.text)).join('')
}
