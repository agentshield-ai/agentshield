import{r as u,a as m,j as e,T as y,C as T,d as x}from"./index-DOkDIJbL.js";function _(){return window.location.origin}function s({code:t,lang:a}){return e.jsxs("div",{className:"snippet",children:[e.jsx("div",{className:"snippet-copy",children:e.jsx(T,{text:t})}),e.jsx("pre",{className:"code-block","data-lang":a,tabIndex:0,children:e.jsx("code",{children:t})})]})}function O({projectId:t,compact:a}){const[n,l]=u.useState("python"),i=m({queryKey:["server-info"],queryFn:x.serverInfo,staleTime:6e4}),r=_(),p=`${r}${i.data?.otel_endpoint??"/otel/v1/traces"}`,o=i.data?.auth_required,c=`# pip install ./sdk/python   (from the Weft repository)
import weft

weft.init("${t}", server_url="${r}")${o?"  # API key from WEFT_API_KEY":""}

@weft.op
def answer(question: str) -> str:
    # OpenAI / Anthropic calls made here are traced too
    return "42"

answer("What is the meaning of life?")`,d=`# Any OpenTelemetry SDK or collector can export here (OTLP/HTTP, protobuf or JSON)
export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="${p}"
export OTEL_EXPORTER_OTLP_TRACES_HEADERS="project_id=${t}${o?",Authorization=Bearer%20<api key>":""}"

# gen_ai.* (OpenTelemetry GenAI) and other common span conventions
# are mapped to inputs, outputs and token usage.`,h=`curl -X POST ${r}/call/start \\
  -H 'Content-Type: application/json'${o?` \\
  -H 'Authorization: Bearer <api key>'`:""} \\
  -d '{"start": {"project_id": "${t}", "op_name": "hello",
       "started_at": "'$(date -u +%Y-%m-%dT%H:%M:%SZ)'", "inputs": {"name": "world"}}}'
# => {"id": "...", "trace_id": "..."}  then POST /call/end with that id`;return e.jsxs("div",{className:"col",style:{gap:10},children:[!a&&e.jsx("p",{className:"muted small",style:{margin:0},children:"Send traces with the Python SDK, or point any OpenTelemetry exporter at this server. Projects are created on first write."}),e.jsx(y,{tabs:[{id:"python",label:"Python SDK"},{id:"otel",label:"OpenTelemetry"},{id:"curl",label:"HTTP"}],value:n,onChange:l}),n==="python"&&e.jsx(s,{code:c,lang:"python"}),n==="otel"&&e.jsx(s,{code:d,lang:"bash"}),n==="curl"&&e.jsx(s,{code:h,lang:"bash"})]})}export{s as C,O as G,_ as s};
