# Traces

**Traces** is the raw view: every span your agents and functions recorded,
with their exact inputs, outputs and attributes. For "what happened in this
conversation", the [Sessions](sessions.md) page is easier; use Traces when you
need the details.

## Find a trace

- The list shows one row per trace (its top-level span), newest first, from
  the last 24 hours. When that is empty it widens to the last 7 or 30 days,
  and says so.
- Untick **Roots only** to list every span instead of every trace.
- The filter bar works as on every other page. When listing traces,
  `agent:`, `model:` and `tool:` keep traces where any span matches; when
  listing spans, they apply to each span. Session filters such as `team:` or
  `has:block` keep the traces of matching sessions. See
  [Filters and saved views](filters.md).
- Plain text in the filter box finds a trace when any of its spans contains
  it: names, inputs, outputs, messages, reasoning, tool calls, error
  messages and hand-offs, not only the top-level span.
- **Status** means the same as on Sessions, read off each trace's
  top-level span (agent span or not): **Failed** keeps traces whose
  top-level span ended with an error; **Went on after a failed step** keeps
  traces whose top-level span ended without one but with a failed span
  under it, and sub-agent traces linked to their caller (span link, parent
  call link or hand-off) that failed; **Succeeded** keeps traces where
  nothing failed. AgentShield's evaluate traces are verdict records, not
  agent runs: each is listed under its session's status, **No agent data**
  for a session known only from verdicts. So the same chip keeps the same
  runs on both pages. The status dot on a row is the top-level span's own
  status. Listing spans, the chip applies to each span (a span that failed,
  or one with a failed span under it). See [Status](filters.md#status).
- **Add filter → Advanced: field condition** adds a condition on any field,
  such as an input value or a token count. It shows as a **Field** chip with
  the others.
- AgentShield's evaluations of replayed tool calls (the engine's own traces
  of a [replay](security.md#replay-recorded-tool-calls-after-a-rule-change))
  are hidden, as on Sessions. Tick **Replay evaluations** to list them.

Press `j` and `k` to move through the rows and `Enter` to open one.

## Columns and views

Use **Columns** to show input and output fields, reorder columns and pin
them. Save the whole setup (filters, columns and sort) under **Views** in the
filter bar, so you and your team can come back to it.

## Do more with spans

Select rows to compare them, add them to a dataset or a review queue, export
them, or delete them. **Show charts** draws counts, latency, tokens and cost
over time for the current filters; **+ Add chart to dashboard** keeps a chart.
