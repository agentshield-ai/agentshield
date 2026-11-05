// Dataset lineage (/api/v1/dataset_sources/*): the steps or conversations
// that rows of this dataset were built from. A quiet table; removing a link
// can be undone for a few seconds (DESIGN.md §7.1 rule 11).
import { Link } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { adminApi } from '../../api/admin'
import { ErrorBox, Spinner } from '../common'
import { PlainTable } from '../ui/PlainTable'
import { useUndoDelete } from '../settings/SettingsList'
import { fmtDate } from '../../lib/format'
import { plural, relativeTime } from '../../lib/summaries'
import { spanUrl } from '../../lib/urls'

export function DatasetSources({ projectId, objectId }: { projectId: string; objectId: string }) {
  const qc = useQueryClient()
  const q = useQuery({ queryKey: ['dataset-sources', projectId, objectId], queryFn: () => adminApi.datasetSources(projectId, objectId).then((r) => r.items) })
  const { hidden, remove } = useUndoDelete()
  if (q.isSuccess && q.data.length === 0) return null
  const links = (q.data ?? []).filter((l) => !hidden.has(l.id))
  const rows = new Set(links.map((l) => l.row_digest)).size
  return (
    <section className="asset-section">
      <div className="asset-section-head">
        <h2>
          Where the rows came from
          {q.data && <span className="quiet-note" style={{ display: 'inline', marginLeft: 8 }}>{`${plural(links.length, 'source')} for ${plural(rows, 'row')}`}</span>}
        </h2>
      </div>
      <ErrorBox error={q.error} />
      {q.isLoading ? (
        <Spinner />
      ) : (
        <div data-testid="dataset-sources">
          <PlainTable caption="Sources of this dataset's rows">
            <thead>
              <tr>
                <th scope="col">Source</th>
                <th scope="col">Kind</th>
                <th scope="col">Row</th>
                <th scope="col">Linked</th>
                <th scope="col">
                  <span className="sr-only">Remove</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {links.map((l) => (
                <tr key={l.id}>
                  <td>{l.source_kind === 'span' ? <Link to={spanUrl(projectId, l.source_id)}>{l.source_display_name}</Link> : l.source_display_name}</td>
                  <td>{l.source_kind === 'span' ? 'step' : l.source_kind}</td>
                  <td className="mono small" title={`Row digest ${l.row_digest}`}>
                    {l.row_digest.slice(0, 10)}
                  </td>
                  <td title={fmtDate(l.created_at)} style={{ whiteSpace: 'nowrap' }}>
                    {relativeTime(l.created_at)}
                    {l.added_by && ` by ${l.added_by}`}
                  </td>
                  <td>
                    <button
                      type="button"
                      className="qbtn sm plain danger"
                      aria-label={`Remove the link to ${l.source_display_name}`}
                      title="Remove this link (the row and the source are kept)"
                      onClick={() =>
                        remove(l.id, 'Removed a source link', async () => {
                          await adminApi.datasetSourcesDelete(projectId, [l.id])
                          await qc.invalidateQueries({ queryKey: ['dataset-sources', projectId, objectId] })
                        })
                      }
                    >
                      Remove
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </PlainTable>
        </div>
      )}
    </section>
  )
}
