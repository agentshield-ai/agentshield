# Shared UI pieces (`src/components/ui`)

The building blocks of the redesigned pages (docs/weft/DESIGN.md §7.2).
The reference pages are Sessions (`pages/sessions/SessionsPage.tsx`) and
one session (`pages/sessions/SessionPage.tsx`); copy their structure.

Colours, type sizes and radii are CSS custom properties in `src/tokens.css`
(light and dark). Use them, never raw colours. Styles for everything below
are in `ui.css` (loaded globally).

## Page frame

```tsx
<div className="page-frame">              {/* list page: 920 px column, centred, 36/40 px padding */}
<div className="page-frame detail">       {/* detail page: the same 920 px column, a back link above the title */}
<div className="page-frame detail wide">  {/* a page with an inspector beside its content: 1120 px, centred */}
```

A detail page that holds an `<aside class="inspector">` gets 1120 px without
`wide`; add `wide` when the inspector comes and goes (as on a session's
Story view) so the column does not change width between views. The sidebar
highlights the list a page's `PageHeader` `back` link leads to (a dataset
at `/objects/…` highlights Datasets), else the one its URL names.

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
| `AgentGraphView`, `GraphTables`, `HandoffTable` | `AgentGraphView.tsx` | The delegation graph (runs and failed runs per agent, hand-offs with failures and median time). Under 560 px it shows tables instead; pass `tablesOnPage` when the page lists the same figures in its own tables (no second copy, a note at narrow widths). `GraphWithTables` puts the graph in a panel with the tables behind a "Show as a table" disclosure (say it once), and shows the tables alone where the graph does not fit. |
| `PlainTable` | `PlainTable.tsx` | Data compared across columns: hairline rows, grey headings, `className="num"` on numeric cells. Scrolls inside its own box. `count` adds a grey number after a visible caption. `stack` turns each row into two lines below 600 px (name, then "7 runs · 3 failed · $0.23"); headings steer the words with `data-stack="run|runs"` (or `""`) and `data-stack-prefix="median"`. |
| `qbtn` (class) | `ui.css` | Quiet buttons (`qbtn`, `qbtn primary` for the one primary action a page may have). |
| `quiet-note` (class) | `ui.css` | A small grey line for notes (widened range, caps). Not a banner. |

## Generated words

Never write a summary by hand in a component: add a tested function to
`src/lib/summaries.ts` (table-driven tests in `summaries.test.ts`).

- `sessionSummaryLine(s, ctx)` the one line under a session row; `rowGlyph`, `needsALook`, `sessionMetaLine`.
- `sessionsSentence({ attention, time, filtered, rows, total })` the Sessions sentence as parts; clauses carry the filter (or the one session) they count.
- `sessionOutcome({ summary, tl, detections })` and `sessionMetaFigures(s)` for a session page.
- Helpers: `plural`, `countWord`, `joinClauses` (serial comma; the Sessions sentence lists without one), `durationWords` ("4 seconds"), `shortDuration` ("150 ms", "9 s"), `offsetText`, `relativeTime` ("50 min ago"), `scoreText` ("goal completion 0.60"), `scorerWords`.
- `src/lib/ruleWords.ts` turns an AgentShield rule into plain words (a noun, a purpose and a one-sentence reason), falling back to the rule's title.

## Rules of thumb (§7.2)

- One sentence answers the page's question; no rows of number tiles.
- Say a fact once: not in a banner, a badge and a field list. If the header
  sentence states a figure, the page does not repeat it in a table, a meta
  line and an inspector. The same figure agrees on every page that shows it.
- Status is a glyph plus words; no coloured pills or badges for status,
  agents or scores, and no coloured dots that tell runs or agents apart by
  colour alone. A score is text: `scoreText` ("goal completion 0.60").
- Versions: people see 1-based "version 3" from `versionLabel()` in
  `src/lib/format.ts` (API v2 → "version 3"). The exact ref (`name:v2`)
  appears only where code is shown, in an inspector row labelled "In code".
  Never "v0", "version 0" or ":v0" anywhere else.
- Times and durations: `fmtRelative` ("50 min ago", "2 h ago", "3 days ago")
  and `fmtDuration` ("150 ms", "1.3 s", "1 min 5 s") from `src/lib/format.ts`.
  No local formatters.
- Approvals and overrides: an approval request the user allowed is
  "approved" ("You approved 3 of them" in a session's own text, "Needed
  approval; approved" in its inspector, "Users approved 3 of those requests"
  on Security). "Override"/"overrode" is only for going past a block.
- Colour: values (numbers, booleans, strings, JSON) are near-black text,
  never syntax-coloured. "Differs" is marked by weight or a neutral tint,
  never amber. Red, amber and green only for failed, warning and success
  glyphs. Switches use the accent blue when on. Deletes are a trash icon.
- Sentence case for headings, labels and rule titles (`sentenceCase` in
  `ruleWords.ts`). A count is a grey number (`glist-count`), not "(8)". One
  primary (blue) button per page; secondary actions grey.
- Ids, raw JSON and token counts go in the inspector or the raw view.
- Plain words; internal terms (span, op, ref, digest) only in raw views, with a tooltip.
- Check 390 px: no horizontal page scroll; long unbroken names wrap (`overflow-wrap: anywhere`);
  long titles wrap to two lines rather than being cut; no table collapses to
  a letter per column (stack cells into rows, or show a list instead).
