import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { Empty, ErrorBox, Loading } from '../../components/common'
import { AliasEditor, ConfirmButton } from '../../components/objects/bits'
import { TagEditor } from '../../components/objects/TagEditor'
import { KIND_META, kindOf, objClass, useObjectVersions } from '../../components/objects/objectUtils'
import { useProjectId } from '../../hooks/useProject'
import { fmtBytes, fmtDate, fmtRelative } from '../../lib/format'
import { objectDiffUrl, objectUrl } from '../../lib/urls'

export function ObjectVersionsPage() {
  const projectId = useProjectId()
  const { objectId = '' } = useParams()
  const nav = useNavigate()
  const qc = useQueryClient()
  const versions = useObjectVersions(projectId, objectId)
  const [sel, setSel] = useState<string[]>([])

  const latest = versions.data?.[0]
  const kind = latest ? kindOf(latest) : 'all'
  const listUrl = `/${projectId}/${kind === 'all' ? 'objects' : kind}`

  const toggle = (d: string) => setSel((s) => (s.includes(d) ? s.filter((x) => x !== d) : [...s, d].slice(-2)))
  const compare = () => {
    if (!versions.data || sel.length !== 2) return
    const [a, b] = sel
      .map((d) => versions.data!.find((v) => v.digest === d)!)
      .sort((x, y) => x.version - y.version)
    nav(objectDiffUrl(projectId, objectId, a.digest, b.digest))
  }

  return (
    <div className="page">
      <div className="breadcrumbs">
        <Link to={listUrl}>{KIND_META[kind].title}</Link>
        <span>/</span>
        <span>{objectId}</span>
      </div>
      <div className="page-header">
        <h1 className="mono">{objectId}</h1>
        {latest && <span className="badge">{objClass(latest)}</span>}
        {versions.data && <span className="muted small">{versions.data.length} versions</span>}
        <span className="spacer" />
        <span className="hint">{sel.length < 2 ? 'Select two versions to compare' : ''}</span>
        <button className="btn" disabled={sel.length !== 2} onClick={compare}>
          Compare {sel.length === 2 ? '2 versions' : ''}
        </button>
        {latest && (
          <Link className="btn primary" to={objectUrl(projectId, objectId, 'latest')}>
            Open latest
          </Link>
        )}
        <ConfirmButton
          className="btn danger"
          title={`Delete ${objectId}?`}
          body={`All ${versions.data?.length ?? ''} versions will be deleted.`}
          onConfirm={async () => {
            await api.objectDelete(projectId, objectId)
            await qc.invalidateQueries({ queryKey: ['objects', projectId] })
            nav(listUrl)
          }}
        >
          Delete all
        </ConfirmButton>
      </div>
      <ErrorBox error={versions.error} />
      {versions.isLoading ? (
        <Loading />
      ) : !versions.data?.length ? (
        <div className="card">
          <Empty>Object “{objectId}” was not found in this project.</Empty>
        </div>
      ) : (
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th style={{ width: 32 }} />
                <th>Version</th>
                <th>Digest</th>
                <th>Created</th>
                <th>Aliases</th>
                <th>Tags</th>
                <th className="num">Size</th>
                <th style={{ width: 40 }} />
              </tr>
            </thead>
            <tbody>
              {versions.data.map((v) => (
                <tr key={v.digest} className={`clickable ${sel.includes(v.digest) ? 'selected' : ''}`} onClick={() => nav(objectUrl(projectId, objectId, v.digest))}>
                  <td onClick={(e) => e.stopPropagation()}>
                    <input type="checkbox" checked={sel.includes(v.digest)} onChange={() => toggle(v.digest)} aria-label={`Select v${v.version}`} />
                  </td>
                  <td>
                    <Link to={objectUrl(projectId, objectId, v.digest)} onClick={(e) => e.stopPropagation()} style={{ fontWeight: 500 }}>
                      v{v.version}
                    </Link>
                    {v.is_latest && <span className="badge accent" style={{ marginLeft: 6 }}>latest</span>}
                  </td>
                  <td className="mono muted" title={v.digest}>
                    {v.digest.slice(0, 12)}
                  </td>
                  <td title={fmtDate(v.created_at)}>{fmtRelative(v.created_at)}</td>
                  <td>
                    <AliasEditor projectId={projectId} objectId={objectId} digest={v.digest} aliases={v.aliases.filter((a) => a !== 'latest')} compact />
                  </td>
                  <td>
                    <TagEditor projectId={projectId} objectId={objectId} digest={v.digest} tags={v.tags ?? []} compact />
                  </td>
                  <td className="num muted">{fmtBytes(v.size_bytes)}</td>
                  <td onClick={(e) => e.stopPropagation()}>
                    <ConfirmButton
                      className="btn ghost sm icon danger"
                      title={`Delete ${objectId}:v${v.version}?`}
                      body="Only this version is deleted. Other versions are kept."
                      onConfirm={async () => {
                        await api.objectDelete(projectId, objectId, [v.digest])
                        await qc.invalidateQueries({ queryKey: ['obj-versions', projectId, objectId] })
                        await qc.invalidateQueries({ queryKey: ['objects', projectId] })
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
    </div>
  )
}
