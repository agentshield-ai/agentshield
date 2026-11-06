import{a as E,r as m,j as e,C as _,I as h,f as T,u as g,d as O,Y as u,b as y}from"./index-Yxl_BvWk.js";import{u as w}from"./useMutation-EikM-L8z.js";import{v as x}from"./v1-CvjkymhU.js";import{G as P}from"./PageHeader-CDa4cHMc.js";function f(){return typeof window<"u"?window.location.origin:"http://127.0.0.1:6345"}const p={python:"Python",typescript:"TypeScript",otel:"OpenTelemetry","claude-code":"Claude Code plugin","claude-code-otel":"Claude Code (built-in telemetry)",codex:"Codex CLI",openclaw:"OpenClaw",pi:"Pi",agentshield:"AgentShield engine","opencode-otel":"OpenCode (built-in telemetry)","copilot-otel":"Copilot CLI (built-in telemetry)","codex-otel":"Codex (built-in telemetry)","gemini-otel":"Gemini CLI (built-in telemetry)"};function L(t,o,s){const r=s?`
export WEFT_API_KEY=<your API key>`:"";return{python:{lang:"python",note:"Record a session with two agents and a tool call. Install the SDK from sdk/python in the Weft repository.",code:`import weft

weft.init("${o}", server_url="${t}")${s?"  # reads WEFT_API_KEY":""}

with weft.start_conversation(agent_name="orchestrator") as conv:
    with conv.start_turn(user_message="Find three facts about Oslo") as turn:
        with turn.start_llm(model="gpt-4o-mini", provider_name="openai") as llm:
            llm.output("I'll ask the researcher.")
        with turn.start_subagent(name="researcher") as sub:
            with sub.start_tool(name="web_search", arguments={"q": "Oslo"}) as tool:
                tool.result = "Capital of Norway; on the Oslofjord"
        turn.output_messages = [weft.Message.assistant("Oslo is Norway's capital.")]`},typescript:{lang:"ts",note:"The same with the TypeScript SDK (sdk/typescript, package weft-sdk). Node 18+.",code:`import * as weft from 'weft-sdk';

await weft.init('${o}', { serverUrl: '${t}'${s?", apiKey: process.env.WEFT_API_KEY":""} });

const conv = weft.startConversation({ agentName: 'orchestrator' });
await conv.startTurn({ userMessage: 'Find three facts about Oslo' }).run(async (turn) => {
  await turn.startSubagent({ name: 'researcher' }).run(async (sub) => {
    await sub.startTool({ name: 'web_search', arguments: { q: 'Oslo' } }).run((t) => { t.result = 'Capital of Norway'; });
  });
  turn.outputMessages = [weft.Message.assistant("Oslo is Norway's capital.")];
});
conv.end();
await weft.finish();`},otel:{lang:"bash",note:"Any OpenTelemetry exporter works: spans that follow the GenAI conventions become sessions, agents, tool calls and reasoning. The two attributes to set are in the comments below.",code:`export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="${t}/otel/v1/traces"
export OTEL_EXPORTER_OTLP_TRACES_HEADERS="project_id=${o}${s?",Authorization=Bearer%20<your API key>":""}"
export OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/protobuf

# Put the session id on every span:  gen_ai.conversation.id = "<session id>"
# and the agent on agent spans:        gen_ai.agent.name = "researcher"`},"claude-code":{lang:"bash",note:"Traces every Claude Code session, its sub-agents and tool calls. Run from the Weft repository.",code:`node plugins/weft-claude-code/install.mjs --project ${o} \\
  --server-url ${t}${s?' --api-key "$WEFT_API_KEY"':""}`},"claude-code-otel":{lang:"bash",note:"Nothing to install: set these where Claude Code starts and each session arrives while it runs. Unlike the plugin, it sends no reasoning and only the sizes of tool results.",code:`export CLAUDE_CODE_ENABLE_TELEMETRY=1
export OTEL_LOGS_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_ENDPOINT="${t}/otel"
export OTEL_EXPORTER_OTLP_HEADERS="project_id=${o}${s?",Authorization=Bearer%20<your API key>":""}"

# Optional: your prompts, Claude's replies and tool arguments
export OTEL_LOG_USER_PROMPTS=1 OTEL_LOG_ASSISTANT_RESPONSES=1 OTEL_LOG_TOOL_DETAILS=1`},codex:{lang:"bash",note:"Adds a Stop hook to Codex that sends each turn to Weft.",code:`node plugins/weft-codex/install.mjs --project ${o} --server-url ${t}${s?' --api-key "$WEFT_API_KEY"':""}`},openclaw:{lang:"bash",note:"Install the gateway plugin, then enable it in ~/.openclaw/openclaw.json with this project and server.",code:`openclaw plugins install ./plugins/weft-openclaw
# ~/.openclaw/openclaw.json:
# plugins: { allow: ["weft"], entries: { weft: { enabled: true,
#   config: { project: "${o}", serverUrl: "${t}" },
#   hooks: { allowConversationAccess: true } } } }`},pi:{lang:"bash",note:"A Pi extension that records each prompt, model call and tool execution.",code:`pi install ./plugins/weft-pi
export WEFT_PROJECT=${o} WEFT_SERVER_URL=${t}${r}
pi`},"opencode-otel":{lang:"bash",note:"Nothing to install: OpenCode exports its own traces. Each prompt becomes a turn with its model calls (prompts and replies included) and tool calls. A one-shot opencode run can exit before its last spans are sent; the TUI and opencode serve send everything.",code:`# opencode.json (project or ~/.config/opencode/): turns on the model and tool spans
#   { "experimental": { "openTelemetry": true } }
export OTEL_EXPORTER_OTLP_ENDPOINT="${t}/otel"
export OTEL_EXPORTER_OTLP_HEADERS="project_id=${o}${s?",Authorization=Bearer <your API key>":""}"
opencode`},"copilot-otel":{lang:"bash",note:"Written from GitHub's documentation of Copilot CLI's OpenTelemetry export; not yet tested against a real run. Prompts, replies and tool arguments are sent only with content capture on.",code:`export COPILOT_OTEL_ENABLED=true
export OTEL_EXPORTER_OTLP_ENDPOINT="${t}/otel"
export OTEL_EXPORTER_OTLP_PROTOCOL=http/json
export OTEL_EXPORTER_OTLP_HEADERS="project_id=${o}${s?",Authorization=Bearer%20<your API key>":""}"

# Optional: prompts, replies, tool arguments and results
export OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true
copilot`},"codex-otel":{lang:"toml",note:"Nothing to install: add this to ~/.codex/config.toml and each Codex session arrives while it runs, with its model calls, tool calls (arguments and output) and sub-agents. Codex does not export its replies or reasoning; the Codex plugin does.",code:`[otel]
log_user_prompt = true   # your prompts
exporter = { otlp-http = { endpoint = "${t}/otel/v1/logs", protocol = "binary", headers = { "project_id" = "${o}"${s?', "Authorization" = "Bearer <your API key>"':""} } } }`},"gemini-otel":{lang:"bash",note:"Nothing to install: set these where Gemini CLI starts and each session arrives while it runs, with its model calls, replies, tool calls and sub-agents. The protocol must be http: Weft has no gRPC receiver.",code:`export GEMINI_TELEMETRY_ENABLED=true
export GEMINI_TELEMETRY_TARGET=local
export GEMINI_TELEMETRY_OTLP_ENDPOINT="${t}/otel"
export GEMINI_TELEMETRY_OTLP_PROTOCOL=http
export GEMINI_TELEMETRY_OTLP_HEADERS="project_id=${o}${s?",Authorization=Bearer <your API key>":""}"
gemini`},agentshield:{lang:"yaml",note:"Add this to AgentShield's config.yaml so the engine sends every non-allow verdict to this project. Detections attach to the matching tool call when the plugins pass the same session and tool-call ids.",code:`weft:
  endpoint: ${t}
  project: ${o}
  api_key_env: WEFT_API_KEY      # a key with the member role${s?"":" (not needed: this server has no auth)"}
  send: non_allow                # or: all`}}}function b({projectId:t,only:o,initial:s}){const r=E({queryKey:["server-info"],queryFn:y.server,staleTime:6e4}),l=L(f(),t,!!r.data?.auth_required),a=o??["python","typescript","otel","claude-code","claude-code-otel","codex","openclaw","pi","opencode-otel","copilot-otel","codex-otel","gemini-otel"],[n,d]=m.useState(s??a[0]),c=l[n];return e.jsxs("div",{className:"snippets",children:[e.jsx("div",{className:"snippet-tabs",role:"tablist","aria-label":"How to send data",children:a.map(i=>e.jsx("button",{type:"button",role:"tab","aria-selected":i===n,className:`seg-btn ${i===n?"on":""}`,onClick:()=>d(i),children:p[i]},i))}),e.jsx("p",{className:"muted small",children:c.note}),e.jsxs("div",{className:"snippet",children:[e.jsx("div",{className:"snippet-copy",children:e.jsx(_,{text:c.code,label:`Copy ${p[n]} snippet`})}),e.jsx("pre",{className:"code-block","data-lang":c.lang,tabIndex:0,"aria-label":`${p[n]} snippet`,children:e.jsx("code",{children:c.code})})]})]})}function R({projectId:t,label:o="Send a test session",className:s="qbtn primary"}){const r=g(),l=O(),a=w({mutationFn:()=>x.demoSession(t),onSuccess:async n=>{await l.invalidateQueries(),u("Sent a two-agent test session with reasoning, tool calls, a hand-off and a blocked command.","ok",6e3),r(`/${t}/sessions/${encodeURIComponent(n.session_id)}`)},onError:n=>u(`Could not send a test session: ${n instanceof Error?n.message:String(n)}`,"error",8e3)});return e.jsxs("button",{type:"button",className:s,onClick:()=>a.mutate(),disabled:a.isPending,children:[e.jsx(h,{name:"spark",size:14})," ",a.isPending?"Sending…":o]})}function $({projectId:t,title:o,children:s,doc:r="setup.md#send-your-agents-data",only:l,initial:a,showTest:n=!0,setup:d}){return e.jsxs("section",{className:"guided-empty","aria-label":"Getting started",children:[e.jsxs("div",{className:"guided-head",children:[e.jsx("div",{className:"guided-icon","aria-hidden":!0,children:e.jsx(h,{name:"spark",size:20})}),e.jsxs("div",{className:"grow",children:[e.jsx("h2",{children:o}),s&&e.jsx("div",{className:"muted",children:s})]})]}),n&&e.jsxs("div",{className:"guided-test",children:[e.jsx(R,{projectId:t}),e.jsxs("span",{className:"muted small",children:["Writes a realistic two-agent session into ",t," so you can explore every view in a few seconds."]})]}),d??e.jsxs(e.Fragment,{children:[e.jsx("div",{className:"guided-or",children:e.jsx("span",{children:"or connect your agents"})}),e.jsx(b,{projectId:t,only:l,initial:a})]}),e.jsx(T,{className:"help-link",to:P(r),children:"Full setup guide"})]})}export{$ as G,b as S,R as a,f as s};
