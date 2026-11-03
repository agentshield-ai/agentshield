// "What is this page?" for the pages that predate the agent-first views
// (DESIGN.md §7.1 rule 12). The agent-first pages render their own.
import { useLocation, useParams } from 'react-router-dom'
import { usePageHelp } from './PageHelp'

const HELP: Record<string, { title: string; body: string; doc: string }> = {
  traces: {
    title: 'Traces',
    body: 'The raw span explorer: every span every agent and function recorded, newest first. Use it when you need exact inputs, outputs and attributes; for “what happened in this conversation”, Sessions is easier.',
    doc: 'README.md#send-data',
  },
  dashboards: {
    title: 'Dashboards',
    body: 'Charts you build from spans and scores (counts, latency, cost, any attribute over time), saved and shared with the project.',
    doc: 'API.md#custom-charts-dashboards-and-export',
  },
  monitors: {
    title: 'Monitors',
    body: 'Scorers that run automatically on new spans or sessions as they arrive, so quality and safety are measured continuously.',
    doc: 'API.md#monitors',
  },
  evaluations: {
    title: 'Evaluations',
    body: 'Runs of a model or agent over a dataset, scored by scorers. Compare runs to see which version does better and where.',
    doc: 'API.md#evaluations',
  },
  leaderboards: { title: 'Leaderboards', body: 'Tables that rank models or agents by their latest evaluation scores.', doc: 'API.md#evaluations' },
  'review-queues': {
    title: 'Review queues',
    body: 'Lists of spans for people to review one by one, with the rating and note fields you choose. Add spans from Traces or a span’s page.',
    doc: 'API.md#annotation-queues',
  },
  playground: {
    title: 'Playground',
    body: 'Try prompts against any configured model, side by side, and save what works as a prompt or a model.',
    doc: 'API.md#llms-and-playground',
  },
  datasets: { title: 'Datasets', body: 'Versioned tables of examples, used by evaluations. Create them from files or from recorded spans.', doc: 'API.md#objects' },
  models: { title: 'Models', body: 'Versioned model and agent configurations (prompt, model, parameters) that evaluations can run.', doc: 'API.md#objects' },
  prompts: { title: 'Prompts', body: 'Versioned prompts. Each change is kept, so you can compare versions and roll back.', doc: 'API.md#objects' },
  scorers: { title: 'Scorers', body: 'Saved scorers (LLM judges, rules, code) that evaluations and monitors use to score outputs.', doc: 'API.md#more-server-runnable-scorers' },
  functions: { title: 'Functions', body: 'The traced functions (ops) of your code, versioned by their source. Each run of a function is a span in Traces.', doc: 'API.md#objects' },
  objects: { title: 'All objects', body: 'Everything versioned in this project: datasets, models, prompts, scorers, functions and more.', doc: 'API.md#objects' },
  automations: {
    title: 'Automations',
    body: 'Rules that act when something happens, for example posting to a webhook when a score drops or a new alert arrives.',
    doc: 'API.md#automations',
  },
  settings: { title: 'Settings', body: 'Project settings: members and roles, API keys, provider secrets, model prices, data retention and redaction.', doc: 'README.md#run-it' },
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
