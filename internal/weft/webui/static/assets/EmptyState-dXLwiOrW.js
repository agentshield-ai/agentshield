import{j as e,Y as p,a4 as m,u as g,d as w,a as y,r as f,C as _,a1 as u,c as x}from"./index-B2F7XFtr.js";import{u as v}from"./useMutation-thJD6g6c.js";import{v as b}from"./bits-vBlrIkO8.js";function E(){return typeof window<"u"?window.location.origin:"http://127.0.0.1:6345"}const d={python:"Python",typescript:"TypeScript",otel:"OpenTelemetry","claude-code":"Claude Code",codex:"Codex CLI",openclaw:"OpenClaw",pi:"Pi",agentshield:"AgentShield engine"};function T(s,t,a){const r=a?`
export WEFT_API_KEY=<your API key>`:"";return{python:{lang:"python",note:"Record a session with two agents and a tool call. Install the SDK from sdk/python in the Weft repository.",code:`import weft

weft.init("${t}", server_url="${s}")${a?"  # reads WEFT_API_KEY":""}

with weft.start_conversation(agent_name="orchestrator") as conv:
    with conv.start_turn(user_message="Find three facts about Oslo") as turn:
        with turn.start_llm(model="gpt-4o-mini", provider_name="openai") as llm:
            llm.output("I'll ask the researcher.")
        with turn.start_subagent(name="researcher") as sub:
            with sub.start_tool(name="web_search", arguments={"q": "Oslo"}) as tool:
                tool.result = "Capital of Norway; on the Oslofjord"
        turn.output_messages = [weft.Message.assistant("Oslo is Norway's capital.")]`},typescript:{lang:"ts",note:"The same with the TypeScript SDK (sdk/typescript, package weft-sdk). Node 18+.",code:`import * as weft from 'weft-sdk';

await weft.init('${t}', { serverUrl: '${s}'${a?", apiKey: process.env.WEFT_API_KEY":""} });

const conv = weft.startConversation({ agentName: 'orchestrator' });
await conv.startTurn({ userMessage: 'Find three facts about Oslo' }).run(async (turn) => {
  await turn.startSubagent({ name: 'researcher' }).run(async (sub) => {
    await sub.startTool({ name: 'web_search', arguments: { q: 'Oslo' } }).run((t) => { t.result = 'Capital of Norway'; });
  });
  turn.outputMessages = [weft.Message.assistant("Oslo is Norway's capital.")];
});
conv.end();
await weft.finish();`},otel:{lang:"bash",note:"Any OpenTelemetry exporter works. Spans with gen_ai.* attributes become sessions (gen_ai.conversation.id), agents (gen_ai.agent.name), tool calls and reasoning.",code:`export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="${s}/otel/v1/traces"
export OTEL_EXPORTER_OTLP_TRACES_HEADERS="project_id=${t}${a?",Authorization=Bearer%20<your API key>":""}"
export OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/protobuf

# Put the session id on every span:  gen_ai.conversation.id = "<session id>"
# and the agent on agent spans:        gen_ai.agent.name = "researcher"`},"claude-code":{lang:"bash",note:"Traces every Claude Code session, its sub-agents and tool calls. Run from the Weft repository.",code:`node plugins/weft-claude-code/install.mjs --project ${t} \\
  --server-url ${s}${a?' --api-key "$WEFT_API_KEY"':""}`},codex:{lang:"bash",note:"Adds a Stop hook to Codex that sends each turn to Weft.",code:`node plugins/weft-codex/install.mjs --project ${t} --server-url ${s}${a?' --api-key "$WEFT_API_KEY"':""}`},openclaw:{lang:"bash",note:"Install the gateway plugin, then enable it in ~/.openclaw/openclaw.json with this project and server.",code:`openclaw plugins install ./plugins/weft-openclaw
# ~/.openclaw/openclaw.json:
# plugins: { allow: ["weft"], entries: { weft: { enabled: true,
#   config: { project: "${t}", serverUrl: "${s}" },
#   hooks: { allowConversationAccess: true } } } }`},pi:{lang:"bash",note:"A Pi extension that records each prompt, model call and tool execution.",code:`pi install ./plugins/weft-pi
export WEFT_PROJECT=${t} WEFT_SERVER_URL=${s}${r}
pi`},agentshield:{lang:"yaml",note:"Add this to AgentShield's config.yaml so the engine sends every non-allow verdict to this project. Detections attach to the matching tool call when the plugins pass the same session and tool-call ids.",code:`weft:
  endpoint: ${s}
  project: ${t}
  api_key_env: WEFT_API_KEY      # a key with the member role${a?"":" (not needed: this server has no auth)"}
  send: non_allow                # or: all`}}}function $({projectId:s,only:t,initial:a}){const r=y({queryKey:["server-info"],queryFn:x.serverInfo,staleTime:6e4}),i=T(E(),s,!!r.data?.auth_required),o=t??["python","typescript","otel","claude-code","codex","openclaw","pi"],[n,h]=f.useState(a??o[0]),c=i[n];return e.jsxs("div",{className:"snippets",children:[e.jsx("div",{className:"snippet-tabs",role:"tablist","aria-label":"How to send data",children:o.map(l=>e.jsx("button",{type:"button",role:"tab","aria-selected":l===n,className:`seg-btn ${l===n?"on":""}`,onClick:()=>h(l),children:d[l]},l))}),e.jsx("p",{className:"muted small",children:c.note}),e.jsxs("div",{className:"snippet",children:[e.jsx("div",{className:"snippet-copy",children:e.jsx(_,{text:c.code,label:`Copy ${d[n]} snippet`})}),e.jsx("pre",{className:"code-block","data-lang":c.lang,tabIndex:0,"aria-label":`${d[n]} snippet`,children:e.jsx("code",{children:c.code})})]})]})}function j({projectId:s,label:t="Send a test session",className:a="btn primary"}){const r=g(),i=w(),o=v({mutationFn:()=>b.demoSession(s),onSuccess:async n=>{await i.invalidateQueries(),u("Sent a two-agent test session with reasoning, tool calls, a hand-off and a blocked command.","ok",6e3),r(`/${s}/sessions/${encodeURIComponent(n.session_id)}`)},onError:n=>u(`Could not send a test session: ${n instanceof Error?n.message:String(n)}`,"error",8e3)});return e.jsxs("button",{type:"button",className:a,onClick:()=>o.mutate(),disabled:o.isPending,children:[e.jsx(p,{name:"spark",size:14})," ",o.isPending?"Sending…":t]})}function C({projectId:s,title:t,children:a,doc:r="README.md#send-data",only:i,initial:o,showTest:n=!0}){return e.jsxs("section",{className:"guided-empty","aria-label":"Getting started",children:[e.jsxs("div",{className:"guided-head",children:[e.jsx("div",{className:"guided-icon","aria-hidden":!0,children:e.jsx(p,{name:"spark",size:20})}),e.jsxs("div",{className:"grow",children:[e.jsx("h2",{children:t}),a&&e.jsx("div",{className:"muted",children:a})]})]}),n&&e.jsxs("div",{className:"guided-test",children:[e.jsx(j,{projectId:s}),e.jsxs("span",{className:"muted small",children:["Writes a realistic two-agent session into ",s," so you can explore every view in a few seconds."]})]}),e.jsx("div",{className:"guided-or",children:e.jsx("span",{children:"or connect your agents"})}),e.jsx($,{projectId:s,only:i,initial:o}),e.jsxs("a",{className:"help-link",href:m(r),target:"_blank",rel:"noreferrer",children:["Full setup guide ",e.jsx(p,{name:"external",size:12})]})]})}export{C as G,j as S,$ as a};
