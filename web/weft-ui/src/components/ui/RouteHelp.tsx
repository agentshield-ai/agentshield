// "What is this page?" for the pages that predate the agent-first views
// (DESIGN.md §7.1 rule 12). The agent-first pages render their own.
import { useLocation, useParams } from 'react-router-dom'
import { usePageHelp } from './PageHelp'

const HELP: Record<string, { title: string; body: string; doc: string }> = {
  traces: {
    title: 'Traces',
    body: 'The raw span explorer: every span every agent and function recorded, newest first. Use it when you need exact inputs, outputs and attributes; for “what happened in this conversation”, Sessions is easier.',
    doc: 'traces.md',
  },
  dashboards: {
    title: 'Dashboards',
    body: 'Charts you build from spans and scores (counts, latency, cost, any attribute over time), saved and shared with the project.',
    doc: 'project.md#dashboards',
  },
  monitors: {
    title: 'Monitors',
    body: 'Scorers that run automatically on new spans or sessions as they arrive, so quality and safety are measured continuously.',
    doc: 'evaluations.md#monitors',
  },
  evaluations: {
    title: 'Evaluations',
    body: 'Runs of a model or agent over a dataset, scored by scorers. Compare runs to see which version does better and where.',
    doc: 'evaluations.md#evaluations',
  },
  leaderboards: { title: 'Leaderboards', body: 'Tables that rank models or agents by their latest evaluation scores.', doc: 'evaluations.md#leaderboards' },
  'review-queues': {
    title: 'Review queues',
    body: 'Lists of spans for people to review one by one, with the rating and note fields you choose. Add spans from Traces or a span’s page.',
    doc: 'evaluations.md#review-queues',
  },
  playground: {
    title: 'Playground',
    body: 'Try prompts against any configured model, side by side, and save what works as a prompt or a model.',
    doc: 'evaluations.md#playground',
  },
  datasets: { title: 'Datasets', body: 'Versioned tables of examples, used by evaluations. Create them from files or from recorded spans.', doc: 'evaluations.md#datasets-models-prompts-and-scorers' },
  models: { title: 'Models', body: 'Versioned model and agent configurations (prompt, model, parameters) that evaluations can run.', doc: 'evaluations.md#datasets-models-prompts-and-scorers' },
  prompts: { title: 'Prompts', body: 'Versioned prompts. Each change is kept, so you can compare versions and roll back.', doc: 'evaluations.md#datasets-models-prompts-and-scorers' },
  scorers: { title: 'Scorers', body: 'Saved scorers (LLM judges, rules, code) that evaluations and monitors use to score outputs.', doc: 'evaluations.md#datasets-models-prompts-and-scorers' },
  functions: { title: 'Functions', body: 'The traced functions (ops) of your code, versioned by their source. Each run of a function is a span in Traces.', doc: 'evaluations.md#datasets-models-prompts-and-scorers' },
  objects: { title: 'All objects', body: 'Everything versioned in this project: datasets, models, prompts, scorers, functions and more.', doc: 'evaluations.md#datasets-models-prompts-and-scorers' },
  automations: {
    title: 'Automations',
    body: 'Rules that act when something happens, for example posting to a webhook when a score drops or a new alert arrives.',
    doc: 'project.md#automations',
  },
  settings: { title: 'Settings', body: 'Project settings: members and roles, API keys, provider secrets, model prices, data retention and redaction.', doc: 'project.md#settings' },
}

export function RouteHelp() {
  const { entity, project } = useParams()
  const loc = useLocation()
  const rest = loc.pathname.slice(`/${entity}/${project}/`.length).replace(/\/$/, '')
  const h = HELP[rest]
  if (!h) return null
  return <RouteHelpPanel key={rest} id={rest} {...h} />
}

function RouteHelpPanel({ id, title, body, doc }: { id: string; title: string; body: string; doc: string }) {
  const help = usePageHelp(`route-${id}`, title, body, doc)
  return help.panel ? <div className="route-help">{help.panel}</div> : <div className="route-help collapsed">{help.button}</div>
}
