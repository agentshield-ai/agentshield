// The setup part of the Evaluations empty state: how to run an evaluation
// from Python against this server and project, as a copyable snippet in the
// guided empty state's look (components/ui/EmptyState.tsx).
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { CopyButton } from '../common'
import { serverOrigin } from '../ui/EmptyState'

export function evalSnippet(origin: string, projectId: string): string {
  return `import asyncio, weft

weft.init("${projectId}", server_url="${origin}")

dataset = weft.Dataset(name="qa", rows=[
    {"question": "2+2?", "expected": "4"},
    {"question": "Capital of Norway?", "expected": "Oslo"},
])

@weft.trace
def answer(question: str) -> str:
    return "4"

class ExactMatch(weft.Scorer):
    @weft.trace
    def score(self, *, output, expected):
        return output == expected

evaluation = weft.Evaluation(dataset=dataset, scorers=[ExactMatch()])
asyncio.run(evaluation.evaluate(answer))`
}

export function EvalSnippet({ projectId }: { projectId: string }) {
  useQuery({ queryKey: ['server-info'], queryFn: api.server, staleTime: 60_000 })
  const code = evalSnippet(serverOrigin(), projectId)
  return (
    <div className="snippets">
      <div className="guided-or">
        <span>or run one from Python</span>
      </div>
      <div className="snippet">
        <div className="snippet-copy">
          <CopyButton text={code} label="Copy Python snippet" />
        </div>
        <pre className="code-block" data-lang="python" tabIndex={0} aria-label="Python snippet">
          <code>{code}</code>
        </pre>
      </div>
    </div>
  )
}
