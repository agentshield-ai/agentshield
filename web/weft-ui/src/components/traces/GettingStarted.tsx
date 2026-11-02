import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { CopyButton, Tabs } from '../common'

type Tab = 'python' | 'otel' | 'curl'

export function serverOrigin(): string {
  const env = import.meta.env.VITE_WEFT_API as string | undefined
  return env || window.location.origin
}

export function CodeSnippet({ code, lang }: { code: string; lang?: string }) {
  return (
    <div className="snippet">
      <div className="snippet-copy">
        <CopyButton text={code} />
      </div>
      <pre className="code-block" data-lang={lang}>
        <code>{code}</code>
      </pre>
    </div>
  )
}

/** How to send data to this server: Python SDK, OTLP exporter, raw HTTP. */
export function GettingStarted({ projectId, compact }: { projectId: string; compact?: boolean }) {
  const [tab, setTab] = useState<Tab>('python')
  const info = useQuery({ queryKey: ['server-info'], queryFn: api.serverInfo, staleTime: 60_000 })
  const origin = serverOrigin()
  const otel = `${origin}${info.data?.otel_endpoint ?? '/otel/v1/traces'}`
  const auth = info.data?.auth_required
  const python = `# pip install ./sdk/python   (from the Weft repository)
import weft

weft.init("${projectId}", server_url="${origin}")${auth ? '  # API key from WEFT_API_KEY' : ''}

@weft.op
def answer(question: str) -> str:
    # OpenAI / Anthropic calls made here are traced too
    return "42"

answer("What is the meaning of life?")`
  const otelCode = `# Any OpenTelemetry SDK or collector can export here (OTLP/HTTP, protobuf or JSON)
export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="${otel}"
export OTEL_EXPORTER_OTLP_TRACES_HEADERS="project_id=${projectId}${auth ? ',Authorization=Bearer%20<api key>' : ''}"

# gen_ai.* (OpenTelemetry GenAI), OpenInference and OpenLLMetry
# attributes are mapped to inputs, outputs and token usage.`
  const curl = `curl -X POST ${origin}/call/start \\
  -H 'Content-Type: application/json'${auth ? " \\\n  -H 'Authorization: Bearer <api key>'" : ''} \\
  -d '{"start": {"project_id": "${projectId}", "op_name": "hello",
       "started_at": "'$(date -u +%Y-%m-%dT%H:%M:%SZ)'", "inputs": {"name": "world"}}}'
# => {"id": "...", "trace_id": "..."}  then POST /call/end with that id`
  return (
    <div className="col" style={{ gap: 10 }}>
      {!compact && (
        <p className="muted small" style={{ margin: 0 }}>
          Send traces with the Python SDK, or point any OpenTelemetry exporter at this server. Projects are created on first write.
        </p>
      )}
      <Tabs<Tab>
        tabs={[
          { id: 'python', label: 'Python SDK' },
          { id: 'otel', label: 'OpenTelemetry' },
          { id: 'curl', label: 'HTTP' },
        ]}
        value={tab}
        onChange={setTab}
      />
      {tab === 'python' && <CodeSnippet code={python} lang="python" />}
      {tab === 'otel' && <CodeSnippet code={otelCode} lang="bash" />}
      {tab === 'curl' && <CodeSnippet code={curl} lang="bash" />}
    </div>
  )
}
