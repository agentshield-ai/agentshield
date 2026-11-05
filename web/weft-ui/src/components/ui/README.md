# Shared UI pieces (`src/components/ui`)

The building blocks of the redesigned pages (docs/weft/DESIGN.md §7.2).
The reference pages are Sessions (`pages/sessions/SessionsPage.tsx`) and
one session (`pages/sessions/SessionPage.tsx`); copy their structure.

Colours, type sizes and radii are CSS custom properties in `src/tokens.css`
(light and dark). Use them, never raw colours. Styles for everything below
are in `ui.css` (loaded globally).

## Page frame

```tsx
<div className="page-frame">           {/* list page: 920 px column, 36/40 px padding */}
<div className="page-frame detail">    {/* detail page with an inspector: 1120 px */}
```

## Components

| Component | File | Use |
|---|---|---|
| `PageHeader` | `PageHeader.tsx` | Title, the sentence, a quiet meta line, the "?" guide link. `variant="list"` puts `controls` (segmented, search, Filter) on the title line; `variant="detail"` adds a `back` link. |
| `HelpLink`, `helpUrl` | `PageHeader.tsx` | The small "?" next to a title, linking to `/help/<guide>`. Replaces the old "What is this page?" panels, which are gone. |
| `Sentence` | `Sentence.tsx` | Generated text with linked clauses: `parts={['10 sessions ran. ', { text: '3 failed', to }, '.']}`. Sizes `outcome` (21 px), `detail` (19 px), `body`. |
| `GroupedList`, `ListRow` | `GroupedList.tsx` | Things people read (sessions, agents, detections, objects): a heading with a quiet count, a white panel, rows with a glyph, title, one summary line, a meta line and the time; inset separators. The whole row is a link; an extra action goes in `trailing` (outside the link). |
| `StatusGlyph` | `StatusGlyph.tsx` | `failed`, `blocked`, `approval`, `warning`, `cost`, `ok`, `running`, `unknown`, `check`. Always labelled (`aria-label`), or `decorative` when the words sit beside it. |
| `Monogram` | `Monogram.tsx` | Who acted: `agent` (dark disc), `user` (grey), `nested` (white with a ring, inside a hand-off). Agents are told apart by name and monogram, never colour. |
| `Segmented` | `Segmented.tsx` | Views (`role="tabs"`, with `panelId`; render `role="tabpanel"`) or one value of a setting (`role="radio"`, e.g. the time range). Arrow keys move. |
| `SearchField` | `SearchField.tsx` | A list's search box. For the shared filter use `FilterSearchField` from `FilterBar.tsx` (same `key:value` syntax, `/` focuses it). |
| `FilterBar` + `FilterButton` + `FilterSearchField` | `FilterBar.tsx` | The one filter model (§7.1 rule 5). On a redesigned list page: `<FilterBar search="external" timePicker="none" collapsible open={open} …/>` under the header, `FilterSearchField` and `FilterButton` in the header's controls. Active chips always show; Add filter and Views open with the button. |
| `Inspector`, `KeyValueList`, `InspectorDivider`, `InspectorLinks`, `CopyText` | `Inspector.tsx` | Detail on demand beside the content (ids with copy, token counts, decisions, links to raw records). `sticky` keeps it in view. |
| `PlainTable` | `PlainTable.tsx` | Data compared across columns: hairline rows, grey headings, `className="num"` on numeric cells. Scrolls inside its own box. |
| `qbtn` (class) | `ui.css` | Quiet buttons (`qbtn`, `qbtn primary` for the one primary action a page may have). |
| `quiet-note` (class) | `ui.css` | A small grey line for notes (widened range, caps). Not a banner. |

## Generated words

Never write a summary by hand in a component: add a tested function to
`src/lib/summaries.ts` (table-driven tests in `summaries.test.ts`).

- `sessionSummaryLine(s, ctx)` the one line under a session row; `rowGlyph`, `needsALook`, `sessionMetaLine`.
- `sessionsSentence({ attention, time, filtered, rows, total })` the Sessions sentence as parts; clauses carry the filter (or the one session) they count.
- `sessionOutcome({ summary, tl, detections })` and `sessionMetaFigures(s)` for a session page.
- Helpers: `plural`, `countWord`, `joinClauses` (serial comma), `durationWords` ("4 seconds"), `shortDuration` ("150 ms", "9 s"), `offsetText`, `relativeTime` ("50 min ago"), `scoreText` ("goal completion 0.60"), `scorerWords`.
- `src/lib/ruleWords.ts` turns an AgentShield rule into plain words (a noun, a purpose and a one-sentence reason), falling back to the rule's title.

## Rules of thumb (§7.2)

- One sentence answers the page's question; no rows of number tiles. The old
  tile (`pages/_legacy/Metric.tsx`) is only for pages not yet redesigned:
  remove it from a page when you redo it, and delete the file with its last user.
- Say a fact once: not in a banner, a badge and a field list.
- Status is a glyph plus words; no coloured pills for status, agents or scores
  (`StatusPill`, `AgentChip`, `ScorePill`, `ActionBadge` in `bits.tsx` are the
  old style; do not use them on redesigned pages). A score is text: `scoreText`.
- Ids, raw JSON and token counts go in the inspector or the raw view.
- Plain words; internal terms (span, op, ref, digest) only in raw views, with a tooltip.
- Check 390 px: no horizontal page scroll; long unbroken names wrap (`overflow-wrap: anywhere`).
