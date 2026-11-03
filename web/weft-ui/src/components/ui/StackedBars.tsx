// A small stacked bar chart drawn with CSS, with a visually hidden table of
// the same numbers for screen readers.
export interface BarSeries {
  id: string;
  label: string;
  cls: string;
}

export interface BarBucket {
  label: string;
  title: string;
  values: Record<string, number>;
}

export function StackedBars({
  buckets,
  series,
  caption,
  height = 140,
}: {
  buckets: BarBucket[];
  series: BarSeries[];
  caption: string;
  height?: number;
}) {
  const max = Math.max(
    1,
    ...buckets.map((b) =>
      series.reduce((a, s) => a + (b.values[s.id] ?? 0), 0),
    ),
  );
  const labelEvery = Math.max(1, Math.ceil(buckets.length / 8));
  return (
    <figure className="sbars">
      <div className="sbars-plot" style={{ height }} aria-hidden>
        {buckets.map((b, i) => {
          const total = series.reduce((a, s) => a + (b.values[s.id] ?? 0), 0);
          return (
            <div
              key={b.title}
              className="sbars-col"
              title={`${b.title}: ${series.map((s) => `${s.label} ${b.values[s.id] ?? 0}`).join(", ")}`}
            >
              <div
                className="sbars-stack"
                style={{ height: `${(total / max) * 100}%` }}
              >
                {series.map((s) =>
                  b.values[s.id] ? (
                    <div
                      key={s.id}
                      className={`sbars-seg ${s.cls}`}
                      style={{ flexGrow: b.values[s.id] }}
                    />
                  ) : null,
                )}
              </div>
              <div className="sbars-x">
                {i % labelEvery === 0 ? b.label : ""}
              </div>
            </div>
          );
        })}
      </div>
      <figcaption className="sbars-legend">
        {series.map((s) => (
          <span key={s.id}>
            <span className={`sw ${s.cls}`} aria-hidden /> {s.label}
          </span>
        ))}
      </figcaption>
      {/* The wrapper clips: a table never shrinks below its content width, so
          the sr-only class on the table itself still widened the page. */}
      <div className="sr-only">
        <table>
          <caption>{caption}</caption>
          <thead>
            <tr>
              <th scope="col">Time</th>
              {series.map((s) => (
                <th key={s.id} scope="col">
                  {s.label}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {buckets
              .filter((b) => series.some((s) => b.values[s.id]))
              .map((b) => (
                <tr key={b.title}>
                  <th scope="row">{b.title}</th>
                  {series.map((s) => (
                    <td key={s.id}>{b.values[s.id] ?? 0}</td>
                  ))}
                </tr>
              ))}
          </tbody>
        </table>
      </div>
    </figure>
  );
}

/** Hour or day buckets between `from` and `to` (both ms), with counts by `keyOf`. */
export function bucketize<T>(
  items: T[],
  timeOf: (x: T) => number,
  keyOf: (x: T) => string,
  from: number,
  to: number,
): BarBucket[] {
  const span = Math.max(1, to - from);
  const hourly = span <= 2 * 86400e3;
  const step = hourly ? 3600e3 : 86400e3;
  const start = hourly ? Math.floor(from / step) * step : startOfDay(from);
  const n = Math.min(400, Math.ceil((to - start) / step));
  const buckets: BarBucket[] = Array.from(
    { length: Math.max(1, n) },
    (_, i) => {
      const t = new Date(start + i * step);
      return {
        label: hourly
          ? `${String(t.getHours()).padStart(2, "0")}:00`
          : t.toLocaleDateString(undefined, { month: "short", day: "numeric" }),
        title: hourly
          ? t.toLocaleString(undefined, {
              month: "short",
              day: "numeric",
              hour: "2-digit",
              minute: "2-digit",
            })
          : t.toLocaleDateString(),
        values: {},
      };
    },
  );
  for (const x of items) {
    const i = Math.floor((timeOf(x) - start) / step);
    if (i < 0 || i >= buckets.length) continue;
    const k = keyOf(x);
    buckets[i].values[k] = (buckets[i].values[k] ?? 0) + 1;
  }
  return buckets;
}

function startOfDay(ms: number) {
  const d = new Date(ms);
  d.setHours(0, 0, 0, 0);
  return d.getTime();
}
