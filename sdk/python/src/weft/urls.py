"""UI links (Weave's ``weave.trace.urls``), against the Weft server.

``agent_conversation_path(entity, project, conversation_id)`` links to a
conversation in **Agents**; ``redirect_call`` to a call. The base is the
current client's server URL (else ``WEFT_SERVER_URL``, else
``http://localhost:6345``), since the Weft UI is served by the server.
"""

from __future__ import annotations

import os
from urllib.parse import quote

DEFAULT_SERVER_URL = "http://localhost:6345"


def _base_url() -> str:
    from ._client import get_client

    c = get_client()
    if c is not None:
        return c.server_url.rstrip("/")
    return (os.environ.get("WEFT_SERVER_URL") or DEFAULT_SERVER_URL).rstrip("/")


def remote_project_root_url(entity_name: str, project_name: str) -> str:
    return f"{_base_url()}/{quote(entity_name)}/{quote(project_name)}"


def project_weft_root_url(entity_name: str, project_name: str) -> str:
    return remote_project_root_url(entity_name, project_name)


project_weave_root_url = project_weft_root_url


def redirect_call(entity_name: str, project_name: str, call_id: str) -> str:
    return f"{remote_project_root_url(entity_name, project_name)}/calls/{quote(call_id)}"


def agent_conversation_path(entity_name: str, project_name: str, conversation_id: str) -> str:
    """Link to a conversation in the Agents view (``/<entity>/<project>/conversations/<id>``)."""
    return f"{remote_project_root_url(entity_name, project_name)}/conversations/{quote(conversation_id, safe='')}"


def op_version_path(entity_name: str, project_name: str, op_name: str, op_version: str) -> str:
    return f"{remote_project_root_url(entity_name, project_name)}/objects/{quote(op_name, safe='')}/versions/{op_version}"


def object_version_path(entity_name: str, project_name: str, object_name: str, obj_version: str) -> str:
    return f"{remote_project_root_url(entity_name, project_name)}/objects/{quote(object_name, safe='')}/versions/{obj_version}"


def leaderboard_path(entity_name: str, project_name: str, object_name: str) -> str:
    return f"{remote_project_root_url(entity_name, project_name)}/leaderboards/{quote(object_name, safe='')}"


__all__ = [
    "agent_conversation_path",
    "redirect_call",
    "remote_project_root_url",
    "project_weft_root_url",
    "project_weave_root_url",
    "op_version_path",
    "object_version_path",
    "leaderboard_path",
]
