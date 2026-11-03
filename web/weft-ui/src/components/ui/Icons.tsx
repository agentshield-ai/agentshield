// Small stroke icons (16px grid). Decorative unless given a title.
import type { ReactElement } from 'react'

const P: Record<string, ReactElement> = {
  sessions: <path d="M3 4h10M3 8h10M3 12h6" />,
  agents: (
    <>
      <circle cx="8" cy="5.5" r="2.5" />
      <path d="M3 13.5c.8-2.6 2.7-4 5-4s4.2 1.4 5 4" />
    </>
  ),
  teams: (
    <>
      <circle cx="5.5" cy="6" r="2" />
      <circle cx="11" cy="6" r="2" />
      <path d="M2 13c.6-2 1.8-3 3.5-3s2.9 1 3.5 3M8.5 12c.5-1.4 1.3-2 2.5-2s2.4.8 3 3" />
    </>
  ),
  shield: <path d="M8 2l5 2v4c0 3-2.2 5.2-5 6-2.8-.8-5-3-5-6V4z" />,
  traces: <path d="M2 4h4M4 8h6M6 12h8M2 4v0M10 8h4" />,
  dashboards: (
    <>
      <rect x="2.5" y="2.5" width="4.5" height="5" rx="1" />
      <rect x="9" y="2.5" width="4.5" height="3" rx="1" />
      <rect x="2.5" y="9.5" width="4.5" height="4" rx="1" />
      <rect x="9" y="7.5" width="4.5" height="6" rx="1" />
    </>
  ),
  monitors: (
    <>
      <path d="M2 9h2.5l1.5-4 2.5 7 1.5-3H14" />
    </>
  ),
  evaluations: <path d="M3 8.5l3 3 7-7" />,
  scores: <path d="M8 2.5l1.7 3.5 3.8.5-2.8 2.6.7 3.8L8 11.1 4.6 12.9l.7-3.8L2.5 6.5l3.8-.5z" />,
  leaderboards: <path d="M3 13V8M8 13V3M13 13V6" />,
  review: (
    <>
      <rect x="3" y="2.5" width="10" height="11" rx="1.5" />
      <path d="M5.5 6h5M5.5 8.5h5M5.5 11h3" />
    </>
  ),
  playground: <path d="M5 3.5l7 4.5-7 4.5z" />,
  datasets: (
    <>
      <ellipse cx="8" cy="4" rx="5" ry="1.8" />
      <path d="M3 4v8c0 1 2.2 1.8 5 1.8s5-.8 5-1.8V4M3 8c0 1 2.2 1.8 5 1.8s5-.8 5-1.8" />
    </>
  ),
  models: <path d="M8 2l5.5 3v6L8 14l-5.5-3V5zM2.5 5L8 8l5.5-3M8 8v6" />,
  prompts: <path d="M3 4.5h10M3 8h10M3 11.5h6M11.5 10.5l1.5 1.5-1.5 1.5" />,
  scorers: (
    <>
      <circle cx="8" cy="8" r="5.5" />
      <circle cx="8" cy="8" r="2" />
    </>
  ),
  functions: <path d="M10.5 2.5c-1.5 0-2 .8-2.3 2.5L7 12c-.3 1.6-.9 2-2.5 2M5 6.5h6" />,
  objects: <path d="M8 2.5l5 2.7v5.6L8 13.5 3 10.8V5.2z" />,
  automations: <path d="M9 1.5L3.5 9H8l-1 5.5L12.5 7H8z" />,
  settings: (
    <>
      <circle cx="8" cy="8" r="2" />
      <path d="M8 1.8v1.7M8 12.5v1.7M1.8 8h1.7M12.5 8h1.7M3.6 3.6l1.2 1.2M11.2 11.2l1.2 1.2M3.6 12.4l1.2-1.2M11.2 4.8l1.2-1.2" />
    </>
  ),
  help: (
    <>
      <circle cx="8" cy="8" r="6" />
      <path d="M6.3 6.2c.2-1 1-1.6 1.9-1.6 1 0 1.8.7 1.8 1.6 0 1.3-1.9 1.4-1.9 2.8" />
      <circle cx="8" cy="11.4" r=".3" />
    </>
  ),
  info: (
    <>
      <circle cx="8" cy="8" r="6" />
      <path d="M8 7.3v3.7" />
      <circle cx="8" cy="5.2" r=".3" />
    </>
  ),
  menu: <path d="M2.5 4h11M2.5 8h11M2.5 12h11" />,
  close: <path d="M4 4l8 8M12 4l-8 8" />,
  sun: (
    <>
      <circle cx="8" cy="8" r="2.8" />
      <path d="M8 1.5v1.5M8 13v1.5M1.5 8H3M13 8h1.5M3.4 3.4l1 1M11.6 11.6l1 1M3.4 12.6l1-1M11.6 4.4l1-1" />
    </>
  ),
  moon: <path d="M12.5 10A5.5 5.5 0 016 3.5a5.5 5.5 0 106.5 6.5z" />,
  ok: (
    <>
      <circle cx="8" cy="8" r="6" />
      <path d="M5.3 8.2l1.8 1.8 3.6-3.8" />
    </>
  ),
  error: (
    <>
      <circle cx="8" cy="8" r="6" />
      <path d="M6 6l4 4M10 6l-4 4" />
    </>
  ),
  running: (
    <>
      <circle cx="8" cy="8" r="6" />
      <path d="M8 4.8V8l2 1.5" />
    </>
  ),
  block: (
    <>
      <circle cx="8" cy="8" r="6" />
      <path d="M3.8 12.2l8.4-8.4" />
    </>
  ),
  approval: (
    <>
      <path d="M8 2l6 11H2z" />
      <path d="M8 6.5v3" />
      <circle cx="8" cy="11.3" r=".3" />
    </>
  ),
  log: (
    <>
      <circle cx="8" cy="8" r="6" />
      <path d="M8 7.3v3.7" />
      <circle cx="8" cy="5.2" r=".3" />
    </>
  ),
  reasoning: (
    <>
      <path d="M5.5 12.5h5M6.3 14.2h3.4" />
      <path d="M8 1.8a4.2 4.2 0 00-2.5 7.6c.5.4.8 1 .8 1.6v.2h3.4V11c0-.6.3-1.2.8-1.6A4.2 4.2 0 008 1.8z" />
    </>
  ),
  tool: <path d="M10.2 2.3a3 3 0 00-3.7 3.9L2.5 10.2l1.3 1.3 2 2 4-4a3 3 0 003.9-3.7l-1.8 1.8-1.7-.4-.4-1.7z" />,
  llm: (
    <>
      <rect x="2.5" y="3" width="11" height="8" rx="2" />
      <path d="M5.5 14l1.5-3h2l1.5 3" />
    </>
  ),
  handoff: <path d="M2.5 8h9M8.5 4.5L12 8l-3.5 3.5" />,
  ret: <path d="M13.5 8h-9M7.5 4.5L4 8l3.5 3.5" />,
  message: <path d="M3 3.5h10v7H7l-3 2.5v-2.5H3z" />,
  turn: (
    <>
      <circle cx="8" cy="8" r="2" />
      <path d="M8 2v4M8 10v4" />
    </>
  ),
  copy: (
    <>
      <rect x="5" y="5" width="8" height="8.5" rx="1.5" />
      <path d="M3 10.5V3.5C3 2.9 3.4 2.5 4 2.5h6" />
    </>
  ),
  external: <path d="M9.5 2.5h4v4M13.5 2.5L7.5 8.5M12 9.5v3c0 .6-.4 1-1 1H4c-.6 0-1-.4-1-1V5c0-.6.4-1 1-1h3" />,
  chevron: <path d="M6 4l4 4-4 4" />,
  down: <path d="M4 6l4 4 4-4" />,
  plus: <path d="M8 3v10M3 8h10" />,
  filter: <path d="M2.5 3.5h11l-4.3 5v4l-2.4 1.2V8.5z" />,
  search: (
    <>
      <circle cx="7" cy="7" r="4.3" />
      <path d="M10.2 10.2l3.3 3.3" />
    </>
  ),
  bookmark: <path d="M4 2.5h8v11l-4-2.8-4 2.8z" />,
  trash: <path d="M3 4.5h10M6 4.5V3h4v1.5M4.5 4.5l.6 9h5.8l.6-9" />,
  replay: <path d="M3 8a5 5 0 105-5H5.5M5.5 1.2L3.7 3 5.5 4.8" />,
  clock: (
    <>
      <circle cx="8" cy="8" r="5.8" />
      <path d="M8 4.8V8l2.2 1.4" />
    </>
  ),
  cost: <path d="M10.5 4.5c-.5-.8-1.4-1.2-2.5-1.2-1.6 0-2.7.8-2.7 2s1.2 1.7 2.7 2 2.8.8 2.8 2.1-1.2 2.1-2.8 2.1c-1.2 0-2.2-.5-2.7-1.3M8 1.8v1.5M8 12.5V14" />,
  spark: <path d="M8 2v3M8 11v3M2 8h3M11 8h3M4 4l2 2M10 10l2 2M4 12l2-2M10 6l2-2" />,
  graph: (
    <>
      <circle cx="4" cy="4" r="1.8" />
      <circle cx="12" cy="4" r="1.8" />
      <circle cx="8" cy="12" r="1.8" />
      <path d="M5.4 5.2l1.8 5M10.6 5.2l-1.8 5M5.8 4h4.4" />
    </>
  ),
  timeline: <path d="M4 2.5v11M4 4.5h8M4 8h6M4 11.5h7" />,
  raw: <path d="M5.5 4L2.5 8l3 4M10.5 4l3 4-3 4" />,
  keyboard: (
    <>
      <rect x="1.5" y="4" width="13" height="8" rx="1.5" />
      <path d="M4 6.5h.5M6.5 6.5H7M9 6.5h.5M11.5 6.5h.5M5 9.5h6" />
    </>
  ),
}

export type IconName = keyof typeof P

export function Icon({ name, size = 16, title, className }: { name: IconName | string; size?: number; title?: string; className?: string }) {
  return (
    <svg
      className={`icon ${className ?? ''}`}
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.5}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden={title ? undefined : true}
      role={title ? 'img' : undefined}
      aria-label={title}
      focusable="false"
    >
      {P[name] ?? P.info}
    </svg>
  )
}
