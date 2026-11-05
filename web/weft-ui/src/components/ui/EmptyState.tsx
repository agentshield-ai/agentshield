// Guided empty states (DESIGN.md §7.1 rule 7): say what would appear here and
// give the shortest way to produce it, with snippets that use this server's
// URL and the current project, plus a one-click test session.
import { useState, type ReactNode } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { v1 } from '../../api/v1'
import { CopyButton } from '../common'
import '../common.css'
import { Icon } from './Icons'
import { helpUrl } from './PageHeader'
import { notify } from './Toasts'

export function serverOrigin(): string {
  const env = import.meta.env.VITE_WEFT_API as string | undefined
  return env || (typeof window !== 'undefined' ? window.location.origin : 'http://127.0.0.1:6345')
}

export type SnippetId = 'python' | 'typescript' | 'otel' | 'claude-code' | 'codex' | 'openclaw' | 'pi' | 'agentshield'

export const SNIPPET_LABEL: Record<SnippetId, string> = {
  python: 'Python',
  typescript: 'TypeScript',
  otel: 'OpenTelemetry',
  'claude-code': 'Claude Code',
  codex: 'Codex CLI',
  openclaw: 'OpenClaw',
  pi: 'Pi',
  agentshield: 'AgentShield engine',
}

/** Copyable setup snippets for the current server and project (unit tested). */
export function snippets(origin: string, projectId: string, auth: boolean): Record<SnippetId, { lang: string; code: string; note: string }> {
  const key = auth ? '\nexport WEFT_API_KEY=<your API key>' : ''
  return {
    python: {
      lang: 'python',
      note: 'Record a session with two agents and a tool call. Install the SDK from sdk/python in the Weft repository.',
      code: `import weft

weft.init("${projectId}", server_url="${origin}")${auth ? '  # reads WEFT_API_KEY' : ''}

with weft.start_conversation(agent_name="orchestrator") as conv:
    with conv.start_turn(user_message="Find three facts about Oslo") as turn:
        with turn.start_llm(model="gpt-4o-mini", provider_name="openai") as llm:
            llm.output("I'll ask the researcher.")
        with turn.start_subagent(name="researcher") as sub:
            with sub.start_tool(name="web_search", arguments={"q": "Oslo"}) as tool:
                tool.result = "Capital of Norway; on the Oslofjord"
        turn.output_messages = [weft.Message.assistant("Oslo is Norway's capital.")]`,
    },
    typescript: {
      lang: 'ts',
      note: 'The same with the TypeScript SDK (sdk/typescript, package weft-sdk). Node 18+.',
      code: `import * as weft from 'weft-sdk';

await weft.init('${projectId}', { serverUrl: '${origin}'${auth ? ', apiKey: process.env.WEFT_API_KEY' : ''} });

const conv = weft.startConversation({ agentName: 'orchestrator' });
await conv.startTurn({ userMessage: 'Find three facts about Oslo' }).run(async (turn) => {
  await turn.startSubagent({ name: 'researcher' }).run(async (sub) => {
    await sub.startTool({ name: 'web_search', arguments: { q: 'Oslo' } }).run((t) => { t.result = 'Capital of Norway'; });
  });
  turn.outputMessages = [weft.Message.assistant("Oslo is Norway's capital.")];
});
conv.end();
await weft.finish();`,
    },
    otel: {
      lang: 'bash',
      note: 'Any OpenTelemetry exporter works: spans that follow the GenAI conventions become sessions, agents, tool calls and reasoning. The two attributes to set are in the comments below.',
      code: `export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="${origin}/otel/v1/traces"
export OTEL_EXPORTER_OTLP_TRACES_HEADERS="project_id=${projectId}${auth ? ',Authorization=Bearer%20<your API key>' : ''}"
export OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/protobuf

# Put the session id on every span:  gen_ai.conversation.id = "<session id>"
# and the agent on agent spans:        gen_ai.agent.name = "researcher"`,
    },
    'claude-code': {
      lang: 'bash',
      note: 'Traces every Claude Code session, its sub-agents and tool calls. Run from the Weft repository.',
      code: `node plugins/weft-claude-code/install.mjs --project ${projectId} \\
  --server-url ${origin}${auth ? ' --api-key "$WEFT_API_KEY"' : ''}`,
    },
    codex: {
      lang: 'bash',
      note: 'Adds a Stop hook to Codex that sends each turn to Weft.',
      code: `node plugins/weft-codex/install.mjs --project ${projectId} --server-url ${origin}${auth ? ' --api-key "$WEFT_API_KEY"' : ''}`,
    },
    openclaw: {
      lang: 'bash',
      note: 'Install the gateway plugin, then enable it in ~/.openclaw/openclaw.json with this project and server.',
      code: `openclaw plugins install ./plugins/weft-openclaw
# ~/.openclaw/openclaw.json:
# plugins: { allow: ["weft"], entries: { weft: { enabled: true,
#   config: { project: "${projectId}", serverUrl: "${origin}" },
#   hooks: { allowConversationAccess: true } } } }`,
    },
    pi: {
      lang: 'bash',
      note: 'A Pi extension that records each prompt, model call and tool execution.',
      code: `pi install ./plugins/weft-pi
export WEFT_PROJECT=${projectId} WEFT_SERVER_URL=${origin}${key}
pi`,
    },
    agentshield: {
      lang: 'yaml',
      note: "Add this to AgentShield's config.yaml so the engine sends every non-allow verdict to this project. Detections attach to the matching tool call when the plugins pass the same session and tool-call ids.",
      code: `weft:
  endpoint: ${origin}
  project: ${projectId}
  api_key_env: WEFT_API_KEY      # a key with the member role${auth ? '' : ' (not needed: this server has no auth)'}
  send: non_allow                # or: all`,
    },
  }
}

