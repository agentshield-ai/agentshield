import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { parseRef } from '../../api/refs'
import type { Any, Call } from '../../api/types'
import { refUrl } from '../../lib/urls'
import { CodeBlock } from '../objects/bits'
import { ErrorBox, Spinner } from '../common'

/** Source of the op a call ran: val.code, or files['obj.py'] for ops published by the official weave client. */
export function useOpSource(opName: string) {
  const ref = parseRef(opName)
  const isOp = ref?.kind === 'op' && !!ref.digest
  const obj = useQuery({
    queryKey: ['obj', ref?.projectId, ref?.name, ref?.digest],
    queryFn: () => api.objRead(ref!.projectId, ref!.name, ref!.digest!).then((r) => r.obj),
    enabled: isOp,
    staleTime: Infinity,
  })
  const v: Any = obj.data?.val ?? {}
  const fileDigest: string | undefined = !v.code && v.files && typeof v.files === 'object' ? (v.files['obj.py'] ?? Object.values(v.files)[0]) : undefined
  const file = useQuery({
    queryKey: ['op-file', ref?.projectId, fileDigest],
    queryFn: () => api.fileContentText(ref!.projectId, fileDigest!),
    enabled: !!fileDigest && !!ref,
    staleTime: Infinity,
  })
  return {
    isOp,
    loading: obj.isLoading || (!!fileDigest && file.isLoading),
    error: obj.error ?? file.error,
    code: (v.code as string | undefined) ?? file.data,
    language: (v.language as string | undefined) ?? 'python',
    name: (v.name as string | undefined) ?? ref?.name,
    signature: v.signature as string | undefined,
  }
}

export function OpCodeView({ call }: { call: Call }) {
  const src = useOpSource(call.op_name)
  if (!src.isOp)
    return (
      <div className="empty small">
        This call has no published op, so there is no source to show. Calls from OpenTelemetry spans and the playground are recorded by name only.
      </div>
    )
  if (src.loading) return <Spinner label="Loading source…" />
  const link = refUrl(call.op_name)
  return (
    <div className="col" style={{ gap: 10 }}>
      <ErrorBox error={src.error} />
      {src.code ? (
        <CodeBlock
          code={src.code}
          language={src.language}
          title={
            <span className="mono">
              {src.name}
              {src.signature ?? ''}
            </span>
          }
          maxHeight={640}
        />
      ) : (
        <div className="empty small">No source code was captured for this op version.</div>
      )}
      {link && (
        <div className="small">
          <Link to={link}>Open op version →</Link>
        </div>
      )}
    </div>
  )
}
