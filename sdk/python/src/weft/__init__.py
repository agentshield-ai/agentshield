"""Weft Python SDK: tracing, versioned objects and evaluations for LLM apps.

>>> import weft
>>> weft.init("my-project")
>>> @weft.op
... def greet(name: str) -> str:
...     return f"hello {name}"
"""

from ._version import __version__
from . import scorers
from ._call import Call, CallFeedback
from ._client import (
    CallsIter,
    WeftClient,
    finish,
    flush,
    get,
    get_client,
    init,
    publish,
    ref,
)
from ._content import PDF, Content, Html, Markdown, Video
from ._context import ThreadPoolExecutor, Thread, attributes, get_current_call, thread, tracing_disabled
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
from ._op import BoundOp, Op, op
from ._platform import AnnotationSpec, Leaderboard, LeaderboardColumn, Monitor, SavedView
from ._refs import CallRef, ObjectRef, OpRef, TableRef, parse_uri
from ._usage import add_usage
from .scorers import Scorer
from . import conversation
from .conversation import (
    LLM,
    Conversation,
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
    get_current_turn,
    log_conversation,
    log_turn,
    start_conversation,
    start_llm,
    start_subagent,
    start_tool,
    start_turn,
)

__all__ = [
    "__version__",
    "init",
    "finish",
    "flush",
    "get_client",
    "op",
    "Op",
    "BoundOp",
    "Call",
    "CallFeedback",
    "CallsIter",
    "WeftClient",
    "WeftHTTPError",
    "get_current_call",
    "attributes",
    "thread",
    "ThreadPoolExecutor",
    "Thread",
    "publish",
    "ref",
    "get",
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
    "OpRef",
    "TableRef",
    "CallRef",
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
    "Video",
    "PDF",
    "Html",
    "Markdown",
    "tracing_disabled",
    # agent conversations (weave.start_conversation & co.)
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
]
