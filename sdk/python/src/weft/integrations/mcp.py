"""Model Context Protocol (``mcp`` and ``fastmcp``) integration.

Client side (``mcp.ClientSession``, which ``mcp.Client`` and ``fastmcp.Client`` use): ``call_tool``
(tool kind, display name ``MCP tool: <name>``), ``read_resource``,
``get_prompt``, and the ``list_*`` methods.

Server side (``mcp.server.fastmcp.FastMCP`` in mcp 1.x, ``mcp.server.mcpserver.MCPServer``
in mcp 2.x, and ``fastmcp.FastMCP``):
``call_tool`` (tool kind), ``read_resource`` and ``get_prompt``; each request a
server handles becomes a call, and ops or LLM calls made by the tool
function nest under it.

Set ``WEFT_MCP_TRACE_LIST_OPERATIONS=false`` to skip the ``list_*`` methods.
"""

from __future__ import annotations

import os
from typing import Any, List

from ._methods import Method, patch_methods

INTEGRATION = "mcp"


def _arg(call: Any, *names: str) -> Any:
    raw = call._raw_inputs or {}
    for n in names:
        if raw.get(n) is not None:
            return raw[n]
    args = raw.get("args")
    if isinstance(args, list) and args:
        return args[0]
    return None


def _tool_display(call: Any) -> str:
    name = _arg(call, "name", "tool_name")
    return f"MCP tool: {name}" if name else "MCP tool"


def _resource_display(call: Any) -> str:
    uri = _arg(call, "uri")
    return f"MCP resource: {uri}" if uri is not None else "MCP resource"


def _prompt_display(call: Any) -> str:
    name = _arg(call, "name")
    return f"MCP prompt: {name}" if name else "MCP prompt"


def _result(res: Any) -> Any:
    """Keep MCP results readable: content blocks, structured content and the error flag."""
    for attr in ("model_dump",):
        fn = getattr(res, attr, None)
        if callable(fn):
            try:
                return fn(exclude_none=True)
            except Exception:
                break
    return res


def _list_ops() -> bool:
    raw = os.environ.get("WEFT_MCP_TRACE_LIST_OPERATIONS", "true").strip().lower()
    return raw not in ("0", "false", "no", "off")


def client_methods(cls: str, prefix: str) -> List[Method]:
    ms = [
        Method(f"{cls}.call_tool", f"{prefix}.call_tool", "tool", display=_tool_display, output=_result),
        Method(f"{cls}.read_resource", f"{prefix}.read_resource", "retriever", display=_resource_display, output=_result),
        Method(f"{cls}.get_prompt", f"{prefix}.get_prompt", None, display=_prompt_display, output=_result),
    ]
    if _list_ops():
        ms += [
            Method(f"{cls}.list_tools", f"{prefix}.list_tools", None, output=_result),
            Method(f"{cls}.list_resources", f"{prefix}.list_resources", None, output=_result),
            Method(f"{cls}.list_prompts", f"{prefix}.list_prompts", None, output=_result),
        ]
    return ms


def server_methods(cls: str, prefix: str) -> List[Method]:
    ms = [
        Method(f"{cls}.call_tool", f"{prefix}.call_tool", "tool", display=_tool_display, output=_result),
        Method(f"{cls}.read_resource", f"{prefix}.read_resource", "retriever", display=_resource_display, output=_result),
        Method(f"{cls}.get_prompt", f"{prefix}.get_prompt", None, display=_prompt_display, output=_result),
    ]
    if _list_ops():
        ms += [
            Method(f"{cls}.list_tools", f"{prefix}.list_tools", None, output=_result),
            Method(f"{cls}.list_resources", f"{prefix}.list_resources", None, output=_result),
        ]
    return ms


def patch_mcp() -> bool:
    """Patch whichever of ``mcp`` 1.x/2.x (client session and server) and ``fastmcp`` 2.x are installed.

    Clients (``mcp.Client``, ``fastmcp.Client``) delegate to
    ``mcp.ClientSession``, which is the one patched, so each request is one call.
    """
    a = patch_methods(INTEGRATION, "mcp.client.session", client_methods("ClientSession", "mcp.ClientSession"))
    b = patch_methods(INTEGRATION, "mcp.server.fastmcp", server_methods("FastMCP", "mcp.FastMCP"))  # mcp 1.x
    c = patch_methods(INTEGRATION, "mcp.server.mcpserver", server_methods("MCPServer", "mcp.MCPServer"))  # mcp 2.x
    # fastmcp 2.x servers dispatch requests through FastMCP._mcp_call_tool & co.
    d = patch_methods(INTEGRATION, "fastmcp", [
        Method("FastMCP._mcp_call_tool", "fastmcp.FastMCP.call_tool", "tool", display=_tool_display, output=_result),
        Method("FastMCP._mcp_read_resource", "fastmcp.FastMCP.read_resource", "retriever", display=_resource_display, output=_result),
        Method("FastMCP._mcp_get_prompt", "fastmcp.FastMCP.get_prompt", None, display=_prompt_display, output=_result),
    ])
    return a or b or c or d
