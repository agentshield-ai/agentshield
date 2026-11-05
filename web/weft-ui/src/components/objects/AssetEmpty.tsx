// The guided empty state of an asset list (DESIGN.md §7.1 rule 7): what would
// appear here, and the shortest way to make one (the page's create button,
// or a copyable SDK snippet for this server and project).
import { useQuery } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { api } from '../../api/client'
import { CopyButton } from '../common'
import { serverOrigin } from '../ui/EmptyState'
import type { ObjectPageKind } from './objectUtils'
import '../settings/settings.css'

const WHAT: Record<ObjectPageKind, { title: string; text: string }> = {
  datasets: { title: 'No datasets yet', text: 'A dataset is a versioned set of examples, such as questions with the answers you expect. Evaluations run a model over every row and score the results.' },
  models: { title: 'No models yet', text: 'A model here is a saved configuration: which LLM to call and with what prompt and settings. The server can run LLM completion models in evaluations and the playground.' },
  prompts: { title: 'No prompts yet', text: 'A prompt is a versioned template, a text or a list of chat messages, with {placeholders} filled from each dataset row.' },
  scorers: { title: 'No scorers yet', text: 'A scorer grades an output: an LLM judge with a scoring prompt, or a rule such as an exact match or a pattern. Monitors and evaluations use them.' },
  ops: { title: 'No traced functions yet', text: 'A traced function records its source code and every run. Decorate a function with @weft.trace and call it; each new version of the code is kept.' },
  all: { title: 'Nothing saved yet', text: 'Everything you save from the SDK or this UI appears here: datasets, models, prompts, scorers, evaluations, monitors, dashboards and more, each with its versions.' },
}

function snippet(kind: ObjectPageKind, projectId: string, origin: string): string {
  const head = `import weft\n\nweft.init("${projectId}", server_url="${origin}")\n\n`
  switch (kind) {
    case 'prompts':
      return head + `weft.save(weft.StringPrompt("Answer briefly: {question}"), "answer-briefly")`
    case 'models':
      return (
        head +
        `weft.save(weft.LLMStructuredCompletionModel(\n    name="qa-model", llm_model_id="gpt-4o-mini",\n    default_params={"messages_template": [{"role": "user", "content": "{question}"}]},\n))`
      )
    case 'scorers':
      return head + `class ExactMatch(weft.Scorer):\n    @weft.trace\n    def score(self, *, output, expected):\n        return output == expected\n\nweft.save(ExactMatch(), "exact-match")`
    case 'ops':
      return head + `@weft.trace\ndef answer(question: str) -> str:\n    return "42"\n\nanswer("What is six times seven?")`
    default:
      return head + `weft.save(weft.Dataset(name="qa", rows=[\n    {"question": "2+2?", "expected": "4"},\n    {"question": "Capital of Norway?", "expected": "Oslo"},\n]))`
  }
}

export function AssetEmpty({ kind, projectId, action }: { kind: ObjectPageKind; projectId: string; action?: ReactNode }) {
  useQuery({ queryKey: ['server-info'], queryFn: api.server, staleTime: 60_000 })
  const code = snippet(kind, projectId, serverOrigin())
  const w = WHAT[kind]
  return (
    <section className="asset-empty" aria-label="Getting started">
      <h2>{w.title}</h2>
      <p>{w.text}</p>
      {action}
      <div className="s-code-box">
        <div className="s-code-head">
          <span>{action ? 'Or save one from Python' : 'Save one from Python'}</span>
          <CopyButton text={code} label="Copy Python snippet" />
        </div>
        <pre className="s-code" tabIndex={0} role="region" aria-label="Python snippet">
          {code}
        </pre>
      </div>
    </section>
  )
}
