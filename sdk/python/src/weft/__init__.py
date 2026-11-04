"""Weft Python SDK: tracing, versioned objects and evaluations for LLM apps.

>>> import weft
>>> weft.init("my-project")
>>> @weft.trace
... def greet(name: str) -> str:
...     return f"hello {name}"
>>> ref = weft.save(weft.StringPrompt("Hello {name}"), "greeting")
>>> weft.get(ref).format(name="Ada")
'Hello Ada'
"""

from typing import Any as _Any


from ._version import __version__
from . import scorers
from ._call import Span, SpanFeedback
from ._client import (
    SpansIter,
    WeftClient,
    add_tags,
    finish,
    flush,
    get,
    get_aliases,
    get_client,
    get_tags,
    get_tags_and_aliases,
    init,
    link_prompt_to_registry,
    link_to_registry,
    list_aliases,
    list_tags,
    publish,
    ref,
    save,
    remove_aliases,
    remove_tags,
    set_aliases,
    set_run,
)
from ._content import PDF, Audio, Content, Html, Image, Markdown, Video
from ._context import (
    NoCurrentSpanError,
    Thread,
    ThreadPoolExecutor,
    attributes,
    get_current_span,
    require_current_span,
    thread,
    tracing_disabled,
)
from ._custom_runtime import CustomRuntimeID
from . import trace_server_interface, urls
from ._evaluation import Evaluation, EvaluationLogger, ScoreLogger, auto_summarize
from ._http import WeftHTTPError
from ._object import (
    Dataset,
    LLMStructuredCompletionModel,
    MessagesPrompt,
    Model,
    Object,
    Prompt,
    StringPrompt,
    Table,
    WeftObject,
)
from ._call import ApplyScorerResult
from ._loader import OpLoadError
from ._op import BoundOp, Op, trace
from ._platform import AnnotationSpec, Leaderboard, LeaderboardColumn, Monitor, SavedView
from ._refs import FunctionRef, ObjectRef, SpanRef, TableRef, parse_uri
from ._usage import add_usage
from .scorers import Scorer
from . import conversation
from .conversation import (
    LLM,
    Conversation,
    Handoff,
    Team,
    LogResult,
    MediaAttachment,
    Message,
    SubAgent,
    Tool,
    Turn,
    Usage,
    end_conversation,
    end_llm,
    end_turn,
    get_current_conversation,
    get_current_llm,
    get_current_team,
    get_current_turn,
    handoff,
    log_conversation,
    log_turn,
    reasoning,
    start_conversation,
    start_handoff,
    start_llm,
    start_subagent,
    start_tool,
    start_turn,
    team,
)

__all__ = [
    "__version__",
    "init",
    "finish",
    "flush",
    "get_client",
    "trace",
    "Op",
    "BoundOp",
    "Span",
    "SpanFeedback",
    "SpansIter",
    "WeftClient",
    "WeftHTTPError",
    "get_current_span",
    "require_current_span",
    "NoCurrentSpanError",
    "CustomRuntimeID",
    "trace_server_interface",
    "urls",
    "attributes",
    "thread",
    "ThreadPoolExecutor",
    "Thread",
    "save",
    "get",
    "ref",
    "link_to_registry",
    "link_prompt_to_registry",
    "set_run",
    # tags and aliases of object versions
    "add_tags",
    "remove_tags",
    "get_tags",
    "set_aliases",
    "remove_aliases",
    "get_aliases",
    "get_tags_and_aliases",
    "list_tags",
    "list_aliases",
    "Object",
    "Dataset",
    "Table",
    "Model",
    "Prompt",
    "StringPrompt",
    "MessagesPrompt",
    "LLMStructuredCompletionModel",
    "Scorer",
    "scorers",
    "Evaluation",
    "EvaluationLogger",
    "ScoreLogger",
    "auto_summarize",
    "ObjectRef",
    "FunctionRef",
    "TableRef",
    "SpanRef",
    "parse_uri",
    "WeftObject",
    "add_usage",
    "AnnotationSpec",
    "Monitor",
    "Leaderboard",
    "LeaderboardColumn",
    "SavedView",
    "ApplyScorerResult",
    "OpLoadError",
    "Content",
    "Image",
    "Audio",
    "Video",
    "PDF",
    "Html",
    "Markdown",
    "tracing_disabled",
    # agent conversations (start_conversation & co.)
    "conversation",
    "LLM",
    "Conversation",
    "LogResult",
    "MediaAttachment",
    "Message",
    "SubAgent",
    "Tool",
    "Turn",
    "Usage",
    "end_conversation",
    "end_llm",
    "end_turn",
    "get_current_conversation",
    "get_current_llm",
    "get_current_turn",
    "log_conversation",
    "log_turn",
    "start_conversation",
    "start_llm",
    "start_subagent",
    "start_tool",
    "start_turn",
    # agent teams, hand-offs and reasoning
    "Team",
    "team",
    "get_current_team",
    "Handoff",
    "handoff",
    "start_handoff",
    "reasoning",
]


# ---------------------------------------------------------------------------
# Earlier names, kept as deprecated aliases for one release (CHANGELOG.md).
# ---------------------------------------------------------------------------


def op(fn: _Any = None, **kwargs: _Any) -> _Any:
    """Deprecated: use ``@weft.trace``."""
    from ._deprecation import deprecated_call

    deprecated_call("weft.op", "weft.trace")
    return trace(fn, **kwargs)


_DEPRECATED = {
    "Call": ("Span", lambda: Span),
    "CallFeedback": ("SpanFeedback", lambda: SpanFeedback),
    "CallsIter": ("SpansIter", lambda: SpansIter),
    "CallRef": ("SpanRef", lambda: SpanRef),
    "OpRef": ("FunctionRef", lambda: FunctionRef),
    "get_current_call": ("get_current_span", lambda: get_current_span),
    "require_current_call": ("require_current_span", lambda: require_current_span),
    "NoCurrentCallError": ("NoCurrentSpanError", lambda: NoCurrentSpanError),
}


def __getattr__(name: str) -> _Any:
    if name in _DEPRECATED:
        from ._deprecation import deprecated_call

        new, get = _DEPRECATED[name]
        deprecated_call(f"weft.{name}", f"weft.{new}")
        return get()
    raise AttributeError(f"module 'weft' has no attribute {name!r}")
