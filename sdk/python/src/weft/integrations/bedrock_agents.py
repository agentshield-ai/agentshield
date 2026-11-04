"""AWS Bedrock Agents: ``invoke_agent`` and ``invoke_inline_agent`` on
``bedrock-agent-runtime`` clients (boto3).

Patched through ``botocore.client.BaseClient._make_api_call`` with the
``bedrock`` integration (``patch_bedrock``), so every client is traced. The
response's ``completion`` event stream is wrapped: the caller iterates it as
usual while each event is turned into calls, live:

* ``bedrock_agent.invoke_agent`` (agent): one per request, a turn of the
  conversation ``sessionId``; output = the streamed answer, citations,
  ``returnControl`` requests and file names. It ends when the stream is
  consumed (or closed).
* ``bedrock_agent.model_invocation`` (llm): each ``modelInvocationInput`` /
  ``modelInvocationOutput`` pair of the orchestration, pre-processing,
  post-processing and routing traces, with the prompt, raw response,
  parsed response and token usage under ``foundationModel``.
* ``bedrock_agent.action_group`` / ``bedrock_agent.code_interpreter`` (tool),
  ``bedrock_agent.knowledge_base`` (retriever): each ``invocationInput`` and
  its ``observation``.
* ``bedrock_agent.collaborator`` (agent): multi-agent collaboration; the
  collaborator's own trace events nest under it.
* ``bedrock_agent.guardrail`` (guardrail): guardrail assessments.
* ``bedrock_agent.return_control`` (tool): functions the agent hands back to
  the application (``returnControl``).

Trace events only arrive with ``enableTrace=True``; without it only the agent
call (answer, citations) is recorded. ``failureTrace`` ends the open steps and
the agent call with an error.
"""

from __future__ import annotations

import json
import logging
import threading
from typing import Any, Dict, List, Optional, Tuple

from .. import _context
from .._usage import add_usage
from . import _settings

logger = logging.getLogger("weft")

INTEGRATION = "bedrock"
AGENT_OPS = {"InvokeAgent": "bedrock_agent.invoke_agent", "InvokeInlineAgent": "bedrock_agent.invoke_inline_agent"}
_STEP_TRACES = ("orchestrationTrace", "preProcessingTrace", "postProcessingTrace", "routingClassifierTrace")
_PROVIDER = "aws.bedrock"


def _maybe_json(v: Any) -> Any:
    if isinstance(v, (bytes, bytearray)):
        v = v.decode("utf-8", "replace")
    if isinstance(v, str):
        s = v.strip()
        if s[:1] in ("{", "["):
            try:
                return json.loads(s)
            except ValueError:
                return v
    return v


def _int(v: Any) -> int:
    try:
        return int(v or 0)
    except (TypeError, ValueError):
        return 0


def _params(ps: Any) -> Dict[str, Any]:
    """Bedrock ``[{name, type, value}]`` parameters -> ``{name: value}``."""
    if not isinstance(ps, list):
        return {}
    return {str(p.get("name")): p.get("value") for p in ps if isinstance(p, dict) and p.get("name") is not None}


def agent_inputs(api_params: Dict[str, Any]) -> Dict[str, Any]:
    inputs = {k: v for k, v in api_params.items() if k not in ("sessionState",)}
    st = api_params.get("sessionState")
    if isinstance(st, dict) and st:
        # Results of returnControl functions are tool outputs from the application.
        inputs["sessionState"] = st
    if isinstance(api_params.get("inputText"), str):
        # Chat-shaped copy for the Agents view (the request fields are kept as sent).
        inputs["messages"] = [{"role": "user", "content": api_params["inputText"]}]
    return inputs


