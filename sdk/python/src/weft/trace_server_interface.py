"""Deprecated: renamed ``weft.api_types``.

Importing this module emits a ``DeprecationWarning``; it re-exports
``weft.api_types``. It is removed in the next release.
"""

from __future__ import annotations

import warnings

from .api_types import *  # noqa: F401,F403
from .api_types import __all__  # noqa: F401
from .api_types import (  # noqa: F401
    SessionSchema as ThreadSchema,
    SessionsQueryFilter as ThreadsQueryFilter,
    SessionsQueryReq as ThreadsQueryReq,
    SessionsQueryRes as ThreadsQueryRes,
    session_from_item as thread_from_session,
    session_from_item,
    sessions_query_body as threads_query_body,
    sessions_query_body,
)

warnings.warn("weft.trace_server_interface is deprecated; use weft.api_types", DeprecationWarning, stacklevel=2)