export function Snippets({ projectId, only, initial }: { projectId: string; only?: SnippetId[]; initial?: SnippetId }) {
  const info = useQuery({ queryKey: ['server-info'], queryFn: api.server, staleTime: 60_000 })
  const all = snippets(serverOrigin(), projectId, !!info.data?.auth_required)
  const ids = only ?? (['python', 'typescript', 'otel', 'claude-code', 'codex', 'openclaw', 'pi'] as SnippetId[])
  const [tab, setTab] = useState<SnippetId>(initial ?? ids[0])
  const s = all[tab]
  return (
    <div className="snippets">
      <div className="snippet-tabs" role="tablist" aria-label="How to send data">
        {ids.map((id) => (
          <button key={id} type="button" role="tab" aria-selected={id === tab} className={`seg-btn ${id === tab ? 'on' : ''}`} onClick={() => setTab(id)}>
            {SNIPPET_LABEL[id]}
          </button>
        ))}
      </div>
      <p className="muted small">{s.note}</p>
      <div className="snippet">
        <div className="snippet-copy">
          <CopyButton text={s.code} label={`Copy ${SNIPPET_LABEL[tab]} snippet`} />
        </div>
        <pre className="code-block" data-lang={s.lang} tabIndex={0} aria-label={`${SNIPPET_LABEL[tab]} snippet`}>
          <code>{s.code}</code>
        </pre>
      </div>
    </div>
  )
}

export function SendTestSession({ projectId, label = 'Send a test session', className = 'qbtn primary' }: { projectId: string; label?: string; className?: string }) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const send = useMutation({
    mutationFn: () => v1.demoSession(projectId),
    onSuccess: async (r) => {
      await qc.invalidateQueries()
      notify('Sent a two-agent test session with reasoning, tool calls, a hand-off and a blocked command.', 'ok', 6000)
      navigate(`/${projectId}/sessions/${encodeURIComponent(r.session_id)}`)
    },
    onError: (e) => notify(`Could not send a test session: ${e instanceof Error ? e.message : String(e)}`, 'error', 8000),
  })
  return (
    <button type="button" className={className} onClick={() => send.mutate()} disabled={send.isPending}>
      <Icon name="spark" size={14} /> {send.isPending ? 'Sending…' : label}
    </button>
  )
}

export function GuidedEmpty({
  projectId,
  title,
  children,
  doc = 'setup.md#send-your-agents-data',
  only,
  initial,
  showTest = true,
  setup,
}: {
  projectId: string
  title: ReactNode
  children?: ReactNode
  doc?: string
  only?: SnippetId[]
  initial?: SnippetId
  showTest?: boolean
  /** Shown in place of the agent setup snippets, for pages whose data does not come from agents (evaluations). */
  setup?: ReactNode
}) {
  return (
    <section className="guided-empty" aria-label="Getting started">
      <div className="guided-head">
        <div className="guided-icon" aria-hidden>
          <Icon name="spark" size={20} />
        </div>
        <div className="grow">
          <h2>{title}</h2>
          {children && <div className="muted">{children}</div>}
        </div>
      </div>
      {showTest && (
        <div className="guided-test">
          <SendTestSession projectId={projectId} />
          <span className="muted small">Writes a realistic two-agent session into {projectId} so you can explore every view in a few seconds.</span>
        </div>
      )}
      {setup ?? (
        <>
          <div className="guided-or">
            <span>or connect your agents</span>
          </div>
          <Snippets projectId={projectId} only={only} initial={initial} />
        </>
      )}
      <Link className="help-link" to={helpUrl(doc)}>
        Full setup guide
      </Link>
    </section>
  )
}