class AgentTraceProcessor:
    """Turns a Bedrock Agents response stream into weft calls."""

    def __init__(self, client: Any, root: Any, *, agent_name: str, session_id: Optional[str], model: Optional[str] = None):
        self.client = client
        self.root = root
        self.agent_name = agent_name
        self.session_id = session_id
        self.model = model
        self.text = ""
        self.citations: List[Any] = []
        self.return_control: List[Dict[str, Any]] = []
        self.files: List[Dict[str, Any]] = []
        self.rationales: List[str] = []
        self.final_response: Optional[str] = None
        self.failure: Optional[str] = None
        self.events = 0
        self.foundation_model: Optional[str] = model
        self.usage = {"input_tokens": 0, "output_tokens": 0}
        self._open: Dict[Tuple[str, str], Any] = {}  # (trace id, "llm"|"tool") -> call
        self._collaborators: Dict[str, Any] = {}  # collaborator name -> call
        self._models: Dict[str, str] = {}  # step trace id -> model
        self._lock = threading.Lock()
        self.done = False

    # ------------------------------------------------------------------ helpers
    def _attrs(self, extra: Dict[str, Any]) -> Dict[str, Any]:
        attrs = _settings.integration_attributes(INTEGRATION)
        attrs["gen_ai.provider.name"] = _PROVIDER
        if self.session_id:
            attrs["gen_ai.conversation.id"] = self.session_id
        for k, v in extra.items():
            if v is not None and v != "":
                attrs[k] = v
        return attrs

    def _start(self, name: str, kind: Optional[str], inputs: Dict[str, Any], parent: Any, display: Optional[str], attrs: Dict[str, Any]) -> Any:
        op = _settings.integration_op(INTEGRATION, name, kind)
        try:
            call = self.client.create_call(op, inputs, parent=parent, display_name=display, attributes=self._attrs(attrs))
        except Exception as e:  # pragma: no cover - never break the stream
            logger.debug("weft: could not start %s: %s", name, e)
            return None
        call._weft_op = op
        return call

    def _end(self, call: Any, output: Any = None, exc: Optional[BaseException] = None) -> None:
        if call is None:
            return
        try:
            self.client.finish_call(call, output=output, exception=exc, op=getattr(call, "_weft_op", None))
        except Exception as e:  # pragma: no cover
            logger.debug("weft: could not end a bedrock agent span: %s", e)

    def _parent(self, outer: Dict[str, Any]) -> Any:
        """The collaborator span a sub-agent's trace belongs to, else the agent call."""
        name = outer.get("collaboratorName")
        chain = outer.get("callerChain") or []
        if name and len(chain) > 1 and name in self._collaborators:
            return self._collaborators[name]
        return self.root

    # ------------------------------------------------------------------ events
    def add(self, ev: Any) -> None:
        if not isinstance(ev, dict):
            return
        with self._lock:
            self.events += 1
            if "chunk" in ev:
                ch = ev.get("chunk") or {}
                b = ch.get("bytes")
                if isinstance(b, (bytes, bytearray)):
                    self.text += b.decode("utf-8", "replace")
                elif isinstance(b, str):
                    self.text += b
                att = ch.get("attribution") or {}
                self.citations.extend(att.get("citations") or [])
            elif "trace" in ev:
                outer = ev.get("trace") or {}
                self._trace(outer, outer.get("trace") or {})
            elif "returnControl" in ev:
                self._return_control(ev.get("returnControl") or {})
            elif "files" in ev:
                for f in (ev.get("files") or {}).get("files") or []:
                    b = f.get("bytes")
                    self.files.append({"name": f.get("name"), "type": f.get("type"), "size": len(b) if isinstance(b, (bytes, bytearray, str)) else None})

    def _trace(self, outer: Dict[str, Any], tr: Dict[str, Any]) -> None:
        parent = self._parent(outer)
        for step in _STEP_TRACES:
            if step in tr:
                self._step(step, tr.get(step) or {}, parent, outer)
        if "guardrailTrace" in tr:
            g = tr.get("guardrailTrace") or {}
            call = self._start("bedrock_agent.guardrail", "guardrail", {"trace_id": g.get("traceId")}, parent, "guardrail", {})
            out = {k: v for k, v in g.items() if k != "traceId"}
            self._end(call, out)
        if "failureTrace" in tr:
            f = tr.get("failureTrace") or {}
            self.failure = str(f.get("failureReason") or "agent failure")
            err = RuntimeError(self.failure)
            tid = f.get("traceId")
            for key in [k for k in self._open if tid and k[0] == tid]:
                self._end(self._open.pop(key), None, err)
        if "customOrchestrationTrace" in tr:
            c = (tr.get("customOrchestrationTrace") or {}).get("event") or {}
            if c.get("text"):
                self.rationales.append(str(c["text"]))

    def _step(self, step: str, t: Dict[str, Any], parent: Any, outer: Dict[str, Any]) -> None:
        stage = {"orchestrationTrace": "orchestration", "preProcessingTrace": "pre_processing", "postProcessingTrace": "post_processing",
                 "routingClassifierTrace": "routing"}[step]
        mi = t.get("modelInvocationInput")
        if isinstance(mi, dict):
            tid = str(mi.get("traceId") or "")
            model = mi.get("foundationModel") or self.foundation_model or self.model
            if mi.get("foundationModel"):
                self.foundation_model = self.foundation_model or mi.get("foundationModel")
            if tid and model:
                self._models[tid] = model
            prompt = _maybe_json(mi.get("text"))
            inputs: Dict[str, Any] = {"model": model, "stage": stage, "type": mi.get("type")}
            if isinstance(prompt, dict) and ("messages" in prompt or "system" in prompt):
                inputs.update({k: prompt[k] for k in ("system", "messages") if k in prompt})
            elif prompt is not None:
                inputs["prompt"] = prompt
            for k in ("inferenceConfiguration", "promptCreationMode", "parserMode", "overrideLambda"):
                if mi.get(k) is not None:
                    inputs[k] = mi[k]
            call = self._start("bedrock_agent.model_invocation", "llm", inputs, parent, f"chat {model}" if model else "chat",
                               {"gen_ai.operation.name": "chat", "gen_ai.request.model": model, "bedrock_agent.stage": stage})
            if call is not None:
                old = self._open.pop((tid, "llm"), None)
                self._end(old, None)
                self._open[(tid, "llm")] = call
        mo = t.get("modelInvocationOutput")
        if isinstance(mo, dict):
            tid = str(mo.get("traceId") or "")
            call = self._open.pop((tid, "llm"), None)
            model = self._models.get(tid) or self.foundation_model or self.model
            if call is None:  # an output without its input event
                call = self._start("bedrock_agent.model_invocation", "llm", {"model": model, "stage": stage}, parent,
                                   f"chat {model}" if model else "chat", {"gen_ai.operation.name": "chat", "gen_ai.request.model": model})
            meta = mo.get("metadata") or {}
            u = meta.get("usage") or {}
            p, c = _int(u.get("inputTokens")), _int(u.get("outputTokens"))
            if call is not None and (p or c):
                add_usage(call, model or f"bedrock-agent:{self.agent_name}", p, c)
            self.usage["input_tokens"] += p
            self.usage["output_tokens"] += c
            raw = (mo.get("rawResponse") or {}).get("content")
            out: Dict[str, Any] = {"content": _maybe_json(raw)}
            if mo.get("parsedResponse") is not None:
                out["parsed_response"] = mo["parsedResponse"]
            if mo.get("reasoningContent") is not None:
                out["reasoning"] = mo["reasoningContent"]
            if meta:
                out["metadata"] = {k: v for k, v in meta.items() if k != "usage"}
            self._end(call, out)
        if isinstance(t.get("rationale"), dict) and t["rationale"].get("text"):
            self.rationales.append(str(t["rationale"]["text"]))
        ii = t.get("invocationInput")
        if isinstance(ii, dict):
            self._invocation(ii, parent)
        ob = t.get("observation")
        if isinstance(ob, dict):
            self._observation(ob)

    def _invocation(self, ii: Dict[str, Any], parent: Any) -> None:
        tid = str(ii.get("traceId") or "")
        typ = ii.get("invocationType")
        call = None
        if "actionGroupInvocationInput" in ii:
            ag = ii.get("actionGroupInvocationInput") or {}
            group = ag.get("actionGroupName") or "action_group"
            fn = ag.get("function") or ag.get("apiPath") or ""
            tool = f"{group}.{fn}" if fn else group
            inputs: Dict[str, Any] = {"action_group": group}
            if ag.get("function"):
                inputs["function"] = ag["function"]
            if ag.get("apiPath"):
                inputs["api_path"], inputs["verb"] = ag["apiPath"], ag.get("verb")
            inputs["parameters"] = _params(ag.get("parameters"))
            if ag.get("requestBody"):
                inputs["request_body"] = ag["requestBody"]
            if ag.get("executionType"):
                inputs["execution_type"] = ag["executionType"]
            call = self._start("bedrock_agent.action_group", "tool", inputs, parent, f"execute_tool {tool}",
                               {"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": tool, "gen_ai.tool.call.id": ag.get("invocationId") or tid})
        elif "knowledgeBaseLookupInput" in ii:
            kb = ii.get("knowledgeBaseLookupInput") or {}
            name = f"knowledge_base {kb.get('knowledgeBaseId') or ''}".strip()
            call = self._start("bedrock_agent.knowledge_base", "retriever", {"knowledge_base_id": kb.get("knowledgeBaseId"), "query": kb.get("text")},
                               parent, name, {"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": name, "gen_ai.tool.call.id": tid})
        elif "codeInterpreterInvocationInput" in ii:
            ci = ii.get("codeInterpreterInvocationInput") or {}
            call = self._start("bedrock_agent.code_interpreter", "tool", {"code": ci.get("code"), "files": ci.get("files")}, parent,
                               "execute_tool code_interpreter", {"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "code_interpreter", "gen_ai.tool.call.id": tid})
        elif "agentCollaboratorInvocationInput" in ii:
            co = ii.get("agentCollaboratorInvocationInput") or {}
            name = co.get("agentCollaboratorName") or "collaborator"
            inp = co.get("input") or {}
            inputs = {"collaborator": name, "alias_arn": co.get("agentCollaboratorAliasArn"), "input": inp.get("text") if isinstance(inp, dict) and "text" in inp else inp}
            call = self._start("bedrock_agent.collaborator", "agent", inputs, parent, f"invoke_agent {name}",
                               {"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": name})
            if call is not None:
                self._collaborators[name] = call
        elif typ and typ != "FINISH":
            call = self._start("bedrock_agent.invocation", "tool", {"invocation_type": typ}, parent, f"execute_tool {str(typ).lower()}",
                               {"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": str(typ).lower()})
        if call is not None:
            old = self._open.pop((tid, "tool"), None)
            self._end(old, None)
            self._open[(tid, "tool")] = call

    def _observation(self, ob: Dict[str, Any]) -> None:
        tid = str(ob.get("traceId") or "")
        call = self._open.pop((tid, "tool"), None)
        out: Any = None
        exc: Optional[BaseException] = None
        if "actionGroupInvocationOutput" in ob:
            ag = ob.get("actionGroupInvocationOutput") or {}
            out = _maybe_json(ag.get("text"))
        elif "knowledgeBaseLookupOutput" in ob:
            refs = (ob.get("knowledgeBaseLookupOutput") or {}).get("retrievedReferences") or []
            out = {"retrieved_references": refs, "count": len(refs)}
        elif "codeInterpreterInvocationOutput" in ob:
            co = ob.get("codeInterpreterInvocationOutput") or {}
            out = {k: co.get(k) for k in ("executionOutput", "executionError", "files", "executionTimeout") if co.get(k) is not None}
            if co.get("executionError"):
                exc = RuntimeError(str(co["executionError"]))
        elif "agentCollaboratorInvocationOutput" in ob:
            co = ob.get("agentCollaboratorInvocationOutput") or {}
            name = co.get("agentCollaboratorName")
            o = co.get("output") or {}
            out = o.get("text") if isinstance(o, dict) and "text" in o else o
            if name and self._collaborators.get(name) is call:
                self._collaborators.pop(name, None)
            elif call is None and name in self._collaborators:
                call = self._collaborators.pop(name)
        if "finalResponse" in ob:
            self.final_response = (ob.get("finalResponse") or {}).get("text")
        if ob.get("repromptResponse"):
            out = {"reprompt": ob["repromptResponse"]} if out is None else out
        if call is not None:
            self._end(call, out, exc)

    def _return_control(self, rc: Dict[str, Any]) -> None:
        entry = {"invocation_id": rc.get("invocationId"), "invocations": []}
        for inv in rc.get("invocationInputs") or []:
            fi = inv.get("functionInvocationInput") or inv.get("apiInvocationInput") or {}
            group = fi.get("actionGroup") or "action_group"
            fn = fi.get("function") or fi.get("apiPath") or ""
            tool = f"{group}.{fn}" if fn else group
            inputs: Dict[str, Any] = {"action_group": group, "parameters": _params(fi.get("parameters"))}
            if fi.get("function"):
                inputs["function"] = fi["function"]
            if fi.get("apiPath"):
                inputs["api_path"], inputs["verb"] = fi["apiPath"], fi.get("httpMethod")
            entry["invocations"].append({"tool": tool, **inputs})
            call = self._start("bedrock_agent.return_control", "tool", inputs, self.root, f"execute_tool {tool}",
                               {"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": tool, "gen_ai.tool.call.id": rc.get("invocationId")})
            self._end(call, {"status": "returned_to_application", "invocation_id": rc.get("invocationId")})
        self.return_control.append(entry)

    # ------------------------------------------------------------------ end
    def output(self, extra: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
        out: Dict[str, Any] = {"completion": self.text}
        answer = self.text or self.final_response
        if answer:
            out["messages"] = [{"role": "assistant", "content": answer}]
        if self.final_response is not None and self.final_response != self.text:
            out["final_response"] = self.final_response
        if self.citations:
            out["citations"] = self.citations
        if self.return_control:
            out["return_control"] = self.return_control
        if self.files:
            out["files"] = self.files
        if self.rationales:
            out["rationales"] = self.rationales
        if self.foundation_model:
            out["foundation_model"] = self.foundation_model
        if self.usage["input_tokens"] or self.usage["output_tokens"]:
            out["usage"] = dict(self.usage)
        out["event_count"] = self.events
        out.update(extra or {})
        return out

    def finish(self, exc: Optional[BaseException] = None, extra: Optional[Dict[str, Any]] = None) -> None:
        with self._lock:
            if self.done:
                return
            self.done = True
            err = exc or (RuntimeError(self.failure) if self.failure else None)
            for call in list(self._open.values()):
                self._end(call, None, err or RuntimeError("the agent response ended before this step finished"))
            self._open.clear()
            for call in list(self._collaborators.values()):
                self._end(call, None, err)
            self._collaborators.clear()
            self._end(self.root, self.output(extra), err)


class AgentStreamProxy:
    """Wraps the ``completion`` event stream; the caller iterates it unchanged."""

    def __init__(self, stream: Any, proc: AgentTraceProcessor, extra: Dict[str, Any]):
        self._stream = stream
        self._proc = proc
        self._extra = extra

    def __iter__(self) -> Any:
        try:
            for ev in self._stream:
                try:
                    self._proc.add(ev)
                except Exception as e:  # pragma: no cover - best effort
                    logger.debug("weft: bedrock agent event handling failed: %s", e)
                yield ev
        except BaseException as e:
            self._proc.finish(e, self._extra)
            raise
        self._proc.finish(None, self._extra)

    def close(self) -> None:
        self._proc.finish(None, self._extra)
        fn = getattr(self._stream, "close", None)
        if callable(fn):
            fn()

    def __getattr__(self, item: str) -> Any:
        return getattr(self._stream, item)


def start_agent_call(client: Any, operation_name: str, api_params: Dict[str, Any]) -> "Tuple[Any, AgentTraceProcessor]":
    """Start the agent call for an InvokeAgent/InvokeInlineAgent request."""
    inline = operation_name == "InvokeInlineAgent"
    agent_id = api_params.get("agentId")
    name = str(api_params.get("agentName") or agent_id or ("inline_agent" if inline else "bedrock_agent"))
    session_id = api_params.get("sessionId")
    model = api_params.get("foundationModel") if inline else None
    op = _settings.integration_op(INTEGRATION, AGENT_OPS[operation_name], "agent")
    attrs = _settings.integration_attributes(INTEGRATION)
    attrs.update({"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": name, "gen_ai.provider.name": _PROVIDER})
    if agent_id:
        attrs["gen_ai.agent.id"] = agent_id
    if api_params.get("agentAliasId"):
        attrs["gen_ai.agent.version"] = api_params["agentAliasId"]
    if session_id:
        attrs["gen_ai.conversation.id"] = session_id
    if model:
        attrs["gen_ai.request.model"] = model
    inputs = agent_inputs(api_params)
    # A Bedrock session is a conversation: thread it unless the caller already did.
    if session_id and _context.current_session_context() is None and not getattr(_context.get_current_call(), "session_id", None):
        with _context.session(str(session_id)):
            call = client.create_call(op, inputs, display_name=f"invoke_agent {name}", attributes=attrs)
    else:
        call = client.create_call(op, inputs, display_name=f"invoke_agent {name}", attributes=attrs)
    call._weft_op = op
    return call, AgentTraceProcessor(client, call, agent_name=name, session_id=session_id, model=model)


def handle_agent_response(proc: AgentTraceProcessor, res: Dict[str, Any]) -> Dict[str, Any]:
    extra = {k: res.get(k) for k in ("sessionId", "memoryId", "contentType") if res.get(k) is not None}
    stream = res.get("completion")
    if stream is None:
        proc.finish(None, extra)
        return res
    res["completion"] = AgentStreamProxy(stream, proc, extra)
    return res
