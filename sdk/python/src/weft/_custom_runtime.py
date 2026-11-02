"""``weft.CustomRuntimeID``: the runtime ID type ``WeftClient.register_custom_runtime`` takes
(Weave's ``weave.CustomRuntimeID``)."""

from __future__ import annotations

from pydantic import BaseModel, Field, StringConstraints

try:  # Python 3.9 has typing_extensions via pydantic
    from typing import Annotated
except ImportError:  # pragma: no cover
    from typing_extensions import Annotated

DEFAULT_CUSTOM_RUNTIME_MAX_TOKENS = 4096


class CustomRuntimeID(BaseModel):
    id: Annotated[str, StringConstraints(strip_whitespace=True, min_length=1, pattern=r"^\S+$")] = Field(
        description="Value sent in the OpenAI-compatible request model field"
    )
    max_tokens: int = Field(default=DEFAULT_CUSTOM_RUNTIME_MAX_TOKENS, gt=0, description="Maximum tokens supported by this runtime ID")
