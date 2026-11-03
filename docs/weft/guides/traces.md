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
- **+ Filter** adds conditions on any field, such as an input value or a
  token count.

Press `j` and `k` to move through the rows and `Enter` to open one.

## Columns and views

Use **Columns** to show input and output fields, reorder columns and pin
them. Save the whole setup (filters, columns and sort) under **Views** in the
filter bar, so you and your team can come back to it.

## Do more with spans

Select rows to compare them, add them to a dataset or a review queue, export
them, or delete them. **Show charts** draws counts, latency, tokens and cost
over time for the current filters; **+ Add chart to dashboard** keeps a chart.
