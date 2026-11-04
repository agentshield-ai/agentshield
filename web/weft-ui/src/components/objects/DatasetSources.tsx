// Dataset lineage (/api/v1/dataset_sources/*): the calls, spans or
// conversations that rows of this dataset were built from.
import { Link } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { adminApi } from '../../api/admin'
import { ErrorBox, Section, Spinner } from '../common'
import { ConfirmButton } from './bits'
import { fmtRelative } from '../../lib/format'
import { spanUrl } from '../../lib/urls'

export function DatasetSources({ projectId, objectId }: { projectId: string; objectId: string }) {
  const qc = useQueryClient()
  const q = useQuery({ queryKey: ['dataset-sources', projectId, objectId], queryFn: () => adminApi.datasetSources(projectId, objectId).then((r) => r.items) })
  if (q.isSuccess && q.data.length === 0) return null
  const rows = new Set(q.data?.map((l) => l.row_digest)).size
  return (
    <Section title={q.data ? `Sources · ${q.data.length} link${q.data.length === 1 ? '' : 's'} on ${rows} row${rows === 1 ? '' : 's'}` : 'Sources'}>
      <ErrorBox error={q.error} />
      {q.isLoading ? (
        <Spinner />
      ) : (
        <div className="table-wrap" style={{ maxHeight: 280 }} data-testid="dataset-sources">
          <table className="table">
            <thead>
              <tr>
                <th>Row</th>
                <th>Source</th>
                <th>Kind</th>
                <th>Started</th>
                <th>Linked</th>
                <th style={{ width: 40 }} />
              </tr>
            </thead>
            <tbody>
              {q.data?.map((l) => (
                <tr key={l.id}>
                  <td className="mono small" title={l.row_digest}>
                    {l.row_digest.slice(0, 10)}
                  </td>
                  <td>{l.source_kind === 'call' || l.source_kind === 'span' ? <Link to={spanUrl(projectId, l.source_id)}>{l.source_display_name}</Link> : <span className="mono small">{l.source_display_name}</span>}</td>
                  <td>
                    <span className="badge">{l.source_kind}</span>
                  </td>
                  <td className="muted small">{fmtRelative(l.source_started_at)}</td>
                  <td className="muted small">
                    {fmtRelative(l.created_at)}
                    {l.added_by && ` by ${l.added_by}`}
                  </td>
                  <td>
                    <ConfirmButton
                      className="btn ghost sm icon danger"
                      title="Remove this link?"
                      body="The row and the source are kept; only the lineage link is removed."
                      confirmLabel="Remove"
                      onConfirm={async () => {
                        await adminApi.datasetSourcesDelete(projectId, [l.id])
                        await qc.invalidateQueries({ queryKey: ['dataset-sources', projectId, objectId] })
                      }}
                    >
                      ✕
                    </ConfirmButton>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Section>
  )
}
