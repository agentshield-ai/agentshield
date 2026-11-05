// StatusGlyph: the small status mark of a row or step (DESIGN.md §7.2 rule
// 5). Colour plus shape plus an accessible label, never colour alone: red
// for failed and blocked, amber for approvals and warnings, green only for
// the success mark.
//
//   <StatusGlyph kind="blocked" />                 // 22 px, labelled "Blocked by AgentShield"
//   <StatusGlyph kind="ok" size={16} label="Succeeded" />
//   <StatusGlyph kind="failed" decorative />       // when the words sit next to it

export type GlyphKind = 'failed' | 'blocked' | 'approval' | 'warning' | 'cost' | 'ok' | 'running' | 'unknown' | 'check'

/** The default accessible label of each glyph. */
export const GLYPH_LABEL: Record<GlyphKind, string> = {
  failed: 'Failed',
  blocked: 'Blocked by AgentShield',
  approval: 'Needs approval',
  warning: 'Finished with a problem',
  cost: 'Cost spike',
  ok: 'Finished',
  running: 'Running',
  unknown: 'Outcome unknown',
  check: 'Succeeded',
}

// 24-unit paths, as drawn in the approved mockup.
const PATH: Record<GlyphKind, string> = {
  failed: 'M12 3a9 9 0 1 0 0 18a9 9 0 1 0 0-18Z M9 9l6 6 M15 9l-6 6',
  blocked: 'M12 3l7 3v5c0 4.5-3 8-7 10c-4-2-7-5.5-7-10V6l7-3Z M9.5 9.5l5 5',
  approval: 'M12 3a9 9 0 1 0 0 18a9 9 0 1 0 0-18Z M12 7.5v5.5 M12 16.5v0.5',
  warning: 'M12 4l9 16H3L12 4Z M12 10v4 M12 17v0.5',
  cost: 'M3 17l6-6 4 4 8-8 M15 7h6v6',
  ok: 'M12 3a9 9 0 1 0 0 18a9 9 0 1 0 0-18Z M8 12.5l2.8 2.8L16 9.5',
  running: 'M12 3a9 9 0 1 0 0 18a9 9 0 1 0 0-18Z M12 7v5l3 2',
  unknown: 'M12 3a9 9 0 1 0 0 18a9 9 0 1 0 0-18Z M9.5 9.5a2.5 2.5 0 1 1 3.5 2.3c-.6.3-1 .8-1 1.5v.4 M12 17v0.5',
  check: 'M5 12.5l4.5 4.5L19 7.5',
}

const TONE: Record<GlyphKind, string> = {
  failed: 'error',
  blocked: 'error',
  approval: 'warn',
  warning: 'warn',
  cost: 'warn',
  ok: 'ok',
  check: 'ok',
  running: 'accent',
  unknown: 'muted',
}

export function StatusGlyph({
  kind,
  size = 22,
  label,
  decorative,
  className,
}: {
  kind: GlyphKind
  size?: number
  /** Overrides the default label (GLYPH_LABEL). */
  label?: string
  /** Hide from assistive technology when the same words are shown next to it. */
  decorative?: boolean
  className?: string
}) {
  return (
    <svg
      className={`glyph tone-${TONE[kind]} ${className ?? ''}`}
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={kind === 'check' ? 2.4 : 2}
      strokeLinecap="round"
      strokeLinejoin="round"
      focusable="false"
      {...(decorative ? { 'aria-hidden': true } : { role: 'img', 'aria-label': label ?? GLYPH_LABEL[kind] })}
    >
      <path d={PATH[kind]} />
    </svg>
  )
}
